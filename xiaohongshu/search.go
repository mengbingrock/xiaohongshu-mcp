package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	"github.com/xpzouying/xiaohongshu-mcp/site"
)

type SearchResult struct {
	Search struct {
		Feeds FeedsValue `json:"feeds"`
	} `json:"search"`
}

// FilterOption 筛选选项结构体
type FilterOption struct {
	SortBy      string `json:"sort_by,omitempty" jsonschema:"排序依据: 综合|最新|最多点赞|最多评论|最多收藏,默认为'综合'"`
	NoteType    string `json:"note_type,omitempty" jsonschema:"笔记类型: 不限|视频|图文,默认为'不限'"`
	PublishTime string `json:"publish_time,omitempty" jsonschema:"发布时间: 不限|一天内|一周内|半年内,默认为'不限'"`
	SearchScope string `json:"search_scope,omitempty" jsonschema:"搜索范围: 不限|已看过|未看过|已关注,默认为'不限'"`
	Location    string `json:"location,omitempty" jsonschema:"位置距离: 不限|同城|附近,默认为'不限'"`
}

// filterGroup 面板上的一个筛选组：标签是什么、对应入参的哪个字段、允许哪些取值。
//
// 组和选项一律按文本定位，不用序号。面板里同一个选项可能渲染成多个 div.tags
// （数量随视口而变），首项是否重复各组也不一致，下标对不齐。
type filterGroup struct {
	label        string                    // 面板上这一组的标签文本
	defaultValue string                    // 页面初始选中的值，不需要重复点击
	pick         func(FilterOption) string // 从入参里取这一组的值
	allowed      []string                  // 合法取值；在打开页面之前就能挡掉写错的值
}

var filterGroups = []filterGroup{
	{"排序依据", "综合", func(f FilterOption) string { return f.SortBy },
		[]string{"综合", "最新", "最多点赞", "最多评论", "最多收藏"}},
	{"笔记类型", "不限", func(f FilterOption) string { return f.NoteType },
		[]string{"不限", "视频", "图文"}},
	{"发布时间", "不限", func(f FilterOption) string { return f.PublishTime },
		[]string{"不限", "一天内", "一周内", "半年内"}},
	{"搜索范围", "不限", func(f FilterOption) string { return f.SearchScope },
		[]string{"不限", "已看过", "未看过", "已关注"}},
	{"位置距离", "不限", func(f FilterOption) string { return f.Location },
		[]string{"不限", "同城", "附近"}},
}

// pendingFilter 一个待应用的筛选项。
type pendingFilter struct {
	group  string // 组标签
	option string // 选项文本
}

// collectFilters 把入参整理成待应用的筛选项，同一组以最后一个非空值为准。
//
// 校验放在这里是为了在打开浏览器之前就挡掉写错的值——否则要等导航、悬停、
// 在面板里找不到之后才能报错，等于为了说一句"你写错了"先向平台发一次请求。
func collectFilters(filters []FilterOption) ([]pendingFilter, error) {
	selected := make(map[string]string, len(filterGroups))

	for _, f := range filters {
		for _, g := range filterGroups {
			value := g.pick(f)
			if value == "" {
				continue
			}
			if !slices.Contains(g.allowed, value) {
				return nil, fmt.Errorf("%s 不支持 %q，可选：%s",
					g.label, value, strings.Join(g.allowed, "、"))
			}
			selected[g.label] = value
		}
	}

	var pending []pendingFilter
	for _, g := range filterGroups {
		value, ok := selected[g.label]
		if !ok || value == g.defaultValue {
			continue
		}
		pending = append(pending, pendingFilter{group: g.label, option: value})
	}

	return pending, nil
}

type SearchAction struct {
	page *rod.Page
}

// 搜索的等待上限。结果到了就提前返回，上限只在页面慢的时候起作用。
// 从美西服务器打开海外站 rednote，首屏结果要 30–37 秒才出来（2026-10-11 实测），
// 原先的「首屏 20 秒、整体 60 秒」会让带筛选的搜索稳定超时；整体上限要低于 marketing-agent 客户端的 180 秒。
// Modified for ycl-2004/xiaohongshu-mcp; see NOTICE.
const (
	searchTimeout        = 150 * time.Second // 一次搜索（导航、首屏、筛选、读结果）的总上限
	searchReadyTimeout   = 50 * time.Second  // 等首屏结果
	filterRefreshTimeout = 20 * time.Second  // 每点一个筛选项后等网页为它重新搜索
	filterSettleTimeout  = 5 * time.Second   // 搜索回包到了之后，等新结果换上（结果可能和原来一样，到点就算）
)

// SearchResults 一次搜索的结果。Unapplied 是点了、但网页没有为它重新搜索的筛选项（如「位置距离:同城」），
// 这时结果没按它筛过：调用方要据此提示，不能当成筛过的数据用。
// Added in ycl-2004/xiaohongshu-mcp; see NOTICE.
type SearchResults struct {
	Feeds     []Feed
	Unapplied []string
}

func NewSearchAction(page *rod.Page) *SearchAction {
	pp := page.Timeout(searchTimeout)

	return &SearchAction{page: pp}
}

func (s *SearchAction) Search(ctx context.Context, keyword string, filters ...FilterOption) (*SearchResults, error) {
	// 先校验筛选取值，必须在导航之前——写错的值不该先向平台发一次请求再报错。
	pending, err := collectFilters(filters)
	if err != nil {
		return nil, err
	}

	// 注意 .Context(ctx) 会替换掉 NewSearchAction 里设的 deadline，必须在其后重新 Timeout，
	// 否则页面等待没有 deadline 可依赖，异常时会永久挂起。
	page := s.page.Context(ctx).Timeout(searchTimeout)

	searchURL := makeSearchURL(keyword)
	page.MustNavigate(searchURL)
	page.MustWaitLoad()
	if waitSearchReady(page, searchReadyTimeout) == searchEmpty {
		return &SearchResults{Feeds: []Feed{}}, nil // 页面明确显示没有结果：筛选面板也不会出现，不必再等
	}
	humanize.Delay(ctx, humanize.AfterNavigate)

	var unapplied []string
	if len(pending) > 0 {
		requests := observeSearchRequests(page)
		defer requests.close()

		bounds, err := openFilterPanel(ctx, page)
		if err != nil {
			return nil, err
		}

		// 一项一项点，每项都等网页为它重新搜索、结果换上之后再点下一项。
		// 原先连着点完只等一次「结果换了一批」：第一项的结果一到就返回，后面几项可能还没生效。
		// 用 ClickNoWait：筛选面板是 hover 浮层，rod 的 WaitInteractable 会误判被遮挡而死等。
		for _, pf := range pending {
			if !filterPanelOpen(page) { // 等结果的这几秒里浮层可能收起
				if bounds, err = openFilterPanel(ctx, page); err != nil {
					return nil, err
				}
			}
			option, err := findFilterOption(page, pf)
			if err != nil {
				return nil, err
			}
			// 每轮重取：点完一项后面板可能重排。
			if b, err := panelBounds(page); err == nil {
				bounds = b
			}
			humanize.Delay(ctx, humanize.BeforeClick)
			beforeIDs, beforeDone := readFeedIDs(page), requests.finished.Load()
			if err := humanize.ClickNoWaitInside(option, bounds); err != nil {
				return nil, fmt.Errorf("点击筛选选项「%s」失败: %w", pf.option, err)
			}
			if !waitFilterApplied(page, requests, beforeIDs, beforeDone) {
				logrus.Warnf("筛选「%s:%s」点了，网页 %s 内没有重新搜索：结果没按它筛", pf.group, pf.option, filterRefreshTimeout)
				unapplied = append(unapplied, pf.group+":"+pf.option)
			}
		}
		requests.logIfUnmatched()
	}

	result := page.MustEval(`() => {
		if (window.__INITIAL_STATE__ &&
		    window.__INITIAL_STATE__.search &&
		    window.__INITIAL_STATE__.search.feeds) {
			const feeds = window.__INITIAL_STATE__.search.feeds;
			const feedsData = feeds.value !== undefined ? feeds.value : feeds._value;
			if (feedsData) {
				return JSON.stringify(feedsData);
			}
		}
		return "";
	}`).String()

	if result == "" {
		return nil, errors.ErrNoFeeds
	}

	var feeds []Feed
	if err := json.Unmarshal([]byte(result), &feeds); err != nil {
		return nil, fmt.Errorf("failed to unmarshal feeds: %w", err)
	}

	return &SearchResults{Feeds: onlyNotes(feeds), Unapplied: unapplied}, nil
}

// feedIDsJS 读当前结果集的 id 列表，用来判断数据有没有换一批。
const feedIDsJS = `() => {
	const f = window.__INITIAL_STATE__?.search?.feeds;
	const v = f ? (f.value !== undefined ? f.value : f._value) : null;
	return v ? v.map(x => x.id).join(",") : "";
}`

func readFeedIDs(page *rod.Page) string {
	res, err := page.Eval(feedIDsJS)
	if err != nil {
		return ""
	}
	return res.Value.Str()
}

const (
	searchPending = ""
	searchEmpty   = "empty"
)

// searchStateJS 读首屏搜索结果的状态："ready" 有结果、"empty" 明确无结果、"" 没到位。
const searchStateJS = `() => {
	const f = window.__INITIAL_STATE__?.search?.feeds;
	const v = f ? (f.value !== undefined ? f.value : f._value) : null;
	if (Array.isArray(v) && v.length > 0) return "ready";
	const text = document.body?.innerText || "";
	return text.includes("暂无相关笔记") || text.includes("没有找到相关结果") ? "empty" : "";
}`

// waitSearchReady 等首屏搜索结果注水进 __INITIAL_STATE__.search.feeds，或页面明确显示无结果。
//
// Modified for ycl-2004/xiaohongshu-mcp，移植自上游 PR #835、#839（修 #813）：
// 上游导航后用 MustWaitStable，搜索页有信息流和图片懒加载、后台请求不断，等不到静止，
// 60s deadline 整个耗在这一步；再接的 __INITIAL_STATE__ !== undefined 也不是就绪信号。
//
// 超时不报错：判定交给后面统一的 ErrNoFeeds，与 feeds.go 等注水的做法一致。
func waitSearchReady(page *rod.Page, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if res, err := page.Eval(searchStateJS); err == nil && res.Value.Str() != searchPending {
			return res.Value.Str()
		}
		time.Sleep(300 * time.Millisecond)
	}
	logrus.Warnf("等待搜索结果加载超时（%s）", timeout)
	return searchPending
}

// waitFilterApplied 点了一个筛选项之后，等网页为它重新搜索：结果换了一批，或者搜索请求的回包到了
// （再等一会儿让新结果换上；新结果可能和原来一样，到点就算）。都没等到就是这一项没生效，
// 例如服务器上的浏览器没有位置，点「同城」网页不会重新搜索（2026-10-12 实测）。
//
// 不能点完立刻读结果：站点是先把 feeds 清空、再灌入新数据，中间读到的要么是空，要么还是上一批。
// Modified for ycl-2004/xiaohongshu-mcp; see NOTICE.
func waitFilterApplied(page *rod.Page, requests *searchRequests, beforeIDs string, beforeDone int64) bool {
	deadline := time.Now().Add(filterRefreshTimeout)
	for time.Now().Before(deadline) {
		if feedsReplaced(page, beforeIDs) {
			return true
		}
		if requests.finished.Load() > beforeDone {
			settle := time.Now().Add(filterSettleTimeout)
			for time.Now().Before(settle) && !feedsReplaced(page, beforeIDs) {
				time.Sleep(300 * time.Millisecond)
			}
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

func feedsReplaced(page *rod.Page, before string) bool {
	now := readFeedIDs(page)
	return now != "" && now != before
}

// searchNotesPath 网页搜索笔记的接口路径（POST …/api/sns/web/v1/search/notes）。
const searchNotesPath = "/search/notes"

// searchRequests 数筛选阶段网页发出、并已收到回包的搜索请求，并把请求里的筛选字段记进日志，
// 核对平台实际收到了什么。认请求的路径要是对不上（平台改版），只剩「结果换了一批」这一个信号，
// logIfUnmatched 会把看到的搜索类请求路径记下来。
// Added in ycl-2004/xiaohongshu-mcp; see NOTICE.
type searchRequests struct {
	cancel   context.CancelFunc
	done     chan struct{}
	finished atomic.Int64

	mu   sync.Mutex
	seen []string // 含 search 的 POST 路径（没认出来时排查用）
}

func observeSearchRequests(page *rod.Page) *searchRequests {
	ctx, cancel := context.WithCancel(page.GetContext())
	p := page.Context(ctx)
	o := &searchRequests{cancel: cancel, done: make(chan struct{})}
	if err := (proto.NetworkEnable{}).Call(p); err != nil {
		logrus.Warnf("监听搜索请求失败，只按结果有没有换一批判断筛选是否生效: %v", err)
		cancel()
		close(o.done)
		return o
	}
	pending := map[proto.NetworkRequestID]bool{}
	wait := p.EachEvent(
		func(e *proto.NetworkRequestWillBeSent) {
			if e.Request.Method != "POST" {
				return
			}
			u, err := url.Parse(e.Request.URL)
			if err != nil || !strings.Contains(u.Path, "search") {
				return
			}
			if !strings.HasSuffix(u.Path, searchNotesPath) {
				o.mu.Lock()
				o.seen = append(o.seen, u.Path)
				o.mu.Unlock()
				return
			}
			pending[e.RequestID] = true
			body := e.Request.PostData
			if body == "" {
				if r, err := (proto.NetworkGetRequestPostData{RequestID: e.RequestID}).Call(p); err == nil {
					body = r.PostData
				}
			}
			logrus.Infof("搜索请求的筛选：%s", searchRequestFilters(body))
		},
		func(e *proto.NetworkLoadingFinished) {
			if pending[e.RequestID] {
				delete(pending, e.RequestID)
				o.finished.Add(1)
			}
		},
		func(e *proto.NetworkLoadingFailed) {
			delete(pending, e.RequestID)
		},
	)
	go func() { defer close(o.done); wait() }()
	return o
}

func (o *searchRequests) close() { o.cancel(); <-o.done }

func (o *searchRequests) logIfUnmatched() {
	if o.finished.Load() > 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	logrus.Warnf("筛选阶段没有认出搜索请求（找的是 …%s），看到的搜索类 POST：%v", searchNotesPath, o.seen)
}

// searchRequestFilters 从搜索请求体里只摘出和筛选有关的字段（不记关键词）。
func searchRequestFilters(body string) string {
	var req map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return fmt.Sprintf("（请求体不是 JSON，%d 字节）", len(body))
	}
	picked := map[string]json.RawMessage{}
	for _, key := range []string{"sort", "note_type", "filters", "geo"} {
		if v, ok := req[key]; ok {
			picked[key] = v
		}
	}
	out, err := json.Marshal(picked)
	if err != nil {
		return "（筛选字段读不出来）"
	}
	return string(out)
}

// openFilterPanel 悬停在筛选按钮上展开面板，再把指针移进面板，之后的移动都限制在面板内。
// 从按钮直接拉到选项的话，路径会有一段既不在按钮上、也还没进面板。
func openFilterPanel(ctx context.Context, page *rod.Page) (humanize.Rect, error) {
	filterButton := page.MustElement(`div.filter`)
	if err := humanize.Hover(filterButton); err != nil {
		return humanize.Rect{}, fmt.Errorf("悬停筛选按钮失败: %w", err)
	}
	humanize.Delay(ctx, humanize.BeforeClick)
	page.MustWait(`() => document.querySelector('div.filter-panel') !== null`)
	bounds, err := panelBounds(page)
	if err != nil {
		return humanize.Rect{}, err
	}
	if err := humanize.MoveInto(page, bounds); err != nil {
		return humanize.Rect{}, fmt.Errorf("移入筛选面板失败: %w", err)
	}
	return bounds, nil
}

// filterPanelOpen 筛选浮层还开着：在页面上、看得见、占着位置。
func filterPanelOpen(page *rod.Page) bool {
	has, panel, err := page.Has("div.filter-panel")
	if err != nil || !has || !humanize.Visible(panel) {
		return false
	}
	_, err = panelBounds(page)
	return err == nil
}

// panelBounds 取筛选面板当前占据的矩形。
func panelBounds(page *rod.Page) (humanize.Rect, error) {
	panel, err := page.Element("div.filter-panel")
	if err != nil {
		return humanize.Rect{}, fmt.Errorf("读取筛选面板失败: %w", err)
	}
	shape, err := panel.Shape()
	if err != nil {
		return humanize.Rect{}, fmt.Errorf("读取筛选面板失败: %w", err)
	}
	if len(shape.Quads) == 0 {
		return humanize.Rect{}, fmt.Errorf("筛选面板没有可用区域")
	}
	q := shape.Quads[0]
	return humanize.Rect{Left: q[0], Top: q[1], Right: q[4], Bottom: q[5]}, nil
}

// findFilterOption 在筛选面板里定位一个选项：按标签找到组，再在组内按文本找选项。
//
// 全程不用序号。同一个选项在面板里可能渲染成多个 div.tags（数量随视口而变，
// 且首项是否重复各组不一致），下标对不齐；早前用 div.tags:nth-child(N) 会选错项。
// 同一个文本可能对应多个节点，只取其中可见的那个。
//
// 作用域必须限定在 div.filter-panel 内且只认 div.tags：页面别处存在同文本的
// 可见元素（顶部频道栏的「图文」「视频」、标签「综合」），放宽会点错地方。
func findFilterOption(page *rod.Page, pf pendingFilter) (*rod.Element, error) {
	groups, err := page.Elements("div.filter-panel div.filters")
	if err != nil {
		return nil, fmt.Errorf("读取筛选面板失败: %w", err)
	}

	for _, group := range groups {
		// 组标签是 div.filters 下的直接子 span
		label, err := group.Element(":scope > span")
		if err != nil {
			continue
		}
		text, err := label.Text()
		if err != nil || strings.TrimSpace(text) != pf.group {
			continue
		}

		options, err := group.Elements("div.tags")
		if err != nil {
			return nil, fmt.Errorf("读取「%s」的选项失败: %w", pf.group, err)
		}

		var available []string
		matched := false
		for _, opt := range options {
			t, err := opt.Text()
			if err != nil {
				continue
			}
			t = strings.TrimSpace(t)
			available = append(available, t)
			if t != pf.option {
				continue
			}

			matched = true
			if humanize.Visible(opt) {
				return opt, nil
			}
		}
		if matched {
			return nil, fmt.Errorf("「%s」里的选项「%s」当前不可见", pf.group, pf.option)
		}
		return nil, fmt.Errorf("「%s」里没有选项「%s」，页面上是：%s",
			pf.group, pf.option, strings.Join(available, "、"))
	}

	return nil, fmt.Errorf("筛选面板里没有「%s」这一组", pf.group)
}

func makeSearchURL(keyword string) string {

	values := url.Values{}
	values.Set("keyword", keyword)
	values.Set("source", "web_explore_feed")

	//https://www.xiaohongshu.com/search_result?keyword=%25E7%258E%258B%25E5%25AD%2590&source=web_search_result_notes
	//https://www.xiaohongshu.com/search_result?keyword=%25E7%258E%258B%25E5%25AD%2590&source=web_explore_feed
	return site.Current().WebURL("/search_result?" + values.Encode())
}

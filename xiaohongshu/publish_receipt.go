// Added in ycl-2004/xiaohongshu-mcp; see NOTICE. 发笔记的守卫和回执：
// - 可见范围、内容类型声明（例如「笔记含AI合成内容」）选完都回读页面确认，没选上就不发；
// - 按网页自己的发布请求和回包拿笔记 id；没认出发布请求或回包里没有 id 时，页面离开了发布页，
//   再读创作中心「最近一篇笔记」，标题对得上就确认发出（同私信的「按历史消息确认」）。

package xiaohongshu

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

// ContentTypes 发布页「添加内容类型声明」里可以直接选的项（2026-10-11 实测；单选）。
var ContentTypes = []string{"虚构演绎，仅供娱乐", "笔记含AI合成内容", "内容包含营销广告"}

const (
	latestNotePath     = "/api/galaxy/creator/home/latest_note_data"
	visibilitySelector = "div.permission-card-wrapper div.d-select-content" // 可见范围下拉框（显示当前选中的项）
)

// publishPath 网页发笔记的接口，形如 /web_api/sns/v2/note（以实测为准再收紧）。
var publishPath = regexp.MustCompile(`/sns/v\d+/note$`)

// contentTypeSelectJS 内容类型声明的下拉框（d-select-wrapper）：没选时显示占位文字「添加内容类型声明」，
// 选了以后显示选中的那一项（2026-10-11 实测，选完占位文字就没了）。没有返回 null。
const contentTypeSelectJS = `(...options) => [...document.querySelectorAll('div.d-select-wrapper')]
	.find(e => { const text = e.innerText || ''; return text.includes('内容类型声明') || options.some(o => text.includes(o)); }) || null`

// selectVisibility 选可见范围并回读：页面上显示的不是它就报错。宁可不发，也不能误发成公开。
func selectVisibility(page *rod.Page, visibility string) error {
	if err := setVisibility(page, visibility); err != nil {
		return err
	}
	if visibility == "" {
		visibility = "公开可见"
	}
	shown, _ := elementText(page, visibilitySelector)
	return requireShown("可见范围", shown, visibility)
}

// requireShown 下拉框选完后，页面上显示的文字里要有 want；没有就报错：宁可不发，不能误发成公开、漏掉声明。
func requireShown(what, shown, want string) error {
	if !strings.Contains(shown, want) {
		return errors.Errorf("%s没有选上「%s」（页面显示 %q），没有发布", what, want, shown)
	}
	return nil
}

// shownContentType 「添加内容类型声明」下拉框现在显示的文字（没选时是占位文字本身）。
func shownContentType(page *rod.Page) string {
	el, err := page.Timeout(3 * time.Second).ElementByJS(rod.Eval(contentTypeSelectJS, contentTypeArgs()...))
	if err != nil || el == nil {
		return ""
	}
	text, _ := el.Text()
	return text
}

// setContentType 在「添加内容类型声明」里选一项，选完回读这个下拉框显示的是它；找不到、选不上都报错（不发布）。
func setContentType(page *rod.Page, option string) error {
	wrapper, err := page.Timeout(5 * time.Second).ElementByJS(rod.Eval(contentTypeSelectJS, contentTypeArgs()...))
	if err != nil || wrapper == nil {
		return errors.New("没找到「添加内容类型声明」，没有发布")
	}
	opener, err := wrapper.Element("div.d-select-content")
	if err != nil {
		return errors.Wrap(err, "没找到内容类型声明的下拉框")
	}
	if err := humanize.Click(opener); err != nil {
		return errors.Wrap(err, "点开内容类型声明失败")
	}
	time.Sleep(800 * time.Millisecond) // 技术等待：等下拉渲染
	options, err := page.Elements("div.d-options-wrapper div.d-option")
	if err != nil {
		return errors.Wrap(err, "读内容类型声明的选项失败")
	}
	for _, opt := range options {
		text, err := opt.Text()
		if err != nil || !strings.Contains(text, option) || !isElementClickable(opt) {
			continue
		}
		if err := humanize.Click(opt); err != nil {
			return errors.Wrap(err, "选择内容类型声明失败")
		}
		time.Sleep(500 * time.Millisecond)
		shown, _ := wrapper.Text()
		if err := requireShown("内容类型声明", shown, option); err != nil {
			return err
		}
		logrus.Infof("已选内容类型声明：%s", option)
		return nil
	}
	return errors.Errorf("内容类型声明里没有「%s」，没有发布", option)
}

func contentTypeArgs() []any {
	args := make([]any, len(ContentTypes))
	for i, option := range ContentTypes {
		args[i] = option
	}
	return args
}

func elementText(page *rod.Page, selector string) (string, error) {
	el, err := page.Timeout(3 * time.Second).Element(selector)
	if err != nil {
		return "", err
	}
	return el.Text()
}

// observePublish 认出网页自己发出的发布请求（路径是发笔记的接口、请求体里有这个标题），读它的回包。
func observePublish(page *rod.Page, title string) *submissionObserver {
	return observeSubmissionSpec(page, submissionSpec{
		onPath: publishPath.MatchString,
		match:  func(body string) bool { return jsonHasText(body, title) },
		field:  func(string) string { return "title" },
		parse:  parsePublishResponse,
	})
}

// parsePublishResponse 发布回包：业务成功而且带笔记 id 才算确认发出；成功但没有 id 是结果不明（交给最近一篇笔记核对）。
func parsePublishResponse(status int, body []byte) InteractionReceipt {
	r, data, ok := businessReceipt(status, body)
	if !ok {
		return r
	}
	var v map[string]any
	if json.Unmarshal(data, &v) == nil {
		r.NoteID = firstString(v, "note_id", "noteId", "id")
		if r.NoteID == "" {
			if note, ok := v["note"].(map[string]any); ok {
				r.NoteID = firstString(note, "note_id", "noteId", "id")
			}
		}
	}
	if r.NoteID != "" {
		r.State = StateConfirmed
	}
	return r
}

// jsonHasText 请求体（JSON）里有一个字符串值和 text 一样（忽略空白）。
func jsonHasText(body, text string) bool {
	var v any
	return json.Unmarshal([]byte(body), &v) == nil && hasText(v, text)
}

func hasText(v any, text string) bool {
	switch x := v.(type) {
	case string:
		return sameText(x, text)
	case map[string]any:
		for _, y := range x {
			if hasText(y, text) {
				return true
			}
		}
	case []any:
		for _, y := range x {
			if hasText(y, text) {
				return true
			}
		}
	}
	return false
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := m[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// publishWithReceipt 点发布前占位回执，按发布请求的回包确认；没确认就退回「最近一篇笔记」核对。
func publishWithReceipt(ctx context.Context, page *rod.Page, trace *publishTrace, key, title string) (*InteractionReceipt, error) {
	observer := observePublish(page, title)
	receipt, err := submitObserved(page, observer, key, func() error { return clickPublishButton(page) })
	if !unconfirmed(err) {
		return receipt, err
	}
	if leftErr := waitPublishSuccess(page, trace, 15*time.Second); leftErr != nil {
		logrus.Warnf("没认出发布回执，页面也没离开发布页: %v", leftErr)
		return receipt, err
	}
	humanize.Delay(ctx, humanize.Reading)
	id, shown, latestErr := latestNote(ctx, page)
	if latestErr != nil || id == "" || !sameText(shown, title) {
		logrus.Warnf("按最近一篇笔记核对没对上（id=%q 标题=%q err=%v）", id, shown, latestErr)
		return receipt, err
	}
	confirmed := InteractionReceipt{State: StateConfirmed, NoteID: id}
	if writeErr := finishReceipt(key, confirmed); writeErr != nil {
		logrus.WithField("key", key).Error("回执改写失败，保留「结果不明」记录")
		return receipt, err
	}
	logrus.Infof("笔记已按最近一篇笔记确认发出: %s", id)
	return &confirmed, nil
}

// latestNote 打开创作中心首页，读「最近一篇笔记」的 id 和标题。字段名按实测再收紧：先记下有哪些字段。
func latestNote(ctx context.Context, page *rod.Page) (id, title string, err error) {
	capture, err := captureAPI(ctx, page, func(u *url.URL) bool { return u.Path == latestNotePath })
	if err != nil {
		return "", "", err
	}
	defer capture.close()
	if err := page.Context(ctx).Navigate(urlOfCreatorHome()); err != nil {
		return "", "", errors.Wrap(err, "打开创作中心首页失败")
	}
	raw, err := capture.wait(ctx, latestNotePath, 0, 20*time.Second)
	if err != nil {
		return "", "", err
	}
	var v struct {
		Data struct {
			NoteInfo map[string]any `json:"noteInfo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", "", err
	}
	keys := make([]string, 0, len(v.Data.NoteInfo))
	for key := range v.Data.NoteInfo {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	logrus.Infof("最近一篇笔记的字段: %v", keys)
	info := v.Data.NoteInfo
	return firstString(info, "noteId", "note_id", "id"), firstString(info, "title", "displayTitle", "display_title"), nil
}

// Added in ycl-2004/xiaohongshu-mcp; see NOTICE. 删除本账号自己发的一篇笔记（写操作）。

package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	"github.com/xpzouying/xiaohongshu-mcp/site"
)

// ErrNoteNotFound 创作中心的笔记管理里没有这篇笔记（不等于已经删了：可能不是本账号的、或不在第一页）。
var ErrNoteNotFound = errors.New("笔记管理里没有这篇笔记")

const (
	noteManagerPath    = "/new/note-manager"
	noteListPath       = "/api/galaxy/v2/creator/note/user/posted" // 笔记管理的列表接口（2026-10-12 实测）
	noteManagerTimeout = 150 * time.Second                         // 服务器上打开海外站很慢（2026-10-12 实测一页 30–110 秒）
	nextPageTimeout    = 60 * time.Second
	maxNotePages       = 10
	markedCard         = `[data-ma-delete-note="1"]`
)

// NoteDeletion 删笔记（或只演练）的经过：怎么认出的卡片、确认弹窗里写的什么、网页发了哪些创作中心请求。
type NoteDeletion struct {
	NoteID   string              `json:"note_id"`
	Title    string              `json:"title"`              // 笔记管理接口里这篇的标题
	Page     int                 `json:"page"`               // 在笔记管理的第几页
	Matched  string              `json:"matched"`            // id：卡片里能找到笔记 id；title：按接口给的标题认出唯一一张
	Dialog   string              `json:"dialog"`             // 确认弹窗的文字
	DryRun   bool                `json:"dry_run"`            // 只演练：点开删除确认后点「取消」，没有删
	Cards    []string            `json:"cards,omitempty"`    // 演练时页面上每张卡片的摘要（标题、有没有笔记 id），排查用
	Requests []string            `json:"requests,omitempty"` // 网页发的创作中心请求（方法 + 域名路径）
	Listed   []string            `json:"listed,omitempty"`   // 没找到这篇时：笔记管理接口里列出的笔记（id 标题），排查用
	Skipped  []string            `json:"skipped,omitempty"`  // 没留下的接口响应（太大或不是 JSON）
	Receipt  *InteractionReceipt `json:"receipt,omitempty"`  // 真删：state confirmed = 重新打开笔记管理已经没有这篇
}

// DeleteOwnNote 在创作中心的笔记管理里删掉本账号自己发的一篇笔记。
//
// 只删笔记管理里列出来的（那里只有本账号的笔记）：先从笔记管理接口的回包里按笔记 id 找到这篇的标题，
// 老笔记要往后翻页；再在页面上找到这张卡片（卡片里有笔记 id 最好；没有就按标题，标题必须唯一），点「删除」，
// 确认弹窗出现后点「确定」。网页删除用的是哪个接口没有固定下来（2026-10-11 没抓到），所以不靠它判断结果：
// 删完重新打开笔记管理、翻到原来那一页都没有这个笔记 id 了才算删掉（confirmed）；还在或核对不了就是结果不明，
// 不要重删，先核验。
// dryRun 只演练到确认弹窗，然后点「取消」。
func DeleteOwnNote(ctx context.Context, page *rod.Page, noteID string, dryRun bool) (*NoteDeletion, error) {
	capture, err := captureAPI(ctx, page, isCreatorAPI)
	if err != nil {
		return nil, err
	}
	defer capture.close()
	p := page.Context(ctx).Timeout(9 * time.Minute) // 翻页找 + 删 + 删后再翻页核对，服务器上很慢
	if err := ensureCreatorSession(p); err != nil {
		return nil, err
	}
	out := &NoteDeletion{NoteID: noteID, DryRun: dryRun}
	defer func() { out.Requests, out.Skipped = capture.requestLog(), capture.skippedLog() }()

	title, onPage, scanned, err := scanNotePages(ctx, p, capture, noteID, maxNotePages)
	if err == nil && onPage == 0 {
		err = fmt.Errorf("%w: 翻了 %d 页都没有", ErrNoteNotFound, scanned)
	}
	if err != nil {
		out.Listed = listedNotes(capture.changedSince(nil))
		return out, err
	}
	out.Title, out.Page = title, onPage
	located, err := markNoteCard(p, noteID, title)
	out.Matched, out.Cards = located.Matched, located.Cards
	if err != nil {
		return out, err
	}
	if !dryRun {
		out.Cards = nil
	}
	card, err := p.Element(markedCard)
	if err != nil {
		return out, &InteractionError{Stage: "find", State: StateNotSent, Cause: err}
	}
	dialog, err := openNoteDeleteConfirm(ctx, p, card)
	if err != nil {
		return out, &InteractionError{Stage: "prepare", State: StateNotSent, Cause: err}
	}
	out.Dialog, _ = dialog.Text()
	if !dialogTitleMatches(out.Dialog, title) { // 弹窗写的标题不是这篇：点错了卡片，不删
		closeNoteDialog(dialog)
		return out, &InteractionError{Stage: "confirm_title", State: StateNotSent,
			Cause: fmt.Errorf("确认弹窗里的标题对不上：%q", out.Dialog)}
	}
	if dryRun {
		closeNoteDialog(dialog)
		return out, nil
	}
	confirm, err := dialogButton(dialog, `^(确定|确认|删除|确认删除)$`)
	if err != nil {
		return out, &InteractionError{Stage: "prepare", State: StateNotSent, Cause: err}
	}
	if err := settle(confirm); err != nil {
		return out, &InteractionError{Stage: "prepare", State: StateNotSent, Cause: fmt.Errorf("确认按钮没有停稳: %w", err)}
	}
	if err := humanize.Click(confirm); err != nil {
		return out, &InteractionError{Stage: "click", State: StateUnknown, Cause: err}
	}
	humanize.Delay(ctx, humanize.AfterNavigate)
	// 删掉以后后面的笔记往前挪：重新打开、翻到原来那一页为止都没有它，才算删掉
	if _, still, _, err := scanNotePages(ctx, p, capture, noteID, onPage); err != nil || still > 0 {
		if err == nil {
			err = fmt.Errorf("删后笔记管理第 %d 页还有这篇", still)
		}
		return out, &InteractionError{Stage: "confirm", State: StateUnknown,
			Cause: fmt.Errorf("核对不了删没删掉，不要重删，先在 App 里看一下: %w", err)}
	}
	logrus.Infof("Note deleted: note=%s title=%q", noteID, title)
	out.Receipt = &InteractionReceipt{State: StateConfirmed, NoteID: noteID}
	return out, nil
}

// scanNotePages 打开笔记管理，一页一页往后翻（最多 maxPages 页），看列表接口的每一页里有没有这篇笔记。
// 返回它的标题和在第几页（没找到为 0），以及看了几页；第一页都没等到才返回错误。
// 列表每页 10 篇、按发布时间倒序（2026-10-12 实测），老笔记要往后翻。
func scanNotePages(ctx context.Context, p *rod.Page, capture *imCapture, noteID string, maxPages int) (string, int, int, error) {
	seen := capture.count(noteListPath)
	if err := p.Navigate(site.Current().CreatorURL(noteManagerPath)); err != nil {
		return "", 0, 0, fmt.Errorf("打开笔记管理失败: %w", err)
	}
	for page := 1; page <= maxPages; page++ {
		wait := nextPageTimeout
		if page == 1 {
			wait = noteManagerTimeout
		}
		body, err := nextListPage(ctx, capture, &seen, wait)
		if err != nil {
			if page == 1 {
				return "", 0, 0, fmt.Errorf("%s 内没等到笔记管理的列表: %w", wait, err)
			}
			return "", 0, page - 1, nil // 翻不动了：没有下一页
		}
		if path, title := noteInBodies(map[string]json.RawMessage{noteListPath: body}, noteID); path != "" {
			humanize.Delay(ctx, humanize.Reading)
			return title, page, page, nil
		}
		if page == maxPages || len(listedNotes(map[string]json.RawMessage{noteListPath: body})) == 0 {
			return "", 0, page, nil
		}
		if err := turnNotePage(ctx, p); err != nil {
			return "", 0, page, nil
		}
	}
	return "", 0, maxPages, nil
}

// nextListPage 等列表接口再回一次包（seen 是已经看过的次数），返回这一页的回包。
func nextListPage(ctx context.Context, capture *imCapture, seen *int, wait time.Duration) (json.RawMessage, error) {
	deadline := time.Now().Add(wait)
	for {
		if n := capture.count(noteListPath); n > *seen {
			*seen = n
			return capture.changedSince(nil)[noteListPath], nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("列表接口没有回包")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-capture.notify:
		case <-time.After(time.Second):
		}
	}
}

// turnNotePage 翻到下一页：有「下一页」按钮就点，没有就滚到底（列表也可能是往下滚自动加载）。
func turnNotePage(ctx context.Context, p *rod.Page) error {
	next, err := p.Timeout(3 * time.Second).ElementByJS(rod.Eval(nextPageJS))
	if err == nil && next != nil {
		if err := next.ScrollIntoView(); err != nil {
			return err
		}
		humanize.Delay(ctx, humanize.BetweenScroll)
		return humanize.Click(next)
	}
	_, err = p.Eval(`() => {
		window.scrollTo(0, document.body.scrollHeight);
		for (const el of document.querySelectorAll('*')) {
			if (el.scrollHeight > el.clientHeight + 40 && /(auto|scroll)/.test(getComputedStyle(el).overflowY)) {
				el.scrollTop = el.scrollHeight;
			}
		}
	}`)
	return err
}

// nextPageJS 显示出来、没禁用的「下一页」：分页器里的 next，或者文字是「下一页」「>」的按钮。
const nextPageJS = `() => {
	const visible = e => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden';
	const disabled = e => e.matches('[disabled], [aria-disabled="true"], [class*="disabled"]') ||
		!!e.closest('[class*="disabled"]');
	const candidates = [
		...document.querySelectorAll('[class*="pagination"] [class*="next"], [class*="pager"] [class*="next"]'),
		...[...document.querySelectorAll('button, li, a, span')].filter(e =>
			e.childElementCount === 0 && ['下一页', '>', '›'].includes(e.textContent.trim())),
	];
	return candidates.find(e => visible(e) && !disabled(e)) || null;
}`

// noteLocation 页面上认卡片的结果。
type noteLocation struct {
	Matched string   `json:"matched"`
	Count   int      `json:"count"`
	Cards   []string `json:"cards"`
	Problem string   `json:"problem"`
}

// markNoteCard 在笔记管理页上找到这篇的卡片，给它打上 markedCard 标记。卡片里（属性、链接、组件数据）能找到
// 笔记 id 的优先；都找不到时按标题，标题必须正好对上一张。
func markNoteCard(p *rod.Page, noteID, title string) (noteLocation, error) {
	var loc noteLocation
	res, err := p.Eval(noteCardJS, noteID, title)
	if err != nil {
		return loc, &InteractionError{Stage: "find", State: StateNotSent, Cause: fmt.Errorf("读笔记卡片失败: %w", err)}
	}
	if err := json.Unmarshal([]byte(res.Value.String()), &loc); err != nil {
		return loc, &InteractionError{Stage: "find", State: StateNotSent, Cause: fmt.Errorf("解析笔记卡片失败: %w", err)}
	}
	if loc.Problem != "" {
		return loc, &InteractionError{Stage: "find", State: StateNotSent, Cause: fmt.Errorf("%w: %s", ErrNoteNotFound, loc.Problem)}
	}
	return loc, nil
}

// noteCardJS 笔记管理页的卡片：.note-card（2026-10-11 实测删除按钮是 .note-card__action-btn--del）。
const noteCardJS = `(id, title) => {
	document.querySelectorAll('[data-ma-delete-note]').forEach(e => e.removeAttribute('data-ma-delete-note'));
	const cards = [...document.querySelectorAll('.note-card')];
	const propsHave = el => {
		for (let node = el, depth = 0; node && depth < 4; node = node.parentElement, depth++) {
			let c = node.__vueParentComponent;
			for (let up = 0; c && up < 3; c = c.parent, up++) {
				try { if (JSON.stringify(c.props || {}).includes(id)) return true; } catch (e) {}
			}
		}
		return false;
	};
	const info = cards.map(card => {
		const text = (card.innerText || '').replace(/\s+/g, ' ').trim();
		const byId = card.outerHTML.includes(id) || propsHave(card);
		const byTitle = !!title && text.includes(title);
		return {card, text, byId, byTitle};
	});
	const summary = info.map(c => (c.byId ? '[id] ' : '') + c.text.slice(0, 40));
	const pick = (list, how) => {
		list[0].card.setAttribute('data-ma-delete-note', '1');
		return JSON.stringify({matched: how, count: cards.length, cards: summary, problem: ''});
	};
	const byId = info.filter(c => c.byId);
	if (byId.length === 1) return pick(byId, 'id');
	if (byId.length > 1) return JSON.stringify({count: cards.length, cards: summary, problem: '好几张卡片都带这个笔记 id'});
	const byTitle = info.filter(c => c.byTitle);
	if (byTitle.length === 1) return pick(byTitle, 'title');
	const problem = cards.length === 0 ? '页面上没有笔记卡片' :
		byTitle.length > 1 ? '好几篇笔记同标题，认不出是哪篇' : '页面上没有这篇的卡片';
	return JSON.stringify({count: cards.length, cards: summary, problem});
}`

// openNoteDeleteConfirm 鼠标移到卡片上、点卡片的「删除」，返回出现的确认弹窗（还没点确定）。
func openNoteDeleteConfirm(ctx context.Context, p *rod.Page, card *rod.Element) (*rod.Element, error) {
	if err := card.ScrollIntoView(); err != nil {
		return nil, fmt.Errorf("滚到卡片失败: %w", err)
	}
	humanize.Delay(ctx, humanize.BetweenScroll)
	if err := humanize.Hover(card); err != nil {
		return nil, fmt.Errorf("鼠标移到卡片上失败: %w", err)
	}
	humanize.Delay(ctx, humanize.PointerSettle)
	del, err := card.Timeout(5 * time.Second).ElementByJS(rod.Eval(`function() {
		return this.querySelector('.note-card__action-btn--del') ||
			[...this.querySelectorAll('button, [role="button"], span, div')].find(e =>
				e.childElementCount === 0 && e.textContent.trim() === '删除') || null;
	}`))
	if err != nil {
		return nil, fmt.Errorf("卡片上没有「删除」: %w", err)
	}
	if err := settle(del); err != nil {
		return nil, fmt.Errorf("「删除」没有停稳: %w", err)
	}
	if err := humanize.Click(del); err != nil {
		return nil, fmt.Errorf("点「删除」失败: %w", err)
	}
	dialog, err := findUntimedWithin(p, 10*time.Second, func(page *rod.Page) (*rod.Element, error) {
		return page.ElementByJS(rod.Eval(noteDialogJS))
	})
	if err != nil {
		return nil, fmt.Errorf("没有出现删除确认弹窗: %w", err)
	}
	if err := settle(dialog); err != nil {
		return nil, fmt.Errorf("删除确认弹窗没有停稳: %w", err)
	}
	return dialog, nil
}

// noteDialogJS 显示出来的、文字里有「删除」的弹窗（取最里层那个带按钮的容器）。
const noteDialogJS = `() => {
	const visible = e => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden';
	const boxes = [...document.querySelectorAll('[role="dialog"], [class*="modal"], [class*="dialog"], [class*="alert"], [class*="popconfirm"]')]
		.filter(e => visible(e) && (e.innerText || '').includes('删除') && e.querySelector('button, [class*="btn"]'));
	return boxes.sort((a, b) => a.innerText.length - b.innerText.length)[0] || null;
}`

// dialogButton 弹窗里文字匹配 pattern 的按钮。
func dialogButton(dialog *rod.Element, pattern string) (*rod.Element, error) {
	button, err := dialog.Timeout(5*time.Second).ElementR(`button, [class*="btn"]`, pattern)
	if err != nil {
		return nil, fmt.Errorf("删除确认弹窗里没有按钮 %s: %w", pattern, err)
	}
	return button, nil
}

// closeNoteDialog 点弹窗的「取消」；没有就按 Esc。演练和标题对不上时用，不能留着弹窗。
func closeNoteDialog(dialog *rod.Element) {
	if cancel, err := dialogButton(dialog, `^(取消|再想想|关闭)$`); err == nil {
		if err := humanize.Click(cancel); err == nil {
			return
		}
	}
	closeOverlay(dialog.Page())
}

// ---- 解析（纯函数，单测覆盖）----

var dialogTitle = regexp.MustCompile(`《(.*?)》`)

// dialogTitleMatches 确认弹窗里书名号中的标题是不是这篇。弹窗会把长标题截短（2026-10-12 实测
// 「嘉义搭子12.27-12.28」显示成《嘉义搭子12.27-》），所以认前缀；弹窗没写标题就不核对（卡片已经认过）。
func dialogTitleMatches(dialog, title string) bool {
	found := dialogTitle.FindStringSubmatch(dialog)
	if found == nil || title == "" {
		return true
	}
	shown := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(found[1]), "…."))
	return shown != "" && strings.HasPrefix(title, shown)
}

// noteInBodies 接口回包里有没有这个笔记 id：返回列出它的接口路径（没有为空）和它的标题。认 id、note_id、noteId 三种写法。
func noteInBodies(bodies map[string]json.RawMessage, noteID string) (string, string) {
	for path, body := range bodies {
		var v any
		if json.Unmarshal(body, &v) != nil {
			continue
		}
		if found, title := findNote(v, noteID); found {
			return path, title
		}
	}
	return "", ""
}

func findNote(v any, noteID string) (bool, string) {
	switch node := v.(type) {
	case map[string]any:
		for _, key := range []string{"id", "note_id", "noteId"} {
			if node[key] == noteID {
				for _, field := range []string{"display_title", "displayTitle", "title"} {
					if title, ok := node[field].(string); ok && title != "" {
						return true, title
					}
				}
				return true, ""
			}
		}
		for _, child := range node {
			if found, title := findNote(child, noteID); found {
				return true, title
			}
		}
	case []any:
		for _, child := range node {
			if found, title := findNote(child, noteID); found {
				return true, title
			}
		}
	}
	return false, ""
}

// listedNotes 回包里列出的笔记：同时带 id 和标题的对象，写成「id 标题」。
func listedNotes(bodies map[string]json.RawMessage) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			id := firstString(node, "id", "note_id", "noteId")
			title := firstString(node, "display_title", "displayTitle", "title")
			if id != "" && title != "" {
				out = append(out, id+" "+title)
			}
			for _, child := range node {
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	for _, body := range bodies {
		var v any
		if json.Unmarshal(body, &v) == nil {
			walk(v)
		}
	}
	return out
}

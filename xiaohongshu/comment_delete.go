package xiaohongshu

// Ported from liaogx/xiaohongshu-mcp (xiaohongshu/cleanup_actions.go; Apache-2.0) and adapted in
// ycl-2004/xiaohongshu-mcp: only deleting the logged-in account's own comment, by exact comment id. See NOTICE.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

// ErrNotOwnComment 要删的评论不是当前登录账号发的：不删。
var ErrNotOwnComment = errors.New("不是本账号的评论")

const commentDeleteEndpoint = "/comment/delete"

// DeleteOwnComment 删除当前登录账号自己的一条评论（演示完清理测试内容用）。
//
// 只按评论 id 精确定位，不按作者或正文猜：同样正文的评论可能有好几条。先从页面状态核对这条评论
// 是本账号发的，再点「…」→「删除评论」→ 确认弹窗的「确定」，按网页自己发出的删除请求和回包判断结果。
// 找不到评论返回 ErrCommentNotFound（不等于已经删了）。删除天然幂等，不记幂等回执。
func (f *CommentFeedAction) DeleteOwnComment(ctx context.Context, feedID, xsecToken, commentID string) (*InteractionReceipt, error) {
	page := f.page.Timeout(5 * time.Minute)
	page.MustNavigate(makeFeedDetailURL(feedID, xsecToken))
	waitFeedPageReady(page)
	humanize.Delay(ctx, humanize.AfterNavigate)

	if err := checkPageAccessible(page); err != nil {
		return nil, err
	}
	el, err := findCommentElement(ctx, page, commentID, "")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCommentNotFound, err)
	}
	if err := checkOwnComment(page, feedID, commentID); err != nil {
		return nil, err
	}
	button, err := openDeleteConfirm(ctx, page, el)
	if err != nil {
		logrus.Warnf("删除评论没有发出（准备阶段）: feed=%s comment=%s: %v", feedID, commentID, err)
		return nil, &InteractionError{Stage: "prepare", State: StateNotSent, Cause: err}
	}

	onPath := func(path string) bool { return strings.HasSuffix(path, commentDeleteEndpoint) }
	observer := observeSubmissionMatching(page, onPath, feedID, "", func(body string) bool {
		return deleteRequestMatches(body, feedID, commentID)
	})
	defer observer.close()
	if observer.setupErr != nil {
		return nil, &InteractionError{Stage: "response_observer", State: StateNotSent, Cause: observer.setupErr}
	}
	if err := humanize.Click(button); err != nil {
		// 点击是否送达说不准，按结果不明处理。
		return nil, &InteractionError{Stage: "click", State: StateUnknown, Cause: err}
	}
	result, err := awaitSubmission(page, observer, 20*time.Second)
	if err != nil {
		return nil, err
	}
	logrus.Infof("Comment deleted: feed=%s comment=%s", feedID, commentID)
	return &result, nil
}

// commentProof 页面状态里的证据：当前登录账号、这条评论在不在、作者是谁。
type commentProof struct {
	Me    string `json:"me"`
	Owner string `json:"owner"`
	Found bool   `json:"found"`
}

// checkOwnComment 从页面状态核对这条评论是当前登录账号发的。
func checkOwnComment(page *rod.Page, feedID, commentID string) error {
	res, err := page.Eval(`(feed, id) => {
		const unwrap = x => x?.value ?? x?._value ?? x;
		const state = window.__INITIAL_STATE__;
		const me = unwrap(state?.user?.userInfo);
		const detail = unwrap(unwrap(state?.note?.noteDetailMap)?.[feed]);
		const walk = cs => {
			for (const c of cs || []) {
				if (c.id === id) return c;
				const found = walk(c.subComments);
				if (found) return found;
			}
		};
		const c = walk(detail?.comments?.list);
		return JSON.stringify({me: me && !me.guest ? (me.userId || '') : '', owner: c?.userInfo?.userId || '', found: !!c});
	}`, feedID, commentID)
	if err != nil {
		return fmt.Errorf("读页面状态失败: %w", err)
	}
	var proof commentProof
	if err := json.Unmarshal([]byte(res.Value.String()), &proof); err != nil {
		return fmt.Errorf("解析页面状态失败: %w", err)
	}
	return judgeOwnership(proof)
}

func judgeOwnership(proof commentProof) error {
	switch {
	case !proof.Found:
		return fmt.Errorf("%w: 页面状态里没有这条评论", ErrCommentNotFound)
	case proof.Me == "":
		return &PageAccessError{Code: CodeLoginRequired}
	case proof.Owner != proof.Me:
		return ErrNotOwnComment
	}
	return nil
}

// openDeleteConfirm 点评论的「…」→「删除评论」，返回确认弹窗里的「确定」（还没点）。
// 「…」平时是透明的，鼠标移到评论上才显示：先滚到评论、移上去，再点（2026-10-10 实测，
// 直接点报「元素当前不可命中」）。
func openDeleteConfirm(ctx context.Context, page *rod.Page, el *rod.Element) (*rod.Element, error) {
	if err := el.ScrollIntoView(); err != nil {
		return nil, fmt.Errorf("滚到评论失败: %w", err)
	}
	humanize.Delay(ctx, humanize.BetweenScroll)
	if err := humanize.Hover(el); err != nil {
		return nil, fmt.Errorf("鼠标移到评论上失败: %w", err)
	}
	humanize.Delay(ctx, humanize.PointerSettle)
	more, err := el.Timeout(5 * time.Second).Element(".more")
	if err != nil {
		return nil, fmt.Errorf("评论没有「…」菜单")
	}
	if err := humanize.Click(more); err != nil {
		return nil, fmt.Errorf("点「…」失败: %w", err)
	}
	menu, err := page.Timeout(5 * time.Second).ElementByJS(rod.Eval(commentDeleteMenuJS))
	if err != nil {
		return nil, fmt.Errorf("菜单里没有「删除评论」：可能不是本账号的评论")
	}
	// 菜单弹出时有淡入动画，透明度没到位就点会被拒（2026-10-10 实测）：等它停稳。
	if err := settle(menu); err != nil {
		return nil, fmt.Errorf("「删除评论」菜单没有停稳: %w", err)
	}
	if err := humanize.Click(menu); err != nil {
		return nil, fmt.Errorf("点「删除评论」失败: %w", err)
	}
	return deleteConfirmButton(page)
}

// commentDeleteMenuJS 页面上可能同时挂着好几个评论菜单：只取真正显示出来的那个「删除评论」。
const commentDeleteMenuJS = `() => {
	return [...document.querySelectorAll('.menu-wrapper .menu-item')].find(e =>
		!e.closest('[aria-hidden="true"],[hidden],[inert]') && e.getClientRects().length > 0 &&
		getComputedStyle(e).visibility !== 'hidden' && e.textContent.trim() === '删除评论');
}`

// deleteConfirmButton 「确认删除此评论」弹窗里的「确定」。只在这个弹窗里找，不点任何通用的「确定」；
// 弹窗还在动画时坐标会变，等它停稳再交给调用方点（点歪了会关掉弹窗而不发删除请求）。
func deleteConfirmButton(page *rod.Page) (*rod.Element, error) {
	dialog, err := page.Timeout(5*time.Second).ElementR(".reds-alert", `确认删除此评论`)
	if err != nil {
		return nil, fmt.Errorf("没有出现删除确认弹窗")
	}
	button, err := dialog.ElementR(".reds-alert-footer .foot-btn.strong", `^(确定|确认)$`)
	if err != nil {
		return nil, fmt.Errorf("删除确认弹窗里没有「确定」")
	}
	if err := settle(button); err != nil {
		return nil, fmt.Errorf("删除确认弹窗没有停稳: %w", err)
	}
	return button, nil
}

// settle 等元素和它所有祖先的动画都放完、位置不再变、完全显示出来（最多 3 秒）。弹出的菜单和弹窗
// 都有淡入动画，没停稳就点会被点击函数当成不可命中，或者点歪。
func settle(el *rod.Element) error {
	stable := el.Timeout(3 * time.Second)
	err := stable.Wait(rod.Eval(`function() {
		for (let node = this; node; node = node.parentElement) {
			for (const animation of node.getAnimations?.() || []) {
				if (animation.pending || animation.playState === 'running') return false;
			}
		}
		return true;
	}`))
	if err == nil {
		err = stable.WaitStable(200 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	for deadline := time.Now().Add(3 * time.Second); !humanize.Visible(el); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			return fmt.Errorf("一直没有完全显示出来")
		}
	}
	return nil
}

// deleteRequestMatches 网页发出的删除请求是不是删这条：笔记和评论 id 都要对上。请求参数可能是驼峰
// 也可能是下划线，两种都看；两种写法互相矛盾就不认，不能因为其中一对对上就把别的删除请求当成这次的。
func deleteRequestMatches(body, feedID, commentID string) bool {
	var v map[string]any
	if feedID == "" || commentID == "" || json.Unmarshal([]byte(body), &v) != nil {
		return false
	}
	complete := false
	for _, pair := range [][2]string{{"noteId", "commentId"}, {"note_id", "comment_id"}} {
		note, hasNote := v[pair[0]]
		comment, hasComment := v[pair[1]]
		if hasNote && note != feedID || hasComment && comment != commentID {
			return false
		}
		complete = complete || hasNote && hasComment
	}
	return complete
}

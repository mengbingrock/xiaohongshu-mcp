package xiaohongshu

// Added in ycl-2004/xiaohongshu-mcp; see NOTICE. 只读一条评论所在的楼。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

// ErrCommentNotFound 页面上找不到要读的那条评论：被删了，或评论区太大、在时限内没翻到。
var ErrCommentNotFound = errors.New("评论不在页面上")

// maxThreadExpandRounds 一楼最多展开几轮。每次展开约十条，读回复用不着更多；
// 也给整个请求的耗时封顶（调用方 180 秒超时，查找评论本身最多 90 秒）。
const maxThreadExpandRounds = 5

// maxIdleExpandRounds 连续这么多轮一个都没点开才放弃。2026-10-11 实测：同一楼（4 条回复）第一次读，
// 找到评论后 3 秒就返回、一下没点开，只读回 1 条；再读一次就点开读全了。点击会偶发失败（页面重画后
// 刚找到的按钮就失效了），一轮没点开不算数，下一轮重新找按钮再点。
const maxIdleExpandRounds = 2

// CommentThread 一楼评论：楼主评论，回复在 Thread.SubComments 里。
type CommentThread struct {
	FeedID string  `json:"feed_id"`
	Thread Comment `json:"thread"`
	// Complete 读到的回复条数不少于页面上显示的回复数；为 false 时还有回复没展开出来。
	Complete bool `json:"complete"`
}

// ReadThread 打开笔记详情页，找到 commentID 那条评论（没给 id 就找 userID 发的第一条）所在的楼，
// 把这一楼折叠的回复展开，从页面状态里读出整楼。只读：不点赞、不回复。
//
// 看回复要用它，不能整页读评论：评论按热度排，评论多的笔记里我们那条往往不在前几十条，
// 楼中楼也默认只露第一条（2026-10-09 实测）。查找和展开复用回复评论时的那套。
func (f *FeedDetailAction) ReadThread(ctx context.Context, feedID, xsecToken, commentID, userID string) (*CommentThread, error) {
	page := f.page.Context(ctx).Timeout(5 * time.Minute)
	page.MustNavigate(makeFeedDetailURL(feedID, xsecToken))
	waitFeedPageReady(page)
	humanize.Delay(ctx, humanize.AfterNavigate)

	if err := checkPageAccessible(page); err != nil {
		return nil, err
	}

	el, err := findCommentElement(ctx, page, commentID, userID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCommentNotFound, err)
	}
	rootID, err := threadRootID(el)
	if err != nil {
		return nil, err
	}
	expandThread(ctx, page, feedID, rootID)
	return readThreadState(page, feedID, rootID)
}

// threadRootID 评论所在楼的楼主评论 id：.parent-comment 包着整楼，里面第一条 .comment-item 是楼主。
func threadRootID(el *rod.Element) (string, error) {
	res, err := el.Eval(`() => {
		const floor = this.closest('.parent-comment');
		const root = floor && floor.querySelector('.comment-item');
		return root ? root.id : '';
	}`)
	if err != nil {
		return "", fmt.Errorf("读评论所在的楼失败: %w", err)
	}
	rootID := strings.TrimPrefix(res.Value.String(), "comment-")
	if rootID == "" {
		return "", fmt.Errorf("%w: 找到了评论，但不在任何一楼里", ErrCommentNotFound)
	}
	return rootID, nil
}

// expandThread 把这一楼折叠的回复展开，直到页面状态里的回复条数够了、连续 maxIdleExpandRounds 轮没点开，
// 或到轮数上限。返回点开的次数。以结果（页面状态里的条数）为准，不以找没找到按钮为准：2026-10-10 实测，
// 以楼主为锚点时只在楼里找按钮一个都没找到，只读回第一条回复；找评论时展开视野里的按钮那套却能读全。
// 所以每轮先把楼末滚进视野（按钮在楼末），楼里的按钮找不到或点不上，就展开视野里的（重新找一遍按钮）。
func expandThread(ctx context.Context, page *rod.Page, feedID, rootID string) int {
	clicked, idle := 0, 0
	for round := 0; round < maxThreadExpandRounds && idle < maxIdleExpandRounds; round++ {
		if thread, err := readThreadState(page, feedID, rootID); err == nil && thread.Complete {
			break
		}
		scrollToFloorEnd(page, rootID)
		humanize.Delay(ctx, humanize.BetweenScroll)
		n := 0
		if button := threadExpandButton(page, rootID); button != nil {
			text, _ := button.Text()
			if clickElementWithHumanBehavior(ctx, page, button, text) {
				n = 1
			}
		}
		if n == 0 {
			n = expandNearbyReplies(ctx, page)
		}
		if n == 0 {
			idle++
			continue
		}
		idle = 0
		clicked += n
	}
	return clicked
}

// scrollToFloorEnd 把这一楼最后一条评论滚进视野：展开按钮跟在楼末。
func scrollToFloorEnd(page *rod.Page, rootID string) {
	last, err := page.Timeout(2 * time.Second).ElementByJS(rod.Eval(`(id) => {
		const root = document.getElementById('comment-' + id);
		const floor = root && root.closest('.parent-comment');
		const items = floor ? floor.querySelectorAll('.comment-item') : [];
		return items.length ? items[items.length - 1] : root;
	}`, rootID))
	if err != nil || last == nil {
		return
	}
	if err := last.ScrollIntoView(); err != nil {
		logrus.Debugf("滚到楼末失败: %v", err)
	}
}

// threadExpandButton 这一楼里第一个可点的展开回复按钮；没有返回 nil。每轮重新找，展开后页面会重画。
func threadExpandButton(page *rod.Page, rootID string) *rod.Element {
	floor, err := page.Timeout(2 * time.Second).ElementByJS(rod.Eval(`(id) => {
		const root = document.getElementById('comment-' + id);
		return root ? root.closest('.parent-comment') : null;
	}`, rootID))
	if err != nil || floor == nil {
		return nil
	}
	buttons, err := floor.Elements(".show-more")
	if err != nil {
		return nil
	}
	for _, button := range buttons {
		text, err := button.Text()
		if err == nil && isElementClickable(button) && isSafeExpandButton(button, text) {
			return button
		}
	}
	return nil
}

// readThreadState 从页面状态里取出这一楼。页面状态随加载、展开实时更新，带评论 id、作者 id 和被回复的那条。
func readThreadState(page *rod.Page, feedID, rootID string) (*CommentThread, error) {
	res, err := page.Eval(`(feedID, rootID) => {
		const detail = window.__INITIAL_STATE__?.note?.noteDetailMap?.[feedID];
		const root = (detail?.comments?.list || []).find(c => c.id === rootID);
		return root ? JSON.stringify(root) : '';
	}`, feedID, rootID)
	if err != nil {
		return nil, fmt.Errorf("读页面状态失败: %w", err)
	}
	return parseThread(feedID, res.Value.String())
}

func parseThread(feedID, raw string) (*CommentThread, error) {
	if raw == "" {
		return nil, fmt.Errorf("%w: 页面状态里没有这一楼", ErrCommentNotFound)
	}
	var root Comment
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return nil, fmt.Errorf("解析评论楼失败: %w", err)
	}
	return &CommentThread{FeedID: feedID, Thread: root, Complete: threadComplete(root)}, nil
}

// threadComplete 读到的回复不少于页面显示的回复数。回复数读不出来时按读全了算（没有更多可展开的依据）。
func threadComplete(root Comment) bool {
	shown, err := strconv.Atoi(root.SubCommentCount)
	return err != nil || len(root.SubComments) >= shown
}

package xiaohongshu

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

// CommentFeedAction 表示 Feed 评论动作
type CommentFeedAction struct {
	page *rod.Page
}

// NewCommentFeedAction 创建 Feed 评论动作
func NewCommentFeedAction(page *rod.Page) *CommentFeedAction {
	return &CommentFeedAction{page: page}
}

// commentTimeout 发一条评论（打开详情页、点输入框、像人一样逐字输入、点发送、等回包）的总上限。
// 海外站从服务器打开详情页就要 30 多秒，再加上逐字输入，2026-10-12 实测 60 秒在点发送之前就用完了（504，
// 状态 not_sent，没发出）。150 秒和搜索、打开主页一致，也短于调用方（Marketing Agent）180 秒的读超时，
// 免得这边还在发、那边已经当成结果不明。
const commentTimeout = 150 * time.Second

// PostComment 发表评论到 Feed
func (f *CommentFeedAction) PostComment(ctx context.Context, feedID, xsecToken, content, key string) (*InteractionReceipt, error) {
	// 不使用 Context(ctx)，避免继承外部 context 的超时
	page := f.page.Timeout(commentTimeout)

	url := makeFeedDetailURL(feedID, xsecToken)
	logrus.Infof("打开 feed 详情页: %s", url)

	// 导航到详情页
	page.MustNavigate(url)
	waitFeedPageReady(page)
	humanize.Delay(ctx, humanize.AfterNavigate)

	// 检测页面是否可访问
	if err := checkPageAccessible(page); err != nil {
		return nil, err
	}

	elem, err := page.Element("div.input-box div.content-edit span")
	if err != nil {
		logrus.Warnf("Failed to find comment input box: %v", err)
		return nil, fmt.Errorf("未找到评论输入框，该帖子可能不支持评论或网页端不可访问: %w", err)
	}

	if err := humanize.Click(elem); err != nil {
		logrus.Warnf("Failed to click comment input box: %v", err)
		return nil, fmt.Errorf("无法点击评论输入框: %w", err)
	}
	humanize.Delay(ctx, humanize.AfterClick)

	elem2, err := page.Element("div.input-box div.content-edit p.content-input")
	if err != nil {
		logrus.Warnf("Failed to find comment input field: %v", err)
		return nil, fmt.Errorf("未找到评论输入区域: %w", err)
	}

	if err := humanize.Type(ctx, elem2, content); err != nil {
		logrus.Warnf("Failed to input comment content: %v", err)
		return nil, fmt.Errorf("无法输入评论内容: %w", err)
	}

	humanize.Delay(ctx, humanize.AfterType)

	submitButton, err := page.Element("div.bottom button.submit")
	if err != nil {
		logrus.Warnf("Failed to find submit button: %v", err)
		return nil, fmt.Errorf("未找到提交按钮: %w", err)
	}

	// 点发送前占位回执，按网页自己的提交请求和平台业务回执判断结果（见 interaction.go）。
	// Modified for ycl-2004/xiaohongshu-mcp：上游按评论区是否出现文字判断，看不到不等于没发出。
	receipt, err := submitWithReceipt(ctx, f.page.Context(ctx), submitButton, key, feedID, content)
	if err != nil {
		return nil, err
	}
	logrus.Infof("Comment posted to feed: %s state=%s", feedID, receipt.State)
	return receipt, nil
}

// ReplyToComment 回复指定评论
func (f *CommentFeedAction) ReplyToComment(ctx context.Context, feedID, xsecToken, commentID, userID, content, key string) (*InteractionReceipt, error) {
	// 增加超时时间，因为需要滚动查找评论
	// 注意：不使用 Context(ctx)，避免继承外部 context 的超时
	page := f.page.Timeout(5 * time.Minute)
	url := makeFeedDetailURL(feedID, xsecToken)
	logrus.Infof("打开 feed 详情页进行回复: %s", url)

	// 导航到详情页
	page.MustNavigate(url)
	waitFeedPageReady(page)
	humanize.Delay(ctx, humanize.AfterNavigate)

	// 检测页面是否可访问
	if err := checkPageAccessible(page); err != nil {
		return nil, err
	}

	time.Sleep(2 * time.Second)

	// 使用 Go 实现的查找逻辑
	commentEl, err := findCommentElement(ctx, page, commentID, userID)
	if err != nil {
		return nil, fmt.Errorf("无法找到评论: %w", err)
	}

	// 滚动到评论位置
	logrus.Info("滚动到评论位置...")
	commentEl.MustScrollIntoView()
	humanize.Delay(ctx, humanize.BetweenScroll)

	logrus.Info("准备点击回复按钮")

	// 查找并点击回复按钮
	replyBtn, err := commentEl.Element(".right .interactions .reply")
	if err != nil {
		return nil, fmt.Errorf("无法找到回复按钮: %w", err)
	}

	if err := humanize.Click(replyBtn); err != nil {
		return nil, fmt.Errorf("点击回复按钮失败: %w", err)
	}

	humanize.Delay(ctx, humanize.AfterClick)

	// 查找回复输入框
	inputEl, err := page.Element("div.input-box div.content-edit p.content-input")
	if err != nil {
		return nil, fmt.Errorf("无法找到回复输入框: %w", err)
	}

	// 输入内容
	if err := humanize.Type(ctx, inputEl, content); err != nil {
		return nil, fmt.Errorf("输入回复内容失败: %w", err)
	}

	humanize.Delay(ctx, humanize.AfterType)

	// 查找并点击提交按钮
	submitBtn, err := page.Element("div.bottom button.submit")
	if err != nil {
		return nil, fmt.Errorf("无法找到提交按钮: %w", err)
	}

	// 点发送前占位回执，按网页自己的提交请求和平台业务回执判断结果（见 interaction.go）。
	// Modified for ycl-2004/xiaohongshu-mcp：上游按评论区是否出现文字判断，看不到不等于没发出。
	receipt, err := submitWithReceipt(ctx, f.page.Context(ctx), submitBtn, key, feedID, content)
	if err != nil {
		return nil, err
	}
	logrus.Infof("Reply posted to feed: %s state=%s", feedID, receipt.State)
	return receipt, nil
}

// findCommentElement 滚动查找指定评论：优先按 commentID 命中，否则按 userID 匹配。
// 二级评论折叠在「展开 N 条回复」后面，未展开时不在 DOM 里，因此查找过程中要展开。
func findCommentElement(ctx context.Context, page *rod.Page, commentID, userID string) (*rod.Element, error) {
	logrus.Infof("开始查找评论 - commentID: %s, userID: %s", commentID, userID)

	scrollToCommentsArea(page)
	humanize.Delay(ctx, humanize.BetweenScroll)

	lastCommentCount := 0
	stagnantChecks := 0
	expandRounds := 0
	deadline := time.Now().Add(maxSearchDuration)

	// attempt 只数下滚，展开不计入，由 deadline 收口。
	for attempt := 0; attempt < maxSearchScrolls; {
		if time.Now().After(deadline) {
			logrus.Warnf("查找超过 %s，停止", maxSearchDuration)
			return nil, fmt.Errorf("评论区过大，%s 内未找到目标评论 (commentID: %s, userID: %s)",
				maxSearchDuration, commentID, userID)
		}

		// 1. 先查已渲染的评论
		if el := lookupComment(page, commentID, userID); el != nil {
			logrus.Infof("✓ 找到目标评论（下滚 %d 次）", attempt)
			return el, nil
		}

		// 2. 展开视野里的楼中楼后再查一次
		if expandRounds < maxExpandRounds {
			if expanded := expandNearbyReplies(ctx, page); expanded > 0 {
				expandRounds++
				humanize.Delay(ctx, humanize.Reading)

				if el := lookupComment(page, commentID, userID); el != nil {
					logrus.Infof("✓ 展开楼中楼后找到目标评论（下滚 %d 次）", attempt)
					return el, nil
				}

				continue
			}
		}
		expandRounds = 0
		attempt++

		// 3. 到底则不再加载
		if checkEndContainer(page) {
			logrus.Info("已到达评论底部，未找到目标评论")
			break
		}

		// 4. 评论数停滞说明加载不动了
		currentCount := getCommentCount(page)
		if currentCount != lastCommentCount {
			lastCommentCount = currentCount
			stagnantChecks = 0
		} else {
			stagnantChecks++
			if stagnantChecks >= 10 {
				logrus.Info("评论数量停滞，停止查找")
				break
			}
		}

		// 5. 滚到最后一条评论再继续下滚，触发懒加载
		if currentCount > 0 {
			if elements, err := page.Timeout(2 * time.Second).Elements(".comment-item"); err == nil && len(elements) > 0 {
				if err := elements[len(elements)-1].ScrollIntoView(); err != nil {
					logrus.Debugf("滚动到最后一条评论失败: %v", err)
				}
			}
			humanize.Delay(ctx, humanize.BetweenScroll)
		}

		humanScroll(ctx, page, "normal", false, 1)
		humanize.Delay(ctx, humanize.BetweenScroll)
	}

	return nil, fmt.Errorf("未找到评论 (commentID: %s, userID: %s)", commentID, userID)
}

// lookupComment 在当前已渲染的评论里查找目标，找不到返回 nil。
func lookupComment(page *rod.Page, commentID, userID string) *rod.Element {
	if commentID != "" {
		if el, err := page.Timeout(2 * time.Second).Element(fmt.Sprintf("#comment-%s", commentID)); err == nil && el != nil {
			return el
		}
	}

	if userID == "" {
		return nil
	}

	// 一级和二级评论都带 .comment-item；.parent-comment 会把整个楼层一起匹配上。
	elements, err := page.Timeout(2 * time.Second).Elements(".comment-item")
	if err != nil {
		return nil
	}
	for _, el := range elements {
		if userEl, err := el.Timeout(500 * time.Millisecond).Element(fmt.Sprintf(`[data-user-id="%s"]`, userID)); err == nil && userEl != nil {
			return el
		}
	}
	return nil
}

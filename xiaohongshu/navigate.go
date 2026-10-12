package xiaohongshu

import (
	"context"
	"time"

	"github.com/go-rod/rod"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
	"github.com/xpzouying/xiaohongshu-mcp/site"
)

type NavigateAction struct {
	page *rod.Page
}

func NewNavigate(page *rod.Page) *NavigateAction {
	return &NavigateAction{page: page}
}

func (n *NavigateAction) ToExplorePage(ctx context.Context) error {
	page := n.page.Context(ctx).Timeout(60 * time.Second) // 加超时保护，避免 MustNavigate/MustWaitStable 无限挂

	page.MustNavigate(site.Current().WebURL("/explore")).
		MustWaitLoad().
		MustElement(`div#app`)

	return nil
}

func (n *NavigateAction) ToProfilePage(ctx context.Context) error {
	page := n.page.Context(ctx).Timeout(profileTimeout) // 加超时保护，避免 MustNavigate/MustWaitStable 无限挂

	// 进发现页只等侧边栏的「我」出现，不等整页加载、不等页面稳定：海外站从服务器打开时发现页的图片流
	// 要加载很久，2026-10-12 实测 ToExplorePage 的 MustWaitLoad 在 60 秒内等不完（504）。
	page.MustNavigate(site.Current().WebURL("/explore"))

	// Find and click the "我" channel link in sidebar
	profileLink := page.MustElement(`div.main-container li.user.side-bar-component a.link-wrapper span.channel`)
	humanize.Delay(ctx, humanize.BeforeClick)
	if err := humanize.Click(profileLink); err != nil {
		return err
	}

	// Wait for navigation to complete
	page.MustWaitLoad()

	return nil
}

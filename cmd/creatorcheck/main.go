// creatorcheck is a diagnostic: it opens a browser from the configured cookie
// file, exactly as a publish does (site + seed + XHS_PROXY honoured), reports
// whether the home page and the creator center each consider the session
// signed in, then records a timeline of the creator publish page (screenshot +
// DOM indicators every few seconds) so a "blank page" can be told apart from a
// slow one. Screenshots go to REDNOTE_DEBUG_DIR.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-rod/rod"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

func main() {
	store := cookies.NewLoadCookie(cookies.GetCookiesFilePath())
	seed := configs.ResolveFingerprintSeed(store)
	configs.SetFingerprintSeed(seed)
	configs.SetProxy(configs.ProxyFromEnv())
	xiaohongshu.SetSite(xiaohongshu.ResolveSite(configs.SiteKeyFromEnv(), store))
	fmt.Printf("site=%s proxy=%q\n", xiaohongshu.CurrentSite().Key, configs.Proxy())

	b := browser.NewBrowser(true,
		browser.WithFingerprintSeed(seed),
		browser.WithProxy(configs.Proxy()),
	)
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	// CREATORCHECK_TIMELINE_ONLY=1 时跳过两项登录检查，只看发布页时间线。
	page := b.NewPage()
	if os.Getenv("CREATORCHECK_TIMELINE_ONLY") == "" {
		wwwOK, wwwErr := xiaohongshu.NewLogin(page).CheckLoginStatus(ctx)
		fmt.Printf("home  logged_in=%v err=%v\n", wwwOK, wwwErr)
		_ = page.Close()

		page = b.NewPage()
		creatorOK, creatorErr := xiaohongshu.NewLogin(page).CheckCreatorLoginStatus(ctx)
		fmt.Printf("creator  logged_in=%v err=%v\n", creatorOK, creatorErr)
		_ = page.Close()
		page = b.NewPage()
	}

	// 时间线：发布页到底是慢还是真白屏。
	defer page.Close()
	trace := xiaohongshu.NewDiagnosticTrace("creator-timeline")
	trace.AttachNetwork(page)
	start := time.Now()
	if err := page.Context(ctx).Timeout(90 * time.Second).Navigate(xiaohongshu.CurrentSite().PublishURL); err != nil {
		fmt.Printf("navigate err=%v\n", err)
		return
	}
	for _, at := range []time.Duration{2, 5, 10, 15, 25, 40, 60, 90} {
		time.Sleep(at*time.Second - time.Since(start))
		info, _ := page.Info()
		fmt.Printf("t=%2ds url=%s | %s | body_text=%d\n", int(time.Since(start).Seconds()), info.URL,
			xiaohongshu.PublishPageSnapshot(page), bodyTextLen(page))
		trace.Capture(page, fmt.Sprintf("t%02ds", int(at.Seconds())))
	}
	fmt.Printf("http errors: %v\n", trace.HTTPErrors())
}

func bodyTextLen(page *rod.Page) int {
	res, err := page.Timeout(5 * time.Second).Eval(`() => (document.body && document.body.innerText ? document.body.innerText.length : 0)`)
	if err != nil {
		return -1
	}
	return res.Value.Int()
}

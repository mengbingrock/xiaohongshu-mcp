// covercheck is a diagnostic: it opens a browser from the configured cookie
// file exactly as a publish does (site + seed + XHS_PROXY honoured), uploads
// the video named by COVERCHECK_VIDEO on the creator publish page, and prints
// the DOM of the "设置封面" control plus whatever its thumbnail opens. Used to
// derive selectors for cover support. It never clicks 发布.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

func main() {
	video := os.Getenv("COVERCHECK_VIDEO")
	if video == "" {
		fmt.Fprintln(os.Stderr, "COVERCHECK_VIDEO is required")
		os.Exit(2)
	}

	store := cookies.NewLoadCookie(cookies.GetCookiesFilePath())
	seed := configs.ResolveFingerprintSeed(store)
	configs.SetFingerprintSeed(seed)
	configs.SetProxy(configs.ProxyFromEnv())
	xiaohongshu.SetSite(xiaohongshu.ResolveSite(configs.SiteKeyFromEnv(), store))
	fmt.Printf("site=%s proxy=%q video=%s\n\n", xiaohongshu.CurrentSite().Key, configs.Proxy(), video)

	b := browser.NewBrowser(true,
		browser.WithFingerprintSeed(seed),
		browser.WithProxy(configs.Proxy()),
	)
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	page := b.NewPage()
	defer page.Close()

	out, err := xiaohongshu.ProbeCoverUI(ctx, page, video)
	fmt.Println(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nprobe error: %v\n", err)
		os.Exit(1)
	}
}

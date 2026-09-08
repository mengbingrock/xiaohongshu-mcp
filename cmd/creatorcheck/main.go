// creatorcheck is a diagnostic: it opens a browser from the configured cookie
// file, exactly as a publish does, and reports whether www.xiaohongshu.com and
// creator.xiaohongshu.com each consider the session signed in. Screenshots go
// to REDNOTE_DEBUG_DIR.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

func main() {
	store := cookies.NewLoadCookie(cookies.GetCookiesFilePath())
	seed := configs.ResolveFingerprintSeed(store)
	configs.SetFingerprintSeed(seed)

	b := browser.NewBrowser(true, browser.WithFingerprintSeed(seed))
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	page := b.NewPage()
	wwwOK, wwwErr := xiaohongshu.NewLogin(page).CheckLoginStatus(ctx)
	fmt.Printf("www.xiaohongshu.com  logged_in=%v err=%v\n", wwwOK, wwwErr)
	_ = page.Close()

	page = b.NewPage()
	creatorOK, creatorErr := xiaohongshu.NewLogin(page).CheckCreatorLoginStatus(ctx)
	fmt.Printf("creator.xiaohongshu.com  logged_in=%v err=%v\n", creatorOK, creatorErr)
	_ = page.Close()
}

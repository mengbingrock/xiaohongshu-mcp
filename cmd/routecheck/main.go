// routecheck is a diagnostic: it starts a browser exactly like the QR login
// flow does (fingerprint, zh-CN override, no cookies) and reports where
// www.xiaohongshu.com/explore actually lands for this host.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
)

func main() {
	seed := configs.FingerprintSeedFromEnv()
	if seed <= 0 {
		seed = 1915411319
	}
	configs.SetFingerprintSeed(seed)
	b := browser.NewBrowser(true, browser.WithFingerprintSeed(seed))
	defer b.Close()

	page := b.NewPage()
	defer page.Close()
	start := time.Now()
	page.Timeout(60 * time.Second).MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()
	time.Sleep(8 * time.Second)

	info, _ := page.Info()
	title, _ := page.Timeout(10 * time.Second).Eval(`() => document.title`)
	body, _ := page.Timeout(10 * time.Second).Eval(`() => (document.body && document.body.innerText || "").slice(0, 4000)`)
	text := body.Value.Str()
	tz, _ := page.Timeout(10 * time.Second).Eval(`() => Intl.DateTimeFormat().resolvedOptions().timeZone + " " + navigator.language + " " + JSON.stringify(navigator.languages)`)
	fmt.Printf("TZ env=%s seed=%d elapsed=%s\n", os.Getenv("TZ"), seed, time.Since(start).Round(time.Second))
	fmt.Printf("final url = %s\n", info.URL)
	fmt.Printf("title     = %s\n", title.Value.Str())
	fmt.Printf("page tz/lang = %s\n", tz.Value.Str())
	fmt.Printf("rednote mentions=%d  xiaohongshu mentions=%d  has_login_button=%v\n",
		strings.Count(strings.ToLower(text), "rednote"), strings.Count(text, "小红书"), strings.Contains(text, "登录"))
}

// intlcheck is the Phase-0 spike for international (rednote.com) accounts.
// It loads a session from COOKIES_PATH, visits the rednote.com properties and
// reports: final URLs, hosts contacted (API host discovery), which CN
// structural selectors exist on the international pages, and the visible
// tab / placeholder / button texts. Screenshots go to INTLCHECK_OUT.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
)

var (
	mu       sync.Mutex
	hosts    = map[string]int{}
	statuses []string
)

const probeJS = `() => {
  const q = (s) => document.querySelector(s) !== null;
  const texts = (s) => Array.from(document.querySelectorAll(s)).map(e => (e.innerText||e.textContent||"").trim()).filter(Boolean).slice(0, 40);
  const attrs = (s, a) => Array.from(document.querySelectorAll(s)).map(e => e.getAttribute(a)).filter(Boolean).slice(0, 40);
  return JSON.stringify({
    url: location.href, title: document.title, lang: navigator.language,
    cn_selectors: {
      www_user_channel: q(".main-container .user .link-wrapper .channel"),
      creator_tab: q("div.creator-tab"),
      upload_content: q("div.upload-content"),
      file_input: q('input[type="file"]'),
      title_input_cn: q('input[placeholder*="填写标题"], textarea[placeholder*="填写标题"], [contenteditable="true"][data-placeholder*="填写标题"]'),
      content_input_cn: q('[contenteditable="true"][data-placeholder*="输入正文"], [contenteditable="true"][aria-label*="正文"]'),
      publish_button: q("xhs-publish-btn, .publish-page-publish-btn button.bg-red"),
      publish_btn_wrapper: q(".publish-page-publish-btn"),
      img_preview_area: q(".img-preview-area"),
    },
    creator_tabs: texts("div.creator-tab"),
    file_input_accept: attrs('input[type="file"]', "accept"),
    placeholders: attrs("input, textarea", "placeholder"),
    data_placeholders: attrs("[data-placeholder]", "data-placeholder"),
    buttons: texts("button"),
    sidebar_links: texts(".main-container .user, .side-bar, [class*=sidebar], [class*=side-bar]").slice(0, 10),
    body_head: (document.body && document.body.innerText || "").slice(0, 500),
  });
}`

const formJS = `() => {
  const vis = (el) => { const r = el.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
  const texts = (s) => Array.from(document.querySelectorAll(s)).filter(vis).map(e => (e.innerText||e.textContent||"").trim()).filter(Boolean).slice(0, 60);
  const find = (words) => Array.from(document.querySelectorAll("div,span,label,button,p")).filter(vis).filter(e => e.children.length === 0 && words.some(w => (e.textContent||"").includes(w))).map(e => e.tagName.toLowerCase()+"."+(e.className||"").toString().split(" ").filter(Boolean).slice(0,3).join(".")+" => "+(e.textContent||"").trim()).slice(0, 25);
  const ce = Array.from(document.querySelectorAll('[contenteditable="true"]')).map(e => ({tag: e.tagName, cls: (e.className||"").toString().slice(0,80), placeholder: e.getAttribute("data-placeholder")||e.getAttribute("aria-label")||e.getAttribute("placeholder")||""}));
  const inputs = Array.from(document.querySelectorAll("input,textarea")).filter(vis).map(e => ({tag: e.tagName, type: e.type, placeholder: e.placeholder||"", cls: (e.className||"").toString().slice(0,60)}));
  return JSON.stringify({
    url: location.href,
    publish_btn_custom_el: !!document.querySelector("xhs-publish-btn"),
    publish_btn_red: !!document.querySelector(".publish-page-publish-btn button.bg-red"),
    publish_btn_any: texts(".publish-page-publish-btn button"),
    title_cn_sel: !!document.querySelector('input[placeholder*="填写标题"], textarea[placeholder*="填写标题"], [contenteditable="true"][data-placeholder*="填写标题"]'),
    body_cn_sel: !!document.querySelector('[contenteditable="true"][data-placeholder*="输入正文"], [contenteditable="true"][aria-label*="正文"]'),
    img_preview_count: document.querySelectorAll(".img-preview-area .pr").length,
    contenteditables: ce,
    visible_inputs: inputs,
    visibility_and_original_nodes: find(["公开可见","仅自己可见","仅互关好友可见","可见范围","原创声明","声明原创","Public","Private","Original","Visibility"]),
    bottom_buttons: texts("button").slice(-12),
  });
}`

func imageFileInput(page *rod.Page) *rod.Element {
	inputs, err := page.Timeout(5 * time.Second).Elements(`input[type="file"]`)
	if err != nil {
		return nil
	}
	for _, in := range inputs {
		accept, err := in.Attribute("accept")
		if err != nil || accept == nil {
			continue
		}
		a := strings.ToLower(*accept)
		if strings.Contains(a, "image/") || strings.Contains(a, ".jpg") || strings.Contains(a, ".png") || strings.Contains(a, ".webp") {
			return in
		}
	}
	return nil
}

func switchToImageTab(page *rod.Page) bool {
	tabs, err := page.Timeout(10 * time.Second).Elements("div.creator-tab")
	if err != nil {
		return false
	}
	for _, t := range tabs {
		txt, _ := t.Text()
		if strings.TrimSpace(txt) != "上传图文" {
			continue
		}
		box, err := t.Timeout(5 * time.Second).Shape()
		if err != nil || len(box.Quads) == 0 {
			continue
		}
		b := box.Box()
		if b.Width == 0 || b.Height == 0 {
			continue // hidden duplicate
		}
		_ = t.Timeout(5*time.Second).Click(proto.InputMouseButtonLeft, 1)
		time.Sleep(1500 * time.Millisecond)
		if imageFileInput(page) != nil {
			return true
		}
		// covered by a transparent overlay: coordinate click like production
		_ = page.Mouse.MoveTo(proto.Point{X: b.X + b.Width/2, Y: b.Y + b.Height/2})
		_ = page.Mouse.Click(proto.InputMouseButtonLeft, 1)
		time.Sleep(1500 * time.Millisecond)
		if imageFileInput(page) != nil {
			return true
		}
	}
	return false
}

func attachNetwork(page *rod.Page) {
	page.EnableDomain(&proto.NetworkEnable{})
	go page.EachEvent(func(e *proto.NetworkRequestWillBeSent) {
		if e.Request == nil {
			return
		}
		h := hostOf(e.Request.URL)
		mu.Lock()
		hosts[h]++
		mu.Unlock()
	}, func(e *proto.NetworkResponseReceived) {
		if e.Response == nil || e.Response.Status < 400 {
			return
		}
		mu.Lock()
		statuses = append(statuses, fmt.Sprintf("%d %s %s", e.Response.Status, e.Type, e.Response.URL))
		mu.Unlock()
	})()
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexByte(u, '/'); i > 0 {
		u = u[:i]
	}
	return u
}

func shot(page *rod.Page, out, name string) {
	data, err := page.Timeout(15*time.Second).Screenshot(true, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		fmt.Printf("screenshot %s failed: %v\n", name, err)
		return
	}
	_ = os.WriteFile(filepath.Join(out, name+".png"), data, 0o600)
}

func probe(page *rod.Page, out, name string) {
	time.Sleep(6 * time.Second)
	shot(page, out, name)
	res, err := page.Timeout(15 * time.Second).Eval(probeJS)
	if err != nil {
		fmt.Printf("=== %s: probe failed: %v\n", name, err)
		return
	}
	var pretty map[string]any
	_ = json.Unmarshal([]byte(res.Value.Str()), &pretty)
	b, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Printf("=== %s ===\n%s\n", name, b)
}

func main() {
	out := os.Getenv("INTLCHECK_OUT")
	if out == "" {
		out = "/tmp/intlcheck-out"
	}
	_ = os.MkdirAll(out, 0o700)

	store := cookies.NewLoadCookie(cookies.GetCookiesFilePath())
	seed := configs.ResolveFingerprintSeed(store)
	configs.SetFingerprintSeed(seed)
	fmt.Printf("cookies=%s seed=%d out=%s\n", cookies.GetCookiesFilePath(), seed, out)

	b := browser.NewBrowser(true, browser.WithFingerprintSeed(seed))
	defer b.Close()
	page := b.NewPage()
	defer page.Close()
	attachNetwork(page)

	nav := func(u string) {
		if err := page.Timeout(60 * time.Second).Navigate(u); err != nil {
			fmt.Printf("navigate %s failed: %v\n", u, err)
		}
		_ = page.Timeout(30 * time.Second).WaitLoad()
	}

	nav("https://www.rednote.com/")
	probe(page, out, "01-www-home")

	nav("https://creator.rednote.com/publish/publish?source=official")
	probe(page, out, "02-creator-publish")

	// Switch to the image tab the way production does: match text, skip
	// zero-size duplicates, click, and fall back to a coordinate click if the
	// tab is covered. Then upload ONE image into the draft (never publish) so
	// the editor form renders and its selectors can be probed.
	clicked := switchToImageTab(page)
	fmt.Printf("image tab switched: %v\n", clicked)
	imgInput := imageFileInput(page)
	if imgInput == nil {
		fmt.Println("no image file input found after tab switch")
	} else if test := os.Getenv("INTLCHECK_IMAGE"); test != "" {
		if err := imgInput.SetFiles([]string{test}); err != nil {
			fmt.Printf("SetFiles failed: %v\n", err)
		} else {
			// wait for the preview like waitForUploadComplete does
			for i := 0; i < 60; i++ {
				if els, err := page.Elements(".img-preview-area .pr"); err == nil && len(els) > 0 {
					fmt.Printf("image preview ready after %ds\n", i)
					break
				}
				time.Sleep(time.Second)
			}
		}
	}
	probe(page, out, "03-creator-editor-form")
	res, err := page.Timeout(15 * time.Second).Eval(formJS)
	if err == nil {
		var pretty map[string]any
		_ = json.Unmarshal([]byte(res.Value.Str()), &pretty)
		b, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Printf("=== 03b-editor-form-details ===\n%s\n", b)
	} else {
		fmt.Printf("form probe failed: %v\n", err)
	}

	nav("https://creator.rednote.com/")
	probe(page, out, "04-creator-home")

	mu.Lock()
	keys := make([]string, 0, len(hosts))
	for k := range hosts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return hosts[keys[i]] > hosts[keys[j]] })
	fmt.Println("=== hosts contacted (count) ===")
	for _, k := range keys {
		fmt.Printf("%5d %s\n", hosts[k], k)
	}
	fmt.Println("=== http >=400 ===")
	for _, s := range statuses {
		fmt.Println(s)
	}
	mu.Unlock()
}

package xiaohongshu

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/pkg/errors"
)

// ProbeCoverUI is a discovery aid for the video cover ("设置封面") control: it
// uploads a video on the creator publish page and dumps the cover section's
// DOM, then the DOM of whatever the cover thumbnail opens. Selectors for the
// cover editor are not documented anywhere, so this records them from the live
// page instead of guessing. Diagnostic only; never called from a publish, and
// it never clicks 发布.
func ProbeCoverUI(ctx context.Context, page *rod.Page, videoPath string) (report string, err error) {
	var b strings.Builder
	say := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}
	// mustClickPublishTab and friends panic through rod's Must* helpers.
	defer func() {
		if r := recover(); r != nil {
			err = errors.Errorf("panic: %v", r)
			report = b.String()
		}
	}()

	// Take the same route a real video publish takes, so a failure here is a
	// real publish failure and not an artefact of the probe.
	action, aerr := NewPublishVideoAction(page)
	if aerr != nil {
		say("NewPublishVideoAction failed: %v", aerr)
		say("url=%s", pageURL(page))
		say("snapshot=%s", publishPageSnapshot(page))
		return b.String(), aerr
	}
	pp := action.page.Context(ctx)
	say("video tab ready, url=%s", pageURL(pp))

	say("== file inputs before upload: %d", countSel(pp, "input[type='file']"))
	if uerr := uploadVideo(pp, videoPath); uerr != nil {
		say("uploadVideo failed: %v", uerr)
		return b.String(), uerr
	}
	say("== video uploaded")
	time.Sleep(6 * time.Second)

	coverContainerJS := `() => {
	  const hit = [...document.querySelectorAll('*')].find(
	    e => e.children.length === 0 && (e.textContent||'').trim() === '设置封面');
	  if (!hit) return null;
	  let n = hit;
	  for (let i = 0; i < 6 && n.parentElement; i++) n = n.parentElement;
	  return n;
	}`

	say("\n== cover section outerHTML (trimmed)")
	say("%s", evalString(pp, `() => { const f = `+coverContainerJS+`; const n = f();
	  return n ? n.outerHTML.slice(0, 6000) : '<<设置封面 heading not found>>'; }`))

	say("\n== elements inside the cover section")
	say("%s", evalString(pp, `() => { const f = `+coverContainerJS+`; const n = f();
	  if (!n) return 'no cover section';
	  return [...n.querySelectorAll('img,button,input,[class*=cover],[class*=upload]')]
	    .slice(0, 40)
	    .map(e => e.tagName + ' class=' + JSON.stringify(e.className.toString()) +
	         (e.getAttribute('type') ? ' type=' + e.getAttribute('type') : '') +
	         ' text=' + JSON.stringify((e.textContent||'').trim().slice(0, 30)))
	    .join('\n'); }`))

	say("\n== clicking the cover thumbnail")
	say("%s", evalString(pp, `() => { const f = `+coverContainerJS+`; const n = f();
	  if (!n) return 'no cover section';
	  const img = n.querySelector('img');
	  if (!img) return 'no img in cover section';
	  const t = img.closest('div') || img;
	  t.click();
	  return 'clicked <' + t.tagName + ' class=' + JSON.stringify(t.className.toString()) + '>'; }`))
	time.Sleep(5 * time.Second)

	say("\n== file inputs after opening the editor: %d", countSel(pp, "input[type='file']"))
	say("\n== visible modal / drawer DOM (trimmed)")
	say("%s", evalString(pp, `() => {
	  const sel = '.d-modal,.d-drawer,[class*=modal],[class*=dialog],[role=dialog],[class*=drawer]';
	  const ms = [...document.querySelectorAll(sel)].filter(m => m.offsetParent !== null);
	  if (!ms.length) return '<<no visible modal>>';
	  return ms.map(m => m.outerHTML.slice(0, 8000)).join('\n---- next ----\n');
	}`))

	say("\n== visible clickable labels on the page")
	say("%s", evalString(pp, `() => [...document.querySelectorAll('button,[role=button],[class*=tab],[class*=btn]')]
	  .filter(e => e.offsetParent !== null)
	  .map(e => JSON.stringify((e.textContent||'').trim().slice(0, 24)) + ' class=' + JSON.stringify(e.className.toString().slice(0,70)))
	  .filter((v,i,a) => a.indexOf(v) === i)
	  .slice(0, 70).join('\n')`))

	return b.String(), nil
}

func pageURL(page *rod.Page) string {
	info, err := page.Info()
	if err != nil {
		return "unknown(" + err.Error() + ")"
	}
	return info.URL
}

func countSel(page *rod.Page, sel string) int {
	els, err := page.Elements(sel)
	if err != nil {
		return -1
	}
	return len(els)
}

func evalString(page *rod.Page, js string) string {
	res, err := page.Eval(js)
	if err != nil {
		return "eval error: " + err.Error()
	}
	return res.Value.String()
}

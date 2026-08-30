//go:build integration

package xiaohongshu

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
)

const securityQRFixture = `
<style>
  .r-captcha-modal { position: absolute; inset: 20px; width: 420px; height: 440px; }
  .qrcode-container, .qrcode-container img { display: block; width: 240px; height: 240px; }
</style>
<div class="login-container"><img class="qrcode-img" style="display:none"></div>
<div class="r-captcha-modal">
  <div class="captcha-modal-content">
    <h2>请通过验证</h2>
    <p>为保护账号安全，请使用已登录该账号的小红书APP扫码验证身份</p>
    <div class="qrcode-container">
      <img alt="security qr" src="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='240' height='240'%3E%3Crect width='240' height='240' fill='white'/%3E%3Cpath d='M20 20h80v80H20zM140 20h80v80h-80zM20 140h80v80H20z' fill='black'/%3E%3C/svg%3E">
    </div>
  </div>
</div>`

func TestObserveAndCaptureAccountSecurityQR(t *testing.T) {
	bin, err := browser.EnsureBrowser()
	if err != nil {
		t.Skipf("SKIP: browser unavailable: %v", err)
	}

	controlURL := launcher.New().Bin(bin).Headless(true).MustLaunch()
	browserInstance := rod.New().ControlURL(controlURL).MustConnect()
	defer browserInstance.MustClose()

	page := browserInstance.MustPage("about:blank")
	page.MustSetViewport(800, 600, 1, false)
	page.MustSetDocumentContent(securityQRFixture)
	page.MustWaitLoad()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	action := NewLogin(page)

	observation, err := action.ObserveLoginState(ctx)
	if err != nil {
		t.Fatalf("observe security QR: %v", err)
	}
	if observation.State != LoginPageCaptchaNeeded {
		t.Fatalf("state = %q, want %q", observation.State, LoginPageCaptchaNeeded)
	}

	image, err := action.CaptureSecurityVerification(ctx)
	if err != nil {
		t.Fatalf("capture security QR: %v", err)
	}
	if len(image) < 8 || !bytes.Equal(image[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		t.Fatalf("capture did not return a PNG image (%d bytes)", len(image))
	}
}

package xiaohongshu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"github.com/sirupsen/logrus"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	rodinput "github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/pkg/errors"
)

type LoginAction struct {
	page *rod.Page
	// WaitForLoginWithState polls the page while SubmitVerificationCode may be
	// called by another HTTP/MCP request. Serialize those CDP operations so the
	// code field cannot be edited while the page is being inspected or closed.
	mu sync.Mutex

	trace *publishTrace
	// scanSeen latches once the QR was scanned. Xiaohongshu may then redirect
	// the page (rednote.com -> xiaohongshu.com), which wipes the "scanned"
	// markers; the OTP prompt that follows must still be classified as OTP.
	scanSeen bool
	// otpSeen latches once the SMS/OTP prompt has appeared, so a later return to
	// the QR login screen can be recognised as a rejected code and auto-retried.
	otpSeen bool
}

// Capture stores a screenshot of the login page under the profile's debug
// directory. A QR login that "succeeds" but leaves a session Xiaohongshu will
// not honour looks identical to a real one in the logs; the frames are the
// evidence.
func (a *LoginAction) Capture(step string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.captureLocked(step)
}

// captureLocked is Capture for callers that already hold a.mu.
func (a *LoginAction) captureLocked(step string) {
	if a.trace == nil {
		a.trace = newPublishTrace("login")
	}
	a.trace.Capture(a.page, step)
}

// ErrVerificationCodeSendNotFound means no visible "send/resend code" control
// exists on the page (the SMS modal is not open).
var ErrVerificationCodeSendNotFound = errors.New("verification code send control not found")

// ErrVerificationCodeCooldown means the resend control is counting down.
var ErrVerificationCodeCooldown = errors.New("verification code resend is cooling down")

// RequestVerificationCode clicks the SMS modal's send/resend control
// (发送验证码 / 获取验证码 / 重新获取). Xiaohongshu sends the first code when the
// modal opens, so this mostly serves "I never received it". Returns the label
// that was clicked and the masked phone number shown in the modal, if any.
func (a *LoginAction) RequestVerificationCode(ctx context.Context) (clicked string, phone string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	pp := a.page.Context(ctx).Timeout(15 * time.Second)
	a.captureLocked("otp_resend_before")
	res, evalErr := pp.Eval(`() => {
		const visible = (el) => {
			if (!el) return false;
			const style = window.getComputedStyle(el);
			const rect = el.getBoundingClientRect();
			return style.display !== "none" && style.visibility !== "hidden" &&
				Number(style.opacity || "1") !== 0 && rect.width > 0 && rect.height > 0;
		};
		const inLoginForm = (el) => !!el.closest('.login-container, .login-modal, [class*="login-container"]');
		const modal = Array.from(document.querySelectorAll('.r-captcha-modal, [class*="captcha-modal"]')).find(visible) || document.body;
		const phone = ((modal.innerText || "").match(/(\*{3,}\d{2,4}|\+?\d[\d\s-]{6,}\d)/) || [""])[0];
		const candidates = Array.from(modal.querySelectorAll('a, button, span, div, [role="button"]'))
			.filter((el) => visible(el) && !inLoginForm(el) && el.children.length <= 1)
			.map((el) => ({ el, text: (el.innerText || el.textContent || "").trim() }))
			.filter(({ text }) => text.length <= 12 && /重新获取|重新发送|重发|获取验证码|发送验证码|resend|send code/i.test(text));
		if (!candidates.length) return JSON.stringify({ found: false, phone });
		const target = candidates[0];
		const cooldown = /\d+\s*(s|秒)/.test(target.text);
		if (!cooldown) target.el.click();
		return JSON.stringify({ found: true, text: target.text, cooldown, phone });
	}`)
	if evalErr != nil {
		return "", "", errors.Wrap(evalErr, "inspect verification code controls")
	}
	var out struct {
		Found    bool   `json:"found"`
		Text     string `json:"text"`
		Cooldown bool   `json:"cooldown"`
		Phone    string `json:"phone"`
	}
	if jsonErr := json.Unmarshal([]byte(res.Value.Str()), &out); jsonErr != nil {
		return "", "", errors.Wrap(jsonErr, "decode verification code controls")
	}
	if !out.Found {
		a.captureLocked("otp_resend_not_found")
		return "", out.Phone, ErrVerificationCodeSendNotFound
	}
	if out.Cooldown {
		a.captureLocked("otp_resend_cooldown")
		return out.Text, out.Phone, errors.Wrapf(ErrVerificationCodeCooldown, "%s", out.Text)
	}
	time.Sleep(800 * time.Millisecond)
	a.captureLocked("otp_resend_clicked")
	logrus.Infof("已点击验证码发送控件: %q phone=%q", out.Text, out.Phone)
	return out.Text, out.Phone, nil
}

// SessionFacts describes where a login actually landed. Xiaohongshu moves
// accounts it classifies as overseas ("holder country" outside CN) to the
// international brand rednote.com after the scan: the browser then holds an
// id_token on .rednote.com while .xiaohongshu.com keeps its guest web_session,
// and neither www.xiaohongshu.com nor creator.xiaohongshu.com will sign in.
type SessionFacts struct {
	XHSCookieNames []string
	XHSWebSession  string // short hash, never the value
	RednoteIDToken bool
	RednoteWebSess string // short hash
	HolderCountry  string // x-rednote-holderctry
	DataCountry    string // x-rednote-datactry
	CurrentURLHost string
}

// RoutedToRednote reports whether the login produced only an international
// (rednote.com) session.
func (f SessionFacts) RoutedToRednote() bool {
	return f.RednoteIDToken || strings.HasSuffix(f.CurrentURLHost, "rednote.com")
}

func (f SessionFacts) String() string {
	return fmt.Sprintf("host=%s xhs_web_session=%s rednote_id_token=%v rednote_web_session=%s holderctry=%q datactry=%q names=%v",
		f.CurrentURLHost, f.XHSWebSession, f.RednoteIDToken, f.RednoteWebSess, f.HolderCountry, f.DataCountry, f.XHSCookieNames)
}

// SessionCookieDigest reports cookie facts from the login browser using short
// hashes only, so a log can show whether the session cookie changed across the
// scan and whether the login was routed to rednote.com.
func (a *LoginAction) SessionCookieDigest() (SessionFacts, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var facts SessionFacts
	cks, err := a.page.Browser().GetCookies()
	if err != nil {
		return facts, err
	}
	if info, infoErr := a.page.Info(); infoErr == nil {
		if u, parseErr := url.Parse(info.URL); parseErr == nil {
			facts.CurrentURLHost = u.Host
		}
	}
	short := func(v string) string {
		sum := sha256.Sum256([]byte(v))
		return hex.EncodeToString(sum[:])[:8]
	}
	for _, c := range cks {
		switch {
		case strings.Contains(c.Domain, "xiaohongshu.com"):
			facts.XHSCookieNames = append(facts.XHSCookieNames, c.Name)
			if c.Name == "web_session" {
				facts.XHSWebSession = short(c.Value)
			}
		case strings.Contains(c.Domain, "rednote.com"):
			switch c.Name {
			case "id_token":
				facts.RednoteIDToken = true
			case "web_session":
				facts.RednoteWebSess = short(c.Value)
			case "x-rednote-holderctry":
				facts.HolderCountry = c.Value
			case "x-rednote-datactry":
				facts.DataCountry = c.Value
			}
		}
	}
	sort.Strings(facts.XHSCookieNames)
	return facts, nil
}

var (
	verificationCodePattern           = regexp.MustCompile(`^[0-9]{6}$`)
	ErrInvalidVerificationCode        = stderrors.New("verification code must contain exactly 6 digits")
	ErrVerificationCodeInputNotFound  = stderrors.New("verification code input is not available")
	ErrVerificationCodeSubmitNotFound = stderrors.New("verification code submit button is not available")
)

// LoginPageState describes the observable state of the retained Xiaohongshu
// login page. It deliberately distinguishes QR-scanned OTP from the phone-login
// OTP field that is already present before a QR scan.
type LoginPageState string

const (
	LoginPageWaitingForScan LoginPageState = "waiting_for_scan"
	LoginPageQRScanned      LoginPageState = "qr_scanned"
	LoginPageOTPRequired    LoginPageState = "otp_required"
	LoginPageCaptchaNeeded  LoginPageState = "captcha_required"
	LoginPageAuthenticated  LoginPageState = "authenticated"
)

type LoginObservation struct {
	State   LoginPageState `json:"state"`
	Message string         `json:"message,omitempty"`
	// Debug is a compact description of the visible inputs/modals and the page
	// host, for logs only.
	Debug string `json:"debug,omitempty"`
}

// loginDOMObservation contains facts collected from the page. Keeping the
// state priority in Go makes the OTP/CAPTCHA overlap deterministic and easy to
// regression-test: Xiaohongshu's SMS modal is named "r-captcha-modal" even
// though it is an OTP prompt rather than an interactive CAPTCHA challenge.
type loginDOMObservation struct {
	Authenticated     bool   `json:"authenticated"`
	CaptchaVisible    bool   `json:"captchaVisible"`
	SecurityQRVisible bool   `json:"securityQRVisible"`
	QRScanned         bool   `json:"qrScanned"`
	QRVisible         bool   `json:"qrVisible"`
	SMSModalVisible   bool   `json:"smsModalVisible"`
	OTPVisible        bool   `json:"otpVisible"`
	Message           string `json:"message,omitempty"`
	Debug             string `json:"debug,omitempty"`
}

func NewLogin(page *rod.Page) *LoginAction {
	return &LoginAction{page: page}
}

// ValidateVerificationCode validates without ever returning the supplied code
// in an error. Keep it a string so leading zeroes survive.
func ValidateVerificationCode(code string) error {
	if !verificationCodePattern.MatchString(strings.TrimSpace(code)) {
		return ErrInvalidVerificationCode
	}
	return nil
}

func classifyLoginDOMObservation(dom loginDOMObservation) LoginObservation {
	switch {
	case dom.Authenticated:
		return LoginObservation{State: LoginPageAuthenticated}
	case dom.SecurityQRVisible:
		// Xiaohongshu mounts both SMS verification and account-security QR
		// challenges inside .r-captcha-modal. The visible security QR is a
		// distinct second scan step and must win over the generic modal name.
		return LoginObservation{State: LoginPageCaptchaNeeded, Message: dom.Message}
	case dom.OTPVisible && dom.QRScanned:
		return LoginObservation{State: LoginPageOTPRequired, Message: dom.Message}
	case dom.SMSModalVisible && dom.QRScanned:
		// The SMS modal mounts before its input. During that brief window its
		// captcha-named CSS must remain a scanned transition, not a terminal
		// interactive CAPTCHA.
		return LoginObservation{State: LoginPageQRScanned}
	case dom.CaptchaVisible:
		return LoginObservation{State: LoginPageCaptchaNeeded, Message: dom.Message}
	case dom.QRScanned:
		return LoginObservation{State: LoginPageQRScanned}
	default:
		return LoginObservation{State: LoginPageWaitingForScan}
	}
}

func isVerificationSubmitText(text string) bool {
	normalized := strings.Join(strings.Fields(text), "")
	return normalized == "验证" || normalized == "确认" || normalized == "提交"
}

func verificationCodeKeys(code string) []rodinput.Key {
	digitKeys := map[rune]rodinput.Key{
		'0': rodinput.Digit0,
		'1': rodinput.Digit1,
		'2': rodinput.Digit2,
		'3': rodinput.Digit3,
		'4': rodinput.Digit4,
		'5': rodinput.Digit5,
		'6': rodinput.Digit6,
		'7': rodinput.Digit7,
		'8': rodinput.Digit8,
		'9': rodinput.Digit9,
	}

	keys := make([]rodinput.Key, 0, len(code))
	for _, digit := range code {
		keys = append(keys, digitKeys[digit])
	}
	return keys
}

func verificationSubmitEnabled(className string, disabled, ariaDisabled *string) bool {
	for _, class := range strings.Fields(className) {
		if class == "btn-disabled" || class == "disabled" {
			return false
		}
	}

	for _, attr := range []*string{disabled, ariaDisabled} {
		if attr == nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(*attr)) {
		case "", "true", "disabled":
			return false
		}
	}
	return true
}

func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
	// 加超时保护：只是查登录态的快速检查，不应无限挂（登录扫码的等待在 Login/WaitForLogin 里）
	pp := a.page.Context(ctx).Timeout(30 * time.Second)
	trace := newPublishTrace("login-check")
	trace.AttachNetwork(pp)
	pp.MustNavigate(CurrentSite().HomeURL).MustWaitLoad()

	time.Sleep(1 * time.Second)

	exists, _, err := pp.Has(`.main-container .user .link-wrapper .channel`)
	if err != nil {
		trace.Capture(pp, "explore_check_error")
		return false, errors.Wrap(err, "check login status failed")
	}

	// Record what the check actually saw: a bounced session (rednote.com,
	// login or security-verification page) fails here with the same generic
	// message as a plain logout, and the screenshot is the only way to tell.
	finalURL := "unknown"
	if info, infoErr := pp.Info(); infoErr == nil {
		finalURL = info.URL
	}
	logrus.Infof("登录状态检查: url=%s user_element=%v", finalURL, exists)

	if !exists {
		trace.Capture(pp, "explore_not_logged_in")
		return false, errors.Errorf("login status element not found (url=%s)", finalURL)
	}

	trace.Capture(pp, "explore_logged_in")
	return true, nil
}

// CheckCreatorLoginStatus verifies the creator-center session used by publish
// operations. A normal www.xiaohongshu.com session can remain valid after the
// creator session has started redirecting to /login with redirectReason=401.
func (a *LoginAction) CheckCreatorLoginStatus(ctx context.Context) (bool, error) {
	pp := a.page.Context(ctx).Timeout(15 * time.Second)
	if err := pp.Navigate(CurrentSite().PublishURL); err != nil {
		return false, errors.Wrap(err, "navigate to creator publish page failed")
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err := checkCreatorSession(pp); err != nil {
			if errors.Is(err, ErrCreatorSessionExpired) {
				return false, nil
			}
			return false, err
		}

		exists, _, err := pp.Has(`div.upload-content`)
		if err == nil && exists {
			return true, nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	return false, errors.New("creator publish page did not expose an authenticated upload panel")
}

// CurrentUser 当前登录用户的基础信息。
type CurrentUser struct {
	Nickname string `json:"nickname"`
	UserID   string `json:"userId"`
}

// CurrentUser 从当前页面的 __INITIAL_STATE__ 读取登录用户信息。
// 需在 CheckLoginStatus 之后调用：复用已加载的 explore 页，不做额外导航。
func (a *LoginAction) CurrentUser(ctx context.Context) (*CurrentUser, error) {
	pp := a.page.Context(ctx).Timeout(10 * time.Second)

	res, err := pp.Eval(`() => {
		const u = window.__INITIAL_STATE__ && window.__INITIAL_STATE__.user;
		const info = u && u.userInfo && u.userInfo.value !== undefined ? u.userInfo.value : (u && u.userInfo);
		if (!info || info.guest) return "";
		return JSON.stringify({nickname: info.nickname, userId: info.userId || info.user_id});
	}`)
	if err != nil {
		return nil, errors.Wrap(err, "read current user state failed")
	}

	raw := res.Value.String()
	if raw == "" {
		return nil, errors.New("current user not found in page state")
	}

	var user CurrentUser
	if err := json.Unmarshal([]byte(raw), &user); err != nil {
		return nil, errors.Wrap(err, "unmarshal current user failed")
	}

	return &user, nil
}

func (a *LoginAction) Login(ctx context.Context) error {
	pp := a.page.Context(ctx)

	// 导航到小红书首页，这会触发二维码弹窗
	pp.MustNavigate(CurrentSite().HomeURL).MustWaitLoad()

	time.Sleep(2 * time.Second)

	if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
		return nil
	}

	pp.MustElement(".main-container .user .link-wrapper .channel")

	return nil
}

func (a *LoginAction) FetchQrcodeImage(ctx context.Context) (string, bool, error) {
	pp := a.page.Context(ctx)

	// 导航到小红书首页，这会触发二维码弹窗
	pp.MustNavigate(CurrentSite().HomeURL).MustWaitLoad()

	time.Sleep(2 * time.Second)

	if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
		return "", true, nil
	}

	src, err := pp.MustElement(".login-container .qrcode-img").Attribute("src")
	if err != nil {
		return "", false, errors.Wrap(err, "get qrcode src failed")
	}
	if src == nil || len(*src) == 0 {
		return "", false, errors.New("qrcode src is empty")
	}

	return *src, false, nil
}

func (a *LoginAction) WaitForLogin(ctx context.Context) bool {
	return a.WaitForLoginWithState(ctx, nil)
}

// WaitForLoginWithState waits for a terminal logged-in state and reports QR,
// OTP, and CAPTCHA transitions to the owner of the retained browser session.
func (a *LoginAction) WaitForLoginWithState(ctx context.Context, observe func(LoginObservation)) bool {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			observation, err := a.ObserveLoginState(ctx)
			if err != nil {
				continue
			}
			if observation.State == LoginPageAuthenticated {
				return true
			}
			if observe != nil {
				observe(observation)
			}
		}
	}
}

// ObserveLoginState inspects the visible login UI. The phone login form always
// contains an OTP field, so OTP is considered required only after the QR is no
// longer visible or the page exposes a scanned marker/text.
func (a *LoginAction) ObserveLoginState(ctx context.Context) (LoginObservation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	pp := a.page.Context(ctx).Timeout(5 * time.Second)
	result, err := pp.Eval(`() => {
		const visible = (el) => {
			if (!el) return false;
			const style = window.getComputedStyle(el);
			const rect = el.getBoundingClientRect();
			return style.display !== "none" && style.visibility !== "hidden" &&
				Number(style.opacity || "1") !== 0 && rect.width > 0 && rect.height > 0;
		};

		const authenticated = !!document.querySelector(
			".main-container .user .link-wrapper .channel"
		);

		const bodyText = (document.body && document.body.innerText) || "";
		const scannedText = /扫码成功|已扫码|请在(?:小红书)?App(?:内)?确认|请在手机上确认/.test(bodyText);
		const scannedMarker = document.querySelector(
			'[data-status="scan_success"], [class*="scan-success"], [class*="scan_success"]'
		);
		const qr = document.querySelector(".login-container .qrcode-img");
		const qrScanned = scannedText || !!scannedMarker || (!!qr && !visible(qr));

		const smsModal = Array.from(document.querySelectorAll(".r-captcha-modal")).find(visible);
		const securityQR = Array.from(document.querySelectorAll(".r-captcha-modal")).find((el) => {
			if (!visible(el)) return false;
			const text = (el.innerText || el.textContent || "").trim();
			return text.includes("请通过验证") &&
				(text.includes("保护账号安全") || text.includes("扫码验证身份")) &&
				Boolean(el.querySelector(
					"img, canvas, svg, [class*=qrcode], [class*=qr-code], [style*=background-image]"
				));
		});
		const otpScoped = Array.from(document.querySelectorAll(
			'.r-captcha-modal input[placeholder*="验证码"], ' +
			'.r-captcha-modal input[autocomplete="one-time-code"], ' +
			'.r-captcha-modal input[maxlength="6"]'
		)).find(visible);
		// The SMS prompt is not always mounted under .r-captcha-modal (the page
		// may have been redirected to the other brand after the scan). Accept a
		// visible code input anywhere EXCEPT the login modal's own phone-number
		// form, which always shows a 输入验证码 field.
		const inLoginForm = (el) => !!el.closest('.login-container, .login-modal, [class*="login-container"]');
		const otpLoose = Array.from(document.querySelectorAll(
			'input[placeholder*="验证码"], input[autocomplete="one-time-code"], input[maxlength="6"]'
		)).find((el) => visible(el) && !inLoginForm(el));
		const otp = otpScoped || otpLoose;
		const dbgInputs = Array.from(document.querySelectorAll("input")).filter(visible).map((el) =>
			(el.placeholder || el.getAttribute("aria-label") || el.name || el.type || "?") + (inLoginForm(el) ? "[login-form]" : "")
		).slice(0, 10);
		const dbgModals = Array.from(document.querySelectorAll('[class*="modal"], [class*="captcha"], [class*="dialog"], [role="dialog"]'))
			.filter(visible).map((el) => (el.className || "").toString().split(" ").filter(Boolean).slice(0, 3).join(".")).slice(0, 8);
		const debug = location.host + " inputs=" + JSON.stringify(dbgInputs) + " modals=" + JSON.stringify(dbgModals);
		const captcha = Array.from(document.querySelectorAll(
			'iframe[src*="captcha"], [class*="captcha"], [id*="captcha"]'
		)).find((el) => {
			if (!visible(el)) return false;
			// Xiaohongshu calls its SMS-code dialog a captcha modal. Exclude
			// that exact modal while still detecting a separate challenge.
			if (!smsModal) return true;
			return !(
				el === smsModal || smsModal.contains(el) || el.contains(smsModal)
			);
		});
		const errorNode = Array.from(document.querySelectorAll(
			'.r-captcha-modal .error-box, .r-captcha-modal .err-msg, ' +
			'.r-captcha-modal [class*="error-message"]'
		)).find(visible);
		const message = errorNode ? (errorNode.textContent || "").trim() : "";
		// The login QR is the active surface (not merely mounted behind a modal)
		// only when no SMS/OTP modal is showing over it.
		const qrVisible = !!qr && visible(qr) && !smsModal && !otp;
		return JSON.stringify({
			authenticated,
			captchaVisible: !!captcha,
			securityQRVisible: !!securityQR,
			qrScanned,
			qrVisible,
			smsModalVisible: !!smsModal,
			otpVisible: !!otp,
			message,
			debug,
		});
	}`)
	if err != nil {
		return LoginObservation{}, errors.Wrap(err, "inspect login page state failed")
	}

	var dom loginDOMObservation
	if err := json.Unmarshal([]byte(result.Value.Str()), &dom); err != nil {
		return LoginObservation{}, errors.Wrap(err, "decode login page state failed")
	}
	if dom.OTPVisible {
		a.otpSeen = true
	}

	if a.otpSeen && dom.QRVisible && !dom.Authenticated {
		// The SMS/OTP prompt was shown, then the page fell back to the QR login
		// screen without authenticating — the code was rejected or expired.
		// Release the scan+OTP latches so the state returns to waiting_for_scan
		// and the login auto-iterates: the fresh QR can be re-scanned and a new
		// code entered, instead of stalling at qr_scanned. Logged once per
		// revert (the latches gate re-entry).
		logrus.Infof("验证码提交后回到二维码登录界面（未登录），重置为等待扫码并自动重试")
		a.scanSeen = false
		a.otpSeen = false
		dom.QRScanned = false
	} else {
		if dom.QRScanned {
			a.scanSeen = true
		}
		dom.QRScanned = dom.QRScanned || a.scanSeen
	}
	obs := classifyLoginDOMObservation(dom)
	obs.Debug = dom.Debug
	return obs, nil
}

// CaptureSecurityVerification refreshes an expired account-security QR inside
// the retained login page and returns only the visible challenge panel. It does
// not create a new browser or replace the active login session.
func (a *LoginAction) CaptureSecurityVerification(ctx context.Context) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	pp := a.page.Context(ctx).Timeout(10 * time.Second)
	refreshed, err := pp.Eval(`() => {
		const visible = (el) => {
			if (!el) return false;
			const style = window.getComputedStyle(el);
			const rect = el.getBoundingClientRect();
			return style.display !== "none" && style.visibility !== "hidden" &&
				Number(style.opacity || "1") !== 0 && rect.width > 0 && rect.height > 0;
		};
		const expired = Array.from(document.querySelectorAll("body *")).find((el) =>
			visible(el) && el.children.length === 0 && /已过期.*刷新/.test((el.textContent || "").trim())
		);
		if (!expired) return false;
		const target = expired.closest('button, [role="button"]') || expired;
		target.click();
		return true;
	}`)
	if err != nil {
		return nil, errors.Wrap(err, "inspect expired security QR")
	}
	if refreshed.Value.Bool() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(750 * time.Millisecond):
		}
	}

	_, err = pp.Eval(`() => {
		for (const old of document.querySelectorAll('[data-postiz-security-challenge]')) {
			old.removeAttribute('data-postiz-security-challenge');
		}
		const visible = (el) => {
			if (!el) return false;
			const style = window.getComputedStyle(el);
			const rect = el.getBoundingClientRect();
			return style.display !== "none" && style.visibility !== "hidden" &&
				Number(style.opacity || "1") !== 0 && rect.width >= 180 && rect.height >= 180;
		};
		const candidates = Array.from(document.querySelectorAll("body *")).filter((el) => {
			if (!visible(el)) return false;
			const text = (el.innerText || el.textContent || "").trim();
			return text.includes("请通过验证") &&
				(text.includes("保护账号安全") || text.includes("扫码验证身份")) &&
				Boolean(el.querySelector("img, canvas, svg, [class*=qrcode], [class*=qr-code], [style*=background-image]"));
		});
		candidates.sort((left, right) => {
			const a = left.getBoundingClientRect();
			const b = right.getBoundingClientRect();
			return (a.width * a.height) - (b.width * b.height);
		});
		if (!candidates[0]) return false;
		candidates[0].setAttribute('data-postiz-security-challenge', 'true');
		return true;
	}`)
	if err != nil {
		return nil, errors.Wrap(err, "locate security QR panel")
	}

	challenge, err := pp.Element(`[data-postiz-security-challenge="true"]`)
	if err != nil {
		return nil, errors.Wrap(err, "security QR panel not found")
	}
	defer func() {
		_, _ = a.page.Timeout(2 * time.Second).Eval(`() => {
			for (const element of document.querySelectorAll('[data-postiz-security-challenge]')) {
				element.removeAttribute('data-postiz-security-challenge');
			}
		}`)
	}()
	image, err := challenge.Screenshot(proto.PageCaptureScreenshotFormatPng, 100)
	if err != nil {
		return nil, errors.Wrap(err, "capture security QR panel")
	}
	return image, nil
}

// SubmitVerificationCode fills and submits the OTP on the same page that
// produced the QR code. It never navigates or creates another browser.
func (a *LoginAction) SubmitVerificationCode(ctx context.Context, code string) error {
	code = strings.TrimSpace(code)
	if err := ValidateVerificationCode(code); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	pp := a.page.Context(ctx).Timeout(20 * time.Second)
	inputs, err := pp.Elements(`
		.r-captcha-modal input[placeholder*="验证码"],
		.r-captcha-modal input[autocomplete="one-time-code"],
		.r-captcha-modal input[maxlength="6"],
		input[placeholder*="验证码"],
		input[autocomplete="one-time-code"],
		input[maxlength="6"]
	`)
	if err != nil {
		return errors.Wrap(ErrVerificationCodeInputNotFound, "find verification code input")
	}

	var codeInput *rod.Element
	for _, candidate := range inputs {
		visible, visibleErr := candidate.Visible()
		if visibleErr != nil || !visible || insideLoginForm(candidate) {
			continue
		}
		codeInput = candidate
		break
	}
	if codeInput == nil {
		a.captureLocked("otp_input_not_found")
		return ErrVerificationCodeInputNotFound
	}
	a.captureLocked("otp_before_typing")

	if err := codeInput.SelectAllText(); err != nil {
		return fmt.Errorf("select verification code input: %w", err)
	}
	// Element.Input uses CDP text insertion. Xiaohongshu's Vue OTP component
	// accepts the visible value but leaves its Verify control disabled unless it
	// receives real key-down/key-up events, so type each validated digit through
	// Rod's keyboard API instead.
	if err := pp.Keyboard.Type(rodinput.Backspace); err != nil {
		return fmt.Errorf("clear verification code input: %w", err)
	}
	if err := pp.Keyboard.Type(verificationCodeKeys(code)...); err != nil {
		return fmt.Errorf("fill verification code input: %w", err)
	}
	a.captureLocked("otp_typed")

	// Click the SMS/captcha modal's primary confirm button. Xiaohongshu's modal
	// text varies (验证/确认/确定/登录/下一步/完成) and the button is sometimes
	// icon-only, so instead of matching exact text we locate the visible,
	// enabled, non-close action inside the captcha modal and click it. The JS
	// returns a dump of the candidate buttons so a miss is diagnosable. Some
	// codes auto-submit on the sixth keystroke and dismiss the modal first; that
	// is handled below. Poll for ~6s because the button enables a beat after the
	// code is entered.
	clickJS := `() => {
		const vis = (el) => { if(!el) return false; const s=getComputedStyle(el); const r=el.getBoundingClientRect(); return s.display!=="none"&&s.visibility!=="hidden"&&Number(s.opacity||"1")!==0&&r.width>0&&r.height>0; };
		const modal = Array.from(document.querySelectorAll(".r-captcha-modal, [class*=\"captcha-modal\"]")).find(vis);
		if(!modal) return JSON.stringify({modal:false});
		const isClose = (el)=>/close|关闭/i.test((el.className||"")+" "+(el.getAttribute("aria-label")||""));
		const enabled = (el)=>!el.disabled && el.getAttribute("aria-disabled")!=="true" && !/\bdisabled\b|btn-disabled/.test(el.className||"");
		let cands = Array.from(modal.querySelectorAll("button, [role=button], input[type=button], input[type=submit], [class*=submit], [class*=btn], [class*=button]"))
			.filter(el=>vis(el)&&!isClose(el));
		const dump = cands.map(el=>({t:(el.innerText||el.value||"").trim().slice(0,14), c:(el.className||"").toString().slice(0,50), en:enabled(el)}));
		const enabledCands = cands.filter(enabled);
		let target = enabledCands.find(el=>/验证|确认|确定|提交|登录|下一步|完成|确\s*定/.test(el.innerText||el.value||""));
		if(!target) target = enabledCands[enabledCands.length-1];
		if(!target) return JSON.stringify({modal:true, clicked:false, dump});
		target.click();
		return JSON.stringify({modal:true, clicked:true, text:(target.innerText||target.value||"").trim(), dump});
	}`

	deadline := time.Now().Add(6 * time.Second)
	for {
		res, evalErr := pp.Eval(clickJS)
		if evalErr == nil {
			var out struct {
				Modal   bool `json:"modal"`
				Clicked bool `json:"clicked"`
				Text    string
				Dump    []struct {
					T  string `json:"t"`
					C  string `json:"c"`
					En bool   `json:"en"`
				} `json:"dump"`
			}
			_ = json.Unmarshal([]byte(res.Value.Str()), &out)
			if out.Clicked {
				logrus.Infof("验证码提交：点击确认按钮 text=%q candidates=%+v", out.Text, out.Dump)
				time.Sleep(1500 * time.Millisecond)
				a.captureLocked("otp_submitted")
				return nil
			}
			if len(out.Dump) > 0 {
				logrus.Infof("验证码提交：暂无可点击确认按钮 candidates=%+v", out.Dump)
			}
		}

		if time.Now().After(deadline) {
			// Auto-submit path: the modal's code input is gone, so the page
			// already consumed the code. The observer still requires the
			// authenticated marker before saving cookies.
			modalInputs, mErr := pp.Elements(`
				.r-captcha-modal input[placeholder*="验证码"],
				.r-captcha-modal input[autocomplete="one-time-code"],
				.r-captcha-modal input[maxlength="6"]
			`)
			if mErr == nil {
				visibleLeft := false
				for _, mi := range modalInputs {
					if v, e := mi.Visible(); e == nil && v {
						visibleLeft = true
						break
					}
				}
				if !visibleLeft {
					a.captureLocked("otp_submitted_auto")
					return nil
				}
			}
			a.captureLocked("otp_submit_button_not_found")
			return ErrVerificationCodeSubmitNotFound
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// insideLoginForm reports whether an element belongs to the login modal's own
// phone-number form, whose permanent 验证码 field must never be mistaken for
// the post-scan SMS prompt.
func insideLoginForm(el *rod.Element) bool {
	res, err := el.Eval(`() => !!this.closest('.login-container, .login-modal, [class*="login-container"]')`)
	return err == nil && res.Value.Bool()
}

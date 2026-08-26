package xiaohongshu

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"regexp"
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
}

// loginDOMObservation contains facts collected from the page. Keeping the
// state priority in Go makes the OTP/CAPTCHA overlap deterministic and easy to
// regression-test: Xiaohongshu's SMS modal is named "r-captcha-modal" even
// though it is an OTP prompt rather than an interactive CAPTCHA challenge.
type loginDOMObservation struct {
	Authenticated   bool   `json:"authenticated"`
	CaptchaVisible  bool   `json:"captchaVisible"`
	QRScanned       bool   `json:"qrScanned"`
	SMSModalVisible bool   `json:"smsModalVisible"`
	OTPVisible      bool   `json:"otpVisible"`
	Message         string `json:"message,omitempty"`
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
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

	time.Sleep(1 * time.Second)

	exists, _, err := pp.Has(`.main-container .user .link-wrapper .channel`)
	if err != nil {
		return false, errors.Wrap(err, "check login status failed")
	}

	if !exists {
		return false, errors.Wrap(err, "login status element not found")
	}

	return true, nil
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
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

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
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

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
		const otp = Array.from(document.querySelectorAll(
			'.r-captcha-modal input[placeholder*="验证码"], ' +
			'.r-captcha-modal input[autocomplete="one-time-code"], ' +
			'.r-captcha-modal input[maxlength="6"]'
		)).find(visible);
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
		return JSON.stringify({
			authenticated,
			captchaVisible: !!captcha,
			qrScanned,
			smsModalVisible: !!smsModal,
			otpVisible: !!otp,
			message,
		});
	}`)
	if err != nil {
		return LoginObservation{}, errors.Wrap(err, "inspect login page state failed")
	}

	var dom loginDOMObservation
	if err := json.Unmarshal([]byte(result.Value.Str()), &dom); err != nil {
		return LoginObservation{}, errors.Wrap(err, "decode login page state failed")
	}
	return classifyLoginDOMObservation(dom), nil
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
		.r-captcha-modal input[maxlength="6"]
	`)
	if err != nil {
		return errors.Wrap(ErrVerificationCodeInputNotFound, "find verification code input")
	}

	var codeInput *rod.Element
	for _, candidate := range inputs {
		visible, visibleErr := candidate.Visible()
		if visibleErr == nil && visible {
			codeInput = candidate
			break
		}
	}
	if codeInput == nil {
		return ErrVerificationCodeInputNotFound
	}

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

	buttonDeadline := time.Now().Add(3 * time.Second)
	for {
		buttons, buttonsErr := pp.Elements(`
			.r-captcha-modal button,
			.r-captcha-modal [role="button"],
			.r-captcha-modal input[type="button"],
			.r-captcha-modal input[type="submit"],
			.r-captcha-modal [class*="submit"],
			.r-captcha-modal [class*="button"],
			.r-captcha-modal [class*="btn"],
			.login-container button.submit,
			button.submit
		`)
		if buttonsErr == nil {
			for _, button := range buttons {
				visible, visibleErr := button.Visible()
				if visibleErr != nil || !visible {
					continue
				}
				text, textErr := button.Text()
				if textErr != nil {
					continue
				}
				if value, valueErr := button.Attribute("value"); valueErr == nil && value != nil && strings.TrimSpace(text) == "" {
					text = *value
				}
				if !isVerificationSubmitText(text) {
					continue
				}

				className := ""
				if class, classErr := button.Attribute("class"); classErr == nil && class != nil {
					className = *class
				}
				disabled, _ := button.Attribute("disabled")
				ariaDisabled, _ := button.Attribute("aria-disabled")
				if !verificationSubmitEnabled(className, disabled, ariaDisabled) {
					continue
				}

				if err := button.Click(proto.InputMouseButtonLeft, 1); err != nil {
					return fmt.Errorf("submit verification code: %w", err)
				}
				return nil
			}
		}

		if time.Now().After(buttonDeadline) {
			// The live Xiaohongshu SMS component auto-submits some valid codes
			// as soon as the sixth trusted key event arrives. In that path the
			// modal disappears before there is an enabled button to click. Treat
			// disappearance as a successful page submission; the background
			// observer still requires the authenticated marker before saving any
			// cookies or marking the session authenticated.
			modalInputs, modalInputsErr := pp.Elements(`
				.r-captcha-modal input[placeholder*="验证码"],
				.r-captcha-modal input[autocomplete="one-time-code"],
				.r-captcha-modal input[maxlength="6"]
			`)
			if modalInputsErr == nil {
				modalInputVisible := false
				for _, modalInput := range modalInputs {
					visible, visibleErr := modalInput.Visible()
					if visibleErr == nil && visible {
						modalInputVisible = true
						break
					}
				}
				if !modalInputVisible {
					return nil
				}
			}
			if buttonsErr != nil {
				return errors.Wrap(ErrVerificationCodeSubmitNotFound, "find enabled verification code submit button")
			}
			return ErrVerificationCodeSubmitNotFound
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

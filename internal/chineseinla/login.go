package chineseinla

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const (
	loginSessionLifetime = 5 * time.Minute
	maxLoginAttempts     = 3
	loginPageCreateLimit = 5 * time.Second
	loginDOMOperationMax = 10 * time.Second
	loginPageCloseLimit  = 2 * time.Second
)

var (
	ErrInvalidLoginRequest   = errors.New("invalid ChineseInLA login request")
	ErrLoginSessionNotFound  = errors.New("ChineseInLA login session not found")
	ErrLoginSessionExpired   = errors.New("ChineseInLA login session expired")
	ErrLoginAttemptsExceeded = errors.New("ChineseInLA login attempt limit reached")
)

type LoginSessionState string

const (
	LoginSessionWaitingCredentials LoginSessionState = "waiting_for_credentials"
	LoginSessionSubmitting         LoginSessionState = "submitting"
	LoginSessionInvalidCredentials LoginSessionState = "invalid_credentials"
	LoginSessionCaptchaRequired    LoginSessionState = "captcha_required"
	LoginSessionAuthenticated      LoginSessionState = "authenticated"
	LoginSessionExpired            LoginSessionState = "expired"
	LoginSessionFailed             LoginSessionState = "failed"
)

// LoginSessionStatus is safe to return to API and MCP clients. Screenshot is
// deliberately excluded from JSON so callers can attach it as an image rather
// than accidentally embedding a large payload in logs.
type LoginSessionStatus struct {
	SessionID  string            `json:"session_id"`
	State      LoginSessionState `json:"state"`
	LoggedIn   bool              `json:"logged_in"`
	URL        string            `json:"url"`
	Message    string            `json:"message"`
	ExpiresAt  time.Time         `json:"expires_at"`
	Attempts   int               `json:"attempts"`
	Screenshot []byte            `json:"-"`
}

type PasswordLoginRequest struct {
	SessionID string `json:"session_id"`
	Username  string `json:"username"`
	Password  string `json:"password"`
}

type retainedLoginSession struct {
	id        string
	state     LoginSessionState
	page      *rod.Page
	cancel    context.CancelFunc
	timer     *time.Timer
	expiresAt time.Time
	attempts  int
	url       string
	message   string
}

type loginSessionStore struct {
	mu      sync.Mutex
	current *retainedLoginSession
}

func (a *Automation) StartLogin(_ context.Context) (LoginSessionStatus, error) {
	store := &a.loginSessions
	store.mu.Lock()
	defer store.mu.Unlock()

	store.closeCurrentLocked(LoginSessionExpired, "A newer ChineseInLA login session replaced this one.")

	sessionID, err := newLoginSessionID()
	if err != nil {
		return LoginSessionStatus{}, fmt.Errorf("create ChineseInLA login session: %w", err)
	}
	sessionContext, cancel := context.WithCancel(context.Background())
	browser, err := a.connect(sessionContext)
	if err != nil {
		cancel()
		return LoginSessionStatus{}, err
	}
	target, err := proto.TargetCreateTarget{URL: LoginURL}.Call(browser.Timeout(loginPageCreateLimit))
	if err != nil {
		cancel()
		return LoginSessionStatus{}, fmt.Errorf("open %s: %w", LoginURL, err)
	}
	// CreateTarget is bounded above, but build the retained Page from the
	// long-lived browser context so the creation deadline is not inherited by
	// later status and credential calls.
	type pageResult struct {
		page *rod.Page
		err  error
	}
	pageReady := make(chan pageResult, 1)
	go func() {
		page, pageErr := browser.PageFromTarget(target.TargetID)
		pageReady <- pageResult{page: page, err: pageErr}
	}()
	pageTimer := time.NewTimer(loginPageCreateLimit)
	var page *rod.Page
	select {
	case result := <-pageReady:
		pageTimer.Stop()
		page, err = result.page, result.err
	case <-pageTimer.C:
		cancel()
		return LoginSessionStatus{}, fmt.Errorf("retain %s login page: timed out", LoginURL)
	}
	if err != nil {
		_, _ = proto.TargetCloseTarget{TargetID: target.TargetID}.Call(browser.Timeout(loginPageCloseLimit))
		cancel()
		return LoginSessionStatus{}, fmt.Errorf("retain %s login page: %w", LoginURL, err)
	}
	timeout := a.Config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	// Only the initial navigation gets a timeout-scoped clone. The retained
	// page itself must remain usable for the full login-session lifetime.
	if err := page.Timeout(timeout).WaitLoad(); err != nil {
		_ = page.Timeout(loginPageCloseLimit).Close()
		cancel()
		return LoginSessionStatus{}, fmt.Errorf("wait for %s: %w", LoginURL, err)
	}
	if err := a.waitForLoginFormIfNeeded(page); err != nil {
		_ = page.Timeout(loginPageCloseLimit).Close()
		cancel()
		return LoginSessionStatus{}, err
	}

	session := &retainedLoginSession{
		id:        sessionID,
		state:     LoginSessionWaitingCredentials,
		page:      page,
		cancel:    cancel,
		expiresAt: time.Now().Add(loginSessionLifetime),
		url:       LoginURL,
		message:   "Enter ChineseInLA credentials through the protected password endpoint. Credentials are not logged or echoed.",
	}
	store.current = session
	session.timer = time.AfterFunc(loginSessionLifetime, func() {
		store.mu.Lock()
		defer store.mu.Unlock()
		if store.current == session && store.current.state != LoginSessionAuthenticated {
			store.closeCurrentLocked(LoginSessionExpired, "The ChineseInLA login session expired; start a new one.")
		}
	})

	status, err := a.refreshLoginSessionLocked(session, true)
	if err != nil {
		store.closeCurrentLocked(LoginSessionFailed, "The ChineseInLA login page could not be inspected.")
		store.current = nil
		return LoginSessionStatus{}, err
	}
	return status, nil
}

func (a *Automation) waitForLoginFormIfNeeded(page *rod.Page) error {
	info, err := page.Timeout(a.loginOperationTimeout()).Info()
	if err != nil {
		return fmt.Errorf("identify ChineseInLA login page: %w", err)
	}
	if !strings.Contains(info.URL, "page_login") && !strings.Contains(info.URL, "/login.html") {
		return nil
	}
	readyPage := page.Timeout(a.loginOperationTimeout())
	for _, selector := range []string{
		`input[name="username"]`,
		`input[name="password"]`,
		`input[type="submit"][name="login"]`,
	} {
		if _, err := readyPage.Element(selector); err != nil {
			return fmt.Errorf("wait for ChineseInLA login form %s: %w", selector, err)
		}
	}
	// The remote Linux renderer can fire load before the first complete paint.
	// A short stabilization window prevents returning a mostly blank screenshot.
	time.Sleep(300 * time.Millisecond)
	return nil
}

func (a *Automation) GetLoginSession(_ context.Context, sessionID string) (LoginSessionStatus, error) {
	store := &a.loginSessions
	store.mu.Lock()
	defer store.mu.Unlock()

	session, err := store.lookupLocked(sessionID)
	if err != nil {
		return LoginSessionStatus{}, err
	}
	return a.refreshLoginSessionLocked(session, true)
}

// CloseLoginSession releases the retained login tab when the MCP process shuts
// down. It intentionally leaves Chromium and its isolated profile running so a
// later process can reconnect without exporting cookies.
func (a *Automation) CloseLoginSession() {
	store := &a.loginSessions
	store.mu.Lock()
	defer store.mu.Unlock()
	store.closeCurrentLocked(LoginSessionExpired, "The ChineseInLA server stopped the login session.")
	store.current = nil
}

func (a *Automation) SubmitPasswordLogin(_ context.Context, request PasswordLoginRequest) (LoginSessionStatus, error) {
	if err := request.normalizeAndValidate(); err != nil {
		return LoginSessionStatus{}, err
	}
	defer func() { request.Password = "" }()
	store := &a.loginSessions
	store.mu.Lock()
	defer store.mu.Unlock()

	session, err := store.lookupLocked(request.SessionID)
	if err != nil {
		return LoginSessionStatus{}, err
	}
	if session.state == LoginSessionAuthenticated {
		return session.snapshot(nil), nil
	}
	if session.state == LoginSessionCaptchaRequired {
		status, statusErr := a.refreshLoginSessionLocked(session, true)
		if statusErr != nil {
			return LoginSessionStatus{}, statusErr
		}
		return status, nil
	}
	if session.attempts >= maxLoginAttempts {
		return session.snapshot(nil), ErrLoginAttemptsExceeded
	}
	if session.page == nil {
		return session.snapshot(nil), ErrLoginSessionExpired
	}

	timeout := a.loginOperationTimeout()
	page := session.page.Timeout(timeout)
	usernameField, err := page.Element(`input[name="username"]`)
	if err != nil {
		return LoginSessionStatus{}, fmt.Errorf("find ChineseInLA username field: %w", err)
	}
	passwordField, err := page.Element(`input[name="password"]`)
	if err != nil {
		return LoginSessionStatus{}, fmt.Errorf("find ChineseInLA password field: %w", err)
	}
	if err := replaceElementText(usernameField, request.Username); err != nil {
		return LoginSessionStatus{}, fmt.Errorf("fill ChineseInLA username: %w", err)
	}
	if err := replaceElementText(passwordField, request.Password); err != nil {
		return LoginSessionStatus{}, fmt.Errorf("fill ChineseInLA password: %w", err)
	}
	request.Password = ""

	if checkbox, checkboxErr := page.Element(`input[name="autologin"]`); checkboxErr == nil {
		checked, evalErr := checkbox.Eval(`() => Boolean(this.checked)`)
		if evalErr == nil && !checked.Value.Bool() {
			if clickErr := checkbox.Click(proto.InputMouseButtonLeft, 1); clickErr != nil {
				return LoginSessionStatus{}, fmt.Errorf("enable ChineseInLA automatic login: %w", clickErr)
			}
		}
	}

	if _, err := page.Eval(`() => {
		window.__chineseInLALoginDialog = "";
		window.__chineseInLAOriginalLoginAlert = window.__chineseInLAOriginalLoginAlert || window.alert;
		window.alert = function(message) {
			window.__chineseInLALoginDialog = String(message || "");
		};
	}`); err != nil {
		return LoginSessionStatus{}, fmt.Errorf("install ChineseInLA login response monitor: %w", err)
	}

	submit, err := page.Element(`input[type="submit"][name="login"]`)
	if err != nil {
		return LoginSessionStatus{}, fmt.Errorf("find ChineseInLA login button: %w", err)
	}
	session.state = LoginSessionSubmitting
	session.message = "ChineseInLA is checking the submitted credentials."
	if err := submit.Click(proto.InputMouseButtonLeft, 1); err != nil {
		session.state = LoginSessionFailed
		session.message = "The ChineseInLA login form could not be submitted."
		return LoginSessionStatus{}, fmt.Errorf("submit ChineseInLA login form: %w", err)
	}
	session.attempts++

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, refreshErr := a.refreshLoginSessionLocked(session, false)
		if refreshErr == nil {
			switch status.State {
			case LoginSessionAuthenticated, LoginSessionInvalidCredentials, LoginSessionCaptchaRequired:
				return a.refreshLoginSessionLocked(session, true)
			}
		}
		time.Sleep(250 * time.Millisecond)
	}

	session.state = LoginSessionFailed
	session.message = "ChineseInLA did not complete login before the timeout. Inspect the returned screenshot before retrying."
	return a.refreshLoginSessionLocked(session, true)
}

func (a *Automation) loginOperationTimeout() time.Duration {
	if a.Config.Timeout > 0 && a.Config.Timeout < loginDOMOperationMax {
		return a.Config.Timeout
	}
	return loginDOMOperationMax
}

func (request *PasswordLoginRequest) normalizeAndValidate() error {
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.Username = strings.TrimSpace(request.Username)
	if request.SessionID == "" {
		return fmt.Errorf("%w: session_id is required", ErrInvalidLoginRequest)
	}
	if request.Username == "" {
		return fmt.Errorf("%w: username is required", ErrInvalidLoginRequest)
	}
	if utf8.RuneCountInString(request.Username) > 40 {
		return fmt.Errorf("%w: username must be 40 characters or fewer", ErrInvalidLoginRequest)
	}
	if request.Password == "" {
		return fmt.Errorf("%w: password is required", ErrInvalidLoginRequest)
	}
	if utf8.RuneCountInString(request.Password) > 32 {
		return fmt.Errorf("%w: password must be 32 characters or fewer", ErrInvalidLoginRequest)
	}
	return nil
}

func newLoginSessionID() (string, error) {
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random[:]), nil
}

func (store *loginSessionStore) lookupLocked(sessionID string) (*retainedLoginSession, error) {
	sessionID = strings.TrimSpace(sessionID)
	if store.current == nil || sessionID == "" || store.current.id != sessionID {
		return nil, ErrLoginSessionNotFound
	}
	if time.Now().After(store.current.expiresAt) && store.current.state != LoginSessionAuthenticated {
		store.closeCurrentLocked(LoginSessionExpired, "The ChineseInLA login session expired; start a new one.")
		return store.current, ErrLoginSessionExpired
	}
	return store.current, nil
}

func (store *loginSessionStore) closeCurrentLocked(state LoginSessionState, message string) {
	if store.current == nil {
		return
	}
	if store.current.page != nil {
		_ = store.current.page.Timeout(loginPageCloseLimit).Close()
		store.current.page = nil
	}
	if store.current.cancel != nil {
		store.current.cancel()
		store.current.cancel = nil
	}
	if store.current.timer != nil {
		store.current.timer.Stop()
		store.current.timer = nil
	}
	if store.current.state != LoginSessionAuthenticated {
		store.current.state = state
		store.current.message = message
	}
}

func (a *Automation) refreshLoginSessionLocked(session *retainedLoginSession, includeScreenshot bool) (LoginSessionStatus, error) {
	if session.state == LoginSessionAuthenticated || session.state == LoginSessionExpired {
		return session.snapshot(nil), nil
	}
	if time.Now().After(session.expiresAt) {
		a.loginSessions.closeCurrentLocked(LoginSessionExpired, "The ChineseInLA login session expired; start a new one.")
		return session.snapshot(nil), nil
	}
	if session.page == nil {
		return session.snapshot(nil), ErrLoginSessionExpired
	}

	operationPage := session.page.Timeout(a.loginOperationTimeout())
	loggedIn, currentURL, err := pageLoginStatus(operationPage)
	if err != nil {
		return LoginSessionStatus{}, err
	}
	session.url = currentURL
	if loggedIn {
		session.state = LoginSessionAuthenticated
		session.message = "ChineseInLA login succeeded and the isolated browser profile is ready for headless use."
		if session.page != nil {
			_ = session.page.Timeout(loginPageCloseLimit).Close()
			session.page = nil
		}
		if session.cancel != nil {
			session.cancel()
			session.cancel = nil
		}
		if session.timer != nil {
			session.timer.Stop()
			session.timer = nil
		}
		// A successful status is sufficient proof. Avoid returning a forum-home
		// screenshot that may contain account-specific information.
		return session.snapshot(nil), nil
	}

	if verificationErr := detectHumanVerification(operationPage); errors.Is(verificationErr, ErrCaptcha) {
		session.state = LoginSessionCaptchaRequired
		session.message = "ChineseInLA requires CAPTCHA or human verification. This integration will not bypass it."
	} else if verificationErr != nil {
		return LoginSessionStatus{}, verificationErr
	} else if rejected, rejectionErr := loginRejected(operationPage); rejectionErr != nil {
		return LoginSessionStatus{}, rejectionErr
	} else if rejected {
		session.state = LoginSessionInvalidCredentials
		session.message = "ChineseInLA rejected the username or password. Verify the credentials before retrying."
	} else if session.state == LoginSessionCaptchaRequired {
		session.state = LoginSessionWaitingCredentials
		session.message = "Human verification is no longer visible. Submit credentials again to continue."
	}

	var screenshot []byte
	if includeScreenshot {
		screenshot, err = captureLoginScreenshot(session.page, a.loginOperationTimeout())
		if err != nil {
			return LoginSessionStatus{}, err
		}
	}
	return session.snapshot(screenshot), nil
}

func loginRejected(page *rod.Page) (bool, error) {
	result, err := page.Eval(`() => {
		const selectors = [".topic_alert", "#vcodeMemo", ".login-error", ".error", ".alert"];
		return selectors.some((selector) => Array.from(document.querySelectorAll(selector)).some((element) => {
			const text = String(element.innerText || element.textContent || "").trim();
			return text.length > 0 && (element.offsetWidth || element.offsetHeight || element.getClientRects().length);
		})) || Boolean(String(window.__chineseInLALoginDialog || "").trim());
	}`)
	if err != nil {
		return false, fmt.Errorf("inspect ChineseInLA login response: %w", err)
	}
	return result.Value.Bool(), nil
}

func captureLoginScreenshot(page *rod.Page, timeout time.Duration) ([]byte, error) {
	// Login screenshots are diagnostic, not a credential transport. Hide all
	// input values during capture, then restore the live form for a retry.
	if _, err := page.Timeout(timeout).Eval(`() => {
		const selector = [
			'input:not([type])',
			'input[type="text"]',
			'input[type="password"]',
			'input[type="email"]',
			'input[type="tel"]',
			'input[type="number"]',
			'input[type="search"]',
			'input[type="url"]',
			'textarea'
		].join(',');
		for (const element of document.querySelectorAll(selector)) {
			if (Object.prototype.hasOwnProperty.call(element, "__chineseInLAScreenshotValue")) continue;
			element.__chineseInLAScreenshotValue = element.value;
			element.value = "";
		}
	}`); err != nil {
		return nil, fmt.Errorf("redact ChineseInLA login screenshot: %w", err)
	}
	defer func() {
		_, _ = page.Timeout(loginPageCloseLimit).Eval(`() => {
			for (const element of document.querySelectorAll("input, textarea")) {
				if (!Object.prototype.hasOwnProperty.call(element, "__chineseInLAScreenshotValue")) continue;
				element.value = element.__chineseInLAScreenshotValue;
				delete element.__chineseInLAScreenshotValue;
			}
		}`)
	}()
	data, err := page.Timeout(timeout).Screenshot(true, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		return nil, fmt.Errorf("capture ChineseInLA login screenshot: %w", err)
	}
	return data, nil
}

func (session *retainedLoginSession) snapshot(screenshot []byte) LoginSessionStatus {
	return LoginSessionStatus{
		SessionID:  session.id,
		State:      session.state,
		LoggedIn:   session.state == LoginSessionAuthenticated,
		URL:        session.url,
		Message:    session.message,
		ExpiresAt:  session.expiresAt,
		Attempts:   session.attempts,
		Screenshot: screenshot,
	}
}

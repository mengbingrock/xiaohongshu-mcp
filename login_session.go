package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
)

const maxLoginCodeAttempts = 3

var loginStatusSixDigitPattern = regexp.MustCompile(`[0-9]{6}`)

var (
	ErrLoginSessionNotFound       = errors.New("login session not found")
	ErrLoginSessionClosed         = errors.New("login session is no longer active")
	ErrLoginCodeAttemptsExceeded  = errors.New("too many login code attempts")
	ErrLoginCodeSubmissionPending = errors.New("a login code submission is already in progress")
	ErrLoginCodeNotRequired       = errors.New("the login page is not requesting a verification code")
)

// LoginSessionState 是云端登录浏览器当前所处的阶段。
type LoginSessionState string

const (
	LoginSessionWaitingForScan LoginSessionState = "waiting_for_scan"
	LoginSessionQRScanned      LoginSessionState = "qr_scanned"
	LoginSessionOTPRequired    LoginSessionState = "otp_required"
	LoginSessionSubmittingOTP  LoginSessionState = "submitting_otp"
	LoginSessionOTPSubmitted   LoginSessionState = "otp_submitted"
	LoginSessionCaptchaNeeded  LoginSessionState = "captcha_required"
	LoginSessionAuthenticated  LoginSessionState = "authenticated"
	LoginSessionFailed         LoginSessionState = "failed"
	LoginSessionExpired        LoginSessionState = "expired"
	LoginSessionCancelled      LoginSessionState = "cancelled"
)

// LoginSessionStatus 是可以安全返回给 API/MCP 客户端的会话快照。
// 不包含浏览器、页面、验证码或取消函数。
type LoginSessionStatus struct {
	SessionID      string            `json:"session_id"`
	State          LoginSessionState `json:"state"`
	ExpiresAt      time.Time         `json:"expires_at"`
	Attempts       int               `json:"attempts"`
	LastError      string            `json:"last_error,omitempty"`
	ChallengeImage []byte            `json:"-"`
}

type loginCodeSubmitter func(context.Context, string) error
type loginCodeRequester func(context.Context) (clicked string, phone string, err error)
type loginChallengeCapture func(context.Context) ([]byte, error)

// loginSession 持有一次二维码登录所对应的浏览器操作。
// opMu 保证提交验证码、保存 cookies 和关闭浏览器不会同时操作同一页面。
type loginSession struct {
	seq              uint64
	id               string
	expiresAt        time.Time
	cancel           context.CancelFunc
	requestCode      loginCodeRequester
	submit           loginCodeSubmitter
	captureChallenge loginChallengeCapture
	opMu             sync.Mutex

	state          LoginSessionState
	attempts       int
	lastError      string
	challengeImage []byte
}

func (s *loginSession) snapshot() LoginSessionStatus {
	return LoginSessionStatus{
		SessionID:      s.id,
		State:          s.state,
		ExpiresAt:      s.expiresAt,
		Attempts:       s.attempts,
		LastError:      s.lastError,
		ChallengeImage: append([]byte(nil), s.challengeImage...),
	}
}

func (s *loginSession) withPageOperation(fn func()) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	fn()
}

// loginSessions 管理当前唯一一份「二维码已发出、浏览器仍存活」的登录会话。
// 新会话会取消旧会话；外部只看到不可预测的随机 ID，而不是内部递增序号。
type loginSessions struct {
	mu      sync.Mutex
	seq     uint64
	current *loginSession
}

func randomLoginSessionID() (string, error) {
	var raw [24]byte // 192 bits; opaque and safe to expose as the lookup key.
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (l *loginSessions) start(cancel context.CancelFunc, submit loginCodeSubmitter, expiresAt time.Time) (*loginSession, error) {
	id, err := randomLoginSessionID()
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	previous := l.current
	var previousCancel context.CancelFunc
	if previous != nil && previous.cancel != nil {
		previousCancel = previous.cancel
		previous.cancel = nil
		previous.submit = nil
		previous.state = LoginSessionCancelled
	}

	l.seq++
	session := &loginSession{
		seq:       l.seq,
		id:        id,
		expiresAt: expiresAt,
		cancel:    cancel,
		submit:    submit,
		state:     LoginSessionWaitingForScan,
	}
	l.current = session
	l.mu.Unlock()

	// 取消动作会触发旧 goroutine 的收尾，必须放在管理锁外。
	if previousCancel != nil {
		previousCancel()
	}
	return session, nil
}

// extend pushes a session's expiry out (never in). Used once the QR has been
// scanned: the 4-minute window is sized for the scan, not for waiting on an
// SMS code and typing it, and submitCode refuses codes after expiresAt.
func (l *loginSessions) extend(session *loginSession, until time.Time) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until.After(session.expiresAt) {
		session.expiresAt = until
	}
	return session.expiresAt
}

func (l *loginSessions) observe(session *loginSession, state LoginSessionState, lastError string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.current != session || session.cancel == nil {
		return
	}

	// 提交后页面可能短暂仍呈现验证码框。没有错误文字时不要把状态倒退。
	if session.state == LoginSessionOTPSubmitted && lastError == "" {
		switch state {
		case LoginSessionWaitingForScan, LoginSessionQRScanned, LoginSessionOTPRequired:
			return
		}
	}

	session.state = state
	session.lastError = sanitizeLoginStatusMessage(lastError)
	if state != LoginSessionCaptchaNeeded {
		session.challengeImage = nil
	}
}

func sanitizeLoginStatusMessage(message string) string {
	return loginStatusSixDigitPattern.ReplaceAllString(strings.TrimSpace(message), "[redacted]")
}

func (l *loginSessions) finish(session *loginSession, state LoginSessionState, lastError string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.current != session || session.cancel == nil {
		return
	}
	session.cancel = nil
	session.submit = nil
	session.state = state
	session.lastError = lastError
	session.challengeImage = nil
}

func (l *loginSessions) status(sessionID string) (LoginSessionStatus, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.current == nil || l.current.id != sessionID {
		return LoginSessionStatus{}, ErrLoginSessionNotFound
	}
	return l.current.snapshot(), nil
}

func (l *loginSessions) captureSecurityChallenge(ctx context.Context, sessionID string) (LoginSessionStatus, error) {
	l.mu.Lock()
	if l.current == nil || l.current.id != sessionID {
		l.mu.Unlock()
		return LoginSessionStatus{}, ErrLoginSessionNotFound
	}
	session := l.current
	capture := session.captureChallenge
	shouldCapture := session.cancel != nil && session.state == LoginSessionCaptchaNeeded && capture != nil
	if !shouldCapture {
		status := session.snapshot()
		l.mu.Unlock()
		return status, nil
	}
	l.mu.Unlock()

	var image []byte
	var captureErr error
	session.withPageOperation(func() {
		image, captureErr = capture(ctx)
	})

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current != session || session.cancel == nil {
		return session.snapshot(), ErrLoginSessionClosed
	}
	if captureErr != nil {
		session.lastError = sanitizeLoginStatusMessage(captureErr.Error())
	} else if len(image) > 0 {
		session.challengeImage = append(session.challengeImage[:0], image...)
		session.lastError = ""
	}
	return session.snapshot(), nil
}

// resendCode clicks the SMS modal's resend control for the current session.
// It is only meaningful while the page is asking for a code.
func (l *loginSessions) resendCode(ctx context.Context, sessionID string) (LoginSessionStatus, string, string, error) {
	l.mu.Lock()
	if l.current == nil || l.current.id != sessionID {
		l.mu.Unlock()
		return LoginSessionStatus{}, "", "", ErrLoginSessionNotFound
	}
	session := l.current
	if time.Now().After(session.expiresAt) || session.cancel == nil || session.requestCode == nil {
		status := session.snapshot()
		l.mu.Unlock()
		return status, "", "", ErrLoginSessionClosed
	}
	if session.state != LoginSessionOTPRequired {
		status := session.snapshot()
		l.mu.Unlock()
		return status, "", "", ErrLoginCodeNotRequired
	}
	request := session.requestCode
	l.mu.Unlock()

	session.opMu.Lock()
	clicked, phone, err := request(ctx)
	session.opMu.Unlock()

	l.mu.Lock()
	defer l.mu.Unlock()
	return session.snapshot(), clicked, phone, err
}

func (l *loginSessions) submitCode(ctx context.Context, sessionID, code string) (LoginSessionStatus, error) {
	l.mu.Lock()
	if l.current == nil || l.current.id != sessionID {
		l.mu.Unlock()
		return LoginSessionStatus{}, ErrLoginSessionNotFound
	}

	session := l.current
	if time.Now().After(session.expiresAt) {
		cancel := session.cancel
		session.state = LoginSessionExpired
		session.cancel = nil
		session.submit = nil
		status := session.snapshot()
		l.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return status, ErrLoginSessionClosed
	}
	if session.cancel == nil || session.submit == nil {
		status := session.snapshot()
		l.mu.Unlock()
		return status, ErrLoginSessionClosed
	}
	if session.state == LoginSessionSubmittingOTP || session.state == LoginSessionOTPSubmitted {
		status := session.snapshot()
		l.mu.Unlock()
		return status, ErrLoginCodeSubmissionPending
	}
	switch session.state {
	case LoginSessionAuthenticated, LoginSessionFailed, LoginSessionExpired, LoginSessionCancelled:
		status := session.snapshot()
		l.mu.Unlock()
		return status, ErrLoginSessionClosed
	case LoginSessionWaitingForScan, LoginSessionCaptchaNeeded:
		status := session.snapshot()
		l.mu.Unlock()
		return status, ErrLoginCodeNotRequired
	}
	if session.attempts >= maxLoginCodeAttempts {
		status := session.snapshot()
		l.mu.Unlock()
		return status, ErrLoginCodeAttemptsExceeded
	}

	previousState := session.state
	session.attempts++
	session.state = LoginSessionSubmittingOTP
	session.lastError = ""
	submit := session.submit
	l.mu.Unlock()

	session.opMu.Lock()
	err := submit(ctx, code)
	session.opMu.Unlock()

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current != session || session.cancel == nil {
		return session.snapshot(), ErrLoginSessionClosed
	}
	if err != nil {
		// A DOM/CDP failure means no code was accepted by the page; do not burn
		// one of the three credential attempts before the submit click succeeds.
		// Do not retain the raw automation error: browser errors can include DOM
		// values, while session status is safe to expose through API and MCP.
		session.attempts--
		session.state = previousState
		session.lastError = "verification code form submission failed"
		return session.snapshot(), err
	}

	session.state = LoginSessionOTPSubmitted
	session.lastError = ""
	return session.snapshot(), nil
}

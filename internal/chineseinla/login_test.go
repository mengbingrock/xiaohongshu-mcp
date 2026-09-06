package chineseinla

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPasswordLoginRequestValidationPreservesPasswordWhitespace(t *testing.T) {
	request := PasswordLoginRequest{
		SessionID: " session-id ",
		Username:  " user@example.com ",
		Password:  " pass word ",
	}

	if err := request.normalizeAndValidate(); err != nil {
		t.Fatalf("normalizeAndValidate: %v", err)
	}
	if request.SessionID != "session-id" || request.Username != "user@example.com" {
		t.Fatalf("identifiers were not normalized: %#v", request)
	}
	if request.Password != " pass word " {
		t.Fatal("password whitespace must not be changed")
	}
}

func TestPasswordLoginRequestValidationRejectsInvalidFields(t *testing.T) {
	tests := []PasswordLoginRequest{
		{},
		{SessionID: "id", Password: "password"},
		{SessionID: "id", Username: strings.Repeat("u", 41), Password: "password"},
		{SessionID: "id", Username: "user"},
		{SessionID: "id", Username: "user", Password: strings.Repeat("p", 33)},
	}
	for _, request := range tests {
		if err := request.normalizeAndValidate(); !errors.Is(err, ErrInvalidLoginRequest) {
			t.Fatalf("request %#v error = %v, want ErrInvalidLoginRequest", request, err)
		}
	}
}

func TestLoginSessionStatusJSONExcludesScreenshot(t *testing.T) {
	data, err := json.Marshal(LoginSessionStatus{SessionID: "id", Screenshot: []byte("secret-image")})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), "secret-image") || strings.Contains(string(data), "screenshot") {
		t.Fatalf("screenshot leaked into JSON: %s", data)
	}
}

func TestExpiredLoginSessionIsClosed(t *testing.T) {
	store := &loginSessionStore{}
	ctx, cancel := context.WithCancel(context.Background())
	store.current = &retainedLoginSession{
		id:        "expired-session",
		state:     LoginSessionWaitingCredentials,
		cancel:    cancel,
		expiresAt: time.Now().Add(-time.Second),
	}

	store.mu.Lock()
	_, err := store.lookupLocked("expired-session")
	store.mu.Unlock()

	if !errors.Is(err, ErrLoginSessionExpired) {
		t.Fatalf("lookup error = %v, want ErrLoginSessionExpired", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("expired session context = %v, want canceled", ctx.Err())
	}
	if store.current.state != LoginSessionExpired {
		t.Fatalf("expired session state = %q", store.current.state)
	}
}

func TestLoginOperationTimeoutIsBounded(t *testing.T) {
	tests := []struct {
		configured time.Duration
		want       time.Duration
	}{
		{configured: 0, want: loginDOMOperationMax},
		{configured: 2 * time.Second, want: 2 * time.Second},
		{configured: 30 * time.Second, want: loginDOMOperationMax},
	}
	for _, test := range tests {
		automation := &Automation{Config: Config{Timeout: test.configured}}
		if got := automation.loginOperationTimeout(); got != test.want {
			t.Fatalf("loginOperationTimeout(%s) = %s, want %s", test.configured, got, test.want)
		}
	}
}

func TestCloseLoginSessionCancelsRetainedPageContext(t *testing.T) {
	automation := &Automation{}
	ctx, cancel := context.WithCancel(context.Background())
	automation.loginSessions.current = &retainedLoginSession{
		id:        "active-session",
		state:     LoginSessionWaitingCredentials,
		cancel:    cancel,
		expiresAt: time.Now().Add(time.Minute),
	}

	automation.CloseLoginSession()

	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("retained context = %v, want canceled", ctx.Err())
	}
	if automation.loginSessions.current != nil {
		t.Fatal("retained login session was not cleared")
	}
}

func TestChineseInLANetworkFailureMessage(t *testing.T) {
	tests := []struct {
		name     string
		snapshot string
		want     string
	}{
		{
			name:     "tunnel",
			snapshot: "This site can’t be reached\nERR_TUNNEL_CONNECTION_FAILED",
			want:     "proxy tunnel",
		},
		{
			name:     "proxy",
			snapshot: "ERR_PROXY_CONNECTION_FAILED",
			want:     "proxy could not be reached",
		},
		{
			name:     "generic Chrome page",
			snapshot: "chrome-error://chromewebdata/\nnetwork-error-page",
			want:     "network error page",
		},
		{
			name:     "normal login",
			snapshot: "https://www.chineseinla.com/f/page_login.html\n洛杉矶华人资讯网",
			want:     "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := chineseInLANetworkFailureMessage(test.snapshot)
			if test.want == "" && got != "" {
				t.Fatalf("chineseInLANetworkFailureMessage() = %q, want no error", got)
			}
			if test.want != "" && !strings.Contains(got, test.want) {
				t.Fatalf("chineseInLANetworkFailureMessage() = %q, want substring %q", got, test.want)
			}
		})
	}
}

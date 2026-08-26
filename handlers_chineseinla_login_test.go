package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/internal/chineseinla"
)

func TestChineseInLALoginHTTPWorkflowDoesNotEchoPassword(t *testing.T) {
	fake := &fakeChineseInLAService{loginSession: chineseinla.LoginSessionStatus{
		SessionID:  "login-session-123",
		State:      chineseinla.LoginSessionInvalidCredentials,
		Screenshot: []byte("redacted-image"),
	}}
	router := setupRoutes(NewAppServerWithChineseInLA(NewXiaohongshuService(), fake, ""))
	const username = "private-account@example.com"
	const password = "never-echo-this-password"
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/chineseinla/login/password",
		strings.NewReader(`{"session_id":"login-session-123","username":"`+username+`","password":"`+password+`"}`))
	request.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	assert.NotContains(t, recorder.Body.String(), username)
	assert.NotContains(t, recorder.Body.String(), password)
	assert.Contains(t, recorder.Body.String(), "data:image/png;base64,")
	assert.Equal(t, password, fake.passwordRequest.Password)
}

func TestChineseInLALoginHTTPErrorResponsesDoNotLeakRequest(t *testing.T) {
	fake := &fakeChineseInLAService{err: chineseinla.ErrLoginSessionNotFound}
	router := setupRoutes(NewAppServerWithChineseInLA(NewXiaohongshuService(), fake, ""))
	const password = "never-echo-this-password"
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/chineseinla/login/password",
		strings.NewReader(`{"session_id":"missing","username":"private-user","password":"`+password+`"}`))
	request.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	assert.NotContains(t, recorder.Body.String(), password)
	var response ErrorResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "CHINESEINLA_LOGIN_SESSION_NOT_FOUND", response.Code)
}

func TestChineseInLALoginHTTPResponseOmitsAuthenticatedScreenshot(t *testing.T) {
	response := newChineseInLALoginHTTPResponse(chineseinla.LoginSessionStatus{
		LoggedIn:   true,
		State:      chineseinla.LoginSessionAuthenticated,
		Screenshot: []byte("account-specific-home-page"),
	})

	assert.Empty(t, response.Img)
}

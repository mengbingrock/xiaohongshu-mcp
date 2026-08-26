package main

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xpzouying/xiaohongshu-mcp/internal/chineseinla"
)

type chineseInLALoginHTTPResponse struct {
	chineseinla.LoginSessionStatus
	Img string `json:"img,omitempty"`
}

func newChineseInLALoginHTTPResponse(status chineseinla.LoginSessionStatus) chineseInLALoginHTTPResponse {
	response := chineseInLALoginHTTPResponse{LoginSessionStatus: status}
	if status.Screenshot != nil && !status.LoggedIn {
		response.Img = "data:image/png;base64," + base64.StdEncoding.EncodeToString(status.Screenshot)
	}
	return response
}

// startChineseInLALoginHandler starts a retained page that can be operated
// without a visible desktop. The response must never be cached because it may
// contain a diagnostic screenshot of an account login page.
func (s *AppServer) startChineseInLALoginHandler(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if s.chineseInLAService == nil {
		respondError(c, http.StatusServiceUnavailable, "CHINESEINLA_NOT_CONFIGURED", "ChineseInLA support is not configured", nil)
		return
	}

	s.chineseInLAMu.Lock()
	defer s.chineseInLAMu.Unlock()
	status, err := s.chineseInLAService.StartLogin(c.Request.Context())
	if err != nil {
		respondChineseInLALoginError(c, err)
		return
	}
	respondSuccess(c, newChineseInLALoginHTTPResponse(status), "ChineseInLA login session started")
}

func (s *AppServer) getChineseInLALoginSessionHandler(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if s.chineseInLAService == nil {
		respondError(c, http.StatusServiceUnavailable, "CHINESEINLA_NOT_CONFIGURED", "ChineseInLA support is not configured", nil)
		return
	}

	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		respondError(c, http.StatusBadRequest, "INVALID_CHINESEINLA_LOGIN_REQUEST", "session_id is required", nil)
		return
	}
	s.chineseInLAMu.Lock()
	defer s.chineseInLAMu.Unlock()
	status, err := s.chineseInLAService.GetLoginSession(c.Request.Context(), sessionID)
	if err != nil {
		respondChineseInLALoginError(c, err)
		return
	}
	respondSuccess(c, newChineseInLALoginHTTPResponse(status), "ChineseInLA login session status retrieved")
}

// submitChineseInLALoginPasswordHandler never logs or echoes the request body.
// Deployments must expose it only through HTTPS (or a local SSH tunnel) and
// configure AUTH_TOKEN, because the password is a long-lived credential.
func (s *AppServer) submitChineseInLALoginPasswordHandler(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if s.chineseInLAService == nil {
		respondError(c, http.StatusServiceUnavailable, "CHINESEINLA_NOT_CONFIGURED", "ChineseInLA support is not configured", nil)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	var request chineseinla.PasswordLoginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondError(c, http.StatusBadRequest, "INVALID_CHINESEINLA_LOGIN_REQUEST", "Invalid login request", nil)
		return
	}
	s.chineseInLAMu.Lock()
	defer s.chineseInLAMu.Unlock()
	status, err := s.chineseInLAService.SubmitPasswordLogin(c.Request.Context(), request)
	request.Password = ""
	if err != nil {
		respondChineseInLALoginError(c, err)
		return
	}
	respondSuccess(c, newChineseInLALoginHTTPResponse(status), "ChineseInLA credentials submitted")
}

func respondChineseInLALoginError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, chineseinla.ErrInvalidLoginRequest):
		respondError(c, http.StatusBadRequest, "INVALID_CHINESEINLA_LOGIN_REQUEST", "Invalid login request", nil)
	case errors.Is(err, chineseinla.ErrLoginSessionNotFound):
		respondError(c, http.StatusNotFound, "CHINESEINLA_LOGIN_SESSION_NOT_FOUND", "ChineseInLA login session not found", nil)
	case errors.Is(err, chineseinla.ErrLoginSessionExpired):
		respondError(c, http.StatusConflict, "CHINESEINLA_LOGIN_SESSION_EXPIRED", "ChineseInLA login session expired", nil)
	case errors.Is(err, chineseinla.ErrLoginAttemptsExceeded):
		respondError(c, http.StatusTooManyRequests, "CHINESEINLA_LOGIN_ATTEMPTS_EXCEEDED", "ChineseInLA login attempt limit reached", nil)
	default:
		respondError(c, http.StatusInternalServerError, "CHINESEINLA_LOGIN_FAILED", "ChineseInLA login operation failed", nil)
	}
}

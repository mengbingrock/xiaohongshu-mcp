package main

// Modified for ycl-2004/xiaohongshu-mcp; see NOTICE.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

var testAccess = accessConfig{
	ReadToken:  strings.Repeat("r", minTokenLength),
	WriteToken: strings.Repeat("w", minTokenLength),
}

func newScopeTestRouter(need scope) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(scopeMiddleware(testAccess, need))
	router.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	return router
}

func TestScopeMiddleware(t *testing.T) {
	read, write := "Bearer "+testAccess.ReadToken, "Bearer "+testAccess.WriteToken
	tests := []struct {
		name          string
		need          scope
		authorization string
		want          int
	}{
		{"读路由接受读 token", scopeRead, read, http.StatusNoContent},
		{"读路由接受写 token", scopeRead, write, http.StatusNoContent},
		{"写路由接受写 token", scopeWrite, write, http.StatusNoContent},
		{"写路由拒绝读 token", scopeWrite, read, http.StatusForbidden},
		{"小写 scheme", scopeRead, "bearer " + testAccess.ReadToken, http.StatusNoContent},
		{"没带头", scopeRead, "", http.StatusUnauthorized},
		{"错误 scheme", scopeWrite, "Basic " + testAccess.WriteToken, http.StatusUnauthorized},
		{"错误 token", scopeRead, "Bearer wrong", http.StatusUnauthorized},
		{"空 token", scopeRead, "Bearer ", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/protected", nil)
			if tt.authorization != "" {
				request.Header.Set("Authorization", tt.authorization)
			}
			newScopeTestRouter(tt.need).ServeHTTP(recorder, request)
			assert.Equal(t, tt.want, recorder.Code)
		})
	}
}

func TestAccessConfigValidate(t *testing.T) {
	long := func(c string) string { return strings.Repeat(c, minTokenLength) }
	assert.NoError(t, accessConfig{ReadToken: long("a"), WriteToken: long("b")}.validate())
	assert.Error(t, accessConfig{ReadToken: "", WriteToken: long("b")}.validate(), "读 token 为空")
	assert.Error(t, accessConfig{ReadToken: long("a"), WriteToken: ""}.validate(), "写 token 为空")
	assert.Error(t, accessConfig{ReadToken: "short", WriteToken: long("b")}.validate(), "太短")
	assert.Error(t, accessConfig{ReadToken: long("a"), WriteToken: long("a")}.validate(), "读写相同")
}

func TestValidateListen(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:18060", "localhost:18060", "[::1]:18060"} {
		assert.NoError(t, validateListen(ok), ok)
	}
	for _, bad := range []string{":18060", "0.0.0.0:18060", "192.168.1.5:18060", "example.com:80", "18060"} {
		assert.Error(t, validateListen(bad), bad)
	}
}

// TestSerialMiddleware 第二个请求要等第一个结束才进入；排队超时返回 503 BUSY。
func TestSerialMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(serialMiddleware(300 * time.Millisecond))
	var mu sync.Mutex
	var events []string
	release := make(chan struct{})
	router.GET("/slow", func(c *gin.Context) {
		mu.Lock()
		events = append(events, "slow-start")
		mu.Unlock()
		<-release
		mu.Lock()
		events = append(events, "slow-end")
		mu.Unlock()
		c.Status(http.StatusNoContent)
	})
	router.GET("/fast", func(c *gin.Context) {
		mu.Lock()
		events = append(events, "fast")
		mu.Unlock()
		c.Status(http.StatusNoContent)
	})

	serve := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		return recorder
	}

	slowDone := make(chan int)
	go func() { slowDone <- serve("/slow").Code }()
	time.Sleep(50 * time.Millisecond) // 让 slow 先拿到锁

	// 排队超时：slow 还占着锁
	busy := serve("/fast")
	assert.Equal(t, http.StatusServiceUnavailable, busy.Code)
	assert.Contains(t, busy.Body.String(), "BUSY")

	// slow 结束后，排队中的请求才进入
	fastDone := make(chan int)
	go func() { fastDone <- serve("/fast").Code }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	assert.Equal(t, http.StatusNoContent, <-slowDone)
	assert.Equal(t, http.StatusNoContent, <-fastDone)
	assert.Equal(t, []string{"slow-start", "slow-end", "fast"}, events)
}

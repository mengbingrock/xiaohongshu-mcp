package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMCPStatelessSinglePost 固定「/mcp 接受不带 initialize 握手的单次 POST」这一契约。
//
// 契约由 routes.go 里一行 Stateless 支撑，丢掉它编译和其他单测都不会报错。
func TestMCPStatelessSinglePost(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), legacyAccess("")))
	server := httptest.NewServer(router)
	defer server.Close()

	post := func(t *testing.T, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		return resp
	}

	// 只用 tools/list：它不碰浏览器，而契约丢失时它恰好就是失败点——
	// 有状态模式下无 session 调用 tools/list 会被回以「握手前不允许调用」。
	resp := post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	require.Nil(t, result.Error, "无握手的 tools/list 不应报错")
	assert.NotEmpty(t, result.Result.Tools, "应返回已注册的工具")
}

// TestNotificationToolsRegistered 固定通知相关工具已注册到 MCP。
//
// 三个工具的注册各是 registerTools 里一段独立代码，漏掉任何一个编译都不会报错，
// 只有真正调用时才会发现工具不存在。
func TestNotificationToolsRegistered(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), legacyAccess("")))
	server := httptest.NewServer(router)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var result struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Required []string `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	names := make(map[string]bool, len(result.Result.Tools))
	required := make(map[string][]string, len(result.Result.Tools))
	for _, tool := range result.Result.Tools {
		names[tool.Name] = true
		required[tool.Name] = tool.InputSchema.Required
	}

	for _, want := range []string{
		"get_unread_count",
		"list_notifications",
		"reply_notification",
		"like_notification",
		"get_login_session_status",
		"submit_login_code",
		"chineseinla_get_login_session_status",
		"chineseinla_submit_login_password",
	} {
		assert.True(t, names[want], "工具 %s 应已注册", want)
	}
	assert.Contains(t, required["get_login_session_status"], "session_id")
	assert.Contains(t, required["submit_login_code"], "session_id")
	assert.Contains(t, required["submit_login_code"], "code")
	assert.Contains(t, required["chineseinla_get_login_session_status"], "session_id")
	assert.Contains(t, required["chineseinla_submit_login_password"], "session_id")
	assert.Contains(t, required["chineseinla_submit_login_password"], "username")
	assert.Contains(t, required["chineseinla_submit_login_password"], "password")
	assert.Contains(t, required["chineseinla_prepare_post"], "confirm_preparation")
	assert.Contains(t, required["chineseinla_publish_post"], "draft_id")
	assert.Contains(t, required["chineseinla_publish_post"], "confirm_publish")
}

// TestChineseInLAToolsRegistered fixes the contract that one MCP endpoint hosts
// both the original Xiaohongshu tools and the namespaced ChineseInLA workflow.
func TestChineseInLAToolsRegistered(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), legacyAccess("")))
	server := httptest.NewServer(router)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var result struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	names := make(map[string]bool, len(result.Result.Tools))
	for _, tool := range result.Result.Tools {
		names[tool.Name] = true
	}

	for _, want := range []string{
		"check_login_status",
		"chineseinla_open_login",
		"chineseinla_get_login_session_status",
		"chineseinla_submit_login_password",
		"chineseinla_check_login",
		"chineseinla_list_forums",
		"chineseinla_prepare_post",
		"chineseinla_publish_post",
	} {
		assert.True(t, names[want], "工具 %s 应已注册", want)
	}
}

// TestNotificationRoutesRegistered 固定通知的 HTTP 路由存在。
//
// 读路由表而不是发请求：这些 handler 会真的起浏览器访问小红书，
// 单测里不能碰。
func TestNotificationRoutesRegistered(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), legacyAccess("")))

	registered := make(map[string]bool)
	for _, r := range router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}

	// 列表接口两个参数都可选，GET 也要能进
	for _, want := range []string{
		"GET /api/v1/notifications/unread",
		"GET /api/v1/notifications/list",
		"POST /api/v1/notifications/list",
		"POST /api/v1/notifications/reply",
		"POST /api/v1/notifications/like",
	} {
		assert.True(t, registered[want], "路由 %s 应已注册", want)
	}
}

func TestLoginOTPRoutesRegistered(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), legacyAccess("")))

	registered := make(map[string]bool)
	for _, r := range router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}

	for _, want := range []string{
		"GET /api/v1/login/sessions/:session_id",
		"POST /api/v1/login/code",
	} {
		assert.True(t, registered[want], "路由 %s 应已注册", want)
	}
}

func TestChineseInLAHeadlessLoginRoutesRegistered(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), legacyAccess("")))

	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}

	for _, want := range []string{
		"POST /api/v1/chineseinla/login/start",
		"GET /api/v1/chineseinla/login/sessions/:session_id",
		"POST /api/v1/chineseinla/login/password",
	} {
		assert.True(t, registered[want], "route %s should be registered", want)
	}
}

func TestProtectedRoutesRequireBearerToken(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), legacyAccess("secret-token")))

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "health remains public", method: http.MethodGet, path: "/health", wantStatus: http.StatusOK},
		{name: "MCP requires token", method: http.MethodPost, path: "/mcp", wantStatus: http.StatusUnauthorized},
		{name: "MCP child path requires token", method: http.MethodPost, path: "/mcp/child", wantStatus: http.StatusUnauthorized},
		{name: "HTTP API requires token", method: http.MethodGet, path: "/api/v1/notifications/unread", wantStatus: http.StatusUnauthorized},
		{name: "ChineseInLA password API requires token", method: http.MethodPost, path: "/api/v1/chineseinla/login/password", wantStatus: http.StatusUnauthorized},
		{name: "CORS preflight remains public", method: http.MethodOptions, path: "/mcp", wantStatus: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tt.method, tt.path, nil)

			router.ServeHTTP(recorder, request)

			assert.Equal(t, tt.wantStatus, recorder.Code)
		})
	}
}

func TestMCPAcceptsConfiguredBearerToken(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), legacyAccess("secret-token")))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	router.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusOK, recorder.Code)
}

func withMCP() accessConfig {
	cfg := testAccess
	cfg.EnableMCP = true
	return cfg
}

// TestStrictRoutesRegistered 严格模式下 Marketing_Agent 用到的路由都在。
// 读路由表而不是发请求：这些 handler 会真的起浏览器访问小红书，单测里不能碰。
func TestStrictRoutesRegistered(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), testAccess))

	registered := make(map[string]bool)
	for _, r := range router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{
		"GET /health",
		"GET /api/v1/login/status",
		"GET /api/v1/access/probe",
		"POST /api/v1/feeds/search",
		"POST /api/v1/feeds/detail",
		"POST /api/v1/user/profile",
		"GET /api/v1/user/me",
		"GET /api/v1/notifications/unread",
		"POST /api/v1/notifications/list",
		"GET /api/v1/chat/conversations",
		"POST /api/v1/chat/inspect",
		"POST /api/v1/chat/messages",
		"POST /api/v1/feeds/comment/thread",
		"POST /api/v1/publish",
		"POST /api/v1/publish/inspect",
		"POST /api/v1/feeds/comment",
		"POST /api/v1/feeds/comment/reply",
		"POST /api/v1/feeds/comment/delete",
		"POST /api/v1/note/delete",
		"POST /api/v1/chat/reply",
		"GET /api/v1/login/qrcode",
		"GET /api/v1/login/qrcode/state",
	} {
		assert.True(t, registered[want], "路由 %s 应已注册", want)
	}
	assert.False(t, registered["POST /mcp"], "严格模式默认不开 /mcp")
}

// TestRouteScopes 写路由拒绝读 token；鉴权通过后请求体为空，在起浏览器之前就被参数校验拦下（400）。
func TestRouteScopes(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), withMCP()))
	read, write := "Bearer "+testAccess.ReadToken, "Bearer "+testAccess.WriteToken

	tests := []struct {
		name          string
		method        string
		path          string
		authorization string
		want          int
	}{
		{"健康检查公开", http.MethodGet, "/health", "", http.StatusOK},
		{"读路由要 token", http.MethodPost, "/api/v1/feeds/detail", "", http.StatusUnauthorized},
		{"读路由接受读 token", http.MethodPost, "/api/v1/feeds/detail", read, http.StatusBadRequest},
		{"写路由拒绝读 token", http.MethodPost, "/api/v1/feeds/comment", read, http.StatusForbidden},
		{"写路由接受写 token", http.MethodPost, "/api/v1/feeds/comment", write, http.StatusBadRequest},
		{"评论缺幂等 key 被参数校验拦下", http.MethodPost, "/api/v1/feeds/comment", write, http.StatusBadRequest},
		{"回复拒绝读 token", http.MethodPost, "/api/v1/feeds/comment/reply", read, http.StatusForbidden},
		{"取登录二维码会改账号状态，拒绝读 token", http.MethodGet, "/api/v1/login/qrcode", read, http.StatusForbidden},
		{"扫码会话状态拒绝读 token", http.MethodGet, "/api/v1/login/qrcode/state", read, http.StatusForbidden},
		{"扫码会话状态只读内存，写 token 直接返回", http.MethodGet, "/api/v1/login/qrcode/state", write, http.StatusOK},
		{"发布拒绝读 token", http.MethodPost, "/api/v1/publish", read, http.StatusForbidden},
		{"发布页探查会上传图片，拒绝读 token", http.MethodPost, "/api/v1/publish/inspect", read, http.StatusForbidden},
		{"发布页探查缺图片被参数校验拦下", http.MethodPost, "/api/v1/publish/inspect", write, http.StatusBadRequest},
		{"删笔记是写操作，拒绝读 token", http.MethodPost, "/api/v1/note/delete", read, http.StatusForbidden},
		{"删笔记缺笔记 id 被参数校验拦下", http.MethodPost, "/api/v1/note/delete", write, http.StatusBadRequest},
		{"MCP 拒绝读 token", http.MethodPost, "/mcp", read, http.StatusForbidden},
		{"没有 CORS 预检", http.MethodOptions, "/api/v1/feeds/detail", "", http.StatusNotFound},
		{"视频发布拒绝读 token", http.MethodPost, "/api/v1/publish_video", read, http.StatusForbidden},
		{"点赞拒绝读 token", http.MethodPost, "/api/v1/feeds/like", read, http.StatusForbidden},
		{"通知回复拒绝读 token", http.MethodPost, "/api/v1/notifications/reply", read, http.StatusForbidden},
		{"清会话拒绝读 token", http.MethodDelete, "/api/v1/login/cookies", read, http.StatusForbidden},
		{"取二维码拒绝读 token", http.MethodGet, "/api/v1/login/qrcode", read, http.StatusForbidden},
		{"验证码拒绝读 token", http.MethodPost, "/api/v1/login/code", read, http.StatusForbidden},
		{"ChineseInLA 登录拒绝读 token", http.MethodPost, "/api/v1/chineseinla/login/password", read, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tt.method, tt.path, strings.NewReader(""))
			request.Header.Set("Content-Type", "application/json")
			if tt.authorization != "" {
				request.Header.Set("Authorization", tt.authorization)
			}
			router.ServeHTTP(recorder, request)
			assert.Equal(t, tt.want, recorder.Code)
			assert.Empty(t, recorder.Header().Get("Access-Control-Allow-Origin"))
		})
	}
}

func TestMCPDisabledByDefault(t *testing.T) {
	router := setupRoutes(NewAppServer(NewXiaohongshuService(), testAccess))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer "+testAccess.WriteToken)
	router.ServeHTTP(recorder, request)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
}

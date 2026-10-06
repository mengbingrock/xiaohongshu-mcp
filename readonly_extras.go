package main

// Added in ycl-2004/xiaohongshu-mcp; see NOTICE. 账号状态探针和私信会话读取（都是只读）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// AccessProbeResponse 账号当前能否正常访问。Code 为空表示没被拦。
type AccessProbeResponse struct {
	*xiaohongshu.PageAccess
	Code string `json:"code"`
}

// ProbeAccess 打开发现页读取访问状态。上游的搜索、详情等流程遇到安全验证页只会等到超时，
// 调用方在任何失败之后调一次这里，才能分清是超时还是被平台拦了。
func (s *XiaohongshuService) ProbeAccess(ctx context.Context) (*AccessProbeResponse, error) {
	b := newBrowser()
	defer b.Close()
	page := b.NewPage()
	defer page.Close()

	access, err := xiaohongshu.ProbeExplore(ctx, page)
	if err != nil {
		return nil, err
	}
	response := &AccessProbeResponse{PageAccess: access}
	var blocked *xiaohongshu.PageAccessError
	if errors.As(access.Err(), &blocked) {
		response.Code = blocked.Code
	}
	return response, nil
}

// accessProbeHandler 处理 [GET /api/v1/access/probe]：被拦也返回 200，结论在 code 字段。
func (s *AppServer) accessProbeHandler(c *gin.Context) {
	result, err := s.xiaohongshuService.ProbeAccess(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusInternalServerError, "ACCESS_PROBE_FAILED", "读取账号访问状态失败", err.Error())
		return
	}
	respondSuccess(c, result, "读取账号访问状态成功")
}

// ChatConversationsResponse 私信会话列表：平台响应原样返回。
type ChatConversationsResponse struct {
	Raw json.RawMessage `json:"raw"`
}

// ReadConversations 打开网页版私信页读取会话列表（只读）。
func (s *XiaohongshuService) ReadConversations(ctx context.Context) (*ChatConversationsResponse, error) {
	b := newBrowser()
	defer b.Close()
	page := b.NewPage()
	defer page.Close()

	raw, err := xiaohongshu.ReadConversations(ctx, page, 45*time.Second)
	if err != nil {
		return nil, err
	}
	return &ChatConversationsResponse{Raw: raw}, nil
}

// chatConversationsHandler 处理 [GET /api/v1/chat/conversations]。
func (s *AppServer) chatConversationsHandler(c *gin.Context) {
	result, err := s.xiaohongshuService.ReadConversations(c.Request.Context())
	if err != nil {
		respondError(c, http.StatusInternalServerError, "CHAT_READ_FAILED", "读取私信会话失败", err.Error())
		return
	}
	respondSuccess(c, result, "读取私信会话成功")
}

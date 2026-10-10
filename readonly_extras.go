package main

// Added in ycl-2004/xiaohongshu-mcp; see NOTICE. 账号状态探针、评论楼、私信会话读取和私信页探查（都是只读）。

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

// CommentThreadRequest 读一楼评论：按评论 id 找；没有 id（人在 App 里发的）就按作者 user id 找他在这篇笔记下的第一条。
type CommentThreadRequest struct {
	FeedID    string `json:"feed_id" binding:"required"`
	XsecToken string `json:"xsec_token" binding:"required"`
	CommentID string `json:"comment_id" binding:"required_without=UserID"`
	UserID    string `json:"user_id" binding:"required_without=CommentID"`
}

// ReadCommentThread 读一条评论所在的楼（只读）。
func (s *XiaohongshuService) ReadCommentThread(ctx context.Context, feedID, xsecToken, commentID, userID string) (*xiaohongshu.CommentThread, error) {
	b := newBrowser()
	defer b.Close()
	page := b.NewPage()
	defer page.Close()

	return xiaohongshu.NewFeedDetailAction(page).ReadThread(ctx, feedID, xsecToken, commentID, userID)
}

// commentThreadHandler 处理 [POST /api/v1/feeds/comment/thread]：找不到评论返回 404 COMMENT_NOT_FOUND。
func (s *AppServer) commentThreadHandler(c *gin.Context) {
	var req CommentThreadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数错误", err.Error())
		return
	}
	result, err := s.xiaohongshuService.ReadCommentThread(c.Request.Context(), req.FeedID, req.XsecToken, req.CommentID, req.UserID)
	if errors.Is(err, xiaohongshu.ErrCommentNotFound) {
		respondError(c, http.StatusNotFound, "COMMENT_NOT_FOUND", "没找到这条评论", err.Error())
		return
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "COMMENT_THREAD_FAILED", "读取评论楼失败", err)
		return
	}
	respondSuccess(c, result, "读取评论楼成功")
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

// ChatInspectRequest 私信页探查：nickname 给了就点开这个昵称的会话。
type ChatInspectRequest struct {
	Nickname string `json:"nickname"`
}

// InspectChat 私信页只读探查（写私信读取和回复之前看清页面结构和接口）。
func (s *XiaohongshuService) InspectChat(ctx context.Context, nickname string) (*xiaohongshu.ChatInspection, error) {
	b := newBrowser()
	defer b.Close()
	page := b.NewPage()
	defer page.Close()

	return xiaohongshu.InspectChat(ctx, page, nickname)
}

// chatInspectHandler 处理 [POST /api/v1/chat/inspect]。
func (s *AppServer) chatInspectHandler(c *gin.Context) {
	var req ChatInspectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数错误", err.Error())
		return
	}
	result, err := s.xiaohongshuService.InspectChat(c.Request.Context(), req.Nickname)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "CHAT_INSPECT_FAILED", "私信页探查失败", err)
		return
	}
	respondSuccess(c, result, "私信页探查完成")
}

// ChatMessagesRequest 读和 user_id 的已有会话里最近的消息。
type ChatMessagesRequest struct {
	UserID string `json:"user_id" binding:"required"`
}

// ReadChatMessages 读一个已有会话的消息（只读；点开会话会让消息标为已读）。
func (s *XiaohongshuService) ReadChatMessages(ctx context.Context, userID string) (*xiaohongshu.ChatMessages, error) {
	b := newBrowser()
	defer b.Close()
	page := b.NewPage()
	defer page.Close()

	return xiaohongshu.ReadMessages(ctx, page, userID)
}

// chatMessagesHandler 处理 [POST /api/v1/chat/messages]。没有这个会话 404 CONVERSATION_NOT_FOUND（不新建会话）。
func (s *AppServer) chatMessagesHandler(c *gin.Context) {
	var req ChatMessagesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数错误", err.Error())
		return
	}
	result, err := s.xiaohongshuService.ReadChatMessages(c.Request.Context(), req.UserID)
	if errors.Is(err, xiaohongshu.ErrConversationNotFound) {
		respondError(c, http.StatusNotFound, "CONVERSATION_NOT_FOUND", "没有和这个人的私信会话", err.Error())
		return
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "CHAT_READ_FAILED", "读取私信消息失败", err)
		return
	}
	respondSuccess(c, result, "读取私信消息成功")
}

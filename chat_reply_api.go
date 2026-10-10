package main

// Added in ycl-2004/xiaohongshu-mcp; see NOTICE. 在已有私信会话里回复（写操作）。不提供新建会话。

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// ChatReplyRequest 在和 user_id 的已有会话里回复 content。idempotency_key 同评论：同一 key 最多提交一次。
type ChatReplyRequest struct {
	UserID         string `json:"user_id" binding:"required"`
	Content        string `json:"content" binding:"required"`
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
}

// ChatReplyResponse 回复结果：receipt.state 为 confirmed 表示平台确认发出。
type ChatReplyResponse struct {
	UserID  string                          `json:"user_id"`
	Receipt *xiaohongshu.InteractionReceipt `json:"receipt"`
}

// ReplyChat 在已有会话里回复。同一 key 已确认成功就重放回执，有记录没确认成功就拒绝（同评论）。
func (s *XiaohongshuService) ReplyChat(ctx context.Context, userID, content, key string) (*ChatReplyResponse, error) {
	if replay, err := xiaohongshu.ReplayOrRefuse(key); err != nil || replay != nil {
		if err != nil {
			return nil, err
		}
		return &ChatReplyResponse{UserID: userID, Receipt: replay}, nil
	}
	b := newBrowser()
	defer b.Close()
	page := b.NewPage()
	defer page.Close()

	receipt, err := xiaohongshu.ReplyInConversation(ctx, page, userID, content, key)
	if err != nil {
		return nil, err
	}
	return &ChatReplyResponse{UserID: userID, Receipt: receipt}, nil
}

// chatReplyHandler 处理 [POST /api/v1/chat/reply]。没有这个会话 404 CONVERSATION_NOT_FOUND（没有发出）；
// 其余失败同评论、回复（details.state 说明发没发出）。
func (s *AppServer) chatReplyHandler(c *gin.Context) {
	var req ChatReplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数错误", err.Error())
		return
	}
	result, err := s.xiaohongshuService.ReplyChat(c.Request.Context(), req.UserID, req.Content, req.IdempotencyKey)
	switch {
	case errors.Is(err, xiaohongshu.ErrConversationNotFound):
		respondError(c, http.StatusNotFound, "CONVERSATION_NOT_FOUND", "没有和这个人的私信会话，没有发出",
			writeFailure{State: xiaohongshu.StateNotSent, Stage: "conversation", Cause: err.Error()})
	case err != nil:
		respondInteractionError(c, "回复私信失败", err)
	default:
		respondSuccess(c, result, "回复私信成功")
	}
}

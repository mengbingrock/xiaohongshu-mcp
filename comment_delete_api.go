package main

// Added in ycl-2004/xiaohongshu-mcp; see NOTICE. 删除本账号自己的评论（写操作）。

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// DeleteCommentRequest 删除本账号的一条评论：只按评论 id。
type DeleteCommentRequest struct {
	FeedID    string `json:"feed_id" binding:"required"`
	XsecToken string `json:"xsec_token" binding:"required"`
	CommentID string `json:"comment_id" binding:"required"`
}

// DeleteCommentResponse 删除结果：receipt.state 为 confirmed 表示平台确认删掉了。
type DeleteCommentResponse struct {
	FeedID    string                          `json:"feed_id"`
	CommentID string                          `json:"comment_id"`
	Receipt   *xiaohongshu.InteractionReceipt `json:"receipt"`
}

// DeleteOwnComment 删除本账号自己的一条评论。
func (s *XiaohongshuService) DeleteOwnComment(ctx context.Context, feedID, xsecToken, commentID string) (*DeleteCommentResponse, error) {
	b := newBrowser()
	defer b.Close()
	page := b.NewPage()
	defer page.Close()

	receipt, err := xiaohongshu.NewCommentFeedAction(page).DeleteOwnComment(ctx, feedID, xsecToken, commentID)
	if err != nil {
		return nil, err
	}
	return &DeleteCommentResponse{FeedID: feedID, CommentID: commentID, Receipt: receipt}, nil
}

// deleteCommentHandler 处理 [POST /api/v1/feeds/comment/delete]。找不到评论 404 COMMENT_NOT_FOUND
// （不等于已经删了），不是本账号的评论 409 NOT_OWN_COMMENT，两者都没有发出删除；其余失败同评论、回复。
func (s *AppServer) deleteCommentHandler(c *gin.Context) {
	var req DeleteCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数错误", err.Error())
		return
	}
	result, err := s.xiaohongshuService.DeleteOwnComment(c.Request.Context(), req.FeedID, req.XsecToken, req.CommentID)
	switch {
	case errors.Is(err, xiaohongshu.ErrCommentNotFound):
		respondError(c, http.StatusNotFound, "COMMENT_NOT_FOUND", "没找到这条评论",
			writeFailure{State: xiaohongshu.StateNotSent, Stage: "find", Cause: err.Error()})
	case errors.Is(err, xiaohongshu.ErrNotOwnComment):
		respondError(c, http.StatusConflict, "NOT_OWN_COMMENT", "不是本账号的评论，没有删除",
			writeFailure{State: xiaohongshu.StateNotSent, Stage: "ownership", Cause: err.Error()})
	case err != nil:
		respondInteractionError(c, "删除评论失败", err)
	default:
		respondSuccess(c, result, "删除评论成功")
	}
}

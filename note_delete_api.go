package main

// Added in ycl-2004/xiaohongshu-mcp; see NOTICE. 删除本账号自己发的一篇笔记（写操作）。

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// DeleteNoteRequest 删除本账号的一篇笔记：只按笔记 id。dry_run 只演练到确认弹窗，然后点「取消」。
type DeleteNoteRequest struct {
	NoteID string `json:"note_id" binding:"required"`
	DryRun bool   `json:"dry_run"`
}

// DeleteOwnNote 删除（或演练删除）本账号自己的一篇笔记。
func (s *XiaohongshuService) DeleteOwnNote(ctx context.Context, noteID string, dryRun bool) (*xiaohongshu.NoteDeletion, error) {
	b := newBrowser()
	defer b.Close()
	page := b.NewPage()
	defer page.Close()

	return xiaohongshu.DeleteOwnNote(ctx, page, noteID, dryRun)
}

// deleteNoteHandler 处理 [POST /api/v1/note/delete]。笔记管理里没有这篇 404 NOTE_NOT_FOUND（没有删）；
// 删后核验不了 502 INTERACTION_UNKNOWN（不要重删）；其余失败同评论。成功时 receipt.state 为 confirmed，
// 演练成功时没有 receipt、dry_run 为 true。失败时 data 里照样带上经过（认卡片的方式、弹窗文字、请求），排查用。
func (s *AppServer) deleteNoteHandler(c *gin.Context) {
	var req DeleteNoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数错误", err.Error())
		return
	}
	result, err := s.xiaohongshuService.DeleteOwnNote(c.Request.Context(), req.NoteID, req.DryRun)
	switch {
	case errors.Is(err, xiaohongshu.ErrNoteNotFound):
		respondError(c, http.StatusNotFound, "NOTE_NOT_FOUND", "笔记管理里没有这篇笔记，没有删除",
			noteFailure{writeFailure{State: xiaohongshu.StateNotSent, Stage: "find", Cause: err.Error()}, result})
	case err != nil:
		var interaction *xiaohongshu.InteractionError
		if errors.As(err, &interaction) && result != nil {
			code := "INTERACTION_NOT_SENT"
			if interaction.State == xiaohongshu.StateUnknown {
				code = "INTERACTION_UNKNOWN" // 点了确定但核验不了：不要重删
			}
			respondError(c, http.StatusBadGateway, code, "删除笔记失败",
				noteFailure{writeFailure{State: interaction.State, Stage: interaction.Stage, Cause: err.Error()}, result})
			return
		}
		respondInteractionError(c, "删除笔记失败", err)
	case req.DryRun:
		respondSuccess(c, result, "删除笔记演练完成（没有删除）")
	default:
		respondSuccess(c, result, "删除笔记成功")
	}
}

// noteFailure 失败时的细节：和评论一样的 state / stage / cause，加上删笔记的经过。
type noteFailure struct {
	writeFailure
	Deletion *xiaohongshu.NoteDeletion `json:"deletion,omitempty"`
}

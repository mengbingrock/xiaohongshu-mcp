package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		name    string
		details any
		status  int
		code    string
	}{
		{"上游未读数超时（字符串）", "读取未读数失败: context deadline exceeded", http.StatusGatewayTimeout, codeTimeout},
		{"搜索 panic 的超时 error", fmt.Errorf("wait: %w", context.DeadlineExceeded), http.StatusGatewayTimeout, codeTimeout},
		{"安全验证 error", fmt.Errorf("搜索: %w", &xiaohongshu.PageAccessError{Code: xiaohongshu.CodeSecurityVerification}), http.StatusConflict, xiaohongshu.CodeSecurityVerification},
		{"登录失效字符串", (&xiaohongshu.PageAccessError{Code: xiaohongshu.CodeLoginRequired}).Error(), http.StatusConflict, xiaohongshu.CodeLoginRequired},
		{"笔记被删", "笔记不可访问: 该笔记已被删除", http.StatusGone, codeNoteUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, code, ok := classifyFailure(tt.details)
			assert.True(t, ok)
			assert.Equal(t, tt.status, status)
			assert.Equal(t, tt.code, code)
		})
	}
	for _, other := range []any{nil, "未找到评论输入框", fmt.Errorf("boom")} {
		_, _, ok := classifyFailure(other)
		assert.False(t, ok, "%v 不应被归类", other)
	}
}

// TestRespondInteractionError 写操作失败要说清楚发没发出：state 和码一起给。
func TestRespondInteractionError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		err    error
		status int
		code   string
		state  string
	}{
		{"重复 key", &xiaohongshu.DuplicateError{Saved: xiaohongshu.InteractionReceipt{State: xiaohongshu.StateUnknown}},
			http.StatusConflict, "DUPLICATE_OR_UNCERTAIN_COMMENT", xiaohongshu.StateUnknown},
		{"平台拒绝", &xiaohongshu.InteractionError{Stage: "confirm", State: xiaohongshu.StateRejected, Cause: errors.New("内容违规")},
			http.StatusConflict, "INTERACTION_REJECTED", xiaohongshu.StateRejected},
		{"拒绝原因是频繁", &xiaohongshu.InteractionError{Stage: "confirm", State: xiaohongshu.StateRejected,
			Cause: &xiaohongshu.PageAccessError{Code: xiaohongshu.CodeRateLimited}},
			http.StatusConflict, xiaohongshu.CodeRateLimited, xiaohongshu.StateRejected},
		{"点了发送后出现验证页", &xiaohongshu.InteractionError{Stage: "confirm", State: xiaohongshu.StateUnknown,
			Cause: &xiaohongshu.PageAccessError{Code: xiaohongshu.CodeSecurityVerification}},
			http.StatusConflict, xiaohongshu.CodeSecurityVerification, xiaohongshu.StateUnknown},
		{"结果不明", &xiaohongshu.InteractionError{Stage: "confirm", State: xiaohongshu.StateUnknown, Cause: context.DeadlineExceeded},
			http.StatusBadGateway, "INTERACTION_UNKNOWN", xiaohongshu.StateUnknown},
		{"点发送前找不到输入框", errors.New("未找到评论输入框"), http.StatusBadGateway, "INTERACTION_NOT_SENT", xiaohongshu.StateNotSent},
		{"点发送前超时", fmt.Errorf("打开详情页: %w", context.DeadlineExceeded), http.StatusGatewayTimeout, codeTimeout, xiaohongshu.StateNotSent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/feeds/comment", nil)
			respondInteractionError(c, "发表评论失败", tt.err)

			assert.Equal(t, tt.status, recorder.Code)
			var body struct {
				Code    string       `json:"code"`
				Details writeFailure `json:"details"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			assert.Equal(t, tt.code, body.Code)
			assert.Equal(t, tt.state, body.Details.State)
		})
	}
}

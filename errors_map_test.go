package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
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

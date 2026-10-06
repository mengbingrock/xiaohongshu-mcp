package main

// Added in ycl-2004/xiaohongshu-mcp; see NOTICE.

import (
	"context"
	stderrors "errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// 失败分类的错误码（调用方据此决定稍后重试、跳过这篇，还是全停等人）。
const (
	codeTimeout         = "TIMEOUT"          // 页面或数据没有在时限内出现
	codeNoteUnavailable = "NOTE_UNAVAILABLE" // 笔记被删、私密或不可见
)

// classifyFailure 把超时、页面拦截和笔记不可见从笼统的 500 里分出来。
// details 可能是 error、panic 的值或 err.Error() 字符串（上游 handler 都这样传）。
func classifyFailure(details any) (status int, code string, ok bool) {
	var text string
	switch v := details.(type) {
	case error:
		var access *xiaohongshu.PageAccessError
		if stderrors.As(v, &access) {
			return http.StatusConflict, access.Code, true
		}
		if stderrors.Is(v, context.DeadlineExceeded) {
			return http.StatusGatewayTimeout, codeTimeout, true
		}
		text = v.Error()
	case string:
		text = v
	case nil:
		return 0, "", false
	default:
		text = fmt.Sprint(v)
	}
	for _, access := range []string{
		xiaohongshu.CodeSecurityVerification,
		xiaohongshu.CodeLoginRequired,
		xiaohongshu.CodeAccountRestricted,
		xiaohongshu.CodeRateLimited,
	} {
		if strings.HasPrefix(text, access+":") {
			return http.StatusConflict, access, true
		}
	}
	switch {
	case strings.Contains(text, "context deadline exceeded"):
		return http.StatusGatewayTimeout, codeTimeout, true
	case strings.Contains(text, "笔记不可访问"):
		return http.StatusGone, codeNoteUnavailable, true
	}
	return 0, "", false
}

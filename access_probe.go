package main

// Added in ycl-2004/xiaohongshu-mcp; see NOTICE.

import (
	"context"
	"errors"
	"net/http"

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

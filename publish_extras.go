package main

// Added in ycl-2004/xiaohongshu-mcp; see NOTICE. 发布页探查：补 AI 合成声明和笔记 id 回执之前看清发布表单和接口。
// 会上传一张图（编辑器选图即上传），所以放在写路由下，要写 token；不发布笔记。

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// PublishInspectRequest 发布页探查：上传哪张图（本机绝对路径，png / jpg）。
type PublishInspectRequest struct {
	Image string `json:"image" binding:"required"`
	xiaohongshu.PublishSettings
}

// InspectPublish 打开发布页、上传一张图、看表单和接口，不发布（见 xiaohongshu.InspectPublish）。
func (s *XiaohongshuService) InspectPublish(ctx context.Context, image string, settings xiaohongshu.PublishSettings) (*xiaohongshu.PublishInspection, error) {
	b := newBrowser()
	defer b.Close()
	page := b.NewPage()
	defer page.Close()

	return xiaohongshu.InspectPublish(ctx, page, image, settings)
}

// publishInspectHandler 处理 [POST /api/v1/publish/inspect]。
func (s *AppServer) publishInspectHandler(c *gin.Context) {
	var req PublishInspectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数错误", err.Error())
		return
	}
	ext := strings.ToLower(filepath.Ext(req.Image))
	if info, err := os.Stat(req.Image); !filepath.IsAbs(req.Image) || err != nil || info.IsDir() || (ext != ".png" && ext != ".jpg" && ext != ".jpeg") {
		respondError(c, http.StatusBadRequest, "INVALID_REQUEST", "image 要是本机已有的 png / jpg 绝对路径", req.Image)
		return
	}
	result, err := s.xiaohongshuService.InspectPublish(c.Request.Context(), req.Image, req.PublishSettings)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "PUBLISH_INSPECT_FAILED", "发布页探查失败", err.Error())
		return
	}
	respondSuccess(c, result, "发布页探查完成（没有发布）")
}

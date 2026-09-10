package xiaohongshu

import "github.com/go-rod/rod"

// 诊断工具（cmd/creatorcheck）用的导出包装，不改变内部实现。

// NewDiagnosticTrace 新建一条截图/网络错误记录，目录落在 REDNOTE_DEBUG_DIR 下。
func NewDiagnosticTrace(kind string) *publishTrace {
	return newPublishTrace(kind)
}

// PublishPageSnapshot 返回发布页关键节点的存在情况（url/upload_panel/title_input/...）。
func PublishPageSnapshot(page *rod.Page) string {
	return publishPageSnapshot(page)
}

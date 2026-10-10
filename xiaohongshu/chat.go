// Added in ycl-2004/xiaohongshu-mcp; see NOTICE.

package xiaohongshu

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-rod/rod"
)

const (
	chatURL           = "https://www.xiaohongshu.com/chat"
	conversationsPath = "/api/im/web/v3/chats"
)

// ReadConversations 打开网页版私信页，原样返回页面自己拉取的会话列表（/api/im/web/v3/chats 的响应体）。
// 只读：不点开会话、不标已读、不发送。字段解析交给调用方，平台改字段时不用改这里。
func ReadConversations(ctx context.Context, page *rod.Page, timeout time.Duration) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	capture, err := captureIM(ctx, page)
	if err != nil {
		return nil, err
	}
	defer capture.close()
	return openChatPage(ctx, page, capture)
}

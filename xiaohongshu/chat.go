// Added in ycl-2004/xiaohongshu-mcp; see NOTICE.

package xiaohongshu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
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
	p := page.Context(ctx)
	if err := (proto.NetworkEnable{}).Call(p); err != nil {
		return nil, err
	}

	found := make(chan json.RawMessage, 1)
	pending := map[proto.NetworkRequestID]bool{}
	wait := p.EachEvent(
		func(e *proto.NetworkResponseReceived) {
			if u, err := url.Parse(e.Response.URL); err == nil && strings.HasSuffix(u.Path, conversationsPath) && e.Response.Status == 200 {
				pending[e.RequestID] = true
			}
		},
		func(e *proto.NetworkLoadingFinished) bool {
			if !pending[e.RequestID] {
				return false
			}
			body, err := (proto.NetworkGetResponseBody{RequestID: e.RequestID}).Call(p)
			if err != nil {
				return false
			}
			data := []byte(body.Body)
			if body.Base64Encoded {
				if data, err = base64.StdEncoding.DecodeString(body.Body); err != nil {
					return false
				}
			}
			if !json.Valid(data) {
				return false
			}
			found <- json.RawMessage(data)
			return true
		},
	)
	done := make(chan struct{})
	go func() { defer close(done); wait() }()
	defer func() { cancel(); <-done }()

	if err := p.Navigate(chatURL); err != nil {
		return nil, err
	}

	select {
	case raw := <-found:
		return raw, nil
	case <-ctx.Done():
		// 没等到会话列表：先看是不是被拦了（登录失效、安全验证），再报超时。
		if access, err := ProbePageAccess(page.Timeout(5 * time.Second)); err == nil {
			if err := access.Err(); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("没有等到私信会话列表: %w", ctx.Err())
	}
}

// Added in ycl-2004/xiaohongshu-mcp; see NOTICE.

package xiaohongshu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

const (
	imPathPrefix       = "/api/im/"
	maxInspectBody     = 200 << 10 // 单个响应最多留 200 KB
	maxSkeletonLines   = 1500
	inspectAfterClick  = 8 * time.Second
	inspectListTimeout = 45 * time.Second
)

// ChatInspection 私信页只读探查的结果：对着真实页面写私信读取和回复之前，看清页面结构和接口。
type ChatInspection struct {
	Before    []string                   `json:"before"`          // 打开私信页后的页面结构：每行一个元素（缩进 = 层级）
	After     []string                   `json:"after,omitempty"` // 点开会话之后的页面结构
	Clicked   bool                       `json:"clicked"`         // 找到并点开了昵称对应的会话
	Requests  []string                   `json:"requests"`        // 观察到的 /api/im/ 请求：方法 + 路径（不含参数）
	Sockets   []string                   `json:"sockets"`         // 页面开的 WebSocket（只到路径）
	Responses map[string]json.RawMessage `json:"responses"`       // 点开会话后 /api/im/ 的响应原文，按路径，同一路径留最后一次
}

// InspectChat 打开网页版私信页，记下页面结构和私信接口；给了昵称就点开那个会话再记一遍。
// 只读：不输入、不发送。点开会话会让对方的消息在平台上标为已读（和人打开一样）。
func InspectChat(ctx context.Context, page *rod.Page, nickname string) (*ChatInspection, error) {
	ctx, cancel := context.WithCancel(ctx)
	p := page.Context(ctx)
	if err := (proto.NetworkEnable{}).Call(p); err != nil {
		cancel()
		return nil, err
	}
	var mu sync.Mutex
	out := &ChatInspection{Requests: []string{}, Sockets: []string{}, Responses: map[string]json.RawMessage{}}
	listed := make(chan struct{}, 1)
	clicked := false
	paths := map[proto.NetworkRequestID]string{}
	stop := p.EachEvent(
		func(e *proto.NetworkRequestWillBeSent) {
			if u, err := url.Parse(e.Request.URL); err == nil && strings.HasPrefix(u.Path, imPathPrefix) {
				mu.Lock()
				out.Requests = append(out.Requests, e.Request.Method+" "+u.Path)
				paths[e.RequestID] = u.Path
				mu.Unlock()
			}
		},
		func(e *proto.NetworkWebSocketCreated) {
			if u, err := url.Parse(e.URL); err == nil {
				mu.Lock()
				out.Sockets = append(out.Sockets, u.Scheme+"://"+u.Host+u.Path)
				mu.Unlock()
			}
		},
		func(e *proto.NetworkLoadingFinished) {
			mu.Lock()
			path, ok := paths[e.RequestID]
			after := clicked
			mu.Unlock()
			if !ok {
				return
			}
			if path == conversationsPath {
				select {
				case listed <- struct{}{}:
				default:
				}
			}
			if !after {
				return
			}
			body, err := (proto.NetworkGetResponseBody{RequestID: e.RequestID}).Call(p)
			if err != nil {
				return
			}
			data := []byte(body.Body)
			if body.Base64Encoded {
				if data, err = base64.StdEncoding.DecodeString(body.Body); err != nil {
					return
				}
			}
			if len(data) <= maxInspectBody && json.Valid(data) {
				mu.Lock()
				out.Responses[path] = json.RawMessage(data)
				mu.Unlock()
			}
		},
	)
	done := make(chan struct{})
	go func() { defer close(done); stop() }()
	defer func() { cancel(); <-done }() // 取消 context 才会让事件监听返回

	if err := p.Navigate(chatURL); err != nil {
		return nil, err
	}
	select {
	case <-listed:
	case <-time.After(inspectListTimeout):
		if access, err := ProbePageAccess(page.Timeout(5 * time.Second)); err == nil {
			if err := access.Err(); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("没有等到私信会话列表")
	}
	humanize.Delay(ctx, humanize.Reading)
	out.Before = skeleton(p)

	if nickname != "" {
		if item, err := p.Timeout(5 * time.Second).ElementByJS(rod.Eval(conversationByNameJS, nickname)); err == nil && item != nil {
			mu.Lock()
			clicked = true
			mu.Unlock()
			if err := humanize.Click(item); err != nil {
				return nil, fmt.Errorf("点开会话失败: %w", err)
			}
			out.Clicked = true
			select {
			case <-time.After(inspectAfterClick):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			out.After = skeleton(p)
		}
	}
	return out, nil // 调用方拿到结果之前，上面的 defer 已经停掉事件监听，不会再有并发写
}

// conversationByNameJS 会话列表里文字正好是这个昵称的叶子元素（点它，点击会冒泡到整条会话）。
const conversationByNameJS = `(name) => {
	const box = document.querySelector('.xhs-im-conv-list__scroll') || document.body;
	return [...box.querySelectorAll('*')].find(e => e.childElementCount === 0 && (e.textContent || '').trim() === name) || null;
}`

// skeleton 页面结构：每行一个元素，缩进表示层级；只留标签、id、前 4 个 class 和几个看结构用的属性，
// 叶子元素带不超过 20 字的文字。跳过脚本、样式和图标。
func skeleton(p *rod.Page) []string {
	res, err := p.Eval(`(maxLines) => {
		const out = [];
		const keep = ['role', 'contenteditable', 'placeholder', 'type', 'aria-label', 'data-user-id', 'data-id'];
		const skip = new Set(['SCRIPT', 'STYLE', 'NOSCRIPT', 'svg', 'SVG', 'path', 'LINK', 'META']);
		const walk = (el, depth) => {
			if (out.length >= maxLines || depth > 40 || skip.has(el.tagName)) return;
			const cls = [...el.classList].slice(0, 4).join('.');
			const attrs = keep.filter(a => el.hasAttribute(a)).map(a => a + '=' + (el.getAttribute(a) || '').slice(0, 30)).join(' ');
			const text = el.childElementCount === 0 ? (el.textContent || '').trim().slice(0, 20) : '';
			out.push('  '.repeat(depth) + el.tagName.toLowerCase() + (el.id ? '#' + el.id : '') + (cls ? '.' + cls : '') +
				(attrs ? ' [' + attrs + ']' : '') + (text ? ' | ' + text : ''));
			for (const child of el.children) walk(child, depth + 1);
		};
		walk(document.body, 0);
		return out;
	}`, maxSkeletonLines)
	if err != nil {
		return []string{"读页面结构失败: " + err.Error()}
	}
	var lines []string
	if err := res.Value.Unmarshal(&lines); err != nil {
		return []string{"解析页面结构失败: " + err.Error()}
	}
	return lines
}

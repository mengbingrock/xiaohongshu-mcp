// Added in ycl-2004/xiaohongshu-mcp; see NOTICE.

package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

const (
	maxSkeletonLines  = 1500
	inspectAfterClick = 8 * time.Second
)

// ChatInspection 私信页只读探查的结果：对着真实页面写私信读取和回复之前（或平台改版后），看清页面结构和接口。
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
// 按昵称找文字正好相同的元素来点，不依赖会话项的 class：平台改版后也能用来重新探查。
func InspectChat(ctx context.Context, page *rod.Page, nickname string) (*ChatInspection, error) {
	capture, err := captureIM(ctx, page)
	if err != nil {
		return nil, err
	}
	defer capture.close()
	p := page.Context(ctx)
	if _, err := openChatPage(ctx, page, capture); err != nil {
		return nil, err
	}
	humanize.Delay(ctx, humanize.Reading)
	out := &ChatInspection{Before: skeleton(p), Responses: map[string]json.RawMessage{}}

	if nickname != "" {
		if item, err := p.Timeout(5 * time.Second).ElementByJS(rod.Eval(textElementJS, nickname)); err == nil && item != nil {
			seen := capture.snapshot()
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
			out.Responses = capture.changedSince(seen)
		}
	}
	out.Requests, out.Sockets = capture.requestLog(), capture.socketLog()
	return out, nil
}

// textElementJS 会话列表里文字正好是这个昵称的叶子元素（点它，点击会冒泡到整条会话）。
const textElementJS = `(name) => {
	const box = document.querySelector('` + conversationsList + `') || document.body;
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

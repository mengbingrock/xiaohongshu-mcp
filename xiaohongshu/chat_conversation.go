// Added in ycl-2004/xiaohongshu-mcp; see NOTICE. 读一个已有会话的消息、在已有会话里回复。不提供新建会话。

package xiaohongshu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

const (
	imPathPrefix      = "/api/im/"
	historyPath       = "/api/im/web/messages/history"
	maxIMBody         = 200 << 10 // 单个响应最多留 200 KB
	chatListTimeout   = 45 * time.Second
	chatOpenTimeout   = 15 * time.Second
	chatEditor        = ".xhs-im-input-bar-editor"
	chatHeaderName    = ".xhs-im-chat-window__header-name"
	conversationsList = ".xhs-im-conv-list__scroll"
)

// ErrConversationNotFound 会话列表里没有和这个人的会话。不新建会话（不冷私信），所以读和回复都到此为止。
var ErrConversationNotFound = errors.New("没有和这个人的会话")

// ChatMessages 一个会话最近的消息：网页自己拉到的 /api/im/web/messages/history 响应原样返回，字段解析交给调用方。
type ChatMessages struct {
	UserID   string          `json:"user_id"`
	Nickname string          `json:"nickname"`
	Raw      json.RawMessage `json:"raw"`
}

// ReadMessages 打开和 userID 的已有会话，返回最近的消息。只读：不输入、不发送；点开会话会让对方的消息标为已读。
func ReadMessages(ctx context.Context, page *rod.Page, userID string) (*ChatMessages, error) {
	capture, err := captureIM(ctx, page)
	if err != nil {
		return nil, err
	}
	defer capture.close()
	return openConversation(ctx, page, capture, userID)
}

// ReplyInConversation 在和 userID 的已有会话里回复。key 是幂等 key（调用方在开浏览器前先用 ReplayOrRefuse 查过）。
// 发送接口还没实测：按网页自己发出的、请求体里同时有对方 user id 和这段正文的 /api/im/ 请求及其回包确认；
// 没认出来就是结果不明，不能重发。发送前后观察到的 /api/im/ 请求写进日志，供核对。
func ReplyInConversation(ctx context.Context, page *rod.Page, userID, content, key string) (*InteractionReceipt, error) {
	capture, err := captureIM(ctx, page)
	if err != nil {
		return nil, err
	}
	defer capture.close()
	if _, err := openConversation(ctx, page, capture, userID); err != nil {
		return nil, err
	}
	editor, err := findUntimed(page, func(p *rod.Page) (*rod.Element, error) { return p.Element(chatEditor) })
	if err != nil {
		return nil, &InteractionError{Stage: "editor", State: StateNotSent, Cause: fmt.Errorf("没有找到私信输入框: %w", err)}
	}
	if err := humanize.Click(editor); err != nil {
		return nil, &InteractionError{Stage: "editor", State: StateNotSent, Cause: err}
	}
	// 按字插入文本：换行只是插入换行符，不是按回车，不会提前发出半句。
	if err := humanize.Type(ctx, editor, content); err != nil {
		return nil, &InteractionError{Stage: "type", State: StateNotSent, Cause: err}
	}
	humanize.Delay(ctx, humanize.AfterType)

	before := len(capture.requestLog())
	onPath := func(path string) bool { return strings.HasPrefix(path, imPathPrefix) }
	observer := observeSubmissionMatching(page, onPath, "", content, func(body string) bool {
		return dmRequestMatches(body, userID, content)
	})
	receipt, err := submitObserved(page, observer, key, func() error { return sendTyped(page, editor) })
	logrus.Infof("私信发送前后的 /api/im/ 请求: %v", capture.requestLog()[before:])
	return receipt, err
}

// openConversation 打开私信页，点开和 userID 的已有会话，返回历史消息。页面上没有 user id：先从会话列表接口
// 查出这个人的昵称，按昵称点开（重名就拒绝，不猜），点开后核对窗口标题是这个昵称、历史消息的收发方有这个人。
func openConversation(ctx context.Context, page *rod.Page, capture *imCapture, userID string) (*ChatMessages, error) {
	p := page.Context(ctx)
	list, err := openChatPage(ctx, page, capture)
	if err != nil {
		return nil, err
	}
	nickname, err := conversationNickname(list, userID)
	if err != nil {
		return nil, err
	}
	humanize.Delay(ctx, humanize.Reading)

	seen := capture.count(historyPath)
	item, err := findUntimed(p, func(q *rod.Page) (*rod.Element, error) {
		return q.ElementByJS(rod.Eval(conversationItemJS, nickname))
	})
	if err != nil || item == nil {
		return nil, fmt.Errorf("会话列表里找不到昵称是 %q 的会话", nickname)
	}
	if err := humanize.Click(item); err != nil {
		return nil, fmt.Errorf("点开会话失败: %w", err)
	}
	history, err := capture.wait(ctx, historyPath, seen, chatOpenTimeout)
	if err != nil {
		return nil, fmt.Errorf("点开会话后没有等到历史消息: %w", err)
	}
	header, err := p.Timeout(5 * time.Second).Element(chatHeaderName)
	if err != nil {
		return nil, fmt.Errorf("会话窗口没有标题: %w", err)
	}
	if title, _ := header.Text(); strings.TrimSpace(title) != nickname {
		return nil, fmt.Errorf("打开的会话标题是 %q，不是 %q", strings.TrimSpace(title), nickname)
	}
	if !historyFits(history, userID) {
		return nil, fmt.Errorf("打开的会话里的消息不是和 %s 的", userID)
	}
	return &ChatMessages{UserID: userID, Nickname: nickname, Raw: history}, nil
}

// openChatPage 打开私信页，等到会话列表（/api/im/web/v3/chats）并返回它的原文。没等到先看是不是被拦了。
func openChatPage(ctx context.Context, page *rod.Page, capture *imCapture) (json.RawMessage, error) {
	if err := page.Context(ctx).Navigate(chatURL); err != nil {
		return nil, err
	}
	list, err := capture.wait(ctx, conversationsPath, 0, chatListTimeout)
	if err != nil {
		if access, probeErr := ProbePageAccess(page.Timeout(5 * time.Second)); probeErr == nil {
			if blocked := access.Err(); blocked != nil {
				return nil, blocked
			}
		}
		return nil, fmt.Errorf("没有等到私信会话列表: %w", err)
	}
	return list, nil
}

// conversationItemJS 昵称正好是 name 的会话项；不是正好一个就返回 null（重名不猜）。
const conversationItemJS = `(name) => {
	const items = [...document.querySelectorAll('` + conversationsList + ` .xhs-im-conv-item')]
		.filter(e => (e.querySelector('.xhs-im-conv-item__name')?.textContent || '').trim() === name);
	return items.length === 1 ? items[0] : null;
}`

// sendTyped 发出输入框里的文字：输入后出现的「发送」按钮优先，没有就在输入框里按回车。
func sendTyped(page *rod.Page, editor *rod.Element) error {
	sendButton := func(p *rod.Page) (*rod.Element, error) {
		return p.ElementR(".xhs-im-input-bar button, .xhs-im-input-bar [role=button]", `^\s*发送\s*$`)
	}
	if button, err := findUntimedWithin(page, 3*time.Second, sendButton); err == nil {
		if err := settle(button); err == nil {
			return humanize.Click(button)
		}
	}
	if err := editor.Focus(); err != nil {
		return err
	}
	return page.Keyboard.Press(input.Enter)
}

// findUntimed 最多等 5 秒找元素，找到后换绑到不带期限的页面上再交出去。rod 的元素会记住找到它时的页面
// （连同期限），之后的点击、移动鼠标、按键都走那个页面：期限一过就超时，取消期限又会把那个页面一起取消
// （2026-10-10 实测两次：「context deadline exceeded」「context canceled」，私信都没发出）。
func findUntimed(page *rod.Page, find func(*rod.Page) (*rod.Element, error)) (*rod.Element, error) {
	return findUntimedWithin(page, 5*time.Second, find)
}

func findUntimedWithin(page *rod.Page, wait time.Duration, find func(*rod.Page) (*rod.Element, error)) (*rod.Element, error) {
	found, err := find(page.Timeout(wait))
	if err != nil {
		return nil, err
	}
	return page.ElementFromObject(found.Object)
}

// ---- 解析（纯函数，单测覆盖）----

type chatList struct {
	Data struct {
		Chats []struct {
			ChatUserID string `json:"chat_user_id"`
			Info       struct {
				Nickname string `json:"nickname"`
			} `json:"info"`
		} `json:"chats"`
	} `json:"data"`
}

// conversationNickname 会话列表里和 userID 的会话的昵称。没有这个会话 → ErrConversationNotFound；
// 昵称为空或和别的会话重名 → 报错（按昵称点开会点错人）。
func conversationNickname(list json.RawMessage, userID string) (string, error) {
	var v chatList
	if err := json.Unmarshal(list, &v); err != nil {
		return "", fmt.Errorf("解析私信会话列表失败: %w", err)
	}
	found, nickname, names := false, "", map[string]int{}
	for _, chat := range v.Data.Chats {
		names[chat.Info.Nickname]++
		if chat.ChatUserID == userID {
			found, nickname = true, chat.Info.Nickname
		}
	}
	switch {
	case !found:
		return "", ErrConversationNotFound
	case nickname == "":
		return "", fmt.Errorf("和 %s 的会话没有昵称，没法在页面上点开", userID)
	case names[nickname] > 1:
		return "", fmt.Errorf("有 %d 个会话的昵称都是 %q，不按昵称点开", names[nickname], nickname)
	}
	return nickname, nil
}

// historyFits 历史消息都在我们和 userID 之间（每条的发送方或接收方是他）。没有消息时无从核对，交给窗口标题。
func historyFits(history json.RawMessage, userID string) bool {
	var v struct {
		Data struct {
			Messages []struct {
				SenderID   string `json:"sender_id"`
				ReceiverID string `json:"receiver_id"`
			} `json:"out_message_list"`
		} `json:"data"`
	}
	if json.Unmarshal(history, &v) != nil {
		return false
	}
	for _, m := range v.Data.Messages {
		if m.SenderID != userID && m.ReceiverID != userID {
			return false
		}
	}
	return true
}

// dmRequestMatches 网页发出的请求是不是这条私信：请求体里（含嵌套的 JSON 字符串）要有对方的 user id，
// 也要有和我们正文一致的一段文字（忽略空白）。发送接口的格式还没实测，按内容认，不按字段名认。
func dmRequestMatches(body, userID, content string) bool {
	hasUser, hasContent := false, false
	for _, s := range jsonStrings(body, 0) {
		hasUser = hasUser || strings.Contains(s, userID)
		hasContent = hasContent || sameText(s, content)
	}
	return hasUser && hasContent
}

// jsonStrings 一段 JSON 里所有的字符串值；值本身又是 JSON 的，往里拆（最多三层）。
func jsonStrings(raw string, depth int) []string {
	var v any
	if depth > 3 || json.Unmarshal([]byte(raw), &v) != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			out = append(out, t)
			if s := strings.TrimSpace(t); strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
				out = append(out, jsonStrings(s, depth+1)...)
			}
		case map[string]any:
			for _, value := range t {
				walk(value)
			}
		case []any:
			for _, value := range t {
				walk(value)
			}
		}
	}
	walk(v)
	return out
}

// ---- 监听 /api/im/ ----

// imCapture 记下页面 /api/im/ 每个路径最近一次响应的原文和收到的次数，以及所有 /api/im/ 请求和 WebSocket（排查用）。
type imCapture struct {
	mu       sync.Mutex
	requests []string
	sockets  []string
	bodies   map[string]json.RawMessage
	counts   map[string]int
	notify   chan struct{}
	cancel   context.CancelFunc
	done     chan struct{}
}

func captureIM(ctx context.Context, page *rod.Page) (*imCapture, error) {
	ctx, cancel := context.WithCancel(ctx)
	p := page.Context(ctx)
	if err := (proto.NetworkEnable{}).Call(p); err != nil {
		cancel()
		return nil, err
	}
	c := &imCapture{
		bodies: map[string]json.RawMessage{}, counts: map[string]int{},
		notify: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{}),
	}
	paths := map[proto.NetworkRequestID]string{}
	wait := p.EachEvent(
		func(e *proto.NetworkRequestWillBeSent) {
			if u, err := url.Parse(e.Request.URL); err == nil && strings.HasPrefix(u.Path, imPathPrefix) {
				c.mu.Lock()
				c.requests = append(c.requests, e.Request.Method+" "+u.Path)
				c.mu.Unlock()
				if e.Request.Method != "OPTIONS" {
					paths[e.RequestID] = u.Path
				}
			}
		},
		func(e *proto.NetworkWebSocketCreated) {
			if u, err := url.Parse(e.URL); err == nil {
				c.mu.Lock()
				c.sockets = append(c.sockets, u.Scheme+"://"+u.Host+u.Path)
				c.mu.Unlock()
			}
		},
		func(e *proto.NetworkLoadingFinished) {
			path, ok := paths[e.RequestID]
			if !ok {
				return
			}
			delete(paths, e.RequestID)
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
			if len(data) > maxIMBody || !json.Valid(data) {
				return
			}
			c.mu.Lock()
			c.bodies[path] = json.RawMessage(data)
			c.counts[path]++
			c.mu.Unlock()
			select {
			case c.notify <- struct{}{}:
			default:
			}
		},
	)
	go func() { defer close(c.done); wait() }()
	return c, nil
}

// close 停止监听（取消 context 才会让事件监听返回）。
func (c *imCapture) close() { c.cancel(); <-c.done }

func (c *imCapture) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[path]
}

func (c *imCapture) requestLog() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.requests...)
}

func (c *imCapture) socketLog() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.sockets...)
}

// snapshot 每个路径到目前为止收到几次响应；配合 changedSince 取某个动作之后新到的响应。
func (c *imCapture) snapshot() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := make(map[string]int, len(c.counts))
	for path, n := range c.counts {
		seen[path] = n
	}
	return seen
}

// changedSince seen 之后又收到响应的路径，各自最近一次的原文。
func (c *imCapture) changedSince(seen map[string]int) map[string]json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]json.RawMessage{}
	for path, n := range c.counts {
		if n > seen[path] {
			out[path] = c.bodies[path]
		}
	}
	return out
}

// wait 等 path 收到第 after+1 次（及以后）的响应，返回最近一次的原文。
func (c *imCapture) wait(ctx context.Context, path string, after int, timeout time.Duration) (json.RawMessage, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		n, body := c.counts[path], c.bodies[path]
		c.mu.Unlock()
		if n > after {
			return body, nil
		}
		select {
		case <-c.notify:
		case <-deadline.C:
			return nil, fmt.Errorf("%s 在 %s 内没有响应", path, timeout)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

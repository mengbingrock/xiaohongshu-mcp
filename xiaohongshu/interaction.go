// Ported from liaogx/xiaohongshu-mcp (xiaohongshu/interaction_result.go, comment_receipt.go,
// comment_submit.go; Apache-2.0) and adapted in ycl-2004/xiaohongshu-mcp: receipts are keyed by a
// caller-supplied idempotency key, a confirmed receipt is replayed instead of refused, and page
// access uses ProbePageAccess. See NOTICE.

package xiaohongshu

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

// 写操作的结果状态。
const (
	StateConfirmed = "confirmed" // 平台业务回执明确成功
	StateRejected  = "rejected"  // 平台明确拒绝
	StateUnknown   = "unknown"   // 可能已发出，但无法确认：不要重发
	StateNotSent   = "not_sent"  // 还没点发送就失败了：可以重试
)

// commentEndpoint 评论和回复评论共用的提交接口（路径后缀）。
const commentEndpoint = "/comment/post"

// InteractionError 区分「还没发出」「平台拒绝」和「结果不明」。页面上没出现文字不能当作被拒。
type InteractionError struct {
	Stage string
	State string
	Cause error
}

func (e *InteractionError) Error() string {
	return fmt.Sprintf("INTERACTION_%s: stage=%s; %v", strings.ToUpper(e.State), e.Stage, e.Cause)
}
func (e *InteractionError) Unwrap() error { return e.Cause }

// DuplicateError 同一个幂等 key 已有未确认成功的记录：禁止重发，先核验。
type DuplicateError struct {
	Saved InteractionReceipt
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("DUPLICATE_OR_UNCERTAIN_COMMENT: 这个幂等 key 已有提交记录（state=%s）；禁止重发，先核验", e.Saved.State)
}

// InteractionReceipt 一次写操作的回执。只存状态、短数字码和固定原因词，不存正文或服务端原文。
type InteractionReceipt struct {
	State        string `json:"state"`
	CommentID    string `json:"comment_id,omitempty"`
	HTTPStatus   int    `json:"http_status,omitempty"`
	Code         string `json:"code,omitempty"`
	Reason       string `json:"reason,omitempty"`
	RequestField string `json:"request_field,omitempty"`
	Replayed     bool   `json:"replayed,omitempty"` // 这次没有提交，返回的是同一 key 之前的成功回执
}

// ---- 回执存储：按幂等 key 落盘，进程重启后仍能阻止重复提交 ----

type savedReceipt struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	InteractionReceipt
}

func receiptPath(key string) string {
	dir := os.Getenv("XHS_INTERACTION_STATE_DIR")
	if dir == "" {
		dir = filepath.Join(filepath.Dir(cookies.GetCookiesFilePath()), "receipts")
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, "interaction-"+hex.EncodeToString(sum[:])+".json")
}

// lookupReceipt 读取已有回执；不存在返回 nil。
func lookupReceipt(key string) (*InteractionReceipt, error) {
	data, err := os.ReadFile(receiptPath(key))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var saved savedReceipt
	if err := json.Unmarshal(data, &saved); err != nil {
		// 读不懂也当作「有记录、结果不明」，宁可不发。
		return &InteractionReceipt{State: StateUnknown}, nil
	}
	return &saved.InteractionReceipt, nil
}

// beginReceipt 原子地占位一条「结果不明」的记录；已存在就返回 DuplicateError。
func beginReceipt(key string) error {
	path := receiptPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		saved, _ := lookupReceipt(key)
		if saved == nil {
			saved = &InteractionReceipt{State: StateUnknown}
		}
		return &DuplicateError{Saved: *saved}
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(savedReceipt{Version: 1, UpdatedAt: time.Now(), InteractionReceipt: InteractionReceipt{State: StateUnknown}}); err != nil {
		return err
	}
	return f.Sync()
}

// finishReceipt 原子地写入最终回执。「还没发出」的记录直接删掉，允许同一个 key 重试。
func finishReceipt(key string, result InteractionReceipt) error {
	path := receiptPath(key)
	if result.State == StateNotSent {
		return os.Remove(path)
	}
	if result.State == "" {
		result.State = StateUnknown
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(savedReceipt{Version: 1, UpdatedAt: time.Now(), InteractionReceipt: result}); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// ReplayOrRefuse 开浏览器之前先查回执：同一个 key 已确认成功就直接返回那份回执；
// 有记录但没确认成功就拒绝；没有记录返回 nil, nil。
func ReplayOrRefuse(key string) (*InteractionReceipt, error) {
	saved, err := lookupReceipt(key)
	if err != nil || saved == nil {
		return nil, err
	}
	if saved.State == StateConfirmed {
		replay := *saved
		replay.Replayed = true
		return &replay, nil
	}
	return nil, &DuplicateError{Saved: *saved}
}

// ---- 观察网页自己发出的提交请求：不重放请求，不存请求头、签名 URL、Cookie 或响应原文 ----

type submissionObserver struct {
	mu       sync.Mutex
	result   InteractionReceipt
	cancel   context.CancelFunc
	done     chan struct{}
	setupErr error
}

func observeSubmission(page *rod.Page, endpoint, feedID, content string) *submissionObserver {
	ctx, cancel := context.WithCancel(page.GetContext())
	p := page.Context(ctx)
	o := &submissionObserver{cancel: cancel, done: make(chan struct{})}
	if err := (proto.NetworkEnable{}).Call(p); err != nil {
		o.setupErr = err
		cancel()
		close(o.done)
		return o
	}
	type pendingResponse struct {
		status int
		field  string
	}
	requests := map[proto.NetworkRequestID]pendingResponse{}
	wait := p.EachEvent(
		func(e *proto.NetworkRequestWillBeSent) {
			if e.Request.Method != "POST" {
				return
			}
			u, err := url.Parse(e.Request.URL)
			if err != nil || !strings.HasSuffix(u.Path, endpoint) {
				return
			}
			body := e.Request.PostData
			if body == "" {
				r, err := (proto.NetworkGetRequestPostData{RequestID: e.RequestID}).Call(p)
				if err != nil {
					return
				}
				body = r.PostData
			}
			if !matchesSubmission(body, feedID, content) {
				return
			}
			requests[e.RequestID] = pendingResponse{field: submissionTargetField(body, feedID)}
			o.mu.Lock()
			o.result.State = StateUnknown
			o.mu.Unlock()
		},
		func(e *proto.NetworkResponseReceived) {
			if req, ok := requests[e.RequestID]; ok {
				req.status = e.Response.Status
				requests[e.RequestID] = req
			}
		},
		func(e *proto.NetworkLoadingFinished) {
			req, ok := requests[e.RequestID]
			if !ok {
				return
			}
			delete(requests, e.RequestID)
			body, err := (proto.NetworkGetResponseBody{RequestID: e.RequestID}).Call(p)
			r := InteractionReceipt{State: StateUnknown, HTTPStatus: req.status}
			if err == nil {
				data := []byte(body.Body)
				if body.Base64Encoded {
					data, err = base64.StdEncoding.DecodeString(body.Body)
				}
				if err == nil {
					r = parseSubmissionResponse(req.status, data, feedID, content)
				}
			}
			r.RequestField = req.field
			o.mu.Lock()
			o.result = r
			o.mu.Unlock()
		},
		func(e *proto.NetworkLoadingFailed) {
			if _, ok := requests[e.RequestID]; !ok {
				return
			}
			delete(requests, e.RequestID)
			o.mu.Lock()
			o.result = InteractionReceipt{State: StateUnknown, Reason: "网络请求未完成，不能据此重发"}
			o.mu.Unlock()
		},
	)
	go func() { defer close(o.done); wait() }()
	return o
}

func (o *submissionObserver) close() { o.cancel(); <-o.done }
func (o *submissionObserver) snapshot() InteractionReceipt {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.result
}

func matchesSubmission(body, feedID, content string) bool {
	var v map[string]any
	if json.Unmarshal([]byte(body), &v) != nil {
		return false
	}
	if submissionTargetField(body, feedID) == "" {
		return false
	}
	if content != "" {
		text, _ := v["content"].(string)
		return text == content
	}
	return true
}

func submissionTargetField(body, feedID string) string {
	var v map[string]any
	if json.Unmarshal([]byte(body), &v) != nil {
		return ""
	}
	for _, key := range []string{"note_id", "noteId", "note_oid"} {
		if id, ok := v[key].(string); ok && id == feedID {
			return key
		}
	}
	return ""
}

func parseSubmissionResponse(status int, body []byte, feedID, content string) InteractionReceipt {
	r := InteractionReceipt{State: StateUnknown, HTTPStatus: status}
	var v struct {
		Success *bool           `json:"success"`
		Code    json.RawMessage `json:"code"`
		Msg     string          `json:"msg"`
		Message string          `json:"message"`
		Data    struct {
			ID      string `json:"id"`
			Comment struct {
				ID      string `json:"id"`
				Content string `json:"content"`
				NoteID  string `json:"note_id"`
			} `json:"comment"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &v) != nil {
		return r
	}
	// 只带出短数字业务码，不回显服务端任意文本。
	code := strings.Trim(string(v.Code), `"`)
	if len(code) <= 12 && code != "" && strings.Trim(code, "-0123456789") == "" {
		r.Code = code
	}
	msg := v.Msg + " " + v.Message
	for _, label := range []string{"安全验证", "登录", "访问频繁", "请求太频繁", "操作频繁", "评论过于频繁", "禁止评论", "评论已关闭", "内容违规"} {
		if strings.Contains(msg, label) {
			r.Reason = label
			break
		}
	}
	if v.Success != nil && !*v.Success || r.Code != "" && r.Code != "0" {
		r.State = StateRejected
		return r
	}
	if status < 200 || status >= 300 {
		return r
	}
	if v.Success == nil && r.Code != "0" {
		return r
	}
	if content != "" {
		c := v.Data.Comment
		if c.Content != "" && c.Content != content || c.NoteID != "" && c.NoteID != feedID {
			return r
		}
		r.CommentID = c.ID
		if r.CommentID == "" {
			r.CommentID = v.Data.ID
		}
		// 只有 code=0、既没有 success 也没有评论 id 的陌生回包，不能当作已发出。
		if v.Success == nil && r.CommentID == "" {
			return r
		}
	}
	r.State = StateConfirmed
	return r
}

// rejectionError 把平台拒绝归类：安全验证、登录、频繁各自对应页面拦截码。
func rejectionError(r InteractionReceipt) error {
	switch r.Reason {
	case "安全验证":
		return &PageAccessError{Code: CodeSecurityVerification}
	case "登录":
		return &PageAccessError{Code: CodeLoginRequired}
	case "访问频繁", "请求太频繁", "操作频繁", "评论过于频繁":
		return &PageAccessError{Code: CodeRateLimited}
	}
	return fmt.Errorf("平台拒绝提交（HTTP=%d, code=%s, reason=%s）；不要自动重复提交", r.HTTPStatus, r.Code, r.Reason)
}

func awaitSubmission(page *rod.Page, observer *submissionObserver, timeout time.Duration) (InteractionReceipt, error) {
	ctx, cancel := context.WithTimeout(page.GetContext(), timeout)
	defer cancel()
	p := page.Context(ctx)
	for {
		r := observer.snapshot()
		if r.State == StateConfirmed {
			return r, nil
		}
		if access, err := ProbePageAccess(p.Timeout(time.Second)); err == nil {
			if err := access.Err(); err != nil {
				return InteractionReceipt{State: StateUnknown}, &InteractionError{Stage: "confirm", State: StateUnknown, Cause: err}
			}
		}
		if r.State == StateRejected {
			return r, &InteractionError{Stage: "confirm", State: StateRejected, Cause: rejectionError(r)}
		}
		select {
		case <-ctx.Done():
			return InteractionReceipt{State: StateUnknown}, &InteractionError{Stage: "confirm", State: StateUnknown,
				Cause: fmt.Errorf("没有拿到可靠的业务回执；不要重发，先核验: %w", ctx.Err())}
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// submitWithReceipt 点发送前占位回执，按网页自己的提交请求和业务回执判断结果，最后落盘。
// 在它之前出错的步骤都算「还没发出」。
func submitWithReceipt(ctx context.Context, page *rod.Page, submit *rod.Element, key, feedID, content string) (*InteractionReceipt, error) {
	observer := observeSubmission(page, commentEndpoint, feedID, content)
	defer observer.close()
	if observer.setupErr != nil {
		return nil, &InteractionError{Stage: "response_observer", State: StateNotSent, Cause: observer.setupErr}
	}
	if err := beginReceipt(key); err != nil {
		var dup *DuplicateError
		if errors.As(err, &dup) {
			return nil, err
		}
		return nil, &InteractionError{Stage: "deduplicate", State: StateNotSent, Cause: err}
	}
	result := InteractionReceipt{State: StateUnknown}
	defer func() {
		if err := finishReceipt(key, result); err != nil {
			logrus.WithField("feed_id", feedID).Error("回执写入失败，保留原「结果不明」记录")
		}
	}()
	if err := humanize.Click(submit); err != nil {
		// 点击是否送达说不准，按结果不明处理。
		return nil, &InteractionError{Stage: "click", State: StateUnknown, Cause: err}
	}
	var err error
	result, err = awaitSubmission(page, observer, 20*time.Second)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

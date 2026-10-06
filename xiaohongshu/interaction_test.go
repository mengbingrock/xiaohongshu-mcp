// Parse and correlation cases ported from liaogx/xiaohongshu-mcp (Apache-2.0); receipt cases
// rewritten for idempotency keys in ycl-2004/xiaohongshu-mcp. See NOTICE.

package xiaohongshu

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmissionResponseEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, body, state, id string
		status                int
	}{
		{"business success", `{"success":true,"code":0,"data":{"comment":{"id":"fixture-comment","content":"fixture text","note_id":"fixture-note"}}}`, StateConfirmed, "fixture-comment", 200},
		{"flag sufficient", `{"success":true,"code":0}`, StateConfirmed, "", 200},
		{"id with zero code", `{"code":0,"data":{"comment":{"id":"fixture-comment"}}}`, StateConfirmed, "fixture-comment", 200},
		{"HTTP alone", `{}`, StateUnknown, "", 200},
		{"zero code only", `{"code":0}`, StateUnknown, "", 200},
		{"explicit rejection", `{"success":false,"code":-1,"msg":"操作频繁"}`, StateRejected, "", 200},
		{"contradictory flags", `{"success":true,"code":-1}`, StateRejected, "", 200},
		{"wrong note", `{"success":true,"data":{"comment":{"id":"fixture-comment","note_id":"another-note"}}}`, StateUnknown, "", 200},
		{"wrong text", `{"success":true,"data":{"comment":{"id":"fixture-comment","content":"another text"}}}`, StateUnknown, "", 200},
		{"server error", `{"success":true}`, StateUnknown, "", 503},
		{"html challenge", `<title>安全验证</title>`, StateUnknown, "", 200},
		{"no arbitrary echo", `{"success":false,"code":"untrusted server text","msg":"untrusted server text"}`, StateRejected, "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := parseSubmissionResponse(tc.status, []byte(tc.body), "fixture-note", "fixture text")
			require.Equal(t, tc.state, r.State)
			require.Equal(t, tc.id, r.CommentID)
			require.NotContains(t, r.Code+r.Reason, "untrusted server text")
		})
	}
}

func TestSubmissionRequestCorrelation(t *testing.T) {
	require.True(t, matchesSubmission(`{"note_id":"fixture-note","content":"fixture text"}`, "fixture-note", "fixture text"))
	require.False(t, matchesSubmission(`{"note_id":"different-note","content":"fixture text"}`, "fixture-note", "fixture text"))
	require.False(t, matchesSubmission(`{"note_id":"fixture-note","content":"different text"}`, "fixture-note", "fixture text"))
	require.True(t, matchesSubmission(`{"note_oid":"fixture-note"}`, "fixture-note", ""))
	require.False(t, matchesSubmission(`{"success":true}`, "fixture-note", ""))
}

func TestRejectionError(t *testing.T) {
	code := func(reason string) string {
		var access *PageAccessError
		if errors.As(rejectionError(InteractionReceipt{Reason: reason}), &access) {
			return access.Code
		}
		return ""
	}
	assert.Equal(t, CodeSecurityVerification, code("安全验证"))
	assert.Equal(t, CodeLoginRequired, code("登录"))
	assert.Equal(t, CodeRateLimited, code("评论过于频繁"))
	assert.Equal(t, "", code("内容违规"), "内容被拒不是账号拦截")
}

// TestReceiptLifecycle 并发只有一个能占位；结果不明时拒绝重发；还没发出就删记录允许重试；
// 确认成功后重放回执而不是再提交。记录文件 0600，不含正文。
func TestReceiptLifecycle(t *testing.T) {
	t.Setenv("XHS_INTERACTION_STATE_DIR", t.TempDir())
	const key = "outreach:demo:r1"

	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if beginReceipt(key) == nil {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, winners.Load())

	_, err := ReplayOrRefuse(key)
	var dup *DuplicateError
	require.ErrorAs(t, err, &dup)
	assert.Equal(t, StateUnknown, dup.Saved.State)

	require.NoError(t, finishReceipt(key, InteractionReceipt{State: StateNotSent}))
	replay, err := ReplayOrRefuse(key)
	require.NoError(t, err)
	assert.Nil(t, replay, "还没发出的记录已删除，可以重试")

	require.NoError(t, beginReceipt(key))
	require.NoError(t, finishReceipt(key, InteractionReceipt{State: StateConfirmed, CommentID: "c1"}))
	replay, err = ReplayOrRefuse(key)
	require.NoError(t, err)
	require.NotNil(t, replay)
	assert.True(t, replay.Replayed)
	assert.Equal(t, "c1", replay.CommentID)

	info, err := os.Stat(receiptPath(key))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	assert.NotEqual(t, receiptPath(key), receiptPath("outreach:demo:r2"))

	require.NoError(t, beginReceipt("rejected-key"))
	require.NoError(t, finishReceipt("rejected-key", InteractionReceipt{State: StateRejected}))
	_, err = ReplayOrRefuse("rejected-key")
	require.ErrorAs(t, err, &dup, "被拒绝的也不自动重发")
}

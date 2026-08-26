package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startTestLoginSession(t *testing.T, sessions *loginSessions, submit loginCodeSubmitter) (*loginSession, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	session, err := sessions.start(cancel, submit, time.Now().Add(time.Minute))
	require.NoError(t, err)
	return session, ctx
}

// TestLoginSessions 固定「同一时刻只保留一个待扫码会话」这条约束。
//
// 这是浏览器不再堆积的依据：每个待扫码会话都占着一个浏览器活到超时为止，
// 只要新会话没能关掉旧的，进程就会累积。
func TestLoginSessions(t *testing.T) {
	t.Run("开新会话会关掉上一个", func(t *testing.T) {
		var l loginSessions
		_, firstCtx := startTestLoginSession(t, &l, func(context.Context, string) error { return nil })
		assert.NoError(t, firstCtx.Err(), "第一个会话不该被关")

		startTestLoginSession(t, &l, func(context.Context, string) error { return nil })
		assert.ErrorIs(t, firstCtx.Err(), context.Canceled, "开第二个时应关掉第一个")
	})

	t.Run("第一个会话无需关闭任何东西", func(t *testing.T) {
		var l loginSessions
		assert.NotPanics(t, func() {
			startTestLoginSession(t, &l, func(context.Context, string) error { return nil })
		})
	})

	t.Run("会话结束后不会再被关第二次", func(t *testing.T) {
		var l loginSessions
		session, firstCtx := startTestLoginSession(t, &l, func(context.Context, string) error { return nil })
		l.finish(session, LoginSessionAuthenticated, "")

		startTestLoginSession(t, &l, func(context.Context, string) error { return nil })
		assert.NoError(t, firstCtx.Err(), "已结束的会话不该再被关闭")
	})

	t.Run("旧会话的收尾不会顶掉新会话", func(t *testing.T) {
		var l loginSessions
		oldSession, _ := startTestLoginSession(t, &l, func(context.Context, string) error { return nil })
		_, newCtx := startTestLoginSession(t, &l, func(context.Context, string) error { return nil })

		// 旧会话此时才走完收尾，它必须认出自己已不是当前会话
		l.finish(oldSession, LoginSessionExpired, "")

		// 再开一个：如果上一步误清了登记，新会话就永远关不掉了
		startTestLoginSession(t, &l, func(context.Context, string) error { return nil })
		assert.ErrorIs(t, newCtx.Err(), context.Canceled, "新会话仍应被后来者关闭")
	})

	t.Run("并发开会话时每个序号和外部 ID 唯一", func(t *testing.T) {
		var l loginSessions
		const n = 50

		var mu sync.Mutex
		seenSeq := make(map[uint64]bool, n)
		seenID := make(map[string]bool, n)
		errs := make(chan error, n)

		var wg sync.WaitGroup
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, cancel := context.WithCancel(context.Background())
				defer cancel()
				session, err := l.start(cancel, func(context.Context, string) error { return nil }, time.Now().Add(time.Minute))
				if err != nil {
					errs <- err
					return
				}
				mu.Lock()
				seenSeq[session.seq] = true
				seenID[session.id] = true
				mu.Unlock()
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}

		assert.Len(t, seenSeq, n, "序号必须唯一，否则 finish 会误清别人的登记")
		assert.Len(t, seenID, n, "外部会话 ID 必须不可预测且唯一")
	})
}

func TestLoginSessionCodeSubmission(t *testing.T) {
	t.Run("扫码前拒绝验证码", func(t *testing.T) {
		var l loginSessions
		session, _ := startTestLoginSession(t, &l, func(context.Context, string) error {
			t.Fatal("submit callback must not run before QR scan")
			return nil
		})

		status, err := l.submitCode(context.Background(), session.id, "012345")
		assert.ErrorIs(t, err, ErrLoginCodeNotRequired)
		assert.Equal(t, 0, status.Attempts)
	})

	t.Run("验证码只提交给匹配的活动会话", func(t *testing.T) {
		var l loginSessions
		var got string
		session, _ := startTestLoginSession(t, &l, func(_ context.Context, code string) error {
			got = code
			return nil
		})
		l.observe(session, LoginSessionOTPRequired, "")

		status, err := l.submitCode(context.Background(), session.id, "012345")
		require.NoError(t, err)
		assert.Equal(t, "012345", got)
		assert.Equal(t, LoginSessionOTPSubmitted, status.State)
		assert.Equal(t, 1, status.Attempts)

		_, err = l.submitCode(context.Background(), "wrong-session", "012345")
		assert.ErrorIs(t, err, ErrLoginSessionNotFound)
	})

	t.Run("浏览器提交失败不消耗尝试且不泄露表单值", func(t *testing.T) {
		var l loginSessions
		session, _ := startTestLoginSession(t, &l, func(context.Context, string) error {
			return errors.New("DOM failure near value 012345")
		})
		l.observe(session, LoginSessionOTPRequired, "")

		status, err := l.submitCode(context.Background(), session.id, "012345")
		require.Error(t, err)
		assert.Equal(t, LoginSessionOTPRequired, status.State)
		assert.Equal(t, 0, status.Attempts)
		assert.NotContains(t, status.LastError, "012345")
		assert.NotContains(t, status.LastError, "DOM failure")
	})

	t.Run("扫码后表单尚未出现时保留过渡状态", func(t *testing.T) {
		var l loginSessions
		session, _ := startTestLoginSession(t, &l, func(context.Context, string) error {
			return errors.New("OTP input not rendered yet")
		})
		l.observe(session, LoginSessionQRScanned, "")

		status, err := l.submitCode(context.Background(), session.id, "012345")
		require.Error(t, err)
		assert.Equal(t, LoginSessionQRScanned, status.State)
		assert.Equal(t, 0, status.Attempts)
	})

	t.Run("页面提示中的六位数不会进入会话响应", func(t *testing.T) {
		var l loginSessions
		session, _ := startTestLoginSession(t, &l, func(context.Context, string) error { return nil })
		l.observe(session, LoginSessionOTPRequired, "验证码 012345 错误")

		status, err := l.status(session.id)
		require.NoError(t, err)
		assert.Equal(t, "验证码 [redacted] 错误", status.LastError)
	})

	t.Run("最多允许三次提交", func(t *testing.T) {
		var l loginSessions
		session, _ := startTestLoginSession(t, &l, func(context.Context, string) error {
			return nil
		})
		l.observe(session, LoginSessionOTPRequired, "")

		for range maxLoginCodeAttempts {
			_, err := l.submitCode(context.Background(), session.id, "123456")
			require.NoError(t, err)
			l.observe(session, LoginSessionOTPRequired, "验证码错误")
		}
		status, err := l.submitCode(context.Background(), session.id, "123456")
		assert.ErrorIs(t, err, ErrLoginCodeAttemptsExceeded)
		assert.Equal(t, maxLoginCodeAttempts, status.Attempts)
	})

	t.Run("并发重复提交被拒绝", func(t *testing.T) {
		var l loginSessions
		entered := make(chan struct{})
		release := make(chan struct{})
		session, _ := startTestLoginSession(t, &l, func(context.Context, string) error {
			close(entered)
			<-release
			return nil
		})
		l.observe(session, LoginSessionOTPRequired, "")

		done := make(chan error, 1)
		go func() {
			_, err := l.submitCode(context.Background(), session.id, "123456")
			done <- err
		}()
		<-entered

		_, err := l.submitCode(context.Background(), session.id, "654321")
		assert.ErrorIs(t, err, ErrLoginCodeSubmissionPending)
		close(release)
		assert.NoError(t, <-done)
	})
}

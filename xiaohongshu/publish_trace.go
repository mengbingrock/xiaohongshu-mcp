package xiaohongshu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
)

var unsafeTraceName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// publishTrace keeps every screenshot for one publication in its own private
// directory. Postiz gives each account a different REDNOTE_DEBUG_DIR, while a
// standalone MCP process falls back to a directory beside its cookie file.
type publishTrace struct {
	directory string
	sequence  int

	mu       sync.Mutex
	httpErrs []string
}

func newPublishTrace(kind string) *publishTrace {
	root := strings.TrimSpace(os.Getenv("REDNOTE_DEBUG_DIR"))
	if root == "" {
		if cookiePath := strings.TrimSpace(os.Getenv("COOKIES_PATH")); cookiePath != "" {
			root = filepath.Join(filepath.Dir(cookiePath), "debug")
		}
	}
	trace := &publishTrace{}
	if root == "" {
		return trace
	}

	runName := fmt.Sprintf(
		"%s-%s",
		time.Now().UTC().Format("20060102T150405.000000000Z"),
		sanitizeTraceName(kind),
	)
	trace.directory = filepath.Join(root, runName)
	if err := os.MkdirAll(trace.directory, 0o700); err != nil {
		logrus.Warnf("RedNote publish trace directory unavailable: %v", err)
		trace.directory = ""
		return trace
	}
	logrus.Infof("RedNote publish trace started: directory=%s", trace.directory)
	return trace
}

func sanitizeTraceName(value string) string {
	value = strings.Trim(unsafeTraceName.ReplaceAllString(value, "_"), "_")
	if value == "" {
		return "step"
	}
	return value
}

// Capture is best-effort: diagnostics must never change whether a post is
// published. A fresh context lets an error screenshot survive an expired Rod
// timeout from the failed browser step.
func (t *publishTrace) Capture(page *rod.Page, step string) {
	t.sequence++
	logrus.Infof("RedNote publish step: sequence=%02d step=%s", t.sequence, step)
	if t.directory == "" || page == nil {
		return
	}

	name := fmt.Sprintf("%02d-%s.png", t.sequence, sanitizeTraceName(step))
	path := filepath.Join(t.directory, name)
	debugPage := page.Context(context.Background()).Timeout(10 * time.Second)
	data, err := debugPage.Screenshot(true, &proto.PageCaptureScreenshot{
		Format: proto.PageCaptureScreenshotFormatPng,
	})
	if err != nil {
		logrus.Warnf("RedNote publish screenshot failed: step=%s error=%v", step, err)
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		logrus.Warnf("RedNote publish screenshot write failed: step=%s error=%v", step, err)
		return
	}
	logrus.Infof("RedNote publish screenshot saved: step=%s path=%s", step, path)
}

// maxHTTPErrs bounds what one publication keeps in memory and in its error text.
const maxHTTPErrs = 20

// AttachNetwork records every HTTP error response from xiaohongshu.com for the
// life of the page. When the creator SPA bounces to /login?redirectReason=401
// this is the only way to learn which API actually rejected the session.
func (t *publishTrace) AttachNetwork(page *rod.Page) {
	if page == nil {
		return
	}
	page.EnableDomain(&proto.NetworkEnable{})
	go page.EachEvent(func(e *proto.NetworkResponseReceived) {
		if e.Response == nil || e.Response.Status < 400 {
			return
		}
		url := e.Response.URL
		if !strings.Contains(url, "xiaohongshu.com") {
			return
		}
		t.recordHTTPError(fmt.Sprintf("%d %s %s", e.Response.Status, e.Type, url))
	})()
}

func (t *publishTrace) recordHTTPError(entry string) {
	logrus.Warnf("RedNote HTTP error during publish: %s", entry)
	t.mu.Lock()
	if len(t.httpErrs) < maxHTTPErrs {
		t.httpErrs = append(t.httpErrs, entry)
	}
	t.mu.Unlock()

	if t.directory == "" {
		return
	}
	line := time.Now().UTC().Format(time.RFC3339Nano) + " " + entry + "\n"
	f, err := os.OpenFile(filepath.Join(t.directory, "network-errors.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// HTTPErrors returns the error responses seen so far, oldest first.
func (t *publishTrace) HTTPErrors() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.httpErrs...)
}

// Annotate appends the observed HTTP errors to a failure so the message that
// reaches Postiz names the rejecting endpoint. %w keeps errors.Is intact.
func (t *publishTrace) Annotate(err error) error {
	if err == nil {
		return nil
	}
	errs := t.HTTPErrors()
	if len(errs) == 0 {
		return err
	}
	if len(errs) > 3 {
		errs = errs[len(errs)-3:]
	}
	return fmt.Errorf("%w（最近的 HTTP 错误: %s）", err, strings.Join(errs, "; "))
}

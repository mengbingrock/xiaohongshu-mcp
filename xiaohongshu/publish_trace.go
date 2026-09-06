package xiaohongshu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

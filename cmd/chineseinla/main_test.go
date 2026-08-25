package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrepareArgumentParsing(t *testing.T) {
	t.Parallel()

	request, err := parsePrepare([]string{
		"--forum-id", "21",
		"--post-type", "other",
		"--title", "洛杉矶周末活动",
		"--body", "活动详情",
		"--tag", "本地,活动",
		"--image-url", "https://example.com/image.jpg",
		"--video-url", "https://example.com/video",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parsePrepare: %v", err)
	}
	if request.ForumID != 21 || request.Title != "洛杉矶周末活动" || len(request.Tags) != 1 {
		t.Fatalf("unexpected request: %#v", request)
	}
}

func TestRunHelp(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(help) = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "publish --confirm") {
		t.Fatalf("help does not document confirmation: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "--headless") {
		t.Fatalf("help does not document headless mode: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "--preview-image") {
		t.Fatalf("help does not document the headless preview image: %s", stdout.String())
	}
}

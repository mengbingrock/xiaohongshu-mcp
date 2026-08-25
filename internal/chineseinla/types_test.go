package chineseinla

import (
	"strings"
	"testing"
)

func TestDefaultConfigHeadlessEnv(t *testing.T) {
	t.Setenv("CHINESEINLA_HEADLESS", "true")
	t.Setenv("CHINESEINLA_PREVIEW_PATH", "/tmp/chineseinla-preview.png")

	config, err := DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	if !config.Headless {
		t.Fatal("CHINESEINLA_HEADLESS=true did not enable headless mode")
	}
	if config.PreviewPath != "/tmp/chineseinla-preview.png" {
		t.Fatalf("preview path = %q", config.PreviewPath)
	}
}

func TestDefaultConfigRejectsInvalidHeadlessEnv(t *testing.T) {
	t.Setenv("CHINESEINLA_HEADLESS", "sometimes")

	if _, err := DefaultConfig(); err == nil {
		t.Fatal("DefaultConfig accepted an invalid CHINESEINLA_HEADLESS value")
	}
}

func TestParsePostType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  PostType
	}{
		{input: "question", want: PostTypeQuestion},
		{input: " CLASSIFIED ", want: PostTypeClassified},
		{input: "other", want: PostTypeOther},
	}
	for _, test := range tests {
		got, err := ParsePostType(test.input)
		if err != nil {
			t.Fatalf("ParsePostType(%q): %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("ParsePostType(%q) = %q, want %q", test.input, got, test.want)
		}
	}
	if _, err := ParsePostType("news"); err == nil {
		t.Fatal("ParsePostType(news) unexpectedly succeeded")
	}
}

func TestPrepareRequestFinalBody(t *testing.T) {
	t.Parallel()

	request := PrepareRequest{
		ForumID:   21,
		PostType:  PostTypeOther,
		Title:     "洛杉矶周末活动指南",
		Body:      "本周末有三场社区活动。",
		Tags:      []string{" 活动 , 本地 ", "活动"},
		SourceURL: "https://example.com/story",
		VideoURLs: []string{"https://video.example/watch/1"},
	}
	warnings, err := request.NormalizeAndValidate()
	if err != nil {
		t.Fatalf("NormalizeAndValidate: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if got, want := strings.Join(request.Tags, ","), "活动,本地"; got != want {
		t.Fatalf("tags = %q, want %q", got, want)
	}
	body := request.FinalBody()
	for _, expected := range []string{"来源：https://example.com/story", "相关视频：", "https://video.example/watch/1"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("FinalBody() missing %q: %s", expected, body)
		}
	}
}

func TestPrepareRequestRejectsContactInTitle(t *testing.T) {
	t.Parallel()

	request := PrepareRequest{
		ForumID:  1,
		PostType: PostTypeClassified,
		Title:    "请联系 user@example.com",
		Body:     "正文",
	}
	if _, err := request.NormalizeAndValidate(); err == nil {
		t.Fatal("expected contact information in title to be rejected")
	}
}

func TestFinalBodyDoesNotDuplicateLinks(t *testing.T) {
	t.Parallel()

	request := PrepareRequest{
		Body:      "详情见 https://example.com/source\n视频：https://example.com/video",
		SourceURL: "https://example.com/source",
		VideoURLs: []string{"https://example.com/video"},
	}
	body := request.FinalBody()
	if strings.Count(body, request.SourceURL) != 1 || strings.Count(body, request.VideoURLs[0]) != 1 {
		t.Fatalf("FinalBody duplicated an existing link: %s", body)
	}
}

func TestIsChineseInLAURL(t *testing.T) {
	t.Parallel()

	marker := "/f/page_viewtopic"
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "https://www.chineseinla.com/f/page_viewtopic/t_123.html", want: true},
		{raw: "https://chineseinla.com/f/page_viewtopic/t_123.html", want: true},
		{raw: "http://www.chineseinla.com/f/page_viewtopic/t_123.html", want: false},
		{raw: "https://evil.example/?next=/f/page_viewtopic", want: false},
	}
	for _, test := range tests {
		if got := isChineseInLAURL(test.raw, marker); got != test.want {
			t.Errorf("isChineseInLAURL(%q) = %t, want %t", test.raw, got, test.want)
		}
	}
}

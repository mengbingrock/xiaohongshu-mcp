package chineseinla

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
}

func TestImageDownloaderUsesSourceRefererAndCleansUp(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got, want := request.Referer(), "https://source.example/article"; got != want {
			t.Errorf("Referer = %q, want %q", got, want)
		}
		if !strings.Contains(request.UserAgent(), "Mozilla") {
			t.Errorf("unexpected User-Agent: %q", request.UserAgent())
		}
		writer.Header().Set("Content-Type", "image/png")
		_, _ = writer.Write(tinyPNG)
	}))
	defer server.Close()

	downloader := &ImageDownloader{
		Client:      server.Client(),
		MaxBytes:    1024,
		ValidateURL: func(context.Context, string) error { return nil },
		TempDir:     t.TempDir(),
	}
	files, err := downloader.Prepare(context.Background(), nil, []string{server.URL + "/image"}, "https://source.example/article")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(files.Paths) != 1 || !strings.HasSuffix(files.Paths[0], ".png") {
		t.Fatalf("unexpected image paths: %#v", files.Paths)
	}
	if _, err := os.Stat(files.Paths[0]); err != nil {
		t.Fatalf("temporary image was not created: %v", err)
	}
	files.Cleanup()
	if _, err := os.Stat(files.Paths[0]); !os.IsNotExist(err) {
		t.Fatalf("temporary image still exists after cleanup: %v", err)
	}
}

func TestImageDownloaderRejectsUnsupportedBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("not an image"))
	}))
	defer server.Close()
	downloader := &ImageDownloader{
		Client:      server.Client(),
		MaxBytes:    1024,
		ValidateURL: func(context.Context, string) error { return nil },
		TempDir:     t.TempDir(),
	}
	if _, err := downloader.Prepare(context.Background(), nil, []string{server.URL}, ""); err == nil {
		t.Fatal("expected unsupported response body to fail")
	}
}

func TestIsPublicIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  bool
	}{
		{value: "8.8.8.8", want: true},
		{value: "2606:4700:4700::1111", want: true},
		{value: "127.0.0.1", want: false},
		{value: "10.0.0.1", want: false},
		{value: "169.254.1.1", want: false},
		{value: "::1", want: false},
	}
	for _, test := range tests {
		if got := isPublicIP(net.ParseIP(test.value)); got != test.want {
			t.Errorf("isPublicIP(%s) = %t, want %t", test.value, got, test.want)
		}
	}
}

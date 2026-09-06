package chineseinla

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestRunningBrowserControlURLAcceptsMatchingLoopbackEndpoint(t *testing.T) {
	var port int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/json/version" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		fmt.Fprintf(response, `{"webSocketDebuggerUrl":"ws://127.0.0.1:%d/devtools/browser/test-id"}`, port)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err = strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}

	got, err := runningBrowserControlURL(t.Context(), port)
	if err != nil {
		t.Fatalf("runningBrowserControlURL: %v", err)
	}
	want := fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/test-id", port)
	if got != want {
		t.Fatalf("control URL = %q, want %q", got, want)
	}
}

func TestRunningBrowserControlURLRejectsNonLocalWebSocket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(response, `{"webSocketDebuggerUrl":"ws://example.com:9223/devtools/browser/test-id"}`)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := runningBrowserControlURL(t.Context(), port); err == nil {
		t.Fatal("accepted a non-local CDP WebSocket URL")
	}
}

func TestRunningBrowserControlURLTimesOutUnresponsiveEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	_, err = runningBrowserControlURL(context.Background(), port)
	if err == nil {
		t.Fatal("unresponsive CDP endpoint unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("unresponsive CDP endpoint took %s, want at most 2s", elapsed)
	}
}

func TestNoSandboxIsRestrictedToLinuxHeadless(t *testing.T) {
	tests := []struct {
		goos     string
		headless bool
		want     bool
	}{
		{goos: "linux", headless: true, want: true},
		{goos: "linux", headless: false, want: false},
		{goos: "darwin", headless: true, want: false},
		{goos: "windows", headless: true, want: false},
	}
	for _, test := range tests {
		if got := linuxHeadlessNeedsNoSandbox(test.goos, test.headless); got != test.want {
			t.Fatalf("linuxHeadlessNeedsNoSandbox(%q, %t) = %t, want %t", test.goos, test.headless, got, test.want)
		}
	}
}

func TestBrowserArgumentsMatchConfiguredProxy(t *testing.T) {
	profile := t.TempDir()
	arguments := []string{
		"chromium",
		"--user-data-dir=" + profile,
		"--proxy-server=http://127.0.0.1:18443",
	}

	matches, err := browserArgumentsMatchConfiguration(
		arguments,
		profile,
		"http://127.0.0.1:18443/",
	)
	if err != nil {
		t.Fatalf("browserArgumentsMatchConfiguration: %v", err)
	}
	if !matches {
		t.Fatal("matching ChineseInLA browser proxy was rejected")
	}
}

func TestBrowserArgumentsRejectBrowserWithoutConfiguredProxy(t *testing.T) {
	profile := t.TempDir()
	matches, err := browserArgumentsMatchConfiguration(
		[]string{"chromium", "--user-data-dir", profile},
		profile,
		"http://127.0.0.1:18443",
	)
	if err != nil {
		t.Fatalf("browserArgumentsMatchConfiguration: %v", err)
	}
	if matches {
		t.Fatal("ChineseInLA browser without its configured proxy was accepted")
	}
}

func TestBrowserArgumentsRejectDifferentProfileWithoutClosingIt(t *testing.T) {
	configuredProfile := t.TempDir()
	otherProfile := t.TempDir()
	_, err := browserArgumentsMatchConfiguration(
		[]string{
			"chromium",
			"--user-data-dir=" + otherProfile,
			"--proxy-server=http://127.0.0.1:18443",
		},
		configuredProfile,
		"http://127.0.0.1:18443",
	)
	if err == nil {
		t.Fatal("browser using a different profile was accepted")
	}
}

func TestSetProxyAcceptsOnlyTenantLoopbackEndpoint(t *testing.T) {
	automation := NewAutomation(Config{})
	if err := automation.SetProxy(" http://127.0.0.1:32123 "); err != nil {
		t.Fatalf("SetProxy: %v", err)
	}
	if automation.Config.Proxy != "http://127.0.0.1:32123" {
		t.Fatalf("proxy = %q", automation.Config.Proxy)
	}

	for _, invalid := range []string{
		"https://127.0.0.1:32123",
		"http://localhost:32123",
		"http://10.0.0.1:32123",
		"http://127.0.0.1:80",
		"http://127.0.0.1:32123/path",
	} {
		if err := automation.SetProxy(invalid); err == nil {
			t.Errorf("SetProxy(%q) unexpectedly succeeded", invalid)
		}
	}
}

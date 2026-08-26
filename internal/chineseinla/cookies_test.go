package chineseinla

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

func TestChineseInLACookieFileRoundTripIsScopedAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session", "cookies.json")
	cookies := []*proto.NetworkCookieParam{
		{Name: "forum_session", Value: "sensitive-value", Domain: ".chineseinla.com", Path: "/", Secure: true, HTTPOnly: true},
		{Name: "www_cookie", Value: "allowed", Domain: "www.chineseinla.com", Path: "/"},
		{Name: "rednote_cookie", Value: "must-not-be-saved", Domain: ".xiaohongshu.com", Path: "/"},
	}

	if err := saveChineseInLACookies(path, cookies); err != nil {
		t.Fatalf("saveChineseInLACookies: %v", err)
	}
	// Saving twice exercises replacement of an existing destination, including
	// the Windows-compatible fallback path.
	if err := saveChineseInLACookies(path, cookies); err != nil {
		t.Fatalf("second saveChineseInLACookies: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("cookie mode = %o, want 600", info.Mode().Perm())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "sensitive-value") {
		t.Fatal("cookie value did not survive the JSON round trip")
	}
	if strings.Contains(string(data), "must-not-be-saved") {
		t.Fatal("a non-ChineseInLA cookie was persisted")
	}

	loaded, err := loadChineseInLACookies(path)
	if err != nil {
		t.Fatalf("loadChineseInLACookies: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d cookies, want 2", len(loaded))
	}
	if loaded[0].Name != "forum_session" || !loaded[0].HTTPOnly || !loaded[0].Secure {
		t.Fatalf("first cookie metadata was not preserved: %#v", loaded[0])
	}
}

func TestLoadChineseInLACookiesAcceptsBareArrayAndFiltersDomains(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	data, err := json.Marshal([]*proto.NetworkCookieParam{
		{Name: "from_url", Value: "one", URL: "https://www.chineseinla.com/f/"},
		{Name: "subdomain", Value: "two", Domain: "account.chineseinla.com"},
		{Name: "lookalike", Value: "three", Domain: "notchineseinla.com"},
		{Name: "unrelated", Value: "four", URL: "https://example.com/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadChineseInLACookies(path)
	if err != nil {
		t.Fatalf("loadChineseInLACookies: %v", err)
	}
	if len(loaded) != 2 || loaded[0].Name != "from_url" || loaded[1].Name != "subdomain" {
		t.Fatalf("loaded cookies = %#v", loaded)
	}
}

func TestLoadChineseInLACookiesRejectsUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"cookies":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadChineseInLACookies(path); err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Fatalf("error = %v, want unsupported version", err)
	}
}

func TestNetworkCookieParamPreservesAuthenticationFields(t *testing.T) {
	cookie := &proto.NetworkCookie{
		Name:       "session",
		Value:      "secret",
		Domain:     ".chineseinla.com",
		Path:       "/f",
		Expires:    proto.TimeSinceEpoch(12345),
		HTTPOnly:   true,
		Secure:     true,
		Session:    false,
		SourcePort: 443,
	}
	param := networkCookieParam(cookie)
	if param.Name != cookie.Name || param.Value != cookie.Value || param.Domain != cookie.Domain ||
		param.Path != cookie.Path || param.Expires != cookie.Expires || !param.HTTPOnly || !param.Secure {
		t.Fatalf("cookie fields were not preserved: %#v", param)
	}
	if param.SourcePort == nil || *param.SourcePort != 443 {
		t.Fatalf("source port = %v, want 443", param.SourcePort)
	}

	cookie.Session = true
	if got := networkCookieParam(cookie).Expires; got != 0 {
		t.Fatalf("session cookie expiry = %v, want zero", got)
	}
}

func TestValidateCookieIsolation(t *testing.T) {
	directory := t.TempDir()
	xiaohongshuPath := filepath.Join(directory, "xiaohongshu.json")
	chineseInLAPath := filepath.Join(directory, "chineseinla.json")
	if err := ValidateCookieIsolation(chineseInLAPath, xiaohongshuPath); err != nil {
		t.Fatalf("separate paths rejected: %v", err)
	}
	if err := ValidateCookieIsolation(xiaohongshuPath, xiaohongshuPath); err == nil {
		t.Fatal("identical cookie paths were accepted")
	}

	if err := os.WriteFile(xiaohongshuPath, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	hardLinkPath := filepath.Join(directory, "same-file.json")
	if err := os.Link(xiaohongshuPath, hardLinkPath); err == nil {
		if err := ValidateCookieIsolation(hardLinkPath, xiaohongshuPath); err == nil {
			t.Fatal("hard-linked cookie paths were accepted")
		}
	}
}

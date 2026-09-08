package reddit

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const (
	redditCookieFileVersion = 1
	maxCookieFileSize       = 2 << 20
	maxCookies              = 500
)

type cookieFile struct {
	Version int                         `json:"version"`
	SavedAt string                      `json:"saved_at,omitempty"`
	Cookies []*proto.NetworkCookieParam `json:"cookies"`
}

func loadCookies(path string) ([]*proto.NetworkCookieParam, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxCookieFileSize {
		return nil, fmt.Errorf("Reddit cookie file exceeds %d bytes", maxCookieFileSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Reddit cookie file: %w", err)
	}
	var file cookieFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("decode Reddit cookie file: %w", err)
	}
	if file.Version != redditCookieFileVersion {
		return nil, fmt.Errorf("unsupported Reddit cookie file version %d", file.Version)
	}
	if len(file.Cookies) > maxCookies {
		return nil, fmt.Errorf("Reddit cookie file contains more than %d cookies", maxCookies)
	}
	filtered := make([]*proto.NetworkCookieParam, 0, len(file.Cookies))
	for _, cookie := range file.Cookies {
		if cookie == nil || strings.TrimSpace(cookie.Name) == "" || !isRedditCookieParam(cookie) {
			continue
		}
		if cookie.Expires < 0 {
			cookie.Expires = 0
		}
		filtered = append(filtered, cookie)
	}
	return filtered, nil
}

func restoreCookies(browser *rod.Browser, path string) error {
	cookies, err := loadCookies(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotLoggedIn
	}
	if err != nil {
		return err
	}
	if len(cookies) == 0 {
		return ErrNotLoggedIn
	}
	if err := browser.SetCookies(cookies); err != nil {
		return fmt.Errorf("restore Reddit cookies: %w", err)
	}
	return nil
}

func persistCookies(browser *rod.Browser, path string) error {
	cookies, err := browser.GetCookies()
	if err != nil {
		return fmt.Errorf("read Reddit browser cookies: %w", err)
	}
	params := make([]*proto.NetworkCookieParam, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie == nil || !isRedditDomain(cookie.Domain) {
			continue
		}
		param := &proto.NetworkCookieParam{
			Name:         cookie.Name,
			Value:        cookie.Value,
			Domain:       cookie.Domain,
			Path:         cookie.Path,
			Secure:       cookie.Secure,
			HTTPOnly:     cookie.HTTPOnly,
			SameSite:     cookie.SameSite,
			Priority:     cookie.Priority,
			SameParty:    cookie.SameParty,
			SourceScheme: cookie.SourceScheme,
			PartitionKey: cookie.PartitionKey,
		}
		if !cookie.Session {
			param.Expires = cookie.Expires
		}
		if cookie.SourcePort == -1 || (cookie.SourcePort >= 1 && cookie.SourcePort <= 65535) {
			sourcePort := cookie.SourcePort
			param.SourcePort = &sourcePort
		}
		params = append(params, param)
	}
	if len(params) == 0 {
		return nil
	}
	return saveCookies(path, params)
}

func saveCookies(path string, cookies []*proto.NetworkCookieParam) error {
	if len(cookies) > maxCookies {
		return fmt.Errorf("refusing to save more than %d Reddit cookies", maxCookies)
	}
	filtered := make([]*proto.NetworkCookieParam, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie != nil && strings.TrimSpace(cookie.Name) != "" && isRedditCookieParam(cookie) {
			filtered = append(filtered, cookie)
		}
	}
	data, err := json.MarshalIndent(cookieFile{
		Version: redditCookieFileVersion,
		SavedAt: time.Now().UTC().Format(time.RFC3339),
		Cookies: filtered,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Reddit cookies: %w", err)
	}
	data = append(data, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Reddit profile directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".reddit-cookies-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary Reddit cookie file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary Reddit cookie file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary Reddit cookie file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary Reddit cookie file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary Reddit cookie file: %w", err)
	}
	if err := replaceCookieFile(temporaryPath, path); err != nil {
		return fmt.Errorf("replace Reddit cookie file: %w", err)
	}
	removeTemporary = false
	return nil
}

func replaceCookieFile(temporaryPath, destination string) error {
	if err := os.Rename(temporaryPath, destination); err == nil {
		return nil
	}
	if _, err := os.Stat(destination); err != nil {
		return os.Rename(temporaryPath, destination)
	}
	backup := destination + ".previous"
	_ = os.Remove(backup)
	if err := os.Rename(destination, backup); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		_ = os.Rename(backup, destination)
		return err
	}
	_ = os.Remove(backup)
	return nil
}

func isRedditCookieParam(cookie *proto.NetworkCookieParam) bool {
	if isRedditDomain(cookie.Domain) {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(cookie.URL))
	return err == nil && isRedditDomain(parsed.Hostname())
}

func isRedditDomain(domain string) bool {
	domain = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
	return domain == "reddit.com" || strings.HasSuffix(domain, ".reddit.com")
}

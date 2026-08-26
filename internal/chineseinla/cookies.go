package chineseinla

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const (
	chineseInLACookieFileVersion = 1
	maxChineseInLACookieFileSize = 2 << 20
	maxChineseInLACookies        = 3000
)

type chineseInLACookieFile struct {
	Version int                         `json:"version"`
	SavedAt string                      `json:"saved_at,omitempty"`
	Cookies []*proto.NetworkCookieParam `json:"cookies"`
}

// cookiePersistenceState guards cookie-file I/O and ensures a persisted file
// is restored at most once per Chromium process. The WebSocket control URL
// contains Chromium's browser ID and changes when that process is replaced.
type cookiePersistenceState struct {
	mu                 sync.Mutex
	restoredControlURL string
}

func (a *Automation) restoreCookiesOnce(browser *rod.Browser, controlURL string) error {
	state := &a.cookiePersistence
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.restoredControlURL == controlURL {
		return nil
	}
	cookies, err := loadChineseInLACookies(a.Config.CookiePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			state.restoredControlURL = controlURL
			return nil
		}
		return err
	}
	if len(cookies) > 0 {
		if err := browser.SetCookies(cookies); err != nil {
			return fmt.Errorf("restore ChineseInLA cookies: %w", err)
		}
	}
	state.restoredControlURL = controlURL
	return nil
}

func (a *Automation) persistCookies(browser *rod.Browser) error {
	cookies, err := browser.GetCookies()
	if err != nil {
		return fmt.Errorf("read ChineseInLA browser cookies: %w", err)
	}
	params := make([]*proto.NetworkCookieParam, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie == nil || !isChineseInLACookieDomain(cookie.Domain) {
			continue
		}
		params = append(params, networkCookieParam(cookie))
	}
	// Public pages can be opened without authentication. Do not replace a
	// previously valid session file with an empty set after such a request.
	if len(params) == 0 {
		return nil
	}

	state := &a.cookiePersistence
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := saveChineseInLACookies(a.Config.CookiePath, params); err != nil {
		return err
	}
	return nil
}

func networkCookieParam(cookie *proto.NetworkCookie) *proto.NetworkCookieParam {
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
	return param
}

func loadChineseInLACookies(path string) ([]*proto.NetworkCookieParam, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("ChineseInLA cookie path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxChineseInLACookieFileSize {
		return nil, fmt.Errorf("ChineseInLA cookie file exceeds %d bytes", maxChineseInLACookieFileSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ChineseInLA cookie file: %w", err)
	}

	var file chineseInLACookieFile
	if err := json.Unmarshal(data, &file); err != nil || file.Cookies == nil {
		// Accept a bare CDP cookie-param array for straightforward migration,
		// while always writing the versioned envelope below.
		var bare []*proto.NetworkCookieParam
		if bareErr := json.Unmarshal(data, &bare); bareErr != nil {
			if err != nil {
				return nil, fmt.Errorf("decode ChineseInLA cookie file: %w", err)
			}
			return nil, fmt.Errorf("decode ChineseInLA cookie file: %w", bareErr)
		}
		file.Cookies = bare
	} else if file.Version != chineseInLACookieFileVersion {
		return nil, fmt.Errorf("unsupported ChineseInLA cookie file version %d", file.Version)
	}
	if len(file.Cookies) > maxChineseInLACookies {
		return nil, fmt.Errorf("ChineseInLA cookie file contains more than %d cookies", maxChineseInLACookies)
	}

	filtered := make([]*proto.NetworkCookieParam, 0, len(file.Cookies))
	for _, cookie := range file.Cookies {
		if cookie == nil || strings.TrimSpace(cookie.Name) == "" || !isChineseInLACookieParam(cookie) {
			continue
		}
		filtered = append(filtered, cookie)
	}
	return filtered, nil
}

func saveChineseInLACookies(path string, cookies []*proto.NetworkCookieParam) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("ChineseInLA cookie path is empty")
	}
	if len(cookies) > maxChineseInLACookies {
		return fmt.Errorf("refusing to save more than %d ChineseInLA cookies", maxChineseInLACookies)
	}
	filtered := make([]*proto.NetworkCookieParam, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie != nil && strings.TrimSpace(cookie.Name) != "" && isChineseInLACookieParam(cookie) {
			filtered = append(filtered, cookie)
		}
	}
	data, err := json.MarshalIndent(chineseInLACookieFile{
		Version: chineseInLACookieFileVersion,
		SavedAt: time.Now().UTC().Format(time.RFC3339),
		Cookies: filtered,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode ChineseInLA cookies: %w", err)
	}
	data = append(data, '\n')

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create ChineseInLA cookie directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".chineseinla-cookies-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary ChineseInLA cookie file: %w", err)
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
		return fmt.Errorf("secure temporary ChineseInLA cookie file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary ChineseInLA cookie file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary ChineseInLA cookie file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary ChineseInLA cookie file: %w", err)
	}
	if err := replaceCookieFile(temporaryPath, path); err != nil {
		return fmt.Errorf("replace ChineseInLA cookie file: %w", err)
	}
	removeTemporary = false
	return nil
}

// replaceCookieFile preserves a complete old or new file even on platforms
// where os.Rename cannot replace an existing destination (notably Windows).
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

func isChineseInLACookieParam(cookie *proto.NetworkCookieParam) bool {
	if isChineseInLACookieDomain(cookie.Domain) {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(cookie.URL))
	return err == nil && isChineseInLACookieDomain(parsed.Hostname())
}

func isChineseInLACookieDomain(domain string) bool {
	domain = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
	return domain == "chineseinla.com" || strings.HasSuffix(domain, ".chineseinla.com")
}

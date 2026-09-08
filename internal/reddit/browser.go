package reddit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

type Automation struct {
	Config Config
	mu     sync.Mutex
}

func NewAutomation(config Config) *Automation {
	return &Automation{Config: config}
}

type browserSession struct {
	browser *rod.Browser
	command *exec.Cmd
	exited  chan error
}

func (a *Automation) withBrowser(ctx context.Context, profileKey string, action func(*rod.Browser) error) error {
	profileKey, err := ValidateProfileKey(profileKey)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	profileDir, err := a.Config.ProfileDir(profileKey)
	if err != nil {
		return err
	}
	cookiePath, err := a.Config.CookiePath(profileKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return fmt.Errorf("create Reddit browser profile: %w", err)
	}
	session, err := a.launchBrowser(ctx, profileDir)
	if err != nil {
		return err
	}
	defer session.close()
	if err := restoreCookies(session.browser, cookiePath); err != nil {
		return err
	}
	if err := action(session.browser); err != nil {
		return err
	}
	if err := persistCookies(session.browser, cookiePath); err != nil {
		return err
	}
	return nil
}

func (a *Automation) launchBrowser(ctx context.Context, profileDir string) (*browserSession, error) {
	binary := strings.TrimSpace(a.Config.BrowserBin)
	if binary == "" || !filepath.IsAbs(binary) {
		return nil, errors.New("Reddit browser binary must be an absolute path")
	}
	if info, err := os.Stat(binary); err != nil || info.IsDir() {
		return nil, fmt.Errorf("Reddit browser binary is unavailable: %s", binary)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("reserve Reddit browser debugging port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-save-password-bubble",
		"--disable-features=PasswordManagerOnboarding",
		"--disable-blink-features=AutomationControlled",
		"--window-size=1280,900",
	}
	if a.Config.Headless {
		args = append(args, "--headless=new")
	}
	if runtime.GOOS == "linux" {
		args = append(args, "--no-sandbox", "--disable-dev-shm-usage")
	}
	args = append(args, "about:blank")

	command := exec.CommandContext(ctx, binary, args...)
	command.Stdout = nil
	command.Stderr = nil
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start Reddit browser: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	session := &browserSession{command: command, exited: exited}

	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(20 * time.Second)
	var controlURL string
	for time.Now().Before(deadline) {
		select {
		case processErr := <-exited:
			if processErr == nil {
				return nil, errors.New("Reddit browser exited before CDP was ready")
			}
			return nil, fmt.Errorf("Reddit browser exited before CDP was ready: %w", processErr)
		default:
		}
		response, requestErr := client.Get(endpoint + "/json/version")
		if requestErr == nil {
			var version struct {
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&version)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && strings.HasPrefix(version.WebSocketDebuggerURL, "ws") {
				controlURL = version.WebSocketDebuggerURL
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if controlURL == "" {
		session.close()
		return nil, errors.New("Reddit browser did not expose its DevTools endpoint")
	}
	browser := rod.New().Context(ctx).ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		session.close()
		return nil, fmt.Errorf("connect to Reddit browser: %w", err)
	}
	session.browser = browser
	return session, nil
}

func (s *browserSession) close() {
	if s == nil {
		return
	}
	if s.browser != nil {
		_ = s.browser.Close()
	}
	if s.command == nil || s.command.Process == nil {
		return
	}
	select {
	case <-s.exited:
		return
	case <-time.After(3 * time.Second):
		_ = s.command.Process.Kill()
	}
	select {
	case <-s.exited:
	case <-time.After(time.Second):
	}
}

func (a *Automation) openPage(browser *rod.Browser, targetURL string) (*rod.Page, error) {
	timeout := a.Config.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	page, err := browser.Page(proto.TargetCreateTarget{URL: targetURL})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", targetURL, err)
	}
	if err := page.Timeout(timeout).WaitLoad(); err != nil {
		_ = page.Close()
		return nil, fmt.Errorf("wait for %s: %w", targetURL, err)
	}
	return page, nil
}

func inspectPage(page *rod.Page) (string, string, error) {
	info, err := page.Info()
	if err != nil {
		return "", "", err
	}
	body, err := page.Timeout(5 * time.Second).Element("body")
	if err != nil {
		return info.URL, "", err
	}
	text, err := body.Text()
	if err != nil {
		return info.URL, "", err
	}
	compact := strings.Join(strings.Fields(text), " ")
	if len(compact) > 1_000 {
		compact = compact[:1_000]
	}
	if strings.Contains(strings.ToLower(compact), "blocked by network security") {
		return info.URL, compact, ErrBlocked
	}
	return info.URL, compact, nil
}

func authenticatedUsername(page *rod.Page) (string, error) {
	if _, _, err := inspectPage(page); err != nil {
		return "", err
	}
	element, err := page.Timeout(10 * time.Second).Element(`#header-bottom-right .user a`)
	if err != nil {
		return "", ErrNotLoggedIn
	}
	username, err := element.Text()
	if err != nil || strings.TrimSpace(username) == "" {
		return "", ErrNotLoggedIn
	}
	return strings.TrimSpace(username), nil
}

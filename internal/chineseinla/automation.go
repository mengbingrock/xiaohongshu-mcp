package chineseinla

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	xhsbrowser "github.com/xpzouying/xiaohongshu-mcp/browser"
)

var (
	ErrNotLoggedIn = errors.New("not logged in to ChineseInLA")
	ErrCaptcha     = errors.New("ChineseInLA requested a CAPTCHA or human verification")
)

const browserConnectTimeout = 5 * time.Second

type Automation struct {
	Config     Config
	Downloader *ImageDownloader
	State      StateStore

	cookiePersistence cookiePersistenceState

	// loginSessions keeps the one short-lived page used by the headless login
	// workflow. The zero value is ready to use, which also keeps Automation
	// safe when tests construct it without NewAutomation.
	loginSessions loginSessionStore
}

type LoginStatus struct {
	LoggedIn bool   `json:"logged_in"`
	URL      string `json:"url"`
	Message  string `json:"message"`
}

type PrepareResult struct {
	Status       string   `json:"status"`
	DraftID      string   `json:"draft_id"`
	Forum        Forum    `json:"forum"`
	PostType     PostType `json:"post_type"`
	PostTypeName string   `json:"post_type_name"`
	Title        string   `json:"title"`
	ImageCount   int      `json:"image_count"`
	FormURL      string   `json:"form_url"`
	PreviewImage string   `json:"preview_image,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
	Message      string   `json:"message"`
}

type PublishResult struct {
	Status   string `json:"status"`
	TopicURL string `json:"topic_url"`
	Message  string `json:"message"`
}

func NewAutomation(config Config) *Automation {
	return &Automation{
		Config:     config,
		Downloader: NewImageDownloader(),
		State:      StateStore{Path: config.StatePath},
	}
}

// SetProxy updates the tenant-specific Postiz relay endpoint before any
// ChineseInLA browser operation. Only a loopback HTTP proxy is accepted so an
// MCP caller cannot turn the browser into an arbitrary network proxy client.
func (a *Automation) SetProxy(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || strings.ToLower(parsed.Scheme) != "http" || parsed.Hostname() != "127.0.0.1" {
		return errors.New("ChineseInLA runtime proxy must be an HTTP URL on 127.0.0.1")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1024 || port > 65535 || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("ChineseInLA runtime proxy must contain only a valid loopback host and unprivileged port")
	}
	a.Config.Proxy = fmt.Sprintf("http://127.0.0.1:%d", port)
	return nil
}

func (a *Automation) Login(ctx context.Context) (LoginStatus, error) {
	browser, err := a.connect(ctx)
	if err != nil {
		return LoginStatus{}, err
	}
	page, err := openPage(browser, LoginURL, a.Config.Timeout)
	if err != nil {
		return LoginStatus{}, err
	}
	info, _ := page.Info()
	currentURL := LoginURL
	if info != nil {
		currentURL = info.URL
	}
	message := "Complete login in the dedicated ChineseInLA browser window, then run check-login."
	if a.Config.Headless {
		message = "The login page is open in headless Chromium. Use the MCP or protected HTTP login-session workflow to submit credentials to this page."
	}
	return LoginStatus{
		LoggedIn: false,
		URL:      currentURL,
		Message:  message,
	}, nil
}

func (a *Automation) CheckLogin(ctx context.Context) (LoginStatus, error) {
	browser, err := a.connect(ctx)
	if err != nil {
		return LoginStatus{}, err
	}
	// The public news homepage is separate from the forum application and does
	// not reliably render forum account controls even when the forum session is
	// authenticated. My Topics is session-gated: anonymous visitors are sent to
	// the login page, while authenticated visitors remain on the account page.
	page, err := openPage(browser, MyTopicsURL, a.Config.Timeout)
	if err != nil {
		return LoginStatus{}, err
	}
	loggedIn, currentURL, err := pageLoginStatus(page)
	if err != nil {
		return LoginStatus{}, err
	}
	if loggedIn {
		if err := a.persistCookies(browser); err != nil {
			return LoginStatus{}, err
		}
	}
	message := "ChineseInLA login is active and synchronized to its dedicated cookie store."
	if !loggedIn {
		message = "Not logged in. Run login and complete sign-in in the dedicated browser window."
		if a.Config.Headless {
			message = "Not logged in. Start a retained ChineseInLA headless login session and submit credentials through the protected endpoint."
		}
	}
	return LoginStatus{LoggedIn: loggedIn, URL: currentURL, Message: message}, nil
}

func (a *Automation) Forums(ctx context.Context) ([]Forum, error) {
	browser, err := a.connect(ctx)
	if err != nil {
		return nil, err
	}
	forums, err := a.forumsWithBrowser(browser)
	if err != nil {
		return nil, err
	}
	if err := a.persistCookies(browser); err != nil {
		return nil, err
	}
	return forums, nil
}

func (a *Automation) forumsWithBrowser(browser *rod.Browser) ([]Forum, error) {
	page, err := openPage(browser, ForumSelectorURL, a.Config.Timeout)
	if err != nil {
		return nil, err
	}
	if err := detectHumanVerification(page); err != nil {
		return nil, err
	}
	htmlSource, err := page.HTML()
	if err != nil {
		return nil, fmt.Errorf("read forum catalog: %w", err)
	}
	return ParseForums(strings.NewReader(htmlSource))
}

func (a *Automation) Prepare(ctx context.Context, request PrepareRequest) (PrepareResult, error) {
	warnings, err := request.NormalizeAndValidate()
	if err != nil {
		return PrepareResult{}, err
	}
	draftID, err := newDraftID()
	if err != nil {
		return PrepareResult{}, err
	}

	browser, err := a.connect(ctx)
	if err != nil {
		return PrepareResult{}, err
	}
	forums, err := a.forumsWithBrowser(browser)
	if err != nil {
		return PrepareResult{}, err
	}
	forum, ok := findForum(forums, request.ForumID)
	if !ok {
		return PrepareResult{}, fmt.Errorf("forum ID %d is not present in the live ChineseInLA catalog", request.ForumID)
	}
	if forum.Restricted {
		warnings = append(warnings, "The selected forum appears restricted or sponsor-only; verify that this account is allowed to post there.")
	}

	formURL := fmt.Sprintf("%s/f/page_pppping/mode_newtopic/f_%d.html", BaseURL, forum.ID)
	page, err := openPage(browser, formURL, a.Config.Timeout)
	if err != nil {
		return PrepareResult{}, err
	}
	loggedIn, currentURL, err := pageLoginStatus(page)
	if err != nil {
		return PrepareResult{}, err
	}
	if !loggedIn && !strings.Contains(currentURL, "page_login") {
		hasForm, _, hasErr := page.Has(`input[name="subject"]`)
		loggedIn = hasErr == nil && hasForm
	}
	if !loggedIn || strings.Contains(currentURL, "page_login") {
		return PrepareResult{}, ErrNotLoggedIn
	}
	if err := detectHumanVerification(page); err != nil {
		return PrepareResult{}, err
	}

	downloader := a.Downloader
	if downloader == nil {
		downloader = NewImageDownloader()
	}
	images, err := downloader.Prepare(ctx, request.Images, request.ImageURLs, request.SourceURL)
	if err != nil {
		return PrepareResult{}, err
	}
	defer images.Cleanup()

	if err := fillPostForm(page, request, images.Paths, a.Config.Timeout); err != nil {
		return PrepareResult{}, err
	}
	previewImage := ""
	message := "The form is filled in the dedicated browser. Review it there; publishing still requires publish --confirm."
	if a.Config.Headless {
		previewImage, err = saveHeadlessPreview(page, a.Config.PreviewPath)
		if err != nil {
			return PrepareResult{}, err
		}
		message = "The form is filled in headless Chromium. Review preview_image; publishing still requires a separate publish --confirm."
	}
	pageInfo, err := page.Info()
	if err != nil {
		return PrepareResult{}, fmt.Errorf("identify prepared browser tab: %w", err)
	}
	state := PreparedState{
		DraftID:      draftID,
		ForumID:      forum.ID,
		ForumName:    forum.Name,
		FormURL:      formURL,
		TargetID:     string(pageInfo.TargetID),
		Title:        request.Title,
		ImageCount:   len(images.Paths),
		Headless:     a.Config.Headless,
		PreviewImage: previewImage,
		CreatedAt:    time.Now().UTC(),
	}
	if err := a.State.Save(state); err != nil {
		return PrepareResult{}, err
	}
	if err := a.persistCookies(browser); err != nil {
		return PrepareResult{}, err
	}

	return PrepareResult{
		Status:       "ready_to_preview",
		DraftID:      draftID,
		Forum:        forum,
		PostType:     request.PostType,
		PostTypeName: request.PostType.Label(),
		Title:        request.Title,
		ImageCount:   len(images.Paths),
		FormURL:      formURL,
		PreviewImage: previewImage,
		Warnings:     warnings,
		Message:      message,
	}, nil
}

func (a *Automation) Publish(ctx context.Context, confirmed bool) (PublishResult, error) {
	return a.publish(ctx, "", confirmed, false)
}

// PublishPrepared publishes only the exact draft returned by Prepare. MCP uses
// this method so a second client cannot silently replace and publish another
// client's prepared form between the two confirmation steps.
func (a *Automation) PublishPrepared(ctx context.Context, draftID string, confirmed bool) (PublishResult, error) {
	draftID = strings.TrimSpace(draftID)
	if draftID == "" {
		return PublishResult{}, errors.New("draft_id is required; use the value returned by chineseinla_prepare_post")
	}
	return a.publish(ctx, draftID, confirmed, true)
}

func (a *Automation) publish(ctx context.Context, expectedDraftID string, confirmed, requireDraftID bool) (PublishResult, error) {
	if !confirmed {
		return PublishResult{}, errors.New("refusing to publish without the literal --confirm flag")
	}
	state, err := a.State.Load()
	if err != nil {
		return PublishResult{}, err
	}
	if requireDraftID {
		if state.DraftID == "" {
			return PublishResult{}, errors.New("the prepared post predates draft IDs; prepare it again before publishing through MCP")
		}
		if state.DraftID != expectedDraftID {
			return PublishResult{}, errors.New("draft_id does not match the currently prepared ChineseInLA post; review and confirm the current draft instead")
		}
	}
	if state.Headless != a.Config.Headless {
		preparedMode := "visible"
		requestedMode := "visible"
		if state.Headless {
			preparedMode = "headless"
		}
		if a.Config.Headless {
			requestedMode = "headless"
		}
		return PublishResult{}, fmt.Errorf("the post was prepared in %s mode; rerun publish in %s mode instead of %s mode", preparedMode, preparedMode, requestedMode)
	}
	browser, err := a.connect(ctx)
	if err != nil {
		return PublishResult{}, err
	}
	page, err := findPreparedPage(browser, state)
	if err != nil {
		return PublishResult{}, err
	}
	if err := detectSiteRejection(page); err != nil {
		return PublishResult{}, err
	}
	if err := detectHumanVerification(page); err != nil {
		return PublishResult{}, err
	}
	if err := prepareImageSubmission(page, state.ImageCount); err != nil {
		return PublishResult{}, err
	}
	preparedTitle := state.Title
	if titleField, titleErr := page.Element(`input[name="subject"]`); titleErr == nil {
		if value, valueErr := titleField.Eval(`() => this.value`); valueErr == nil {
			if currentTitle := strings.TrimSpace(value.Value.Str()); currentTitle != "" {
				preparedTitle = currentTitle
			}
		}
	}
	existingTopicURLs, snapshotErr := a.publishedTopicURLs(browser, preparedTitle)

	if _, err := page.Eval(`() => {
		window.__chineseInLALastDialog = "";
		window.__chineseInLAOriginalAlert = window.__chineseInLAOriginalAlert || window.alert;
		window.alert = function(message) {
			window.__chineseInLALastDialog = String(message || "");
		};
	}`); err != nil {
		return PublishResult{}, fmt.Errorf("install form validation monitor: %w", err)
	}

	submit, err := page.Timeout(a.Config.Timeout).Element(`a[href="javascript:beforePost()"]`)
	if err != nil {
		return PublishResult{}, fmt.Errorf("find ChineseInLA publish control: %w", err)
	}
	// The site's cookie banner can overlap the submit anchor in headless mode.
	// Dispatch the anchor's native DOM click so the existing beforePost()
	// validation still runs without waiting for pointer hit-testing to succeed.
	if _, err := submit.Eval(`() => this.click()`); err != nil {
		return PublishResult{}, fmt.Errorf("click ChineseInLA publish control: %w", err)
	}

	deadline := time.Now().Add(a.Config.Timeout)
	nextPublishedTopicCheck := time.Time{}
	var publishedTopicCheckErr error
	for time.Now().Before(deadline) {
		if rejectionErr := detectSiteRejection(page); rejectionErr != nil {
			restoreAlert(page)
			return PublishResult{}, rejectionErr
		}
		info, infoErr := page.Info()
		if infoErr == nil && info != nil && isChineseInLAURL(info.URL, "/f/page_viewtopic") {
			if err := a.persistCookies(browser); err != nil {
				return PublishResult{}, err
			}
			return a.completePublish(state, info.URL, "ChineseInLA accepted the post and redirected to the topic page.")
		}
		if infoErr == nil && info != nil && snapshotErr == nil &&
			!isChineseInLAURL(info.URL, "/mode_newtopic/") && time.Now().After(nextPublishedTopicCheck) {
			topicURL, checkErr := a.findNewPublishedTopic(browser, preparedTitle, existingTopicURLs)
			publishedTopicCheckErr = checkErr
			if checkErr == nil && topicURL != "" {
				if err := a.persistCookies(browser); err != nil {
					return PublishResult{}, err
				}
				return a.completePublish(state, topicURL, "ChineseInLA accepted the post and listed it under the account's published topics.")
			}
			nextPublishedTopicCheck = time.Now().Add(2 * time.Second)
		}
		if verificationErr := detectHumanVerification(page); errors.Is(verificationErr, ErrCaptcha) {
			restoreAlert(page)
			return PublishResult{}, verificationErr
		}
		time.Sleep(500 * time.Millisecond)
	}

	message := ""
	if result, evalErr := page.Eval(`() => {
		const message = window.__chineseInLALastDialog || "";
		if (window.__chineseInLAOriginalAlert) {
			window.alert = window.__chineseInLAOriginalAlert;
			delete window.__chineseInLAOriginalAlert;
		}
		return message;
	}`); evalErr == nil {
		message = strings.TrimSpace(result.Value.Str())
	}
	if message != "" {
		return PublishResult{}, fmt.Errorf("ChineseInLA rejected the form: %s", message)
	}
	if snapshotErr != nil {
		return PublishResult{}, fmt.Errorf("ChineseInLA did not redirect to a topic page, and the pre-publish topic list could not be read for fallback verification: %w; inspect My Published Topics before retrying", snapshotErr)
	}
	if publishedTopicCheckErr != nil {
		return PublishResult{}, fmt.Errorf("ChineseInLA left the form, but the published-topic list could not be verified: %w; inspect My Published Topics before retrying", publishedTopicCheckErr)
	}
	return PublishResult{}, errors.New("ChineseInLA did not expose a new topic URL; inspect My Published Topics before retrying so the post is not duplicated")
}

func newDraftID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate prepared-post draft ID: %w", err)
	}
	return hex.EncodeToString(random[:]), nil
}

func (a *Automation) completePublish(state PreparedState, topicURL, message string) (PublishResult, error) {
	var cleanupWarnings []string
	if err := a.State.Clear(); err != nil {
		cleanupWarnings = append(cleanupWarnings, fmt.Sprintf("prepared state could not be cleared: %v", err))
	}
	if state.PreviewImage != "" {
		if err := os.Remove(state.PreviewImage); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupWarnings = append(cleanupWarnings, fmt.Sprintf("preview image could not be removed: %v", err))
		}
	}
	if len(cleanupWarnings) > 0 {
		message += " Cleanup warning: " + strings.Join(cleanupWarnings, "; ")
	}
	return PublishResult{Status: "published", TopicURL: topicURL, Message: message}, nil
}

func saveHeadlessPreview(page *rod.Page, destination string) (string, error) {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return "", errors.New("headless preview image path is empty")
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return "", fmt.Errorf("resolve headless preview image path: %w", err)
	}
	if err := ensureDirectory(filepath.Dir(absolute)); err != nil {
		return "", err
	}

	data, err := page.Screenshot(true, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		return "", fmt.Errorf("capture headless prepared-form preview: %w", err)
	}
	if err := os.WriteFile(absolute, data, 0o600); err != nil {
		return "", fmt.Errorf("write headless prepared-form preview: %w", err)
	}
	return absolute, nil
}

func (a *Automation) publishedTopicURLs(browser *rod.Browser, title string) (map[string]struct{}, error) {
	topics, err := a.loadPublishedTopics(browser)
	if err != nil {
		return nil, err
	}
	return topicURLsWithTitle(topics, title), nil
}

func (a *Automation) findNewPublishedTopic(browser *rod.Browser, title string, before map[string]struct{}) (string, error) {
	topics, err := a.loadPublishedTopics(browser)
	if err != nil {
		return "", err
	}
	return firstUnseenTopicURL(topics, title, before), nil
}

func (a *Automation) loadPublishedTopics(browser *rod.Browser) ([]publishedTopic, error) {
	timeout := a.Config.Timeout
	if timeout <= 0 || timeout > 10*time.Second {
		timeout = 10 * time.Second
	}
	page, err := openPage(browser, MyTopicsURL, timeout)
	if err != nil {
		return nil, fmt.Errorf("open published-topic list: %w", err)
	}
	defer func() { _ = page.Close() }()
	if err := detectHumanVerification(page); err != nil {
		return nil, err
	}
	loggedIn, _, err := pageLoginStatus(page)
	if err != nil {
		return nil, err
	}
	if !loggedIn {
		return nil, ErrNotLoggedIn
	}
	htmlSource, err := page.HTML()
	if err != nil {
		return nil, fmt.Errorf("read published-topic list: %w", err)
	}
	return parsePublishedTopics(strings.NewReader(htmlSource))
}

func (a *Automation) connect(ctx context.Context) (*rod.Browser, error) {
	if a.Config.CDPPort < 1 || a.Config.CDPPort > 65535 {
		return nil, errors.New("CDP port must be between 1 and 65535")
	}
	// Chromium can outlive the MCP process so that the isolated profile and
	// login session remain available. Reconnect to that loopback-only CDP
	// endpoint before trying to launch another process with the same profile.
	if controlURL, err := runningBrowserControlURL(ctx, a.Config.CDPPort); err == nil {
		browser, connectErr := connectRodBrowser(ctx, controlURL)
		if connectErr == nil {
			matches, configurationErr := browserMatchesLaunchConfiguration(
				browser,
				a.Config.ProfileDir,
				a.Config.Proxy,
			)
			if configurationErr != nil {
				return nil, configurationErr
			}
			if matches {
				if restoreErr := a.restoreCookiesOnce(browser, controlURL); restoreErr != nil {
					return nil, restoreErr
				}
				return browser, nil
			}
			// A browser started before CHINESEINLA_PROXY was configured would
			// otherwise silently send the operation through the server's public
			// IP. Replace only the browser using this integration's isolated
			// profile, then launch it below with the configured proxy flag.
			if closeErr := closeBrowserAndWait(ctx, browser, a.Config.CDPPort); closeErr != nil {
				return nil, closeErr
			}
		}
	}
	if err := ensureDirectory(a.Config.ProfileDir); err != nil {
		return nil, err
	}
	binary := a.Config.BrowserBin
	if binary == "" {
		var err error
		binary, err = xhsbrowser.EnsureBrowser()
		if err != nil {
			return nil, fmt.Errorf("locate the bundled browser: %w", err)
		}
	}

	browserLauncher := launcher.New().
		Bin(binary).
		UserDataDir(a.Config.ProfileDir).
		Headless(a.Config.Headless).
		Leakless(false).
		RemoteDebuggingPort(a.Config.CDPPort)
	if a.Config.Proxy != "" {
		browserLauncher.Proxy(a.Config.Proxy)
	}
	// Ubuntu cloud hosts commonly disable unprivileged user namespaces, which
	// leaves Chromium without a usable sandbox. Restrict this exception to the
	// unprivileged Linux headless process; CDP remains bound to loopback.
	if linuxHeadlessNeedsNoSandbox(runtime.GOOS, a.Config.Headless) {
		browserLauncher.NoSandbox(true)
	}
	controlURL, err := browserLauncher.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch or reconnect to the ChineseInLA browser: %w", err)
	}
	browser, err := connectRodBrowser(ctx, controlURL)
	if err != nil {
		return nil, fmt.Errorf("connect to the ChineseInLA browser: %w", err)
	}
	if err := a.restoreCookiesOnce(browser, controlURL); err != nil {
		return nil, err
	}
	return browser, nil
}

func browserMatchesLaunchConfiguration(browser *rod.Browser, profileDir, proxy string) (bool, error) {
	commandLine, err := proto.BrowserGetBrowserCommandLine{}.Call(browser)
	if err != nil {
		return false, fmt.Errorf("verify the running ChineseInLA browser proxy: %w", err)
	}
	return browserArgumentsMatchConfiguration(commandLine.Arguments, profileDir, proxy)
}

func browserArgumentsMatchConfiguration(arguments []string, profileDir, proxy string) (bool, error) {
	expectedProfile, err := filepath.Abs(profileDir)
	if err != nil {
		return false, fmt.Errorf("resolve the configured ChineseInLA browser profile: %w", err)
	}
	actualProfile, ok := commandLineFlagValue(arguments, "user-data-dir")
	if !ok {
		return false, errors.New("the ChineseInLA CDP port is occupied by a browser whose profile cannot be verified")
	}
	actualProfile, err = filepath.Abs(actualProfile)
	if err != nil {
		return false, fmt.Errorf("resolve the running ChineseInLA browser profile: %w", err)
	}
	if filepath.Clean(actualProfile) != filepath.Clean(expectedProfile) {
		return false, fmt.Errorf("the ChineseInLA CDP port is occupied by a different browser profile: %s", actualProfile)
	}

	actualProxy, _ := commandLineFlagValue(arguments, "proxy-server")
	return normalizeProxyURL(actualProxy) == normalizeProxyURL(proxy), nil
}

func commandLineFlagValue(arguments []string, name string) (string, bool) {
	prefix := "--" + name + "="
	standalone := "--" + name
	for index, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(argument, prefix)), true
		}
		if argument == standalone && index+1 < len(arguments) {
			return strings.TrimSpace(arguments[index+1]), true
		}
	}
	return "", false
}

func normalizeProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimSuffix(raw, "/")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	return parsed.String()
}

func closeBrowserAndWait(ctx context.Context, browser *rod.Browser, port int) error {
	closeErr := browser.Close()
	deadline := time.Now().Add(browserConnectTimeout)
	for time.Now().Before(deadline) {
		if _, err := runningBrowserControlURL(ctx, port); err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if closeErr != nil {
		return fmt.Errorf("restart the ChineseInLA browser with its configured proxy: %w", closeErr)
	}
	return errors.New("restart the ChineseInLA browser with its configured proxy: browser did not exit")
}

func linuxHeadlessNeedsNoSandbox(goos string, headless bool) bool {
	return goos == "linux" && headless
}

func connectRodBrowser(ctx context.Context, controlURL string) (*rod.Browser, error) {
	connecting := rod.New().Context(ctx).ControlURL(controlURL).Timeout(browserConnectTimeout)
	if err := connecting.Connect(); err != nil {
		return nil, err
	}
	return connecting.Context(ctx), nil
}

func runningBrowserControlURL(ctx context.Context, port int) (string, error) {
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 750 * time.Millisecond}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/json/version", port), nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("CDP version endpoint returned HTTP %d", response.StatusCode)
	}

	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&version); err != nil {
		return "", fmt.Errorf("decode CDP version endpoint: %w", err)
	}
	controlURL, err := url.Parse(strings.TrimSpace(version.WebSocketDebuggerURL))
	if err != nil {
		return "", fmt.Errorf("parse CDP WebSocket URL: %w", err)
	}
	host := strings.ToLower(controlURL.Hostname())
	if controlURL.Scheme != "ws" ||
		(host != "127.0.0.1" && host != "localhost" && host != "::1") ||
		controlURL.Port() != strconv.Itoa(port) ||
		!strings.HasPrefix(controlURL.EscapedPath(), "/devtools/browser/") ||
		controlURL.User != nil {
		return "", errors.New("CDP version endpoint returned a non-local browser URL")
	}
	return controlURL.String(), nil
}

func ensureDirectory(path string) error {
	if path == "" {
		return errors.New("browser profile directory is empty")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create browser profile directory: %w", err)
	}
	return nil
}

func openPage(browser *rod.Browser, targetURL string, timeout time.Duration) (*rod.Page, error) {
	page, err := browser.Page(proto.TargetCreateTarget{URL: targetURL})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", targetURL, err)
	}
	// Keep the timeout scoped to navigation. Returning the timed page makes the
	// deadline apply to the entire lifetime of the tab, so a later image upload
	// or user review can fail even though that individual operation is healthy.
	if err := page.Timeout(timeout).WaitLoad(); err != nil {
		return nil, fmt.Errorf("wait for %s: %w", targetURL, err)
	}
	return page, nil
}

func pageLoginStatus(page *rod.Page) (bool, string, error) {
	info, err := page.Info()
	if err != nil {
		return false, "", fmt.Errorf("read ChineseInLA page URL: %w", err)
	}
	htmlSource, err := page.HTML()
	if err != nil {
		return false, info.URL, fmt.Errorf("inspect ChineseInLA login state: %w", err)
	}
	if strings.Contains(info.URL, "page_login") || strings.Contains(info.URL, "/login.html") {
		return false, info.URL, nil
	}
	lower := strings.ToLower(htmlSource)
	authenticatedMarkers := []string{"退出登录", "logout", "page_privmsg", "page_profile"}
	for _, marker := range authenticatedMarkers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true, info.URL, nil
		}
	}
	return false, info.URL, nil
}

func detectHumanVerification(page *rod.Page) error {
	htmlSource, err := page.HTML()
	if err != nil {
		return fmt.Errorf("inspect ChineseInLA page: %w", err)
	}
	lower := strings.ToLower(htmlSource)
	markers := []string{"g-recaptcha", "hcaptcha", "cf-turnstile", "captcha-container", "人机验证"}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return ErrCaptcha
		}
	}
	return nil
}

func detectSiteRejection(page *rod.Page) error {
	result, err := page.Eval(`() => document.body ? document.body.innerText : ""`)
	if err != nil {
		// Navigation can briefly destroy the JavaScript execution context. The
		// publish loop will inspect the replacement document on its next pass.
		return nil
	}
	return chineseInLASiteRejection(result.Value.Str())
}

func chineseInLASiteRejection(pageText string) error {
	upper := strings.ToUpper(strings.Join(strings.Fields(pageText), " "))
	const marker = "YOUR ISP IS NOT ALLOWED"
	if !strings.Contains(upper, marker) {
		return nil
	}

	message := "ChineseInLA blocked this server's network provider during submission (YOUR ISP IS NOT ALLOWED)"
	if index := strings.Index(upper, "YOUR IP IS "); index >= 0 {
		fields := strings.Fields(upper[index+len("YOUR IP IS "):])
		if len(fields) > 0 {
			ip := strings.Trim(fields[0], ".,;:()[]{}")
			if parsed := net.ParseIP(ip); parsed != nil {
				message += "; rejected egress IP: " + parsed.String()
			}
		}
	}
	return errors.New(message + ". Configure CHINESEINLA_PROXY with an allowed egress and restart the ChineseInLA browser before preparing another post")
}

func findForum(forums []Forum, id int) (Forum, bool) {
	for _, forum := range forums {
		if forum.ID == id {
			return forum, true
		}
	}
	return Forum{}, false
}

func findPreparedPage(browser *rod.Browser, state PreparedState) (*rod.Page, error) {
	pages, err := browser.Pages()
	if err != nil {
		return nil, fmt.Errorf("list browser tabs: %w", err)
	}
	if state.TargetID != "" {
		for _, page := range pages {
			info, infoErr := page.Info()
			if infoErr == nil && info != nil && string(info.TargetID) == state.TargetID {
				if rejectionErr := detectSiteRejection(page); rejectionErr != nil {
					return nil, rejectionErr
				}
				if !isChineseInLAFormURL(info.URL, state.ForumID) {
					return nil, errors.New("the prepared browser tab has navigated away from the ChineseInLA form; run prepare again")
				}
				return page, nil
			}
		}
		return nil, errors.New("the exact prepared ChineseInLA form tab is no longer open; run prepare again")
	}
	for _, page := range pages {
		info, infoErr := page.Info()
		if infoErr != nil || info == nil {
			continue
		}
		if info.URL == state.FormURL || isChineseInLAFormURL(info.URL, state.ForumID) {
			return page, nil
		}
	}
	return nil, errors.New("the prepared ChineseInLA form tab is no longer open; run prepare again")
}

func isChineseInLAFormURL(raw string, forumID int) bool {
	markers := []string{
		fmt.Sprintf("/f/page_pppping/mode_newtopic/f_%d.html", forumID),
		fmt.Sprintf("/f/page_pppping/f_%d/mode_newtopic.html", forumID),
	}
	for _, marker := range markers {
		if isChineseInLAURL(raw, marker) {
			return true
		}
	}
	return false
}

func isChineseInLAURL(raw, pathMarker string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "www.chineseinla.com" && host != "chineseinla.com" {
		return false
	}
	return strings.Contains(parsed.Path, pathMarker)
}

func restoreAlert(page *rod.Page) {
	_, _ = page.Eval(`() => {
		if (window.__chineseInLAOriginalAlert) {
			window.alert = window.__chineseInLAOriginalAlert;
			delete window.__chineseInLAOriginalAlert;
		}
	}`)
}

func fillPostForm(page *rod.Page, request PrepareRequest, imagePaths []string, timeout time.Duration) error {
	title, err := page.Timeout(timeout).Element(`input[name="subject"]`)
	if err != nil {
		return fmt.Errorf("find title field: %w", err)
	}
	if err := replaceElementText(title, request.Title); err != nil {
		return fmt.Errorf("fill title: %w", err)
	}

	body, err := firstElement(page.Timeout(timeout), []string{
		`div#editor[contenteditable="true"]`,
		`#editor [contenteditable="true"]`,
		`div[contenteditable="true"]`,
	})
	if err != nil {
		return fmt.Errorf("find body editor: %w", err)
	}
	if err := replaceContentEditableText(body, request.FinalBody()); err != nil {
		return fmt.Errorf("fill body: %w", err)
	}

	typeSelector := fmt.Sprintf(`input[name="is_question"][value="%s"]`, request.PostType.FormValue())
	typeRadio, err := page.Timeout(timeout).Element(typeSelector)
	if err != nil {
		return fmt.Errorf("find post type %s: %w", request.PostType, err)
	}
	if err := typeRadio.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("select post type %s: %w", request.PostType, err)
	}

	if len(request.Tags) > 0 {
		tags, tagErr := page.Timeout(timeout).Element(`input#tags`)
		if tagErr != nil {
			return fmt.Errorf("find tags field: %w", tagErr)
		}
		if tagErr := replaceElementText(tags, strings.Join(request.Tags, ",")); tagErr != nil {
			return fmt.Errorf("fill tags: %w", tagErr)
		}
	}

	if len(imagePaths) > 0 {
		if err := uploadImages(page, imagePaths, timeout); err != nil {
			return err
		}
	}
	return nil
}

func replaceElementText(element *rod.Element, value string) error {
	if err := element.SelectAllText(); err != nil {
		return err
	}
	return element.Input(value)
}

func replaceContentEditableText(element *rod.Element, value string) error {
	if _, err := element.Eval(`() => {
		this.focus();
		const range = document.createRange();
		range.selectNodeContents(this);
		const selection = window.getSelection();
		selection.removeAllRanges();
		selection.addRange(range);
	}`); err != nil {
		return err
	}
	return element.Input(value)
}

func firstElement(page *rod.Page, selectors []string) (*rod.Element, error) {
	var lastErr error
	for _, selector := range selectors {
		element, err := page.Timeout(3 * time.Second).Element(selector)
		if err == nil {
			return element, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func uploadImages(page *rod.Page, paths []string, timeout time.Duration) error {
	existingImages, err := insertedImageCount(page)
	if err != nil {
		return fmt.Errorf("count existing ChineseInLA images: %w", err)
	}
	button, err := page.Timeout(timeout).Element(`input[type="button"][value="上传图片"]`)
	if err != nil {
		return fmt.Errorf("find upload-image button: %w", err)
	}
	if err := button.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("open image uploader: %w", err)
	}

	fileInput, err := page.Timeout(timeout).Element(`input.edui-f5image-file, input[name="upfile"][type="file"]`)
	if err != nil {
		return fmt.Errorf("find image file input: %w", err)
	}
	if err := fileInput.SetFiles(paths); err != nil {
		return fmt.Errorf("send images to ChineseInLA uploader: %w", err)
	}

	// The F5 UEditor uploader has existed in both auto-upload and explicit-start variants.
	// Click an explicit start control when present. Its confirm control is enabled even while
	// the upload is in progress, so wait for both the completed thumbnails and inactive mask
	// before clicking it; otherwise the dialog closes and its late callback never inserts the
	// image into #uploadBox.
	_, _ = clickControlByText(page, []string{"开始上传"}, 5*time.Second)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		ready, readyErr := imageUploadReady(page, len(paths))
		if readyErr != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if !ready {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		clicked, clickErr := clickControlByText(page, []string{"确定", "确认", "插入"}, 1500*time.Millisecond)
		if clickErr == nil && clicked {
			for time.Now().Before(deadline) {
				insertedImages, countErr := insertedImageCount(page)
				if countErr == nil && insertedImages >= existingImages+len(paths) {
					return nil
				}
				time.Sleep(300 * time.Millisecond)
			}
			return errors.New("ChineseInLA closed the image dialog without inserting every uploaded image into the post")
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("image upload did not finish within 90 seconds; inspect the visible uploader and run prepare again")
}

func imageUploadReady(page *rod.Page, expected int) (bool, error) {
	result, err := page.Timeout(2*time.Second).Eval(`(expected) => {
		const controls = Array.from(document.querySelectorAll('.edui-btn-primary')).filter((control) => {
			const rect = control.getBoundingClientRect();
			return String(control.innerText || control.textContent || '').trim() === '确认' && rect.width > 0 && rect.height > 0;
		});
		return controls.some((control) => {
			const dialog = control.closest('.edui-modal') || document;
			const mask = dialog.querySelector('.edui-f5image-mask, .edui-image-mask');
			const completed = dialog.querySelectorAll('.edui-f5image-upload-item, .edui-image-upload-item').length;
			return completed >= expected && (!mask || !mask.classList.contains('edui-active'));
		});
	}`, expected)
	if err != nil {
		return false, err
	}
	return result.Value.Bool(), nil
}

func insertedImageCount(page *rod.Page) (int, error) {
	result, err := page.Timeout(2 * time.Second).Eval(`() => {
		const uploaded = document.querySelectorAll('#uploadBox img.postingImg').length;
		const editor = document.querySelector('div#editor[contenteditable="true"], #editor [contenteditable="true"], div[contenteditable="true"]');
		return uploaded + (editor ? editor.querySelectorAll('img').length : 0);
	}`)
	if err != nil {
		return 0, err
	}
	return result.Value.Int(), nil
}

func prepareImageSubmission(page *rod.Page, expected int) error {
	if expected <= 0 {
		return nil
	}
	result, err := page.Timeout(3*time.Second).Eval(`(expected) => {
		const images = Array.from(document.querySelectorAll('#uploadBox .uploaded_img img'));
		const input = document.querySelector('#uploaded_img_input');
		if (!input || images.length < expected) {
			return { count: images.length, valueLength: 0 };
		}
		const html = images.map((image, index) => {
			image.removeAttribute('style');
			image.setAttribute('index', String(index + 1));
			return image.outerHTML;
		}).join('');
		input.value = html;
		return { count: images.length, valueLength: input.value.length };
	}`, expected)
	if err != nil {
		return fmt.Errorf("prepare ChineseInLA image submission metadata: %w", err)
	}
	data := result.Value.Map()
	count := data["count"].Int()
	valueLength := data["valueLength"].Int()
	if count < expected || valueLength == 0 {
		return fmt.Errorf("prepared ChineseInLA draft expected %d image(s), but only %d image(s) were ready for submission", expected, count)
	}
	return nil
}

func clickControlByText(page *rod.Page, names []string, timeout time.Duration) (bool, error) {
	result, err := page.Timeout(timeout).Eval(`(names) => {
		const controls = Array.from(document.querySelectorAll('button, a, input[type="button"], [role="button"], .edui-btn'));
		const control = controls.find((candidate) => {
			const text = String(candidate.innerText || candidate.textContent || candidate.value || '').trim();
			const rect = candidate.getBoundingClientRect();
			return names.includes(text) && !candidate.disabled && rect.width > 0 && rect.height > 0;
		});
		if (!control) return false;
		control.click();
		return true;
	}`, names)
	if err != nil {
		return false, err
	}
	return result.Value.Bool(), nil
}

package chineseinla

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if err := detectHumanVerification(page); err != nil {
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
	if err := submit.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return PublishResult{}, fmt.Errorf("click ChineseInLA publish control: %w", err)
	}

	deadline := time.Now().Add(a.Config.Timeout)
	nextPublishedTopicCheck := time.Time{}
	var publishedTopicCheckErr error
	for time.Now().Before(deadline) {
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
			if restoreErr := a.restoreCookiesOnce(browser, controlURL); restoreErr != nil {
				return nil, restoreErr
			}
			return browser, nil
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
	page = page.Timeout(timeout)
	if err := page.WaitLoad(); err != nil {
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
	forumMarker := fmt.Sprintf("/mode_newtopic/f_%d.html", state.ForumID)
	if state.TargetID != "" {
		for _, page := range pages {
			info, infoErr := page.Info()
			if infoErr == nil && info != nil && string(info.TargetID) == state.TargetID {
				if !isChineseInLAURL(info.URL, forumMarker) {
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
		if info.URL == state.FormURL || isChineseInLAURL(info.URL, forumMarker) {
			return page, nil
		}
	}
	return nil, errors.New("the prepared ChineseInLA form tab is no longer open; run prepare again")
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
	// Click an explicit start control when present, then wait for an enabled insert/confirm control.
	_, _ = clickControlByText(page, []string{"开始上传"}, 5*time.Second)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if err := detectHumanVerification(page); err != nil {
			return err
		}
		clicked, clickErr := clickControlByText(page, []string{"确定", "确认", "插入"}, 1500*time.Millisecond)
		if clickErr == nil && clicked {
			for time.Now().Before(deadline) {
				hasInput, input, hasErr := page.Has(`input.edui-f5image-file`)
				if hasErr == nil && !hasInput {
					return nil
				}
				if hasErr == nil && input != nil {
					visible, visibleErr := input.Visible()
					if visibleErr == nil && !visible {
						return nil
					}
				}
				time.Sleep(300 * time.Millisecond)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("image upload did not finish within 90 seconds; inspect the visible uploader and run prepare again")
}

func clickControlByText(page *rod.Page, names []string, timeout time.Duration) (bool, error) {
	controls, err := page.Timeout(timeout).Elements(`button, a, input[type="button"]`)
	if err != nil {
		return false, err
	}
	for _, control := range controls {
		text, textErr := control.Text()
		if textErr != nil {
			continue
		}
		if value, valueErr := control.Attribute("value"); valueErr == nil && value != nil && strings.TrimSpace(text) == "" {
			text = *value
		}
		text = strings.TrimSpace(text)
		for _, name := range names {
			if text != name {
				continue
			}
			visible, visibleErr := control.Visible()
			if visibleErr != nil || !visible {
				continue
			}
			if err := control.WaitEnabled(); err != nil {
				continue
			}
			if err := control.Click(proto.InputMouseButtonLeft, 1); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

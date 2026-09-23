package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/headless_browser"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/downloader"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/xhsutil"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// XiaohongshuService 小红书业务服务
type XiaohongshuService struct {
	logins loginSessions
}

// NewXiaohongshuService 创建小红书服务实例
func NewXiaohongshuService() *XiaohongshuService {
	return &XiaohongshuService{}
}

// PublishRequest 发布请求
type PublishRequest struct {
	Title      string   `json:"title" binding:"required"`
	Content    string   `json:"content" binding:"required"`
	Images     []string `json:"images" binding:"required,min=1"`
	Tags       []string `json:"tags,omitempty"`
	ScheduleAt string   `json:"schedule_at,omitempty"` // 定时发布时间，ISO8601格式，为空则立即发布
	IsOriginal bool     `json:"is_original,omitempty"` // 是否声明原创
	Visibility string   `json:"visibility,omitempty"`  // 可见范围: "公开可见"(默认), "仅自己可见", "仅互关好友可见"
	Products   []string `json:"products,omitempty"`    // 商品关键词列表，用于绑定带货商品
}

// LoginStatusResponse 登录状态响应
type LoginStatusResponse struct {
	IsLoggedIn bool   `json:"is_logged_in"`
	Username   string `json:"username,omitempty"` // 当前登录账号的昵称
	UserID     string `json:"user_id,omitempty"`  // 用户唯一标识（个人主页 URL 中的 ID）
}

// LoginQrcodeResponse 登录扫码二维码
type LoginQrcodeResponse struct {
	Timeout    string            `json:"timeout"`
	IsLoggedIn bool              `json:"is_logged_in"`
	Img        string            `json:"img,omitempty"`
	SessionID  string            `json:"session_id,omitempty"`
	State      LoginSessionState `json:"state,omitempty"`
	ExpiresAt  time.Time         `json:"expires_at,omitempty"`
}

// SubmitLoginCodeRequest submits an OTP to the browser page retained by a QR
// login session. Code is a string so leading zeroes are preserved.
type SubmitLoginCodeRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	Code      string `json:"code" binding:"required"`
}

// PublishResponse 发布响应
type PublishResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Images  int    `json:"images"`
	Status  string `json:"status"`
}

// PublishVideoRequest 发布视频请求（仅支持本地单个视频文件）
type PublishVideoRequest struct {
	Title      string   `json:"title" binding:"required"`
	Content    string   `json:"content" binding:"required"`
	Video      string   `json:"video" binding:"required"`
	Cover      string   `json:"cover,omitempty"` // 自定义封面本地图片路径，为空则用小红书默认首帧
	Tags       []string `json:"tags,omitempty"`
	ScheduleAt string   `json:"schedule_at,omitempty"` // 定时发布时间，ISO8601格式，为空则立即发布
	Visibility string   `json:"visibility,omitempty"`  // 可见范围: "公开可见"(默认), "仅自己可见", "仅互关好友可见"
	Products   []string `json:"products,omitempty"`    // 商品关键词列表，用于绑定带货商品
}

// PublishVideoResponse 发布视频响应
type PublishVideoResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Video   string `json:"video"`
	Status  string `json:"status"`
}

// FeedsListResponse Feeds列表响应
type FeedsListResponse struct {
	Feeds []xiaohongshu.Feed `json:"feeds"`
	Count int                `json:"count"`
}

// UserProfileResponse 用户主页响应
type UserProfileResponse struct {
	UserBasicInfo xiaohongshu.UserBasicInfo      `json:"userBasicInfo"`
	Interactions  []xiaohongshu.UserInteractions `json:"interactions"`
	Feeds         []xiaohongshu.Feed             `json:"feeds"`
}

// DeleteCookies 删除 cookies 文件，用于登录重置
func (s *XiaohongshuService) DeleteCookies(ctx context.Context) error {
	cookiePath := cookies.GetCookiesFilePath()
	cookieLoader := cookies.NewLoadCookie(cookiePath)
	return cookieLoader.DeleteCookies()
}

// CheckLoginStatus 检查登录状态
func (s *XiaohongshuService) CheckLoginStatus(ctx context.Context) (*LoginStatusResponse, error) {
	// A reconnect may have been performed by another process on this profile;
	// pick up the site it stamped before choosing where to look.
	xiaohongshu.SetSite(xiaohongshu.ResolveSite(configs.SiteKeyFromEnv(),
		cookies.NewLoadCookie(cookies.GetCookiesFilePath())))
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	loginAction := xiaohongshu.NewLogin(page)

	isLoggedIn, err := loginAction.CheckLoginStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w（site=%s）", err, xiaohongshu.CurrentSite().Key)
	}

	response := &LoginStatusResponse{
		IsLoggedIn: isLoggedIn,
	}

	// 已登录时从当前页读取真实账号信息；读不到只记 warn，不影响状态返回。
	if isLoggedIn {
		if user, err := loginAction.CurrentUser(ctx); err != nil {
			logrus.Warnf("failed to get current user info: %v", err)
		} else {
			response.Username = user.Nickname
			response.UserID = user.UserID
		}

		creatorLoggedIn, err := loginAction.CheckCreatorLoginStatus(ctx)
		if err != nil {
			// 发布页白屏只说明页面没渲染出来（代理慢、静态资源被拦），不是登录失效：
			// 唯一权威的失效信号是被重定向到登录页。主站已确认登录，这里不把白屏当作失败。
			if errors.Is(err, xiaohongshu.ErrCreatorPageBlank) {
				logrus.Warnf("创作中心发布页白屏，沿用主站登录判定（已登录）: %v", err)
				return response, nil
			}
			return nil, err
		}
		response.IsLoggedIn = creatorLoggedIn
	}

	return response, nil
}

// GetLoginQrcode 获取登录的扫码二维码
func (s *XiaohongshuService) GetLoginQrcode(ctx context.Context, visible bool) (*LoginQrcodeResponse, error) {
	b := newLoginBrowser(visible)
	page := b.NewPage()

	deferFunc := func() {
		_ = page.Close()
		b.Close()
	}

	loginAction := xiaohongshu.NewLogin(page)

	img, loggedIn, err := loginAction.FetchQrcodeImage(ctx)
	if err != nil || loggedIn {
		defer deferFunc()
	}
	if err != nil {
		return nil, err
	}

	timeout := 4 * time.Minute
	var session *loginSession

	if !loggedIn {
		session, err = s.waitScanInBackground(loginAction, page, deferFunc, timeout)
		if err != nil {
			deferFunc()
			return nil, err
		}
	}

	response := &LoginQrcodeResponse{
		Timeout: func() string {
			if loggedIn {
				return "0s"
			}
			return timeout.String()
		}(),
		Img:        img,
		IsLoggedIn: loggedIn,
	}
	if session != nil {
		response.SessionID = session.id
		// The background observer may already be updating session.state. The QR
		// response deliberately reports the initial state; callers use the
		// session-status endpoint for subsequent transitions.
		response.State = LoginSessionWaitingForScan
		response.ExpiresAt = session.expiresAt
	}
	return response, nil
}

const loginSessionNotReusableMessage = "扫码成功，但保存的会话在新浏览器中未被小红书接受（可能触发了账号风控）。请稍后再试，或先在手机 App 中确认设备安全提示。"

// savedSessionReusable opens a fresh browser from the cookie file, exactly as
// a publish would, and checks www.xiaohongshu.com for a signed-in user. One
// retry absorbs a slow first render; two misses mean Xiaohongshu is not
// honouring the session outside the browser that created it.
func (s *XiaohongshuService) savedSessionReusable(ctx context.Context, seq uint64) bool {
	for attempt := 1; attempt <= 2; attempt++ {
		var ok bool
		err := withBrowserPage(func(page *rod.Page) error {
			checkCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
			defer cancel()
			loggedIn, err := xiaohongshu.NewLogin(page).CheckLoginStatus(checkCtx)
			ok = loggedIn
			return err
		})
		logrus.Infof("保存后会话复用检查: 会话 #%d attempt=%d ok=%v err=%v", seq, attempt, ok, err)
		if ok {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// waitScanInBackground 在后台等用户扫码，扫上了就存 cookie。
//
// 浏览器必须一直活着才检测得到扫码，所以这里不能提前关；但也不能任由它堆积——
// 再取一次二维码就会把上一个还在等的会话关掉，同一时刻只留一个。
func (s *XiaohongshuService) waitScanInBackground(
	loginAction *xiaohongshu.LoginAction, page *rod.Page, closeBrowser func(), timeout time.Duration,
) (*loginSession, error) {
	// The context outlives the scan window by otpGrace so a session that has
	// been scanned can wait for the SMS code; an unscanned session is still cut
	// at `timeout` by the observer below.
	const otpGrace = 6 * time.Minute
	scanDeadline := time.Now().Add(timeout)
	ctxTimeout, cancel := context.WithTimeout(context.Background(), timeout+otpGrace)
	session, err := s.logins.start(cancel, loginAction.SubmitVerificationCode, scanDeadline)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create login session: %w", err)
	}
	session.captureChallenge = loginAction.CaptureSecurityVerification
	session.requestCode = loginAction.RequestVerificationCode
	logrus.Infof("等待扫码登录，会话 #%d，超时 %s", session.seq, timeout)

	pre, _ := loginAction.SessionCookieDigest()
	logrus.Infof("扫码前 cookie: 会话 #%d %s", session.seq, pre)

	go func() {
		defer session.withPageOperation(closeBrowser)
		defer cancel()

		// Log and photograph every state transition: "scanned but no OTP
		// prompt" is invisible in the UI without this.
		var lastState xiaohongshu.LoginPageState
		var scanSeenAt, lastTick time.Time
		autoSent := false
		if loginAction.WaitForLoginWithState(ctxTimeout, func(observation xiaohongshu.LoginObservation) {
			if observation.State != lastState {
				logrus.Infof("登录页状态变化: 会话 #%d %s -> %s message=%q dom=%s",
					session.seq, lastState, observation.State, observation.Message, observation.Debug)
				loginAction.Capture("state_" + string(observation.State))
				lastState = observation.State
				if observation.State == xiaohongshu.LoginPageOTPRequired && !autoSent {
					// Make sure the SMS is actually requested: the modal's
					// 发送/获取/重新获取 control is what triggers it.
					autoSent = true
					go func() {
						time.Sleep(2 * time.Second)
						clicked, phone, err := loginAction.RequestVerificationCode(ctxTimeout)
						logrus.Infof("首次要求验证码，自动点击发送: 会话 #%d clicked=%q phone=%q err=%v", session.seq, clicked, phone, err)
					}()
				}
				if scanSeenAt.IsZero() && observation.State != xiaohongshu.LoginPageWaitingForScan {
					// Scanned (or straight into OTP/captcha): give the user time
					// for the SMS round-trip.
					scanSeenAt = time.Now()
					until := s.logins.extend(session, time.Now().Add(otpGrace))
					logrus.Infof("扫码已确认，会话 #%d 有效期延长至 %s", session.seq, until.Format(time.RFC3339))
				}
			}
			if scanSeenAt.IsZero() && time.Now().After(scanDeadline) {
				cancel() // no scan within the QR window: end as before
				return
			}
			// After a scan, keep a frame + DOM summary every 15s so a prompt we
			// fail to classify is still on record.
			if !scanSeenAt.IsZero() && time.Since(lastTick) > 15*time.Second {
				lastTick = time.Now()
				logrus.Infof("扫码后观察: 会话 #%d state=%s dom=%s", session.seq, observation.State, observation.Debug)
				loginAction.Capture("post_scan_tick")
			}
			s.logins.observe(session, mapLoginPageState(observation.State), observation.Message)
		}) {
			var saveErr error
			var post xiaohongshu.SessionFacts
			session.withPageOperation(func() {
				loginAction.Capture("authenticated")
				post, _ = loginAction.SessionCookieDigest()
				logrus.Infof("扫码后 cookie: 会话 #%d %s xhs_web_session_changed=%v",
					session.seq, post, post.XHSWebSession != pre.XHSWebSession)
				saveErr = saveCookies(page)
				loginAction.Capture("cookies_saved")
			})
			if saveErr != nil {
				s.logins.finish(session, LoginSessionFailed, "save cookies failed")
				logrus.Errorf("扫码成功但保存 cookies 失败，会话 #%d: %v", session.seq, saveErr)
				return
			}

			// Where did the login land? Xiaohongshu moves overseas-held accounts
			// to rednote.com after the scan. Stamp the site on the session file
			// and switch the process to it so the reuse check below, and every
			// later publish, use the right hosts. Re-derived on every login, so a
			// CN account reconnecting on an INTL profile flips back cleanly.
			site := xiaohongshu.SiteForFacts(post)
			if err := cookies.NewLoadCookie(cookies.GetCookiesFilePath()).SaveSite(site.Key); err != nil {
				logrus.Warnf("保存站点失败: %v", err)
			}
			xiaohongshu.SetSite(site)
			logrus.Infof("登录站点判定: 会话 #%d site=%s holderctry=%q datactry=%q",
				session.seq, site.Key, post.HolderCountry, post.DataCountry)

			// The scan only proves the login browser is signed in. Postiz's next
			// step opens a fresh browser from the saved file, so do that here and
			// report a reusable-session failure with its own message instead of a
			// misleading "authenticated" followed by "not logged in".
			if !s.savedSessionReusable(ctxTimeout, session.seq) {
				s.logins.finish(session, LoginSessionFailed, loginSessionNotReusableMessage)
				logrus.Errorf("扫码成功但保存的会话在新浏览器中不可用，会话 #%d", session.seq)
				return
			}

			s.logins.finish(session, LoginSessionAuthenticated, "")
			logrus.Infof("扫码登录成功，cookies 已保存，会话 #%d", session.seq)
			return
		}

		// 没等到扫码：要么超时，要么被新取的二维码取代
		if ctxTimeout.Err() == context.DeadlineExceeded {
			s.logins.finish(session, LoginSessionExpired, "")
		} else {
			s.logins.finish(session, LoginSessionCancelled, "")
		}
		logrus.Infof("登录会话 #%d 结束，未检测到扫码（超时或已被新的二维码取代）", session.seq)
	}()

	return session, nil
}

func mapLoginPageState(state xiaohongshu.LoginPageState) LoginSessionState {
	switch state {
	case xiaohongshu.LoginPageQRScanned:
		return LoginSessionQRScanned
	case xiaohongshu.LoginPageOTPRequired:
		return LoginSessionOTPRequired
	case xiaohongshu.LoginPageCaptchaNeeded:
		return LoginSessionCaptchaNeeded
	case xiaohongshu.LoginPageAuthenticated:
		return LoginSessionAuthenticated
	default:
		return LoginSessionWaitingForScan
	}
}

// GetLoginSessionStatus returns the latest state observed on the retained page.
func (s *XiaohongshuService) GetLoginSessionStatus(ctx context.Context, sessionID string) (*LoginSessionStatus, error) {
	status, err := s.logins.captureSecurityChallenge(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &status, nil
}

// SubmitLoginCode enters the OTP into the same headless page that generated the
// QR code. The code is validated before it reaches the browser session manager.
// ResendLoginCode clicks the SMS modal's resend control for the session.
func (s *XiaohongshuService) ResendLoginCode(ctx context.Context, sessionID string) (*LoginSessionStatus, string, string, error) {
	status, clicked, phone, err := s.logins.resendCode(ctx, sessionID)
	return &status, clicked, phone, err
}

func (s *XiaohongshuService) SubmitLoginCode(ctx context.Context, req SubmitLoginCodeRequest) (*LoginSessionStatus, error) {
	if err := xiaohongshu.ValidateVerificationCode(req.Code); err != nil {
		return nil, err
	}
	status, err := s.logins.submitCode(ctx, req.SessionID, req.Code)
	if err != nil {
		return &status, err
	}
	return &status, nil
}

// PublishContent 发布内容
func (s *XiaohongshuService) PublishContent(ctx context.Context, req *PublishRequest) (*PublishResponse, error) {
	// 验证标题长度（小红书限制：最大20个字）
	if xhsutil.CalcTitleLength(req.Title) > 20 {
		return nil, fmt.Errorf("标题长度超过限制")
	}

	imagePaths, err := s.processImages(req.Images)
	if err != nil {
		return nil, err
	}

	var scheduleTime *time.Time
	if req.ScheduleAt != "" {
		t, err := time.Parse(time.RFC3339, req.ScheduleAt)
		if err != nil {
			return nil, fmt.Errorf("定时发布时间格式错误，请使用 ISO8601 格式: %v", err)
		}

		// 校验定时发布时间范围：1小时至14天
		now := time.Now()
		minTime := now.Add(1 * time.Hour)
		maxTime := now.Add(14 * 24 * time.Hour)

		if t.Before(minTime) {
			return nil, fmt.Errorf("定时发布时间必须至少在1小时后，当前设置: %s，最早可选: %s",
				t.Format("2006-01-02 15:04"), minTime.Format("2006-01-02 15:04"))
		}
		if t.After(maxTime) {
			return nil, fmt.Errorf("定时发布时间不能超过14天，当前设置: %s，最晚可选: %s",
				t.Format("2006-01-02 15:04"), maxTime.Format("2006-01-02 15:04"))
		}

		scheduleTime = &t
		logrus.Infof("设置定时发布时间: %s", t.Format("2006-01-02 15:04"))
	}

	content := xiaohongshu.PublishImageContent{
		Title:        req.Title,
		Content:      req.Content,
		Tags:         req.Tags,
		ImagePaths:   imagePaths,
		ScheduleTime: scheduleTime,
		IsOriginal:   req.IsOriginal,
		Visibility:   req.Visibility,
		Products:     req.Products,
	}

	if err := s.publishContent(ctx, content); err != nil {
		logrus.Errorf("发布内容失败: title=%s %v", content.Title, err)
		return nil, err
	}

	response := &PublishResponse{
		Title:   req.Title,
		Content: req.Content,
		Images:  len(imagePaths),
		Status:  "发布完成",
	}

	return response, nil
}

// processImages 处理图片列表，支持URL下载和本地路径
func (s *XiaohongshuService) processImages(images []string) ([]string, error) {
	processor := downloader.NewImageProcessor()
	return processor.ProcessImages(images)
}

// publishContent 执行内容发布
func (s *XiaohongshuService) publishContent(ctx context.Context, content xiaohongshu.PublishImageContent) error {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action, err := xiaohongshu.NewPublishImageAction(page)
	if err != nil {
		return err
	}

	err = action.Publish(ctx, content)
	if errors.Is(err, xiaohongshu.ErrCreatorSessionExpired) {
		cookieLoader := cookies.NewLoadCookie(cookies.GetCookiesFilePath())
		if deleteErr := cookieLoader.DeleteCookies(); deleteErr != nil {
			logrus.Errorf("creator session expired; failed to invalidate saved cookies: %v", deleteErr)
		} else {
			logrus.Warn("creator session expired; invalidated saved cookies so the next check requires reconnect")
		}
	}
	return err
}

// PublishVideo 发布视频（本地文件）
func (s *XiaohongshuService) PublishVideo(ctx context.Context, req *PublishVideoRequest) (*PublishVideoResponse, error) {
	// 标题长度校验（小红书限制：最大20个字）
	if xhsutil.CalcTitleLength(req.Title) > 20 {
		return nil, fmt.Errorf("标题长度超过限制")
	}

	// 本地视频文件校验
	if req.Video == "" {
		return nil, fmt.Errorf("必须提供本地视频文件")
	}
	if _, err := os.Stat(req.Video); err != nil {
		return nil, fmt.Errorf("视频文件不存在或不可访问: %v", err)
	}

	var scheduleTime *time.Time
	if req.ScheduleAt != "" {
		t, err := time.Parse(time.RFC3339, req.ScheduleAt)
		if err != nil {
			return nil, fmt.Errorf("定时发布时间格式错误，请使用 ISO8601 格式: %v", err)
		}

		// 校验定时发布时间范围：1小时至14天
		now := time.Now()
		minTime := now.Add(1 * time.Hour)
		maxTime := now.Add(14 * 24 * time.Hour)

		if t.Before(minTime) {
			return nil, fmt.Errorf("定时发布时间必须至少在1小时后，当前设置: %s，最早可选: %s",
				t.Format("2006-01-02 15:04"), minTime.Format("2006-01-02 15:04"))
		}
		if t.After(maxTime) {
			return nil, fmt.Errorf("定时发布时间不能超过14天，当前设置: %s，最晚可选: %s",
				t.Format("2006-01-02 15:04"), maxTime.Format("2006-01-02 15:04"))
		}

		scheduleTime = &t
		logrus.Infof("设置定时发布时间: %s", t.Format("2006-01-02 15:04"))
	}

	content := xiaohongshu.PublishVideoContent{
		Title:        req.Title,
		Content:      req.Content,
		Tags:         req.Tags,
		VideoPath:    req.Video,
		CoverPath:    req.Cover,
		ScheduleTime: scheduleTime,
		Visibility:   req.Visibility,
		Products:     req.Products,
	}

	if err := s.publishVideo(ctx, content); err != nil {
		return nil, err
	}

	resp := &PublishVideoResponse{
		Title:   req.Title,
		Content: req.Content,
		Video:   req.Video,
		Status:  "发布完成",
	}
	return resp, nil
}

// publishVideo 执行视频发布
func (s *XiaohongshuService) publishVideo(ctx context.Context, content xiaohongshu.PublishVideoContent) error {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action, err := xiaohongshu.NewPublishVideoAction(page)
	if err != nil {
		return err
	}

	return action.PublishVideo(ctx, content)
}

// ListFeeds 获取Feeds列表
func (s *XiaohongshuService) ListFeeds(ctx context.Context) (*FeedsListResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewFeedsListAction(page)

	feeds, err := action.GetFeedsList(ctx)
	if err != nil {
		logrus.Errorf("获取 Feeds 列表失败: %v", err)
		return nil, err
	}

	response := &FeedsListResponse{
		Feeds: feeds,
		Count: len(feeds),
	}

	return response, nil
}

func (s *XiaohongshuService) SearchFeeds(ctx context.Context, keyword string, filters ...xiaohongshu.FilterOption) (*FeedsListResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewSearchAction(page)

	feeds, err := action.Search(ctx, keyword, filters...)
	if err != nil {
		return nil, err
	}

	response := &FeedsListResponse{
		Feeds: feeds,
		Count: len(feeds),
	}

	return response, nil
}

// GetFeedDetail 获取Feed详情
func (s *XiaohongshuService) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool) (*FeedDetailResponse, error) {
	return s.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, xiaohongshu.DefaultCommentLoadConfig())
}

// GetFeedDetailWithConfig 使用配置获取Feed详情
func (s *XiaohongshuService) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config xiaohongshu.CommentLoadConfig) (*FeedDetailResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewFeedDetailAction(page)

	result, err := action.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, config)
	if err != nil {
		return nil, err
	}

	response := &FeedDetailResponse{
		FeedID: feedID,
		Data:   result,
	}

	return response, nil
}

// UserProfile 获取用户信息
func (s *XiaohongshuService) UserProfile(ctx context.Context, userID, xsecToken, tab string) (*UserProfileResponse, error) {
	parsed, err := xiaohongshu.ParseProfileTab(tab)
	if err != nil {
		return nil, err
	}

	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewUserProfileAction(page)

	result, err := action.UserProfile(ctx, userID, xsecToken, parsed)
	if err != nil {
		return nil, err
	}
	response := &UserProfileResponse{
		UserBasicInfo: result.UserBasicInfo,
		Interactions:  result.Interactions,
		Feeds:         result.Feeds,
	}

	return response, nil

}

// PostCommentToFeed 发表评论到Feed
func (s *XiaohongshuService) PostCommentToFeed(ctx context.Context, feedID, xsecToken, content string) (*PostCommentResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewCommentFeedAction(page)

	if err := action.PostComment(ctx, feedID, xsecToken, content); err != nil {
		return nil, err
	}

	return &PostCommentResponse{FeedID: feedID, Success: true, Message: "评论发表成功"}, nil
}

// LikeFeed 点赞笔记
func (s *XiaohongshuService) LikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewLikeAction(page)
	if err := action.Like(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "点赞成功或已点赞"}, nil
}

// UnlikeFeed 取消点赞笔记
func (s *XiaohongshuService) UnlikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewLikeAction(page)
	if err := action.Unlike(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "取消点赞成功或未点赞"}, nil
}

// FavoriteFeed 收藏笔记
func (s *XiaohongshuService) FavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewFavoriteAction(page)
	if err := action.Favorite(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "收藏成功或已收藏"}, nil
}

// UnfavoriteFeed 取消收藏笔记
func (s *XiaohongshuService) UnfavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewFavoriteAction(page)
	if err := action.Unfavorite(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "取消收藏成功或未收藏"}, nil
}

// ReplyCommentToFeed 回复指定评论
func (s *XiaohongshuService) ReplyCommentToFeed(ctx context.Context, feedID, xsecToken, commentID, userID, content string) (*ReplyCommentResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewCommentFeedAction(page)

	if err := action.ReplyToComment(ctx, feedID, xsecToken, commentID, userID, content); err != nil {
		return nil, err
	}

	return &ReplyCommentResponse{
		FeedID:          feedID,
		TargetCommentID: commentID,
		TargetUserID:    userID,
		Success:         true,
		Message:         "评论回复成功",
	}, nil
}

// GetUnreadCount 获取通知未读数
func (s *XiaohongshuService) GetUnreadCount(ctx context.Context) (*xiaohongshu.NotificationCount, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	return xiaohongshu.NewNotificationAction(page).UnreadCount(ctx)
}

// ListNotifications 获取指定分区的通知列表
func (s *XiaohongshuService) ListNotifications(ctx context.Context, tab string, limit int) (*xiaohongshu.NotificationList, error) {
	parsed, err := xiaohongshu.ParseNotificationTab(tab)
	if err != nil {
		return nil, err
	}

	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	return xiaohongshu.NewNotificationAction(page).List(ctx, parsed, limit)
}

// LikeNotification 给通知里的评论点赞或取消点赞
func (s *XiaohongshuService) LikeNotification(ctx context.Context, commentID string, unlike bool) (*xiaohongshu.NotificationLikeResult, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	return xiaohongshu.NewNotificationAction(page).Like(ctx, commentID, unlike)
}

// ReplyNotification 在通知页就地回复评论
func (s *XiaohongshuService) ReplyNotification(ctx context.Context, commentID, content string) (*xiaohongshu.NotificationReplyResult, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	return xiaohongshu.NewNotificationAction(page).Reply(ctx, commentID, content)
}

func newBrowser() *headless_browser.Browser {
	return browser.NewBrowser(configs.IsHeadless(),
		browser.WithFingerprintSeed(configs.FingerprintSeed()),
		browser.WithProxy(configs.Proxy()),
	)
}

// newLoginBrowser builds the browser for an interactive login. When visible is
// true the browser runs headful (on the process DISPLAY) so a human can scan
// and type directly — e.g. through an embedded VNC view — while publishing and
// the default QR-relay login stay headless. visible=false keeps the existing
// behaviour exactly.
func newLoginBrowser(visible bool) *headless_browser.Browser {
	headless := configs.IsHeadless()
	if visible {
		headless = false
	}
	return browser.NewBrowser(headless,
		browser.WithFingerprintSeed(configs.FingerprintSeed()),
		browser.WithProxy(configs.Proxy()),
	)
}

func saveCookies(page *rod.Page) error {
	cks, err := page.Browser().GetCookies()
	if err != nil {
		return err
	}

	data, err := json.Marshal(cks)
	if err != nil {
		return err
	}

	cookieLoader := cookies.NewLoadCookie(cookies.GetCookiesFilePath())
	if err := cookieLoader.SaveCookies(data); err != nil {
		return err
	}
	// A first login on a profile (or a legacy file that was removed outright)
	// has no seed on disk yet, while this process already runs with one. Persist
	// it so a restart keeps the fingerprint the login was performed under.
	if cookieLoader.LoadSeed() <= 0 {
		if seed := configs.FingerprintSeed(); seed > 0 {
			if err := cookieLoader.SaveSeed(seed); err != nil {
				logrus.Warnf("保存会话 seed 失败: %v", err)
			}
		}
	}
	return nil
}

// withBrowserPage 执行需要浏览器页面的操作的通用函数
func withBrowserPage(fn func(*rod.Page) error) error {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	return fn(page)
}

// GetMyProfile 获取当前登录用户的个人信息
func (s *XiaohongshuService) GetMyProfile(ctx context.Context, tab string) (*UserProfileResponse, error) {
	parsed, err := xiaohongshu.ParseProfileTab(tab)
	if err != nil {
		return nil, err
	}

	var result *xiaohongshu.UserProfileResponse

	err = withBrowserPage(func(page *rod.Page) error {
		action := xiaohongshu.NewUserProfileAction(page)
		result, err = action.GetMyProfileViaSidebar(ctx, parsed)
		return err
	})

	if err != nil {
		return nil, err
	}

	response := &UserProfileResponse{
		UserBasicInfo: result.UserBasicInfo,
		Interactions:  result.Interactions,
		Feeds:         result.Feeds,
	}

	return response, nil
}

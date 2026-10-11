// Added in ycl-2004/xiaohongshu-mcp; page probe adapted from liaogx/xiaohongshu-mcp
// (xiaohongshu/search.go readSearchPageProbe, Apache-2.0); see NOTICE.

package xiaohongshu

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-rod/rod"
	"github.com/xpzouying/xiaohongshu-mcp/site"
)

// 页面访问拦截的错误码。调用方遇到这些要停下等人处理，而不是重试。
const (
	CodeSecurityVerification = "SECURITY_VERIFICATION_REQUIRED" // 平台要求人工安全验证
	CodeLoginRequired        = "LOGIN_REQUIRED"                 // 登录失效或显示登录窗口
	CodeAccountRestricted    = "ACCOUNT_RESTRICTED"             // 账号被提示有风险或受限
	CodeRateLimited          = "RATE_LIMITED"                   // 访问或操作过于频繁
)

// PageAccessError 页面被平台拦下。消息里不带 URL、令牌或二维码。
type PageAccessError struct {
	Code string
}

func (e *PageAccessError) Error() string {
	return e.Code + ": 小红书拦下了这次访问，暂停读写，等人处理后再继续"
}

// PageAccess 页面访问状态的最小快照：只有状态和命中的提示词，不含正文、Cookie 或令牌。
type PageAccess struct {
	Path                 string   `json:"path"`
	SecurityVerification bool     `json:"security_verification"`
	LoginVisible         bool     `json:"login_visible"`
	Authenticated        bool     `json:"authenticated"`
	Guest                bool     `json:"guest"`
	Markers              []string `json:"markers"`
}

var (
	restrictedMarkers  = []string{"该账号疑似存在风险", "账号异常", "账号已被封禁", "账号已被限制"}
	rateLimitedMarkers = []string{"访问频繁", "操作频繁", "请求太频繁", "评论过于频繁"}
)

// Err 把快照归成一个错误码；没被拦就返回 nil。安全验证优先于其他判断。
func (a *PageAccess) Err() error {
	has := func(markers []string) bool {
		for _, m := range a.Markers {
			for _, want := range markers {
				if m == want {
					return true
				}
			}
		}
		return false
	}
	switch {
	case a.SecurityVerification:
		return &PageAccessError{Code: CodeSecurityVerification}
	case a.LoginVisible || a.Guest:
		return &PageAccessError{Code: CodeLoginRequired}
	case has(restrictedMarkers):
		return &PageAccessError{Code: CodeAccountRestricted}
	case has(rateLimitedMarkers):
		return &PageAccessError{Code: CodeRateLimited}
	}
	return nil
}

// ProbePageAccess 读取当前页面是否处于安全验证、登录窗口、受限或限流状态。
func ProbePageAccess(page *rod.Page) (*PageAccess, error) {
	markers := append(append([]string{"安全验证", "验证后继续", "验证码"}, restrictedMarkers...), rateLimitedMarkers...)
	result, err := page.Eval(`(markers) => {
		const body = (document.body && document.body.innerText) || "";
		const state = window.__INITIAL_STATE__;
		const unwrap = x => x && (x.value !== undefined ? x.value : x._value !== undefined ? x._value : x);
		const info = unwrap(state && state.user && state.user.userInfo);
		const login = document.querySelector('.login-container');
		const loginVisible = !!login && login.getClientRects().length > 0 && getComputedStyle(login).visibility !== 'hidden';
		const security = /\/website-login\/(captcha|verify|error)(\/|$)/.test(location.pathname)
			|| /安全验证|身份验证/.test(document.title)
			|| (!state && /扫码验证身份|验证后继续/.test(body));
		return JSON.stringify({
			path: location.pathname,
			security_verification: security,
			login_visible: loginVisible,
			authenticated: !!(info && !info.guest && (info.userId || info.user_id)),
			guest: !!(info && info.guest === true),
			markers: markers.filter(m => body.includes(m))
		});
	}`, markers)
	if err != nil {
		return nil, err
	}
	var access PageAccess
	if err := json.Unmarshal([]byte(result.Value.Str()), &access); err != nil {
		return nil, err
	}
	return &access, nil
}

// ProbeExplore 打开发现页并读取访问状态：操作失败后用它判断是不是被平台拦了。
func ProbeExplore(ctx context.Context, page *rod.Page) (*PageAccess, error) {
	p := page.Context(ctx).Timeout(60 * time.Second)
	if err := p.Navigate(site.Current().WebURL("/explore")); err != nil {
		return nil, err
	}
	if err := p.WaitLoad(); err != nil {
		return nil, err
	}
	// 等页面注水；拦截页没有 #app 也要能读，所以只等固定时间。
	time.Sleep(3 * time.Second)
	return ProbePageAccess(p)
}

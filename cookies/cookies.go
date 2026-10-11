package cookies

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/xpzouying/xiaohongshu-mcp/site"
)

// sessionFile 是 v2 的文件结构。v1 是一个裸 cookie 数组，没有外层对象。
// cookies 用 RawMessage 原样透传，不解析不重组，避免往返时字段走样。
type sessionFile struct {
	Version int             `json:"version"`
	Seed    int             `json:"seed,omitempty"`
	Site    string          `json:"site,omitempty"` // "cn" | "intl"（老版本写 "rednote"）：会话属于哪个站点；没有这个字段的老文件都是中文站
	SavedAt string          `json:"saved_at,omitempty"`
	Cookies json.RawMessage `json:"cookies"`
}

// ErrSiteMismatch 会话文件属于另一个站点。中文站和海外站是两套登录状态，不能混用。
// Added in ycl-2004/xiaohongshu-mcp; see NOTICE.
var ErrSiteMismatch = stderrors.New("会话文件属于另一个站点")

// siteOf 会话文件属于哪个站点；不是能认出的会话格式时 known 为 false。
func siteOf(data []byte) (name string, known bool) {
	var f sessionFile
	if err := json.Unmarshal(data, &f); err == nil {
		if f.Site == "" {
			return site.CN.Name, true
		}
		return f.Site, true
	}
	var v1 []json.RawMessage
	if err := json.Unmarshal(data, &v1); err == nil {
		return site.CN.Name, true
	}
	return "", false
}

// checkSite 钉住站点时，会话文件不属于当前站点就返回 ErrSiteMismatch；没钉住时不查（登录会重新判定站点）。
func checkSite(data []byte) error {
	if !site.Pinned() {
		return nil
	}
	name, known := siteOf(data)
	if known && !site.SameSite(name, site.Current().Name) {
		return fmt.Errorf("%w：文件是 %s 的，当前站点是 %s", ErrSiteMismatch, name, site.Current().Name)
	}
	return nil
}

// localCookiesPath 当前目录下的默认文件名。
const localCookiesPath = "cookies.json"

type Cookier interface {
	LoadCookies() ([]byte, error)
	SaveCookies(data []byte) error
	DeleteCookies() error
	// LoadSeed 读取会话绑定的 seed；老格式、文件损坏或未设时返回 0。
	LoadSeed() int
	// SaveSeed 写入 seed，保留文件中已有的 cookies。
	SaveSeed(seed int) error
	// LoadSite 读取上次登录所在站点（"cn"/"intl"）；未设或文件损坏返回 ""。
	LoadSite() string
	// SaveSite 写入站点，保留文件中已有的 cookies 和 seed。
	SaveSite(site string) error
}

type localCookie struct {
	path string
}

func NewLoadCookie(path string) Cookier {
	if path == "" {
		panic("path is required")
	}

	return &localCookie{
		path: path,
	}
}

// LoadCookies 从文件中加载 cookies 数组的原始字节。
// v2 从外层对象里取出 cookies 字段；v1 文件本身就是数组，原样返回。
func (c *localCookie) LoadCookies() ([]byte, error) {

	data, err := os.ReadFile(c.path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read cookies from tmp file")
	}
	if err := checkSite(data); err != nil {
		return nil, err
	}

	var f sessionFile
	if err := json.Unmarshal(data, &f); err == nil && f.Version >= 2 {
		if !hasCookieEntries(f.Cookies) {
			// v2 文件存在但 cookies 为空：账号已登出，seed 仍在。
			return nil, ErrNoCookies
		}
		return f.Cookies, nil
	}

	return data, nil
}

// ErrNoCookies 表示会话文件存在但没有任何 cookie（已登出但保留了设备 seed）。
var ErrNoCookies = errors.New("no cookies saved")

// hasCookieEntries 判断 cookies 字段是否真的有条目（不是空、null 或 []）。
func hasCookieEntries(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null" && trimmed != "[]"
}

// LoadSeed 读取会话绑定的 seed。老格式（裸数组）没有这个值，返回 0；钉住站点时另一个站点的会话也返回 0。
func (c *localCookie) LoadSeed() int {
	data, err := os.ReadFile(c.path)
	if err != nil || checkSite(data) != nil {
		return 0
	}

	var f sessionFile
	if err := json.Unmarshal(data, &f); err != nil {
		return 0
	}
	return f.Seed
}

// SaveCookies 保存 cookies 到文件中，保留文件里已有的 seed。
func (c *localCookie) SaveCookies(data []byte) error {
	return c.write(data, c.LoadSeed(), c.LoadSite())
}

// LoadSite 读取会话所属站点。老格式或未设返回 ""。
func (c *localCookie) LoadSite() string {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return ""
	}
	var f sessionFile
	if err := json.Unmarshal(data, &f); err != nil {
		return ""
	}
	if s, err := site.Parse(f.Site); err == nil && f.Site != "" {
		return s.Key // 老版本写的 "rednote" 读成 "intl"
	}
	return f.Site
}

// SaveSite 写入站点，保留文件里已有的 cookies 和 seed。
func (c *localCookie) SaveSite(site string) error {
	cks, err := c.LoadCookies()
	if err != nil {
		cks = nil
	}
	return c.write(cks, c.LoadSeed(), site)
}

// SaveSeed 写入 seed，保留文件里已有的 cookies。
func (c *localCookie) SaveSeed(seed int) error {
	cks, err := c.LoadCookies()
	if err != nil {
		cks = nil // 文件还不存在：先把 seed 落下来，cookies 之后再补
	}
	return c.write(cks, seed, c.LoadSite())
}

// write 以 v2 格式落盘。cookies 用 RawMessage 原样嵌入，不经过结构体往返。
// 钉住站点时：记下当前站点；已有的文件属于另一个站点时拒绝写，不覆盖别的站点的会话。
func (c *localCookie) write(cks []byte, seed int, siteKey string) error {
	if site.Pinned() {
		if existing, err := os.ReadFile(c.path); err == nil {
			if err := checkSite(existing); err != nil {
				return err
			}
		}
		siteKey = site.Current().Key
	}
	if len(cks) == 0 {
		cks = []byte("[]")
	}

	data, err := json.MarshalIndent(sessionFile{
		Version: 2,
		Seed:    seed,
		Site:    siteKey,
		SavedAt: time.Now().Format(time.RFC3339),
		Cookies: json.RawMessage(cks),
	}, "", "  ")
	if err != nil {
		return errors.Wrap(err, "marshal session file failed")
	}

	return writePrivateAtomic(c.path, data)
}

// writePrivateAtomic 以 0600 原子写入：先写同目录临时文件再改名，崩溃时不会留下半个会话文件。
// Modified for ycl-2004/xiaohongshu-mcp（上游直接 0644 覆盖写）；see NOTICE.
func writePrivateAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.Wrap(err, "create cookies dir failed")
	}
	f, err := os.CreateTemp(dir, ".cookies-*")
	if err != nil {
		return errors.Wrap(err, "create temp cookies file failed")
	}
	defer os.Remove(f.Name()) // 改名成功后它已不存在，删除无害
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return errors.Wrap(err, "chmod cookies file failed")
	}
	if _, err := f.Write(data); err != nil {
		return errors.Wrap(err, "write cookies file failed")
	}
	if err := f.Sync(); err != nil {
		return errors.Wrap(err, "sync cookies file failed")
	}
	if err := f.Close(); err != nil {
		return errors.Wrap(err, "close cookies file failed")
	}
	return os.Rename(f.Name(), path)
}

// DeleteCookies 登出：清空 cookies，但保留会话文件里的设备 seed。
//
// seed 决定浏览器指纹。以前这里直接删文件，seed 跟着丢，下一次扫码登录就换了
// 一套指纹——同一个账号在小红书看来每次重连都是"新设备"，很快会被风控在
// 发布时 401 踢下线。登出只该丢凭证，不该丢设备身份。
// 没有 seed 的老文件仍然整个删除，行为与之前一致。
func (c *localCookie) DeleteCookies() error {
	if _, err := os.Stat(c.path); os.IsNotExist(err) {
		// 文件不存在，返回 nil（认为已经删除）
		return nil
	}
	seed := c.LoadSeed()
	if seed <= 0 {
		return os.Remove(c.path)
	}
	// 站点一并保留：下次扫码前就能直接去对的域名取二维码；登录后会重新判定。
	return c.write(nil, seed, c.LoadSite())
}

// GetCookiesFilePath 获取 cookies 文件路径：COOKIES_PATH 优先，否则当前目录下的 cookies.json。
// Modified for ycl-2004/xiaohongshu-mcp：去掉了上游回落到共享 /tmp/cookies.json 的旧路径。
func GetCookiesFilePath() string {
	// 显式指定优先，无条件——环境里的残留文件不能盖掉用户明说的配置
	if path := os.Getenv("COOKIES_PATH"); path != "" {
		return path
	}
	return localCookiesPath
}

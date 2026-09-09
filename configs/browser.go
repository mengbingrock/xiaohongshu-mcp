package configs

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
)

var (
	useHeadless = true

	fingerprintSeed = 0

	proxy = ""
)

func InitHeadless(h bool) {
	useHeadless = h
}

// IsHeadless 是否无头模式。
func IsHeadless() bool {
	return useHeadless
}

func SetFingerprintSeed(s int) {
	fingerprintSeed = s
}

func FingerprintSeed() int {
	return fingerprintSeed
}

// FingerprintSeedFromEnv 从 XHS_FP_SEED 环境变量解析固定 seed。
// 未设或非法返回 0（回退随机）。env 读取集中在配置层，浏览器工厂只收 Option。
func FingerprintSeedFromEnv() int {
	s := os.Getenv("XHS_FP_SEED")
	if s == "" {
		return 0
	}
	seed, err := strconv.Atoi(s)
	if err != nil || seed <= 0 {
		logrus.Warnf("invalid XHS_FP_SEED=%q, ignored (fallback to random seed)", s)
		return 0
	}
	return seed
}

func SetProxy(p string) {
	proxy = p
}

func Proxy() string {
	return proxy
}

// SetRuntimeProxy 运行期切换小红书浏览器的出口代理（Postiz 拿到租约后调用）。
// 只接受 127.0.0.1 上的 HTTP 回环代理（非特权端口、无凭据/路径），空串表示直连。
// 浏览器按操作即建即销，改动对下一次打开的浏览器生效。
func SetRuntimeProxy(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		proxy = ""
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || strings.ToLower(parsed.Scheme) != "http" || parsed.Hostname() != "127.0.0.1" {
		return errors.New("runtime proxy must be an HTTP URL on 127.0.0.1")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1024 || port > 65535 || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("runtime proxy must contain only a valid loopback host and unprivileged port")
	}
	proxy = fmt.Sprintf("http://127.0.0.1:%d", port)
	return nil
}

// ProxyFromEnv 从 XHS_PROXY 环境变量读取代理地址。env 读取集中在配置层。
func ProxyFromEnv() string {
	return os.Getenv("XHS_PROXY")
}

// SiteKeyFromEnv 从 XHS_SITE 读取站点覆盖（"cn" 或 "intl"）。未设或非法返回 ""。
// 与 seed 一样，环境变量优先于会话文件：用于强制某个部署走指定站点。
func SiteKeyFromEnv() string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("XHS_SITE")))
	switch v {
	case "", "cn", "intl":
		return v
	default:
		logrus.Warnf("invalid XHS_SITE=%q, ignored (site resolved from session file)", v)
		return ""
	}
}

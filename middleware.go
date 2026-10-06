package main

// Modified for ycl-2004/xiaohongshu-mcp; see NOTICE. 上游是一枚可为空的 token 加 CORS *；
// 给了读、写 token 时是严格模式：两枚必填、只听本机、不开 CORS、/mcp 默认关（调用方只有本机进程）。
// 两枚都没给时沿用原来的单 token（AUTH_TOKEN，可为空）、CORS 和 /mcp，原来的部署不受影响。

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// minTokenLength 两枚 token 的最短长度，防止弱口令。
const minTokenLength = 32

// accessConfig 鉴权配置。严格模式：读 token 只能调读路由，写 token 什么都能调。
// 兼容模式（读、写 token 都没给）：一枚 LegacyToken 管所有路由，为空时不鉴权。
type accessConfig struct {
	ReadToken   string
	WriteToken  string
	EnableMCP   bool
	LegacyToken string
}

// strict 给了读或写 token 就是严格模式。
func (a accessConfig) strict() bool {
	return a.ReadToken != "" || a.WriteToken != ""
}

// legacyAccess 兼容模式的配置：单 token，/mcp 照开。
func legacyAccess(token string) accessConfig {
	return accessConfig{LegacyToken: token, EnableMCP: true}
}

// validate 启动前检查。严格模式：两枚都要有、够长、互不相同（上游 token 为空时会放行所有请求）；
// 兼容模式不检查。
func (a accessConfig) validate() error {
	if !a.strict() {
		return nil
	}
	for name, token := range map[string]string{"读": a.ReadToken, "写": a.WriteToken} {
		if len(token) < minTokenLength {
			return fmt.Errorf("%s token 少于 %d 个字符，拒绝启动", name, minTokenLength)
		}
	}
	if a.ReadToken == a.WriteToken {
		return fmt.Errorf("读、写 token 不能相同，拒绝启动")
	}
	return nil
}

// validateListen 只允许监听本机回环地址。
func validateListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("监听地址 %q 无效: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("只允许监听本机回环地址，%q 不是", addr)
	}
	return nil
}

type scope int

const (
	scopeRead scope = iota
	scopeWrite
)

// scopeMiddleware 读路由接受读或写 token；写路由只接受写 token。
// 没带或不认识的 token 返回 401；读 token 访问写路由返回 403。
func scopeMiddleware(cfg accessConfig, need scope) gin.HandlerFunc {
	read, write := []byte(cfg.ReadToken), []byte(cfg.WriteToken)
	return func(c *gin.Context) {
		scheme, credentials, found := strings.Cut(c.GetHeader("Authorization"), " ")
		given := []byte(strings.TrimLeft(credentials, " "))
		isWrite := found && strings.EqualFold(scheme, "Bearer") && subtle.ConstantTimeCompare(given, write) == 1
		isRead := found && strings.EqualFold(scheme, "Bearer") && subtle.ConstantTimeCompare(given, read) == 1
		switch {
		case isWrite || (isRead && need == scopeRead):
			c.Next()
		case isRead:
			respondError(c, http.StatusForbidden, "FORBIDDEN", "只读 token 不能调用写操作", nil)
			c.Abort()
		default:
			c.Header("WWW-Authenticate", "Bearer")
			respondError(c, http.StatusUnauthorized, "UNAUTHORIZED", "未授权", nil)
			c.Abort()
		}
	}
}

// authMiddleware 兼容模式的静态 Bearer Token 鉴权中间件，Token 为空时关闭鉴权。
func authMiddleware(token string) gin.HandlerFunc {
	expectedToken := []byte(token)

	return func(c *gin.Context) {
		if token == "" {
			c.Next()
			return
		}

		scheme, credentials, found := strings.Cut(c.GetHeader("Authorization"), " ")
		credentials = strings.TrimLeft(credentials, " ")
		if !found || !strings.EqualFold(scheme, "Bearer") ||
			subtle.ConstantTimeCompare([]byte(credentials), expectedToken) != 1 {
			c.Header("WWW-Authenticate", "Bearer")
			respondError(c, http.StatusUnauthorized, "UNAUTHORIZED", "未授权", nil)
			c.Abort()
			return
		}

		c.Next()
	}
}

// accessMiddleware 按模式挑鉴权：严格模式按读 / 写分，兼容模式一枚 token 管所有。
func accessMiddleware(cfg accessConfig, need scope) gin.HandlerFunc {
	if cfg.strict() {
		return scopeMiddleware(cfg, need)
	}
	return authMiddleware(cfg.LegacyToken)
}

// corsMiddleware 兼容模式的 CORS 中间件（严格模式不开）
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// errorHandlingMiddleware 错误处理中间件
func errorHandlingMiddleware() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {
		logrus.Errorf("服务器内部错误: %v, path: %s", recovered, c.Request.URL.Path)

		respondError(c, http.StatusInternalServerError, "INTERNAL_ERROR",
			"服务器内部错误", recovered)
	})
}

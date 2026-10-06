package main

// Modified for ycl-2004/xiaohongshu-mcp; see NOTICE. 只注册 Marketing_Agent 用到的路由，按读、写分组；
// /mcp 默认关闭。上游的二维码登录、发视频、首页推荐、点赞收藏、通知回复和点赞路由不注册
// （对应的 MCP 工具仍在，开启 -enable-mcp 后可用，且需要写 token）。

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// setupRoutes 设置路由配置
func setupRoutes(appServer *AppServer) *gin.Engine {
	// 设置 Gin 模式
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	// 添加中间件
	router.Use(errorHandlingMiddleware())
	cfg := appServer.access
	if !cfg.strict() {
		router.Use(corsMiddleware())
	}

	// 健康检查
	router.GET("/health", healthHandler)

	if cfg.EnableMCP {
		// MCP 端点 - 使用官方 SDK 的 Streamable HTTP Handler
		mcpHandler := mcp.NewStreamableHTTPHandler(
			func(r *http.Request) *mcp.Server {
				return appServer.mcpServer
			},
			&mcp.StreamableHTTPOptions{
				JSONResponse: true, // 支持 JSON 响应
				// 换取客户端可跳过 initialize 握手直接调工具。代价是服务端无法反向
				// 请求客户端（sampling / elicitation / roots），眼下一处都没用到；
				// 要用得先摘掉这行。
				Stateless: true,
			},
		)
		mcpGroup := router.Group("", accessMiddleware(cfg, scopeWrite))
		mcpGroup.Any("/mcp", gin.WrapH(mcpHandler))
		mcpGroup.Any("/mcp/*path", gin.WrapH(mcpHandler))
	}

	// 读路由：严格模式下读、写 token 都能调
	read := router.Group("/api/v1", accessMiddleware(cfg, scopeRead))
	{
		read.GET("/login/status", appServer.checkLoginStatusHandler)
		read.GET("/feeds/list", appServer.listFeedsHandler)
		read.GET("/feeds/search", appServer.searchFeedsHandler)
		read.POST("/feeds/search", appServer.searchFeedsHandler)
		read.POST("/feeds/detail", appServer.getFeedDetailHandler)
		read.POST("/user/profile", appServer.userProfileHandler)
		read.GET("/user/me", appServer.myProfileHandler)
		read.GET("/notifications/unread", appServer.getUnreadCountHandler)
		read.GET("/notifications/list", appServer.listNotificationsHandler)
		read.POST("/notifications/list", appServer.listNotificationsHandler)
	}

	// 写路由：严格模式下只认写 token。登录也算写：它会换掉这个号的会话
	write := router.Group("/api/v1", accessMiddleware(cfg, scopeWrite))
	{
		write.GET("/login/qrcode", appServer.getLoginQrcodeHandler)
		write.GET("/login/sessions/:session_id", appServer.getLoginSessionStatusHandler)
		write.POST("/login/code", appServer.submitLoginCodeHandler)
		write.DELETE("/login/cookies", appServer.deleteCookiesHandler)
		write.POST("/chineseinla/login/start", appServer.startChineseInLALoginHandler)
		write.GET("/chineseinla/login/sessions/:session_id", appServer.getChineseInLALoginSessionHandler)
		write.POST("/chineseinla/login/password", appServer.submitChineseInLALoginPasswordHandler)
		write.POST("/publish", appServer.publishHandler)
		write.POST("/publish_video", appServer.publishVideoHandler)
		write.POST("/feeds/comment", appServer.postCommentHandler)
		write.POST("/feeds/comment/reply", appServer.replyCommentHandler)
		write.POST("/feeds/like", appServer.likeFeedHandler)
		write.POST("/feeds/favorite", appServer.favoriteFeedHandler)
		write.POST("/notifications/reply", appServer.replyNotificationHandler)
		write.POST("/notifications/like", appServer.likeNotificationHandler)
	}

	return router
}

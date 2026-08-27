package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/internal/chineseinla"
)

// ChineseInLAService is the subset of the ChineseInLA automation exposed by MCP.
// Keeping it as an interface makes the confirmation and draft-safety behavior
// testable without starting a browser.
type ChineseInLAService interface {
	StartLogin(context.Context) (chineseinla.LoginSessionStatus, error)
	GetLoginSession(context.Context, string) (chineseinla.LoginSessionStatus, error)
	SubmitPasswordLogin(context.Context, chineseinla.PasswordLoginRequest) (chineseinla.LoginSessionStatus, error)
	CloseLoginSession()
	CheckLogin(context.Context) (chineseinla.LoginStatus, error)
	Forums(context.Context) ([]chineseinla.Forum, error)
	ListPosts(context.Context, chineseinla.ListPostsRequest) (chineseinla.ListPostsResult, error)
	ReadPost(context.Context, chineseinla.ReadPostRequest) (chineseinla.ReadPostResult, error)
	Prepare(context.Context, chineseinla.PrepareRequest) (chineseinla.PrepareResult, error)
	PublishPrepared(context.Context, string, bool) (chineseinla.PublishResult, error)
}

// AppServer 应用服务器结构体，封装所有服务和处理器
type AppServer struct {
	xiaohongshuService *XiaohongshuService
	chineseInLAService ChineseInLAService
	chineseInLAMu      sync.Mutex
	mcpServer          *mcp.Server
	router             *gin.Engine
	httpServer         *http.Server
	authToken          string
}

// NewAppServer 创建新的应用服务器实例
func NewAppServer(xiaohongshuService *XiaohongshuService, authToken string) *AppServer {
	return NewAppServerWithChineseInLA(xiaohongshuService, nil, authToken)
}

// NewAppServerWithChineseInLA creates one MCP server hosting both Xiaohongshu
// and ChineseInLA tools. Each service keeps its own browser configuration.
func NewAppServerWithChineseInLA(xiaohongshuService *XiaohongshuService, chineseInLAService ChineseInLAService, authToken string) *AppServer {
	appServer := &AppServer{
		xiaohongshuService: xiaohongshuService,
		chineseInLAService: chineseInLAService,
		authToken:          authToken,
	}

	// 初始化 MCP Server（需要在创建 appServer 之后，因为工具注册需要访问 appServer）
	appServer.mcpServer = InitMCPServer(appServer)

	return appServer
}

// Start 启动服务器
func (s *AppServer) Start(port string) error {
	s.router = setupRoutes(s)

	s.httpServer = &http.Server{
		Addr:    port,
		Handler: s.router,
	}

	// 启动服务器的 goroutine
	go func() {
		logrus.Infof("启动 HTTP 服务器: %s", port)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logrus.Errorf("服务器启动失败: %v", err)
			os.Exit(1)
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logrus.Infof("正在关闭服务器...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.httpServer.Shutdown(ctx); err != nil {
		logrus.Warnf("等待连接关闭超时，强制退出: %v", err)
	} else {
		logrus.Infof("服务器已优雅关闭")
	}
	if s.chineseInLAService != nil {
		s.chineseInLAService.CloseLoginSession()
	}

	return nil
}

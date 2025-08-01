package main

import (
	"embed"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"hubproxy/config"
	"hubproxy/handlers"
	"hubproxy/utils"
)

//go:embed public/*
var staticFiles embed.FS

// 服务嵌入的静态文件
func serveEmbedFile(c *gin.Context, filename string) {
	data, err := staticFiles.ReadFile(filename)
	if err != nil {
		c.Status(404)
		return
	}
	contentType := "text/html; charset=utf-8"
	if strings.HasSuffix(filename, ".ico") {
		contentType = "image/x-icon"
	}
	c.Data(200, contentType, data)
}

var (
	globalLimiter *utils.IPRateLimiter

	// 服务启动时间
	serviceStartTime = time.Now()
)

func main() {
	// 加载配置
	if err := config.LoadConfig(); err != nil {
		fmt.Printf("配置加载失败: %v\n", err)
		return
	}

	// 初始化HTTP客户端
	utils.InitHTTPClients()

	// 初始化限流器
	globalLimiter = utils.InitGlobalLimiter()

	// 初始化Docker流式代理
	handlers.InitDockerProxy()

	// 初始化镜像流式下载器
	handlers.InitImageStreamer()

	// 初始化防抖器
	handlers.InitDebouncer()

	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	// 全局Panic恢复保护
	router.Use(gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		log.Printf("🚨 Panic recovered: %v", recovered)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Internal server error",
			"code":  "INTERNAL_ERROR",
		})
	}))

	// 全局限流中间件
	router.Use(utils.RateLimitMiddleware(globalLimiter))

	// 初始化监控端点
	initHealthRoutes(router)

	// 初始化镜像tar下载路由
	handlers.InitImageTarRoutes(router)

	// 静态文件路由
	router.GET("/", func(c *gin.Context) {
		serveEmbedFile(c, "public/index.html")
	})
	router.GET("/public/*filepath", func(c *gin.Context) {
		filepath := strings.TrimPrefix(c.Param("filepath"), "/")
		serveEmbedFile(c, "public/"+filepath)
	})

	router.GET("/images.html", func(c *gin.Context) {
		serveEmbedFile(c, "public/images.html")
	})
	router.GET("/search.html", func(c *gin.Context) {
		serveEmbedFile(c, "public/search.html")
	})
	router.GET("/favicon.ico", func(c *gin.Context) {
		serveEmbedFile(c, "public/favicon.ico")
	})

	// 注册dockerhub搜索路由
	handlers.RegisterSearchRoute(router)

	// 注册Docker认证路由
	router.Any("/token", handlers.ProxyDockerAuthGin)
	router.Any("/token/*path", handlers.ProxyDockerAuthGin)

	// 注册Docker Registry代理路由
	router.Any("/v2/*path", handlers.ProxyDockerRegistryGin)

	// 注册GitHub代理路由（NoRoute处理器）
	router.NoRoute(handlers.GitHubProxyHandler)

	cfg := config.GetConfig()
	fmt.Printf("HubProxy 启动成功\n")
	fmt.Printf("监听地址: %s:%d\n", cfg.Server.Host, cfg.Server.Port)
	fmt.Printf("限流配置: %d请求/%g小时\n", cfg.RateLimit.RequestLimit, cfg.RateLimit.PeriodHours)

	// 显示HTTP/2支持状态
	if cfg.Server.EnableH2C {
		fmt.Printf("H2c: 已启用\n")
	}

	fmt.Printf("版本号: v1.1.6\n")
	fmt.Printf("项目地址: https://github.com/sky22333/hubproxy\n")

	// 创建HTTP2服务器
	server := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// 根据配置决定是否启用H2C
	if cfg.Server.EnableH2C {
		h2cHandler := h2c.NewHandler(router, &http2.Server{
			MaxConcurrentStreams:         250,
			IdleTimeout:                  300 * time.Second,
			MaxReadFrameSize:             4 << 20,
			MaxUploadBufferPerConnection: 8 << 20,
			MaxUploadBufferPerStream:     2 << 20,
		})
		server.Handler = h2cHandler
	} else {
		server.Handler = router
	}

	err := server.ListenAndServe()
	if err != nil {
		fmt.Printf("启动服务失败: %v\n", err)
	}
}

// 简单的健康检查
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	} else if d < time.Hour {
		return fmt.Sprintf("%d分钟%d秒", int(d.Minutes()), int(d.Seconds())%60)
	} else if d < 24*time.Hour {
		return fmt.Sprintf("%d小时%d分钟", int(d.Hours()), int(d.Minutes())%60)
	} else {
		days := int(d.Hours()) / 24
		hours := int(d.Hours()) % 24
		return fmt.Sprintf("%d天%d小时", days, hours)
	}
}

func getUptimeInfo() (time.Duration, float64, string) {
	uptime := time.Since(serviceStartTime)
	return uptime, uptime.Seconds(), formatDuration(uptime)
}

func initHealthRoutes(router *gin.Engine) {
	router.GET("/ready", func(c *gin.Context) {
		_, uptimeSec, uptimeHuman := getUptimeInfo()
		c.JSON(http.StatusOK, gin.H{
			"ready":           true,
			"service":         "hubproxy",
			"start_time_unix": serviceStartTime.Unix(),
			"uptime_sec":      uptimeSec,
			"uptime_human":    uptimeHuman,
		})
	})
}

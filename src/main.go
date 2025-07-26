package main

import (
	"embed"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
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
	fmt.Printf("🚀 HubProxy 启动成功\n")
	fmt.Printf("📡 监听地址: %s:%d\n", cfg.Server.Host, cfg.Server.Port)
	fmt.Printf("⚡ 限流配置: %d请求/%g小时\n", cfg.RateLimit.RequestLimit, cfg.RateLimit.PeriodHours)
	fmt.Printf("🔗 项目地址: https://github.com/sky22333/hubproxy\n")

	err := router.Run(fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port))
	if err != nil {
		fmt.Printf("启动服务失败: %v\n", err)
	}
}



// 简单的健康检查
func formatBeijingTime(t time.Time) string {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	return t.In(loc).Format("2006-01-02 15:04:05")
}

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

func initHealthRoutes(router *gin.Engine) {
	router.GET("/health", func(c *gin.Context) {
		uptime := time.Since(serviceStartTime)
		c.JSON(http.StatusOK, gin.H{
			"status":         "healthy",
			"timestamp_unix": serviceStartTime.Unix(),
			"uptime_sec":     uptime.Seconds(),
			"service":        "hubproxy",
			"start_time_bj":  formatBeijingTime(serviceStartTime),
			"uptime_human":   formatDuration(uptime),
		})
	})

	router.GET("/ready", func(c *gin.Context) {
		uptime := time.Since(serviceStartTime)
		c.JSON(http.StatusOK, gin.H{
			"ready":          true,
			"timestamp_unix": time.Now().Unix(),
			"uptime_sec":     uptime.Seconds(),
			"uptime_human":   formatDuration(uptime),
		})
	})
}


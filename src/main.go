package main

import (
	"embed"
	"fmt"
	"log"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"hubproxy/config"
	"hubproxy/handlers"
	"hubproxy/utils"
)

//go:embed all:dist
var staticFiles embed.FS

var (
	globalLimiter    *utils.IPRateLimiter
	serviceStartTime = time.Now()
)

var Version = "dev"

func init() {
	for ext, typ := range map[string]string{
		".js":    "application/javascript; charset=utf-8",
		".mjs":   "application/javascript; charset=utf-8",
		".woff":  "font/woff",
		".woff2": "font/woff2",
		".map":   "application/json",
	} {
		_ = mime.AddExtensionType(ext, typ)
	}
}

func contentTypeFor(filename string) string {
	if ct := mime.TypeByExtension(path.Ext(filename)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func serveEmbedFile(c *gin.Context, filename string) {
	data, err := staticFiles.ReadFile(filename)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Data(http.StatusOK, contentTypeFor(filename), data)
}

func serveSPA(c *gin.Context) {
	serveEmbedFile(c, "dist/index.html")
}

func registerFrontendRoutes(router *gin.Engine, enabled bool) {
	if !enabled {
		notFound := func(c *gin.Context) { c.Status(http.StatusNotFound) }
		router.GET("/", notFound)
		router.GET("/images", notFound)
		router.GET("/search", notFound)
		router.GET("/assets/*filepath", notFound)
		router.GET("/favicon.ico", notFound)
		return
	}

	router.GET("/", serveSPA)
	router.GET("/images", serveSPA)
	router.GET("/search", serveSPA)
	router.GET("/favicon.ico", func(c *gin.Context) {
		serveEmbedFile(c, "dist/favicon.ico")
	})
	router.GET("/assets/*filepath", func(c *gin.Context) {
		filepath := strings.TrimPrefix(c.Param("filepath"), "/")
		if filepath == "" || strings.Contains(filepath, "..") {
			c.Status(http.StatusNotFound)
			return
		}
		serveEmbedFile(c, path.Join("dist/assets", filepath))
	})
}

func buildRouter(cfg *config.AppConfig) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	router.Use(gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		log.Printf("Panic 已恢复: %v", recovered)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Internal server error",
			"code":  "INTERNAL_ERROR",
		})
	}))

	router.Use(utils.RateLimitMiddleware(globalLimiter))

	initHealthRoutes(router)
	handlers.InitImageTarRoutes(router)
	registerFrontendRoutes(router, cfg.Server.EnableFrontend)
	handlers.RegisterSearchRoute(router)

	router.Any("/token", handlers.ProxyDockerAuthGin)
	router.Any("/token/*path", handlers.ProxyDockerAuthGin)
	router.Any("/v2/*path", handlers.ProxyDockerRegistryGin)
	router.NoRoute(handlers.GitHubProxyHandler)

	return router
}

func main() {
	if err := config.LoadConfig(); err != nil {
		fmt.Printf("配置加载失败: %v\n", err)
		return
	}

	utils.InitHTTPClients()
	globalLimiter = utils.InitGlobalLimiter()
	handlers.InitDockerProxy()
	handlers.InitImageStreamer()
	handlers.InitDebouncer()

	cfg := config.GetConfig()
	router := buildRouter(cfg)

	fmt.Printf("HubProxy 启动成功\n")
	fmt.Printf("监听地址: %s:%d\n", cfg.Server.Host, cfg.Server.Port)
	fmt.Printf("限流配置: %d请求/%g小时\n", cfg.RateLimit.RequestLimit, cfg.RateLimit.PeriodHours)
	if cfg.Server.EnableH2C {
		fmt.Printf("H2c: 已启用\n")
	}
	fmt.Printf("版本号: %s\n", Version)
	fmt.Printf("项目地址: https://github.com/sky22333/hubproxy\n")

	server := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 30 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	if cfg.Server.EnableH2C {
		server.Handler = h2c.NewHandler(router, &http2.Server{
			MaxConcurrentStreams:         250,
			IdleTimeout:                  300 * time.Second,
			MaxReadFrameSize:             4 << 20,
			MaxUploadBufferPerConnection: 8 << 20,
			MaxUploadBufferPerStream:     2 << 20,
		})
	} else {
		server.Handler = router
	}

	if err := server.ListenAndServe(); err != nil {
		fmt.Printf("启动服务失败: %v\n", err)
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d分钟%d秒", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d小时%d分钟", int(d.Hours()), int(d.Minutes())%60)
	}

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	return fmt.Sprintf("%d天%d小时", days, hours)
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
			"version":         Version,
			"start_time_unix": serviceStartTime.Unix(),
			"uptime_sec":      uptimeSec,
			"uptime_human":    uptimeHuman,
		})
	})
}

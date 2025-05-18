package main

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sizeLimit = 1024 * 1024 * 1024 * 10 // 允许的文件大小，默认10GB
	host      = "0.0.0.0"               // 监听地址
	port      = 5000                    // 监听端口
)

var (
	exps = []*regexp.Regexp{
		regexp.MustCompile(`^(?:https?://)?github\.com/([^/]+)/([^/]+)/(?:releases|archive)/.*$`),
		regexp.MustCompile(`^(?:https?://)?github\.com/([^/]+)/([^/]+)/(?:blob|raw)/.*$`),
		regexp.MustCompile(`^(?:https?://)?github\.com/([^/]+)/([^/]+)/(?:info|git-).*$`),
		regexp.MustCompile(`^(?:https?://)?raw\.github(?:usercontent|)\.com/([^/]+)/([^/]+)/.+?/.+$`),
		regexp.MustCompile(`^(?:https?://)?gist\.github(?:usercontent|)\.com/([^/]+)/.+?/.+`),
		regexp.MustCompile(`^(?:https?://)?api\.github\.com/repos/([^/]+)/([^/]+)/.*`),
		regexp.MustCompile(`^(?:https?://)?huggingface\.co(?:/spaces)?/([^/]+)/(.+)$`),
		regexp.MustCompile(`^(?:https?://)?cdn-lfs\.hf\.co(?:/spaces)?/([^/]+)/([^/]+)(?:/(.*))?$`),
		regexp.MustCompile(`^(?:https?://)?download\.docker\.com/([^/]+)/.*\.(tgz|zip)$`),
		regexp.MustCompile(`^(?:https?://)?(github|opengraph)\.githubassets\.com/([^/]+)/.+?$`),
	}
	httpClient *http.Client
	config     *Config
	configLock sync.RWMutex
)

type Config struct {
	WhiteList []string `json:"whiteList"`
	BlackList []string `json:"blackList"`
}

func main() {
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	httpClient = &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          1000,
			MaxIdleConnsPerHost:   1000,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 300 * time.Second,
		},
	}

	loadConfig()
	go func() {
		for {
			time.Sleep(10 * time.Minute)
			loadConfig()
		}
	}()
	
	// 初始化Skopeo相关路由 - 必须在任何通配符路由之前注册
	initSkopeoRoutes(router)
	
	// 单独处理根路径请求，避免冲突
	router.GET("/", func(c *gin.Context) {
		c.File("./public/index.html")
	})
	
	// 指定具体的静态文件路径，避免使用通配符
	router.Static("/public", "./public")
	
	// 对于.html等特定文件也直接注册
	router.GET("/skopeo.html", func(c *gin.Context) {
		c.File("./public/skopeo.html")
	})
	
	// 图标文件
	router.GET("/favicon.ico", func(c *gin.Context) {
		c.File("./public/favicon.ico")
	})
	
	router.GET("/bj.svg", func(c *gin.Context) {
		c.File("./public/bj.svg")
	})
	
	// 创建GitHub文件下载专用的限流器
	githubLimiter := NewIPRateLimiter()
	
	// 注册NoRoute处理器，应用限流中间件
	router.NoRoute(RateLimitMiddleware(githubLimiter), handler)

	err := router.Run(fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		fmt.Printf("Error starting server: %v\n", err)
	}
}

func handler(c *gin.Context) {
	rawPath := strings.TrimPrefix(c.Request.URL.RequestURI(), "/")

	for strings.HasPrefix(rawPath, "/") {
		rawPath = strings.TrimPrefix(rawPath, "/")
	}

	if !strings.HasPrefix(rawPath, "http") {
		c.String(http.StatusForbidden, "无效输入")
		return
	}

	matches := checkURL(rawPath)
	if matches != nil {
		if len(config.WhiteList) > 0 && !checkList(matches, config.WhiteList) {
			c.String(http.StatusForbidden, "不在白名单内，限制访问。")
			return
		}
		if len(config.BlackList) > 0 && checkList(matches, config.BlackList) {
			c.String(http.StatusForbidden, "黑名单限制访问")
			return
		}
	} else {
		c.String(http.StatusForbidden, "无效输入")
		return
	}

	if exps[1].MatchString(rawPath) {
		rawPath = strings.Replace(rawPath, "/blob/", "/raw/", 1)
	}

	proxy(c, rawPath)
}

func proxy(c *gin.Context, u string) {
	req, err := http.NewRequest(c.Request.Method, u, c.Request.Body)
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf("server error %v", err))
		return
	}

	for key, values := range c.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Del("Host")

	resp, err := httpClient.Do(req)
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf("server error %v", err))
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(resp.Body)

	// 检查文件大小限制
	if contentLength := resp.Header.Get("Content-Length"); contentLength != "" {
		if size, err := strconv.Atoi(contentLength); err == nil && size > sizeLimit {
			c.String(http.StatusRequestEntityTooLarge, "File too large.")
			return
		}
	}

	// 清理安全相关的头
	resp.Header.Del("Content-Security-Policy")
	resp.Header.Del("Referrer-Policy")
	resp.Header.Del("Strict-Transport-Security")
	
	// 对于需要处理的shell文件，我们使用chunked传输
	isShellFile := strings.HasSuffix(strings.ToLower(u), ".sh")
	if isShellFile {
		resp.Header.Del("Content-Length")
		resp.Header.Set("Transfer-Encoding", "chunked")
	}

	// 复制其他响应头
	for key, values := range resp.Header {
		for _, value := range values {
			c.Header(key, value)
		}
	}

	if location := resp.Header.Get("Location"); location != "" {
		if checkURL(location) != nil {
			c.Header("Location", "/"+location)
		} else {
			proxy(c, location)
			return
		}
	}

	c.Status(resp.StatusCode)

	// 处理响应体
	if isShellFile {
		// 获取真实域名
		realHost := c.Request.Header.Get("X-Forwarded-Host")
		if realHost == "" {
			realHost = c.Request.Host
		}
		// 如果域名中没有协议前缀，添加https://
		if !strings.HasPrefix(realHost, "http://") && !strings.HasPrefix(realHost, "https://") {
			realHost = "https://" + realHost
		}
		// 使用ProcessGitHubURLs处理.sh文件
		processedBody, _, err := ProcessGitHubURLs(resp.Body, resp.Header.Get("Content-Encoding") == "gzip", realHost, true)
		if err != nil {
			c.String(http.StatusInternalServerError, fmt.Sprintf("处理shell文件时发生错误: %v", err))
			return
		}
		if _, err := io.Copy(c.Writer, processedBody); err != nil {
			c.String(http.StatusInternalServerError, fmt.Sprintf("写入响应时发生错误: %v", err))
			return
		}
	} else {
		// 对于非.sh文件，直接复制响应体
		if _, err := io.Copy(c.Writer, resp.Body); err != nil {
			return
		}
	}
}

func loadConfig() {
	file, err := os.Open("config.json")
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		return
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {

		}
	}(file)

	var newConfig Config
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&newConfig); err != nil {
		fmt.Printf("Error decoding config: %v\n", err)
		return
	}

	configLock.Lock()
	config = &newConfig
	configLock.Unlock()
}

func checkURL(u string) []string {
	for _, exp := range exps {
		if matches := exp.FindStringSubmatch(u); matches != nil {
			return matches[1:]
		}
	}
	return nil
}

func checkList(matches, list []string) bool {
	for _, item := range list {
		if strings.HasPrefix(matches[0], item) {
			return true
		}
	}
	return false
}

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// 常量定义
const (
	sizeLimit = 1024 * 1024 * 1024 * 10 // 允许的文件大小，默认10GB
	host      = "0.0.0.0"               // 监听地址
	port      = 5000                    // 监听端口
)

type Config struct {
	WhiteList []string `json:"whiteList"`
	BlackList []string `json:"blackList"`
}

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
	}
	httpClient *http.Client
	config     *Config
	configLock sync.RWMutex
)

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
	
	// 每60分钟热重载黑白名单
	go func() {
		ticker := time.NewTicker(60 * time.Minute)
		defer ticker.Stop()
		
		for range ticker.C {
			loadConfig()
		}
	}()
	
	// 前端访问路径，默认根路径
	router.Static("/", "./public")
	router.NoRoute(handler)

	serverAddr := fmt.Sprintf("%s:%d", host, port)
	if err := router.Run(serverAddr); err != nil {
		fmt.Printf("Error starting server: %v\n", err)
	}
}

func handler(c *gin.Context) {
	rawPath := strings.TrimPrefix(c.Request.URL.RequestURI(), "/")
	
	for strings.HasPrefix(rawPath, "/") {
		rawPath = strings.TrimPrefix(rawPath, "/")
	}
	// 脚本嵌套路径处理
	if rawPath == "perl-pe-para" {
		handlePerlPePara(c)
		return
	}

	if !strings.HasPrefix(rawPath, "http") {
		c.String(http.StatusForbidden, "无效输入")
		return
	}

	matches := checkURL(rawPath)
	if matches != nil {
		configLock.RLock()
		defer configLock.RUnlock()
		
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
// 处理脚本嵌套相关函数
func handlePerlPePara(c *gin.Context) {
	perlstr := "perl -pe"
	responseText := fmt.Sprintf(`s#(bash.*?\.sh)([^/\w\d])#\1 | %s "$(curl -L %s/perl-pe-para)" \2#g; s# (git)# https://\1#g; s#(http.*?git[^/]*?/)#%s/\1#g`, perlstr, c.Request.URL.String(), c.Request.URL.String())
	c.Header("Content-Type", "text/plain")
	c.Header("Cache-Control", "max-age=300")
	c.String(http.StatusOK, responseText)
}

func proxy(c *gin.Context, u string) {
	// 检查是否脚本嵌套路径
	if strings.HasSuffix(u, "perl-pe-para") {
		handlePerlPePara(c)
		return
	}

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
	defer func() {
		if err := resp.Body.Close(); err != nil {
			fmt.Printf("Error closing response body: %v\n", err)
		}
	}()

	if contentLength, ok := resp.Header["Content-Length"]; ok {
		if size, err := strconv.Atoi(contentLength[0]); err == nil && size > sizeLimit {
			c.String(http.StatusRequestEntityTooLarge, "File too large.")
			return
		}
	}

	resp.Header.Del("Content-Security-Policy")
	resp.Header.Del("Referrer-Policy")
	resp.Header.Del("Strict-Transport-Security")

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
	if _, err := io.Copy(c.Writer, resp.Body); err != nil {
		fmt.Printf("Error copying response body: %v\n", err)
		return
	}
}

func loadConfig() {
	file, err := os.Open("config.json")
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			fmt.Printf("Error closing config file: %v\n", err)
		}
	}()

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

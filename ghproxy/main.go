package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"crypto/sha256"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

const (
	MaxFileSize = 10 * 1024 * 1024 * 1024 // 允许的文件大小，默认10GB
	ListenHost  = "0.0.0.0"               // 监听地址
	ListenPort  = 5000                    // 监听端口
	CacheDir    = "cache"
	// 是否开启缓存
	CacheExpiry = 0 * time.Minute // 默认不缓存
)

var (
	cache      = sync.Map{}
	exps       = initRegexps()
	httpClient = initHTTPClient()
	config     *Config
	configLock sync.RWMutex
)

type Config struct {
	WhiteList []string `json:"whiteList"`
	BlackList []string `json:"blackList"`
}

type CachedResponse struct {
	Header     http.Header
	StatusCode int
	Body       []byte
	Timestamp  time.Time
}

func init() {
	if err := os.MkdirAll(CacheDir, 0755); err != nil {
		log.Fatalf("Failed to create cache directory: %v", err)
	}
	go func() {
		for {
			time.Sleep(10 * time.Minute)
			loadConfig()
		}
	}()
	loadConfig()
}

func initRegexps() []*regexp.Regexp {
	return []*regexp.Regexp{
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
}

func initHTTPClient() *http.Client {
	return &http.Client{
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
}

func main() {
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()
	router.Static("/", "./public")
	router.NoRoute(handler)

	addr := fmt.Sprintf("%s:%d", ListenHost, ListenPort)
	if err := router.Run(addr); err != nil {
		log.Fatalf("Error starting server: %v", err)
	}
}

func handler(c *gin.Context) {
	rawPath := strings.TrimPrefix(c.Request.URL.RequestURI(), "/")
	rawPath = strings.TrimPrefix(rawPath, "/")

	if !strings.HasPrefix(rawPath, "http") {
		c.String(http.StatusForbidden, "无效输入")
		return
	}

	matches := checkURL(rawPath)
	if matches == nil {
		c.String(http.StatusForbidden, "无效输入")
		return
	}

	if len(config.WhiteList) > 0 && !checkList(matches, config.WhiteList) {
		c.String(http.StatusForbidden, "不在白名单内，限制访问。")
		return
	}
	if len(config.BlackList) > 0 && checkList(matches, config.BlackList) {
		c.String(http.StatusForbidden, "黑名单限制访问")
		return
	}

	if exps[1].MatchString(rawPath) {
		rawPath = strings.Replace(rawPath, "/blob/", "/raw/", 1)
	}

	proxy(c, rawPath)
}

func proxy(c *gin.Context, u string) {
	cacheKey := generateCacheKey(u)
	// 当 CacheExpiry 为 0 时，不使用缓存
	if CacheExpiry != 0 {
		if cachedData, ok := cache.Load(cacheKey); ok {
			log.Printf("Using cached response for %s", u)
			cached := cachedData.(*CachedResponse)
			if time.Since(cached.Timestamp) < CacheExpiry {
				setHeaders(c, cached.Header)
				c.Status(cached.StatusCode)
				c.Writer.Write(cached.Body)
				return
			}
		}
	}

	log.Printf("use proxy response for %s", u)

	req, err := http.NewRequest(c.Request.Method, u, c.Request.Body)
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf("server error %v", err))
		return
	}

	copyHeaders(req.Header, c.Request.Header)
	req.Header.Del("Host")

	resp, err := httpClient.Do(req)
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf("server error %v", err))
		return
	}
	defer closeWithLog(resp.Body)

	if contentLength, ok := resp.Header["Content-Length"]; ok {
		if size, err := strconv.Atoi(contentLength[0]); err == nil && size > MaxFileSize {
			c.String(http.StatusRequestEntityTooLarge, "File too large.")
			return
		}
	}

	removeHeaders(resp.Header, "Content-Security-Policy", "Referrer-Policy", "Strict-Transport-Security")
	setHeaders(c, resp.Header)

	if location := resp.Header.Get("Location"); location != "" {
		if checkURL(location) != nil {
			c.Header("Location", "/"+location)
		} else {
			proxy(c, location)
			return
		}
	}

	c.Status(resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read response body: %v", err)
		return
	}
	if _, err := c.Writer.Write(body); err != nil {
		log.Printf("Failed to write response body: %v", err)
		return
	}

	// 当 CacheExpiry 不为 0 时，保存到缓存
	if CacheExpiry != 0 {
		// Save to cache
		cached := &CachedResponse{
			Header:     resp.Header,
			StatusCode: resp.StatusCode,
			Body:       body,
			Timestamp:  time.Now(),
		}
		cache.Store(cacheKey, cached)
		cacheFilePath := filepath.Join(CacheDir, cacheKey)
		// 修改 ioutil.WriteFile 为 os.WriteFile
		if err := os.WriteFile(cacheFilePath, body, 0644); err != nil {
			log.Printf("Failed to write cache file: %v", err)
		}
	}
}

func generateCacheKey(u string) string {
	hash := sha256.Sum256([]byte(u))
	return hex.EncodeToString(hash[:])
}

func loadConfig() {
	file, err := os.Open("config.json")
	if err != nil {
		log.Printf("Error loading config: %v", err)
		return
	}
	defer closeWithLog(file)

	var newConfig Config
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&newConfig); err != nil {
		log.Printf("Error decoding config: %v", err)
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

func setHeaders(c *gin.Context, headers http.Header) {
	for key, values := range headers {
		for _, value := range values {
			c.Header(key, value)
		}
	}
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func removeHeaders(headers http.Header, keys ...string) {
	for _, key := range keys {
		headers.Del(key)
	}
}

func closeWithLog(c io.Closer) {
	if err := c.Close(); err != nil {
		log.Printf("Failed to close: %v", err)
	}
}

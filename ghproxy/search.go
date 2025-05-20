package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// SearchResult Docker Hub搜索结果
type SearchResult struct {
	Count    int          `json:"count"`
	Next     string       `json:"next"`
	Previous string       `json:"previous"`
	Results  []Repository `json:"results"`
}

// Repository 仓库信息
type Repository struct {
	Name           string    `json:"name"`
	Namespace      string    `json:"namespace"`
	Description    string    `json:"description"`
	IsOfficial     bool      `json:"is_official"`
	IsAutomated    bool      `json:"is_automated"`
	StarCount      int       `json:"star_count"`
	PullCount      int       `json:"pull_count"`
	LastUpdated    time.Time `json:"last_updated"`
	Status         int       `json:"status"`
	Organization   string    `json:"organization,omitempty"`
	IsTrusted      bool      `json:"is_trusted"`
	IsPrivate      bool      `json:"is_private"`
	PullsLastWeek  int       `json:"pulls_last_week"`
}

// TagInfo 标签信息
type TagInfo struct {
	Name          string    `json:"name"`
	FullSize      int64     `json:"full_size"`
	LastUpdated   time.Time `json:"last_updated"`
	LastPusher    string    `json:"last_pusher"`
	Images        []Image   `json:"images"`
	Vulnerabilities struct {
		Critical int `json:"critical"`
		High     int `json:"high"`
		Medium   int `json:"medium"`
		Low      int `json:"low"`
		Unknown  int `json:"unknown"`
	} `json:"vulnerabilities"`
}

// Image 镜像信息
type Image struct {
	Architecture string `json:"architecture"`
	Features     string `json:"features"`
	Variant      string `json:"variant,omitempty"`
	Digest       string `json:"digest"`
	OS           string `json:"os"`
	OSFeatures   string `json:"os_features"`
	Size         int64  `json:"size"`
}

type cacheEntry struct {
	data      interface{}
	timestamp time.Time
}

var (
	cache     = make(map[string]cacheEntry)
	cacheLock sync.RWMutex
	cacheTTL  = 30 * time.Minute
)

func getCachedResult(key string) (interface{}, bool) {
	cacheLock.RLock()
	defer cacheLock.RUnlock()
	entry, exists := cache[key]
	if !exists {
		return nil, false
	}
	if time.Since(entry.timestamp) > cacheTTL {
		return nil, false
	}
	return entry.data, true
}

func setCacheResult(key string, data interface{}) {
	cacheLock.Lock()
	defer cacheLock.Unlock()
	cache[key] = cacheEntry{
		data:      data,
		timestamp: time.Now(),
	}
}

// searchDockerHub 搜索镜像
func searchDockerHub(ctx context.Context, query string, page, pageSize int) (*SearchResult, error) {
	cacheKey := fmt.Sprintf("search:%s:%d:%d", query, page, pageSize)
	if cached, ok := getCachedResult(cacheKey); ok {
		return cached.(*SearchResult), nil
	}

	baseURL := "https://hub.docker.com/v2/search/repositories/"
	params := url.Values{}
	params.Set("query", query)
	params.Set("page", fmt.Sprintf("%d", page))
	params.Set("page_size", fmt.Sprintf("%d", pageSize))

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result SearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	// 添加调试日志
	fmt.Printf("搜索结果: 总数=%d, 结果数=%d\n", result.Count, len(result.Results))
	for i, repo := range result.Results {
		fmt.Printf("仓库[%d]: 名称=%s, 命名空间=%s, 描述=%s, 是否官方=%v\n",
			i, repo.Name, repo.Namespace, repo.Description, repo.IsOfficial)
	}

	setCacheResult(cacheKey, &result)
	return &result, nil
}

// getRepositoryTags 获取仓库标签信息
func getRepositoryTags(ctx context.Context, namespace, name string, page, pageSize int) ([]TagInfo, error) {
	cacheKey := fmt.Sprintf("tags:%s:%s:%d:%d", namespace, name, page, pageSize)
	if cached, ok := getCachedResult(cacheKey); ok {
		return cached.([]TagInfo), nil
	}

	var baseURL string
	if namespace == "library" {
		baseURL = fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/%s/tags", namespace, name)
	} else {
		baseURL = fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/%s/tags", namespace, name)
	}

	params := url.Values{}
	params.Set("page", fmt.Sprintf("%d", page))
	params.Set("page_size", fmt.Sprintf("%d", pageSize))

	fullURL := baseURL + "?" + params.Encode()
	fmt.Printf("请求标签URL: %s\n", fullURL)

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		fmt.Printf("创建标签请求失败: %v\n", err)
		return nil, fmt.Errorf("创建标签请求失败: %v", err)
	}

	// 添加必要的请求头
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("发送标签请求失败: %v\n", err)
		return nil, fmt.Errorf("发送标签请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("获取标签失败: 状态码=%d, 响应体=%s\n", resp.StatusCode, string(body))
		return nil, fmt.Errorf("获取标签失败: 状态码=%d", resp.StatusCode)
	}

	var result struct {
		Count    int       `json:"count"`
		Next     string    `json:"next"`
		Previous string    `json:"previous"`
		Results  []TagInfo `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Printf("解析标签响应失败: %v\n", err)
		return nil, fmt.Errorf("解析标签响应失败: %v", err)
	}

	fmt.Printf("获取到标签: 总数=%d, 结果数=%d\n", result.Count, len(result.Results))

	setCacheResult(cacheKey, result.Results)
	return result.Results, nil
}

// RegisterSearchRoute 注册搜索相关路由
func RegisterSearchRoute(r *gin.Engine) {
	// 搜索镜像
	r.GET("/search", func(c *gin.Context) {
		query := c.Query("q")
		page := 1
		pageSize := 25
		if p := c.Query("page"); p != "" {
			fmt.Sscanf(p, "%d", &page)
		}
		if ps := c.Query("page_size"); ps != "" {
			fmt.Sscanf(ps, "%d", &pageSize)
		}

		result, err := searchDockerHub(c.Request.Context(), query, page, pageSize)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, result)
	})

	// 获取标签信息
	r.GET("/tags/:namespace/:name", func(c *gin.Context) {
		namespace := c.Param("namespace")
		name := c.Param("name")
		
		// 打印请求参数
		fmt.Printf("获取标签请求: namespace=%s, name=%s\n", namespace, name)
		
		page := 1
		pageSize := 100
		if p := c.Query("page"); p != "" {
			fmt.Sscanf(p, "%d", &page)
		}
		if ps := c.Query("page_size"); ps != "" {
			fmt.Sscanf(ps, "%d", &pageSize)
		}

		tags, err := getRepositoryTags(c.Request.Context(), namespace, name, page, pageSize)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, tags)
	})
}

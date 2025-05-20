package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type DockerHubSearchResult struct {
	Count    int          `json:"count"`
	Next     string       `json:"next"`
	Previous string       `json:"previous"`
	Results  []Repository `json:"results"`
}

type Repository struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	RepositoryType string `json:"repository_type"`
	Status         int    `json:"status"`
	Description    string `json:"description"`
	IsOfficial     bool   `json:"is_official"`
	IsPrivate      bool   `json:"is_private"`
	StarCount      int    `json:"star_count"`
	PullCount      int    `json:"pull_count"`
}

type cacheEntry struct {
	data      *DockerHubSearchResult
	timestamp time.Time
}

var (
	cache     = make(map[string]cacheEntry)
	cacheLock sync.RWMutex
	cacheTTL  = 8 * time.Hour
)

func getCachedResult(key string) (*DockerHubSearchResult, bool) {
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

func setCacheResult(key string, data *DockerHubSearchResult) {
	cacheLock.Lock()
	defer cacheLock.Unlock()
	cache[key] = cacheEntry{
		data:      data,
		timestamp: time.Now(),
	}
}

// SearchDockerHub 独立函数
func SearchDockerHub(ctx context.Context, query string, page, pageSize int, userAgent string) (*DockerHubSearchResult, error) {
	if query == "" {
		return nil, fmt.Errorf("query 不能为空")
	}
	cacheKey := fmt.Sprintf("q=%s&p=%d&ps=%d", query, page, pageSize)
	if cached, ok := getCachedResult(cacheKey); ok {
		return cached, nil
	}

	baseURL := "https://hub.docker.com/v2/search/repositories/"
	params := url.Values{}
	params.Set("query", query)
	params.Set("page", fmt.Sprintf("%d", page))
	params.Set("page_size", fmt.Sprintf("%d", pageSize))

	reqURL := baseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	} else {
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; MyDockerHubClient/1.0)")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker hub api 返回状态 %d", resp.StatusCode)
	}

	var result DockerHubSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	setCacheResult(cacheKey, &result)
	return &result, nil
}

// RegisterSearchRoute 注册 /search 路由
func RegisterSearchRoute(r *gin.Engine) {
	r.GET("/search", func(c *gin.Context) {
		query := c.Query("q")
		page := 1
		pageSize := 10
		if p := c.Query("page"); p != "" {
			fmt.Sscanf(p, "%d", &page)
		}
		if ps := c.Query("page_size"); ps != "" {
			fmt.Sscanf(ps, "%d", &pageSize)
		}
		userAgent := c.GetHeader("User-Agent")

		result, err := SearchDockerHub(c.Request.Context(), query, page, pageSize, userAgent)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, result)
	})
}

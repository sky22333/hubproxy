package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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
	Name           string    `json:"repo_name"`
	Description    string    `json:"short_description"`
	IsOfficial     bool      `json:"is_official"`
	IsAutomated    bool      `json:"is_automated"`
	StarCount      int       `json:"star_count"`
	PullCount      int       `json:"pull_count"`
	RepoOwner      string    `json:"repo_owner"`
	LastUpdated    string    `json:"last_updated"`
	Status         int       `json:"status"`
	Organization   string    `json:"affiliation"`
	PullsLastWeek  int       `json:"pulls_last_week"`
	Namespace      string    `json:"namespace"`
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

	// 构建Docker Hub API请求
	baseURL := "https://registry.hub.docker.com/v2/search/repositories/"
	params := url.Values{}
	params.Set("query", query)
	params.Set("page", fmt.Sprintf("%d", page))
	params.Set("page_size", fmt.Sprintf("%d", pageSize))

	fullURL := baseURL + "?" + params.Encode()
	fmt.Printf("搜索URL: %s\n", fullURL)

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %v", err)
	}

	// 添加必要的请求头
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

	// 发送请求
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求失败: 状态码=%d, 响应=%s", resp.StatusCode, string(body))
	}

	// 打印响应内容以便调试
	fmt.Printf("搜索响应: %s\n", string(body))

	// 解析响应
	var result SearchResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	// 打印解析后的结果
	fmt.Printf("搜索结果: 总数=%d, 结果数=%d\n", result.Count, len(result.Results))
	for i, repo := range result.Results {
		fmt.Printf("仓库[%d]: 名称=%s, 所有者=%s, 描述=%s, 是否官方=%v\n",
			i, repo.Name, repo.RepoOwner, repo.Description, repo.IsOfficial)
	}

	// 处理搜索结果
	for i := range result.Results {
		if result.Results[i].IsOfficial {
			// 确保官方镜像有正确的名称格式
			if !strings.Contains(result.Results[i].Name, "/") {
				result.Results[i].Name = "library/" + result.Results[i].Name
			}
			// 设置命名空间为 library
			result.Results[i].Namespace = "library"
		} else {
			// 从 repo_name 中提取 namespace
			parts := strings.Split(result.Results[i].Name, "/")
			if len(parts) > 1 {
				result.Results[i].Namespace = parts[0]
				result.Results[i].Name = parts[1]
			} else {
				result.Results[i].Namespace = result.Results[i].RepoOwner
			}
		}
	}

	setCacheResult(cacheKey, &result)
	return &result, nil
}

// getRepositoryTags 获取仓库标签信息
func getRepositoryTags(ctx context.Context, namespace, name string) ([]TagInfo, error) {
	if namespace == "" || name == "" {
		return nil, fmt.Errorf("无效输入：命名空间和名称不能为空")
	}

	cacheKey := fmt.Sprintf("tags:%s:%s", namespace, name)
	if cached, ok := getCachedResult(cacheKey); ok {
		return cached.([]TagInfo), nil
	}

	// 构建API URL
	baseURL := fmt.Sprintf("https://registry.hub.docker.com/v2/repositories/%s/%s/tags", namespace, name)
	params := url.Values{}
	params.Set("page_size", "100")
	params.Set("ordering", "last_updated")

	fullURL := baseURL + "?" + params.Encode()
	fmt.Printf("获取标签URL: %s\n", fullURL)

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %v", err)
	}

	// 添加必要的请求头
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

	// 发送请求
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求失败: 状态码=%d, 响应=%s", resp.StatusCode, string(body))
	}

	// 打印响应内容以便调试
	fmt.Printf("标签响应: %s\n", string(body))

	// 解析响应
	var result struct {
		Count    int       `json:"count"`
		Next     string    `json:"next"`
		Previous string    `json:"previous"`
		Results  []TagInfo `json:"results"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	// 打印解析后的结果
	fmt.Printf("标签结果: 总数=%d, 结果数=%d\n", result.Count, len(result.Results))
	for i, tag := range result.Results {
		fmt.Printf("标签[%d]: 名称=%s, 大小=%d, 更新时间=%v\n",
			i, tag.Name, tag.FullSize, tag.LastUpdated)
	}

	setCacheResult(cacheKey, result.Results)
	return result.Results, nil
}

// RegisterSearchRoute 注册搜索相关路由
func RegisterSearchRoute(r *gin.Engine) {
	// 搜索镜像
	r.GET("/search", func(c *gin.Context) {
		query := c.Query("q")
		if query == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "搜索关键词不能为空"})
			return
		}

		page := 1
		pageSize := 25
		if p := c.Query("page"); p != "" {
			fmt.Sscanf(p, "%d", &page)
		}
		if ps := c.Query("page_size"); ps != "" {
			fmt.Sscanf(ps, "%d", &pageSize)
		}

		// 如果是搜索官方镜像
		if !strings.Contains(query, "/") {
			query = strings.ToLower(query)
		}

		fmt.Printf("搜索请求: query=%s, page=%d, pageSize=%d\n", query, page, pageSize)

		result, err := searchDockerHub(c.Request.Context(), query, page, pageSize)
		if err != nil {
			fmt.Printf("搜索失败: %v\n", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// 过滤搜索结果，只保留相关的镜像
		filteredResults := make([]Repository, 0)
		searchTerm := strings.ToLower(strings.TrimPrefix(query, "library/"))
		
		for _, repo := range result.Results {
			repoName := strings.ToLower(repo.Name)
			// 如果是精确匹配或者以搜索词开头，或者包含 "searchTerm/searchTerm"
			if repoName == searchTerm || 
			   strings.HasPrefix(repoName, searchTerm+"/") || 
			   strings.Contains(repoName, "/"+searchTerm) {
				filteredResults = append(filteredResults, repo)
			}
		}
		
		result.Results = filteredResults
		result.Count = len(filteredResults)

		c.JSON(http.StatusOK, result)
	})

	// 获取标签信息
	r.GET("/tags/:namespace/:name", func(c *gin.Context) {
		namespace := c.Param("namespace")
		name := c.Param("name")

		fmt.Printf("获取标签请求: namespace=%s, name=%s\n", namespace, name)

		if namespace == "" || name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "命名空间和名称不能为空"})
			return
		}

		tags, err := getRepositoryTags(c.Request.Context(), namespace, name)
		if err != nil {
			fmt.Printf("获取标签失败: %v\n", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, tags)
	})
}

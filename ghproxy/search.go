package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
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

const (
	maxCacheSize = 1000 // 最大缓存条目数
	cacheTTL     = 30 * time.Minute
)

type Cache struct {
	data     map[string]cacheEntry
	mu       sync.RWMutex
	maxSize  int
}

var (
	searchCache = &Cache{
		data:    make(map[string]cacheEntry),
		maxSize: maxCacheSize,
	}
)

func (c *Cache) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	entry, exists := c.data[key]
	c.mu.RUnlock()
	
	if !exists {
		return nil, false
	}
	
	if time.Since(entry.timestamp) > cacheTTL {
		c.mu.Lock()
		delete(c.data, key)
		c.mu.Unlock()
		return nil, false
	}
	
	return entry.data, true
}

func (c *Cache) Set(key string, data interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	// 如果缓存已满，删除最旧的条目
	if len(c.data) >= c.maxSize {
		oldest := time.Now()
		var oldestKey string
		for k, v := range c.data {
			if v.timestamp.Before(oldest) {
				oldest = v.timestamp
				oldestKey = k
			}
		}
		delete(c.data, oldestKey)
	}
	
	c.data[key] = cacheEntry{
		data:      data,
		timestamp: time.Now(),
	}
}

func (c *Cache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	now := time.Now()
	for key, entry := range c.data {
		if now.Sub(entry.timestamp) > cacheTTL {
			delete(c.data, key)
		}
	}
}

// 定期清理过期缓存
func init() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			searchCache.Cleanup()
		}
	}()
}

// 改进的搜索结果过滤函数
func filterSearchResults(results []Repository, query string) []Repository {
	searchTerm := strings.ToLower(strings.TrimPrefix(query, "library/"))
	filtered := make([]Repository, 0)
	
	for _, repo := range results {
		// 标准化仓库名称
		repoName := strings.ToLower(repo.Name)
		repoDesc := strings.ToLower(repo.Description)
		
		// 计算相关性得分
		score := 0
		
		// 完全匹配
		if repoName == searchTerm {
			score += 100
		}
		
		// 前缀匹配
		if strings.HasPrefix(repoName, searchTerm) {
			score += 50
		}
		
		// 包含匹配
		if strings.Contains(repoName, searchTerm) {
			score += 30
		}
		
		// 描述匹配
		if strings.Contains(repoDesc, searchTerm) {
			score += 10
		}
		
		// 官方镜像加分
		if repo.IsOfficial {
			score += 20
		}
		
		// 分数达到阈值的结果才保留
		if score > 0 {
			filtered = append(filtered, repo)
		}
	}
	
	// 按相关性排序
	sort.Slice(filtered, func(i, j int) bool {
		// 优先考虑官方镜像
		if filtered[i].IsOfficial != filtered[j].IsOfficial {
			return filtered[i].IsOfficial
		}
		// 其次考虑拉取次数
		return filtered[i].PullCount > filtered[j].PullCount
	})
	
	return filtered
}

// searchDockerHub 搜索镜像
func searchDockerHub(ctx context.Context, query string, page, pageSize int) (*SearchResult, error) {
	cacheKey := fmt.Sprintf("search:%s:%d:%d", query, page, pageSize)
	
	// 尝试从缓存获取
	if cached, ok := searchCache.Get(cacheKey); ok {
		return cached.(*SearchResult), nil
	}
	
	// 判断是否是用户/仓库格式的搜索
	isUserRepo := strings.Contains(query, "/")
	var namespace, repoName string
	
	if isUserRepo {
		parts := strings.Split(query, "/")
		if len(parts) == 2 {
			namespace = parts[0]
			repoName = parts[1]
		}
	}
	
	// 构建搜索URL
	baseURL := "https://registry.hub.docker.com/v2/repositories/"
	var fullURL string
	var params url.Values
	
	if isUserRepo && namespace != "" {
		// 如果是用户/仓库格式，直接搜索用户的仓库
		fullURL = fmt.Sprintf("%s%s/", baseURL, namespace)
		params = url.Values{
			"page":      {fmt.Sprintf("%d", page)},
			"page_size": {fmt.Sprintf("%d", pageSize)},
		}
		if repoName != "" {
			params.Set("name", repoName)
		}
	} else {
		// 普通搜索
		fullURL = baseURL + "search/"
		params = url.Values{
			"query":     {query},
			"page":      {fmt.Sprintf("%d", page)},
			"page_size": {fmt.Sprintf("%d", pageSize)},
		}
	}
	
	fullURL = fullURL + "?" + params.Encode()
	fmt.Printf("搜索URL: %s\n", fullURL)
	
	// 发送请求
	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %v", err)
	}
	
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
			DisableKeepAlives:   false,
			MaxIdleConnsPerHost: 10,
		},
	}
	
	var result *SearchResult
	var lastErr error
	
	// 重试逻辑
	for retries := 3; retries > 0; retries-- {
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("发送请求失败: %v", err)
			if !isRetryableError(err) {
				break
			}
			time.Sleep(time.Second * time.Duration(4-retries))
			continue
		}
		defer resp.Body.Close()
		
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = fmt.Errorf("读取响应失败: %v", err)
			if !isRetryableError(err) {
				break
			}
			time.Sleep(time.Second * time.Duration(4-retries))
			continue
		}
		
		if resp.StatusCode != http.StatusOK {
			switch resp.StatusCode {
			case http.StatusTooManyRequests:
				lastErr = fmt.Errorf("请求过于频繁，请稍后重试")
			case http.StatusNotFound:
				lastErr = fmt.Errorf("未找到相关镜像")
			case http.StatusBadGateway, http.StatusServiceUnavailable:
				lastErr = fmt.Errorf("Docker Hub服务暂时不可用，请稍后重试")
			default:
				lastErr = fmt.Errorf("请求失败: 状态码=%d, 响应=%s", resp.StatusCode, string(body))
			}
			if !isRetryableError(lastErr) {
				break
			}
			time.Sleep(time.Second * time.Duration(4-retries))
			continue
		}
		
		// 解析响应
		if isUserRepo && namespace != "" {
			// 解析用户仓库列表响应
			var userRepos struct {
				Count    int          `json:"count"`
				Next     string       `json:"next"`
				Previous string       `json:"previous"`
				Results  []Repository `json:"results"`
			}
			if err := json.Unmarshal(body, &userRepos); err != nil {
				lastErr = fmt.Errorf("解析响应失败: %v", err)
				break
			}
			
			// 转换为SearchResult格式
			result = &SearchResult{
				Count:    userRepos.Count,
				Next:     userRepos.Next,
				Previous: userRepos.Previous,
				Results:  make([]Repository, 0),
			}
			
			// 处理结果
			for _, repo := range userRepos.Results {
				// 如果指定了仓库名，只保留匹配的结果
				if repoName == "" || strings.Contains(strings.ToLower(repo.Name), strings.ToLower(repoName)) {
					repo.Namespace = namespace
					result.Results = append(result.Results, repo)
				}
			}
			result.Count = len(result.Results)
		} else {
			// 解析普通搜索响应
			result = &SearchResult{}
			if err := json.Unmarshal(body, &result); err != nil {
				lastErr = fmt.Errorf("解析响应失败: %v", err)
				break
			}
			
			// 处理搜索结果
			for i := range result.Results {
				if result.Results[i].IsOfficial {
					if !strings.Contains(result.Results[i].Name, "/") {
						result.Results[i].Name = "library/" + result.Results[i].Name
					}
					result.Results[i].Namespace = "library"
				} else {
					parts := strings.Split(result.Results[i].Name, "/")
					if len(parts) > 1 {
						result.Results[i].Namespace = parts[0]
						result.Results[i].Name = parts[1]
					} else if result.Results[i].RepoOwner != "" {
						result.Results[i].Namespace = result.Results[i].RepoOwner
					}
				}
			}
		}
		
		// 成功获取结果，跳出重试循环
		lastErr = nil
		break
	}
	
	if lastErr != nil {
		return nil, fmt.Errorf("搜索失败: %v", lastErr)
	}
	
	// 缓存结果
	searchCache.Set(cacheKey, result)
	return result, nil
}

// 判断错误是否可重试
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	
	// 网络错误、超时等可以重试
	if strings.Contains(err.Error(), "timeout") ||
	   strings.Contains(err.Error(), "connection refused") ||
	   strings.Contains(err.Error(), "no such host") ||
	   strings.Contains(err.Error(), "too many requests") {
		return true
	}
	
	return false
}

// getRepositoryTags 获取仓库标签信息
func getRepositoryTags(ctx context.Context, namespace, name string) ([]TagInfo, error) {
	if namespace == "" || name == "" {
		return nil, fmt.Errorf("无效输入：命名空间和名称不能为空")
	}

	cacheKey := fmt.Sprintf("tags:%s:%s", namespace, name)
	if cached, ok := searchCache.Get(cacheKey); ok {
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

	// 缓存结果
	searchCache.Set(cacheKey, result.Results)
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

		fmt.Printf("搜索请求: query=%s, page=%d, pageSize=%d\n", query, page, pageSize)

		result, err := searchDockerHub(c.Request.Context(), query, page, pageSize)
		if err != nil {
			fmt.Printf("搜索失败: %v\n", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

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

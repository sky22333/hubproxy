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

// TagPageResult 分页标签结果
type TagPageResult struct {
	Tags    []TagInfo `json:"tags"`
	HasMore bool      `json:"has_more"`
}

type cacheEntry struct {
	data      interface{}
	expiresAt time.Time // 存储过期时间
}

const (
	maxCacheSize        = 1000              // 最大缓存条目数
	maxPaginationCache  = 200               // 分页缓存最大条目数
	cacheTTL            = 30 * time.Minute
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
	
	// 比较过期时间
	if time.Now().After(entry.expiresAt) {
		c.mu.Lock()
		delete(c.data, key)
		c.mu.Unlock()
		return nil, false
	}
	
	return entry.data, true
}

func (c *Cache) Set(key string, data interface{}) {
	c.SetWithTTL(key, data, cacheTTL)
}

func (c *Cache) SetWithTTL(key string, data interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	// 惰性清理：仅在容量超限时清理过期项
	if len(c.data) >= c.maxSize {
		c.cleanupExpiredLocked()
	}
	
	// 计算过期时间
	c.data[key] = cacheEntry{
		data:      data,
		expiresAt: time.Now().Add(ttl),
	}
}

func (c *Cache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupExpiredLocked()
}

// cleanupExpiredLocked 清理过期缓存（需要已持有锁）
func (c *Cache) cleanupExpiredLocked() {
	now := time.Now()
	for key, entry := range c.data {
		if now.After(entry.expiresAt) {
			delete(c.data, key)
		}
	}
}

// 定期清理过期缓存
func init() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop() // 确保ticker资源释放
		
		for range ticker.C {
			searchCache.Cleanup()
		}
	}()
}

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

// normalizeRepository 统一规范化仓库信息（消除重复逻辑）
func normalizeRepository(repo *Repository) {
	if repo.IsOfficial {
		repo.Namespace = "library"
		if !strings.Contains(repo.Name, "/") {
			repo.Name = "library/" + repo.Name
		}
	} else {
		// 处理用户仓库：设置命名空间但保持Name为纯仓库名
		if repo.Namespace == "" && repo.RepoOwner != "" {
			repo.Namespace = repo.RepoOwner
		}
		
		// 如果Name包含斜杠，提取纯仓库名
		if strings.Contains(repo.Name, "/") {
			parts := strings.Split(repo.Name, "/")
			if len(parts) > 1 {
				if repo.Namespace == "" {
					repo.Namespace = parts[0]
				}
				repo.Name = parts[len(parts)-1] // 取最后部分作为仓库名
			}
		}
	}
}

// searchDockerHub 搜索镜像
func searchDockerHub(ctx context.Context, query string, page, pageSize int) (*SearchResult, error) {
	return searchDockerHubWithDepth(ctx, query, page, pageSize, 0)
}

// searchDockerHubWithDepth 搜索镜像（带递归深度控制）
func searchDockerHubWithDepth(ctx context.Context, query string, page, pageSize int, depth int) (*SearchResult, error) {
	// 防止无限递归：最多允许1次递归调用
	if depth > 1 {
		return nil, fmt.Errorf("搜索请求过于复杂，请尝试更具体的关键词")
	}
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
	baseURL := "https://registry.hub.docker.com/v2"
	var fullURL string
	var params url.Values
	
	if isUserRepo && namespace != "" {
		// 如果是用户/仓库格式，使用repositories接口
		fullURL = fmt.Sprintf("%s/repositories/%s/", baseURL, namespace)
		params = url.Values{
			"page":      {fmt.Sprintf("%d", page)},
			"page_size": {fmt.Sprintf("%d", pageSize)},
		}
	} else {
		// 普通搜索
		fullURL = baseURL + "/search/repositories/"
		params = url.Values{
			"query":     {query},
			"page":      {fmt.Sprintf("%d", page)},
			"page_size": {fmt.Sprintf("%d", pageSize)},
		}
	}
	
	fullURL = fullURL + "?" + params.Encode()
	
	// 使用统一的搜索HTTP客户端
	resp, err := GetSearchHTTPClient().Get(fullURL)
	if err != nil {
		return nil, fmt.Errorf("请求Docker Hub API失败: %v", err)
	}
	defer safeCloseResponseBody(resp.Body, "搜索响应体")
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}
	
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			return nil, fmt.Errorf("请求过于频繁，请稍后重试")
		case http.StatusNotFound:
			if isUserRepo && namespace != "" {
				// 如果用户仓库搜索失败，尝试普通搜索（递归调用）
				return searchDockerHubWithDepth(ctx, repoName, page, pageSize, depth+1)
			}
			return nil, fmt.Errorf("未找到相关镜像")
		case http.StatusBadGateway, http.StatusServiceUnavailable:
			return nil, fmt.Errorf("Docker Hub服务暂时不可用，请稍后重试")
		default:
			return nil, fmt.Errorf("请求失败: 状态码=%d, 响应=%s", resp.StatusCode, string(body))
		}
	}
	
	// 解析响应
	var result *SearchResult
	if isUserRepo && namespace != "" {
		// 解析用户仓库列表响应
		var userRepos struct {
			Count    int          `json:"count"`
			Next     string       `json:"next"`
			Previous string       `json:"previous"`
			Results  []Repository `json:"results"`
		}
		if err := json.Unmarshal(body, &userRepos); err != nil {
			return nil, fmt.Errorf("解析响应失败: %v", err)
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
				// 设置命名空间并使用统一的规范化函数
				repo.Namespace = namespace
				normalizeRepository(&repo)
				result.Results = append(result.Results, repo)
			}
		}
		
		// 如果没有找到结果，尝试普通搜索（递归调用）
		if len(result.Results) == 0 {
			return searchDockerHubWithDepth(ctx, repoName, page, pageSize, depth+1)
		}
		
		result.Count = len(result.Results)
	} else {
		// 解析普通搜索响应
		result = &SearchResult{}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("解析响应失败: %v", err)
		}
		
		// 处理搜索结果：使用统一的规范化函数
		for i := range result.Results {
			normalizeRepository(&result.Results[i])
		}
		
		// 如果是用户/仓库搜索，过滤结果
		if isUserRepo && namespace != "" {
			filteredResults := make([]Repository, 0)
			for _, repo := range result.Results {
				if strings.EqualFold(repo.Namespace, namespace) {
					filteredResults = append(filteredResults, repo)
				}
			}
			result.Results = filteredResults
			result.Count = len(filteredResults)
		}
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

// getRepositoryTags 获取仓库标签信息（支持分页）
func getRepositoryTags(ctx context.Context, namespace, name string, page, pageSize int) ([]TagInfo, bool, error) {
	if namespace == "" || name == "" {
		return nil, false, fmt.Errorf("无效输入：命名空间和名称不能为空")
	}

	// 默认参数
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 100
	}

	// 分页缓存key
	cacheKey := fmt.Sprintf("tags:%s:%s:page_%d", namespace, name, page)
	if cached, ok := searchCache.Get(cacheKey); ok {
		result := cached.(TagPageResult)
		return result.Tags, result.HasMore, nil
	}

	// 构建API URL
	baseURL := fmt.Sprintf("https://registry.hub.docker.com/v2/repositories/%s/%s/tags", namespace, name)
	params := url.Values{}
	params.Set("page", fmt.Sprintf("%d", page))
	params.Set("page_size", fmt.Sprintf("%d", pageSize))
	params.Set("ordering", "last_updated")

	fullURL := baseURL + "?" + params.Encode()

	// 获取当前页数据
	pageResult, err := fetchTagPage(ctx, fullURL, 3)
	if err != nil {
		return nil, false, fmt.Errorf("获取标签失败: %v", err)
	}

	hasMore := pageResult.Next != ""

	// 缓存结果（分页缓存时间较短）
	result := TagPageResult{Tags: pageResult.Results, HasMore: hasMore}
	searchCache.SetWithTTL(cacheKey, result, 30*time.Minute)

	return pageResult.Results, hasMore, nil
}

// fetchTagPage 获取单页标签数据，带重试机制
func fetchTagPage(ctx context.Context, url string, maxRetries int) (*struct {
	Count    int       `json:"count"`
	Next     string    `json:"next"`
	Previous string    `json:"previous"`
	Results  []TagInfo `json:"results"`
}, error) {
	var lastErr error
	
	for retry := 0; retry < maxRetries; retry++ {
		if retry > 0 {
			// 重试前等待一段时间
			time.Sleep(time.Duration(retry) * 500 * time.Millisecond)
		}

		resp, err := GetSearchHTTPClient().Get(url)
		if err != nil {
			lastErr = err
			if isRetryableError(err) && retry < maxRetries-1 {
				continue
			}
			return nil, fmt.Errorf("发送请求失败: %v", err)
		}

		// 读取响应体（立即关闭，避免defer在循环中累积）
		body, err := func() ([]byte, error) {
			defer safeCloseResponseBody(resp.Body, "标签响应体")
			return io.ReadAll(resp.Body)
		}()
		
		if err != nil {
			lastErr = err
			if retry < maxRetries-1 {
				continue
			}
			return nil, fmt.Errorf("读取响应失败: %v", err)
		}

		// 检查响应状态码
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("状态码=%d, 响应=%s", resp.StatusCode, string(body))
			// 4xx错误通常不需要重试
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
				return nil, fmt.Errorf("请求失败: %v", lastErr)
			}
			if retry < maxRetries-1 {
				continue
			}
			return nil, fmt.Errorf("请求失败: %v", lastErr)
		}

		// 解析响应
		var result struct {
			Count    int       `json:"count"`
			Next     string    `json:"next"`
			Previous string    `json:"previous"`
			Results  []TagInfo `json:"results"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			lastErr = err
			if retry < maxRetries-1 {
				continue
			}
			return nil, fmt.Errorf("解析响应失败: %v", err)
		}

		return &result, nil
	}
	
	return nil, lastErr
}

// parsePaginationParams 解析分页参数
func parsePaginationParams(c *gin.Context, defaultPageSize int) (page, pageSize int) {
	page = 1
	pageSize = defaultPageSize
	
	if p := c.Query("page"); p != "" {
		fmt.Sscanf(p, "%d", &page)
	}
	if ps := c.Query("page_size"); ps != "" {
		fmt.Sscanf(ps, "%d", &pageSize)
	}
	
	return page, pageSize
}

// safeCloseResponseBody 安全关闭HTTP响应体（统一资源管理）
func safeCloseResponseBody(body io.ReadCloser, context string) {
	if body != nil {
		if err := body.Close(); err != nil {
			fmt.Printf("关闭%s失败: %v\n", context, err)
		}
	}
}

// sendErrorResponse 统一错误响应处理
func sendErrorResponse(c *gin.Context, message string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": message})
}

// RegisterSearchRoute 注册搜索相关路由
func RegisterSearchRoute(r *gin.Engine) {
	// 搜索镜像
	r.GET("/search", func(c *gin.Context) {
		query := c.Query("q")
		if query == "" {
			sendErrorResponse(c, "搜索关键词不能为空")
			return
		}

		page, pageSize := parsePaginationParams(c, 25)

		result, err := searchDockerHub(c.Request.Context(), query, page, pageSize)
		if err != nil {
			sendErrorResponse(c, err.Error())
			return
		}

		c.JSON(http.StatusOK, result)
	})

	// 获取标签信息
	r.GET("/tags/:namespace/:name", func(c *gin.Context) {
		namespace := c.Param("namespace")
		name := c.Param("name")

		if namespace == "" || name == "" {
			sendErrorResponse(c, "命名空间和名称不能为空")
			return
		}

		page, pageSize := parsePaginationParams(c, 100)

		tags, hasMore, err := getRepositoryTags(c.Request.Context(), namespace, name, page, pageSize)
		if err != nil {
			sendErrorResponse(c, err.Error())
			return
		}

		if c.Query("page") != "" || c.Query("page_size") != "" {
			c.JSON(http.StatusOK, gin.H{
				"tags":      tags,
				"has_more":  hasMore,
				"page":      page,
				"page_size": pageSize,
			})
		} else {
			c.JSON(http.StatusOK, tags)
		}
	})
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
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
	Name           string    `json:"name"`
	Namespace      string    `json:"namespace"`
	Description    string    `json:"description"`
	IsOfficial     bool      `json:"is_official"`
	StarCount      int       `json:"star_count"`
	PullCount      int       `json:"pull_count"`
	LastUpdated    time.Time `json:"last_updated"`
	Status         int       `json:"status"`
	Organization   string    `json:"organization,omitempty"`
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

// searchWithSkopeo 使用skopeo搜索镜像
func searchWithSkopeo(ctx context.Context, query string) (*SearchResult, error) {
	// 执行skopeo search命令
	cmd := exec.CommandContext(ctx, "skopeo", "list-tags", fmt.Sprintf("docker://docker.io/%s", query))
	output, err := cmd.CombinedOutput()
	if err != nil {
		// 如果是因为找不到镜像，尝试搜索
		cmd = exec.CommandContext(ctx, "skopeo", "search", fmt.Sprintf("docker://%s", query))
		output, err = cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("搜索失败: %v, 输出: %s", err, string(output))
		}
	}

	// 解析输出
	var result SearchResult
	result.Results = make([]Repository, 0)

	// 按行解析输出
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 解析仓库信息
		parts := strings.Fields(line)
		if len(parts) < 1 {
			continue
		}

		fullName := parts[0]
		nameParts := strings.Split(fullName, "/")
		
		repo := Repository{}
		
		if len(nameParts) == 1 {
			repo.Name = nameParts[0]
			repo.Namespace = "library"
			repo.IsOfficial = true
		} else {
			repo.Name = nameParts[len(nameParts)-1]
			repo.Namespace = strings.Join(nameParts[:len(nameParts)-1], "/")
		}

		if len(parts) > 1 {
			repo.Description = strings.Join(parts[1:], " ")
		}

		result.Results = append(result.Results, repo)
	}

	result.Count = len(result.Results)
	return &result, nil
}

// getTagsWithSkopeo 使用skopeo获取标签信息
func getTagsWithSkopeo(ctx context.Context, namespace, name string) ([]TagInfo, error) {
	repoName := name
	if namespace != "library" {
		repoName = namespace + "/" + name
	}

	// 执行skopeo list-tags命令
	cmd := exec.CommandContext(ctx, "skopeo", "list-tags", fmt.Sprintf("docker://docker.io/%s", repoName))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("获取标签失败: %v, 输出: %s", err, string(output))
	}

	var tags []TagInfo
	if err := json.Unmarshal(output, &tags); err != nil {
		// 如果解析JSON失败，尝试按行解析
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			
			tag := TagInfo{
				Name: line,
				LastUpdated: time.Now(),
			}
			tags = append(tags, tag)
		}
	}

	return tags, nil
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

		result, err := searchWithSkopeo(c.Request.Context(), query)
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
		
		fmt.Printf("获取标签请求: namespace=%s, name=%s\n", namespace, name)
		
		tags, err := getTagsWithSkopeo(c.Request.Context(), namespace, name)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, tags)
	})
}

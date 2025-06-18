package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// CachedItem 通用缓存项，支持Token和Manifest
type CachedItem struct {
	Data        []byte         // 缓存数据(token字符串或manifest字节)
	ContentType string         // 内容类型
	Headers     map[string]string // 额外的响应头
	ExpiresAt   time.Time      // 过期时间
}

// UniversalCache 通用缓存，支持Token和Manifest
type UniversalCache struct {
	cache sync.Map
}

var globalCache = &UniversalCache{}

// Get 获取缓存项
func (c *UniversalCache) Get(key string) *CachedItem {
	if v, ok := c.cache.Load(key); ok {
		if cached := v.(*CachedItem); time.Now().Before(cached.ExpiresAt) {
			return cached
		}
		c.cache.Delete(key)
	}
	return nil
}

func (c *UniversalCache) Set(key string, data []byte, contentType string, headers map[string]string, ttl time.Duration) {
	c.cache.Store(key, &CachedItem{
		Data:        data,
		ContentType: contentType,
		Headers:     headers,
		ExpiresAt:   time.Now().Add(ttl),
	})
}

func (c *UniversalCache) GetToken(key string) string {
	if item := c.Get(key); item != nil {
		return string(item.Data)
	}
	return ""
}

func (c *UniversalCache) SetToken(key, token string, ttl time.Duration) {
	c.Set(key, []byte(token), "application/json", nil, ttl)
}

// buildCacheKey 构建稳定的缓存key
func buildCacheKey(prefix, query string) string {
	return fmt.Sprintf("%s:%x", prefix, md5.Sum([]byte(query)))
}

func buildTokenCacheKey(query string) string {
	return buildCacheKey("token", query)
}

func buildManifestCacheKey(imageRef, reference string) string {
	key := fmt.Sprintf("%s:%s", imageRef, reference)
	return buildCacheKey("manifest", key)
}

func buildManifestCacheKeyWithPlatform(imageRef, reference, platform string) string {
	if platform == "" {
		platform = "default"
	}
	key := fmt.Sprintf("%s:%s@%s", imageRef, reference, platform)
	return buildCacheKey("manifest", key)
}

func getManifestTTL(reference string) time.Duration {
	cfg := GetConfig()
	defaultTTL := 30 * time.Minute
	if cfg.TokenCache.DefaultTTL != "" {
		if parsed, err := time.ParseDuration(cfg.TokenCache.DefaultTTL); err == nil {
			defaultTTL = parsed
		}
	}
	
	if strings.HasPrefix(reference, "sha256:") {
		return 24 * time.Hour
	}
	
	// mutable tag的智能判断
	if reference == "latest" || reference == "main" || reference == "master" || 
	   reference == "dev" || reference == "develop" {
		// 热门可变标签: 短期缓存
		return 10 * time.Minute
	}
	
	return defaultTTL
}

// extractTTLFromResponse 从响应中智能提取TTL
func extractTTLFromResponse(responseBody []byte) time.Duration {
	var tokenResp struct {
		ExpiresIn int `json:"expires_in"`
	}
	
	// 默认30分钟TTL，确保稳定性
	defaultTTL := 30 * time.Minute
	
	if json.Unmarshal(responseBody, &tokenResp) == nil && tokenResp.ExpiresIn > 0 {
		safeTTL := time.Duration(tokenResp.ExpiresIn-300) * time.Second
		if safeTTL > 5*time.Minute {
			return safeTTL
		}
	}
	
	return defaultTTL
}

func writeTokenResponse(c *gin.Context, cachedBody string) {
	c.Header("Content-Type", "application/json")
	c.String(200, cachedBody)
}

func writeCachedResponse(c *gin.Context, item *CachedItem) {
	if item.ContentType != "" {
		c.Header("Content-Type", item.ContentType)
	}
	
	// 设置额外的响应头
	for key, value := range item.Headers {
		c.Header(key, value)
	}
	
	// 返回数据
	c.Data(200, item.ContentType, item.Data)
}

// isCacheEnabled 检查缓存是否启用
func isCacheEnabled() bool {
	cfg := GetConfig()
	return cfg.TokenCache.Enabled
}

// isTokenCacheEnabled 检查token缓存是否启用(向后兼容)
func isTokenCacheEnabled() bool {
	return isCacheEnabled()
} 
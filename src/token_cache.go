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
	cache sync.Map // 线程安全的并发映射
}

var globalCache = &UniversalCache{}

// Get 获取缓存项，如果不存在或过期返回nil
func (c *UniversalCache) Get(key string) *CachedItem {
	if v, ok := c.cache.Load(key); ok {
		if cached := v.(*CachedItem); time.Now().Before(cached.ExpiresAt) {
			return cached
		}
		// 自动清理过期项，保持内存整洁
		c.cache.Delete(key)
	}
	return nil
}

// Set 设置缓存项
func (c *UniversalCache) Set(key string, data []byte, contentType string, headers map[string]string, ttl time.Duration) {
	c.cache.Store(key, &CachedItem{
		Data:        data,
		ContentType: contentType,
		Headers:     headers,
		ExpiresAt:   time.Now().Add(ttl),
	})
}

// GetToken 获取缓存的token(向后兼容)
func (c *UniversalCache) GetToken(key string) string {
	if item := c.Get(key); item != nil {
		return string(item.Data)
	}
	return ""
}

// SetToken 设置token缓存(向后兼容)
func (c *UniversalCache) SetToken(key, token string, ttl time.Duration) {
	c.Set(key, []byte(token), "application/json", nil, ttl)
}

// buildCacheKey 构建稳定的缓存key
func buildCacheKey(prefix, query string) string {
	// 使用MD5确保key的一致性和简洁性
	return fmt.Sprintf("%s:%x", prefix, md5.Sum([]byte(query)))
}

// buildTokenCacheKey 构建token缓存key(向后兼容)
func buildTokenCacheKey(query string) string {
	return buildCacheKey("token", query)
}

// buildManifestCacheKey 构建manifest缓存key
func buildManifestCacheKey(imageRef, reference string) string {
	key := fmt.Sprintf("%s:%s", imageRef, reference)
	return buildCacheKey("manifest", key)
}

// getManifestTTL 根据引用类型智能确定TTL
func getManifestTTL(reference string) time.Duration {
	cfg := GetConfig()
	defaultTTL := 30 * time.Minute
	if cfg.TokenCache.DefaultTTL != "" {
		if parsed, err := time.ParseDuration(cfg.TokenCache.DefaultTTL); err == nil {
			defaultTTL = parsed
		}
	}
	
	// 智能TTL策略
	if strings.HasPrefix(reference, "sha256:") {
		// immutable digest: 长期缓存
		return 24 * time.Hour
	}
	
	// mutable tag的智能判断
	if reference == "latest" || reference == "main" || reference == "master" || 
	   reference == "dev" || reference == "develop" {
		// 热门可变标签: 短期缓存
		return 10 * time.Minute
	}
	
	// 普通tag: 中等缓存时间
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
		// 使用响应中的过期时间，但提前5分钟过期确保安全边际
		safeTTL := time.Duration(tokenResp.ExpiresIn-300) * time.Second
		if safeTTL > 5*time.Minute { // 确保至少有5分钟的缓存时间
			return safeTTL
		}
	}
	
	return defaultTTL
}

// writeTokenResponse 写入token响应(向后兼容)
func writeTokenResponse(c *gin.Context, cachedBody string) {
	// 直接返回缓存的完整响应体，保持格式一致性
	c.Header("Content-Type", "application/json")
	c.String(200, cachedBody)
}

// writeCachedResponse 写入缓存响应
func writeCachedResponse(c *gin.Context, item *CachedItem) {
	// 设置内容类型
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
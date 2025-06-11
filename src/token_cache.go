package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// CachedToken 缓存的Token信息
type CachedToken struct {
	Token     string
	ExpiresAt time.Time
}

// SimpleTokenCache 极简Token缓存
type SimpleTokenCache struct {
	cache sync.Map // 线程安全的并发映射
}

var globalTokenCache = &SimpleTokenCache{}

// Get 获取缓存的token，如果不存在或过期返回空字符串
func (c *SimpleTokenCache) Get(key string) string {
	if v, ok := c.cache.Load(key); ok {
		if cached := v.(*CachedToken); time.Now().Before(cached.ExpiresAt) {
			return cached.Token
		}
		// 自动清理过期token，保持内存整洁
		c.cache.Delete(key)
	}
	return ""
}

// Set 设置token缓存，自动计算过期时间
func (c *SimpleTokenCache) Set(key, token string, ttl time.Duration) {
	c.cache.Store(key, &CachedToken{
		Token:     token,
		ExpiresAt: time.Now().Add(ttl),
	})
}

// buildCacheKey 构建稳定的缓存key
func buildCacheKey(query string) string {
	// 使用MD5确保key的一致性和简洁性
	return fmt.Sprintf("token:%x", md5.Sum([]byte(query)))
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

// writeTokenResponse 写入token响应
func writeTokenResponse(c *gin.Context, cachedBody string) {
	// 直接返回缓存的完整响应体，保持格式一致性
	c.Header("Content-Type", "application/json")
	c.String(200, cachedBody)
}

// isTokenCacheEnabled 检查token缓存是否启用
func isTokenCacheEnabled() bool {
	cfg := GetConfig()
	return cfg.TokenCache.Enabled
} 
package utils

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"hubproxy/config"
)

// CachedItem 通用缓存项
type CachedItem struct {
	Data        []byte
	ContentType string
	Headers     map[string]string
	ExpiresAt   time.Time
}

// UniversalCache 通用缓存
type UniversalCache struct {
	cache sync.Map
}

var GlobalCache = &UniversalCache{}

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

// BuildCacheKey 构建稳定的缓存key
func BuildCacheKey(prefix, query string) string {
	return fmt.Sprintf("%s:%x", prefix, md5.Sum([]byte(query)))
}

func BuildTokenCacheKey(query string) string {
	return BuildCacheKey("token", query)
}

func BuildManifestCacheKey(imageRef, reference string) string {
	key := fmt.Sprintf("%s:%s", imageRef, reference)
	return BuildCacheKey("manifest", key)
}

func GetManifestTTL(reference string) time.Duration {
	cfg := config.GetConfig()
	defaultTTL := 30 * time.Minute
	if cfg.TokenCache.DefaultTTL != "" {
		if parsed, err := time.ParseDuration(cfg.TokenCache.DefaultTTL); err == nil {
			defaultTTL = parsed
		}
	}

	if strings.HasPrefix(reference, "sha256:") {
		return 24 * time.Hour
	}

	if reference == "latest" || reference == "main" || reference == "master" ||
		reference == "dev" || reference == "develop" {
		return 10 * time.Minute
	}

	return defaultTTL
}

// ExtractTTLFromResponse 从响应中智能提取TTL
func ExtractTTLFromResponse(responseBody []byte) time.Duration {
	var tokenResp struct {
		ExpiresIn int `json:"expires_in"`
	}

	defaultTTL := 30 * time.Minute

	if json.Unmarshal(responseBody, &tokenResp) == nil && tokenResp.ExpiresIn > 0 {
		safeTTL := time.Duration(tokenResp.ExpiresIn-300) * time.Second
		if safeTTL > 5*time.Minute {
			return safeTTL
		}
	}

	return defaultTTL
}

func WriteTokenResponse(c *gin.Context, cachedBody string) {
	c.Header("Content-Type", "application/json")
	c.String(200, cachedBody)
}

func WriteCachedResponse(c *gin.Context, item *CachedItem) {
	if item.ContentType != "" {
		c.Header("Content-Type", item.ContentType)
	}

	for key, value := range item.Headers {
		c.Header(key, value)
	}

	c.Data(200, item.ContentType, item.Data)
}

// IsCacheEnabled 检查缓存是否启用
func IsCacheEnabled() bool {
	cfg := config.GetConfig()
	return cfg.TokenCache.Enabled
}

// IsTokenCacheEnabled 检查token缓存是否启用
func IsTokenCacheEnabled() bool {
	return IsCacheEnabled()
}

// 定期清理过期缓存
func init() {
	go func() {
		ticker := time.NewTicker(20 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			now := time.Now()
			expiredKeys := make([]string, 0)

			GlobalCache.cache.Range(func(key, value interface{}) bool {
				if cached := value.(*CachedItem); now.After(cached.ExpiresAt) {
					expiredKeys = append(expiredKeys, key.(string))
				}
				return true
			})

			for _, key := range expiredKeys {
				GlobalCache.cache.Delete(key)
			}
		}
	}()
}

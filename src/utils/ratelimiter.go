package utils

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
	"hubproxy/config"
)

const (
	CleanupInterval = 10 * time.Minute
	MaxIPCacheSize  = 10000
)

// IPRateLimiter IP限流器结构体
type IPRateLimiter struct {
	ips       map[string]*rateLimiterEntry
	mu        *sync.RWMutex
	r         rate.Limit
	b         int
	whitelist []*net.IPNet
	blacklist []*net.IPNet
}

// rateLimiterEntry 限流器条目
type rateLimiterEntry struct {
	limiter    *rate.Limiter
	lastAccess time.Time
}

// InitGlobalLimiter 初始化全局限流器
func InitGlobalLimiter() *IPRateLimiter {
	cfg := config.GetConfig()

	whitelist := make([]*net.IPNet, 0, len(cfg.Security.WhiteList))
	for _, item := range cfg.Security.WhiteList {
		if item = strings.TrimSpace(item); item != "" {
			if !strings.Contains(item, "/") {
				item = item + "/32"
			}
			_, ipnet, err := net.ParseCIDR(item)
			if err == nil {
				whitelist = append(whitelist, ipnet)
			} else {
				fmt.Printf("警告: 无效的白名单IP格式: %s\n", item)
			}
		}
	}

	blacklist := make([]*net.IPNet, 0, len(cfg.Security.BlackList))
	for _, item := range cfg.Security.BlackList {
		if item = strings.TrimSpace(item); item != "" {
			if !strings.Contains(item, "/") {
				item = item + "/32"
			}
			_, ipnet, err := net.ParseCIDR(item)
			if err == nil {
				blacklist = append(blacklist, ipnet)
			} else {
				fmt.Printf("警告: 无效的黑名单IP格式: %s\n", item)
			}
		}
	}

	ratePerSecond := rate.Limit(float64(cfg.RateLimit.RequestLimit) / (cfg.RateLimit.PeriodHours * 3600))

	burstSize := cfg.RateLimit.RequestLimit
	if burstSize < 1 {
		burstSize = 1
	}

	limiter := &IPRateLimiter{
		ips:       make(map[string]*rateLimiterEntry),
		mu:        &sync.RWMutex{},
		r:         ratePerSecond,
		b:         burstSize,
		whitelist: whitelist,
		blacklist: blacklist,
	}

	go limiter.cleanupRoutine()

	return limiter
}

// cleanupRoutine 定期清理过期的限流器
func (i *IPRateLimiter) cleanupRoutine() {
	ticker := time.NewTicker(CleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		expired := make([]string, 0)

		i.mu.RLock()
		for ip, entry := range i.ips {
			if now.Sub(entry.lastAccess) > 1*time.Hour {
				expired = append(expired, ip)
			}
		}
		i.mu.RUnlock()

		if len(expired) > 0 || len(i.ips) > MaxIPCacheSize {
			i.mu.Lock()
			for _, ip := range expired {
				delete(i.ips, ip)
			}

			if len(i.ips) > MaxIPCacheSize {
				i.ips = make(map[string]*rateLimiterEntry)
			}
			i.mu.Unlock()
		}
	}
}

// extractIPFromAddress 从地址中提取纯IP
func extractIPFromAddress(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

// normalizeIPForRateLimit 标准化IP地址用于限流
func normalizeIPForRateLimit(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ipStr
	}

	if ip.To4() != nil {
		return ipStr
	}

	ipv6 := ip.To16()
	for i := 8; i < 16; i++ {
		ipv6[i] = 0
	}
	return ipv6.String() + "/64"
}

// isIPInCIDRList 检查IP是否在CIDR列表中
func isIPInCIDRList(ip string, cidrList []*net.IPNet) bool {
	cleanIP := extractIPFromAddress(ip)
	parsedIP := net.ParseIP(cleanIP)
	if parsedIP == nil {
		return false
	}

	for _, cidr := range cidrList {
		if cidr.Contains(parsedIP) {
			return true
		}
	}
	return false
}

// GetLimiter 获取指定IP的限流器
func (i *IPRateLimiter) GetLimiter(ip string) (*rate.Limiter, bool) {
	cleanIP := extractIPFromAddress(ip)

	if isIPInCIDRList(cleanIP, i.blacklist) {
		return nil, false
	}

	if isIPInCIDRList(cleanIP, i.whitelist) {
		return rate.NewLimiter(rate.Inf, i.b), true
	}

	normalizedIP := normalizeIPForRateLimit(cleanIP)

	now := time.Now()

	i.mu.RLock()
	entry, exists := i.ips[normalizedIP]
	i.mu.RUnlock()

	if exists {
		i.mu.Lock()
		if entry, stillExists := i.ips[normalizedIP]; stillExists {
			entry.lastAccess = now
			i.mu.Unlock()
			return entry.limiter, true
		}
		i.mu.Unlock()
	}

	i.mu.Lock()
	if entry, exists := i.ips[normalizedIP]; exists {
		entry.lastAccess = now
		i.mu.Unlock()
		return entry.limiter, true
	}

	entry = &rateLimiterEntry{
		limiter:    rate.NewLimiter(i.r, i.b),
		lastAccess: now,
	}
	i.ips[normalizedIP] = entry
	i.mu.Unlock()

	return entry.limiter, true
}

// RateLimitMiddleware 速率限制中间件
func RateLimitMiddleware(limiter *IPRateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if path == "/" || path == "/favicon.ico" || path == "/images.html" || path == "/search.html" ||
			strings.HasPrefix(path, "/public/") {
			c.Next()
			return
		}

		var ip string

		if forwarded := c.GetHeader("X-Forwarded-For"); forwarded != "" {
			ips := strings.Split(forwarded, ",")
			ip = strings.TrimSpace(ips[0])
		} else if realIP := c.GetHeader("X-Real-IP"); realIP != "" {
			ip = realIP
		} else if remoteIP := c.GetHeader("X-Original-Forwarded-For"); remoteIP != "" {
			ips := strings.Split(remoteIP, ",")
			ip = strings.TrimSpace(ips[0])
		} else {
			ip = c.ClientIP()
		}

		cleanIP := extractIPFromAddress(ip)

		normalizedIP := normalizeIPForRateLimit(cleanIP)
		if cleanIP != normalizedIP {
			fmt.Printf("请求IP: %s (提纯后: %s, 限流段: %s), X-Forwarded-For: %s, X-Real-IP: %s\n",
				ip, cleanIP, normalizedIP,
				c.GetHeader("X-Forwarded-For"),
				c.GetHeader("X-Real-IP"))
		} else {
			fmt.Printf("请求IP: %s (提纯后: %s), X-Forwarded-For: %s, X-Real-IP: %s\n",
				ip, cleanIP,
				c.GetHeader("X-Forwarded-For"),
				c.GetHeader("X-Real-IP"))
		}

		ipLimiter, allowed := limiter.GetLimiter(cleanIP)

		if !allowed {
			c.JSON(403, gin.H{
				"error": "您已被限制访问",
			})
			c.Abort()
			return
		}

		if !ipLimiter.Allow() {
			c.JSON(429, gin.H{
				"error": "请求频率过快，暂时限制访问",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}
package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// IP限流配置
var (
	// 默认限流：每个IP每1小时允许20个请求
	DefaultRateLimit      = 20.0  // 默认限制请求数
	DefaultRatePeriodHours = 1.0   // 默认时间周期（小时）
	
	// 白名单列表，支持IP和CIDR格式，如："192.168.1.1", "10.0.0.0/8"
	WhitelistIPs = []string{
		"127.0.0.1",     // 本地回环地址
		"10.0.0.0/8",    // 内网地址段
		"172.16.0.0/12", // 内网地址段
		"192.168.0.0/16", // 内网地址段
	}
	
	// 黑名单列表，支持IP和CIDR格式
	BlacklistIPs = []string{
		// 示例: "1.2.3.4", "5.6.7.0/24"
	}
	
	// 清理间隔：多久清理一次过期的限流器
	CleanupInterval = 1 * time.Hour
	
	// IP限流器缓存上限，超过此数量将触发清理
	MaxIPCacheSize = 10000
)

// IPRateLimiter 定义IP限流器结构
type IPRateLimiter struct {
	ips       map[string]*rateLimiterEntry // IP到限流器的映射
	mu        *sync.RWMutex                // 读写锁，保证并发安全
	r         rate.Limit                   // 速率限制（每秒允许的请求数）
	b         int                          // 令牌桶容量（突发请求数）
	whitelist []*net.IPNet                 // 白名单IP段
	blacklist []*net.IPNet                 // 黑名单IP段
}

// rateLimiterEntry 限流器条目，包含限流器和最后访问时间
type rateLimiterEntry struct {
	limiter    *rate.Limiter // 限流器
	lastAccess time.Time     // 最后访问时间
}

// NewIPRateLimiter 创建新的IP限流器
func NewIPRateLimiter() *IPRateLimiter {
	// 从环境变量读取限流配置（如果有）
	rateLimit := DefaultRateLimit
	ratePeriod := DefaultRatePeriodHours
	
	if val, exists := os.LookupEnv("RATE_LIMIT"); exists {
		if parsed, err := strconv.ParseFloat(val, 64); err == nil && parsed > 0 {
			rateLimit = parsed
		}
	}
	
	if val, exists := os.LookupEnv("RATE_PERIOD_HOURS"); exists {
		if parsed, err := strconv.ParseFloat(val, 64); err == nil && parsed > 0 {
			ratePeriod = parsed
		}
	}
	
	// 从环境变量读取白名单（如果有）
	whitelistIPs := WhitelistIPs
	if val, exists := os.LookupEnv("IP_WHITELIST"); exists && val != "" {
		whitelistIPs = append(whitelistIPs, strings.Split(val, ",")...)
	}
	
	// 从环境变量读取黑名单（如果有）
	blacklistIPs := BlacklistIPs
	if val, exists := os.LookupEnv("IP_BLACKLIST"); exists && val != "" {
		blacklistIPs = append(blacklistIPs, strings.Split(val, ",")...)
	}
	
	// 解析白名单IP段
	whitelist := make([]*net.IPNet, 0, len(whitelistIPs))
	for _, item := range whitelistIPs {
		if item = strings.TrimSpace(item); item != "" {
			if !strings.Contains(item, "/") {
				item = item + "/32" // 单个IP转为CIDR格式
			}
			_, ipnet, err := net.ParseCIDR(item)
			if err == nil {
				whitelist = append(whitelist, ipnet)
			}
		}
	}
	
	// 解析黑名单IP段
	blacklist := make([]*net.IPNet, 0, len(blacklistIPs))
	for _, item := range blacklistIPs {
		if item = strings.TrimSpace(item); item != "" {
			if !strings.Contains(item, "/") {
				item = item + "/32" // 单个IP转为CIDR格式
			}
			_, ipnet, err := net.ParseCIDR(item)
			if err == nil {
				blacklist = append(blacklist, ipnet)
			}
		}
	}
	
	// 计算速率：将 "每N小时X个请求" 转换为 "每秒Y个请求"
	// rate.Limit的单位是每秒允许的请求数
	ratePerSecond := rate.Limit(rateLimit / (ratePeriod * 3600))
	
	limiter := &IPRateLimiter{
		ips:       make(map[string]*rateLimiterEntry),
		mu:        &sync.RWMutex{},
		r:         ratePerSecond,
		b:         int(rateLimit), // 令牌桶容量设为允许的请求总数
		whitelist: whitelist,
		blacklist: blacklist,
	}
	
	// 启动定期清理goroutine
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
		
		// 查找过期的条目
		i.mu.RLock()
		for ip, entry := range i.ips {
			// 如果最后访问时间超过1小时，认为过期
			if now.Sub(entry.lastAccess) > 1*time.Hour {
				expired = append(expired, ip)
			}
		}
		i.mu.RUnlock()
		
		// 如果有过期条目或者缓存过大，进行清理
		if len(expired) > 0 || len(i.ips) > MaxIPCacheSize {
			i.mu.Lock()
			// 删除过期条目
			for _, ip := range expired {
				delete(i.ips, ip)
			}
			
			// 如果缓存仍然过大，全部清理
			if len(i.ips) > MaxIPCacheSize {
				i.ips = make(map[string]*rateLimiterEntry)
			}
			i.mu.Unlock()
		}
	}
}

// isIPInCIDRList 检查IP是否在CIDR列表中
func isIPInCIDRList(ip string, cidrList []*net.IPNet) bool {
	parsedIP := net.ParseIP(ip)
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

// GetLimiter 获取指定IP的限流器，同时返回是否允许访问
func (i *IPRateLimiter) GetLimiter(ip string) (*rate.Limiter, bool) {
	// 检查是否在黑名单中
	if isIPInCIDRList(ip, i.blacklist) {
		return nil, false // 黑名单中的IP不允许访问
	}
	
	// 检查是否在白名单中
	if isIPInCIDRList(ip, i.whitelist) {
		return rate.NewLimiter(rate.Inf, i.b), true // 白名单中的IP不受限制
	}
	
	// 从缓存获取限流器
	i.mu.RLock()
	entry, exists := i.ips[ip]
	i.mu.RUnlock()
	
	now := time.Now()
	
	if !exists {
		// 创建新的限流器
		i.mu.Lock()
		entry = &rateLimiterEntry{
			limiter:    rate.NewLimiter(i.r, i.b),
			lastAccess: now,
		}
		i.ips[ip] = entry
		i.mu.Unlock()
	} else {
		// 更新最后访问时间
		i.mu.Lock()
		entry.lastAccess = now
		i.mu.Unlock()
	}
	
	return entry.limiter, true
}

// RateLimitMiddleware 速率限制中间件
func RateLimitMiddleware(limiter *IPRateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 获取客户端真实IP
		var ip string
		
		// 优先尝试从请求头获取真实IP
		if forwarded := c.GetHeader("X-Forwarded-For"); forwarded != "" {
			// X-Forwarded-For可能包含多个IP，取第一个
			ips := strings.Split(forwarded, ",")
			ip = strings.TrimSpace(ips[0])
		} else if realIP := c.GetHeader("X-Real-IP"); realIP != "" {
			// 如果有X-Real-IP头
			ip = realIP
		} else if remoteIP := c.GetHeader("X-Original-Forwarded-For"); remoteIP != "" {
			// 某些代理可能使用此头
			ips := strings.Split(remoteIP, ",")
			ip = strings.TrimSpace(ips[0])
		} else {
			// 回退到ClientIP方法
			ip = c.ClientIP()
		}
		
		// 日志记录请求IP和头信息（调试用）
		fmt.Printf("请求IP: %s, X-Forwarded-For: %s, X-Real-IP: %s\n", 
			ip, 
			c.GetHeader("X-Forwarded-For"), 
			c.GetHeader("X-Real-IP"))
		
		// 获取限流器并检查是否允许访问
		ipLimiter, allowed := limiter.GetLimiter(ip)
		
		// 如果IP在黑名单中
		if !allowed {
			c.JSON(403, gin.H{
				"error": "您已被限制访问",
			})
			c.Abort()
			return
		}
		
		// 检查是否允许本次请求
		if !ipLimiter.Allow() {
			c.JSON(429, gin.H{
				"error": "请求频率过快，暂时限制访问",
			})
			c.Abort()
			return
		}
		
		// 允许请求继续处理
		c.Next()
	}
}

// ApplyRateLimit 应用限流到特定路由
func ApplyRateLimit(router *gin.Engine, path string, method string, handler gin.HandlerFunc) {
	// 创建限流器(如果未创建)
	limiter := NewIPRateLimiter()
	
	// 根据HTTP方法应用限流
	switch method {
	case "GET":
		router.GET(path, RateLimitMiddleware(limiter), handler)
	case "POST":
		router.POST(path, RateLimitMiddleware(limiter), handler)
	case "PUT":
		router.PUT(path, RateLimitMiddleware(limiter), handler)
	case "DELETE":
		router.DELETE(path, RateLimitMiddleware(limiter), handler)
	default:
		router.Any(path, RateLimitMiddleware(limiter), handler)
	}
}

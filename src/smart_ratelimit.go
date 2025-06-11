package main

import (
	"strings"
	"sync"
	"time"
)

// SmartRateLimit 智能限流会话管理
type SmartRateLimit struct {
	sessions sync.Map // IP -> *PullSession
}

// PullSession Docker拉取会话
type PullSession struct {
	LastManifestTime time.Time
	RequestCount     int
}

// 全局智能限流实例
var smartLimiter = &SmartRateLimit{}

// 硬编码的智能限流参数 - 无需配置管理
const (
	// manifest请求后的活跃窗口时间
	activeWindowDuration = 3 * time.Minute
	// 活跃窗口内最大免费blob请求数(防止滥用)
	maxFreeBlobRequests = 100
	// 会话清理间隔
	sessionCleanupInterval = 10 * time.Minute
	// 会话过期时间
	sessionExpireTime = 30 * time.Minute
)

func init() {
	// 启动会话清理协程
	go smartLimiter.cleanupSessions()
}

// ShouldSkipRateLimit 判断是否应该跳过限流计数
// 返回true表示跳过限流，false表示正常计入限流
func (s *SmartRateLimit) ShouldSkipRateLimit(ip, path string) bool {
	// 提取请求类型
	requestType, _ := parseRequestInfo(path)
	
	// 只对manifest和blob请求做智能处理
	if requestType != "manifests" && requestType != "blobs" {
		return false // 其他请求正常计入限流
	}
	
	// 获取或创建会话
	sessionKey := ip
	sessionInterface, _ := s.sessions.LoadOrStore(sessionKey, &PullSession{})
	session := sessionInterface.(*PullSession)
	
	now := time.Now()
	
	if requestType == "manifests" {
		// manifest请求：始终计入限流，但更新会话状态
		session.LastManifestTime = now
		session.RequestCount = 0 // 重置计数
		return false // manifest请求正常计入限流
	}
	
	// blob请求：检查是否在活跃窗口内
	if requestType == "blobs" {
		// 检查是否在活跃拉取窗口内
		if !session.LastManifestTime.IsZero() && 
		   now.Sub(session.LastManifestTime) <= activeWindowDuration {
			
			// 在活跃窗口内，检查是否超过最大免费请求数
			session.RequestCount++
			if session.RequestCount <= maxFreeBlobRequests {
				return true // 跳过限流计数
			}
		}
	}
	
	return false // 正常计入限流
}

// parseRequestInfo 解析请求路径，提取请求类型和镜像引用
func parseRequestInfo(path string) (requestType, imageRef string) {
	// 清理路径前缀
	path = strings.TrimPrefix(path, "/v2/")
	
	// 查找manifest或blob路径
	if idx := strings.Index(path, "/manifests/"); idx != -1 {
		return "manifests", path[:idx]
	}
	if idx := strings.Index(path, "/blobs/"); idx != -1 {
		return "blobs", path[:idx]
	}
	if idx := strings.Index(path, "/tags/"); idx != -1 {
		return "tags", path[:idx]
	}
	
	return "unknown", ""
}

// cleanupSessions 定期清理过期会话，防止内存泄露
func (s *SmartRateLimit) cleanupSessions() {
	ticker := time.NewTicker(sessionCleanupInterval)
	defer ticker.Stop()
	
	for range ticker.C {
		now := time.Now()
		expiredKeys := make([]string, 0)
		
		// 找出过期的会话
		s.sessions.Range(func(key, value interface{}) bool {
			session := value.(*PullSession)
			if !session.LastManifestTime.IsZero() && 
			   now.Sub(session.LastManifestTime) > sessionExpireTime {
				expiredKeys = append(expiredKeys, key.(string))
			}
			return true
		})
		
		// 删除过期会话
		for _, key := range expiredKeys {
			s.sessions.Delete(key)
		}
	}
} 
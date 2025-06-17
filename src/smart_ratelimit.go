package main

import (
	"strings"
	"sync"
	"time"
)

// SmartRateLimit 智能限流会话管理
type SmartRateLimit struct {
	sessions sync.Map
}

// PullSession Docker拉取会话
type PullSession struct {
	LastManifestTime time.Time
	RequestCount     int
}

// 全局智能限流实例
var smartLimiter = &SmartRateLimit{}

const (
	// manifest请求后的活跃窗口时间
	activeWindowDuration = 3 * time.Minute
	// 活跃窗口内最大免费blob请求数(防止滥用)
	maxFreeBlobRequests = 100
	sessionCleanupInterval = 10 * time.Minute
	sessionExpireTime = 30 * time.Minute
)

func init() {
	go smartLimiter.cleanupSessions()
}

// ShouldSkipRateLimit 判断是否应该跳过限流计数
func (s *SmartRateLimit) ShouldSkipRateLimit(ip, path string) bool {
	requestType, _ := parseRequestInfo(path)
	
	if requestType != "manifests" && requestType != "blobs" {
		return false
	}
	
	sessionKey := normalizeIPForRateLimit(ip)
	sessionInterface, _ := s.sessions.LoadOrStore(sessionKey, &PullSession{})
	session := sessionInterface.(*PullSession)
	
	now := time.Now()
	
	if requestType == "manifests" {
		session.LastManifestTime = now
		session.RequestCount = 0
		return false
	}
	
	if requestType == "blobs" {
		if !session.LastManifestTime.IsZero() && 
		   now.Sub(session.LastManifestTime) <= activeWindowDuration {
			
			session.RequestCount++
			if session.RequestCount <= maxFreeBlobRequests {
				return true
			}
		}
	}
	
	return false
}

func parseRequestInfo(path string) (requestType, imageRef string) {
	path = strings.TrimPrefix(path, "/v2/")
	
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
		
		s.sessions.Range(func(key, value interface{}) bool {
			session := value.(*PullSession)
			if !session.LastManifestTime.IsZero() && 
			   now.Sub(session.LastManifestTime) > sessionExpireTime {
				expiredKeys = append(expiredKeys, key.(string))
			}
			return true
		})
		
		for _, key := range expiredKeys {
			s.sessions.Delete(key)
		}
	}
} 
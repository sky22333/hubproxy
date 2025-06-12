package main

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// 性能优化常量
	SMART_BUFFER_SIZE = 32 * 1024      // 32KB 缓冲区
	CACHE_DURATION   = 20 * time.Minute // 20分钟缓存
	MAX_CACHE_ITEMS  = 500              // 最大缓存条目
	
	// 处理限制
	MAX_CONTENT_SIZE = 30 * 1024 * 1024 // 30MB文件大小限制
	PROCESS_TIMEOUT  = 5 * time.Second   // 5秒处理超时
)

// SmartProcessor 智能处理器 - 核心引擎
type SmartProcessor struct {
	// 编译优化
	githubRegex   *regexp.Regexp      // GitHub URL正则
	shellPatterns []string            // Shell特征模式
	
	// 高性能缓存
	cache         sync.Map            // 线程安全缓存
	bufferPool    sync.Pool           // 缓冲池
	
	// 原子统计
	totalRequests int64               // 总请求数
	cacheHits     int64               // 缓存命中
	processed     int64               // 实际处理
	
	// 控制开关
	enabled       int32               // 启用状态
}

// CacheItem 缓存项 - 内存对齐
type CacheItem struct {
	content   string                 // 处理后内容
	timestamp int64                  // 创建时间
	hits      int32                  // 访问次数
}

// 全局单例
var (
	smartProcessor *SmartProcessor
	smartOnce      sync.Once
	cleanupOnce    sync.Once
)

// GetSmartProcessor returns the singleton instance of SmartProcessor, initializing it with GitHub URL detection, script pattern recognition, buffer pooling, and background cache cleanup if not already created.
func GetSmartProcessor() *SmartProcessor {
	smartOnce.Do(func() {
		smartProcessor = &SmartProcessor{
			githubRegex: regexp.MustCompile(`https?://(?:github\.com|raw\.githubusercontent\.com|raw\.github\.com|gist\.githubusercontent\.com|gist\.github\.com|api\.github\.com)[^\s'"]+`),
			shellPatterns: []string{
				"#!/bin/bash", "#!/bin/sh", "#!/usr/bin/env bash",
				"curl ", "wget ", "git clone", "docker ", "export ",
			},
			enabled: 1, // 默认启用
		}
		
		// 初始化缓冲池
		smartProcessor.bufferPool.New = func() interface{} {
			return make([]byte, SMART_BUFFER_SIZE)
		}
		
		// 启动后台清理
		cleanupOnce.Do(func() {
			go smartProcessor.backgroundCleanup()
		})
	})
	return smartProcessor
}

// ProcessSmart reads content from the input, detects and rewrites GitHub URLs in script-like text using a proxy host, and returns the processed content as an io.Reader along with its length.
// If the processor is disabled, the input is returned unmodified. Large files and non-script content are bypassed. Results are cached for performance.
// Returns an error if reading the input fails.
func ProcessSmart(input io.ReadCloser, isCompressed bool, host string) (io.Reader, int64, error) {
	processor := GetSmartProcessor()
	
	// 快速检查是否启用
	if atomic.LoadInt32(&processor.enabled) == 0 {
		return input, 0, nil
	}
	
	atomic.AddInt64(&processor.totalRequests, 1)
	
	// 快速读取内容
	content, err := processor.readContent(input, isCompressed)
	if err != nil {
		// 优雅降级：读取错误时返回错误，让上层处理
		return nil, 0, fmt.Errorf("内容读取失败: %v", err)
	}
	if len(content) == 0 {
		// 空内容，返回空读取器
		return strings.NewReader(""), 0, nil
	}
	
	contentSize := int64(len(content))
	
	// 大文件检查 - 自动支持大文件
	if contentSize > MAX_CONTENT_SIZE {
		return strings.NewReader(content), contentSize, nil // 直接返回，不处理
	}
	
	// 智能内容分析
	if !processor.needsProcessing(content) {
		return strings.NewReader(content), contentSize, nil
	}
	
	// 缓存检查
	cacheKey := processor.getCacheKey(content, host)
	if cached := processor.getFromCache(cacheKey); cached != nil {
		atomic.AddInt64(&processor.cacheHits, 1)
		return strings.NewReader(cached.content), int64(len(cached.content)), nil
	}
	
	// 智能处理
	processed := processor.processContent(content, host)
	atomic.AddInt64(&processor.processed, 1)
	
	// 存入缓存
	processor.saveToCache(cacheKey, processed)
	
	return strings.NewReader(processed), int64(len(processed)), nil
}


// needsProcessing 智能判断是否需要处理
func (sp *SmartProcessor) needsProcessing(content string) bool {
	// 快速检查：是否包含GitHub URL
	if !strings.Contains(content, "github.com") && 
	   !strings.Contains(content, "githubusercontent.com") {
		return false
	}
	
	// 智能检查：是否为脚本类内容
	return sp.isScriptContent(content)
}

// isScriptContent 智能脚本内容识别
func (sp *SmartProcessor) isScriptContent(content string) bool {
	// 空内容检查
	if len(content) < 10 {
		return false
	}
	
	// 二进制文件快速排除
	if sp.isBinaryContent(content) {
		return false
	}
	
	// 检查前500字符的特征
	preview := content
	if len(content) > 500 {
		preview = content[:500]
	}
	
	// Shell特征检查
	for _, pattern := range sp.shellPatterns {
		if strings.Contains(preview, pattern) {
			return true
		}
	}
	
	// Dockerfile特征
	if strings.Contains(preview, "FROM ") || strings.Contains(preview, "RUN ") {
		return true
	}
	

	
	// 其他脚本特征
	scriptIndicators := []string{
		"#!/", "set -e", "function ", "echo ", "mkdir ", "chmod ",
		"apt-get", "yum ", "brew ", "sudo ", "source ", ". /",
	}
	
	for _, indicator := range scriptIndicators {
		if strings.Contains(preview, indicator) {
			return true
		}
	}
	
	return false
}

// isBinaryContent 二进制内容检测
func (sp *SmartProcessor) isBinaryContent(content string) bool {
	if len(content) < 100 {
		return false
	}
	
	// 检查前100字节中的空字节
	for i := 0; i < 100 && i < len(content); i++ {
		if content[i] == 0 {
			return true
		}
	}
	
	return false
}

// readContent 高效内容读取
func (sp *SmartProcessor) readContent(input io.ReadCloser, isCompressed bool) (string, error) {
	defer input.Close()
	
	// 使用缓冲池
	buffer := sp.bufferPool.Get().([]byte)
	defer sp.bufferPool.Put(buffer)
	
	var result strings.Builder
	result.Grow(SMART_BUFFER_SIZE) // 预分配
	
	var reader io.Reader = input
	var gzReader *gzip.Reader
	
	// 智能压缩处理 - 防止双重解压
	if isCompressed {
		// 先读取一小部分数据来检测是否真的是gzip格式
		peek := make([]byte, 2)
		n, err := input.Read(peek)
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("读取数据失败: %v", err)
		}
		
		// 检查gzip魔数 (0x1f, 0x8b)
		if n >= 2 && peek[0] == 0x1f && peek[1] == 0x8b {
			// 确认是gzip格式，创建MultiReader组合peek数据和剩余数据
			combinedReader := io.MultiReader(bytes.NewReader(peek[:n]), input)
			gzReader, err = gzip.NewReader(combinedReader)
			if err != nil {
				return "", fmt.Errorf("gzip解压失败: %v", err)
			}
			defer gzReader.Close()
			reader = gzReader
		} else {
			// 不是gzip格式或者HTTP客户端已经解压，使用原始数据
			combinedReader := io.MultiReader(bytes.NewReader(peek[:n]), input)
			reader = combinedReader
		}
	}
	
	// 读取内容
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			result.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("读取内容失败: %v", err)
		}
		
		// 大文件保护
		if result.Len() > MAX_CONTENT_SIZE {
			break
		}
	}
	
	return result.String(), nil
}

// processContent 智能内容处理
func (sp *SmartProcessor) processContent(content, host string) string {
	// 预分配结果缓冲区
	var result bytes.Buffer
	result.Grow(len(content) + len(content)/5) // 预留20%空间
	
	// 高效替换
	matches := sp.githubRegex.FindAllStringIndex(content, -1)
	if len(matches) == 0 {
		return content // 无需处理
	}
	
	lastIndex := 0
	for _, match := range matches {
		// 写入URL前的内容
		result.WriteString(content[lastIndex:match[0]])
		
		// 处理URL
		originalURL := content[match[0]:match[1]]
		newURL := sp.transformURL(originalURL, host)
		result.WriteString(newURL)
		
		lastIndex = match[1]
	}
	
	// 写入剩余内容
	result.WriteString(content[lastIndex:])
	
	return result.String()
}

// transformURL 智能URL转换
func (sp *SmartProcessor) transformURL(url, host string) string {
	// 避免重复处理
	if strings.Contains(url, host) {
		return url
	}
	
	// 协议标准化
	if !strings.HasPrefix(url, "https://") {
		if strings.HasPrefix(url, "http://") {
			url = "https://" + url[7:]
		} else if !strings.HasPrefix(url, "//") {
			url = "https://" + url
		}
	}
	
	// Host清理
	cleanHost := strings.TrimPrefix(host, "https://")
	cleanHost = strings.TrimPrefix(cleanHost, "http://")
	cleanHost = strings.TrimSuffix(cleanHost, "/")
	
	// 构建新URL
	return cleanHost + "/" + url
}

// getCacheKey 生成缓存键
func (sp *SmartProcessor) getCacheKey(content, host string) string {
	// 使用MD5快速哈希 - 安全的字符串转换
	hasher := md5.New()
	hasher.Write([]byte(content))
	hasher.Write([]byte(host))
	return fmt.Sprintf("%x", hasher.Sum(nil))[:16]
}

// getFromCache 从缓存获取
func (sp *SmartProcessor) getFromCache(key string) *CacheItem {
	if value, ok := sp.cache.Load(key); ok {
		item := value.(*CacheItem)
		
		// TTL检查
		if time.Now().UnixNano()-item.timestamp > int64(CACHE_DURATION) {
			sp.cache.Delete(key)
			return nil
		}
		
		atomic.AddInt32(&item.hits, 1)
		return item
	}
	return nil
}

// saveToCache 保存到缓存
func (sp *SmartProcessor) saveToCache(key, content string) {
	item := &CacheItem{
		content:   content,
		timestamp: time.Now().UnixNano(),
		hits:      0,
	}
	sp.cache.Store(key, item)
}


func (sp *SmartProcessor) backgroundCleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	
	for range ticker.C {
		sp.cleanExpiredCache()
		sp.logStats()
	}
}

// cleanExpiredCache 清理过期缓存
func (sp *SmartProcessor) cleanExpiredCache() {
	now := time.Now().UnixNano()
	count := 0
	
	sp.cache.Range(func(key, value interface{}) bool {
		count++
		item := value.(*CacheItem)
		
		// 清理过期项
		if now-item.timestamp > int64(CACHE_DURATION) {
			sp.cache.Delete(key)
			return true
		}
		
		// 限制缓存大小（LRU）
		if count > MAX_CACHE_ITEMS && item.hits < 2 {
			sp.cache.Delete(key)
		}
		
		return true
	})
}

// 调试日志开关
var smartDebugLog int32 = 1 // 1=开启, 0=关闭

// logStats 统计日志
func (sp *SmartProcessor) logStats() {
	if atomic.LoadInt32(&smartDebugLog) == 0 {
		return
	}
	
	total := atomic.LoadInt64(&sp.totalRequests)
	hits := atomic.LoadInt64(&sp.cacheHits)
	processed := atomic.LoadInt64(&sp.processed)
	
	hitRate := float64(0)
	if total > 0 {
		hitRate = float64(hits) / float64(total) * 100
	}
	
	fmt.Printf("智能处理器统计: 总请求=%d, 缓存命中=%d(%.1f%%), 实际处理=%d\n",
		total, hits, hitRate, processed)
}

// Enable 启用处理器
func (sp *SmartProcessor) Enable() {
	atomic.StoreInt32(&sp.enabled, 1)
}

// Disable 禁用处理器  
func (sp *SmartProcessor) Disable() {
	atomic.StoreInt32(&sp.enabled, 0)
}

// IsEnabled 检查状态
func (sp *SmartProcessor) IsEnabled() bool {
	return atomic.LoadInt32(&sp.enabled) == 1
}

// ClearCache 清空缓存
func (sp *SmartProcessor) ClearCache() {
	sp.cache.Range(func(key, value interface{}) bool {
		sp.cache.Delete(key)
		return true
	})
}

// GetStats 日志
func (sp *SmartProcessor) GetStats() (total, hits, processed int64) {
	return atomic.LoadInt64(&sp.totalRequests),
		   atomic.LoadInt64(&sp.cacheHits),
		   atomic.LoadInt64(&sp.processed)
}
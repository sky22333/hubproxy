package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// DockerProxy Docker代理配置
type DockerProxy struct {
	registry name.Registry
	options  []remote.Option
}

var dockerProxy *DockerProxy

// RegistryDetector Registry检测器
type RegistryDetector struct{}

// detectRegistryDomain 检测Registry域名并返回域名和剩余路径
func (rd *RegistryDetector) detectRegistryDomain(path string) (string, string) {
	cfg := GetConfig()
	
	// 检查路径是否以已知Registry域名开头
	for domain := range cfg.Registries {
		if strings.HasPrefix(path, domain+"/") {
			// 找到匹配的域名，返回域名和剩余路径
			remainingPath := strings.TrimPrefix(path, domain+"/")
			return domain, remainingPath
		}
	}
	
	return "", path
}

// isRegistryEnabled 检查Registry是否启用
func (rd *RegistryDetector) isRegistryEnabled(domain string) bool {
	cfg := GetConfig()
	if mapping, exists := cfg.Registries[domain]; exists {
		return mapping.Enabled
	}
	return false
}

// getRegistryMapping 获取Registry映射配置
func (rd *RegistryDetector) getRegistryMapping(domain string) (RegistryMapping, bool) {
	cfg := GetConfig()
	mapping, exists := cfg.Registries[domain]
	return mapping, exists && mapping.Enabled
}

var registryDetector = &RegistryDetector{}

// 初始化Docker代理
func initDockerProxy() {
	// 创建目标registry
	registry, err := name.NewRegistry("registry-1.docker.io")
	if err != nil {
		fmt.Printf("创建Docker registry失败: %v\n", err)
		return
	}

	// 配置代理选项
	options := []remote.Option{
		remote.WithAuth(authn.Anonymous),
		remote.WithUserAgent("hubproxy/go-containerregistry"),
		remote.WithTransport(GetGlobalHTTPClient().Transport),
	}

	dockerProxy = &DockerProxy{
		registry: registry,
		options:  options,
	}
}

// ProxyDockerRegistryGin 标准Docker Registry API v2代理
func ProxyDockerRegistryGin(c *gin.Context) {
	path := c.Request.URL.Path

	// 处理 /v2/ API版本检查
	if path == "/v2/" {
		c.JSON(http.StatusOK, gin.H{})
		return
	}

	// 处理不同的API端点
	if strings.HasPrefix(path, "/v2/") {
		handleRegistryRequest(c, path)
	} else {
		c.String(http.StatusNotFound, "Docker Registry API v2 only")
	}
}

// handleRegistryRequest 处理Registry请求
func handleRegistryRequest(c *gin.Context, path string) {
	// 移除 /v2/ 前缀
	pathWithoutV2 := strings.TrimPrefix(path, "/v2/")
	
	if registryDomain, remainingPath := registryDetector.detectRegistryDomain(pathWithoutV2); registryDomain != "" {
		if registryDetector.isRegistryEnabled(registryDomain) {
			// 设置目标Registry信息到Context
			c.Set("target_registry_domain", registryDomain)
			c.Set("target_path", remainingPath)
			
			// 处理多Registry请求
			handleMultiRegistryRequest(c, registryDomain, remainingPath)
			return
		}
	}
	
	imageName, apiType, reference := parseRegistryPath(pathWithoutV2)
	if imageName == "" || apiType == "" {
		c.String(http.StatusBadRequest, "Invalid path format")
		return
	}

	// 自动处理官方镜像的library命名空间
	if !strings.Contains(imageName, "/") {
		imageName = "library/" + imageName
	}

	// Docker镜像访问控制检查
	if allowed, reason := GlobalAccessController.CheckDockerAccess(imageName); !allowed {
		fmt.Printf("Docker镜像 %s 访问被拒绝: %s\n", imageName, reason)
		c.String(http.StatusForbidden, "镜像访问被限制")
		return
	}

	// 构建完整的镜像引用
	imageRef := fmt.Sprintf("%s/%s", dockerProxy.registry.Name(), imageName)

	switch apiType {
	case "manifests":
		handleManifestRequest(c, imageRef, reference)
	case "blobs":
		handleBlobRequest(c, imageRef, reference)
	case "tags":
		handleTagsRequest(c, imageRef)
	default:
		c.String(http.StatusNotFound, "API endpoint not found")
	}
}

// parseRegistryPath 解析Registry路径
func parseRegistryPath(path string) (imageName, apiType, reference string) {
	// 查找API端点关键字
	if idx := strings.Index(path, "/manifests/"); idx != -1 {
		imageName = path[:idx]
		apiType = "manifests"
		reference = path[idx+len("/manifests/"):]
		return
	}
	
	if idx := strings.Index(path, "/blobs/"); idx != -1 {
		imageName = path[:idx]
		apiType = "blobs"
		reference = path[idx+len("/blobs/"):]
		return
	}
	
	if idx := strings.Index(path, "/tags/list"); idx != -1 {
		imageName = path[:idx]
		apiType = "tags"
		reference = "list"
		return
	}

	return "", "", ""
}

// handleManifestRequest 处理manifest请求
func handleManifestRequest(c *gin.Context, imageRef, reference string) {
	// Manifest缓存逻辑(仅对GET请求缓存)
	if isCacheEnabled() && c.Request.Method == http.MethodGet {
		cacheKey := buildManifestCacheKey(imageRef, reference)
		
		// 优先从缓存获取
		if cachedItem := globalCache.Get(cacheKey); cachedItem != nil {
			writeCachedResponse(c, cachedItem)
			return
		}
	}
	
	var ref name.Reference
	var err error

	// 判断reference是digest还是tag
	if strings.HasPrefix(reference, "sha256:") {
		// 是digest
		ref, err = name.NewDigest(fmt.Sprintf("%s@%s", imageRef, reference))
	} else {
		// 是tag
		ref, err = name.NewTag(fmt.Sprintf("%s:%s", imageRef, reference))
	}

	if err != nil {
		fmt.Printf("解析镜像引用失败: %v\n", err)
		c.String(http.StatusBadRequest, "Invalid reference")
		return
	}

	// 根据请求方法选择操作
	if c.Request.Method == http.MethodHead {
		// HEAD请求，使用remote.Head
		desc, err := remote.Head(ref, dockerProxy.options...)
		if err != nil {
			fmt.Printf("HEAD请求失败: %v\n", err)
			c.String(http.StatusNotFound, "Manifest not found")
			return
		}

		// 设置响应头
		c.Header("Content-Type", string(desc.MediaType))
		c.Header("Docker-Content-Digest", desc.Digest.String())
		c.Header("Content-Length", fmt.Sprintf("%d", desc.Size))
		c.Status(http.StatusOK)
	} else {
		// GET请求，使用remote.Get
		desc, err := remote.Get(ref, dockerProxy.options...)
		if err != nil {
			fmt.Printf("GET请求失败: %v\n", err)
			c.String(http.StatusNotFound, "Manifest not found")
			return
		}

		// 设置响应头
		headers := map[string]string{
			"Docker-Content-Digest": desc.Digest.String(),
			"Content-Length":        fmt.Sprintf("%d", len(desc.Manifest)),
		}
		
		// 缓存响应
		if isCacheEnabled() {
			cacheKey := buildManifestCacheKey(imageRef, reference)
			ttl := getManifestTTL(reference)
			globalCache.Set(cacheKey, desc.Manifest, string(desc.MediaType), headers, ttl)
		}

		// 设置响应头
		c.Header("Content-Type", string(desc.MediaType))
		for key, value := range headers {
			c.Header(key, value)
		}

		// 返回manifest内容
		c.Data(http.StatusOK, string(desc.MediaType), desc.Manifest)
	}
}

// handleBlobRequest 处理blob请求
func handleBlobRequest(c *gin.Context, imageRef, digest string) {
	// 构建digest引用
	digestRef, err := name.NewDigest(fmt.Sprintf("%s@%s", imageRef, digest))
	if err != nil {
		fmt.Printf("解析digest引用失败: %v\n", err)
		c.String(http.StatusBadRequest, "Invalid digest reference")
		return
	}

	// 使用remote.Layer获取layer
	layer, err := remote.Layer(digestRef, dockerProxy.options...)
	if err != nil {
		fmt.Printf("获取layer失败: %v\n", err)
		c.String(http.StatusNotFound, "Layer not found")
		return
	}

	// 获取layer信息
	size, err := layer.Size()
	if err != nil {
		fmt.Printf("获取layer大小失败: %v\n", err)
		c.String(http.StatusInternalServerError, "Failed to get layer size")
		return
	}

	// 获取layer内容
	reader, err := layer.Compressed()
	if err != nil {
		fmt.Printf("获取layer内容失败: %v\n", err)
		c.String(http.StatusInternalServerError, "Failed to get layer content")
		return
	}
	defer reader.Close()

	// 设置响应头
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", fmt.Sprintf("%d", size))
	c.Header("Docker-Content-Digest", digest)

	// 流式传输blob内容
	c.Status(http.StatusOK)
	io.Copy(c.Writer, reader)
}

// handleTagsRequest 处理tags列表请求
func handleTagsRequest(c *gin.Context, imageRef string) {
	// 解析repository
	repo, err := name.NewRepository(imageRef)
	if err != nil {
		fmt.Printf("解析repository失败: %v\n", err)
		c.String(http.StatusBadRequest, "Invalid repository")
		return
	}

	// 使用remote.List获取tags
	tags, err := remote.List(repo, dockerProxy.options...)
	if err != nil {
		fmt.Printf("获取tags失败: %v\n", err)
		c.String(http.StatusNotFound, "Tags not found")
		return
	}

	// 构建响应
	response := map[string]interface{}{
		"name": strings.TrimPrefix(imageRef, dockerProxy.registry.Name()+"/"),
		"tags": tags,
	}

	c.JSON(http.StatusOK, response)
}

// ProxyDockerAuthGin Docker认证代理（带缓存优化）
func ProxyDockerAuthGin(c *gin.Context) {
	// 检查是否启用token缓存
	if isTokenCacheEnabled() {
		proxyDockerAuthWithCache(c)
	} else {
		proxyDockerAuthOriginal(c)
	}
}

// proxyDockerAuthWithCache 带缓存的认证代理
func proxyDockerAuthWithCache(c *gin.Context) {
	// 1. 构建缓存key（基于完整的查询参数）
	cacheKey := buildTokenCacheKey(c.Request.URL.RawQuery)
	
	// 2. 尝试从缓存获取token
	if cachedToken := globalCache.GetToken(cacheKey); cachedToken != "" {
		writeTokenResponse(c, cachedToken)
		return
	}
	
	// 3. 缓存未命中，创建响应记录器
	recorder := &ResponseRecorder{
		ResponseWriter: c.Writer,
		statusCode:     200,
	}
	c.Writer = recorder
	
	// 4. 调用原有认证逻辑
	proxyDockerAuthOriginal(c)
	
	// 5. 如果认证成功，缓存响应
	if recorder.statusCode == 200 && len(recorder.body) > 0 {
		ttl := extractTTLFromResponse(recorder.body)
		globalCache.SetToken(cacheKey, string(recorder.body), ttl)
	}
	
	// 6. 写入实际响应
	c.Writer = recorder.ResponseWriter
	c.Data(recorder.statusCode, "application/json", recorder.body)
}

// ResponseRecorder HTTP响应记录器
type ResponseRecorder struct {
	gin.ResponseWriter
	statusCode int
	body       []byte
}

func (r *ResponseRecorder) WriteHeader(code int) {
	r.statusCode = code
}

func (r *ResponseRecorder) Write(data []byte) (int, error) {
	r.body = append(r.body, data...)
	return len(data), nil
}

func proxyDockerAuthOriginal(c *gin.Context) {
	var authURL string
	if targetDomain, exists := c.Get("target_registry_domain"); exists {
		if mapping, found := registryDetector.getRegistryMapping(targetDomain.(string)); found {
			// 使用Registry特定的认证服务器
			authURL = "https://" + mapping.AuthHost + c.Request.URL.Path
		} else {
			// fallback到默认Docker认证
			authURL = "https://auth.docker.io" + c.Request.URL.Path
		}
	} else {
		// 构建默认Docker认证URL
		authURL = "https://auth.docker.io" + c.Request.URL.Path
	}
	
	if c.Request.URL.RawQuery != "" {
		authURL += "?" + c.Request.URL.RawQuery
	}

	// 创建HTTP客户端，复用全局传输配置（包含代理设置）
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: GetGlobalHTTPClient().Transport,
	}

	// 创建请求
	req, err := http.NewRequestWithContext(
		context.Background(),
		c.Request.Method,
		authURL,
		c.Request.Body,
	)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to create request")
		return
	}

	// 复制请求头
	for key, values := range c.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// 执行请求
	resp, err := client.Do(req)
	if err != nil {
		c.String(http.StatusBadGateway, "Auth request failed")
		return
	}
	defer resp.Body.Close()

	// 获取当前代理的Host地址
	proxyHost := c.Request.Host
	if proxyHost == "" {
		// 使用配置中的服务器地址和端口
		cfg := GetConfig()
		proxyHost = fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
		if cfg.Server.Host == "0.0.0.0" {
			proxyHost = fmt.Sprintf("localhost:%d", cfg.Server.Port)
		}
	}

	// 复制响应头并重写认证URL
	for key, values := range resp.Header {
		for _, value := range values {
			// 重写WWW-Authenticate头中的realm URL
			if key == "Www-Authenticate" {
				// 支持多Registry的URL重写
				value = rewriteAuthHeader(value, proxyHost)
			}
			c.Header(key, value)
		}
	}

	// 返回响应
	c.Status(resp.StatusCode)
	io.Copy(c.Writer, resp.Body)
}

// rewriteAuthHeader 重写认证头
func rewriteAuthHeader(authHeader, proxyHost string) string {
	// 重写各种Registry的认证URL
	authHeader = strings.ReplaceAll(authHeader, "https://auth.docker.io", "http://"+proxyHost)
	authHeader = strings.ReplaceAll(authHeader, "https://ghcr.io", "http://"+proxyHost)
	authHeader = strings.ReplaceAll(authHeader, "https://gcr.io", "http://"+proxyHost)
	authHeader = strings.ReplaceAll(authHeader, "https://quay.io", "http://"+proxyHost)
	
	return authHeader
}

// handleMultiRegistryRequest 处理多Registry请求
func handleMultiRegistryRequest(c *gin.Context, registryDomain, remainingPath string) {
	// 获取Registry映射配置
	mapping, exists := registryDetector.getRegistryMapping(registryDomain)
	if !exists {
		c.String(http.StatusBadRequest, "Registry not configured")
		return
	}
	
	// 解析剩余路径
	imageName, apiType, reference := parseRegistryPath(remainingPath)
	if imageName == "" || apiType == "" {
		c.String(http.StatusBadRequest, "Invalid path format")
		return
	}

	// 访问控制检查（使用完整的镜像路径）
	fullImageName := registryDomain + "/" + imageName
	if allowed, reason := GlobalAccessController.CheckDockerAccess(fullImageName); !allowed {
		fmt.Printf("镜像 %s 访问被拒绝: %s\n", fullImageName, reason)
		c.String(http.StatusForbidden, "镜像访问被限制")
		return
	}

	// 构建上游Registry引用
	upstreamImageRef := fmt.Sprintf("%s/%s", mapping.Upstream, imageName)

	// 根据API类型处理请求
	switch apiType {
	case "manifests":
		handleUpstreamManifestRequest(c, upstreamImageRef, reference, mapping)
	case "blobs":
		handleUpstreamBlobRequest(c, upstreamImageRef, reference, mapping)
	case "tags":
		handleUpstreamTagsRequest(c, upstreamImageRef, mapping)
	default:
		c.String(http.StatusNotFound, "API endpoint not found")
	}
}

// handleUpstreamManifestRequest 处理上游Registry的manifest请求
func handleUpstreamManifestRequest(c *gin.Context, imageRef, reference string, mapping RegistryMapping) {
	// Manifest缓存逻辑(仅对GET请求缓存)
	if isCacheEnabled() && c.Request.Method == http.MethodGet {
		cacheKey := buildManifestCacheKey(imageRef, reference)
		
		// 优先从缓存获取
		if cachedItem := globalCache.Get(cacheKey); cachedItem != nil {
			writeCachedResponse(c, cachedItem)
			return
		}
	}
	
	var ref name.Reference
	var err error

	// 判断reference是digest还是tag
	if strings.HasPrefix(reference, "sha256:") {
		ref, err = name.NewDigest(fmt.Sprintf("%s@%s", imageRef, reference))
	} else {
		ref, err = name.NewTag(fmt.Sprintf("%s:%s", imageRef, reference))
	}

	if err != nil {
		fmt.Printf("解析镜像引用失败: %v\n", err)
		c.String(http.StatusBadRequest, "Invalid reference")
		return
	}

	// 创建针对上游Registry的选项
	options := createUpstreamOptions(mapping)

	// 根据请求方法选择操作
	if c.Request.Method == http.MethodHead {
		desc, err := remote.Head(ref, options...)
		if err != nil {
			fmt.Printf("HEAD请求失败: %v\n", err)
			c.String(http.StatusNotFound, "Manifest not found")
			return
		}

		c.Header("Content-Type", string(desc.MediaType))
		c.Header("Docker-Content-Digest", desc.Digest.String())
		c.Header("Content-Length", fmt.Sprintf("%d", desc.Size))
		c.Status(http.StatusOK)
	} else {
		desc, err := remote.Get(ref, options...)
		if err != nil {
			fmt.Printf("GET请求失败: %v\n", err)
			c.String(http.StatusNotFound, "Manifest not found")
			return
		}

		// 设置响应头
		headers := map[string]string{
			"Docker-Content-Digest": desc.Digest.String(),
			"Content-Length":        fmt.Sprintf("%d", len(desc.Manifest)),
		}
		
		// 缓存响应
		if isCacheEnabled() {
			cacheKey := buildManifestCacheKey(imageRef, reference)
			ttl := getManifestTTL(reference)
			globalCache.Set(cacheKey, desc.Manifest, string(desc.MediaType), headers, ttl)
		}

		// 设置响应头
		c.Header("Content-Type", string(desc.MediaType))
		for key, value := range headers {
			c.Header(key, value)
		}
		
		c.Data(http.StatusOK, string(desc.MediaType), desc.Manifest)
	}
}

// handleUpstreamBlobRequest 处理上游Registry的blob请求
func handleUpstreamBlobRequest(c *gin.Context, imageRef, digest string, mapping RegistryMapping) {
	digestRef, err := name.NewDigest(fmt.Sprintf("%s@%s", imageRef, digest))
	if err != nil {
		fmt.Printf("解析digest引用失败: %v\n", err)
		c.String(http.StatusBadRequest, "Invalid digest reference")
		return
	}

	options := createUpstreamOptions(mapping)
	layer, err := remote.Layer(digestRef, options...)
	if err != nil {
		fmt.Printf("获取layer失败: %v\n", err)
		c.String(http.StatusNotFound, "Layer not found")
		return
	}

	size, err := layer.Size()
	if err != nil {
		fmt.Printf("获取layer大小失败: %v\n", err)
		c.String(http.StatusInternalServerError, "Failed to get layer size")
		return
	}

	reader, err := layer.Compressed()
	if err != nil {
		fmt.Printf("获取layer内容失败: %v\n", err)
		c.String(http.StatusInternalServerError, "Failed to get layer content")
		return
	}
	defer reader.Close()

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", fmt.Sprintf("%d", size))
	c.Header("Docker-Content-Digest", digest)

	c.Status(http.StatusOK)
	io.Copy(c.Writer, reader)
}

// handleUpstreamTagsRequest 处理上游Registry的tags请求
func handleUpstreamTagsRequest(c *gin.Context, imageRef string, mapping RegistryMapping) {
	repo, err := name.NewRepository(imageRef)
	if err != nil {
		fmt.Printf("解析repository失败: %v\n", err)
		c.String(http.StatusBadRequest, "Invalid repository")
		return
	}

	options := createUpstreamOptions(mapping)
	tags, err := remote.List(repo, options...)
	if err != nil {
		fmt.Printf("获取tags失败: %v\n", err)
		c.String(http.StatusNotFound, "Tags not found")
		return
	}

	response := map[string]interface{}{
		"name": strings.TrimPrefix(imageRef, mapping.Upstream+"/"),
		"tags": tags,
	}

	c.JSON(http.StatusOK, response)
}

// createUpstreamOptions 创建上游Registry选项
func createUpstreamOptions(mapping RegistryMapping) []remote.Option {
	options := []remote.Option{
		remote.WithAuth(authn.Anonymous),
		remote.WithUserAgent("hubproxy/go-containerregistry"),
		remote.WithTransport(GetGlobalHTTPClient().Transport),
	}

	// 根据Registry类型添加特定的认证选项（方便后续扩展）
	switch mapping.AuthType {
	case "github":
	case "google":
	case "quay":
	}

	return options
}

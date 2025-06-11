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
		remote.WithUserAgent("ghproxy/go-containerregistry"),
	}

	dockerProxy = &DockerProxy{
		registry: registry,
		options:  options,
	}

	fmt.Printf("Docker代理已初始化\n")
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
	
	// 解析路径
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
		c.Header("Content-Type", string(desc.MediaType))
		c.Header("Docker-Content-Digest", desc.Digest.String())
		c.Header("Content-Length", fmt.Sprintf("%d", len(desc.Manifest)))

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

// ProxyDockerAuthGin Docker认证代理
func ProxyDockerAuthGin(c *gin.Context) {
	// 构建认证URL
	authURL := "https://auth.docker.io" + c.Request.URL.Path
	if c.Request.URL.RawQuery != "" {
		authURL += "?" + c.Request.URL.RawQuery
	}

	// 创建HTTP客户端
	client := &http.Client{
		Timeout: 30 * time.Second,
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
			if key == "Www-Authenticate" && strings.Contains(value, "auth.docker.io") {
				value = strings.ReplaceAll(value, "https://auth.docker.io", "http://"+proxyHost)
			}
			c.Header(key, value)
		}
	}

	// 返回响应
	c.Status(resp.StatusCode)
	io.Copy(c.Writer, resp.Body)
}

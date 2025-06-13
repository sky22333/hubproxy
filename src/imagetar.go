package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// ImageStreamer 镜像流式下载器
type ImageStreamer struct {
	concurrency   int
	remoteOptions []remote.Option
}

// ImageStreamerConfig 下载器配置
type ImageStreamerConfig struct {
	Concurrency int
}

// NewImageStreamer 创建镜像下载器
func NewImageStreamer(config *ImageStreamerConfig) *ImageStreamer {
	if config == nil {
		config = &ImageStreamerConfig{}
	}

	concurrency := config.Concurrency
	if concurrency <= 0 {
		cfg := GetConfig()
		concurrency = cfg.Download.MaxImages
		if concurrency <= 0 {
			concurrency = 10
		}
	}

	remoteOptions := []remote.Option{
		remote.WithAuth(authn.Anonymous),
		remote.WithTransport(GetGlobalHTTPClient().Transport),
	}

	return &ImageStreamer{
		concurrency:   concurrency,
		remoteOptions: remoteOptions,
	}
}

// StreamOptions 下载选项
type StreamOptions struct {
	Platform    string
	Compression bool
}

// StreamImageToWriter 流式下载镜像到Writer
func (is *ImageStreamer) StreamImageToWriter(ctx context.Context, imageRef string, writer io.Writer, options *StreamOptions) error {
	if options == nil {
		options = &StreamOptions{}
	}

	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("解析镜像引用失败: %w", err)
	}

	log.Printf("开始下载镜像: %s", ref.String())

	contextOptions := append(is.remoteOptions, remote.WithContext(ctx))

	desc, err := is.getImageDescriptor(ref, contextOptions)
	if err != nil {
		return fmt.Errorf("获取镜像描述失败: %w", err)
	}
	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		return is.streamMultiArchImage(ctx, desc, writer, options, contextOptions)
	case types.OCIManifestSchema1, types.DockerManifestSchema2:
		return is.streamSingleImage(ctx, desc, writer, options, contextOptions)
	default:
		return is.streamSingleImage(ctx, desc, writer, options, contextOptions)
	}
}

// getImageDescriptor 获取镜像描述符
func (is *ImageStreamer) getImageDescriptor(ref name.Reference, options []remote.Option) (*remote.Descriptor, error) {
	if isCacheEnabled() {
		var reference string
		if tagged, ok := ref.(name.Tag); ok {
			reference = tagged.TagStr()
		} else if digested, ok := ref.(name.Digest); ok {
			reference = digested.DigestStr()
		}
		
		if reference != "" {
			cacheKey := buildManifestCacheKey(ref.Context().String(), reference)
					if cachedItem := globalCache.Get(cacheKey); cachedItem != nil {
			desc := &remote.Descriptor{
				Manifest: cachedItem.Data,
			}
			log.Printf("使用缓存的manifest: %s", ref.String())
			return desc, nil
		}
		}
	}

	desc, err := remote.Get(ref, options...)
	if err != nil {
		return nil, err
	}

	if isCacheEnabled() {
		var reference string
		if tagged, ok := ref.(name.Tag); ok {
			reference = tagged.TagStr()
		} else if digested, ok := ref.(name.Digest); ok {
			reference = digested.DigestStr()
		}
		
		if reference != "" {
			cacheKey := buildManifestCacheKey(ref.Context().String(), reference)
			ttl := getManifestTTL(reference)
			headers := map[string]string{
				"Docker-Content-Digest": desc.Digest.String(),
			}
			globalCache.Set(cacheKey, desc.Manifest, string(desc.MediaType), headers, ttl)
			log.Printf("缓存manifest: %s (TTL: %v)", ref.String(), ttl)
		}
	}

	return desc, nil
}

// StreamImageToGin 流式响应到Gin
func (is *ImageStreamer) StreamImageToGin(ctx context.Context, imageRef string, c *gin.Context, options *StreamOptions) error {
	if options == nil {
		options = &StreamOptions{}
	}

	filename := strings.ReplaceAll(imageRef, "/", "_") + ".docker"
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	
	if options.Compression {
		c.Header("Content-Encoding", "gzip")
	}

	return is.StreamImageToWriter(ctx, imageRef, c.Writer, options)
}

// streamMultiArchImage 处理多架构镜像
func (is *ImageStreamer) streamMultiArchImage(ctx context.Context, desc *remote.Descriptor, writer io.Writer, options *StreamOptions, remoteOptions []remote.Option) error {
	index, err := desc.ImageIndex()
	if err != nil {
		return fmt.Errorf("获取镜像索引失败: %w", err)
	}

	manifest, err := index.IndexManifest()
	if err != nil {
		return fmt.Errorf("获取索引清单失败: %w", err)
	}

	// 选择合适的平台
	var selectedDesc *v1.Descriptor
	for _, m := range manifest.Manifests {
		if m.Platform == nil {
			continue
		}
		
		if options.Platform != "" {
			platformParts := strings.Split(options.Platform, "/")
			if len(platformParts) == 2 && 
				m.Platform.OS == platformParts[0] && 
				m.Platform.Architecture == platformParts[1] {
				selectedDesc = &m
				break
			}
		} else if m.Platform.OS == "linux" && m.Platform.Architecture == "amd64" {
			selectedDesc = &m
			break
		}
	}

	if selectedDesc == nil && len(manifest.Manifests) > 0 {
		selectedDesc = &manifest.Manifests[0]
	}

	if selectedDesc == nil {
		return fmt.Errorf("未找到合适的平台镜像")
	}

	img, err := index.Image(selectedDesc.Digest)
	if err != nil {
		return fmt.Errorf("获取选中镜像失败: %w", err)
	}

	return is.streamImageLayers(ctx, img, writer, options)
}

// streamSingleImage 处理单架构镜像
func (is *ImageStreamer) streamSingleImage(ctx context.Context, desc *remote.Descriptor, writer io.Writer, options *StreamOptions, remoteOptions []remote.Option) error {
	img, err := desc.Image()
	if err != nil {
		return fmt.Errorf("获取镜像失败: %w", err)
	}

	return is.streamImageLayers(ctx, img, writer, options)
}

// streamImageLayers 处理镜像层
func (is *ImageStreamer) streamImageLayers(ctx context.Context, img v1.Image, writer io.Writer, options *StreamOptions) error {
	var finalWriter io.Writer = writer

	if options.Compression {
		gzWriter := gzip.NewWriter(writer)
		defer gzWriter.Close()
		finalWriter = gzWriter
	}

	tarWriter := tar.NewWriter(finalWriter)
	defer tarWriter.Close()

	configFile, err := img.ConfigFile()
	if err != nil {
		return fmt.Errorf("获取镜像配置失败: %w", err)
	}

	layers, err := img.Layers()
	if err != nil {
		return fmt.Errorf("获取镜像层失败: %w", err)
	}

	log.Printf("镜像包含 %d 层", len(layers))

	return is.streamDockerFormat(ctx, tarWriter, img, layers, configFile)
}

// streamDockerFormat 生成Docker格式
func (is *ImageStreamer) streamDockerFormat(ctx context.Context, tarWriter *tar.Writer, img v1.Image, layers []v1.Layer, configFile *v1.ConfigFile) error {
	configDigest, err := img.ConfigName()
	if err != nil {
		return err
	}
	
	configData, err := json.Marshal(configFile)
	if err != nil {
		return err
	}
	
	configHeader := &tar.Header{
		Name: configDigest.String() + ".json",
		Size: int64(len(configData)),
		Mode: 0644,
	}
	
	if err := tarWriter.WriteHeader(configHeader); err != nil {
		return err
	}
	if _, err := tarWriter.Write(configData); err != nil {
		return err
	}

	layerDigests := make([]string, len(layers))
	for i, layer := range layers {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := func() error { // ✅ 匿名函数确保资源立即释放
			digest, err := layer.Digest()
			if err != nil {
				return err
			}
			layerDigests[i] = digest.String()

			layerDir := digest.String()
			layerHeader := &tar.Header{
				Name:     layerDir + "/",
				Typeflag: tar.TypeDir,
				Mode:     0755,
			}
			
			if err := tarWriter.WriteHeader(layerHeader); err != nil {
				return err
			}

			layerReader, err := layer.Uncompressed()
			if err != nil {
				return err
			}
			defer layerReader.Close() // ✅ 函数结束立即释放

			size, err := layer.Size()
			if err != nil {
				return err
			}

			layerTarHeader := &tar.Header{
				Name: layerDir + "/layer.tar",
				Size: size,
				Mode: 0644,
			}
			
			if err := tarWriter.WriteHeader(layerTarHeader); err != nil {
				return err
			}

			if _, err := io.Copy(tarWriter, layerReader); err != nil {
				return err
			}

			return nil
		}(); err != nil {
			return err
		}

		log.Printf("已处理层 %d/%d", i+1, len(layers))
	}


	manifest := []map[string]interface{}{{
		"Config":   configDigest.String() + ".json",
		"RepoTags": []string{"imported:latest"},
		"Layers":   func() []string {
			var layers []string
			for _, digest := range layerDigests {
				layers = append(layers, digest+"/layer.tar")
			}
			return layers
		}(),
	}}
	
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	
	manifestHeader := &tar.Header{
		Name: "manifest.json",
		Size: int64(len(manifestData)),
		Mode: 0644,
	}
	
	if err := tarWriter.WriteHeader(manifestHeader); err != nil {
		return err
	}
	
	_, err = tarWriter.Write(manifestData)
	return err
}



var globalImageStreamer *ImageStreamer

// initImageStreamer 初始化镜像下载器
func initImageStreamer() {
	globalImageStreamer = NewImageStreamer(nil)
	log.Printf("镜像下载器初始化完成，并发数: %d，缓存: %v", 
		globalImageStreamer.concurrency, isCacheEnabled())
}

// formatPlatformText 格式化平台文本
func formatPlatformText(platform string) string {
	if platform == "" {
		return "自动选择"
	}
	return platform
}

// initImageTarRoutes 初始化镜像下载路由
func initImageTarRoutes(router *gin.Engine) {
	imageAPI := router.Group("/api/image")
	{
		imageAPI.GET("/download/:image", RateLimitMiddleware(globalLimiter), handleDirectImageDownload)
		imageAPI.GET("/info/:image", RateLimitMiddleware(globalLimiter), handleImageInfo)
		imageAPI.POST("/batch", RateLimitMiddleware(globalLimiter), handleSimpleBatchDownload)
	}
}

// handleDirectImageDownload 处理单镜像下载
func handleDirectImageDownload(c *gin.Context) {
	imageParam := c.Param("image")
	if imageParam == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少镜像参数"})
		return
	}

	imageRef := strings.ReplaceAll(imageParam, "_", "/")
	platform := c.Query("platform")
	tag := c.DefaultQuery("tag", "")

	if tag != "" && !strings.Contains(imageRef, ":") && !strings.Contains(imageRef, "@") {
		imageRef = imageRef + ":" + tag
	} else if !strings.Contains(imageRef, ":") && !strings.Contains(imageRef, "@") {
		imageRef = imageRef + ":latest"
	}

	if _, err := name.ParseReference(imageRef); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "镜像引用格式错误: " + err.Error()})
		return
	}

	options := &StreamOptions{
		Platform:    platform,
		Compression: false,
	}

	ctx := c.Request.Context()
	log.Printf("下载镜像: %s (平台: %s)", imageRef, formatPlatformText(platform))

	if err := globalImageStreamer.StreamImageToGin(ctx, imageRef, c, options); err != nil {
		log.Printf("镜像下载失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "镜像下载失败: " + err.Error()})
		return
	}
}

// handleSimpleBatchDownload 处理批量下载
func handleSimpleBatchDownload(c *gin.Context) {
	var req struct {
		Images   []string `json:"images" binding:"required"`
		Platform string   `json:"platform"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
		return
	}

	if len(req.Images) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "镜像列表不能为空"})
		return
	}

	cfg := GetConfig()
	if len(req.Images) > cfg.Download.MaxImages {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("镜像数量超过限制，最大允许: %d", cfg.Download.MaxImages),
		})
		return
	}

	options := &StreamOptions{
		Platform:    req.Platform,
		Compression: true,
	}

	ctx := c.Request.Context()
	log.Printf("批量下载 %d 个镜像 (平台: %s)", len(req.Images), formatPlatformText(req.Platform))

	filename := fmt.Sprintf("batch_%d_images.docker.gz", len(req.Images))
	
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Header("Content-Encoding", "gzip")

	if err := globalImageStreamer.StreamMultipleImages(ctx, req.Images, c.Writer, options); err != nil {
		log.Printf("批量镜像下载失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "批量镜像下载失败: " + err.Error()})
		return
	}
}

// handleImageInfo 处理镜像信息查询
func handleImageInfo(c *gin.Context) {
	imageParam := c.Param("image")
	if imageParam == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少镜像参数"})
		return
	}

	imageRef := strings.ReplaceAll(imageParam, "_", "/")
	tag := c.DefaultQuery("tag", "latest")

	if !strings.Contains(imageRef, ":") && !strings.Contains(imageRef, "@") {
		imageRef = imageRef + ":" + tag
	}

	ref, err := name.ParseReference(imageRef)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "镜像引用格式错误: " + err.Error()})
		return
	}

	ctx := c.Request.Context()
	contextOptions := append(globalImageStreamer.remoteOptions, remote.WithContext(ctx))

	desc, err := globalImageStreamer.getImageDescriptor(ref, contextOptions)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取镜像信息失败: " + err.Error()})
		return
	}

	info := gin.H{
		"name":      ref.String(),
		"mediaType": desc.MediaType,
		"digest":    desc.Digest.String(),
		"size":      desc.Size,
	}

	if desc.MediaType == types.OCIImageIndex || desc.MediaType == types.DockerManifestList {
		index, err := desc.ImageIndex()
		if err == nil {
			manifest, err := index.IndexManifest()
			if err == nil {
				var platforms []string
				for _, m := range manifest.Manifests {
					if m.Platform != nil {
						platforms = append(platforms, m.Platform.OS+"/"+m.Platform.Architecture)
					}
				}
				info["platforms"] = platforms
				info["multiArch"] = true
			}
		}
	} else {
		info["multiArch"] = false
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": info})
}

// StreamMultipleImages 批量下载多个镜像
func (is *ImageStreamer) StreamMultipleImages(ctx context.Context, imageRefs []string, writer io.Writer, options *StreamOptions) error {
	if options == nil {
		options = &StreamOptions{}
	}

	var finalWriter io.Writer = writer
	if options.Compression {
		gzWriter := gzip.NewWriter(writer)
		defer gzWriter.Close()
		finalWriter = gzWriter
	}

	tarWriter := tar.NewWriter(finalWriter)
	defer tarWriter.Close()

	for i, imageRef := range imageRefs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		log.Printf("处理镜像 %d/%d: %s", i+1, len(imageRefs), imageRef)
		
		dirName := fmt.Sprintf("image_%d_%s/", i, strings.ReplaceAll(imageRef, "/", "_"))
		
		dirHeader := &tar.Header{
			Name:     dirName,
			Typeflag: tar.TypeDir,
			Mode:     0755,
		}
		
		if err := tarWriter.WriteHeader(dirHeader); err != nil {
			return fmt.Errorf("创建镜像目录失败: %w", err)
		}

		if err := is.StreamImageToWriter(ctx, imageRef, tarWriter, &StreamOptions{
			Platform:    options.Platform,
			Compression: false,
		}); err != nil {
			log.Printf("下载镜像 %s 失败: %v", imageRef, err)
			continue
		}
	}

	return nil
} 
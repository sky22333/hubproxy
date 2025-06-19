package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// DebounceEntry 防抖条目
type DebounceEntry struct {
	LastRequest time.Time
	UserID      string
}

// DownloadDebouncer 下载防抖器
type DownloadDebouncer struct {
	mu          sync.RWMutex
	entries     map[string]*DebounceEntry
	window      time.Duration
	lastCleanup time.Time
}

// NewDownloadDebouncer 创建下载防抖器
func NewDownloadDebouncer(window time.Duration) *DownloadDebouncer {
	return &DownloadDebouncer{
		entries:     make(map[string]*DebounceEntry),
		window:      window,
		lastCleanup: time.Now(),
	}
}

// ShouldAllow 检查是否应该允许请求
func (d *DownloadDebouncer) ShouldAllow(userID, contentKey string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	key := userID + ":" + contentKey
	now := time.Now()

	if entry, exists := d.entries[key]; exists {
		if now.Sub(entry.LastRequest) < d.window {
			return false // 在防抖窗口内，拒绝请求
		}
	}

	// 更新或创建条目
	d.entries[key] = &DebounceEntry{
		LastRequest: now,
		UserID:      userID,
	}

	// 清理过期条目（每5分钟清理一次）
	if time.Since(d.lastCleanup) > 5*time.Minute {
		d.cleanup(now)
		d.lastCleanup = now
	}

	return true
}

// cleanup 清理过期条目
func (d *DownloadDebouncer) cleanup(now time.Time) {
	for key, entry := range d.entries {
		if now.Sub(entry.LastRequest) > d.window*2 {
			delete(d.entries, key)
		}
	}
}

// generateContentFingerprint 生成内容指纹
func generateContentFingerprint(images []string, platform string) string {
	// 对镜像列表排序确保顺序无关
	sortedImages := make([]string, len(images))
	copy(sortedImages, images)
	sort.Strings(sortedImages)

	// 组合内容：镜像列表 + 平台信息
	content := strings.Join(sortedImages, "|") + ":" + platform

	// 生成MD5哈希
	hash := md5.Sum([]byte(content))
	return hex.EncodeToString(hash[:])
}

// getUserID 获取用户标识
func getUserID(c *gin.Context) string {
	// 优先使用会话Cookie
	if sessionID, err := c.Cookie("session_id"); err == nil && sessionID != "" {
		return "session:" + sessionID
	}

	// 备用方案：IP + User-Agent组合
	ip := c.ClientIP()
	userAgent := c.GetHeader("User-Agent")
	if userAgent == "" {
		userAgent = "unknown"
	}

	// 生成简短标识
	combined := ip + ":" + userAgent
	hash := md5.Sum([]byte(combined))
	return "ip:" + hex.EncodeToString(hash[:8]) // 只取前8字节
}

// 全局防抖器实例
var (
	singleImageDebouncer *DownloadDebouncer
	batchImageDebouncer  *DownloadDebouncer
)

// initDebouncer 初始化防抖器
func initDebouncer() {
	// 单个镜像：5秒防抖窗口
	singleImageDebouncer = NewDownloadDebouncer(5 * time.Second)
	// 批量镜像：60秒防抖窗口
	batchImageDebouncer = NewDownloadDebouncer(60 * time.Second)
}

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
	Platform            string
	Compression         bool
	UseCompressedLayers bool // 是否保存原始压缩层，默认开启
}

// StreamImageToWriter 流式下载镜像到Writer
func (is *ImageStreamer) StreamImageToWriter(ctx context.Context, imageRef string, writer io.Writer, options *StreamOptions) error {
	if options == nil {
		options = &StreamOptions{UseCompressedLayers: true}
	}

	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("解析镜像引用失败: %w", err)
	}

	log.Printf("开始下载镜像: %s", ref.String())

	contextOptions := append(is.remoteOptions, remote.WithContext(ctx))

	desc, err := is.getImageDescriptorWithPlatform(ref, contextOptions, options.Platform)
	if err != nil {
		return fmt.Errorf("获取镜像描述失败: %w", err)
	}
	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		return is.streamMultiArchImage(ctx, desc, writer, options, contextOptions, imageRef)
	case types.OCIManifestSchema1, types.DockerManifestSchema2:
		return is.streamSingleImage(ctx, desc, writer, options, contextOptions, imageRef)
	default:
		return is.streamSingleImage(ctx, desc, writer, options, contextOptions, imageRef)
	}
}

// getImageDescriptor 获取镜像描述符
func (is *ImageStreamer) getImageDescriptor(ref name.Reference, options []remote.Option) (*remote.Descriptor, error) {
	return is.getImageDescriptorWithPlatform(ref, options, "")
}

// getImageDescriptorWithPlatform 获取指定平台的镜像描述符
func (is *ImageStreamer) getImageDescriptorWithPlatform(ref name.Reference, options []remote.Option, platform string) (*remote.Descriptor, error) {
	// 直接从网络获取完整的descriptor，确保对象完整性
	return remote.Get(ref, options...)
}

// StreamImageToGin 流式响应到Gin
func (is *ImageStreamer) StreamImageToGin(ctx context.Context, imageRef string, c *gin.Context, options *StreamOptions) error {
	if options == nil {
		options = &StreamOptions{UseCompressedLayers: true}
	}

	filename := strings.ReplaceAll(imageRef, "/", "_") + ".tar"
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))

	if options.Compression {
		c.Header("Content-Encoding", "gzip")
	}

	return is.StreamImageToWriter(ctx, imageRef, c.Writer, options)
}

// streamMultiArchImage 处理多架构镜像
func (is *ImageStreamer) streamMultiArchImage(ctx context.Context, desc *remote.Descriptor, writer io.Writer, options *StreamOptions, remoteOptions []remote.Option, imageRef string) error {
	img, err := is.selectPlatformImage(desc, options)
	if err != nil {
		return err
	}

	return is.streamImageLayers(ctx, img, writer, options, imageRef)
}

// streamSingleImage 处理单架构镜像
func (is *ImageStreamer) streamSingleImage(ctx context.Context, desc *remote.Descriptor, writer io.Writer, options *StreamOptions, remoteOptions []remote.Option, imageRef string) error {
	img, err := desc.Image()
	if err != nil {
		return fmt.Errorf("获取镜像失败: %w", err)
	}

	return is.streamImageLayers(ctx, img, writer, options, imageRef)
}

// streamImageLayers 处理镜像层
func (is *ImageStreamer) streamImageLayers(ctx context.Context, img v1.Image, writer io.Writer, options *StreamOptions, imageRef string) error {
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

	return is.streamDockerFormat(ctx, tarWriter, img, layers, configFile, imageRef, options)
}

// streamDockerFormat 生成Docker格式
func (is *ImageStreamer) streamDockerFormat(ctx context.Context, tarWriter *tar.Writer, img v1.Image, layers []v1.Layer, configFile *v1.ConfigFile, imageRef string, options *StreamOptions) error {
	return is.streamDockerFormatWithReturn(ctx, tarWriter, img, layers, configFile, imageRef, nil, nil, options)
}

// streamDockerFormatWithReturn 生成Docker格式并返回manifest和repositories信息
func (is *ImageStreamer) streamDockerFormatWithReturn(ctx context.Context, tarWriter *tar.Writer, img v1.Image, layers []v1.Layer, configFile *v1.ConfigFile, imageRef string, manifestOut *map[string]interface{}, repositoriesOut *map[string]map[string]string, options *StreamOptions) error {
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

		if err := func() error {
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

			var layerSize int64
			var layerReader io.ReadCloser

			// 根据配置选择使用压缩层或未压缩层
			if options != nil && options.UseCompressedLayers {
				layerSize, err = layer.Size()
				if err != nil {
					return err
				}
				layerReader, err = layer.Compressed()
			} else {
				layerSize, err = partial.UncompressedSize(layer)
				if err != nil {
					return err
				}
				layerReader, err = layer.Uncompressed()
			}

			if err != nil {
				return err
			}
			defer layerReader.Close()

			layerTarHeader := &tar.Header{
				Name: layerDir + "/layer.tar",
				Size: layerSize,
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

	// 构建单个镜像的manifest信息
	singleManifest := map[string]interface{}{
		"Config":   configDigest.String() + ".json",
		"RepoTags": []string{imageRef},
		"Layers": func() []string {
			var layers []string
			for _, digest := range layerDigests {
				layers = append(layers, digest+"/layer.tar")
			}
			return layers
		}(),
	}

	// 构建repositories信息
	repositories := make(map[string]map[string]string)
	parts := strings.Split(imageRef, ":")
	if len(parts) == 2 {
		repoName := parts[0]
		tag := parts[1]
		repositories[repoName] = map[string]string{tag: configDigest.String()}
	}

	// 如果是批量下载，返回信息而不写入文件
	if manifestOut != nil && repositoriesOut != nil {
		*manifestOut = singleManifest
		*repositoriesOut = repositories
		return nil
	}

	// 单镜像下载，直接写入manifest.json
	manifest := []map[string]interface{}{singleManifest}

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

	if _, err := tarWriter.Write(manifestData); err != nil {
		return err
	}

	// 写入repositories文件
	repositoriesData, err := json.Marshal(repositories)
	if err != nil {
		return err
	}

	repositoriesHeader := &tar.Header{
		Name: "repositories",
		Size: int64(len(repositoriesData)),
		Mode: 0644,
	}

	if err := tarWriter.WriteHeader(repositoriesHeader); err != nil {
		return err
	}

	_, err = tarWriter.Write(repositoriesData)
	return err
}

// processImageForBatch 处理镜像的公共逻辑，用于批量下载
func (is *ImageStreamer) processImageForBatch(ctx context.Context, img v1.Image, tarWriter *tar.Writer, imageRef string, options *StreamOptions) (map[string]interface{}, map[string]map[string]string, error) {
	layers, err := img.Layers()
	if err != nil {
		return nil, nil, fmt.Errorf("获取镜像层失败: %w", err)
	}

	configFile, err := img.ConfigFile()
	if err != nil {
		return nil, nil, fmt.Errorf("获取镜像配置失败: %w", err)
	}

	log.Printf("镜像包含 %d 层", len(layers))

	var manifest map[string]interface{}
	var repositories map[string]map[string]string

	err = is.streamDockerFormatWithReturn(ctx, tarWriter, img, layers, configFile, imageRef, &manifest, &repositories, options)
	if err != nil {
		return nil, nil, err
	}

	return manifest, repositories, nil
}

func (is *ImageStreamer) streamSingleImageForBatch(ctx context.Context, tarWriter *tar.Writer, imageRef string, options *StreamOptions) (map[string]interface{}, map[string]map[string]string, error) {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return nil, nil, fmt.Errorf("解析镜像引用失败: %w", err)
	}

	contextOptions := append(is.remoteOptions, remote.WithContext(ctx))

	desc, err := is.getImageDescriptorWithPlatform(ref, contextOptions, options.Platform)
	if err != nil {
		return nil, nil, fmt.Errorf("获取镜像描述失败: %w", err)
	}

	var img v1.Image

	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		// 处理多架构镜像
		img, err = is.selectPlatformImage(desc, options)
		if err != nil {
			return nil, nil, fmt.Errorf("选择平台镜像失败: %w", err)
		}
	case types.OCIManifestSchema1, types.DockerManifestSchema2:
		img, err = desc.Image()
		if err != nil {
			return nil, nil, fmt.Errorf("获取镜像失败: %w", err)
		}
	default:
		img, err = desc.Image()
		if err != nil {
			return nil, nil, fmt.Errorf("获取镜像失败: %w", err)
		}
	}

	return is.processImageForBatch(ctx, img, tarWriter, imageRef, options)
}

// selectPlatformImage 从多架构镜像中选择合适的平台镜像
func (is *ImageStreamer) selectPlatformImage(desc *remote.Descriptor, options *StreamOptions) (v1.Image, error) {
	index, err := desc.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("获取镜像索引失败: %w", err)
	}

	manifest, err := index.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("获取索引清单失败: %w", err)
	}

	// 选择合适的平台
	var selectedDesc *v1.Descriptor
	for _, m := range manifest.Manifests {
		if m.Platform == nil {
			continue
		}

		if options.Platform != "" {
			platformParts := strings.Split(options.Platform, "/")
			if len(platformParts) >= 2 {
				targetOS := platformParts[0]
				targetArch := platformParts[1]
				targetVariant := ""
				if len(platformParts) >= 3 {
					targetVariant = platformParts[2]
				}

				if m.Platform.OS == targetOS &&
					m.Platform.Architecture == targetArch &&
					m.Platform.Variant == targetVariant {
					selectedDesc = &m
					break
				}
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
		return nil, fmt.Errorf("未找到合适的平台镜像")
	}

	img, err := index.Image(selectedDesc.Digest)
	if err != nil {
		return nil, fmt.Errorf("获取选中镜像失败: %w", err)
	}

	return img, nil
}

var globalImageStreamer *ImageStreamer

// initImageStreamer 初始化镜像下载器
func initImageStreamer() {
	globalImageStreamer = NewImageStreamer(nil)
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
		imageAPI.GET("/download/:image", handleDirectImageDownload)
		imageAPI.GET("/info/:image", handleImageInfo)
		imageAPI.POST("/batch", handleSimpleBatchDownload)
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
	useCompressed := c.DefaultQuery("compressed", "true") == "true"

	if tag != "" && !strings.Contains(imageRef, ":") && !strings.Contains(imageRef, "@") {
		imageRef = imageRef + ":" + tag
	} else if !strings.Contains(imageRef, ":") && !strings.Contains(imageRef, "@") {
		imageRef = imageRef + ":latest"
	}

	if _, err := name.ParseReference(imageRef); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "镜像引用格式错误: " + err.Error()})
		return
	}

	// 防抖检查
	userID := getUserID(c)
	contentKey := generateContentFingerprint([]string{imageRef}, platform)

	if !singleImageDebouncer.ShouldAllow(userID, contentKey) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":       "请求过于频繁，请稍后再试",
			"retry_after": 5,
		})
		return
	}

	options := &StreamOptions{
		Platform:            platform,
		Compression:         false,
		UseCompressedLayers: useCompressed,
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
		Images              []string `json:"images" binding:"required"`
		Platform            string   `json:"platform"`
		UseCompressedLayers *bool    `json:"useCompressedLayers"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
		return
	}

	if len(req.Images) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "镜像列表不能为空"})
		return
	}

	for i, imageRef := range req.Images {
		if !strings.Contains(imageRef, ":") && !strings.Contains(imageRef, "@") {
			req.Images[i] = imageRef + ":latest"
		}
	}

	cfg := GetConfig()
	if len(req.Images) > cfg.Download.MaxImages {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("镜像数量超过限制，最大允许: %d", cfg.Download.MaxImages),
		})
		return
	}

	// 批量下载防抖检查
	userID := getUserID(c)
	contentKey := generateContentFingerprint(req.Images, req.Platform)

	if !batchImageDebouncer.ShouldAllow(userID, contentKey) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":       "批量下载请求过于频繁，请稍后再试",
			"retry_after": 60,
		})
		return
	}

	useCompressed := true // 默认启用原始压缩层
	if req.UseCompressedLayers != nil {
		useCompressed = *req.UseCompressedLayers
	}

	options := &StreamOptions{
		Platform:            req.Platform,
		Compression:         false,
		UseCompressedLayers: useCompressed,
	}

	ctx := c.Request.Context()
	log.Printf("批量下载 %d 个镜像 (平台: %s)", len(req.Images), formatPlatformText(req.Platform))

	filename := fmt.Sprintf("batch_%d_images.tar", len(req.Images))

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))

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
		options = &StreamOptions{UseCompressedLayers: true}
	}

	var finalWriter io.Writer = writer
	if options.Compression {
		gzWriter := gzip.NewWriter(writer)
		defer gzWriter.Close()
		finalWriter = gzWriter
	}

	tarWriter := tar.NewWriter(finalWriter)
	defer tarWriter.Close()

	var allManifests []map[string]interface{}
	var allRepositories = make(map[string]map[string]string)

	// 流式处理每个镜像
	for i, imageRef := range imageRefs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		log.Printf("处理镜像 %d/%d: %s", i+1, len(imageRefs), imageRef)

		// 防止单个镜像处理时间过长
		timeoutCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		manifest, repositories, err := is.streamSingleImageForBatch(timeoutCtx, tarWriter, imageRef, options)
		cancel()

		if err != nil {
			log.Printf("下载镜像 %s 失败: %v", imageRef, err)
			return fmt.Errorf("下载镜像 %s 失败: %w", imageRef, err)
		}

		if manifest == nil {
			return fmt.Errorf("镜像 %s manifest数据为空", imageRef)
		}

		// 收集manifest信息
		allManifests = append(allManifests, manifest)

		// 合并repositories信息
		for repo, tags := range repositories {
			if allRepositories[repo] == nil {
				allRepositories[repo] = make(map[string]string)
			}
			for tag, digest := range tags {
				allRepositories[repo][tag] = digest
			}
		}
	}

	// 写入合并的manifest.json
	manifestData, err := json.Marshal(allManifests)
	if err != nil {
		return fmt.Errorf("序列化manifest失败: %w", err)
	}

	manifestHeader := &tar.Header{
		Name: "manifest.json",
		Size: int64(len(manifestData)),
		Mode: 0644,
	}

	if err := tarWriter.WriteHeader(manifestHeader); err != nil {
		return fmt.Errorf("写入manifest header失败: %w", err)
	}

	if _, err := tarWriter.Write(manifestData); err != nil {
		return fmt.Errorf("写入manifest数据失败: %w", err)
	}

	// 写入合并的repositories文件
	repositoriesData, err := json.Marshal(allRepositories)
	if err != nil {
		return fmt.Errorf("序列化repositories失败: %w", err)
	}

	repositoriesHeader := &tar.Header{
		Name: "repositories",
		Size: int64(len(repositoriesData)),
		Mode: 0644,
	}

	if err := tarWriter.WriteHeader(repositoriesHeader); err != nil {
		return fmt.Errorf("写入repositories header失败: %w", err)
	}

	if _, err := tarWriter.Write(repositoriesData); err != nil {
		return fmt.Errorf("写入repositories数据失败: %w", err)
	}

	log.Printf("批量下载完成，共处理 %d 个镜像", len(imageRefs))
	return nil
}

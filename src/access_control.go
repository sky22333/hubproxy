package main

import (
	"strings"
	"sync"
)

// ResourceType 资源类型
type ResourceType string

const (
	ResourceTypeGitHub ResourceType = "github"
	ResourceTypeDocker ResourceType = "docker"
)

// AccessController 统一访问控制器
type AccessController struct {
	mu sync.RWMutex
}

// DockerImageInfo Docker镜像信息
type DockerImageInfo struct {
	Namespace  string
	Repository string
	Tag        string
	FullName   string
}

// 全局访问控制器实例
var GlobalAccessController = &AccessController{}

// ParseDockerImage 解析Docker镜像名称
func (ac *AccessController) ParseDockerImage(image string) DockerImageInfo {
	// 移除可能的协议前缀
	image = strings.TrimPrefix(image, "docker://")
	
	// 分离标签
	var tag string
	if idx := strings.LastIndex(image, ":"); idx != -1 {
		// 检查是否是端口号而不是标签（包含斜杠）
		part := image[idx+1:]
		if !strings.Contains(part, "/") {
			tag = part
			image = image[:idx]
		}
	}
	if tag == "" {
		tag = "latest"
	}
	
	// 分离命名空间和仓库名
	var namespace, repository string
	if strings.Contains(image, "/") {
		// 处理自定义registry的情况，如 registry.com/user/repo
		parts := strings.Split(image, "/")
		if len(parts) >= 2 {
			// 检查第一部分是否是域名（包含.）
			if strings.Contains(parts[0], ".") {
				// 跳过registry域名，取用户名和仓库名
				if len(parts) >= 3 {
					namespace = parts[1]
					repository = parts[2]
				} else {
					namespace = "library"
					repository = parts[1]
				}
			} else {
				// 标准格式：user/repo
				namespace = parts[0]
				repository = parts[1]
			}
		}
	} else {
		// 官方镜像，如 nginx
		namespace = "library"
		repository = image
	}
	
	fullName := namespace + "/" + repository
	
	return DockerImageInfo{
		Namespace:  namespace,
		Repository: repository,
		Tag:        tag,
		FullName:   fullName,
	}
}

// CheckDockerAccess 检查Docker镜像访问权限
func (ac *AccessController) CheckDockerAccess(image string) (allowed bool, reason string) {
	cfg := GetConfig()
	
	// 解析镜像名称
	imageInfo := ac.ParseDockerImage(image)
	
	// 检查白名单（如果配置了白名单，则只允许白名单中的镜像）
	if len(cfg.Proxy.WhiteList) > 0 {
		if !ac.matchImageInList(imageInfo, cfg.Proxy.WhiteList) {
			return false, "不在Docker镜像白名单内"
		}
	}
	
	// 检查黑名单
	if len(cfg.Proxy.BlackList) > 0 {
		if ac.matchImageInList(imageInfo, cfg.Proxy.BlackList) {
			return false, "Docker镜像在黑名单内"
		}
	}
	
	return true, ""
}

// CheckGitHubAccess 检查GitHub仓库访问权限
func (ac *AccessController) CheckGitHubAccess(matches []string) (allowed bool, reason string) {
	if len(matches) < 2 {
		return false, "无效的GitHub仓库格式"
	}
	
	cfg := GetConfig()
	
	// 检查白名单
	if len(cfg.Proxy.WhiteList) > 0 && !ac.checkList(matches, cfg.Proxy.WhiteList) {
		return false, "不在GitHub仓库白名单内"
	}
	
	// 检查黑名单
	if len(cfg.Proxy.BlackList) > 0 && ac.checkList(matches, cfg.Proxy.BlackList) {
		return false, "GitHub仓库在黑名单内"
	}
	
	return true, ""
}

// matchImageInList 检查Docker镜像是否在指定列表中
func (ac *AccessController) matchImageInList(imageInfo DockerImageInfo, list []string) bool {
	fullName := strings.ToLower(imageInfo.FullName)
	namespace := strings.ToLower(imageInfo.Namespace)
	
	for _, item := range list {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		
		if fullName == item {
			return true
		}
		
		if item == namespace || item == namespace+"/*" {
			return true
		}
		
		if strings.HasSuffix(item, "*") {
			prefix := strings.TrimSuffix(item, "*")
			if strings.HasPrefix(fullName, prefix) {
				return true
			}
		}
		
		if strings.HasPrefix(item, "*/") {
			repoPattern := strings.TrimPrefix(item, "*/")
			if strings.HasSuffix(repoPattern, "*") {
				repoPrefix := strings.TrimSuffix(repoPattern, "*")
				if strings.HasPrefix(imageInfo.Repository, repoPrefix) {
					return true
				}
			} else {
				if strings.ToLower(imageInfo.Repository) == repoPattern {
					return true
				}
			}
		}
		
		// 5. 子仓库匹配（防止 user/repo 匹配到 user/repo-fork）
		if strings.HasPrefix(fullName, item+"/") {
			return true
		}
	}
	return false
}

// checkList GitHub仓库检查逻辑
func (ac *AccessController) checkList(matches, list []string) bool {
	if len(matches) < 2 {
		return false
	}
	
	// 组合用户名和仓库名，处理.git后缀
	username := strings.ToLower(strings.TrimSpace(matches[0]))
	repoName := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(matches[1], ".git")))
	fullRepo := username + "/" + repoName
	
	for _, item := range list {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		
		// 支持多种匹配模式：
		// 1. 精确匹配: "vaxilu/x-ui"
		// 2. 用户级匹配: "vaxilu/*" 或 "vaxilu"
		// 3. 前缀匹配: "vaxilu/x-ui-*"
		if fullRepo == item {
			return true
		}
		
		// 用户级匹配
		if item == username || item == username+"/*" {
			return true
		}
		
		// 前缀匹配（支持通配符）
		if strings.HasSuffix(item, "*") {
			prefix := strings.TrimSuffix(item, "*")
			if strings.HasPrefix(fullRepo, prefix) {
				return true
			}
		}
		
		// 子仓库匹配（防止 user/repo 匹配到 user/repo-fork）
		if strings.HasPrefix(fullRepo, item+"/") {
			return true
		}
	}
	return false
} 
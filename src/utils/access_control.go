package utils

import (
	"strings"
	"sync"

	"hubproxy/config"
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

// GlobalAccessController 全局访问控制器实例
var GlobalAccessController = &AccessController{}

// ParseDockerImage 解析Docker镜像名称
func (ac *AccessController) ParseDockerImage(image string) DockerImageInfo {
	image = strings.TrimPrefix(image, "docker://")

	var tag string
	if idx := strings.LastIndex(image, ":"); idx != -1 {
		part := image[idx+1:]
		if !strings.Contains(part, "/") {
			tag = part
			image = image[:idx]
		}
	}
	if tag == "" {
		tag = "latest"
	}

	var namespace, repository string
	if strings.Contains(image, "/") {
		parts := strings.Split(image, "/")
		if len(parts) >= 2 {
			if strings.Contains(parts[0], ".") {
				if len(parts) >= 3 {
					namespace = parts[1]
					repository = parts[2]
				} else {
					namespace = "library"
					repository = parts[1]
				}
			} else {
				namespace = parts[0]
				repository = parts[1]
			}
		}
	} else {
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
	cfg := config.GetConfig()

	imageInfo := ac.ParseDockerImage(image)

	if len(cfg.Access.WhiteList) > 0 {
		if !ac.matchImageInList(imageInfo, cfg.Access.WhiteList) {
			return false, "不在Docker镜像白名单内"
		}
	}

	if len(cfg.Access.BlackList) > 0 {
		if ac.matchImageInList(imageInfo, cfg.Access.BlackList) {
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

	cfg := config.GetConfig()

	if len(cfg.Access.WhiteList) > 0 && !ac.checkList(matches, cfg.Access.WhiteList) {
		return false, "不在GitHub仓库白名单内"
	}

	if len(cfg.Access.BlackList) > 0 && ac.checkList(matches, cfg.Access.BlackList) {
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

	username := strings.ToLower(strings.TrimSpace(matches[0]))
	repoName := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(matches[1], ".git")))
	fullRepo := username + "/" + repoName

	for _, item := range list {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}

		if fullRepo == item {
			return true
		}

		if item == username || item == username+"/*" {
			return true
		}

		if strings.HasSuffix(item, "*") {
			prefix := strings.TrimSuffix(item, "*")
			if strings.HasPrefix(fullRepo, prefix) {
				return true
			}
		}

		if strings.HasPrefix(fullRepo, item+"/") {
			return true
		}

		if strings.HasPrefix(item, "*/") {
			p := item[2:]
			if p == repoName || (strings.HasSuffix(p, "*") && strings.HasPrefix(repoName, p[:len(p)-1])) {
				return true
			}
		}
	}
	return false
}

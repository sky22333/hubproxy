# HubProxy

🚀 **Docker 和 GitHub 加速代理服务器**

一个轻量级、高性能的多功能代理服务，提供 Docker 镜像加速、GitHub 文件加速、下载离线镜像、在线搜索 Docker 镜像等功能。

## ✨ 特性

- 🐳 **Docker 镜像加速** - 单域名实现 Docker Hub、GHCR、Quay 等多个镜像仓库加速，流式传输优化拉取速度。
- 🐳 **离线镜像包** - 支持下载离线镜像包，流式传输加防抖设计。
- 📁 **GitHub 文件加速** - 加速 GitHub Release、Raw 文件下载，支持`api.github.com`，脚本嵌套加速等等
- 🤖 **AI 模型库支持** - 支持 Hugging Face 模型下载加速
- 🛡️ **智能限流** - IP 限流保护，防止滥用
- 🚫 **仓库审计** - 强大的自定义黑名单，白名单，同时审计镜像仓库，和GitHub仓库
- 🔍 **镜像搜索** - 在线搜索 Docker 镜像
- ⚡ **轻量高效** - 基于 Go 语言，单二进制文件运行，资源占用低，优雅的内存清理机制。
- 🔧 **统一配置** - 统一配置管理


## 🚀 快速开始

### Docker部署（推荐）
```
docker run -d \
  --name hubproxy \
  -p 5000:5000 \
  --restart always \
  ghcr.io/sky22333/hubproxy
```



### 一键脚本安装

```bash
curl -fsSL https://raw.githubusercontent.com/sky22333/hubproxy/main/install-service.sh | sudo bash
```

也可以直接下载二进制文件执行`./hubproxy`使用，无需配置文件即可启动，内置默认配置，支持所有功能。初始内存占用约18M，二进制文件大小约12M

这个命令会：
- 🔍 自动检测系统架构（AMD64/ARM64）
- 📥 从 GitHub Releases 下载最新版本
- ⚙️ 自动配置系统服务
- 🔄 保留现有配置（升级时）



## 📖 使用方法

### Docker 镜像加速

```bash
# 原命令
docker pull nginx

# 使用加速
docker pull yourdomain.com/nginx

# ghcr加速
docker pull yourdomain.com/ghcr.io/sky22333/hubproxy

# 符合Docker Registry API v2标准的仓库都支持
```

### GitHub 文件加速

```bash
# 原链接
https://github.com/user/repo/releases/download/v1.0.0/file.tar.gz

# 加速链接
https://yourdomain.com/https://github.com/user/repo/releases/download/v1.0.0/file.tar.gz
```



## ⚙️ 配置

容器内的配置文件位于 `/root/config.toml`

脚本部署配置文件位于 `/opt/hubproxy/config.toml`

为了IP限流能够正常运行，反向代理需要传递IP头用来获取访客真实IP，以caddy为例：
```
example.com {
    reverse_proxy 127.0.0.1:5000 {
        header_up X-Forwarded-For {http.request.header.CF-Connecting-IP}
        header_up X-Real-IP {http.request.header.CF-Connecting-IP}
        header_up X-Forwarded-Proto https
        header_up X-Forwarded-Host {host}
    }
}
```

### `config.toml`配置示例

不配置也可以，程序内置有默认值
```
[server]
# 监听地址，默认监听所有接口
host = "0.0.0.0"
# 监听端口
port = 5000
# Github文件大小限制（字节），默认2GB
fileSize = 2147483648

[rateLimit]
# 每个IP每小时允许的请求数(注意Docker镜像每个层为一个请求)
requestLimit = 200
# 限流周期（小时）
periodHours = 1.0

[security]
# IP白名单，支持单个IP或IP段
# 白名单中的IP不受限流限制
whiteList = [
    "127.0.0.1",
    "192.168.1.0/24"
]

# IP黑名单，支持单个IP或IP段
# 黑名单中的IP将被直接拒绝访问
blackList = [
    "192.168.100.1"
]

[proxy]
# 代理服务白名单（支持GitHub仓库和Docker镜像，支持通配符）
# 只允许访问白名单中的仓库/镜像，为空时不限制
whiteList = []

# 代理服务黑名单（支持GitHub仓库和Docker镜像，支持通配符）
# 禁止访问黑名单中的仓库/镜像
blackList = [
    "baduser/malicious-repo",
    "*/malicious-repo",
    "baduser/*"
] 

[download]
# 批量下载离线镜像数量限制
maxImages = 10

# Registry映射配置，支持多种镜像仓库上游
[registries]

# GitHub Container Registry
[registries."ghcr.io"]
upstream = "ghcr.io"
authHost = "ghcr.io/token" 
authType = "github"
enabled = true

# Google Container Registry
[registries."gcr.io"]
upstream = "gcr.io"
authHost = "gcr.io/v2/token"
authType = "google"
enabled = true

# Quay.io Container Registry
[registries."quay.io"]
upstream = "quay.io"
authHost = "quay.io/v2/auth"
authType = "quay"
enabled = true

# Kubernetes Container Registry
[registries."registry.k8s.io"]
upstream = "registry.k8s.io"
authHost = "registry.k8s.io"
authType = "anonymous"
enabled = true

[tokenCache]
# 是否启用缓存(同时控制Token和Manifest缓存)显著提升性能
enabled = true
# 默认缓存时间(分钟)
defaultTTL = "20m"
```



## ⚠️ 免责声明

- 本程序仅供学习交流使用，请勿用于非法用途
- 使用本程序需遵守当地法律法规
- 作者不对使用者的任何行为承担责任

---

<div align="center">

**⭐ 如果这个项目对你有帮助，请给个 Star！⭐**

</div>

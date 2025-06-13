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
- 🔧 **配置热重载** - 统一配置管理，部分配置项支持热重载，无需重启服务

## 🚀 快速开始

### Docker部署（推荐）
```
docker run -d \
  --name hubproxy \
  -p 5000:5000 \
  --restart always \
  ghcr.io/sky22333/hubproxy
```



### 一键安装

```bash
curl -fsSL https://raw.githubusercontent.com/sky22333/hubproxy/main/install-service.sh | sudo bash
```

可直接下载二进制文件执行`./hubproxy`使用，无需配置文件即可启动，内置默认配置，支持所有功能。初始内存占用约18M，二进制文件大小约12M

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
docker pull demo.52013120.xyz/nginx

# ghcr加速
docker pull demo.52013120.xyz/ghcr.io/sky22333/hubproxy

# 符合Docker Registry API v2标准的仓库都支持
```

### GitHub 文件加速

```bash
# 原链接
https://github.com/user/repo/releases/download/v1.0.0/file.tar.gz

# 加速链接
https://yourdomain.com/https://github.com/user/repo/releases/download/v1.0.0/file.tar.gz
```



## ⚙️ 提示

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


## ⚠️ 免责声明

- 本程序仅供学习交流使用，请勿用于非法用途
- 使用本程序需遵守当地法律法规
- 作者不对使用者的任何行为承担责任

---

<div align="center">

**⭐ 如果这个项目对你有帮助，请给个 Star！⭐**

</div>

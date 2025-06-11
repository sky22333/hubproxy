# HubProxy

🚀 **Docker 和 GitHub 加速代理服务器**

一个轻量级、高性能的多功能代理服务，提供 Docker 镜像加速、GitHub 文件加速等功能。

## ✨ 特性

- 🐳 **Docker 镜像加速** - 支持 Docker Hub、GHCR、Quay 等多个镜像仓库，以及优化拉取速度。支持批量下载离线镜像包。
- 📁 **GitHub 文件加速** - 加速 GitHub Release、Raw 文件下载，脚本嵌套加速，以及api.github。com
- 🤖 **AI 模型库支持** - 支持 Hugging Face 模型下载加速
- 🛡️ **智能限流** - IP 限流保护，防止滥用
- 🔍 **镜像搜索** - 在线搜索 Docker 镜像
- ⚡ **轻量高效** - 基于 Go 语言，单二进制文件运行，资源占用低
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

# 使用加速（替换 yourdomain.com）
docker pull yourdomain.com/nginx

# ghcr加速（替换 yourdomain.com）
docker pull yourdomain.com/ghcr.io/user/images
```

### GitHub 文件加速

```bash
# 原链接
https://github.com/user/repo/releases/download/v1.0.0/file.tar.gz

# 加速链接
https://yourdomain.com/https://github.com/user/repo/releases/download/v1.0.0/file.tar.gz
```



## ⚙️ 配置

主配置文件位于 `/opt/hubproxy/config.toml`：




## 🙏 致谢


- UI 界面参考了[相关开源项目](https://github.com/WJQSERVER-STUDIO/GHProxy-Frontend)

## ⚠️ 免责声明

- 本程序仅供学习交流使用，请勿用于非法用途
- 使用本程序需遵守当地法律法规
- 作者不对使用者的任何行为承担责任

---

<div align="center">

**⭐ 如果这个项目对你有帮助，请给个 Star！⭐**

</div>

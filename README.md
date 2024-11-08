### Docker-proxy

- 一键部署多个仓库的`docker`加速源
- 优化繁琐的搭建部署
- 部署超级简单



1：请先将`k8sgcr`，`ghcr`，`gcr`，`dockerhub`，`registryk8s`这个几个配置为你的二级域名。

2：然后修改`docker-compose.yml`配置里的环境变量，修改为你的主域名，然后`docker compose up -d`启动即可。

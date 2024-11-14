### Docker-proxy介绍

- 使用`docker`一键部署多种仓库的docker加速
- 优化繁琐的搭建部署
- 部署超级简单
- 自动配置HTTPS
- 拉取的镜像在服务器缓存3天后自动清理（可自行修改）

---

### 部署教程

1：域名解析：将`hub`，`quay`，`ghcr`，`gcr`，`docker`，`registryk8s`这个几个解析为你的二级域名。

> 嫌麻烦也可以直接泛解析


2：拉取本项目
```
git clone https://github.com/sky22333/docker-proxy.git
```


3：其他无需修改，只需修改`docker-compose.yml`配置里的域名环境变量，修改为你的`根域名`
然后启动即可。

> 这里的`根域名`只是为了动态写入配置和前端，你不用解析，当然也不会影响你的根域名使用。
```
docker compose up -d
```

4：部署完成后访问`hub.example.com`查看前端

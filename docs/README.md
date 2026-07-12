# HubProxy 文档站

基于 [Astro Starlight](https://starlight.astro.build/)，默认中文，英文位于 `/en/`。

## 本地开发

```bash
cd docs
npm install
npm run dev
```

访问 http://localhost:4321/hubproxy/（GitHub Pages 子路径与线上一致）。

## 构建

```bash
npm run build    # 输出到 dist/
npm run preview  # 本地预览构建结果
```

## 目录

```
src/content/docs/     # 中文文档
src/content/docs/en/  # 英文文档
src/assets/           # logo、hero 等资源
public/favicon.svg    # 浏览器标签页图标
src/styles/custom.css # 自定义样式
astro.config.mjs      # Starlight 配置（含 base: /hubproxy/）
```

## 贡献

1. 修改 `src/content/docs/` 中文页面，并同步 `en/` 镜像
2. 侧边栏结构见 `astro.config.mjs`
3. 向主仓库提交 PR

部署由 `.github/workflows/docs.yml` 自动发布至 GitHub Pages。

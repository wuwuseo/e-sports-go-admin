# 文档导航

本目录存放 API 规范与专题文档。项目概述、快速开始、配置说明请先阅读[根目录 README](../README.md)。

## API 规范

- [swagger.json](swagger.json) / [swagger.yaml](swagger.yaml) — 由代码注解生成的 OpenAPI 2.0 规范，`docs.go` 为同一规范的 Go 内嵌形式。三者均由 [Swag](https://github.com/swaggo/swag) 生成，**不要手工编辑**：

  ```bash
  swag init -g main.go -o docs
  ```

- Swagger UI 默认关闭（安全基线）。在 `config/config.yml` 中开启 `app.swagger: true`（或 `app.debug: true`）后，访问 `http://localhost:9501/docs` 查看，运行时读取本目录的 `swagger.json`。

## 专题文档

- [oauth-github.md](oauth-github.md) — GitHub OAuth 接入指南：主密钥配置、OAuth App 创建、Provider 配置接口与绑定流程。
- [plugin-protocols.md](plugin-protocols.md) — 插件协议说明：插件包结构、进程外插件运行方式与 gRPC/HTTP/stdio 传输。
- [source-apps.md](source-apps.md) — 源码功能集开发说明：功能集的注册、校验、路由挂载与代码生成流程。
- [cms.md](cms.md) — CMS 文章、单页、分类的公开 API 与管理接口。
- [task-queue-backends.md](task-queue-backends.md) — 任务队列后端能力：各 driver 的 broker 连接、概览指标与管理 API 差异。

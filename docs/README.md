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
- [缺陷修复方案-权限与数据范围.md](缺陷修复方案-权限与数据范围.md) — 权限与数据范围缺陷的根因分析与修复验收记录（2026-08-18 验收通过），作为历史归档保留。

## 历史设计与计划

- [superpowers/plans/](superpowers/plans/) 与 [superpowers/specs/](superpowers/specs/) — 历次重构的设计与实施计划，仅作历史参考，不反映当前代码的完整设计。

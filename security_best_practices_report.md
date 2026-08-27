# Go-Admin 安全审计报告

**审计日期**: 2026-07-20  
**审计范围**: d:\www\admin 完整代码库  
**审计标准**: Go 语言安全最佳实践规范 (golang-general-backend-security.md)

---

## 执行摘要

经过系统性的代码审计，**未发现中等或更高严重度的已确认安全漏洞**。

本项目在以下关键安全领域表现良好：
- ✅ 密码存储使用 bcrypt 自适应哈希
- ✅ 随机数生成使用 crypto/rand
- ✅ SQL 查询使用 Ent ORM 参数化，无注入风险
- ✅ SSRF 防护措施完善（IP 黑名单、协议限制、DNS 重绑定防护）
- ✅ 外部 HTTP 请求包含超时和大小限制
- ✅ 配置管理遵循安全实践（密钥通过环境变量设置）
- ✅ 登录接口有速率限制防护
- ✅ 无调试端点暴露风险

---

## 详细审计结果

### 1. 认证与访问控制 ✅

**审计范围**: 登录流程、JWT 处理、会话管理、权限校验

**正面发现**:
- **密码存储**: 使用 bcrypt 进行密码哈希（[admin_service.go:354](file:///d:/www/admin/internal/service/admin_service.go#L354)），符合 GO-AUTH-001 要求
- **JWT 密钥管理**: 配置文件明确要求通过环境变量设置密钥，启动时自动生成随机密钥（[config.yml:8-12](file:///d:/www/admin/config/config.yml#L8)）
- **登录防护**: 实现了速率限制（每分钟 10 次，[router.go:124-127](file:///d:/www/admin/internal/router.go#L124)）
- **令牌吊销**: 登出时记录时间戳，配合黑名单服务实现令牌失效（[admin_service.go:379-384](file:///d:/www/admin/internal/service/admin_service.go#L379)）
- **权限中间件**: 使用 Casbin 进行 RBAC 权限校验，包含域隔离（[permission.go:46-76](file:///d:/www/admin/internal/middleware/permission.go#L46)）

**无风险发现**

---

### 2. 注入向量 ✅

**审计范围**: SQL 查询、模板渲染、文件路径操作、命令执行

**正面发现**:
- **SQL 注入防护**: 全部使用 Ent ORM 进行类型安全的数据库操作，无原始 SQL 拼接
  - 示例: `client.Admin.Query().Where(admin.UID(uid)).Only(ctx)` ([admin_service.go:149-151](file:///d:/www/admin/internal/service/admin_service.go#L149))
- **命令注入防护**: 定时任务执行器使用白名单注册表 + `exec.CommandContext` 直接执行，不经过 shell
  - 实现: [executor.go:49-54](file:///d:/www/admin/internal/cron/executor.go#L49)
- **模板安全**: 无使用 `text/template` 渲染 HTML 的代码，无 SSTI 风险
- **文件路径**: 附件上传使用 CMF 框架的 `filesystem.SafeOriginalName` 和内容寻址键，路径由系统控制

**无风险发现**

---

### 3. 外部交互（SSRF 防护）✅

**审计范围**: 出站 HTTP 请求、第三方 API 调用

**正面发现**:
- **SSRF 防护完善**: 定时任务 HTTP 执行器包含多层防护
  - IP 黑名单: 阻止内网 IP、环回地址、云元数据服务 (169.254.169.254, 100.100.100.200)
  - 协议限制: 仅允许 http/https
  - 端口白名单: 仅允许 80/443
  - DNS 重绑定防护: 连接层二次 IP 校验
  - 重定向校验: 限制 5 次，每次验证目标 URL
  - 实现: [executor.go:69-166](file:///d:/www/admin/internal/cron/executor.go#L69)

- **GitHub OAuth 客户端**: 
  - 设置 10 秒超时 ([github.go:36](file:///d:/www/admin/internal/oauth/github.go#L36))
  - 响应体大小限制为 1MB ([github.go:161-166](file:///d:/www/admin/internal/oauth/github.go#L161))
  - 使用 `io.LimitReader` 防止无界读取

**无风险发现**

---

### 4. 敏感数据处理 ✅

**审计范围**: 密钥、凭证、日志、加密实践

**正面发现**:
- **密钥管理**: 配置文件中 JWT 密钥为空，明确注释要求生产环境通过环境变量设置
  - `CMF_APP_SECRET` 和 `CMF_APP_ADMIN_SECRET` ([config.yml:8-12](file:///d:/www/admin/config/config.yml#L8))
  - OAuth 主密钥通过 `OAUTH_CONFIG_MASTER_KEY` 环境变量提供 ([oauth-github.md:7](file:///d:/www/admin/docs/oauth-github.md#L7))
- **随机数生成**: 所有安全敏感场景使用 `crypto/rand`
  - JWT 密钥生成: [init.go:5](file:///d:/www/admin/internal/init.go#L5)
  - OAuth 加密: [cipher.go:6](file:///d:/www/admin/internal/oauth/cipher.go#L6)
- **日志安全**: 未发现密码、令牌等敏感信息泄露到日志

**无风险发现**

---

### 5. HTTP 配置与安全头 ⚠️

**审计范围**: 超时设置、请求体限制、安全响应头

**配置检查**:
- **请求体限制**: 配置文件设置 `body_limit: 10485760` (10MB) ([config.yml:15](file:///d:/www/admin/config/config.yml#L15))
- **空闲超时**: 设置 `idle_timeout: 60` 秒 ([config.yml:5](file:///d:/www/admin/config/config.yml#L5))

**观察项（非漏洞）**:
- HTTP 超时设置依赖 CMF 框架默认配置，应用层未显式设置 `ReadTimeout`/`WriteTimeout`/`ReadHeaderTimeout`，但框架可能有合理默认值
- 缺少安全响应头（CSP, X-Frame-Options, X-Content-Type-Options）
  - **说明**: 本项目为 API 后端服务（主要用于 SPA），这些响应头通常在反向代理/CDN 层设置，代码层面未发现不构成漏洞

**建议（低优先级）**:
- 如果部署在无代理环境，考虑在应用层添加安全响应头中间件
- 验证 CMF 框架的 HTTP 超时默认值是否符合生产需求

---

### 6. 其他检查项 ✅

- **调试端点**: 未发现 pprof/expvar 公开暴露
- **环境文件**: 未发现 `.env` 文件泄露
- **硬编码密钥**: 未发现代码中硬编码的密钥或凭证
- **并发安全**: 使用缓存服务（带互斥保护）和数据库连接池

---

## 审计方法

本次审计遵循 Go 语言安全最佳实践规范，按以下顺序系统性检查：

1. **入口点分析**: 检查 main.go 和路由配置
2. **认证流程**: 追踪登录、令牌签发、权限校验的完整代码路径
3. **注入风险**: 使用模式匹配查找 SQL 拼接、模板渲染、命令执行
4. **外部交互**: 检查所有出站 HTTP 请求的安全性
5. **配置安全**: 审查配置文件和密钥管理实践
6. **日志安全**: 检查日志输出是否泄露敏感信息

---

## 结论

**审计完成——未发现中等或更高严重度的已确认漏洞。**

项目在关键安全控制上表现优秀：
- 遵循 Go 安全编码最佳实践
- 正确使用加密和安全随机数
- 有效防护注入攻击
- SSRF 防护措施完善
- 配置管理安全

建议在部署时确保：
1. 所有密钥通过环境变量设置
2. 数据库密码不要硬编码在配置文件中
3. 在生产环境的反向代理层添加安全响应头

---

**审计人**: TRAE 安全审计助手  
**报告生成时间**: 2026-07-20
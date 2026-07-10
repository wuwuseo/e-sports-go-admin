# Admin 重复代码重构设计

日期：2026-07-10

## 背景

本次审查聚焦手写 Go 代码中的真实重复和已有 helper 未复用问题。重构前 `go test ./...` 全部通过；工作区已有的 `internal/service/permission_helper_test.go` 修改属于用户改动，本次不触碰。

静态重复检测发现 35 个克隆点。多数是短小的 CRUD、分页和 HTTP 编排模板；将它们泛型化只会增加回调、字符串参数和跨文件跳转，因此不纳入本次重构。

## 目标

- 合并必须保持一致的认证、授权和令牌逻辑。
- 将同一领域内已稳定出现的重复收口到私有 helper。
- 优先复用仓库已有 helper，不增加依赖或公共 API。
- 保持响应、错误、日志、副作用和安全校验顺序不变。

## 采用的方案

采用平衡范围：处理高收益重复和两处已有 helper 未复用问题，不建设通用 CRUD 或事务框架。

### 1. 登录加密凭据校验

在 `internal/handler` 包内增加私有 `decryptLoginCredentials`，供用户登录和管理员登录复用。两个 handler 继续负责请求解析、只有两个加密字段均存在时才进入解密的门控，以及把解密结果写回各自请求对象。

helper 必须保持现有顺序：时间戳检查、nonce 检查并消费、`key_id` 检查、身份字段解密、密码解密。解密失败后 nonce 已被消费的现有行为也必须保留。身份字段名称作为参数，仅用于保持 `email` 与 `username` 的既有错误文案。

### 2. 超级管理员系统权限查询

将 `getEnabledSystemMenuPermissions` 和 `getEnabledSystemButtonPermissions` 的公共实现收口为私有 `getEnabledSystemPermissions`。原有两个函数保留为薄包装器，避免修改调用者和现有测试。

通用实现接收权限类型，并保持以下语义：空 domain 不过滤、指定 domain 时包含 common 权限、按 Object 去重、Object 回退规则不变、最终按 Object 排序。菜单和按钮的错误文案保持不变。

### 3. JWT access/refresh 令牌对

在 `internal/service/token_issuer.go` 中抽取私有 `createTokenPair`，复用到用户密码登录、管理员密码登录、OAuth 用户签发和 OAuth 管理员签发四条路径。

helper 只负责 claims 和两个 JWT 的创建。用户或管理员查询、状态检查、密码校验、最后登录时间更新，以及管理员登录后的 logout 标记清理仍留在各自服务中。调用者继续传入正确的用户/管理员 secret 和过期时间。

为保持时间语义，helper 接收时钟函数：密码登录传 `time.Now`，OAuth 签发传返回既定 `now` 的闭包。访问和刷新令牌仍分别取过期时间与签发时间，不顺手统一时间戳。

### 4. 角色启用与禁用

在 `RoleService` 内增加私有 `setStatus`，`Enable` 和 `Disable` 保持原签名并仅传入目标状态。

helper 根据目标状态生成既有动作错误文案，并严格保留查询、操作者自持角色保护、系统角色保护、幂等判断、数据范围校验、已分配管理员校验、更新和缓存失效的顺序。缓存只在真实更新成功后清理。

### 5. 字典种子数据

把 `initDictSeedData` 中两组结构相同的循环合并为一个局部数据切片和一个循环。每条数据包含 `dictType`、`label`、`value` 和 `sort`；不新增跨文件 seeder。

查询、按需创建、日志字段和错误文案保持不变。本次不引入 batch/upsert，也不修改数据库约束。

### 6. Captcha 已有 helper 复用

- `internal/captcha/runtime.go` 在启用主密钥时复用现有 `NewSecretManager`，继续保留 active-version 门控和当前错误包装。
- `internal/captcha/settings.go` 使用现有 `cloneConfig` 复制公开配置。

## 明确不做

- 不抽象跨资源的 CRUD、分页和删除 handler。
- 不抽象 Cron Delete/Enable/Disable/Run handler。
- 不推广或扩展 `ManagedTx`；现有手工事务迁移不会减少代码，并有 rollback 错误语义差异。
- 不合并 OAuth 用户/管理员实体查询等仅表面相似、可能独立演进的逻辑。
- 不修改 Ent 生成代码、数据库结构、路由或公开接口。

## 验证

- 为登录加密 helper 增加一个聚焦的表驱动回归测试，至少覆盖校验顺序相关错误。
- 运行受影响包测试：`go test ./internal/handler ./internal/service ./internal/captcha ./internal`。
- 运行完整测试：`go test ./...`。
- 运行 `gofmt`；再次执行只读 dupl 检查，确认选中的克隆消失，同时接受明确保留的 CRUD/Cron 重复。

## 成功标准

- 所有测试通过。
- 用户已有工作区改动保持原样。
- 没有新增依赖、公共接口或通用框架。
- 选定重复点只保留一个实现，且外部可观察行为不变。

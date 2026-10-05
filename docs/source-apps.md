# 源码功能集

源码功能集编入 go-admin 的同一个二进制，与进程外插件独立。通用注册、校验、路由挂载和生成命令位于 `github.com/wuwuseo/cmf/app`；go-admin 负责鉴权、配置与权限入库。

## 新增功能集

1. 在 `internal/apps/<code>/` 添加 `app.go`，在 `init()` 中调用 `app.Register(app.App{...})`。`code` 必须以小写字母开头，后续只能是小写字母或数字，且不能使用 `admin`、`public`、`user`、`plugin`、`attachments`、`auth`。
2. `App` 声明 `Config`、`Routes` 和 `Permissions`。路由路径是以 `/` 开头的功能集内相对路径；例如 `GET /items` 挂载为 `GET /api/v1/<code>/items`。路由鉴权可选 `AuthPublic`、`AuthUser`、`AuthAdmin`。
3. 每条管理端路由都要声明一条方法和完整路径一致的 `PermAPI` 权限；每条 API 权限也必须对应管理端路由。路径参数在权限中写成 `{id}`，在 Fiber 路由中写成 `:id`。所有权限代码以 `<code>:` 开头，`Parent` 引用同功能集内的权限代码。
4. 如需 Ent 模型，在 `internal/apps/<code>/schema/` 定义标准 `ent.Schema`。类型名以功能集代码首字母大写形式开头，例如 `CmsContent`；关系只能引用同一功能集的 schema。字段需按项目生成钩子添加 JSON `StructTag`。
5. 依次运行生成命令并提交全部生成文件：

   ```bash
   go generate ./internal/apps
   go generate ./internal/ent
   go build ./...
   go test ./internal/apps ./internal
   ```

`internal/apps/cms` 是可运行的内容管理功能集：文章、单页、分类，公开只读 API、管理端 CRUD、菜单和按钮权限。接口说明见 [cms.md](cms.md)。

Ent 生成还会更新 `zz_tableprefix_gen.go`，为新增模型及其关系表应用数据库表前缀；无需手工维护表名清单。

## 启动与数据

应用启动时先校验声明，再执行 Ent 迁移。核心权限种子完成后，功能集配置以 `app.<code>.<key>` 写入 `system_config`，已存在的配置值不覆盖；功能集权限按 `(domain, code)` 更新并解析父级 ID。新建 `normal_admin` 时按现有规则赋权，已有角色不自动补权。`Enabled=false` 跳过配置、权限和路由，但保留模型及其迁移，以免禁用功能时丢失模型定义。

菜单权限中的 `MenuPath` 和 `Component` 供独立的 admin-web 项目使用。后端不打包或挂载功能集页面；CMS 的 `cms/index` 对应 `admin-web/web/src/apps/cms/views/index.vue`。

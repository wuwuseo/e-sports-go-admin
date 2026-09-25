# 幻灯片管理与前端分组 API 设计

日期:2026-09-25
状态:已确认(方案 B 极简版,无时间窗字段)

## 背景与目标

系统缺少幻灯片(banner)管理能力。需要:

1. 管理端(admin-web)新增幻灯片管理页面:增删改查、启用/禁用、排序。
2. 公开免登录接口供前台站点按分组拉取启用中的幻灯片:`GET /api/v1/public/slides?group=home`。

已确认的决策:

- 公开接口**免登录**,挂在现有 `public` 路由组,只读。
- 分组**复用字典管理**:分组即字典类型 `slide_group` 下的字典项,不新建分组表、不加公开分组列表接口(前台写死分组编码)。
- 幻灯片菜单挂在「系统管理」(菜单 ID 3)下,不新建顶级菜单。
- **不做**展示时间窗(start_time/end_time),启用/禁用开关是唯一上下线手段。
- 图片存 URL 字符串(与 admin/user 的 avatar 一致),管理端表单用现成 `attachment-selector` 组件选图。

## 数据模型

新增 Ent schema `internal/ent/schema/slide.go`,生成表 `slides`(Ent 默认蛇形复数命名,与 `dict_data`/`login_logs` 一致)。改完 schema 执行 `go generate ./internal/ent`(Windows 下遇 "user-mapped section open" 文件锁按既有重试/清理再生成流程处理)。

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | int | autoincr, immutable, unique | |
| title | string(100) | NotEmpty | 名称,同时作为图片 alt 文本 |
| image | string(500) | NotEmpty | 图片 URL 字符串(见下方「图片 URL 约定」) |
| link | string(500) | Optional | 点击跳转地址 |
| group_code | string(100) | NotEmpty | 分组编码,对应字典类型 `slide_group` 的字典值 |
| sort | int | 默认 0 | 升序 |
| status | int | 默认 1 | 1 启用 0 禁用 |
| remark | string(500) | Optional | 备注 |
| created_at / updated_at | time | 同 dict_data 惯例 | Immutable / UpdateDefault |

索引:`(group_code, status)` 复合索引,覆盖公开接口的过滤条件。

**图片 URL 约定(2026-09-25 复核确认)**:附件内容的访问地址分两种模式——公开目录返回永久直链,私有目录返回带 TTL 的签名 URL。已确认 `image` 仍存 URL 字符串(与 avatar 一致),**运营约定幻灯片图片统一上传到公开目录**;私有目录图片的签名地址过期后前台会裂图,属已知取舍,不做代码层处理。管理端表单选图后由前端调用现有 access-url 解析(带缓存)得到 URL 存入。

校验策略:后端只校验非空与长度,**不校验 group_code 是否存在于字典**(防拼错由管理端分组下拉保证;字典项被删后幻灯片 group_code 悬空属可接受行为,公开接口按 code 精确匹配,匹配不到返回空数组)。

## 后端 API

### 管理端(`adminAuth` 组,PermissionMiddleware + Casbin 生效)

- `GET /api/v1/admin/auth/slides` — 分页列表,支持 `group_code`、`status` 筛选,按 `sort asc, id asc` 排序。
- `POST /api/v1/admin/auth/slides` — 创建。
- `GET /api/v1/admin/auth/slides/{id}` — 详情。
- `PUT /api/v1/admin/auth/slides/{id}` — 更新。
- `DELETE /api/v1/admin/auth/slides/{id}` — 删除。

分组下拉由前端直接调现有 `GET /api/v1/admin/auth/dict-data/type/slide_group`,后端零改动。

> **权限依赖(最终审查补充,2026-09-25)**:该字典接口受 API 权限种子 **ID 95**(`dict-data:type`,挂在字典管理菜单下)保护。普通管理员只授予幻灯片种子(192–200)时,分组下拉会 403 且无法建/编幻灯片——给角色分配幻灯片权限时须**同时授予 `dict-data:type` API 权限**(标准 RBAC 操作,无需代码改动)。

### 公开端(`public` 组,免登录)

- `GET /api/v1/public/slides?group=home`
  - `group` 可选:传了按 group_code 精确过滤,不传返回全部启用项。
  - 只返回 `status=1` 的记录,按 `sort asc, id asc` 排序,不分页(幻灯片单组数量小)。
  - 返回体只含 `id`、`title`、`image`、`link`、`group_code` 五个字段,不暴露 remark 及审计字段。
  - 查询结果为空时返回空数组而非 404。

### 代码落点

- `internal/service/slide.go` — SlideService(CRUD + ListPublic),模式参照 `dict_data.go`。
- `internal/handler/slide.go` — handler,thin,业务在 service。
- `internal/router.go` — 管理端路由挂 `adminAuth` 组并注入 service;公开路由挂 `public` 组。

### 权限种子(`internal/service/system_permission.go`)

挂在系统管理(ParentID 3)下,SortOrder 15(任务队列之后)。种子机制按 (domain, code) upsert,重复启动安全;**实施前须核对目标库中 192–200 号段未被并行会话占用**(查 `system_permissions` 的 max(id)),被占用则顺延并同步调整种子常量。

| ID | 类型 | Code | 说明 |
|---|---|---|---|
| 192 | menu | `system:slide` | 菜单,/system/slide,component system/slide/index |
| 193 | api | `slide:list` | GET /api/v1/admin/auth/slides |
| 194 | api | `slide:create` | POST 同上 |
| 195 | api | `slide:get` | GET …/{id} |
| 196 | api | `slide:update` | PUT …/{id} |
| 197 | api | `slide:delete-api` | DELETE …/{id} |
| 198 | button | `slide:add` | 新增按钮 |
| 199 | button | `slide:edit` | 编辑按钮 |
| 200 | button | `slide:delete` | 删除按钮 |

(api code 用 `slide:delete-api` 与按钮 `slide:delete` 区分,沿用用户管理 166/176 的先例。)

### 字典预置(`internal/init.go`)

字典类型不是独立实体——只要存在 `dict_type=slide_group` 的字典项,即自动出现在字典管理页(前端按 dict_type 分组渲染)。仿照 `sys_config_group` 的预置机制,启动时插入默认字典项「首页 banner」(dict_type=`slide_group`, label=首页 banner, value=`home`),已存在则跳过。

## 前端(admin-web,仓库 D:\www\admin-web\web)

- 路由:`src/router/routes/modules/system.ts` 新增 slide 子路由,`meta.permissionCode: ['/system/slide', 'slide']`。
- 列表页:`src/views/system/slide/index.vue`,克隆现有 list 页模板(操作日志/登录日志页模式):筛选栏(分组下拉[字典 API]/状态)、`a-table` 列含图片缩略预览、标题、分组、链接、排序、状态开关、备注、操作;reactive 分页;response shim `result.items || result.data`。
- 表单:`src/views/system/slide/components/SlideForm.vue` 新建/编辑弹窗,图片字段用现成 `attachment-selector` 组件。
- API:`src/api/slides.ts`,类型集中到 `src/types/api.ts`。
- i18n:视图级 `locale/zh-CN.ts` + `en-US.ts` 并汇入全局 locale;菜单 key `menu.system.slide`。
- 按钮权限:`v-permission` / `hasButtonPermission` 配合 `slide:add`/`slide:edit`/`slide:delete`。
- 验证:`npm run type:check` + eslint + `npm run build`;部署按既有流程把 dist 移入后端 `web/dist`。

## 错误处理

- service 层校验失败返回参数错误(沿用现有 service 错误返回惯例)。
- 公开接口无认证、无敏感字段、空结果返回空数组;不对公开接口做缓存(可由调用方/网关层自理,YAGNI)。
- 删除幻灯片为物理删除,不涉及附件文件删除(附件生命周期由附件模块自理)。

## 测试

- service 层(现有 in-memory SQLite 测试模式):CRUD 基本流、参数校验、ListPublic 的 status 过滤、group 过滤、排序。
- handler/路由层:公开接口免登录可达、group 参数过滤生效、返回字段不含 remark;管理端接口在无权限时被 PermissionMiddleware 拦截。
- 前端:`npm run type:check` + build 通过。

## 明确不做(YAGNI)

- 独立分组表/分组 CRUD 页面、公开分组列表接口。
- 展示时间窗字段。
- 公开接口缓存、多语言幻灯片、点击统计。

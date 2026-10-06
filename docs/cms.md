# CMS 内容管理 API

功能集代码为 `cms`。内容分为 `article`（文章）和 `page`（单页）；`format` 可选 `markdown`、`html`、`image`。`status=0` 为草稿，`status=1` 为已发布。公开接口只返回已发布内容，管理接口需管理员 JWT 和对应的 `cms:*` API 权限。

## 公开接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/cms/articles` | 文章列表，支持 `page`、`page_size`、`category_id`、`keyword` |
| GET | `/api/v1/cms/articles/{id}` | 文章详情 |
| GET | `/api/v1/cms/pages` | 单页列表，支持 `page`、`page_size`、`keyword` |
| GET | `/api/v1/cms/pages/{id}` | 单页详情 |
| GET | `/api/v1/cms/categories` | 启用的分类列表 |

列表响应为 `{"code":1,"data":{"list":[],"total":0,"page":1,"page_size":10}}`。`page_size` 范围为 1–100。详情响应的 `data` 包含 `id`、`title`、`kind`、`format`、`content`、`category_id`、`status` 和时间字段。停用分类下的文章也不会公开。三种格式都以 `content` 承载正文：Markdown 由前台解析并对生成的 HTML 做安全过滤，HTML 入库前经安全过滤，纯图片是逗号分隔的图片地址列表。没有单独的图片字段。

## 管理接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET / POST | `/api/v1/cms/admin/contents` | 按 `kind=article|page` 查询 / 新增内容 |
| GET / PUT / DELETE | `/api/v1/cms/admin/contents/{id}` | 详情 / 全量更新 / 删除 |
| GET / POST | `/api/v1/cms/admin/categories` | 分类列表 / 新增分类 |
| PUT / DELETE | `/api/v1/cms/admin/categories/{id}` | 更新 / 删除分类 |

新增与更新内容使用相同 JSON 字段，例如：

```json
{
  "title": "示例文章",
  "kind": "article",
  "format": "markdown",
  "content": "# 正文",
  "category_id": 0,
  "status": 0
}
```

`format=image` 的 `content` 是**逗号分隔的图片地址列表**，数量不限，例如 `"/uploads/cms/a.png,/uploads/cms/b.png"`。服务端逐条校验地址（仅接受 `https`、`http` 或 `/uploads/` 路径），去掉空白后按逗号重新拼接；列表为空或含非法地址则返回错误。由于逐条校验而不按 HTML 处理，地址中的 `&` 不会被转义。单页不使用分类。分类请求字段为 `name`、`status`。删除仍被文章引用的分类返回 409。内容的 `format` 在创建后不应再修改：改动格式会让既有 `content` 失去解释方式，管理端编辑时已隐藏格式选择。

管理端图片使用现有附件选择器和 `/api/v1/admin/auth/attachments/upload`，保存公开附件地址。部署时需分别构建 admin-web 并更新后端使用的静态资源。原 `demo` 示例表、配置与权限不会自动迁移或清除；已有管理员角色需按现有权限管理流程授予新的 `cms:*` 权限。

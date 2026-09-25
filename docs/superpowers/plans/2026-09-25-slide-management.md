# 幻灯片管理与前端分组 API 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增幻灯片管理模块:后端 `slides` 表 + 管理端 CRUD + 免登录公开接口 `GET /api/v1/public/slides?group=xxx` + admin-web 幻灯片管理页面(分组复用字典类型 `slide_group`)。

**Architecture:** 单表 Ent 实体,service/handler 沿用仓库现有 CRUD 模式(`DictDataService` 为模板,CRUDBase + 泛型 CRUD 接口);公开接口挂 `public` 路由组只返回安全字段;权限走 system_permission 种子 + Casbin;前端克隆登录日志列表页模板,图片选择复用 `AttachmentSelector` 组件。

**Tech Stack:** Go 1.25.x + Fiber v3 + Ent + SQLite(enttest 内存库测试);前端 Vue3 + TS + Arco Design + axios。

**规格文件:** `docs/superpowers/specs/2026-09-25-slide-management-design.md`(实现与规格冲突时以规格为准;本计划已经过规格比对)。

## Global Constraints

- 后端仓库 `D:\www\admin`,前端仓库是**独立仓库** `D:\www\admin-web\web`(pnpm,node_modules 已装好)。
- Go 版本以 `go.mod` 为准(≥1.25.1);改动过的 Go 文件必须过 `gofmt`;提交信息遵循 Conventional Commits。
- **禁止手工编辑 `internal/ent/` 下的生成文件**,只能改 `internal/ent/schema/` 后重新生成。
- Ent 代码生成在 Windows 下可能报 "user-mapped section open"(文件锁/QQPCRTP):重试 `go generate ./internal/ent` 最多 3 次;仍失败则删除 `internal/ent/slide/`、`internal/ent/slide.go` 等新生成残留后重跑。
- 前端仓库存在**他人未提交的改动**(`src/hooks/user.ts`、`src/views/system/admin/components/AdminForm.vue`、`src/hooks/user.ts.bak`):不得 stage/commit/删除它们;前端提交只 add 本计划列出的文件(前端有 lint-staged pre-commit,会自动格式化暂存文件)。
- 幻灯片 `image` 存 URL 字符串(与 avatar 一致);运营约定图片上传到公开目录,私有目录签名 URL 过期裂图属已知取舍(规格已确认),代码不做处理。
- 公开接口 `GET /api/v1/public/slides` 免登录、只读、只返回 `id/title/image/link/group_code`,空结果返回空数组。
- 权限种子 ID 占用 **192–200**(实施前须核对目标库 `SELECT MAX(id) FROM system_permissions;` 未被并行会话占用,被占用则整段顺延并同步修改种子常量)。
- 后端每任务收尾跑 `go build ./...`;提交前只 `git add` 本任务明确列出的文件(并行会话可能有其他未跟踪改动,如 `.tmp/`、`docs/task-queue-multi-backend-plan.md`,一律不碰)。

---

### Task 1: Ent Schema `Slide` 与代码生成

**Files:**
- Create: `internal/ent/schema/slide.go`
- Generate: `internal/ent/slide/`、`internal/ent/slide.go`、`internal/ent/client.go`、`internal/ent/migrate/schema.go` 等(由 `go generate` 修改)

**Interfaces:**
- Consumes: 无(首个任务)。
- Produces: Ent 客户端 `s.Client.Slide`(Create/Query/Get/UpdateOne/DeleteOne),谓词包 `internal/ent/slide`(谓词 `slide.IDEQ`/`slide.StatusEQ`/`slide.GroupCodeEQ`,常量 `slide.FieldSort`/`slide.FieldID`/`slide.FieldStatus`/`slide.FieldGroupCode`),实体类型 `*ent.Slide`(字段 `ID int`、`Title string`、`Image string`、`Link string`、`GroupCode string`、`Sort int`、`Status int`、`Remark string`、`CreatedAt time.Time`、`UpdatedAt time.Time`)。Task 2 的 service 全部依赖这些名字。

- [ ] **Step 1: 创建 schema 文件**

写入 `internal/ent/schema/slide.go`(字段与索引逐字如下):

```go
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Slide 幻灯片实体(对应表 slides)
type Slide struct {
	ent.Schema
}

// Fields 幻灯片字段定义
func (Slide) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id" autoincr:"true"`),
		field.String("title").
			NotEmpty().
			MaxLen(100).
			StructTag(`json:"title"`).
			Comment("标题,同时作为图片alt文本"),
		field.String("image").
			NotEmpty().
			MaxLen(500).
			StructTag(`json:"image"`).
			Comment("图片URL字符串,约定上传到公开目录"),
		field.String("link").
			Optional().
			MaxLen(500).
			StructTag(`json:"link,omitempty"`).
			Comment("点击跳转地址"),
		field.String("group_code").
			NotEmpty().
			MaxLen(100).
			StructTag(`json:"group_code"`).
			Comment("分组编码,对应字典类型slide_group的字典值"),
		field.Int("sort").
			Default(0).
			StructTag(`json:"sort"`).
			Comment("排序,升序"),
		field.Int("status").
			Default(1).
			StructTag(`json:"status"`).
			Comment("状态 1启用 0禁用"),
		field.String("remark").
			Optional().
			MaxLen(500).
			StructTag(`json:"remark,omitempty"`).
			Comment("备注"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			StructTag(`json:"created_at"`).
			Comment("创建时间"),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			StructTag(`json:"updated_at"`).
			Comment("更新时间"),
	}
}

// Indexes 幻灯片索引
func (Slide) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group_code", "status"),
	}
}
```

- [ ] **Step 2: 生成代码**

Run: `go generate ./internal/ent`
Expected: 无输出、退出码 0;`internal/ent/slide/` 目录与 `internal/ent/slide.go` 出现。若报 "user-mapped section open",按 Global Constraints 的重试规则重跑。

- [ ] **Step 3: 验证编译**

Run: `go build ./...`
Expected: 退出码 0,无编译错误。

- [ ] **Step 4: gofmt 检查**

Run: `gofmt -l internal/ent/schema/slide.go`
Expected: 无输出(已格式化)。

- [ ] **Step 5: Commit(后端仓库 `D:\www\admin`)**

```bash
git add internal/ent/schema/slide.go internal/ent/slide internal/ent/slide.go internal/ent/client.go internal/ent/migrate/schema.go
git commit -m "feat(slide): add slide ent schema"
```

注:若 `git status` 显示 generate 还改了 `internal/ent/runtime.go`、`internal/ent/enttest/` 等文件,一并 `git add` 进本次提交;`.tmp/` 与 `docs/task-queue-multi-backend-plan.md` 不添加。

---

### Task 2: SlideService(测试先行)

**Files:**
- Test: `internal/service/slide_test.go`
- Create: `internal/service/slide.go`

**Interfaces:**
- Consumes: Task 1 的 Ent 客户端(`s.Client.Slide`)、仓库既有 `CRUDBase`/`ListRequest`/`ListResponse`/`GetByID`/`DeleteByID`/`BuildList`(均在 `internal/service/base.go`)、`utils.NormalizePagination`。
- Produces(后续任务按这些签名调用,不得改名):
  - `type SlideService struct` + `func NewSlideService() *SlideService`(内部用 `global.ORM`)
  - `var ErrSlideNotFound = errors.New("幻灯片不存在")`
  - `func (s *SlideService) Create(ctx context.Context, req *CreateSlideRequest) (*SlideResponse, error)`
  - `func (s *SlideService) Get(ctx context.Context, id int) (*SlideResponse, error)`
  - `func (s *SlideService) List(ctx context.Context, req *ListSlideRequest) (*ListSlideResponse, error)`
  - `func (s *SlideService) Update(ctx context.Context, id int, req *UpdateSlideRequest) error`
  - `func (s *SlideService) Delete(ctx context.Context, id int) error`
  - `func (s *SlideService) ListPublic(ctx context.Context, groupCode string) ([]*SlidePublicResponse, error)`
  - 请求/响应类型(Task 3/4、前端类型保持同构):
    - `CreateSlideRequest{Title string; Image string; Link string; GroupCode string; Sort *int; Status *int; Remark string}`(JSON 标签 `title/image/link/group_code/sort/status/remark`,validate 标签 `required,max=100` / `required,max=500` / `omitempty,max=500` / `required,max=100` / `omitempty,max=500`)
    - `UpdateSlideRequest{Title *string; Image *string; Link *string; GroupCode *string; Sort *int; Status *int; Remark *string}`(全部 omitempty + max 校验)
    - `ListSlideRequest{ListRequest; GroupCode string \`query:"group_code"\`; Status *int \`query:"status"\`}`
    - `SlideResponse{ID int; Title string; Image string; Link string; GroupCode string; Sort int; Status int; Remark string; CreatedAt time.Time; UpdatedAt time.Time}`(JSON: `id/title/image/link/group_code/sort/status/remark/created_at/updated_at`)
    - `ListSlideResponse = ListResponse[*SlideResponse]`
    - `SlidePublicResponse{ID int; Title string; Image string; Link string; GroupCode string}`(JSON: `id/title/image/link(omitempty)/group_code`)——**不含 remark/时间戳**。

- [ ] **Step 1: 写失败测试**

写入 `internal/service/slide_test.go`。注意:`intPtr`/`boolPtr` 已在 `dict_data_test.go` 定义(同包),**不要重复定义**。

```go
package service

import (
	"context"
	"testing"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestSlideDB(t *testing.T) (*SlideService, func()) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)

	prevORM := global.ORM
	global.ORM = client

	svc := NewSlideService()
	cleanup := func() {
		global.ORM = prevORM
		client.Close()
	}
	return svc, cleanup
}

func TestSlideService_Create_Defaults(t *testing.T) {
	svc, cleanup := setupTestSlideDB(t)
	defer cleanup()
	ctx := t.Context()

	resp, err := svc.Create(ctx, &CreateSlideRequest{
		Title:     "首页横幅一",
		Image:     "https://cdn.example.com/banner1.png",
		GroupCode: "home",
	})
	if err != nil {
		t.Fatalf("创建幻灯片失败: %v", err)
	}
	if resp.ID != 1 {
		t.Errorf("期望 ID=1, 实际 %d", resp.ID)
	}
	if resp.Sort != 0 {
		t.Errorf("期望 Sort 默认 0, 实际 %d", resp.Sort)
	}
	if resp.Status != 1 {
		t.Errorf("期望 Status 默认 1(启用), 实际 %d", resp.Status)
	}
	if resp.Link != "" {
		t.Errorf("期望 Link 默认空, 实际 %q", resp.Link)
	}
}

func TestSlideService_List_FiltersAndOrder(t *testing.T) {
	svc, cleanup := setupTestSlideDB(t)
	defer cleanup()
	ctx := t.Context()

	mustCreate := func(title, group string, sort, status int) {
		_, err := svc.Create(ctx, &CreateSlideRequest{
			Title: title, Image: "https://cdn.example.com/x.png",
			GroupCode: group, Sort: intPtr(sort), Status: intPtr(status),
		})
		if err != nil {
			t.Fatalf("准备数据失败: %v", err)
		}
	}
	// sort: 20, 10, 30 — 期望列表按 sort 升序 10,20,30
	mustCreate("B-home", "home", 20, 1)
	mustCreate("A-home", "home", 10, 1)
	mustCreate("C-about", "about", 30, 1)
	mustCreate("D-home-disabled", "home", 5, 0)

	// 按分组过滤(含禁用,管理端列表不过滤状态)
	list, err := svc.List(ctx, &ListSlideRequest{GroupCode: "home"})
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if list.Total != 3 {
		t.Fatalf("期望 home 分组共 3 条, 实际 %d", list.Total)
	}
	// 按状态过滤
	list, err = svc.List(ctx, &ListSlideRequest{Status: intPtr(0)})
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if list.Total != 1 || list.Data[0].Title != "D-home-disabled" {
		t.Fatalf("期望仅 1 条禁用记录 D-home-disabled, 实际 total=%d", list.Total)
	}
	// 排序:sort 升序
	list, err = svc.List(ctx, &ListSlideRequest{GroupCode: "home", Status: intPtr(1)})
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if list.Data[0].Title != "A-home" || list.Data[1].Title != "B-home" {
		t.Fatalf("期望按 sort 升序 A-home,B-home, 实际 %s,%s", list.Data[0].Title, list.Data[1].Title)
	}
}

func TestSlideService_Update_Partial(t *testing.T) {
	svc, cleanup := setupTestSlideDB(t)
	defer cleanup()
	ctx := t.Context()

	created, err := svc.Create(ctx, &CreateSlideRequest{
		Title: "原标题", Image: "https://cdn.example.com/old.png", GroupCode: "home",
	})
	if err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	newTitle := "新标题"
	newStatus := 0
	if err := svc.Update(ctx, created.ID, &UpdateSlideRequest{Title: &newTitle, Status: &newStatus}); err != nil {
		t.Fatalf("更新失败: %v", err)
	}

	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if got.Title != "新标题" || got.Status != 0 {
		t.Fatalf("期望 title=新标题 status=0, 实际 title=%s status=%d", got.Title, got.Status)
	}
	// 未更新的字段保持原值
	if got.Image != "https://cdn.example.com/old.png" || got.GroupCode != "home" {
		t.Fatalf("未更新的字段不应变化, 实际 image=%s group=%s", got.Image, got.GroupCode)
	}
}

func TestSlideService_Delete_And_GetNotFound(t *testing.T) {
	svc, cleanup := setupTestSlideDB(t)
	defer cleanup()
	ctx := t.Context()

	created, err := svc.Create(ctx, &CreateSlideRequest{
		Title: "待删除", Image: "https://cdn.example.com/del.png", GroupCode: "home",
	})
	if err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	if err := svc.Delete(ctx, created.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := svc.Get(ctx, created.ID); err != ErrSlideNotFound {
		t.Fatalf("删除后查询期望 ErrSlideNotFound, 实际 %v", err)
	}
	if err := svc.Delete(ctx, 9999); err != ErrSlideNotFound {
		t.Fatalf("删除不存在的记录期望 ErrSlideNotFound, 实际 %v", err)
	}
}

func TestSlideService_ListPublic_OnlyEnabledSorted(t *testing.T) {
	svc, cleanup := setupTestSlideDB(t)
	defer cleanup()
	ctx := t.Context()

	mustCreate := func(title, group string, sort, status int) {
		_, err := svc.Create(ctx, &CreateSlideRequest{
			Title: title, Image: "https://cdn.example.com/x.png",
			GroupCode: group, Sort: intPtr(sort), Status: intPtr(status), Remark: "内部备注",
		})
		if err != nil {
			t.Fatalf("准备数据失败: %v", err)
		}
	}
	mustCreate("第二位", "home", 2, 1)
	mustCreate("第一位", "home", 1, 1)
	mustCreate("已禁用", "home", 0, 0) // 禁用不出现
	mustCreate("别组", "about", 1, 1) // 别分组不出现

	items, err := svc.ListPublic(ctx, "home")
	if err != nil {
		t.Fatalf("ListPublic 失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("期望 2 条启用中的 home 幻灯片, 实际 %d", len(items))
	}
	if items[0].Title != "第一位" || items[1].Title != "第二位" {
		t.Fatalf("期望按 sort 升序, 实际 %s,%s", items[0].Title, items[1].Title)
	}
	for _, it := range items {
		if it.GroupCode != "home" {
			t.Fatalf("公开响应不应包含其他分组, 实际 %s", it.GroupCode)
		}
	}
}

func TestSlideService_ListPublic_NoGroupReturnsAllEnabled(t *testing.T) {
	svc, cleanup := setupTestSlideDB(t)
	defer cleanup()
	ctx := t.Context()

	_, err := svc.Create(ctx, &CreateSlideRequest{Title: "a", Image: "i", GroupCode: "home"})
	if err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	_, err = svc.Create(ctx, &CreateSlideRequest{Title: "b", Image: "i", GroupCode: "about", Status: intPtr(0)})
	if err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	items, err := svc.ListPublic(ctx, "")
	if err != nil {
		t.Fatalf("ListPublic 失败: %v", err)
	}
	if len(items) != 1 || items[0].GroupCode != "home" {
		t.Fatalf("不传分组期望仅返回全部启用 1 条 home, 实际 %d", len(items))
	}
}

func TestSlideService_ListPublic_EmptyReturnsEmptySlice(t *testing.T) {
	svc, cleanup := setupTestSlideDB(t)
	defer cleanup()

	items, err := svc.ListPublic(t.Context(), "not-exist")
	if err != nil {
		t.Fatalf("空结果不应返回错误: %v", err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("期望非 nil 空切片, 实际 %#v", items)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/service -run TestSlideService -v`
Expected: FAIL,报 `undefined: NewSlideService`(或 slide 相关类型未定义)。

- [ ] **Step 3: 实现 service**

写入 `internal/service/slide.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/slide"
	"go-admin/internal/global"
	"go-admin/internal/utils"
)

var ErrSlideNotFound = errors.New("幻灯片不存在")

// SlideService 幻灯片服务
type SlideService struct {
	*CRUDBase
}

var _ CRUD[CreateSlideRequest, UpdateSlideRequest, ListSlideRequest, SlideResponse] = (*SlideService)(nil)

// NewSlideService 创建幻灯片服务实例
func NewSlideService() *SlideService {
	return &SlideService{
		CRUDBase: &CRUDBase{Client: global.ORM},
	}
}

// CreateSlideRequest 创建幻灯片请求
type CreateSlideRequest struct {
	Title     string `json:"title" validate:"required,max=100"`
	Image     string `json:"image" validate:"required,max=500"`
	Link      string `json:"link" validate:"omitempty,max=500"`
	GroupCode string `json:"group_code" validate:"required,max=100"`
	Sort      *int   `json:"sort"`
	Status    *int   `json:"status"`
	Remark    string `json:"remark" validate:"omitempty,max=500"`
}

// UpdateSlideRequest 更新幻灯片请求
type UpdateSlideRequest struct {
	Title     *string `json:"title" validate:"omitempty,max=100"`
	Image     *string `json:"image" validate:"omitempty,max=500"`
	Link      *string `json:"link" validate:"omitempty,max=500"`
	GroupCode *string `json:"group_code" validate:"omitempty,max=100"`
	Sort      *int    `json:"sort"`
	Status    *int    `json:"status"`
	Remark    *string `json:"remark" validate:"omitempty,max=500"`
}

// ListSlideRequest 幻灯片列表请求
type ListSlideRequest struct {
	ListRequest
	GroupCode string `query:"group_code"`
	Status    *int   `query:"status"`
}

// SlideResponse 幻灯片响应(管理端)
type SlideResponse struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	Image     string    `json:"image"`
	Link      string    `json:"link,omitempty"`
	GroupCode string    `json:"group_code"`
	Sort      int       `json:"sort"`
	Status    int       `json:"status"`
	Remark    string    `json:"remark,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListSlideResponse 幻灯片列表响应
type ListSlideResponse = ListResponse[*SlideResponse]

// SlidePublicResponse 公开幻灯片响应(仅安全字段)
type SlidePublicResponse struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Image     string `json:"image"`
	Link      string `json:"link,omitempty"`
	GroupCode string `json:"group_code"`
}

// Create 创建幻灯片
func (s *SlideService) Create(ctx context.Context, req *CreateSlideRequest) (*SlideResponse, error) {
	builder := s.Client.Slide.Create().
		SetTitle(req.Title).
		SetImage(req.Image).
		SetGroupCode(req.GroupCode)

	if req.Link != "" {
		builder.SetLink(req.Link)
	}
	if req.Sort != nil {
		builder.SetSort(*req.Sort)
	}
	if req.Status != nil {
		builder.SetStatus(*req.Status)
	}
	if req.Remark != "" {
		builder.SetRemark(req.Remark)
	}

	item, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建幻灯片失败: %w", err)
	}

	return toSlideResponse(item), nil
}

// Get 获取幻灯片详情
func (s *SlideService) Get(ctx context.Context, id int) (*SlideResponse, error) {
	return GetByID(ctx,
		func(ctx context.Context) (*ent.Slide, error) {
			return s.Client.Slide.Query().Where(slide.IDEQ(id)).Only(ctx)
		}, toSlideResponse, ErrSlideNotFound, "幻灯片")
}

// List 获取幻灯片列表(管理端,含禁用)
func (s *SlideService) List(ctx context.Context, req *ListSlideRequest) (*ListSlideResponse, error) {
	req.Page, req.PageSize = utils.NormalizePagination(req.Page, req.PageSize)

	query := s.Client.Slide.Query()

	if req.GroupCode != "" {
		query = query.Where(slide.GroupCodeEQ(req.GroupCode))
	}
	if req.Status != nil {
		query = query.Where(slide.StatusEQ(*req.Status))
	}

	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取幻灯片总数失败: %w", err)
	}

	query = query.Order(ent.Asc(slide.FieldSort), ent.Asc(slide.FieldID))
	if req.PageSize > 0 {
		offset := (req.Page - 1) * req.PageSize
		query = query.Offset(offset).Limit(req.PageSize)
	}
	items, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取幻灯片列表失败: %w", err)
	}

	return BuildList(items, total, req.Page, toSlideResponse), nil
}

// Update 更新幻灯片
func (s *SlideService) Update(ctx context.Context, id int, req *UpdateSlideRequest) error {
	item, err := s.Client.Slide.Query().
		Where(slide.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrSlideNotFound
		}
		return fmt.Errorf("查询幻灯片失败: %w", err)
	}

	update := s.Client.Slide.UpdateOne(item)

	if req.Title != nil {
		update.SetTitle(*req.Title)
	}
	if req.Image != nil {
		update.SetImage(*req.Image)
	}
	if req.Link != nil {
		update.SetLink(*req.Link)
	}
	if req.GroupCode != nil {
		update.SetGroupCode(*req.GroupCode)
	}
	if req.Sort != nil {
		update.SetSort(*req.Sort)
	}
	if req.Status != nil {
		update.SetStatus(*req.Status)
	}
	if req.Remark != nil {
		update.SetRemark(*req.Remark)
	}

	if _, err := update.Save(ctx); err != nil {
		return fmt.Errorf("更新幻灯片失败: %w", err)
	}

	return nil
}

// Delete 删除幻灯片
func (s *SlideService) Delete(ctx context.Context, id int) error {
	return DeleteByID(ctx,
		func(ctx context.Context) (*ent.Slide, error) {
			return s.Client.Slide.Query().Where(slide.IDEQ(id)).Only(ctx)
		},
		func(e *ent.Slide) error {
			return s.Client.Slide.DeleteOne(e).Exec(ctx)
		},
		ErrSlideNotFound, "幻灯片")
}

// ListPublic 公开获取启用中的幻灯片(免登录);groupCode 为空返回全部分组,按 sort 升序
func (s *SlideService) ListPublic(ctx context.Context, groupCode string) ([]*SlidePublicResponse, error) {
	query := s.Client.Slide.Query().Where(slide.StatusEQ(1))
	if groupCode != "" {
		query = query.Where(slide.GroupCodeEQ(groupCode))
	}

	items, err := query.
		Order(ent.Asc(slide.FieldSort), ent.Asc(slide.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询公开幻灯片失败: %w", err)
	}

	data := make([]*SlidePublicResponse, 0, len(items))
	for _, item := range items {
		data = append(data, &SlidePublicResponse{
			ID:        item.ID,
			Title:     item.Title,
			Image:     item.Image,
			Link:      item.Link,
			GroupCode: item.GroupCode,
		})
	}

	return data, nil
}

// toSlideResponse 转换为响应格式
func toSlideResponse(item *ent.Slide) *SlideResponse {
	return &SlideResponse{
		ID:        item.ID,
		Title:     item.Title,
		Image:     item.Image,
		Link:      item.Link,
		GroupCode: item.GroupCode,
		Sort:      item.Sort,
		Status:    item.Status,
		Remark:    item.Remark,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/service -run TestSlideService -v`
Expected: 全部 PASS(8 个测试函数)。

- [ ] **Step 5: 全包回归 + gofmt**

Run: `go test ./internal/service` 与 `gofmt -l internal/service/slide.go internal/service/slide_test.go`
Expected: 测试 PASS;gofmt 无输出。

- [ ] **Step 6: Commit(后端仓库)**

```bash
git add internal/service/slide.go internal/service/slide_test.go
git commit -m "feat(slide): add slide service with public grouped query"
```

---

### Task 3: Slide Handler(管理端 5 个 + 公开 1 个,测试先行)

**Files:**
- Test: `internal/handler/slide_public_test.go`
- Create: `internal/handler/slide.go`

**Interfaces:**
- Consumes: Task 2 的 `service.SlideService` 全部方法与类型;仓库既有 handler 设施:`c.Locals("utils.Response").(*utils.Response)`、`validate.ParseAndValidate`/`validate.ParseQueryAndValidate`/`validate.IsValidationError`/`validate.FirstErrorMessage`、`global.Response*Code` 常量、`logOperation(c, logService, module, action, detail)`。
- Produces(路由任务按这些名字注册):handler 函数 `CreateSlide`、`ListSlides`、`GetSlide`、`UpdateSlide`、`DeleteSlide`、`ListPublicSlides`,均 `func(c fiber.Ctx) error`。

- [ ] **Step 1: 写公开接口的失败测试**

写入 `internal/handler/slide_public_test.go`(公开接口免登录、group 过滤、安全字段,在 handler 层闭环验证):

```go
package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"
)

func setupSlidePublicTestApp(t *testing.T) (*fiber.App, *service.SlideService, func()) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)

	prevORM := global.ORM
	global.ORM = client
	svc := service.NewSlideService()

	app := fiber.New()
	app.Get("/api/v1/public/slides", func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("service.SlideService", svc)
		return ListPublicSlides(c)
	})

	cleanup := func() {
		global.ORM = prevORM
		client.Close()
	}
	return app, svc, cleanup
}

func TestListPublicSlides_AnonymousAccessibleAndFiltered(t *testing.T) {
	app, svc, cleanup := setupSlidePublicTestApp(t)
	defer cleanup()

	homeOn := mustCreateSlideForTest(t, svc, "第一位", "home", 1, 1)
	homeOn2 := mustCreateSlideForTest(t, svc, "第二位", "home", 2, 1)
	_ = mustCreateSlideForTest(t, svc, "已禁用", "home", 0, 0)
	_ = mustCreateSlideForTest(t, svc, "别组", "about", 1, 1)

	// group=home:仅启用中的 home,按 sort 升序
	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/slides?group=home", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.StatusCode, string(body))
	}

	var decoded struct {
		Code int `json:"code"`
		Data []struct {
			ID        int    `json:"id"`
			Title     string `json:"title"`
			Image     string `json:"image"`
			Link      string `json:"link"`
			GroupCode string `json:"group_code"`
			Remark    string `json:"remark"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response failed: %v, body = %s", err, string(body))
	}
	if decoded.Code != global.ResponseSuccessCode {
		t.Fatalf("response code = %d, want %d, body = %s", decoded.Code, global.ResponseSuccessCode, string(body))
	}
	if len(decoded.Data) != 2 {
		t.Fatalf("期望 2 条, 实际 %d, body = %s", len(decoded.Data), string(body))
	}
	if decoded.Data[0].ID != homeOn.ID || decoded.Data[1].ID != homeOn2.ID {
		t.Fatalf("期望按 sort 升序 %d,%d, 实际 %d,%d", homeOn.ID, homeOn2.ID, decoded.Data[0].ID, decoded.Data[1].ID)
	}
	for _, item := range decoded.Data {
		if item.Remark != "" {
			t.Fatalf("公开响应不得包含 remark 字段, body = %s", string(body))
		}
	}
	if !strings.Contains(string(body), `"group_code":"home"`) {
		t.Fatalf("响应应包含 group_code 字段, body = %s", string(body))
	}
}

func TestListPublicSlides_NoGroupReturnsAllEnabled(t *testing.T) {
	app, svc, cleanup := setupSlidePublicTestApp(t)
	defer cleanup()

	_ = mustCreateSlideForTest(t, svc, "甲", "home", 1, 1)
	_ = mustCreateSlideForTest(t, svc, "乙", "about", 1, 1)
	_ = mustCreateSlideForTest(t, svc, "丙", "about", 1, 0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/slides", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	var decoded struct {
		Code int `json:"code"`
		Data []struct {
			GroupCode string `json:"group_code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if decoded.Code != global.ResponseSuccessCode || len(decoded.Data) != 2 {
		t.Fatalf("不传 group 期望全部启用 2 条, code=%d len=%d body=%s", decoded.Code, len(decoded.Data), string(body))
	}
}

func TestListPublicSlides_EmptyGroupReturnsEmptyArray(t *testing.T) {
	app, _, cleanup := setupSlidePublicTestApp(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/slides?group=nope", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	if !strings.Contains(string(body), `"data":[]`) {
		t.Fatalf("空分组期望返回空数组, body = %s", string(body))
	}
}
```

同文件追加测试辅助函数:

```go
func mustCreateSlideForTest(t *testing.T, svc *service.SlideService, title, group string, sort, status int) *service.SlideResponse {
	t.Helper()
	item, err := svc.Create(context.Background(), &service.CreateSlideRequest{
		Title: title, Image: "https://cdn.example.com/x.png",
		GroupCode: group, Sort: &sort, Status: &status, Remark: "内部备注",
	})
	if err != nil {
		t.Fatalf("准备幻灯片数据失败: %v", err)
	}
	return item
}
```

(并在测试文件 import 块中加入 `"context"`。)

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/handler -run TestListPublicSlides -v`
Expected: FAIL,报 `undefined: ListPublicSlides`。

- [ ] **Step 3: 实现 handler**

写入 `internal/handler/slide.go`:

```go
package handler

import (
	"fmt"
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/validate"
)

// CreateSlide 创建幻灯片
// @Summary 创建幻灯片
// @Description 创建新的幻灯片
// @Tags 幻灯片管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body service.CreateSlideRequest true "幻灯片信息"
// @Success 200 {object} utils.Response{data=service.SlideResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/slides [post]
func CreateSlide(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SlideService").(*service.SlideService)

	req, err := validate.ParseAndValidate[service.CreateSlideRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	item, err := svc.Create(c.Context(), req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "创建幻灯片失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "system", "创建", fmt.Sprintf("创建幻灯片: %s", req.Title))

	return resp.Success(item, global.ResponseSuccessCode, "幻灯片创建成功").ToJSON(c)
}

// ListSlides 获取幻灯片列表
// @Summary 获取幻灯片列表
// @Description 获取幻灯片列表,支持分页、分组和状态筛选
// @Tags 幻灯片管理
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码,默认1"
// @Param page_size query int false "每页数量,默认10"
// @Param keyword query string false "关键字"
// @Param group_code query string false "分组编码"
// @Param status query int false "状态 1启用 0禁用"
// @Success 200 {object} utils.Response{data=service.ListSlideResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/slides [get]
func ListSlides(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SlideService").(*service.SlideService)

	req, err := validate.ParseQueryAndValidate[service.ListSlideRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if req.Page == 0 {
		req.Page = 1
	}
	if req.PageSize == 0 {
		req.PageSize = 10
	}

	result, err := svc.List(c.Context(), req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取幻灯片列表失败").ToJSON(c)
	}

	return resp.Success(result, global.ResponseSuccessCode, "获取幻灯片列表成功").ToJSON(c)
}

// GetSlide 获取幻灯片详情
// @Summary 获取幻灯片详情
// @Description 根据ID获取幻灯片详细信息
// @Tags 幻灯片管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "幻灯片ID"
// @Success 200 {object} utils.Response{data=service.SlideResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/slides/{id} [get]
func GetSlide(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SlideService").(*service.SlideService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的幻灯片ID").ToJSON(c)
	}

	item, err := svc.Get(c.Context(), id)
	if err != nil {
		if err == service.ErrSlideNotFound {
			return resp.Fail(global.ResponseNotFound, "幻灯片不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取幻灯片失败").ToJSON(c)
	}

	return resp.Success(item, global.ResponseSuccessCode, "获取幻灯片成功").ToJSON(c)
}

// UpdateSlide 更新幻灯片
// @Summary 更新幻灯片
// @Description 根据ID更新幻灯片信息
// @Tags 幻灯片管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "幻灯片ID"
// @Param body body service.UpdateSlideRequest true "更新信息"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/slides/{id} [put]
func UpdateSlide(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SlideService").(*service.SlideService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的幻灯片ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.UpdateSlideRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if err := svc.Update(c.Context(), id, req); err != nil {
		if err == service.ErrSlideNotFound {
			return resp.Fail(global.ResponseNotFound, "幻灯片不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "更新幻灯片失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "system", "更新", fmt.Sprintf("更新幻灯片 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "幻灯片更新成功").ToJSON(c)
}

// DeleteSlide 删除幻灯片
// @Summary 删除幻灯片
// @Description 根据ID删除幻灯片
// @Tags 幻灯片管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "幻灯片ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/slides/{id} [delete]
func DeleteSlide(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SlideService").(*service.SlideService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的幻灯片ID").ToJSON(c)
	}

	if err := svc.Delete(c.Context(), id); err != nil {
		if err == service.ErrSlideNotFound {
			return resp.Fail(global.ResponseNotFound, "幻灯片不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "删除幻灯片失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "system", "删除", fmt.Sprintf("删除幻灯片 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "幻灯片删除成功").ToJSON(c)
}

// ListPublicSlides 公开获取幻灯片(免登录)
// @Summary 公开幻灯片列表
// @Description 免登录获取启用中的幻灯片,可按分组过滤,按sort升序返回;空结果返回空数组
// @Tags 公开接口
// @Produce json
// @Param group query string false "分组编码,不传返回全部分组"
// @Success 200 {object} utils.Response{data=[]service.SlidePublicResponse}
// @Failure 500 {object} utils.Response
// @Router /api/v1/public/slides [get]
func ListPublicSlides(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SlideService").(*service.SlideService)

	items, err := svc.ListPublic(c.Context(), c.Query("group"))
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取幻灯片失败").ToJSON(c)
	}

	return resp.Success(items, global.ResponseSuccessCode, "获取幻灯片成功").ToJSON(c)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/handler -run TestListPublicSlides -v`
Expected: 3 个测试全部 PASS。

- [ ] **Step 5: 全包回归 + gofmt**

Run: `go test ./internal/handler` 与 `gofmt -l internal/handler/slide.go internal/handler/slide_public_test.go`
Expected: PASS;gofmt 无输出。

- [ ] **Step 6: Commit(后端仓库)**

```bash
git add internal/handler/slide.go internal/handler/slide_public_test.go
git commit -m "feat(slide): add slide handlers incl. anonymous public endpoint"
```

---

### Task 4: 路由接线 + 权限种子 + 字典预置

**Files:**
- Modify: `internal/router.go`(dictData 路由块之后,约 495 行处)
- Modify: `internal/service/system_permission.go`(ID 191 种子条目之后追加新 append 块)
- Modify: `internal/init.go`(字典预置 items 切片,约 485 行)

**Interfaces:**
- Consumes: Task 2 `service.NewSlideService()`、Task 3 六个 handler 函数;`SeedPermission` 结构(`internal/service/system_permission.go:132`);`public`/`adminAuth` 路由组变量(`internal/router.go`)。
- Produces: 生效路由 `POST/GET /api/v1/admin/auth/slides`、`GET/PUT/DELETE /api/v1/admin/auth/slides/:id`、`GET /api/v1/public/slides`;种子权限 192–200;字典类型 `slide_group` 预置项「首页 banner」(value=`home`)。

- [ ] **Step 1: 注册路由**

在 `internal/router.go` 中找到 dictData 路由块(以 `dictData.Delete("/:id", handler.DeleteDictData)` 结尾),在其后插入:

```go
	// 幻灯片管理路由
	slideService := service.NewSlideService()
	slides := adminAuth.Group("/slides", func(c fiber.Ctx) error {
		c.Locals("service.SlideService", slideService)
		return c.Next()
	})
	slides.Post("", handler.CreateSlide)
	slides.Get("", handler.ListSlides)
	slides.Get("/:id", handler.GetSlide)
	slides.Put("/:id", handler.UpdateSlide)
	slides.Delete("/:id", handler.DeleteSlide)

	// 公开幻灯片接口(免登录,前台按分组拉取)
	public.Get("/slides", func(c fiber.Ctx) error {
		c.Locals("service.SlideService", slideService)
		return c.Next()
	}, handler.ListPublicSlides)
```

- [ ] **Step 2: 追加权限种子**

先核对种子 ID 号段未冲突(目标库执行):

```sql
SELECT MAX(id) FROM system_permissions;
```

若 ≥192,把下述所有 ID 整体顺延(191→+N 偏移)并保持相对关系;否则原样使用。

在 `internal/service/system_permission.go` 中找到种子 `ID: 191`(任务队列删除API)所在行,它所在的 `seedData = append(seedData, ...)` 块结束的 `)` 之后,追加:

```go
	// 幻灯片管理权限(2026-09-25 幻灯片模块)
	seedData = append(seedData,
		SeedPermission{ID: 192, Domain: "default", Type: "menu", Name: "幻灯片管理", Code: "system:slide", MenuPath: "/system/slide", Component: "system/slide/index", ParentID: 3, SortOrder: 15, IsCommon: false, Status: 1, Description: "幻灯片管理页面"},
		SeedPermission{ID: 193, Domain: "default", Type: "api", Name: "幻灯片列表", Code: "slide:list", Path: "/api/v1/admin/auth/slides", Method: "GET", ParentID: 192, SortOrder: 0, IsCommon: false, Status: 1, Description: "获取幻灯片列表API"},
		SeedPermission{ID: 194, Domain: "default", Type: "api", Name: "创建幻灯片", Code: "slide:create", Path: "/api/v1/admin/auth/slides", Method: "POST", ParentID: 192, SortOrder: 1, IsCommon: false, Status: 1, Description: "创建幻灯片API"},
		SeedPermission{ID: 195, Domain: "default", Type: "api", Name: "幻灯片详情", Code: "slide:get", Path: "/api/v1/admin/auth/slides/{id}", Method: "GET", ParentID: 192, SortOrder: 2, IsCommon: false, Status: 1, Description: "获取幻灯片详情API"},
		SeedPermission{ID: 196, Domain: "default", Type: "api", Name: "更新幻灯片", Code: "slide:update", Path: "/api/v1/admin/auth/slides/{id}", Method: "PUT", ParentID: 192, SortOrder: 3, IsCommon: false, Status: 1, Description: "更新幻灯片API"},
		SeedPermission{ID: 197, Domain: "default", Type: "api", Name: "删除幻灯片", Code: "slide:delete-api", Path: "/api/v1/admin/auth/slides/{id}", Method: "DELETE", ParentID: 192, SortOrder: 4, IsCommon: false, Status: 1, Description: "删除幻灯片API"},
		SeedPermission{ID: 198, Domain: "default", Type: "button", Name: "幻灯片管理-新增按钮", Code: "slide:add", ParentID: 192, SortOrder: 0, IsCommon: false, Status: 1, Description: "幻灯片管理页面新增按钮"},
		SeedPermission{ID: 199, Domain: "default", Type: "button", Name: "幻灯片管理-编辑按钮", Code: "slide:edit", ParentID: 192, SortOrder: 1, IsCommon: false, Status: 1, Description: "幻灯片管理页面编辑按钮"},
		SeedPermission{ID: 200, Domain: "default", Type: "button", Name: "幻灯片管理-删除按钮", Code: "slide:delete", ParentID: 192, SortOrder: 2, IsCommon: false, Status: 1, Description: "幻灯片管理页面删除按钮"},
	)
```

(api code 用 `slide:delete-api` 与按钮 `slide:delete` 区分,沿用用户管理 166/176 的先例。)

- [ ] **Step 3: 预置字典项**

在 `internal/init.go` 的字典种子 items 切片中,`{"sys_config_group", "附件配置", "attachment", 2},` 之后追加一行:

```go
		{"slide_group", "首页 banner", "home", 1},
```

- [ ] **Step 4: 编译 + 全量测试**

Run: `go build ./... && go test ./internal/service -run TestInitSeedPermissions -v; go test ./internal/router`
Expected: 编译 0 退出;相关测试 PASS(router 包无测试则显示 no test files,属正常)。

- [ ] **Step 5: 全量后端回归**

Run: `go test ./...`
Expected: 全部 PASS(或既有 skip)。

- [ ] **Step 6: Commit(后端仓库)**

```bash
git add internal/router.go internal/service/system_permission.go internal/init.go
git commit -m "feat(slide): wire routes, permission seeds and slide_group dict preset"
```

---

### Task 5: 前端类型与 API 模块

**Files:**
- Modify: `D:\www\admin-web\web\src\types\api.ts`(文件末尾追加)
- Create: `D:\www\admin-web\web\src\api\slides.ts`

**Interfaces:**
- Consumes: `src/types/api.ts` 既有 `ApiResponse<T>`、`PaginatedResponse<T>`。
- Produces(Task 6 页面按此调用):`getSlides(params?)`、`getSlideDetail(id)`、`createSlide(data)`、`updateSlide(id, data)`、`deleteSlide(id)`;类型 `SlideResponse`、`SlideQuery`、`CreateSlideRequest`、`UpdateSlideRequest`。

- [ ] **Step 1: 追加类型**

在 `src/types/api.ts` 文件末尾追加:

```ts
export interface SlideResponse {
  id: number;
  title: string;
  image: string;
  link?: string;
  group_code: string;
  sort: number;
  status: number;
  remark?: string;
  created_at: string;
  updated_at: string;
}

export interface SlideQuery {
  page?: number;
  page_size?: number;
  keyword?: string;
  group_code?: string;
  status?: number;
}

export interface CreateSlideRequest {
  title: string;
  image: string;
  link?: string;
  group_code: string;
  sort?: number;
  status?: number;
  remark?: string;
}

export interface UpdateSlideRequest {
  title?: string;
  image?: string;
  link?: string;
  group_code?: string;
  sort?: number;
  status?: number;
  remark?: string;
}
```

- [ ] **Step 2: 创建 API 模块**

写入 `src/api/slides.ts`:

```ts
import axios from 'axios';
import type {
  SlideResponse,
  SlideQuery,
  CreateSlideRequest,
  UpdateSlideRequest,
  ApiResponse,
  PaginatedResponse,
} from '@/types/api';

// 获取幻灯片列表
export function getSlides(params?: SlideQuery) {
  return axios.get<ApiResponse<PaginatedResponse<SlideResponse>>>(
    '/api/v1/admin/auth/slides',
    { params }
  );
}

// 获取幻灯片详情
export function getSlideDetail(id: number) {
  return axios.get<ApiResponse<SlideResponse>>(
    `/api/v1/admin/auth/slides/${id}`
  );
}

// 创建幻灯片
export function createSlide(data: CreateSlideRequest) {
  return axios.post<ApiResponse<SlideResponse>>(
    '/api/v1/admin/auth/slides',
    data
  );
}

// 更新幻灯片
export function updateSlide(id: number, data: UpdateSlideRequest) {
  return axios.put<ApiResponse<SlideResponse>>(
    `/api/v1/admin/auth/slides/${id}`,
    data
  );
}

// 删除幻灯片
export function deleteSlide(id: number) {
  return axios.delete<ApiResponse<null>>(`/api/v1/admin/auth/slides/${id}`);
}
```

- [ ] **Step 3: 类型检查**

Run(在 `D:\www\admin-web\web` 下):`npm run type:check`
Expected: 退出码 0,无类型错误。

- [ ] **Step 4: Commit(前端仓库)**

```bash
cd D:/www/admin-web/web
git add src/types/api.ts src/api/slides.ts
git commit -m "feat: slide api module and types"
```

---

### Task 6: 前端幻灯片管理页(列表 + 表单)

**Files:**
- Create: `D:\www\admin-web\web\src\views\system\slide\index.vue`
- Create: `D:\www\admin-web\web\src\views\system\slide\components\SlideForm.vue`

**Interfaces:**
- Consumes: Task 5 的 API 函数与类型;全局 axios 拦截器;`@/api/dict-data` 的 `getDictDataByType(type)`(返回 `ApiResponse<DictDataResponse[]>`,`DictDataResponse` 含 `label/value`);`@/services/attachment-access.service` 的 `resolveAttachmentAccessUrl(id: number): Promise<string>`;`@/components/attachment-selector/index.vue`(props: `modelValue: AttachmentResponse | AttachmentResponse[] | null`、`accept`;emits: `select(AttachmentResponse | AttachmentResponse[])`);`@/utils` 的 `formatDateTime`;`@arco-design/web-vue` 的 `Message`;`@/hooks/permission` 的 `usePermission()`(`hasButtonPermission(code)`);i18n 键(Task 7 注册)。
- Produces: 页面组件 `SystemSlide`(index.vue default name),子组件 SlideForm(props: `visible: boolean`、`record: SlideResponse | null`;emits: `update:visible`、`saved`)。

- [ ] **Step 1: 创建表单组件**

写入 `src/views/system/slide/components/SlideForm.vue`:

```vue
<template>
  <a-modal
    :visible="visible"
    :title="isEdit ? $t('system.slide.form.editTitle') : $t('system.slide.form.createTitle')"
    :width="560"
    :mask-closable="false"
    @cancel="handleCancel"
    @before-ok="handleOk"
  >
    <a-form ref="formRef" :model="form" auto-label-width :rules="rules">
      <a-form-item field="title" :label="$t('system.slide.title')" required>
        <a-input
          v-model="form.title"
          :max-length="100"
          :placeholder="$t('system.slide.title.placeholder')"
        />
      </a-form-item>
      <a-form-item field="group_code" :label="$t('system.slide.group')" required>
        <a-select
          v-model="form.group_code"
          :placeholder="$t('system.slide.group.placeholder')"
          :loading="groupLoading"
          allow-clear
        >
          <a-option
            v-for="opt in groupOptions"
            :key="opt.value"
            :value="opt.value"
            :label="opt.label"
          />
        </a-select>
      </a-form-item>
      <a-form-item field="image" :label="$t('system.slide.image')" required>
        <div class="slide-image-field">
          <a-input
            v-model="form.image"
            :max-length="500"
            :placeholder="$t('system.slide.image.placeholder')"
          />
          <div v-if="form.image" class="slide-image-preview">
            <img :src="form.image" :alt="form.title" />
          </div>
          <AttachmentSelector
            :model-value="selectedAttachment"
            accept="image/*"
            @select="handleImageSelect"
          />
        </div>
      </a-form-item>
      <a-form-item field="link" :label="$t('system.slide.link')">
        <a-input
          v-model="form.link"
          :max-length="500"
          :placeholder="$t('system.slide.link.placeholder')"
        />
      </a-form-item>
      <a-form-item field="sort" :label="$t('system.slide.sort')">
        <a-input-number v-model="form.sort" :min="0" style="width: 140px" />
      </a-form-item>
      <a-form-item field="status" :label="$t('system.slide.status')">
        <a-radio-group v-model="form.status">
          <a-radio :value="1">{{ $t('system.slide.status.enabled') }}</a-radio>
          <a-radio :value="0">{{ $t('system.slide.status.disabled') }}</a-radio>
        </a-radio-group>
      </a-form-item>
      <a-form-item field="remark" :label="$t('system.slide.remark')">
        <a-textarea
          v-model="form.remark"
          :max-length="500"
          :auto-size="{ minRows: 2, maxRows: 4 }"
          :placeholder="$t('system.slide.remark.placeholder')"
        />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script lang="ts" setup>
  import { ref, reactive, watch, computed } from 'vue';
  import { Message } from '@arco-design/web-vue';
  import { useI18n } from 'vue-i18n';
  import {
    createSlide,
    updateSlide,
  } from '@/api/slides';
  import { getDictDataByType } from '@/api/dict-data';
  import { resolveAttachmentAccessUrl } from '@/services/attachment-access.service';
  import AttachmentSelector from '@/components/attachment-selector/index.vue';
  import type {
    SlideResponse,
    DictDataResponse,
    AttachmentResponse,
  } from '@/types/api';

  const { t } = useI18n();

  const props = defineProps<{
    visible: boolean;
    record: SlideResponse | null;
  }>();

  const emit = defineEmits<{
    (e: 'update:visible', value: boolean): void;
    (e: 'saved'): void;
  }>();

  const SLIDE_GROUP_DICT_TYPE = 'slide_group';

  const formRef = ref();
  const groupLoading = ref(false);
  const groupOptions = ref<DictDataResponse[]>([]);
  const selectedAttachment = ref<AttachmentResponse | null>(null);

  const isEdit = computed(() => !!props.record);

  const form = reactive({
    title: '',
    group_code: '',
    image: '',
    link: '',
    sort: 0,
    status: 1,
    remark: '',
  });

  const rules = {
    title: [{ required: true, message: t('system.slide.title.required') }],
    group_code: [{ required: true, message: t('system.slide.group.required') }],
    image: [{ required: true, message: t('system.slide.image.required') }],
  };

  const loadGroupOptions = async () => {
    groupLoading.value = true;
    try {
      const { data: responseData } = await getDictDataByType(
        SLIDE_GROUP_DICT_TYPE
      );
      groupOptions.value = responseData.data || [];
    } finally {
      groupLoading.value = false;
    }
  };

  const handleImageSelect = async (
    attachment: AttachmentResponse | AttachmentResponse[]
  ) => {
    const next = Array.isArray(attachment) ? attachment[0] : attachment;
    if (!next) return;
    selectedAttachment.value = next;
    // 统一上传约定:幻灯片图片存访问 URL 字符串(公开目录为永久直链)
    form.image = await resolveAttachmentAccessUrl(next.id);
  };

  const resetForm = () => {
    form.title = '';
    form.group_code = '';
    form.image = '';
    form.link = '';
    form.sort = 0;
    form.status = 1;
    form.remark = '';
    selectedAttachment.value = null;
    formRef.value?.clearValidate();
  };

  const fillForm = (record: SlideResponse) => {
    form.title = record.title;
    form.group_code = record.group_code;
    form.image = record.image;
    form.link = record.link || '';
    form.sort = record.sort;
    form.status = record.status;
    form.remark = record.remark || '';
    selectedAttachment.value = null;
  };

  watch(
    () => props.visible,
    (visible) => {
      if (!visible) return;
      loadGroupOptions();
      if (props.record) {
        fillForm(props.record);
      } else {
        resetForm();
      }
    }
  );

  const handleCancel = () => {
    emit('update:visible', false);
  };

  const handleOk = async () => {
    const errors = await formRef.value?.validate();
    if (errors) {
      return false;
    }
    try {
      if (props.record) {
        await updateSlide(props.record.id, {
          title: form.title,
          group_code: form.group_code,
          image: form.image,
          link: form.link,
          sort: form.sort,
          status: form.status,
          remark: form.remark,
        });
        Message.success(t('system.slide.message.updateSuccess'));
      } else {
        await createSlide({
          title: form.title,
          group_code: form.group_code,
          image: form.image,
          link: form.link,
          sort: form.sort,
          status: form.status,
          remark: form.remark,
        });
        Message.success(t('system.slide.message.createSuccess'));
      }
      emit('saved');
      emit('update:visible', false);
      return true;
    } catch (error) {
      return false;
    }
  };
</script>

<style scoped>
  .slide-image-field {
    width: 100%;
  }

  .slide-image-preview {
    margin: 8px 0;
  }

  .slide-image-preview img {
    max-width: 240px;
    max-height: 120px;
    border-radius: 4px;
    border: 1px solid var(--color-border-2);
  }
</style>
```

- [ ] **Step 2: 创建列表页**

写入 `src/views/system/slide/index.vue`:

```vue
<template>
  <div class="container page-container">
    <a-card class="general-card" :title="$t('menu.system.slide')">
      <!-- 筛选栏 -->
      <a-row style="margin-bottom: 20px">
        <a-col :span="24">
          <a-space wrap>
            <a-select
              v-model="searchForm.group_code"
              :placeholder="$t('system.slide.group.placeholder')"
              style="width: 160px"
              allow-clear
            >
              <a-option
                v-for="opt in groupOptions"
                :key="opt.value"
                :value="opt.value"
                :label="opt.label"
              />
            </a-select>
            <a-select
              v-model="searchForm.status"
              :placeholder="$t('system.slide.status.placeholder')"
              style="width: 130px"
              allow-clear
            >
              <a-option :value="1">{{
                $t('system.slide.status.enabled')
              }}</a-option>
              <a-option :value="0">{{
                $t('system.slide.status.disabled')
              }}</a-option>
            </a-select>
            <a-button type="primary" @click="handleSearch">
              <template #icon><icon-search /></template>
              {{ $t('system.slide.search') }}
            </a-button>
            <a-button @click="handleReset">
              <template #icon><icon-refresh /></template>
              {{ $t('system.slide.reset') }}
            </a-button>
          </a-space>
        </a-col>
      </a-row>

      <!-- 操作栏 -->
      <a-row style="margin-bottom: 12px">
        <a-col :span="24">
          <a-space>
            <a-button
              v-if="canAdd"
              type="primary"
              @click="handleCreate"
            >
              <template #icon><icon-plus /></template>
              {{ $t('system.slide.create') }}
            </a-button>
          </a-space>
        </a-col>
      </a-row>

      <!-- 表格 -->
      <a-table
        :data="slideList"
        :loading="loading"
        :pagination="pagination"
        row-key="id"
        @page-change="handlePageChange"
        @page-size-change="handlePageSizeChange"
      >
        <template #columns>
          <a-table-column title="ID" data-index="id" :width="65" />
          <a-table-column
            :title="$t('system.slide.image')"
            data-index="image"
            :width="120"
          >
            <template #cell="{ record }">
              <a-image
                :src="record.image"
                :alt="record.title"
                width="80"
                height="45"
                fit="cover"
              />
            </template>
          </a-table-column>
          <a-table-column
            :title="$t('system.slide.title')"
            data-index="title"
            :width="160"
            :ellipsis="true"
            :tooltip="true"
          />
          <a-table-column
            :title="$t('system.slide.group')"
            data-index="group_code"
            :width="120"
          />
          <a-table-column
            :title="$t('system.slide.link')"
            data-index="link"
            :ellipsis="true"
            :tooltip="true"
          />
          <a-table-column
            :title="$t('system.slide.sort')"
            data-index="sort"
            :width="80"
            align="center"
          />
          <a-table-column
            :title="$t('system.slide.status')"
            data-index="status"
            :width="90"
            align="center"
          >
            <template #cell="{ record }">
              <a-switch
                v-if="canEdit"
                :model-value="record.status === 1"
                @change="(value: string | number | boolean) => handleToggleStatus(record, value)"
              />
              <a-tag v-else :color="record.status === 1 ? 'green' : 'red'">
                {{
                  record.status === 1
                    ? $t('system.slide.status.enabled')
                    : $t('system.slide.status.disabled')
                }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column
            :title="$t('system.slide.createdAt')"
            data-index="created_at"
            :width="170"
          >
            <template #cell="{ record }">
              {{ formatDateTime(record.created_at) }}
            </template>
          </a-table-column>
          <a-table-column
            :title="$t('system.slide.operations')"
            :width="140"
            fixed="right"
          >
            <template #cell="{ record }">
              <a-space>
                <a-button
                  v-if="canEdit"
                  type="text"
                  size="small"
                  @click="handleEdit(record)"
                >
                  <template #icon><icon-edit /></template>
                  {{ $t('system.slide.edit') }}
                </a-button>
                <a-popconfirm
                  v-if="canDelete"
                  :content="$t('system.slide.deleteConfirm')"
                  type="warning"
                  @ok="handleDelete(record)"
                >
                  <a-button type="text" size="small" status="danger">
                    <template #icon><icon-delete /></template>
                    {{ $t('system.slide.delete') }}
                  </a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </a-card>

    <SlideForm
      v-model:visible="formVisible"
      :record="currentSlide"
      @saved="fetchData"
    />
  </div>
</template>

<script lang="ts" setup>
  import { ref, reactive, onMounted, computed } from 'vue';
  import { Message } from '@arco-design/web-vue';
  import { useI18n } from 'vue-i18n';
  import { getSlides, updateSlide, deleteSlide } from '@/api/slides';
  import { getDictDataByType } from '@/api/dict-data';
  import type { SlideResponse, DictDataResponse } from '@/types/api';
  import { formatDateTime } from '@/utils';
  import usePermission from '@/hooks/permission';
  import SlideForm from './components/SlideForm.vue';

  const SLIDE_GROUP_DICT_TYPE = 'slide_group';

  const { hasButtonPermission } = usePermission();
  const canAdd = computed(() => hasButtonPermission('slide:add'));
  const canEdit = computed(() => hasButtonPermission('slide:edit'));
  const canDelete = computed(() => hasButtonPermission('slide:delete'));

  const { t } = useI18n();

  const loading = ref(false);
  const slideList = ref<SlideResponse[]>([]);
  const groupOptions = ref<DictDataResponse[]>([]);
  const formVisible = ref(false);
  const currentSlide = ref<SlideResponse | null>(null);
  const currentPage = ref(1);
  const pageSize = ref(10);

  const searchForm = reactive({
    group_code: undefined as string | undefined,
    status: undefined as number | undefined,
  });

  const pagination = reactive({
    current: currentPage.value,
    pageSize: pageSize.value,
    total: 0,
    showTotal: true,
    showPageSize: true,
  });

  const loadGroupOptions = async () => {
    const { data: responseData } = await getDictDataByType(
      SLIDE_GROUP_DICT_TYPE
    );
    groupOptions.value = responseData.data || [];
  };

  const fetchData = async () => {
    loading.value = true;
    try {
      const params: Record<string, any> = {
        page: currentPage.value,
        page_size: pageSize.value,
      };
      if (searchForm.group_code) {
        params.group_code = searchForm.group_code;
      }
      if (searchForm.status !== undefined && searchForm.status !== null) {
        params.status = searchForm.status;
      }
      const { data: responseData } = await getSlides(params);
      // 兼容 Swagger 返回的 items 字段和标准 PaginatedResponse 的 data 字段
      const result = responseData.data as any;
      if (result) {
        slideList.value = result.items || result.data || [];
        pagination.total = result.total || 0;
      }
      pagination.current = currentPage.value;
    } finally {
      loading.value = false;
    }
  };

  const handleToggleStatus = async (
    record: SlideResponse,
    value: string | number | boolean
  ) => {
    const nextStatus = value ? 1 : 0;
    await updateSlide(record.id, { status: nextStatus });
    record.status = nextStatus;
    Message.success(
      nextStatus === 1
        ? t('system.slide.message.enableSuccess')
        : t('system.slide.message.disableSuccess')
    );
  };

  const handleSearch = () => {
    currentPage.value = 1;
    fetchData();
  };

  const handleReset = () => {
    searchForm.group_code = undefined;
    searchForm.status = undefined;
    currentPage.value = 1;
    fetchData();
  };

  const handlePageChange = (page: number) => {
    currentPage.value = page;
    fetchData();
  };

  const handlePageSizeChange = (size: number) => {
    pageSize.value = size;
    currentPage.value = 1;
    fetchData();
  };

  const handleCreate = () => {
    currentSlide.value = null;
    formVisible.value = true;
  };

  const handleEdit = (record: SlideResponse) => {
    currentSlide.value = record;
    formVisible.value = true;
  };

  const handleDelete = async (record: SlideResponse) => {
    await deleteSlide(record.id);
    Message.success(t('system.slide.message.deleteSuccess'));
    fetchData();
  };

  onMounted(() => {
    loadGroupOptions();
    fetchData();
  });
</script>

<script lang="ts">
  export default {
    name: 'SystemSlide',
  };
</script>
```

- [ ] **Step 3: 类型检查**

Run(在 `D:\www\admin-web\web` 下):`npm run type:check`
Expected: 退出码 0。

- [ ] **Step 4: Commit(前端仓库)**

```bash
cd D:/www/admin-web/web
git add src/views/system/slide
git commit -m "feat: slide management page with image picker"
```

---

### Task 7: 前端路由 + i18n + 构建验证

**Files:**
- Modify: `D:\www\admin-web\web\src\router\routes\modules\system.ts`
- Create: `D:\www\admin-web\web\src\views\system\slide\locale\zh-CN.ts`
- Create: `D:\www\admin-web\web\src\views\system\slide\locale\en-US.ts`
- Modify: `D:\www\admin-web\web\src\locale\zh-CN.ts`
- Modify: `D:\www\admin-web\web\src\locale\en-US.ts`

**Interfaces:**
- Consumes: Task 6 页面组件路径 `@/views/system/slide/index.vue`。
- Produces: 菜单「系统管理 → 幻灯片管理」(仅持有 `slide` 相关权限或超管可见);i18n 键全集。

- [ ] **Step 1: 注册路由**

在 `src/router/routes/modules/system.ts` 的 `children` 数组中(queue-job 子路由对象之后)追加:

```ts
    {
      path: 'slide',
      name: 'SystemSlide',
      component: () => import('@/views/system/slide/index.vue'),
      meta: {
        locale: 'menu.system.slide',
        requiresAuth: true,
        permissionCode: ['/system/slide', 'system:slide', 'slide'],
      },
    },
```

(permissionCode 三项式参照 department 的写法:menu_path + 完整 code + 去前缀别名,命中任意即放行。)

- [ ] **Step 2: 视图级 locale**

写入 `src/views/system/slide/locale/zh-CN.ts`:

```ts
export default {
  'menu.system.slide': '幻灯片管理',
  'system.slide.title': '标题',
  'system.slide.title.placeholder': '请输入标题',
  'system.slide.title.required': '标题不能为空',
  'system.slide.group': '分组',
  'system.slide.group.placeholder': '请选择分组(字典类型 slide_group)',
  'system.slide.group.required': '分组不能为空',
  'system.slide.image': '图片',
  'system.slide.image.placeholder': '请输入图片URL或从附件库选择',
  'system.slide.image.required': '图片不能为空',
  'system.slide.link': '跳转链接',
  'system.slide.link.placeholder': '请输入点击跳转地址(可选)',
  'system.slide.sort': '排序',
  'system.slide.status': '状态',
  'system.slide.status.placeholder': '请选择状态',
  'system.slide.status.enabled': '启用',
  'system.slide.status.disabled': '禁用',
  'system.slide.remark': '备注',
  'system.slide.remark.placeholder': '请输入备注(可选)',
  'system.slide.createdAt': '创建时间',
  'system.slide.search': '搜索',
  'system.slide.reset': '重置',
  'system.slide.create': '新增幻灯片',
  'system.slide.edit': '编辑',
  'system.slide.delete': '删除',
  'system.slide.deleteConfirm': '确认删除该幻灯片吗?',
  'system.slide.operations': '操作',
  'system.slide.form.createTitle': '新增幻灯片',
  'system.slide.form.editTitle': '编辑幻灯片',
  'system.slide.message.createSuccess': '幻灯片创建成功',
  'system.slide.message.updateSuccess': '幻灯片更新成功',
  'system.slide.message.deleteSuccess': '幻灯片删除成功',
  'system.slide.message.enableSuccess': '已启用',
  'system.slide.message.disableSuccess': '已禁用',
};
```

写入 `src/views/system/slide/locale/en-US.ts`:

```ts
export default {
  'menu.system.slide': 'Slides',
  'system.slide.title': 'Title',
  'system.slide.title.placeholder': 'Enter title',
  'system.slide.title.required': 'Title is required',
  'system.slide.group': 'Group',
  'system.slide.group.placeholder': 'Select group (dict type slide_group)',
  'system.slide.group.required': 'Group is required',
  'system.slide.image': 'Image',
  'system.slide.image.placeholder': 'Enter image URL or pick from attachments',
  'system.slide.image.required': 'Image is required',
  'system.slide.link': 'Link',
  'system.slide.link.placeholder': 'Enter target URL (optional)',
  'system.slide.sort': 'Sort',
  'system.slide.status': 'Status',
  'system.slide.status.placeholder': 'Select status',
  'system.slide.status.enabled': 'Enabled',
  'system.slide.status.disabled': 'Disabled',
  'system.slide.remark': 'Remark',
  'system.slide.remark.placeholder': 'Enter remark (optional)',
  'system.slide.createdAt': 'Created At',
  'system.slide.search': 'Search',
  'system.slide.reset': 'Reset',
  'system.slide.create': 'New Slide',
  'system.slide.edit': 'Edit',
  'system.slide.delete': 'Delete',
  'system.slide.deleteConfirm': 'Delete this slide?',
  'system.slide.operations': 'Actions',
  'system.slide.form.createTitle': 'New Slide',
  'system.slide.form.editTitle': 'Edit Slide',
  'system.slide.message.createSuccess': 'Slide created',
  'system.slide.message.updateSuccess': 'Slide updated',
  'system.slide.message.deleteSuccess': 'Slide deleted',
  'system.slide.message.enableSuccess': 'Enabled',
  'system.slide.message.disableSuccess': 'Disabled',
};
```

- [ ] **Step 3: 汇入全局 locale**

`src/locale/zh-CN.ts`:在 `import localeSystemQueueJob from '@/views/system/queue-job/locale/zh-CN';` 之后加:

```ts
import localeSystemSlide from '@/views/system/slide/locale/zh-CN';
```

在 export default 对象内 `...localeSystemQueueJob,` 之后加:

```ts
  ...localeSystemSlide,
```

`src/locale/en-US.ts`:同样两处(import 路径换成 `.../locale/en-US`;spread 行相同)。

- [ ] **Step 4: 类型检查 + 构建**

Run(在 `D:\www\admin-web\web` 下):`npm run type:check && npm run build`
Expected: type:check 退出码 0;build 正常产出 `dist/index.html` + `dist/assets/`(压缩插件日志里 `dist/D:/...` 行是噪音,忽略)。

- [ ] **Step 5: Commit(前端仓库)**

```bash
cd D:/www/admin-web/web
git add src/router/routes/modules/system.ts src/views/system/slide/locale src/locale/zh-CN.ts src/locale/en-US.ts
git commit -m "feat: register slide route and i18n"
```

---

### Task 8: 端到端手工验收(可选但推荐)

**Files:** 无新文件;验证产物。

**Interfaces:**
- Consumes: 前面全部任务的产出。

- [ ] **Step 1: 启动后端**

Run(在 `D:\www\admin` 下):`go run main.go`
Expected: 服务起在 9501;日志出现幻灯片权限种子与 `slide_group` 字典项创建(或 skip)。

- [ ] **Step 2: 部署前端 dist**

Run: `rm -rf D:/www/admin/web/dist && mv D:/www/admin-web/web/dist D:/www/admin/web/dist`
Expected: 后端内嵌 dist 被替换。

- [ ] **Step 3: 验收清单**

1. 超管登录 admin 端 → 系统管理 → 幻灯片管理:列表空、可新增(分组下拉出现「首页 banner」;附件选图回填 URL 预览)。
2. 新增两条 home 分组、一条禁用;公开接口 `curl "http://127.0.0.1:9501/api/v1/public/slides?group=home"` 仅返回启用项、按 sort 升序、字段只有 id/title/image/link/group_code。
3. 不带 group 调用返回全部分组启用项;空分组返回 `"data":[]`。
4. 给普通角色只授部分 slide 权限,验证菜单/按钮/API 的 Casbin 生效;未授权管理员访问 `/system/slide` 应 403(与 queue 页面行为一致)。

---

## Self-Review 记录(已执行)

- **规格覆盖:** 数据模型(Task 1)、管理端 CRUD(Task 2/3/4)、公开接口含 group 过滤与安全字段(Task 2/3/4)、权限种子 192–200(Task 4)、字典预置(Task 4)、前端页面/表单/路由/i18n(Task 5–7)、图片 URL 约定(Task 6 表单注释 + Global Constraints)、错误处理与测试(Task 2/3)——均有对应任务。
- **占位符:** 无 TBD/TODO;Task 6 两处"注意"是明确的仓库现状适配指引,不是占位。
- **类型一致性:** service 方法签名与 handler/路由/前端调用逐一对齐;`slide:delete-api`(API)与 `slide:delete`(按钮)区分沿用用户管理先例;`setupTestSlideDB`/`mustCreateSlideForTest` 等测试助手命名无冲突;复用 `dict_data_test.go` 的 `intPtr` 未重复定义。

package service

import (
	"testing"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func TestSlideService_LocalUploadImageUsesRelativePath(t *testing.T) {
	svc, cleanup := setupTestSlideDB(t)
	defer cleanup()
	ctx := t.Context()

	created, err := svc.Create(ctx, &CreateSlideRequest{
		Title:     "首页横幅",
		Image:     "http://127.0.0.1:9501/uploads/slides/banner.png",
		GroupCode: "home",
	})
	if err != nil {
		t.Fatalf("创建幻灯片失败: %v", err)
	}
	if created.Image != "/uploads/slides/banner.png" {
		t.Fatalf("创建响应图片地址 = %q", created.Image)
	}

	newImage := "https://example.com/uploads/slides/next.png"
	if err := svc.Update(ctx, created.ID, &UpdateSlideRequest{Image: &newImage}); err != nil {
		t.Fatalf("更新幻灯片失败: %v", err)
	}
	stored, err := svc.Client.Slide.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取幻灯片失败: %v", err)
	}
	if stored.Image != "/uploads/slides/next.png" {
		t.Fatalf("数据库图片地址 = %q", stored.Image)
	}
	public, err := svc.ListPublic(ctx, "home")
	if err != nil || len(public) != 1 || public[0].Image != stored.Image {
		t.Fatalf("公开响应图片地址不一致: items=%+v, err=%v", public, err)
	}
}

func TestNormalizeSlideImageLeavesExternalURL(t *testing.T) {
	for _, image := range []string{
		"https://cdn.example.com/banner.png",
		"https://cdn.example.com/uploads/banner.png?signature=abc",
		"/uploads/slides/banner.png",
	} {
		if got := normalizeSlideImage(image); got != image {
			t.Errorf("normalizeSlideImage(%q) = %q", image, got)
		}
	}
}

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

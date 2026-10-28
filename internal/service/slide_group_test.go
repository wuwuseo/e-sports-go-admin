package service

import (
	"errors"
	"testing"
)

func TestSlideGroupService_CRUD(t *testing.T) {
	slideSvc, cleanup := setupTestSlideDB(t)
	defer cleanup()
	svc := NewSlideGroupService()
	ctx := t.Context()

	created, err := svc.Create(ctx, &CreateSlideGroupRequest{Label: "首页横幅", Value: "home"})
	if err != nil {
		t.Fatalf("创建分组失败: %v", err)
	}
	if created.Sort != 0 || created.Status != 1 || created.SlideCount != 0 {
		t.Fatalf("分组默认值不正确: %+v", created)
	}

	other, err := svc.Create(ctx, &CreateSlideGroupRequest{Label: "活动位", Value: "activity"})
	if err != nil {
		t.Fatalf("创建第二个分组失败: %v", err)
	}
	if other.Sort != 10 {
		t.Fatalf("未指定排序时应追加到末尾, sort = %d", other.Sort)
	}

	// 分组编码同类型下唯一
	if _, err := svc.Create(ctx, &CreateSlideGroupRequest{Label: "重复", Value: "home"}); !errors.Is(err, ErrSlideGroupValueExists) {
		t.Fatalf("重复编码应返回 ErrSlideGroupValueExists, 实际: %v", err)
	}

	// 幻灯片引用后 count 递增，且不允许删组、不允许改编码
	if _, err := slideSvc.Create(ctx, &CreateSlideRequest{Title: "开业海报", Image: "/uploads/slides/a.png", GroupCode: "home"}); err != nil {
		t.Fatalf("创建幻灯片失败: %v", err)
	}
	items, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("查询分组失败: %v", err)
	}
	if len(items) != 2 || items[0].Value != "home" || items[0].SlideCount != 1 || items[1].SlideCount != 0 {
		t.Fatalf("分组列表不符合预期: %+v", items)
	}

	if err := svc.Delete(ctx, created.ID); !errors.Is(err, ErrSlideGroupInUse) {
		t.Fatalf("删除被引用分组应返回 ErrSlideGroupInUse, 实际: %v", err)
	}
	newValue := "homepage"
	if _, err := svc.Update(ctx, created.ID, &UpdateSlideGroupRequest{Value: &newValue}); !errors.Is(err, ErrSlideGroupValueInUse) {
		t.Fatalf("改被引用分组的编码应被拒绝, 实际: %v", err)
	}

	// 标签/状态可改，编码不变
	newLabel := "首页轮播"
	disabled := 0
	updated, err := svc.Update(ctx, created.ID, &UpdateSlideGroupRequest{Label: &newLabel, Status: &disabled})
	if err != nil {
		t.Fatalf("更新分组失败: %v", err)
	}
	if updated.Label != "首页轮播" || updated.Status != 0 || updated.Value != "home" || updated.SlideCount != 1 {
		t.Fatalf("分组更新结果不符合预期: %+v", updated)
	}

	// 空分组可直接删除
	if err := svc.Delete(ctx, other.ID); err != nil {
		t.Fatalf("删除空分组失败: %v", err)
	}
	items, err = svc.List(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("删除后分组列表应有 1 项: items=%+v, err=%v", items, err)
	}

	// 不存在的分组
	if _, err := svc.Update(ctx, 99999, &UpdateSlideGroupRequest{Label: &newLabel}); !errors.Is(err, ErrSlideGroupNotFound) {
		t.Fatalf("更新不存在分组应返回 ErrSlideGroupNotFound, 实际: %v", err)
	}
	if err := svc.Delete(ctx, 99999); !errors.Is(err, ErrSlideGroupNotFound) {
		t.Fatalf("删除不存在分组应返回 ErrSlideGroupNotFound, 实际: %v", err)
	}
}

func TestSlideGroupService_RejectsInvalidValue(t *testing.T) {
	_, cleanup := setupTestSlideDB(t)
	defer cleanup()
	svc := NewSlideGroupService()
	ctx := t.Context()

	for _, value := range []string{"1home", "首页", "home group", "home.group"} {
		if _, err := svc.Create(ctx, &CreateSlideGroupRequest{Label: "非法", Value: value}); !errors.Is(err, ErrSlideGroupValueInvalid) {
			t.Errorf("分组编码 %q 应被拒绝, 实际: %v", value, err)
		}
	}

	created, err := svc.Create(ctx, &CreateSlideGroupRequest{Label: "合法", Value: "home_page-2"})
	if err != nil {
		t.Fatalf("合法编码应通过: %v", err)
	}
	invalid := "2bad"
	if _, err := svc.Update(ctx, created.ID, &UpdateSlideGroupRequest{Value: &invalid}); !errors.Is(err, ErrSlideGroupValueInvalid) {
		t.Fatalf("更新为非法编码应被拒绝, 实际: %v", err)
	}
}

// 分组数据源是数据字典 slide_group，幻灯片表单的分组下拉走同一条字典查询，
// 因此分组必须在 GetByType 结果里出现，否则新建分组选不到。
func TestSlideGroupService_AppearsInDictDataByType(t *testing.T) {
	_, cleanup := setupTestSlideDB(t)
	defer cleanup()
	svc := NewSlideGroupService()
	ctx := t.Context()

	created, err := svc.Create(ctx, &CreateSlideGroupRequest{Label: "首页横幅", Value: "home"})
	if err != nil {
		t.Fatalf("创建分组失败: %v", err)
	}

	dictSvc := NewDictDataService()
	items, err := dictSvc.GetByType(ctx, SlideGroupDictType)
	if err != nil {
		t.Fatalf("按类型查询字典失败: %v", err)
	}
	found := false
	for _, item := range items {
		if item.ID == created.ID && item.Value == "home" && item.Label == "首页横幅" {
			found = true
		}
	}
	if !found {
		t.Fatalf("新建分组未出现在 slide_group 字典中: %+v", items)
	}
}

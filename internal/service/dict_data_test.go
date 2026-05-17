package service

import (
	"testing"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) (*DictDataService, func()) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)

	prevORM := global.ORM
	global.ORM = client

	svc := NewDictDataService()
	cleanup := func() {
		global.ORM = prevORM
		client.Close()
	}
	return svc, cleanup
}

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

func TestDictDataService_Create_Success(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	req := &CreateDictDataRequest{
		Label:    "男",
		Value:    "1",
		DictType: "sys_user_sex",
	}
	resp, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatalf("创建字典数据失败: %v", err)
	}
	if resp.ID != 1 {
		t.Errorf("期望 ID=1, 实际 ID=%d", resp.ID)
	}
	if resp.Label != "男" {
		t.Errorf("期望 Label='男', 实际 Label='%s'", resp.Label)
	}
	if resp.Value != "1" {
		t.Errorf("期望 Value='1', 实际 Value='%s'", resp.Value)
	}
	if resp.DictType != "sys_user_sex" {
		t.Errorf("期望 DictType='sys_user_sex', 实际 DictType='%s'", resp.DictType)
	}
	if resp.Sort != 0 {
		t.Errorf("期望 Sort=0 (默认值), 实际 Sort=%d", resp.Sort)
	}
	if resp.Status != 1 {
		t.Errorf("期望 Status=1 (默认值), 实际 Status=%d", resp.Status)
	}
	if resp.IsDefault != false {
		t.Errorf("期望 IsDefault=false (默认值), 实际 IsDefault=%v", resp.IsDefault)
	}
	if resp.CSSClass != "" {
		t.Errorf("期望 CSSClass='', 实际 CSSClass='%s'", resp.CSSClass)
	}
	if resp.Remark != "" {
		t.Errorf("期望 Remark='', 实际 Remark='%s'", resp.Remark)
	}
}

func TestDictDataService_Create_AllOptionalFields(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	sortVal := 99
	statusVal := 0
	isDefaultVal := true
	req := &CreateDictDataRequest{
		Label:     "女",
		Value:     "2",
		DictType:  "sys_user_sex",
		Sort:      &sortVal,
		Status:    &statusVal,
		CSSClass:  "text-pink",
		IsDefault: &isDefaultVal,
		Remark:    "性别为女",
	}
	resp, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatalf("创建字典数据(带可选字段)失败: %v", err)
	}
	if resp.Sort != 99 {
		t.Errorf("期望 Sort=99, 实际 Sort=%d", resp.Sort)
	}
	if resp.Status != 0 {
		t.Errorf("期望 Status=0, 实际 Status=%d", resp.Status)
	}
	if resp.CSSClass != "text-pink" {
		t.Errorf("期望 CSSClass='text-pink', 实际 CSSClass='%s'", resp.CSSClass)
	}
	if resp.IsDefault != true {
		t.Errorf("期望 IsDefault=true, 实际 IsDefault=%v", resp.IsDefault)
	}
	if resp.Remark != "性别为女" {
		t.Errorf("期望 Remark='性别为女', 实际 Remark='%s'", resp.Remark)
	}
}

func TestDictDataService_Get_Found(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	created, err := svc.Create(ctx, &CreateDictDataRequest{
		Label:    "启用",
		Value:    "1",
		DictType: "sys_normal_disable",
	})
	if err != nil {
		t.Fatalf("创建字典数据失败: %v", err)
	}

	resp, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("获取字典数据失败: %v", err)
	}
	if resp.ID != created.ID {
		t.Errorf("期望 ID=%d, 实际 ID=%d", created.ID, resp.ID)
	}
	if resp.Label != "启用" {
		t.Errorf("期望 Label='启用', 实际 Label='%s'", resp.Label)
	}
}

func TestDictDataService_Get_NotFound(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	_, err := svc.Get(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误, 实际返回 nil")
	}
	if err != ErrDictDataNotFound {
		t.Errorf("期望 ErrDictDataNotFound, 实际错误: %v", err)
	}
}

func TestDictDataService_List_NoFilters(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	// 创建5条数据
	for i := range 5 {
		req := &CreateDictDataRequest{
			Label:    "选项" + string(rune('A'+i)),
			Value:    string(rune('0' + i)),
			DictType: "sys_notice_type",
		}
		if _, err := svc.Create(ctx, req); err != nil {
			t.Fatalf("创建第%d条数据失败: %v", i+1, err)
		}
	}

	// 第一页，每页2条
	resp, err := svc.List(ctx, &ListDictDataRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 2},
		},
	})
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if resp.Total != 5 {
		t.Errorf("期望 Total=5, 实际 Total=%d", resp.Total)
	}
	if resp.Page != 1 {
		t.Errorf("期望 Page=1, 实际 Page=%d", resp.Page)
	}
	if len(resp.Data) != 2 {
		t.Errorf("期望第一页返回2条, 实际返回 %d 条", len(resp.Data))
	}

	// 第三页，每页2条，应该只有1条
	resp, err = svc.List(ctx, &ListDictDataRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 3, PageSize: 2},
		},
	})
	if err != nil {
		t.Fatalf("第三页列表查询失败: %v", err)
	}
	if resp.Total != 5 {
		t.Errorf("期望 Total=5, 实际 Total=%d", resp.Total)
	}
	if len(resp.Data) != 1 {
		t.Errorf("期望第三页返回1条, 实际返回 %d 条", len(resp.Data))
	}
}

func TestDictDataService_List_WithKeyword(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	_, err := svc.Create(ctx, &CreateDictDataRequest{
		Label:    "系统通知",
		Value:    "1",
		DictType: "sys_notice_type",
	})
	if err != nil {
		t.Fatalf("创建数据失败: %v", err)
	}
	_, err = svc.Create(ctx, &CreateDictDataRequest{
		Label:    "告警通知",
		Value:    "2",
		DictType: "sys_notice_type",
	})
	if err != nil {
		t.Fatalf("创建数据失败: %v", err)
	}
	_, err = svc.Create(ctx, &CreateDictDataRequest{
		Label:    "其他类型",
		Value:    "other",
		DictType: "other_type",
	})
	if err != nil {
		t.Fatalf("创建数据失败: %v", err)
	}

	// 按关键字"通知"搜索
	resp, err := svc.List(ctx, &ListDictDataRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
			Keyword:           "通知",
		},
	})
	if err != nil {
		t.Fatalf("关键字列表查询失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望 Total=2 (Label匹配), 实际 Total=%d", resp.Total)
	}

	// 按Value搜索
	resp, err = svc.List(ctx, &ListDictDataRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
			Keyword:           "other",
		},
	})
	if err != nil {
		t.Fatalf("按Value关键字查询失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望 Total=1 (Value匹配), 实际 Total=%d", resp.Total)
	}
}

func TestDictDataService_List_WithDictType(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	for i := range 3 {
		_, err := svc.Create(ctx, &CreateDictDataRequest{
			Label:    "类型A-" + string(rune('A'+i)),
			Value:    string(rune('0' + i)),
			DictType: "type_a",
		})
		if err != nil {
			t.Fatalf("创建type_a数据失败: %v", err)
		}
	}
	for i := range 2 {
		_, err := svc.Create(ctx, &CreateDictDataRequest{
			Label:    "类型B-" + string(rune('A'+i)),
			Value:    string(rune('0' + i)),
			DictType: "type_b",
		})
		if err != nil {
			t.Fatalf("创建type_b数据失败: %v", err)
		}
	}

	resp, err := svc.List(ctx, &ListDictDataRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
		},
		DictType: "type_a",
	})
	if err != nil {
		t.Fatalf("按DictType查询失败: %v", err)
	}
	if resp.Total != 3 {
		t.Errorf("期望 Total=3 (仅type_a), 实际 Total=%d", resp.Total)
	}

	resp, err = svc.List(ctx, &ListDictDataRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
		},
		DictType: "type_b",
	})
	if err != nil {
		t.Fatalf("按DictType=type_b查询失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望 Total=2 (仅type_b), 实际 Total=%d", resp.Total)
	}
}

func TestDictDataService_List_WithStatus(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	for i := range 3 {
		statusVal := 1
		if i == 2 {
			statusVal = 0
		}
		_, err := svc.Create(ctx, &CreateDictDataRequest{
			Label:    "状态测试" + string(rune('A'+i)),
			Value:    string(rune('0' + i)),
			DictType: "status_test",
			Status:   &statusVal,
		})
		if err != nil {
			t.Fatalf("创建状态测试数据失败: %v", err)
		}
	}

	// 查启用状态
	resp, err := svc.List(ctx, &ListDictDataRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
		},
		Status: intPtr(1),
	})
	if err != nil {
		t.Fatalf("按启用状态查询失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望 Total=2 (启用状态), 实际 Total=%d", resp.Total)
	}

	// 查禁用状态
	resp, err = svc.List(ctx, &ListDictDataRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
		},
		Status: intPtr(0),
	})
	if err != nil {
		t.Fatalf("按禁用状态查询失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望 Total=1 (禁用状态), 实际 Total=%d", resp.Total)
	}
}

func TestDictDataService_List_PageSizeZeroNoPagination(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	for i := range 5 {
		_, err := svc.Create(ctx, &CreateDictDataRequest{
			Label:    "不分页测试" + string(rune('A'+i)),
			Value:    string(rune('0' + i)),
			DictType: "no_page_test",
		})
		if err != nil {
			t.Fatalf("创建测试数据失败: %v", err)
		}
	}

	resp, err := svc.List(ctx, &ListDictDataRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 0},
		},
	})
	if err != nil {
		t.Fatalf("不分页查询失败: %v", err)
	}
	if len(resp.Data) != 5 {
		t.Errorf("期望返回全部5条数据, 实际返回 %d 条", len(resp.Data))
	}
}

func TestDictDataService_GetByType_OnlyEnabledItems(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	enabledStatus := 1
	disabledStatus := 0

	_, err := svc.Create(ctx, &CreateDictDataRequest{
		Label:    "启用项1",
		Value:    "enabled_1",
		DictType: "test_type",
		Status:   &enabledStatus,
		Sort:     intPtr(100),
	})
	if err != nil {
		t.Fatalf("创建启用项1失败: %v", err)
	}
	_, err = svc.Create(ctx, &CreateDictDataRequest{
		Label:    "禁用项",
		Value:    "disabled",
		DictType: "test_type",
		Status:   &disabledStatus,
	})
	if err != nil {
		t.Fatalf("创建禁用项失败: %v", err)
	}
	_, err = svc.Create(ctx, &CreateDictDataRequest{
		Label:    "启用项2",
		Value:    "enabled_2",
		DictType: "test_type",
		Status:   &enabledStatus,
		Sort:     intPtr(50),
	})
	if err != nil {
		t.Fatalf("创建启用项2失败: %v", err)
	}

	items, err := svc.GetByType(ctx, "test_type")
	if err != nil {
		t.Fatalf("按类型查询失败: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("期望返回2条启用项, 实际返回 %d 条", len(items))
	}
	for _, item := range items {
		if item.Status != 1 {
			t.Errorf("期望所有返回项 Status=1, 实际存在 Status=%d", item.Status)
		}
		if item.DictType != "test_type" {
			t.Errorf("期望 DictType='test_type', 实际 DictType='%s'", item.DictType)
		}
	}
	// 按Sort升序，Label升序，Sort=50 的应该在前
	if items[0].Value != "enabled_2" {
		t.Errorf("期望第一项 Value='enabled_2' (Sort=50), 实际 Value='%s'", items[0].Value)
	}
	if items[1].Value != "enabled_1" {
		t.Errorf("期望第二项 Value='enabled_1' (Sort=100), 实际 Value='%s'", items[1].Value)
	}
}

func TestDictDataService_GetByType_EmptyResult(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	items, err := svc.GetByType(ctx, "non_existent_type")
	if err != nil {
		t.Fatalf("查询不存在的类型失败: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("期望返回0条数据, 实际返回 %d 条", len(items))
	}
}

func TestDictDataService_Update_Success(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	created, err := svc.Create(ctx, &CreateDictDataRequest{
		Label:    "原始标签",
		Value:    "original",
		DictType: "original_type",
	})
	if err != nil {
		t.Fatalf("创建测试数据失败: %v", err)
	}

	newLabel := "更新标签"
	newValue := "updated"
	newDictType := "updated_type"
	err = svc.Update(ctx, created.ID, &UpdateDictDataRequest{
		Label:    &newLabel,
		Value:    &newValue,
		DictType: &newDictType,
	})
	if err != nil {
		t.Fatalf("更新字典数据失败: %v", err)
	}

	// 验证更新结果
	resp, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("获取更新后数据失败: %v", err)
	}
	if resp.Label != "更新标签" {
		t.Errorf("期望 Label='更新标签', 实际 Label='%s'", resp.Label)
	}
	if resp.Value != "updated" {
		t.Errorf("期望 Value='updated', 实际 Value='%s'", resp.Value)
	}
	if resp.DictType != "updated_type" {
		t.Errorf("期望 DictType='updated_type', 实际 DictType='%s'", resp.DictType)
	}
}

func TestDictDataService_Update_NotFound(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	newLabel := "不存在"
	err := svc.Update(ctx, 99999, &UpdateDictDataRequest{
		Label: &newLabel,
	})
	if err == nil {
		t.Fatal("期望返回错误, 实际返回 nil")
	}
	if err != ErrDictDataNotFound {
		t.Errorf("期望 ErrDictDataNotFound, 实际错误: %v", err)
	}
}

func TestDictDataService_Delete_Success(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	created, err := svc.Create(ctx, &CreateDictDataRequest{
		Label:    "待删除",
		Value:    "to_delete",
		DictType: "delete_test",
	})
	if err != nil {
		t.Fatalf("创建测试数据失败: %v", err)
	}

	err = svc.Delete(ctx, created.ID)
	if err != nil {
		t.Fatalf("删除字典数据失败: %v", err)
	}

	// 验证已删除
	_, err = svc.Get(ctx, created.ID)
	if err != ErrDictDataNotFound {
		t.Errorf("期望 ErrDictDataNotFound, 实际错误: %v", err)
	}
}

func TestDictDataService_Delete_NotFound(t *testing.T) {
	svc, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()

	err := svc.Delete(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误, 实际返回 nil")
	}
	if err != ErrDictDataNotFound {
		t.Errorf("期望 ErrDictDataNotFound, 实际错误: %v", err)
	}
}

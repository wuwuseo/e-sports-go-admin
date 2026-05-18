package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/attachmentgroup"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupAttachmentGroupTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

func createTestAttachmentGroup(t *testing.T, client *ent.Client, name string, sort int) *ent.AttachmentGroup {
	ctx := t.Context()
	ag, err := client.AttachmentGroup.Create().
		SetName(name).
		SetSort(sort).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试附件分组失败: %v", err)
	}
	return ag
}

// ======================== Create 测试 ========================

func TestAttachmentGroupService_Create_Success(t *testing.T) {
	setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	req := &CreateGroupRequest{
		Name:        "测试分组",
		Description: "这是一个测试分组",
		Sort:        1,
	}

	resp, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatalf("创建附件分组失败: %v", err)
	}
	if resp.Name != "测试分组" {
		t.Errorf("期望名称为 测试分组, 实际为 %s", resp.Name)
	}
	if resp.Description != "这是一个测试分组" {
		t.Errorf("期望描述为 这是一个测试分组, 实际为 %s", resp.Description)
	}
	if resp.Sort != 1 {
		t.Errorf("期望排序为 1, 实际为 %d", resp.Sort)
	}
}

func TestAttachmentGroupService_Create_NameAlreadyExists(t *testing.T) {
	client := setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	createTestAttachmentGroup(t, client, "重复名称分组", 1)

	req := &CreateGroupRequest{
		Name: "重复名称分组",
	}

	_, err := svc.Create(ctx, req)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrAttachmentGroupExists) {
		t.Errorf("期望错误为 ErrAttachmentGroupExists, 实际错误: %v", err)
	}
}

// ======================== Update 测试 ========================

func TestAttachmentGroupService_Update_Success(t *testing.T) {
	client := setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	ag := createTestAttachmentGroup(t, client, "旧名称", 2)

	updateReq := &UpdateGroupRequest{
		Name:        "新名称",
		Description: "新描述",
		Sort:        &[]int{10}[0],
	}

	err := svc.Update(ctx, ag.ID, updateReq)
	if err != nil {
		t.Fatalf("更新附件分组失败: %v", err)
	}

	updated, err := client.AttachmentGroup.Get(ctx, ag.ID)
	if err != nil {
		t.Fatalf("查询更新后分组失败: %v", err)
	}
	if updated.Name != "新名称" {
		t.Errorf("期望名称为 新名称, 实际为 %s", updated.Name)
	}
	if updated.Description != "新描述" {
		t.Errorf("期望描述为 新描述, 实际为 %s", updated.Description)
	}
	if updated.Sort != 10 {
		t.Errorf("期望排序为 10, 实际为 %d", updated.Sort)
	}
}

func TestAttachmentGroupService_Update_NameConflictWithOther(t *testing.T) {
	client := setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	createTestAttachmentGroup(t, client, "已存在分组", 1)
	ag2 := createTestAttachmentGroup(t, client, "待更新分组", 2)

	updateReq := &UpdateGroupRequest{
		Name: "已存在分组",
	}

	err := svc.Update(ctx, ag2.ID, updateReq)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrAttachmentGroupExists) {
		t.Errorf("期望错误为 ErrAttachmentGroupExists, 实际错误: %v", err)
	}
}

func TestAttachmentGroupService_Update_GroupNotFound(t *testing.T) {
	setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	updateReq := &UpdateGroupRequest{
		Name: "不会存在",
	}

	err := svc.Update(ctx, 99999, updateReq)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrAttachmentGroupNotFound) {
		t.Errorf("期望错误为 ErrAttachmentGroupNotFound, 实际错误: %v", err)
	}
}

func TestAttachmentGroupService_Update_AllowSameName(t *testing.T) {
	client := setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	ag := createTestAttachmentGroup(t, client, "保持不变", 5)

	updateReq := &UpdateGroupRequest{
		Name:        "保持不变", // 相同名称应该允许
		Description: "更新描述",
	}

	err := svc.Update(ctx, ag.ID, updateReq)
	if err != nil {
		t.Fatalf("更新同名附件分组失败: %v", err)
	}
}

// ======================== Delete 测试 ========================

func TestAttachmentGroupService_Delete_Success(t *testing.T) {
	client := setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	ag := createTestAttachmentGroup(t, client, "待删除分组", 1)

	err := svc.Delete(ctx, ag.ID)
	if err != nil {
		t.Fatalf("删除附件分组失败: %v", err)
	}

	exists, err := client.AttachmentGroup.Query().
		Where(attachmentgroup.IDEQ(ag.ID)).
		Exist(ctx)
	if err != nil {
		t.Fatalf("检查分组存在性失败: %v", err)
	}
	if exists {
		t.Error("分组应该已被删除，但仍然存在")
	}
}

func TestAttachmentGroupService_Delete_GroupNotFound(t *testing.T) {
	setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	err := svc.Delete(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrAttachmentGroupNotFound) {
		t.Errorf("期望错误为 ErrAttachmentGroupNotFound, 实际错误: %v", err)
	}
}

func TestAttachmentGroupService_Delete_GroupHasAttachments(t *testing.T) {
	client := setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	ag := createTestAttachmentGroup(t, client, "有附件的分组", 1)

	// 创建一个附件关联到此分组
	_, err := client.Attachment.Create().
		SetName("test.txt").
		SetOriginalName("test.txt").
		SetPath("/path/to/test.txt").
		SetMimeType("text/plain").
		SetExtension("txt").
		SetSize(100).
		SetGroupID(ag.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试附件失败: %v", err)
	}

	err = svc.Delete(ctx, ag.ID)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrAttachmentGroupNotEmpty) {
		t.Errorf("期望错误为 ErrAttachmentGroupNotEmpty, 实际错误: %v", err)
	}
}

// ======================== Get 测试 ========================

func TestAttachmentGroupService_Get_Found(t *testing.T) {
	client := setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	ag := createTestAttachmentGroup(t, client, "获取分组", 5)

	resp, err := svc.Get(ctx, ag.ID)
	if err != nil {
		t.Fatalf("获取附件分组失败: %v", err)
	}
	if resp.ID != ag.ID {
		t.Errorf("期望ID为 %d, 实际为 %d", ag.ID, resp.ID)
	}
	if resp.Name != "获取分组" {
		t.Errorf("期望名称为 获取分组, 实际为 %s", resp.Name)
	}
	if resp.Sort != 5 {
		t.Errorf("期望排序为 5, 实际为 %d", resp.Sort)
	}
}

func TestAttachmentGroupService_Get_NotFound(t *testing.T) {
	setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	_, err := svc.Get(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrAttachmentGroupNotFound) {
		t.Errorf("期望错误为 ErrAttachmentGroupNotFound, 实际错误: %v", err)
	}
}

// ======================== List 测试 ========================

func TestAttachmentGroupService_List_Empty(t *testing.T) {
	setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	req := &ListGroupsRequest{
		Page:     1,
		PageSize: 10,
	}

	resp, err := svc.List(ctx, req)
	if err != nil {
		t.Fatalf("获取附件分组列表失败: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("期望总数为 0, 实际为 %d", resp.Total)
	}
	if resp.Page != 1 {
		t.Errorf("期望页码为 1, 实际为 %d", resp.Page)
	}
	if len(resp.Data) != 0 {
		t.Errorf("期望数据长度为 0, 实际为 %d", len(resp.Data))
	}
}

func TestAttachmentGroupService_List_ReturnsAll(t *testing.T) {
	client := setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	createTestAttachmentGroup(t, client, "分组1", 2)
	createTestAttachmentGroup(t, client, "分组2", 1)

	req := &ListGroupsRequest{
		Page:     1,
		PageSize: 10,
	}

	resp, err := svc.List(ctx, req)
	if err != nil {
		t.Fatalf("获取附件分组列表失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望总数为 2, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 2 {
		t.Errorf("期望数据长度为 2, 实际为 %d", len(resp.Data))
	}
}

func TestAttachmentGroupService_List_KeywordSearch(t *testing.T) {
	client := setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	createTestAttachmentGroup(t, client, "文档分组", 1)
	createTestAttachmentGroup(t, client, "图片分组", 2)

	req := &ListGroupsRequest{
		Page:     1,
		PageSize: 10,
		Keyword:  "文档",
	}

	resp, err := svc.List(ctx, req)
	if err != nil {
		t.Fatalf("按关键字搜索失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望总数为 1, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 1 || resp.Data[0].Name != "文档分组" {
		t.Error("搜索结果不正确")
	}
}

func TestAttachmentGroupService_List_Pagination(t *testing.T) {
	client := setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	for i := 1; i <= 5; i++ {
		createTestAttachmentGroup(t, client, "分页分组"+string(rune('0'+i)), i)
	}

	// 第一页 2 条
	resp1, err := svc.List(ctx, &ListGroupsRequest{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("获取第一页失败: %v", err)
	}
	if resp1.Total != 5 {
		t.Errorf("期望总数为 5, 实际为 %d", resp1.Total)
	}
	if len(resp1.Data) != 2 {
		t.Errorf("期望第一页返回 2 条, 实际为 %d", len(resp1.Data))
	}

	// 第二页 2 条
	resp2, err := svc.List(ctx, &ListGroupsRequest{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatalf("获取第二页失败: %v", err)
	}
	if len(resp2.Data) != 2 {
		t.Errorf("期望第二页返回 2 条, 实际为 %d", len(resp2.Data))
	}

	// 第三页 1 条
	resp3, err := svc.List(ctx, &ListGroupsRequest{Page: 3, PageSize: 2})
	if err != nil {
		t.Fatalf("获取第三页失败: %v", err)
	}
	if len(resp3.Data) != 1 {
		t.Errorf("期望第三页返回 1 条, 实际为 %d", len(resp3.Data))
	}
}

func TestAttachmentGroupService_List_SortOrder(t *testing.T) {
	client := setupAttachmentGroupTest(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	// 创建不同sort值的分组
	createTestAttachmentGroup(t, client, "sort2", 2)
	createTestAttachmentGroup(t, client, "sort1", 1)
	createTestAttachmentGroup(t, client, "sort3", 3)

	req := &ListGroupsRequest{
		Page:     1,
		PageSize: 10,
	}

	resp, err := svc.List(ctx, req)
	if err != nil {
		t.Fatalf("获取列表失败: %v", err)
	}
	if len(resp.Data) < 3 {
		t.Fatalf("期望至少 3 条数据, 实际为 %d", len(resp.Data))
	}

	// 检查是否按sort升序排列
	if resp.Data[0].Sort > resp.Data[1].Sort || resp.Data[1].Sort > resp.Data[2].Sort {
		t.Error("分组没有按sort升序排列")
	}
}

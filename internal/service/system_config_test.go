package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

// setupConfigTestDB 初始化测试数据库并注入全局 ORM
func setupConfigTestDB(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	return client
}

func TestConfigService_Create_Success(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	svc := NewConfigService(nil)

	status := 1
	req := &CreateConfigRequest{
		Name:   "站点名称",
		Key:    "site_name",
		Value:  "Go-Admin",
		Type:   "string",
		Group:  "site",
		Status: &status,
		Remark: "站点名称配置",
	}

	resp, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatalf("创建配置失败: %v", err)
	}

	if resp.ID == 0 {
		t.Fatal("期望配置ID不为0")
	}
	if resp.Name != "站点名称" {
		t.Fatalf("期望 Name=%q, 实际 %q", "站点名称", resp.Name)
	}
	if resp.Key != "site_name" {
		t.Fatalf("期望 Key=%q, 实际 %q", "site_name", resp.Key)
	}
	if resp.Value != "Go-Admin" {
		t.Fatalf("期望 Value=%q, 实际 %q", "Go-Admin", resp.Value)
	}
	if resp.Type != "string" {
		t.Fatalf("期望 Type=%q, 实际 %q", "string", resp.Type)
	}
	if resp.Group != "site" {
		t.Fatalf("期望 Group=%q, 实际 %q", "site", resp.Group)
	}
	if resp.Status != 1 {
		t.Fatalf("期望 Status=1, 实际 %d", resp.Status)
	}
	if resp.Remark != "站点名称配置" {
		t.Fatalf("期望 Remark=%q, 实际 %q", "站点名称配置", resp.Remark)
	}

	// 验证持久化
	item, err := client.SystemConfig.Query().Where(systemconfig.IDEQ(resp.ID)).Only(ctx)
	if err != nil {
		t.Fatalf("查询已创建的配置失败: %v", err)
	}
	if item.Name != "站点名称" {
		t.Fatalf("持久化数据不匹配: Name=%q", item.Name)
	}
}

func TestConfigService_Create_WithAllOptionalFields(t *testing.T) {
	_ = setupConfigTestDB(t)
	ctx := t.Context()

	svc := NewConfigService(nil)

	status := 0
	req := &CreateConfigRequest{
		Name:   "测试配置",
		Key:    "test_all_fields",
		Value:  `{"hello":"world"}`,
		Type:   "json",
		Group:  "test",
		Status: &status,
		Remark: "全字段测试",
	}

	resp, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatalf("创建配置失败: %v", err)
	}

	if resp.Type != "json" {
		t.Fatalf("期望 Type=%q, 实际 %q", "json", resp.Type)
	}
	if resp.Group != "test" {
		t.Fatalf("期望 Group=%q, 实际 %q", "test", resp.Group)
	}
	if resp.Status != 0 {
		t.Fatalf("期望 Status=0, 实际 %d", resp.Status)
	}
	if resp.Remark != "全字段测试" {
		t.Fatalf("期望 Remark=%q, 实际 %q", "全字段测试", resp.Remark)
	}
}

func TestConfigService_Create_DuplicateKey(t *testing.T) {
	_ = setupConfigTestDB(t)
	ctx := t.Context()

	svc := NewConfigService(nil)

	status := 1
	req := &CreateConfigRequest{
		Name:   "第一次创建",
		Key:    "duplicate_key",
		Value:  "value1",
		Status: &status,
	}

	_, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatalf("首次创建配置失败: %v", err)
	}

	// 再次创建相同key
	req2 := &CreateConfigRequest{
		Name:  "第二次创建",
		Key:   "duplicate_key",
		Value: "value2",
	}
	_, err = svc.Create(ctx, req2)
	if err == nil {
		t.Fatal("期望 ErrConfigKeyExists 错误，但没有错误")
	}
	if !errors.Is(err, ErrConfigKeyExists) {
		t.Fatalf("期望 ErrConfigKeyExists, 实际 %v", err)
	}
}

func TestConfigService_Get_Found(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	// 直接通过ent创建
	item, err := client.SystemConfig.Create().
		SetName("查询配置").
		SetKey("get_test").
		SetValue("get_value").
		SetType("string").
		SetGroup("test").
		SetStatus(1).
		SetRemark("用于测试查询").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试配置失败: %v", err)
	}

	svc := NewConfigService(nil)
	resp, err := svc.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("获取配置失败: %v", err)
	}

	if resp.ID != item.ID {
		t.Fatalf("期望 ID=%d, 实际 %d", item.ID, resp.ID)
	}
	if resp.Name != "查询配置" {
		t.Fatalf("期望 Name=%q, 实际 %q", "查询配置", resp.Name)
	}
	if resp.Key != "get_test" {
		t.Fatalf("期望 Key=%q, 实际 %q", "get_test", resp.Key)
	}
}

func TestConfigService_Get_NotFound(t *testing.T) {
	setupConfigTestDB(t)
	ctx := t.Context()

	svc := NewConfigService(nil)
	_, err := svc.Get(ctx, 99999)
	if err == nil {
		t.Fatal("期望 ErrConfigNotFound 错误，但没有错误")
	}
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("期望 ErrConfigNotFound, 实际 %v", err)
	}
}

func TestConfigService_List_NoFilters(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	// 创建3条配置
	for i, entry := range []struct {
		name, key, value string
	}{
		{"配置A", "config_a", "val_a"},
		{"配置B", "config_b", "val_b"},
		{"配置C", "config_c", "val_c"},
	} {
		_, err := client.SystemConfig.Create().
			SetName(entry.name).
			SetKey(entry.key).
			SetValue(entry.value).
			SetType("string").
			SetGroup("list_test").
			SetStatus(1).
			Save(ctx)
		if err != nil {
			t.Fatalf("创建第%d条测试配置失败: %v", i+1, err)
		}
	}

	svc := NewConfigService(nil)
	resp, err := svc.List(ctx, &ListConfigRequest{})
	if err != nil {
		t.Fatalf("获取配置列表失败: %v", err)
	}

	if resp.Total != 3 {
		t.Fatalf("期望 Total=3, 实际 %d", resp.Total)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("期望列表长度=3, 实际 %d", len(resp.Data))
	}
}

func TestConfigService_List_KeywordFilter(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	// 创建配置，其中一条名称包含"日志"
	entries := []struct {
		name, key, value string
	}{
		{"日志配置", "log_config", "log_value"},
		{"站点配置", "site_config", "site_value"},
		{"系统配置", "sys_config", "sys_value"},
	}
	for _, entry := range entries {
		_, err := client.SystemConfig.Create().
			SetName(entry.name).
			SetKey(entry.key).
			SetValue(entry.value).
			SetType("string").
			SetGroup("kw_test").
			SetStatus(1).
			Save(ctx)
		if err != nil {
			t.Fatalf("创建测试配置失败: %v", err)
		}
	}

	svc := NewConfigService(nil)
	resp, err := svc.List(ctx, &ListConfigRequest{
		ListRequest: ListRequest{Keyword: "日志"},
	})
	if err != nil {
		t.Fatalf("关键字过滤查询失败: %v", err)
	}

	if resp.Total != 1 {
		t.Fatalf("期望 Total=1, 实际 %d", resp.Total)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("期望1条结果, 实际 %d 条", len(resp.Data))
	}
	if resp.Data[0].Name != "日志配置" {
		t.Fatalf("期望 Name=%q, 实际 %q", "日志配置", resp.Data[0].Name)
	}
}

func TestConfigService_List_GroupFilter(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	// 创建不同分组的配置
	entries := []struct {
		name, key, value, group string
	}{
		{"站点名", "site_name", "Go-Admin", "site"},
		{"站点描述", "site_desc", "管理后台", "site"},
		{"邮箱主机", "smtp_host", "smtp.example.com", "email"},
	}
	for _, entry := range entries {
		_, err := client.SystemConfig.Create().
			SetName(entry.name).
			SetKey(entry.key).
			SetValue(entry.value).
			SetType("string").
			SetGroup(entry.group).
			SetStatus(1).
			Save(ctx)
		if err != nil {
			t.Fatalf("创建测试配置失败: %v", err)
		}
	}

	svc := NewConfigService(nil)
	resp, err := svc.List(ctx, &ListConfigRequest{Group: "site"})
	if err != nil {
		t.Fatalf("分组过滤查询失败: %v", err)
	}

	if resp.Total != 2 {
		t.Fatalf("期望 Total=2, 实际 %d", resp.Total)
	}
	for _, d := range resp.Data {
		if d.Group != "site" {
			t.Fatalf("期望 Group=%q, 实际 %q", "site", d.Group)
		}
	}
}

func TestConfigService_List_StatusFilter(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	// 创建启用和禁用的配置
	_, err := client.SystemConfig.Create().
		SetName("启用的配置").
		SetKey("enabled_cfg").
		SetValue("enabled").
		SetType("string").
		SetGroup("status_test").
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建启用配置失败: %v", err)
	}
	_, err = client.SystemConfig.Create().
		SetName("禁用的配置").
		SetKey("disabled_cfg").
		SetValue("disabled").
		SetType("string").
		SetGroup("status_test").
		SetStatus(0).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建禁用配置失败: %v", err)
	}

	svc := NewConfigService(nil)
	statusEnabled := 1
	resp, err := svc.List(ctx, &ListConfigRequest{Status: &statusEnabled})
	if err != nil {
		t.Fatalf("状态过滤查询失败: %v", err)
	}

	if resp.Total != 1 {
		t.Fatalf("期望 Total=1, 实际 %d", resp.Total)
	}
	if resp.Data[0].Name != "启用的配置" {
		t.Fatalf("期望 Name=%q, 实际 %q", "启用的配置", resp.Data[0].Name)
	}
}

func TestConfigService_GetByKey_Found(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	_, err := client.SystemConfig.Create().
		SetName("搜索配置").
		SetKey("search_key").
		SetValue("search_value").
		SetType("string").
		SetGroup("search").
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试配置失败: %v", err)
	}

	svc := NewConfigService(nil)
	resp, err := svc.GetByKey(ctx, "search_key")
	if err != nil {
		t.Fatalf("按key获取配置失败: %v", err)
	}

	if resp.Key != "search_key" {
		t.Fatalf("期望 Key=%q, 实际 %q", "search_key", resp.Key)
	}
	if resp.Value != "search_value" {
		t.Fatalf("期望 Value=%q, 实际 %q", "search_value", resp.Value)
	}
}

func TestConfigService_GetByKey_NotFound(t *testing.T) {
	setupConfigTestDB(t)
	ctx := t.Context()

	svc := NewConfigService(nil)
	_, err := svc.GetByKey(ctx, "non_existent_key")
	if err == nil {
		t.Fatal("期望 ErrConfigNotFound 错误，但没有错误")
	}
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("期望 ErrConfigNotFound, 实际 %v", err)
	}
}

func TestConfigService_GetByKey_DisabledNotReturned(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	// 创建一条禁用的配置
	_, err := client.SystemConfig.Create().
		SetName("禁用的配置").
		SetKey("disabled_key").
		SetValue("disabled_value").
		SetType("string").
		SetGroup("disabled_test").
		SetStatus(0).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建禁用配置失败: %v", err)
	}

	svc := NewConfigService(nil)
	_, err = svc.GetByKey(ctx, "disabled_key")
	if err == nil {
		t.Fatal("期望 ErrConfigNotFound 错误（禁用配置不应被返回），但没有错误")
	}
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("期望 ErrConfigNotFound, 实际 %v", err)
	}
}

func TestConfigService_GetByGroup_ReturnsEnabled(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	entries := []struct {
		name, key, value, group string
		status                  int
	}{
		{"站点名称", "site_name", "Go-Admin", "site", 1},
		{"站点描述", "site_desc", "管理后台", "site", 1},
		{"禁用的站点配置", "site_disabled", "禁用", "site", 0},
		{"邮箱配置", "email_host", "smtp.example.com", "email", 1},
	}
	for _, entry := range entries {
		_, err := client.SystemConfig.Create().
			SetName(entry.name).
			SetKey(entry.key).
			SetValue(entry.value).
			SetType("string").
			SetGroup(entry.group).
			SetStatus(entry.status).
			Save(ctx)
		if err != nil {
			t.Fatalf("创建测试配置失败: %v", err)
		}
	}

	svc := NewConfigService(nil)
	resp, err := svc.GetByGroup(ctx, "site")
	if err != nil {
		t.Fatalf("按分组获取配置失败: %v", err)
	}

	// 只应返回2条启用配置，禁用那条不应出现
	if len(resp) != 2 {
		t.Fatalf("期望2条启用配置, 实际 %d 条", len(resp))
	}
	for _, r := range resp {
		if r.Status != 1 {
			t.Fatalf("返回了禁用的配置: key=%q status=%d", r.Key, r.Status)
		}
		if r.Group != "site" {
			t.Fatalf("返回了错误分组的配置: key=%q group=%q", r.Key, r.Group)
		}
	}

	// 验证按key升序排列（site_desc < site_name 字母序）
	if resp[0].Key != "site_desc" || resp[1].Key != "site_name" {
		t.Fatalf("期望按key升序, 实际 [%q, %q]", resp[0].Key, resp[1].Key)
	}
}

func TestConfigService_Update_Success(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	item, err := client.SystemConfig.Create().
		SetName("原始名称").
		SetKey("update_key").
		SetValue("原始值").
		SetType("string").
		SetGroup("update_test").
		SetStatus(1).
		SetRemark("原始备注").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试配置失败: %v", err)
	}

	svc := NewConfigService(nil)

	newName := "更新后名称"
	newValue := "更新后的值"
	newType := "json"
	newGroup := "updated_group"
	newStatus := 0
	newRemark := "更新后的备注"

	err = svc.Update(ctx, item.ID, &UpdateConfigRequest{
		Name:   &newName,
		Value:  &newValue,
		Type:   &newType,
		Group:  &newGroup,
		Status: &newStatus,
		Remark: &newRemark,
	})
	if err != nil {
		t.Fatalf("更新配置失败: %v", err)
	}

	// 验证更新结果
	updated, err := client.SystemConfig.Query().Where(systemconfig.IDEQ(item.ID)).Only(ctx)
	if err != nil {
		t.Fatalf("查询更新后的配置失败: %v", err)
	}
	if updated.Name != newName {
		t.Fatalf("期望 Name=%q, 实际 %q", newName, updated.Name)
	}
	if updated.Value != newValue {
		t.Fatalf("期望 Value=%q, 实际 %q", newValue, updated.Value)
	}
	if updated.Type != newType {
		t.Fatalf("期望 Type=%q, 实际 %q", newType, updated.Type)
	}
	if updated.Group != newGroup {
		t.Fatalf("期望 Group=%q, 实际 %q", newGroup, updated.Group)
	}
	if updated.Status != newStatus {
		t.Fatalf("期望 Status=%d, 实际 %d", newStatus, updated.Status)
	}
	if updated.Remark != newRemark {
		t.Fatalf("期望 Remark=%q, 实际 %q", newRemark, updated.Remark)
	}
}

func TestConfigService_Update_DuplicateKey(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	// 创建两条不同key的配置
	_, err := client.SystemConfig.Create().
		SetName("配置一").
		SetKey("key_one").
		SetValue("val1").
		SetType("string").
		SetGroup("dup_test").
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建配置一失败: %v", err)
	}

	item2, err := client.SystemConfig.Create().
		SetName("配置二").
		SetKey("key_two").
		SetValue("val2").
		SetType("string").
		SetGroup("dup_test").
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建配置二失败: %v", err)
	}

	// 尝试把配置二的key改成配置一的key
	svc := NewConfigService(nil)
	dupKey := "key_one"
	err = svc.Update(ctx, item2.ID, &UpdateConfigRequest{
		Key: &dupKey,
	})
	if err == nil {
		t.Fatal("期望 ErrConfigKeyExists 错误，但没有错误")
	}
	if !errors.Is(err, ErrConfigKeyExists) {
		t.Fatalf("期望 ErrConfigKeyExists, 实际 %v", err)
	}
}

func TestConfigService_Update_NotFound(t *testing.T) {
	setupConfigTestDB(t)
	ctx := t.Context()

	svc := NewConfigService(nil)
	newName := "更新失败"
	err := svc.Update(ctx, 99999, &UpdateConfigRequest{
		Name: &newName,
	})
	if err == nil {
		t.Fatal("期望 ErrConfigNotFound 错误，但没有错误")
	}
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("期望 ErrConfigNotFound, 实际 %v", err)
	}
}

func TestConfigService_Delete_Success(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	item, err := client.SystemConfig.Create().
		SetName("待删除配置").
		SetKey("delete_key").
		SetValue("delete_value").
		SetType("string").
		SetGroup("delete_test").
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试配置失败: %v", err)
	}

	svc := NewConfigService(nil)
	err = svc.Delete(ctx, item.ID)
	if err != nil {
		t.Fatalf("删除配置失败: %v", err)
	}

	// 验证已删除
	exists, err := client.SystemConfig.Query().Where(systemconfig.IDEQ(item.ID)).Exist(ctx)
	if err != nil {
		t.Fatalf("查询删除结果失败: %v", err)
	}
	if exists {
		t.Fatal("配置应已被删除，但仍存在")
	}
}

func TestConfigService_Delete_NotFound(t *testing.T) {
	setupConfigTestDB(t)
	ctx := t.Context()

	svc := NewConfigService(nil)
	err := svc.Delete(ctx, 99999)
	if err == nil {
		t.Fatal("期望 ErrConfigNotFound 错误，但没有错误")
	}
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("期望 ErrConfigNotFound, 实际 %v", err)
	}
}

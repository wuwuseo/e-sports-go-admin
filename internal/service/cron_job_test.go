package service

import (
	"errors"
	"testing"
	"time"

	"go-admin/internal/cron"
	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

// setupCronJobTest 初始化测试数据库、CronManager 并注入全局 ORM
func setupCronJobTest(t *testing.T) (*ent.Client, *cron.CronManager) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	tmpDir := t.TempDir()
	manager := cron.NewCronManager(client, tmpDir, 24*time.Hour)

	return client, manager
}

// setupCronJobTestWithClient 使用已有的client创建CronManager（集成测试用）
func setupCronJobTestWithClient(t *testing.T, client *ent.Client, tmpDir string) (*ent.Client, *cron.CronManager) {
	manager := cron.NewCronManager(client, tmpDir, 24*time.Hour)
	return client, manager
}

// ======================== Create 测试 ========================

func TestCronJobService_Create_Success(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	req := &CreateCronJobRequest{
		Name:        "测试定时任务",
		Description: "这是一个测试任务",
		Type:        "curl",
		Expression:  "0 */5 * * * *",
		Command:     `{"method":"GET","url":"http://example.com"}`,
	}

	resp, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatalf("创建定时任务失败: %v", err)
	}

	if resp.ID == 0 {
		t.Fatal("期望定时任务ID不为0")
	}
	if resp.Name != "测试定时任务" {
		t.Fatalf("期望 Name=%q, 实际 %q", "测试定时任务", resp.Name)
	}
	if resp.Type != "curl" {
		t.Fatalf("期望 Type=%q, 实际 %q", "curl", resp.Type)
	}
	if resp.Expression != "0 */5 * * * *" {
		t.Fatalf("期望 Expression=%q, 实际 %q", "0 */5 * * * *", resp.Expression)
	}
	if resp.Description != "这是一个测试任务" {
		t.Fatalf("期望 Description=%q, 实际 %q", "这是一个测试任务", resp.Description)
	}
	if !resp.Enabled {
		t.Fatal("期望默认启用状态为 true")
	}
	if resp.CreatedAt.IsZero() {
		t.Fatal("期望 CreatedAt 不为零值")
	}
	if resp.UpdatedAt.IsZero() {
		t.Fatal("期望 UpdatedAt 不为零值")
	}
}

func TestCronJobService_Create_DuplicateName(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	req := &CreateCronJobRequest{
		Name:       "唯一任务名",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
	}

	_, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatalf("第一次创建定时任务失败: %v", err)
	}

	_, err = svc.Create(ctx, req)
	if err == nil {
		t.Fatal("期望重复名称返回错误，但未返回")
	}
	if !errors.Is(err, ErrCronJobNameExists) {
		t.Errorf("期望错误为 ErrCronJobNameExists, 实际为 %v", err)
	}
}

func TestCronJobService_Create_InvalidCronExpression(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	req := &CreateCronJobRequest{
		Name:       "无效表达式任务",
		Type:       "curl",
		Expression: "invalid-cron-expression",
		Command:    `{"method":"GET","url":"http://example.com"}`,
	}

	_, err := svc.Create(ctx, req)
	if err == nil {
		t.Fatal("期望无效 cron 表达式返回错误，但未返回")
	}
	if !errors.Is(err, ErrCronJobInvalidExpr) {
		t.Errorf("期望错误为 ErrCronJobInvalidExpr, 实际为 %v", err)
	}
}

func TestCronJobService_Create_InvalidType(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	req := &CreateCronJobRequest{
		Name:       "无效类型任务",
		Type:       "cmd",
		Expression: "0 */5 * * * *",
		Command:    "echo test",
	}

	_, err := svc.Create(ctx, req)
	if err == nil {
		t.Fatal("期望无效类型返回错误，但未返回")
	}
	// ent 枚举验证会先于服务层错误触发
	if err == nil {
		t.Error("期望返回错误")
	}
}

func TestCronJobService_Create_WithDisabled(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	disabled := false
	req := &CreateCronJobRequest{
		Name:       "禁用状态任务",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
		Enabled:    &disabled,
	}

	resp, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatalf("创建禁用状态定时任务失败: %v", err)
	}

	if resp.Enabled {
		t.Fatal("期望 Enabled=false, 实际为 true")
	}
}

// ======================== Get 测试 ========================

func TestCronJobService_Get_Found(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	req := &CreateCronJobRequest{
		Name:       "查询测试任务",
		Type:       "curl",
		Expression: "0 */10 * * * *",
		Command:    `{"method":"POST","url":"http://example.com/api"}`,
	}

	created, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatalf("创建定时任务失败: %v", err)
	}

	resp, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("获取定时任务失败: %v", err)
	}

	if resp.ID != created.ID {
		t.Fatalf("期望 ID=%d, 实际 %d", created.ID, resp.ID)
	}
	if resp.Name != "查询测试任务" {
		t.Fatalf("期望 Name=%q, 实际 %q", "查询测试任务", resp.Name)
	}
	if resp.Type != "curl" {
		t.Fatalf("期望 Type=%q, 实际 %q", "curl", resp.Type)
	}
	if resp.Expression != "0 */10 * * * *" {
		t.Fatalf("期望 Expression=%q, 实际 %q", "0 */10 * * * *", resp.Expression)
	}
}

func TestCronJobService_Get_NotFound(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	_, err := svc.Get(ctx, 99999)
	if err == nil {
		t.Fatal("期望不存在的定时任务返回错误，但未返回")
	}
	if !errors.Is(err, ErrCronJobNotFound) {
		t.Errorf("期望错误为 ErrCronJobNotFound, 实际为 %v", err)
	}
}

// ======================== List 测试 ========================

func TestCronJobService_List_WithData(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	// 创建多条测试数据
	for i := range 3 {
		name := "列表测试任务" + string(rune('A'+i))
		_, err := svc.Create(ctx, &CreateCronJobRequest{
			Name:       name,
			Type:       "curl",
			Expression: "0 */5 * * * *",
			Command:    `{"method":"GET","url":"http://example.com"}`,
		})
		if err != nil {
			t.Fatalf("创建定时任务 %s 失败: %v", name, err)
		}
	}

	resp, err := svc.List(ctx, &ListCronJobsRequest{})
	if err != nil {
		t.Fatalf("获取定时任务列表失败: %v", err)
	}

	if resp.Total != 3 {
		t.Fatalf("期望 Total=3, 实际 %d", resp.Total)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("期望 Data 长度=3, 实际 %d", len(resp.Data))
	}
	if resp.Page != 1 {
		t.Fatalf("期望 Page=1, 实际 %d", resp.Page)
	}
}

func TestCronJobService_List_Pagination(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	// 创建 5 条测试数据
	for i := range 5 {
		name := "分页测试任务" + string(rune('A'+i))
		_, err := svc.Create(ctx, &CreateCronJobRequest{
			Name:       name,
			Type:       "curl",
			Expression: "0 */5 * * * *",
			Command:    `{"method":"GET","url":"http://example.com"}`,
		})
		if err != nil {
			t.Fatalf("创建定时任务 %s 失败: %v", name, err)
		}
	}

	// 第一页，每页 2 条
	resp, err := svc.List(ctx, &ListCronJobsRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{
				Page:     1,
				PageSize: 2,
			},
		},
	})
	if err != nil {
		t.Fatalf("获取第一页失败: %v", err)
	}
	if resp.Total != 5 {
		t.Fatalf("期望 Total=5, 实际 %d", resp.Total)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("期望第一页 Data 长度=2, 实际 %d", len(resp.Data))
	}
	if resp.Page != 1 {
		t.Fatalf("期望 Page=1, 实际 %d", resp.Page)
	}

	// 第二页，每页 2 条
	resp, err = svc.List(ctx, &ListCronJobsRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{
				Page:     2,
				PageSize: 2,
			},
		},
	})
	if err != nil {
		t.Fatalf("获取第二页失败: %v", err)
	}
	if resp.Total != 5 {
		t.Fatalf("期望 Total=5, 实际 %d", resp.Total)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("期望第二页 Data 长度=2, 实际 %d", len(resp.Data))
	}

	// 第三页，每页 2 条（仅剩 1 条）
	resp, err = svc.List(ctx, &ListCronJobsRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{
				Page:     3,
				PageSize: 2,
			},
		},
	})
	if err != nil {
		t.Fatalf("获取第三页失败: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("期望第三页 Data 长度=1, 实际 %d", len(resp.Data))
	}
}

func TestCronJobService_List_TypeFilter(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	// 创建 curl 类型任务
	_, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "curl任务",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
	})
	if err != nil {
		t.Fatalf("创建 curl 任务失败: %v", err)
	}

	// 按 curl 类型筛选
	resp, err := svc.List(ctx, &ListCronJobsRequest{
		Type: "curl",
	})
	if err != nil {
		t.Fatalf("按类型筛选失败: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("期望 curl 类型 Total=1, 实际 %d", resp.Total)
	}

	// 按 task 类型筛选（无数据）
	resp, err = svc.List(ctx, &ListCronJobsRequest{
		Type: "task",
	})
	if err != nil {
		t.Fatalf("按类型筛选失败: %v", err)
	}
	if resp.Total != 0 {
		t.Fatalf("期望 task 类型 Total=0, 实际 %d", resp.Total)
	}
}

func TestCronJobService_List_KeywordSearch(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	_, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "关键词搜索任务",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 搜索匹配的关键词
	resp, err := svc.List(ctx, &ListCronJobsRequest{
		ListRequest: ListRequest{
			Keyword: "关键词",
		},
	})
	if err != nil {
		t.Fatalf("关键词搜索失败: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("期望搜索到 1 条结果, 实际 %d", resp.Total)
	}

	// 搜索不匹配的关键词
	resp, err = svc.List(ctx, &ListCronJobsRequest{
		ListRequest: ListRequest{
			Keyword: "不存在的关键词",
		},
	})
	if err != nil {
		t.Fatalf("关键词搜索失败: %v", err)
	}
	if resp.Total != 0 {
		t.Fatalf("期望搜索到 0 条结果, 实际 %d", resp.Total)
	}
}

// ======================== Update 测试 ========================

func TestCronJobService_Update_Success(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	// 先创建一个任务
	created, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "更新前任务名",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 更新任务
	newName := "更新后任务名"
	newDesc := "更新后的描述"
	newExpr := "0 */10 * * * *"
	newCmd := `{"method":"POST","url":"http://example.com/api"}`
	newType := "curl"

	err = svc.Update(ctx, created.ID, &UpdateCronJobRequest{
		Name:        &newName,
		Description: &newDesc,
		Expression:  &newExpr,
		Command:     &newCmd,
		Type:        &newType,
	})
	if err != nil {
		t.Fatalf("更新定时任务失败: %v", err)
	}

	// 验证更新结果
	resp, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("获取更新后任务失败: %v", err)
	}
	if resp.Name != newName {
		t.Fatalf("期望 Name=%q, 实际 %q", newName, resp.Name)
	}
	if resp.Description != newDesc {
		t.Fatalf("期望 Description=%q, 实际 %q", newDesc, resp.Description)
	}
	if resp.Expression != newExpr {
		t.Fatalf("期望 Expression=%q, 实际 %q", newExpr, resp.Expression)
	}
	if resp.Command != newCmd {
		t.Fatalf("期望 Command=%q, 实际 %q", newCmd, resp.Command)
	}
}

func TestCronJobService_Update_NotFound(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	newName := "不存在的任务"
	err := svc.Update(ctx, 99999, &UpdateCronJobRequest{
		Name: &newName,
	})
	if err == nil {
		t.Fatal("期望不存在的任务返回错误，但未返回")
	}
	if !errors.Is(err, ErrCronJobNotFound) {
		t.Errorf("期望错误为 ErrCronJobNotFound, 实际为 %v", err)
	}
}

func TestCronJobService_Update_DuplicateName(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	// 创建两个任务
	_, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "任务A",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
	})
	if err != nil {
		t.Fatalf("创建任务A失败: %v", err)
	}

	created2, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "任务B",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
	})
	if err != nil {
		t.Fatalf("创建任务B失败: %v", err)
	}

	// 尝试将任务B重命名为任务A（应失败）
	newName := "任务A"
	err = svc.Update(ctx, created2.ID, &UpdateCronJobRequest{
		Name: &newName,
	})
	if err == nil {
		t.Fatal("期望重复名称返回错误，但未返回")
	}
	if !errors.Is(err, ErrCronJobNameExists) {
		t.Errorf("期望错误为 ErrCronJobNameExists, 实际为 %v", err)
	}
}

func TestCronJobService_Update_InvalidExpression(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	created, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "表达式更新测试",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	invalidExpr := "invalid-cron"
	err = svc.Update(ctx, created.ID, &UpdateCronJobRequest{
		Expression: &invalidExpr,
	})
	if err == nil {
		t.Fatal("期望无效 cron 表达式返回错误，但未返回")
	}
	if !errors.Is(err, ErrCronJobInvalidExpr) {
		t.Errorf("期望错误为 ErrCronJobInvalidExpr, 实际为 %v", err)
	}
}

func TestCronJobService_Update_SameNameDifferentID(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	_, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "更新自身名称",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	created2, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "其他任务",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
	})
	if err != nil {
		t.Fatalf("创建第二个任务失败: %v", err)
	}

	// 将自己的名称更新为自身（应成功）
	sameName := "更新自身名称"
	err = svc.Update(ctx, created2.ID, &UpdateCronJobRequest{
		Name: &sameName,
	})
	// 注意：名称已存在于created2自身，但由于 Update 中检查的是 NameEQ 且 IDNEQ，
	// 所以创建的任务A叫"更新自身名称"，将任务B的名字改为"更新自身名称"会冲突
	if err == nil {
		t.Fatal("期望重复名称返回错误，但未返回")
	}
	if !errors.Is(err, ErrCronJobNameExists) {
		t.Errorf("期望错误为 ErrCronJobNameExists, 实际为 %v", err)
	}
}

// ======================== Delete 测试 ========================

func TestCronJobService_Delete_Success(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	// 创建待删除的任务
	created, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "待删除任务",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 删除任务
	err = svc.Delete(ctx, created.ID)
	if err != nil {
		t.Fatalf("删除定时任务失败: %v", err)
	}

	// 验证已删除
	_, err = svc.Get(ctx, created.ID)
	if err == nil {
		t.Fatal("期望已删除的任务返回错误，但未返回")
	}
	if !errors.Is(err, ErrCronJobNotFound) {
		t.Errorf("期望错误为 ErrCronJobNotFound, 实际为 %v", err)
	}
}

func TestCronJobService_Delete_NotFound(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	err := svc.Delete(ctx, 99999)
	if err == nil {
		t.Fatal("期望不存在的任务返回错误，但未返回")
	}
	if !errors.Is(err, ErrCronJobNotFound) {
		t.Errorf("期望错误为 ErrCronJobNotFound, 实际为 %v", err)
	}
}

// ======================== List 额外测试 ========================

func TestCronJobService_List_EnabledFilter(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	// 创建一个启用的任务（默认启用）
	enabledReq := &CreateCronJobRequest{
		Name:       "已启用任务",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
	}
	_, err := svc.Create(ctx, enabledReq)
	if err != nil {
		t.Fatalf("创建已启用任务失败: %v", err)
	}

	// 创建一个禁用的任务
	disabled := false
	_, err = svc.Create(ctx, &CreateCronJobRequest{
		Name:       "已禁用任务",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
		Enabled:    &disabled,
	})
	if err != nil {
		t.Fatalf("创建已禁用任务失败: %v", err)
	}

	// 筛选启用的任务
	trueVal := true
	resp, err := svc.List(ctx, &ListCronJobsRequest{
		Enabled: &trueVal,
	})
	if err != nil {
		t.Fatalf("按启用状态筛选失败: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("期望 Enabled=true 筛选结果 Total=1, 实际 %d", resp.Total)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("期望 Enabled=true 筛选结果 Data 长度=1, 实际 %d", len(resp.Data))
	}
	if !resp.Data[0].Enabled {
		t.Fatal("期望筛选出的任务为启用状态")
	}

	// 筛选禁用的任务
	falseVal := false
	resp, err = svc.List(ctx, &ListCronJobsRequest{
		Enabled: &falseVal,
	})
	if err != nil {
		t.Fatalf("按禁用状态筛选失败: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("期望 Enabled=false 筛选结果 Total=1, 实际 %d", resp.Total)
	}
	if resp.Data[0].Enabled {
		t.Fatal("期望筛选出的任务为禁用状态")
	}
}

func TestCronJobService_List_EmptyResult(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	// 不创建任何数据，直接查询
	resp, err := svc.List(ctx, &ListCronJobsRequest{})
	if err != nil {
		t.Fatalf("获取空列表失败: %v", err)
	}
	if resp.Total != 0 {
		t.Fatalf("期望 Total=0, 实际 %d", resp.Total)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("期望 Data 长度=0, 实际 %d", len(resp.Data))
	}
	if resp.Page != 1 {
		t.Fatalf("期望 Page=1, 实际 %d", resp.Page)
	}
}

func TestCronJobService_List_CombinedFilters(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	// 创建多个不同类型和状态的任务
	_, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "启用的curl任务",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	disabled := false
	_, err = svc.Create(ctx, &CreateCronJobRequest{
		Name:       "禁用的curl任务",
		Type:       "curl",
		Expression: "0 */5 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
		Enabled:    &disabled,
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 按类型+关键字组合筛选
	trueVal := true
	resp, err := svc.List(ctx, &ListCronJobsRequest{
		ListRequest: ListRequest{
			Keyword: "curl",
		},
		Type:    "curl",
		Enabled: &trueVal,
	})
	if err != nil {
		t.Fatalf("组合筛选失败: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("期望组合筛选结果 Total=1, 实际 %d", resp.Total)
	}
}

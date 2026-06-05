package service

import (
	"context"
	"testing"
	"time"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupOperationLogTest(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
}

func createTestLog(t *testing.T, username, module, action string, createdAt time.Time) {
	_, err := global.ORM.OperationLog.Create().
		SetUserID(1).
		SetUsername(username).
		SetModule(module).
		SetAction(action).
		SetDescription("测试操作日志").
		SetMethod("POST").
		SetPath("/api/v1/test").
		SetIP("127.0.0.1").
		SetStatusCode(200).
		SetDurationMs(100).
		SetCreatedAt(createdAt).
		Save(t.Context())
	if err != nil {
		t.Fatalf("创建测试日志失败: %v", err)
	}
}

func TestOperationLogService_List_EmptyList(t *testing.T) {
	setupOperationLogTest(t)
	ctx := t.Context()
	svc := NewOperationLogService()

	resp, err := svc.List(ctx, &ListOperationLogsRequest{})
	if err != nil {
		t.Fatalf("查询空列表失败: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("期望 Total=0, 实际 Total=%d", resp.Total)
	}
	if len(resp.Data) != 0 {
		t.Errorf("期望返回0条数据, 实际返回 %d 条", len(resp.Data))
	}
}

func TestOperationLogService_List_WithData(t *testing.T) {
	setupOperationLogTest(t)
	ctx := t.Context()
	svc := NewOperationLogService()

	now := time.Now()
	createTestLog(t, "admin", "用户管理", "创建", now)
	createTestLog(t, "editor", "文章管理", "更新", now.Add(time.Second))
	createTestLog(t, "viewer", "系统设置", "查看", now.Add(2*time.Second))

	resp, err := svc.List(ctx, &ListOperationLogsRequest{})
	if err != nil {
		t.Fatalf("查询列表失败: %v", err)
	}
	if resp.Total != 3 {
		t.Errorf("期望 Total=3, 实际 Total=%d", resp.Total)
	}
	if len(resp.Data) != 3 {
		t.Errorf("期望返回3条数据, 实际返回 %d 条", len(resp.Data))
	}
	if resp.Page != 1 {
		t.Errorf("期望 Page=1, 实际 Page=%d", resp.Page)
	}
}

func TestOperationLogService_List_UsernameFilter(t *testing.T) {
	setupOperationLogTest(t)
	ctx := t.Context()
	svc := NewOperationLogService()

	now := time.Now()
	createTestLog(t, "admin_user", "用户管理", "创建", now)
	createTestLog(t, "editor_user", "文章管理", "更新", now.Add(time.Second))
	createTestLog(t, "super_admin", "系统设置", "删除", now.Add(2*time.Second))

	resp, err := svc.List(ctx, &ListOperationLogsRequest{
		Username: "admin",
	})
	if err != nil {
		t.Fatalf("按用户名筛选查询失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望 Total=2 (匹配 'admin'), 实际 Total=%d", resp.Total)
	}
	for _, item := range resp.Data {
		if item.Username != "admin_user" && item.Username != "super_admin" {
			t.Errorf("期望用户名包含 'admin', 实际用户名为 '%s'", item.Username)
		}
	}
}

func TestOperationLogService_List_ModuleFilter(t *testing.T) {
	setupOperationLogTest(t)
	ctx := t.Context()
	svc := NewOperationLogService()

	now := time.Now()
	createTestLog(t, "admin", "用户管理", "创建", now)
	createTestLog(t, "editor", "文章管理", "更新", now.Add(time.Second))
	createTestLog(t, "viewer", "用户管理", "删除", now.Add(2*time.Second))

	resp, err := svc.List(ctx, &ListOperationLogsRequest{
		Module: "用户管理",
	})
	if err != nil {
		t.Fatalf("按模块筛选查询失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望 Total=2 (模块='用户管理'), 实际 Total=%d", resp.Total)
	}
	for _, item := range resp.Data {
		if item.Module != "用户管理" {
			t.Errorf("期望模块为 '用户管理', 实际模块为 '%s'", item.Module)
		}
	}
}

func TestOperationLogService_List_ActionFilter(t *testing.T) {
	setupOperationLogTest(t)
	ctx := t.Context()
	svc := NewOperationLogService()

	now := time.Now()
	createTestLog(t, "admin", "用户管理", "创建", now)
	createTestLog(t, "editor", "文章管理", "更新", now.Add(time.Second))
	createTestLog(t, "viewer", "系统设置", "创建", now.Add(2*time.Second))

	resp, err := svc.List(ctx, &ListOperationLogsRequest{
		Action: "创建",
	})
	if err != nil {
		t.Fatalf("按操作类型筛选查询失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望 Total=2 (操作='创建'), 实际 Total=%d", resp.Total)
	}
	for _, item := range resp.Data {
		if item.Action != "创建" {
			t.Errorf("期望操作为 '创建', 实际操作为 '%s'", item.Action)
		}
	}
}

func TestOperationLogService_List_StartTimeFilter(t *testing.T) {
	setupOperationLogTest(t)
	ctx := t.Context()
	svc := NewOperationLogService()

	baseTime := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	createTestLog(t, "admin", "用户管理", "创建", baseTime)
	createTestLog(t, "editor", "文章管理", "更新", baseTime.Add(1*time.Hour))
	createTestLog(t, "viewer", "系统设置", "删除", baseTime.Add(2*time.Hour))

	filterTime := baseTime.Add(30 * time.Minute)
	resp, err := svc.List(ctx, &ListOperationLogsRequest{
		StartTime: filterTime.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("按开始时间筛选查询失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望 Total=2 (>=%s), 实际 Total=%d", filterTime.Format(time.RFC3339), resp.Total)
	}
}

func TestOperationLogService_List_Pagination(t *testing.T) {
	setupOperationLogTest(t)
	ctx := t.Context()
	svc := NewOperationLogService()

	now := time.Now()
	createTestLog(t, "user1", "模块A", "创建", now)
	createTestLog(t, "user2", "模块B", "更新", now.Add(time.Second))
	createTestLog(t, "user3", "模块A", "删除", now.Add(2*time.Second))
	createTestLog(t, "user4", "模块C", "查看", now.Add(3*time.Second))
	createTestLog(t, "user5", "模块B", "创建", now.Add(4*time.Second))

	resp, err := svc.List(ctx, &ListOperationLogsRequest{
		PaginationRequest: PaginationRequest{Page: 1, PageSize: 2},
	})
	if err != nil {
		t.Fatalf("分页查询第一页失败: %v", err)
	}
	if resp.Total != 5 {
		t.Errorf("期望 Total=5, 实际 Total=%d", resp.Total)
	}
	if len(resp.Data) != 2 {
		t.Errorf("期望第一页返回2条, 实际返回 %d 条", len(resp.Data))
	}
	if resp.Page != 1 {
		t.Errorf("期望 Page=1, 实际 Page=%d", resp.Page)
	}

	resp2, err := svc.List(ctx, &ListOperationLogsRequest{
		PaginationRequest: PaginationRequest{Page: 3, PageSize: 2},
	})
	if err != nil {
		t.Fatalf("分页查询第三页失败: %v", err)
	}
	if resp2.Total != 5 {
		t.Errorf("期望 Total=5, 实际 Total=%d", resp2.Total)
	}
	if len(resp2.Data) != 1 {
		t.Errorf("期望第三页返回1条, 实际返回 %d 条", len(resp2.Data))
	}
}

func TestOperationLogService_Get_Found(t *testing.T) {
	setupOperationLogTest(t)
	ctx := t.Context()
	svc := NewOperationLogService()

	now := time.Now()
	createTestLog(t, "admin", "用户管理", "创建", now)

	resp, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("获取操作日志失败: %v", err)
	}
	if resp.ID != 1 {
		t.Errorf("期望 ID=1, 实际 ID=%d", resp.ID)
	}
	if resp.Username != "admin" {
		t.Errorf("期望 Username='admin', 实际 Username='%s'", resp.Username)
	}
	if resp.Module != "用户管理" {
		t.Errorf("期望 Module='用户管理', 实际 Module='%s'", resp.Module)
	}
	if resp.Action != "创建" {
		t.Errorf("期望 Action='创建', 实际 Action='%s'", resp.Action)
	}
	if resp.Description != "测试操作日志" {
		t.Errorf("期望 Description='测试操作日志', 实际 Description='%s'", resp.Description)
	}
	if resp.Method != "POST" {
		t.Errorf("期望 Method='POST', 实际 Method='%s'", resp.Method)
	}
	if resp.Path != "/api/v1/test" {
		t.Errorf("期望 Path='/api/v1/test', 实际 Path='%s'", resp.Path)
	}
	if resp.IP != "127.0.0.1" {
		t.Errorf("期望 IP='127.0.0.1', 实际 IP='%s'", resp.IP)
	}
	if resp.StatusCode != 200 {
		t.Errorf("期望 StatusCode=200, 实际 StatusCode=%d", resp.StatusCode)
	}
	if resp.DurationMs != 100 {
		t.Errorf("期望 DurationMs=100, 实际 DurationMs=%d", resp.DurationMs)
	}
}

func TestOperationLogService_Get_NotFound(t *testing.T) {
	setupOperationLogTest(t)
	ctx := t.Context()
	svc := NewOperationLogService()

	_, err := svc.Get(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误, 实际返回 nil")
	}
	if err != ErrOperationLogNotFound {
		t.Errorf("期望 ErrOperationLogNotFound, 实际错误: %v", err)
	}
}

func TestOperationLogService_Log_QueueBackpressure(t *testing.T) {
	setupOperationLogTest(t)
	svc := NewOperationLogServiceWithOptions(OperationLogOptions{
		QueueSize:       1,
		Workers:         0,
		EnqueueTimeout:  10 * time.Millisecond,
		WriteTimeout:    time.Second,
		MaxRetries:      1,
		MonitorInterval: 0,
	})
	defer svc.Shutdown(context.Background())

	entry := &LogEntry{
		Username:   "admin",
		Module:     "user",
		Action:     "创建",
		Method:     "POST",
		Path:       "/api/v1/users",
		IP:         "127.0.0.1",
		StatusCode: 200,
		DurationMs: 10,
	}

	if err := svc.Log(t.Context(), entry); err != nil {
		t.Fatalf("第一次入队失败: %v", err)
	}
	if err := svc.Log(t.Context(), entry); err == nil {
		t.Fatal("队列满时应返回错误")
	}

	metrics := svc.GetMetrics()
	if metrics["dropped_logs"] != 1 {
		t.Fatalf("期望丢弃日志数为 1，实际为 %d", metrics["dropped_logs"])
	}
	if metrics["queue_size"] != 1 {
		t.Fatalf("期望队列长度为 1，实际为 %d", metrics["queue_size"])
	}
}

func TestOperationLogService_Log_WorkerPersistsAndShutdown(t *testing.T) {
	setupOperationLogTest(t)
	svc := NewOperationLogServiceWithOptions(OperationLogOptions{
		QueueSize:       4,
		Workers:         1,
		EnqueueTimeout:  time.Second,
		WriteTimeout:    time.Second,
		MaxRetries:      1,
		MonitorInterval: 0,
	})

	entry := &LogEntry{
		Username:    "async_user",
		Module:      "user",
		Action:      "创建",
		Description: "异步写入测试",
		Method:      "POST",
		Path:        "/api/v1/users",
		IP:          "127.0.0.1",
		UserAgent:   "test-agent",
		RequestBody: `{"name":"async"}`,
		StatusCode:  201,
		DurationMs:  20,
	}

	if err := svc.Log(t.Context(), entry); err != nil {
		t.Fatalf("日志入队失败: %v", err)
	}
	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭日志服务失败: %v", err)
	}

	count, err := global.ORM.OperationLog.Query().Count(t.Context())
	if err != nil {
		t.Fatalf("查询日志数量失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("期望写入 1 条日志，实际写入 %d 条", count)
	}

	metrics := svc.GetMetrics()
	if metrics["success_logs"] != 1 {
		t.Fatalf("期望成功日志数为 1，实际为 %d", metrics["success_logs"])
	}
}

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"

	_ "github.com/mattn/go-sqlite3"
)

// newTestOperationLogService creates an OperationLogService with test DB
func newTestOperationLogService(t *testing.T) (*OperationLogService, *ent.Client) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	svc := &OperationLogService{client: client}
	return svc, client
}

// createTestLog is a helper that directly creates an operation log in DB
func createTestLog(t *testing.T, client *ent.Client, entry *LogEntry) *ent.OperationLog {
	t.Helper()
	ctx := context.Background()

	builder := client.OperationLog.Create().
		SetUserID(entry.UserID).
		SetUsername(entry.Username).
		SetModule(entry.Module).
		SetAction(entry.Action).
		SetMethod(entry.Method).
		SetPath(entry.Path).
		SetIP(entry.IP).
		SetStatusCode(entry.StatusCode).
		SetDurationMs(entry.DurationMs)

	if entry.Description != "" {
		builder.SetDescription(entry.Description)
	}
	if entry.UserAgent != "" {
		builder.SetUserAgent(entry.UserAgent)
	}
	if entry.RequestBody != "" {
		builder.SetRequestBody(entry.RequestBody)
	}

	l, err := builder.Save(ctx)
	if err != nil {
		t.Fatalf("create test log failed: %v", err)
	}
	return l
}

func TestLogOperation(t *testing.T) {
	svc, client := newTestOperationLogService(t)
	ctx := context.Background()

	// Log is async, so we use it then wait
	err := svc.Log(ctx, &LogEntry{
		UserID:      1,
		Username:    "admin",
		Module:      "用户管理",
		Action:      "创建",
		Description: "创建了用户 test",
		Method:      "POST",
		Path:        "/api/users",
		IP:          "127.0.0.1",
		UserAgent:   "TestAgent",
		StatusCode:  200,
		DurationMs:  50,
	})
	if err != nil {
		t.Fatalf("Log failed: %v", err)
	}

	// Wait for async goroutine to complete
	time.Sleep(500 * time.Millisecond)

	// Verify the log was created
	count, err := client.OperationLog.Query().Count(ctx)
	if err != nil {
		t.Fatalf("Count logs failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 log entry, got %d", count)
	}
}

func TestListOperationLogs(t *testing.T) {
	svc, client := newTestOperationLogService(t)
	ctx := context.Background()

	// Create test logs directly
	for i := 0; i < 5; i++ {
		createTestLog(t, client, &LogEntry{
			UserID:     1,
			Username:   "admin",
			Module:     "用户管理",
			Action:     "查询",
			Method:     "GET",
			Path:       fmt.Sprintf("/api/users/%d", i),
			IP:         "127.0.0.1",
			StatusCode: 200,
			DurationMs: 10,
		})
	}

	resp, err := svc.List(ctx, &ListOperationLogsRequest{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if resp.Total != 5 {
		t.Fatalf("expected total 5, got %d", resp.Total)
	}
	if len(resp.Items) != 5 {
		t.Fatalf("expected 5 items, got %d", len(resp.Items))
	}

	// Test pagination
	resp, err = svc.List(ctx, &ListOperationLogsRequest{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("List with pagination failed: %v", err)
	}
	if resp.Total != 5 {
		t.Fatalf("expected total 5 with pagination, got %d", resp.Total)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("expected 2 items per page, got %d", len(resp.Items))
	}
}

func TestListOperationLogs_FilterByModule(t *testing.T) {
	svc, client := newTestOperationLogService(t)
	ctx := context.Background()

	modules := []string{"用户管理", "角色管理", "用户管理"}
	for i, mod := range modules {
		createTestLog(t, client, &LogEntry{
			UserID:     1,
			Username:   "admin",
			Module:     mod,
			Action:     "查询",
			Method:     "GET",
			Path:       fmt.Sprintf("/api/test/%d", i),
			IP:         "127.0.0.1",
			StatusCode: 200,
			DurationMs: 10,
		})
	}

	resp, err := svc.List(ctx, &ListOperationLogsRequest{Page: 1, PageSize: 10, Module: "用户管理"})
	if err != nil {
		t.Fatalf("List by module failed: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("expected 2 logs for module '用户管理', got %d", resp.Total)
	}
}

func TestListOperationLogs_FilterByAction(t *testing.T) {
	svc, client := newTestOperationLogService(t)
	ctx := context.Background()

	actions := []string{"创建", "更新", "删除", "创建"}
	for i, act := range actions {
		createTestLog(t, client, &LogEntry{
			UserID:     1,
			Username:   "admin",
			Module:     "用户管理",
			Action:     act,
			Method:     "POST",
			Path:       fmt.Sprintf("/api/test/%d", i),
			IP:         "127.0.0.1",
			StatusCode: 200,
			DurationMs: 10,
		})
	}

	resp, err := svc.List(ctx, &ListOperationLogsRequest{Page: 1, PageSize: 10, Action: "创建"})
	if err != nil {
		t.Fatalf("List by action failed: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("expected 2 logs for action '创建', got %d", resp.Total)
	}
}

func TestListOperationLogs_FilterByUsername(t *testing.T) {
	svc, client := newTestOperationLogService(t)
	ctx := context.Background()

	users := []string{"admin", "editor", "admin_test"}
	for i, user := range users {
		createTestLog(t, client, &LogEntry{
			UserID:     i + 1,
			Username:   user,
			Module:     "用户管理",
			Action:     "查询",
			Method:     "GET",
			Path:       fmt.Sprintf("/api/test/%d", i),
			IP:         "127.0.0.1",
			StatusCode: 200,
			DurationMs: 10,
		})
	}

	resp, err := svc.List(ctx, &ListOperationLogsRequest{Page: 1, PageSize: 10, Username: "admin"})
	if err != nil {
		t.Fatalf("List by username failed: %v", err)
	}
	// UsernameContains("admin") matches "admin" and "admin_test"
	if resp.Total != 2 {
		t.Fatalf("expected 2 logs for username containing 'admin', got %d", resp.Total)
	}
}

func TestListOperationLogs_FilterByTimeRange(t *testing.T) {
	svc, client := newTestOperationLogService(t)
	ctx := context.Background()

	// Create logs
	for i := 0; i < 3; i++ {
		createTestLog(t, client, &LogEntry{
			UserID:     1,
			Username:   "admin",
			Module:     "用户管理",
			Action:     "查询",
			Method:     "GET",
			Path:       fmt.Sprintf("/api/test/%d", i),
			IP:         "127.0.0.1",
			StatusCode: 200,
			DurationMs: 10,
		})
	}

	// Use time range that includes now
	now := time.Now()
	startTime := now.Add(-1 * time.Hour).Format(time.RFC3339)
	endTime := now.Add(1 * time.Hour).Format(time.RFC3339)

	resp, err := svc.List(ctx, &ListOperationLogsRequest{
		Page:      1,
		PageSize:  10,
		StartTime: startTime,
		EndTime:   endTime,
	})
	if err != nil {
		t.Fatalf("List by time range failed: %v", err)
	}
	if resp.Total != 3 {
		t.Fatalf("expected 3 logs in time range, got %d", resp.Total)
	}

	// Use future time range - should return 0
	futureStart := now.Add(1 * time.Hour).Format(time.RFC3339)
	futureEnd := now.Add(2 * time.Hour).Format(time.RFC3339)
	resp, err = svc.List(ctx, &ListOperationLogsRequest{
		Page:      1,
		PageSize:  10,
		StartTime: futureStart,
		EndTime:   futureEnd,
	})
	if err != nil {
		t.Fatalf("List by future time range failed: %v", err)
	}
	if resp.Total != 0 {
		t.Fatalf("expected 0 logs in future range, got %d", resp.Total)
	}
}

func TestGetOperationLog(t *testing.T) {
	svc, client := newTestOperationLogService(t)
	ctx := context.Background()

	l := createTestLog(t, client, &LogEntry{
		UserID:      1,
		Username:    "admin",
		Module:      "角色管理",
		Action:      "创建",
		Description: "创建了角色 editor",
		Method:      "POST",
		Path:        "/api/roles",
		IP:          "192.168.1.1",
		UserAgent:   "Mozilla/5.0",
		RequestBody: `{"name":"editor"}`,
		StatusCode:  200,
		DurationMs:  30,
	})

	resp, err := svc.Get(ctx, l.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if resp.UserID != 1 {
		t.Fatalf("expected user_id 1, got %d", resp.UserID)
	}
	if resp.Username != "admin" {
		t.Fatalf("expected username 'admin', got '%s'", resp.Username)
	}
	if resp.Module != "角色管理" {
		t.Fatalf("expected module '角色管理', got '%s'", resp.Module)
	}
	if resp.Action != "创建" {
		t.Fatalf("expected action '创建', got '%s'", resp.Action)
	}
	if resp.Description != "创建了角色 editor" {
		t.Fatalf("expected description '创建了角色 editor', got '%s'", resp.Description)
	}
	if resp.Method != "POST" {
		t.Fatalf("expected method POST, got '%s'", resp.Method)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected status_code 200, got %d", resp.StatusCode)
	}

	// Test not found
	_, err = svc.Get(ctx, 99999)
	if err != ErrOperationLogNotFound {
		t.Fatalf("expected ErrOperationLogNotFound, got: %v", err)
	}
}

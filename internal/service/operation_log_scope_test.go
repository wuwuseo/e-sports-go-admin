package service

import (
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/department"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupOperationLogScopeTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:oplog-scope?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

func createAdminForOperationLogScopeTest(t *testing.T, client *ent.Client, email, uid, username string, deptID int) *ent.Admin {
	t.Helper()
	a, err := client.Admin.Create().
		SetEmail(email).
		SetUID(uid).
		SetUsername(username).
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetDepartmentID(deptID).
		SetStatus(admin.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("创建测试管理员失败: %v", err)
	}
	return a
}

func createDepartmentForOperationLogScopeTest(t *testing.T, client *ent.Client, name string, parentID int) int {
	t.Helper()
	d, err := client.Department.Create().
		SetName(name).
		SetParentID(parentID).
		SetPath("/tmp").
		SetSort(0).
		SetStatus(department.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("创建测试部门失败: %v", err)
	}
	return d.ID
}

func createLogForOperationLogScopeTest(t *testing.T, client *ent.Client, username string) {
	t.Helper()
	_, err := client.OperationLog.Create().
		SetUserID(1).
		SetUsername(username).
		SetModule("测试模块").
		SetAction("创建").
		SetDescription("scope test").
		SetMethod("POST").
		SetPath("/api/v1/test").
		SetIP("127.0.0.1").
		SetStatusCode(200).
		SetDurationMs(10).
		SetCreatedAt(time.Now()).
		Save(t.Context())
	if err != nil {
		t.Fatalf("创建测试日志失败: %v", err)
	}
}

func TestOperationLogService_List_DataScopeSelf(t *testing.T) {
	client := setupOperationLogScopeTest(t)
	ctx := t.Context()
	svc := NewOperationLogServiceWithOptions(OperationLogOptions{Workers: 0, MonitorInterval: 0})
	defer svc.Shutdown(ctx)

	createLogForOperationLogScopeTest(t, client, "self_user")
	createLogForOperationLogScopeTest(t, client, "other_user")

	op := &EffectiveDataScope{Scope: DataScopeSelf, Username: "self_user"}
	resp, err := svc.List(ctx, op, &ListOperationLogsRequest{})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("Self 范围应只看到 1 条日志, 实际 %d", resp.Total)
	}
	if len(resp.Data) != 1 || resp.Data[0].Username != "self_user" {
		t.Errorf("Self 范围应返回 self_user 的日志, 实际 %+v", resp.Data)
	}
}

func TestOperationLogService_List_DataScopeDepartment(t *testing.T) {
	client := setupOperationLogScopeTest(t)
	ctx := t.Context()
	svc := NewOperationLogServiceWithOptions(OperationLogOptions{Workers: 0, MonitorInterval: 0})
	defer svc.Shutdown(ctx)

	deptID := createDepartmentForOperationLogScopeTest(t, client, "Engineering", 0)
	createAdminForOperationLogScopeTest(t, client, "dept1@example.com", "uid-dept1", "dept1_user", deptID)
	createAdminForOperationLogScopeTest(t, client, "dept2@example.com", "uid-dept2", "dept2_user", deptID)
	createAdminForOperationLogScopeTest(t, client, "other@example.com", "uid-other", "other_user", createDepartmentForOperationLogScopeTest(t, client, "Sales", 0))

	createLogForOperationLogScopeTest(t, client, "dept1_user")
	createLogForOperationLogScopeTest(t, client, "dept2_user")
	createLogForOperationLogScopeTest(t, client, "other_user")

	op := &EffectiveDataScope{Scope: DataScopeDepartment, Username: "dept1_user", DepartmentIDs: []int{deptID}}
	resp, err := svc.List(ctx, op, &ListOperationLogsRequest{})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("Department 范围应看到同部门 2 条日志, 实际 %d", resp.Total)
	}
}

func TestOperationLogService_List_DataScopeCustomEmpty(t *testing.T) {
	client := setupOperationLogScopeTest(t)
	ctx := t.Context()
	svc := NewOperationLogServiceWithOptions(OperationLogOptions{Workers: 0, MonitorInterval: 0})
	defer svc.Shutdown(ctx)

	createLogForOperationLogScopeTest(t, client, "any_user")

	op := &EffectiveDataScope{Scope: DataScopeCustom, Username: "operator", DepartmentIDs: []int{}}
	resp, err := svc.List(ctx, op, &ListOperationLogsRequest{})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("Custom 范围部门为空时应看不到任何日志, 实际 %d", resp.Total)
	}
}

func TestOperationLogService_List_DataScopeAll(t *testing.T) {
	client := setupOperationLogScopeTest(t)
	ctx := t.Context()
	svc := NewOperationLogServiceWithOptions(OperationLogOptions{Workers: 0, MonitorInterval: 0})
	defer svc.Shutdown(ctx)

	createLogForOperationLogScopeTest(t, client, "user_a")
	createLogForOperationLogScopeTest(t, client, "user_b")

	op := &EffectiveDataScope{Scope: DataScopeAll, Username: "admin"}
	resp, err := svc.List(ctx, op, &ListOperationLogsRequest{})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("All 范围应看到全部 2 条日志, 实际 %d", resp.Total)
	}
}

func TestOperationLogService_Get_OutOfScope(t *testing.T) {
	client := setupOperationLogScopeTest(t)
	ctx := t.Context()
	svc := NewOperationLogServiceWithOptions(OperationLogOptions{Workers: 0, MonitorInterval: 0})
	defer svc.Shutdown(ctx)

	createLogForOperationLogScopeTest(t, client, "owner")

	logID := client.OperationLog.Query().FirstX(ctx).ID
	op := &EffectiveDataScope{Scope: DataScopeSelf, Username: "attacker"}
	_, err := svc.Get(ctx, op, logID)
	if err != ErrOperationLogNotFound {
		t.Errorf("越权访问应返回 ErrOperationLogNotFound, 实际 %v", err)
	}
}

func TestOperationLogService_Get_InScope(t *testing.T) {
	client := setupOperationLogScopeTest(t)
	ctx := t.Context()
	svc := NewOperationLogServiceWithOptions(OperationLogOptions{Workers: 0, MonitorInterval: 0})
	defer svc.Shutdown(ctx)

	createLogForOperationLogScopeTest(t, client, "owner")

	logID := client.OperationLog.Query().FirstX(ctx).ID
	op := &EffectiveDataScope{Scope: DataScopeSelf, Username: "owner"}
	resp, err := svc.Get(ctx, op, logID)
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if resp.Username != "owner" {
		t.Errorf("应返回 owner 的日志, 实际 %s", resp.Username)
	}
}

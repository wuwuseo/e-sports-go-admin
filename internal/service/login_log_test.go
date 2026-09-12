package service

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/loginlog"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupLoginLogTest(t *testing.T) (*ent.Client, *LoginLogService, func()) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:loginlog?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	prevORM := global.ORM
	global.ORM = client

	svc := NewLoginLogServiceWithOptions(LoginLogOptions{
		QueueSize:      10,
		Workers:        1,
		MaxRetries:     1,
		WriteTimeout:   2 * time.Second,
		EnqueueTimeout: 100 * time.Millisecond,
	})

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = svc.Shutdown(ctx)
		global.ORM = prevORM
		client.Close()
	}
	return client, svc, cleanup
}

func waitForLoginLogCount(t *testing.T, client *ent.Client, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := client.LoginLog.Query().Count(context.Background())
		if err == nil && n == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	n, _ := client.LoginLog.Query().Count(context.Background())
	t.Fatalf("等待 %d 条登录日志超时，当前 %d 条", want, n)
}

func TestLoginLogService_RecordPersistsParsedFields(t *testing.T) {
	client, svc, cleanup := setupLoginLogTest(t)
	defer cleanup()

	err := svc.Record(context.Background(), &LoginLogEntry{
		Username:  "admin",
		UserID:    7,
		LoginType: "admin",
		Status:    "success",
		IP:        "1.2.3.4",
		UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	waitForLoginLogCount(t, client, 1)

	row, err := client.LoginLog.Query().Only(context.Background())
	if err != nil {
		t.Fatalf("查询登录日志失败: %v", err)
	}
	if row.Username != "admin" || row.UserID != 7 || row.LoginType != loginlog.LoginTypeAdmin || row.Status != loginlog.StatusSuccess {
		t.Fatalf("字段不符: %+v", row)
	}
	if row.IP != "1.2.3.4" {
		t.Errorf("ip = %q, want 1.2.3.4", row.IP)
	}
	if row.Browser != "Chrome 126" {
		t.Errorf("browser = %q, want Chrome 126", row.Browser)
	}
	if row.Os != "Windows" {
		t.Errorf("os = %q, want Windows", row.Os)
	}
}

func TestLoginLogService_ListFiltersByScope(t *testing.T) {
	client, svc, cleanup := setupLoginLogTest(t)
	defer cleanup()

	entries := []*LoginLogEntry{
		{Username: "adminA", UserID: 7, LoginType: "admin", Status: "success", IP: "1.1.1.1"},
		{Username: "adminB", UserID: 9, LoginType: "admin", Status: "fail", IP: "2.2.2.2"},
		{Username: "userA", UserID: 7, LoginType: "user", Status: "success", IP: "3.3.3.3"},
	}
	for _, e := range entries {
		if err := svc.Record(context.Background(), e); err != nil {
			t.Fatalf("Record() error = %v", err)
		}
	}
	waitForLoginLogCount(t, client, 3)

	// 超管：不过滤
	resp, err := svc.List(context.Background(), &EffectiveDataScope{IsSuperAdmin: true}, &ListLoginLogsRequest{PaginationRequest: PaginationRequest{Page: 1, PageSize: 10}})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if resp.Total != 3 {
		t.Fatalf("超管应看到全部 3 条，got %d", resp.Total)
	}

	// Self 范围：只能看到自己（user_id=7）的后台管理员登录记录，前台用户登录日志不可见
	resp, err = svc.List(context.Background(), &EffectiveDataScope{Scope: DataScopeSelf, AdminID: 7, AdminUID: "100007"}, &ListLoginLogsRequest{PaginationRequest: PaginationRequest{Page: 1, PageSize: 10}})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("Self 范围应只看到 1 条，got %d", resp.Total)
	}
	if resp.Data[0].Username != "adminA" {
		t.Fatalf("应只看到 adminA 的记录，got %s", resp.Data[0].Username)
	}
}

func TestLoginLogService_ListFiltersByRequest(t *testing.T) {
	client, svc, cleanup := setupLoginLogTest(t)
	defer cleanup()

	entries := []*LoginLogEntry{
		{Username: "adminA", UserID: 7, LoginType: "admin", Status: "success", IP: "1.1.1.1"},
		{Username: "adminB", UserID: 9, LoginType: "admin", Status: "fail", IP: "2.2.2.2", Message: "密码错误"},
	}
	for _, e := range entries {
		if err := svc.Record(context.Background(), e); err != nil {
			t.Fatalf("Record() error = %v", err)
		}
	}
	waitForLoginLogCount(t, client, 2)

	resp, err := svc.List(context.Background(), &EffectiveDataScope{IsSuperAdmin: true}, &ListLoginLogsRequest{PaginationRequest: PaginationRequest{Page: 1, PageSize: 10}, Status: "fail"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if resp.Total != 1 || resp.Data[0].Username != "adminB" {
		t.Fatalf("按状态筛选结果不符: %+v", resp)
	}

	// 详情：范围外的记录视为不存在，避免存在性泄露
	if _, err := svc.Get(context.Background(), &EffectiveDataScope{Scope: DataScopeSelf, AdminID: 7, AdminUID: "100007"}, resp.Data[0].ID); err != ErrLoginLogNotFound {
		t.Fatalf("范围外详情应返回 ErrLoginLogNotFound，got %v", err)
	}
}

func TestLoginLogService_ScopeUsesDatabaseIDs(t *testing.T) {
	client, svc, cleanup := setupLoginLogTest(t)
	defer cleanup()
	ctx := t.Context()
	owner := createAdminForRoleAssignmentScopeTest(t, client, "owner@example.com", "100001", "owner", 10)
	peer := createAdminForRoleAssignmentScopeTest(t, client, "peer@example.com", "100002", "peer", 10)
	outsider := createAdminForRoleAssignmentScopeTest(t, client, "outside@example.com", "100003", "outside", 20)
	ids := []int{owner.ID, peer.ID, outsider.ID, 100001}
	for _, id := range ids {
		client.LoginLog.Create().SetUsername("admin").SetUserID(id).SetLoginType(loginlog.LoginTypeAdmin).
			SetStatus(loginlog.StatusSuccess).SetIP("1.2.3.4").SaveX(ctx)
	}
	for _, scope := range []DataScope{DataScopeSelf, DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom} {
		op := &EffectiveDataScope{Scope: scope, AdminID: owner.ID, AdminUID: owner.UID, DepartmentIDs: []int{10}}
		want := 2
		if scope == DataScopeSelf {
			want = 1
		}
		list, err := svc.List(ctx, op, &ListLoginLogsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if list.Total != want {
			t.Fatalf("scope %s: got %d logs, want %d", scope, list.Total, want)
		}
		for _, row := range client.LoginLog.Query().AllX(ctx) {
			_, err := svc.Get(ctx, op, row.ID)
			allowed := row.UserID == owner.ID || (scope != DataScopeSelf && row.UserID == peer.ID)
			if allowed && err != nil {
				t.Fatalf("own/same-department log missing: %v", err)
			}
			if !allowed && err != ErrLoginLogNotFound {
				t.Fatalf("out-of-scope log visible: %v", err)
			}
		}
	}
}

func TestLoginLogService_TruncatesUntrustedFields(t *testing.T) {
	for _, ua := range []string{strings.Repeat("A", 1025), strings.Repeat("A", 101) + "/1.0", strings.Repeat("界", 400)} {
		client, svc, cleanup := setupLoginLogTest(t)
		entry := &LoginLogEntry{Username: "admin", LoginType: "admin", Status: "fail", IP: "1.2.3.4", UserAgent: ua, Message: strings.Repeat("界", 100)}
		if err := svc.Record(t.Context(), entry); err != nil {
			t.Fatal(err)
		}
		if err := svc.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		row := client.LoginLog.Query().OnlyX(t.Context())
		if len(row.UserAgent) > 1024 || len(row.Browser) > 100 || len(row.Message) > 255 {
			t.Fatalf("oversized persisted fields: ua=%d browser=%d message=%d", len(row.UserAgent), len(row.Browser), len(row.Message))
		}
		if !utf8.ValidString(row.UserAgent) || !utf8.ValidString(row.Message) {
			t.Fatal("truncation split a UTF-8 character")
		}
		if entry.UserAgent != ua {
			t.Fatal("Record mutated its caller's entry")
		}
		cleanup()
	}
}

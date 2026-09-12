package service

import (
	"context"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/loginlog"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/operationlog"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupLogCleanupTest(t *testing.T) (*ent.Client, func()) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:logcleanup?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	prevORM := global.ORM
	global.ORM = client
	return client, func() {
		global.ORM = prevORM
		client.Close()
	}
}

func TestCleanupOperationLogsBefore(t *testing.T) {
	client, cleanup := setupLogCleanupTest(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()
	stale := now.AddDate(0, 0, -40)

	for _, tc := range []struct {
		username  string
		createdAt time.Time
	}{
		{username: "old-op", createdAt: stale},
		{username: "new-op", createdAt: now},
	} {
		if _, err := client.OperationLog.Create().
			SetUserID(1).
			SetUsername(tc.username).SetModule("m").SetAction("a").
			SetMethod("GET").SetPath("/p").SetIP("1.1.1.1").
			SetStatusCode(200).SetDurationMs(0).
			SetCreatedAt(tc.createdAt).
			Save(ctx); err != nil {
			t.Fatalf("插入操作日志失败: %v", err)
		}
	}

	deleted, err := CleanupOperationLogsBefore(ctx, now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("CleanupOperationLogsBefore() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("应删除 1 条过期日志，got %d", deleted)
	}
	remaining, err := client.OperationLog.Query().Where(operationlog.UsernameEQ("new-op")).Count(ctx)
	if err != nil || remaining != 1 {
		t.Fatalf("应保留新日志，got %d err=%v", remaining, err)
	}
}

func TestCleanupLoginLogsBefore(t *testing.T) {
	client, cleanup := setupLogCleanupTest(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()
	stale := now.AddDate(0, 0, -40)

	for _, tc := range []struct {
		username  string
		createdAt time.Time
	}{
		{username: "old-login", createdAt: stale},
		{username: "new-login", createdAt: now},
	} {
		if _, err := client.LoginLog.Create().
			SetUsername(tc.username).SetUserID(1).
			SetLoginType(loginlog.LoginTypeAdmin).SetStatus(loginlog.StatusSuccess).
			SetIP("1.1.1.1").
			SetCreatedAt(tc.createdAt).
			Save(ctx); err != nil {
			t.Fatalf("插入登录日志失败: %v", err)
		}
	}

	deleted, err := CleanupLoginLogsBefore(ctx, now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("CleanupLoginLogsBefore() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("应删除 1 条过期日志，got %d", deleted)
	}
	remaining, err := client.LoginLog.Query().Where(loginlog.UsernameEQ("new-login")).Count(ctx)
	if err != nil || remaining != 1 {
		t.Fatalf("应保留新日志，got %d err=%v", remaining, err)
	}
}

func TestCleanupLogsBefore_NilORMIsNoop(t *testing.T) {
	prevORM := global.ORM
	global.ORM = nil
	defer func() { global.ORM = prevORM }()

	ctx := context.Background()
	if n, err := CleanupOperationLogsBefore(ctx, time.Now()); err != nil || n != 0 {
		t.Fatalf("无 DB 时应为安全 no-op，got n=%d err=%v", n, err)
	}
	if n, err := CleanupLoginLogsBefore(ctx, time.Now()); err != nil || n != 0 {
		t.Fatalf("无 DB 时应为安全 no-op，got n=%d err=%v", n, err)
	}
}

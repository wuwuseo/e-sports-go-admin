package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/department"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

// TestManagedTx_CommitPreventsDoubleCommit 验证重复提交保护
func TestManagedTx_CommitPreventsDoubleCommit(t *testing.T) {
	client := setupManagedTxTest(t)

	tx, err := client.Tx(t.Context())
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}

	mtx := NewManagedTx(tx)
	if err := mtx.Commit(); err != nil {
		t.Fatalf("first commit: %v", err)
	}

	// ponytail: 重复提交应返回明确错误而非静默成功或panic
	err = mtx.Commit()
	if err == nil {
		t.Fatal("expected error on double commit, got nil")
	}
	if err.Error() != "transaction already committed" {
		t.Fatalf("expected 'transaction already committed', got %q", err.Error())
	}
}

// TestManagedTx_RollbackIfNotCommitted 验证未提交时自动回滚
func TestManagedTx_RollbackIfNotCommitted(t *testing.T) {
	client := setupManagedTxTest(t)
	ctx := t.Context()

	dept, err := client.Department.Create().SetName("rollback_test").Save(ctx)
	if err != nil {
		t.Fatalf("create department: %v", err)
	}

	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}

	mtx := NewManagedTx(tx)
	// 在事务中创建部门
	_, err = mtx.Tx().Department.Create().SetName("in_tx_dept").Save(ctx)
	if err != nil {
		t.Fatalf("create in transaction: %v", err)
	}

	// ponytail: 不显式调用Rollback,依赖defer自动回滚
	mtx.RollbackIfNotCommitted()

	// 验证事务已回滚
	exists, err := client.Department.Query().
		Where(department.NameEQ("in_tx_dept")).
		Exist(ctx)
	if err != nil {
		t.Fatalf("check existence: %v", err)
	}
	if exists {
		t.Fatal("expected in_tx_dept to be rolled back")
	}

	// 验证原始数据仍然存在
	stillExists, err := client.Department.Query().
		Where(department.NameEQ(dept.Name)).
		Exist(ctx)
	if err != nil {
		t.Fatalf("check original data: %v", err)
	}
	if !stillExists {
		t.Fatal("original department should still exist")
	}
}

// TestManagedTx_CommitSkipsRollback 验证提交后跳过回滚
func TestManagedTx_CommitSkipsRollback(t *testing.T) {
	client := setupManagedTxTest(t)
	ctx := t.Context()

	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}

	mtx := NewManagedTx(tx)
	deptName := "committed_dept"
	_, err = mtx.Tx().Department.Create().SetName(deptName).Save(ctx)
	if err != nil {
		t.Fatalf("create in transaction: %v", err)
	}

	if err := mtx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// ponytail: 提交后的RollbackIfNotCommitted应该是空操作
	mtx.RollbackIfNotCommitted()

	// 验证数据已持久化
	exists, err := client.Department.Query().
		Where(department.NameEQ(deptName)).
		Exist(ctx)
	if err != nil {
		t.Fatalf("check existence: %v", err)
	}
	if !exists {
		t.Fatal("committed department should exist after rollback skip")
	}
}

// TestManagedTx_RollbackOnError 验证错误时回滚保护数据
func TestManagedTx_RollbackOnError(t *testing.T) {
	client := setupManagedTxTest(t)
	ctx := t.Context()

	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}

	mtx := NewManagedTx(tx)
	deptName := "should_not_persist"
	_, err = mtx.Tx().Department.Create().SetName(deptName).Save(ctx)
	if err != nil {
		t.Fatalf("create in transaction: %v", err)
	}

	// ponytail: 模拟业务逻辑错误触发回滚
	simulatedError := errors.New("business logic failed")
	if simulatedError != nil {
		mtx.RollbackIfNotCommitted()
	}

	// 验证数据已回滚
	exists, err := client.Department.Query().
		Where(department.NameEQ(deptName)).
		Exist(ctx)
	if err != nil {
		t.Fatalf("check existence: %v", err)
	}
	if exists {
		t.Fatal("department should be rolled back on error")
	}
}

func setupManagedTxTest(t *testing.T) *ent.Client {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:managedtx?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

package esports

import (
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
)

// openWalletTestDB 打开一个隔离的内存库，并写入支付时限配置（多数资金链路测试都要求它非零）。
// 原先由储值卡测试文件提供的同名 opener 已随该功能删除，这里保留最小实现供余额 / 账本 / 充值等
// 保留功能的测试继续使用。
func openWalletTestDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(t.Context())
	return db
}

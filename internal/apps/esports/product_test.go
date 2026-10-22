package esports

import (
	_ "github.com/mattn/go-sqlite3"
	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"testing"
)

func openProductDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(t.Context())
	return db
}

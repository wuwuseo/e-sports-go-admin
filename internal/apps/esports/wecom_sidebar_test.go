package esports

import (
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/systemconfig"
)

// 企微侧边栏接入模式：非法 / 缺失回落 account（默认账号登录），js_sdk / account 精确命中。
func TestWecomSidebarAuthMode(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_wecom_sidebar?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	// 缺省回落 account。
	if mode := wecomSidebarAuthMode(ctx, db); mode != wecomAuthModeAccount {
		t.Fatalf("缺失配置应回落 account，实际 %s", mode)
	}

	// js_sdk 精确命中。
	if err := db.SystemConfig.Create().SetKey(wecomSidebarAuthModeKey).
		SetName("企微侧边栏接入模式").SetValue("js_sdk").SetType("string").
		SetGroup("esports").SetStatus(1).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if mode := wecomSidebarAuthMode(ctx, db); mode != wecomAuthModeJsSDK {
		t.Fatalf("js_sdk 配置应命中，实际 %s", mode)
	}

	// account 精确命中。
	if _, err := db.SystemConfig.Update().Where(systemconfig.KeyEQ(wecomSidebarAuthModeKey)).
		SetValue("account").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if mode := wecomSidebarAuthMode(ctx, db); mode != wecomAuthModeAccount {
		t.Fatalf("account 配置应命中，实际 %s", mode)
	}

	// 非法值回落 account。
	if _, err := db.SystemConfig.Update().Where(systemconfig.KeyEQ(wecomSidebarAuthModeKey)).
		SetValue("unknown").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if mode := wecomSidebarAuthMode(ctx, db); mode != wecomAuthModeAccount {
		t.Fatalf("非法值应回落 account，实际 %s", mode)
	}
}

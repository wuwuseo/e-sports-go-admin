package esports

import (
	"errors"
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

// JS-SDK 签名：用企微 / 微信官方文档示例向量校验 sha1 算法与字段拼接顺序。
func TestWecomJsSignature(t *testing.T) {
	const (
		ticket    = "sM4AOVdWfPE4DxkXGEs8VMCPGGVi4C3VM0P37wVUCFvkVAy_90u5h9nbSlYy3-Sl-HhTdfl2fzFy1AOcHKP7qg"
		nonceStr  = "Wm3WZYTPz0wzccnW"
		timestamp = "1414587457"
		url       = "http://mp.weixin.qq.com?params=value"
		want      = "0f9de62fce790f9a083d5c99e95740ceb90c27ed"
	)
	if got := wecomJsSignature(ticket, nonceStr, timestamp, url); got != want {
		t.Fatalf("签名不匹配：want %s got %s", want, got)
	}
}

// JS-SDK 凭据读取：任一缺失返回 errWecomNotConfigured（wraps errNotConfigured），齐备返回凭据。
func TestLoadWecomJsSdkCredential(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_wecom_js_cred?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	// 缺省：未配置。
	if _, err := loadWecomJsSdkCredential(ctx, db); !errors.Is(err, errNotConfigured) {
		t.Fatalf("缺失配置应返回 errNotConfigured，实际 %v", err)
	}

	seed := func(key, value string) {
		if err := db.SystemConfig.Create().SetKey(key).SetName("企微配置").
			SetValue(value).SetType("string").SetGroup("esports").SetStatus(1).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	seed(wecomCorpIDKey, "ww1234567890")
	seed(wecomAgentIDKey, "1000002")
	seed(wecomSecretKey, "secret")
	seed(wecomJsapiTicketKey, "ticket")

	cred, err := loadWecomJsSdkCredential(ctx, db)
	if err != nil {
		t.Fatalf("齐备配置应成功，实际 %v", err)
	}
	if cred.CorpID != "ww1234567890" || cred.AgentID != "1000002" ||
		cred.Secret != "secret" || cred.Ticket != "ticket" {
		t.Fatalf("凭据读取不一致：%+v", cred)
	}
}

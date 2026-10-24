package esports

import (
	"encoding/hex"
	"testing"

	cmfapp "github.com/wuwuseo/cmf/app"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/systemconfig"
)

// 页面预览签名密钥不能存源码固定值：首次播种随机生成、重复播种不轮换、
// 管理员已配置的值（含历史空值补齐）不被覆盖。
func TestSeedPagePreviewSecret(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:page_preview_secret_seed?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	const key = "app.esports.page_preview_secret"
	item := cmfapp.ConfigItem{Key: "page_preview_secret", Name: "页面预览签名密钥", Type: "string", Group: "esports"}
	seed := func() string {
		t.Helper()
		if err := seedPagePreviewSecret(t.Context(), client, key, item); err != nil {
			t.Fatal(err)
		}
		return client.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).OnlyX(t.Context()).Value
	}
	checkRandom := func(value string) {
		t.Helper()
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 {
			t.Fatalf("预览密钥应是 32 字节随机值的 hex 编码，长度=%d，解码错误=%v", len(decoded), err)
		}
	}
	first := seed()
	checkRandom(first)
	if got := seed(); got != first {
		t.Fatal("重复播种不应轮换已有密钥")
	}
	client.SystemConfig.Update().Where(systemconfig.KeyEQ(key)).SetValue("custom-secret").ExecX(t.Context())
	if got := seed(); got != "custom-secret" {
		t.Fatal("重复播种不应覆盖管理员配置的密钥")
	}
	client.SystemConfig.Update().Where(systemconfig.KeyEQ(key)).SetValue(" \t ").ExecX(t.Context())
	backfilled := seed()
	checkRandom(backfilled)
	if backfilled == first {
		t.Fatal("历史空值应生成新的随机密钥")
	}
	if count := client.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).CountX(t.Context()); count != 1 {
		t.Fatalf("预览密钥配置行数=%d，期望 1", count)
	}
}

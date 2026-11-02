package esports

import (
	"fmt"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/user"
)

// TestEsportsRealtimeAddresses 验证 ticket 地址解析：客户 / 陪玩前缀与大厅房间绑定。
func TestEsportsRealtimeAddresses(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_realtime_addresses?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	// 只有客户档案
	customerUser := db.User.Create().SetUID("rt-customer").SetEmail("rt-customer@example.com").
		SetStatus(user.StatusActive).SaveX(ctx)
	customer := db.EsportsCustomer.Create().SetUserID(customerUser.ID).SetDisplayName("客户").SaveX(ctx)

	// 只有陪玩档案
	playerUser := db.User.Create().SetUID("rt-player").SetEmail("rt-player@example.com").
		SetStatus(user.StatusActive).SaveX(ctx)
	player := seedPlayer(t, db, playerUser.ID, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// 双身份
	dualUser := db.User.Create().SetUID("rt-dual").SetEmail("rt-dual@example.com").
		SetStatus(user.StatusActive).SaveX(ctx)
	dualCustomer := db.EsportsCustomer.Create().SetUserID(dualUser.ID).SetDisplayName("客户").SaveX(ctx)
	dualPlayer := seedPlayer(t, db, dualUser.ID, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// 有账号但无任何业务档案
	db.User.Create().SetUID("rt-plain").SetEmail("rt-plain@example.com").
		SetStatus(user.StatusActive).SaveX(ctx)

	cases := []struct {
		name string
		uid  string
		want []string
	}{
		{"只有客户档案", "rt-customer", []string{fmt.Sprintf("customer:%d", customer.ID)}},
		{"只有陪玩档案", "rt-player", []string{fmt.Sprintf("player:%d", player.ID), hallRoom}},
		{"双身份", "rt-dual", []string{
			fmt.Sprintf("customer:%d", dualCustomer.ID),
			fmt.Sprintf("player:%d", dualPlayer.ID),
			hallRoom,
		}},
		{"无业务档案", "rt-plain", nil},
		{"未知 uid", "rt-missing", nil},
		{"空 uid", "", nil},
	}
	for _, tc := range cases {
		got, err := realtimeAddresses(ctx, db, tc.uid)
		if err != nil {
			t.Fatalf("%s：解析失败 %v", tc.name, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s：地址 = %v，期望 %v", tc.name, got, tc.want)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("%s：地址 = %v，期望 %v", tc.name, got, tc.want)
			}
		}
	}
}

package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsmemberlevelrule"
	"go-admin/internal/ent/migrate"
)

// openRound4MemberDB 是本轮会员/钱包用例的独立内存库（每个用例唯一 DSN，避免互相污染）。
func openRound4MemberDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// 五档等级幂等播种（§10.1-01）：新库播种后恰好五档、门槛递增且最低档门槛为 0，
// 新客户结单即可被命中；重复播种不新增、不覆盖运营对已有等级的改动。
func TestSeedMemberLevelsIdempotentFiveTiers(t *testing.T) {
	db := openRound4MemberDB(t, "r4wm_member_seed")
	defer db.Close()
	ctx := t.Context()

	created, err := SeedMemberLevels(ctx, db)
	if err != nil {
		t.Fatalf("播种会员等级失败：%v", err)
	}
	if created != 5 {
		t.Fatalf("首次播种应新建五档：得到 %d 档", created)
	}
	if count := db.EsportsMemberLevelRule.Query().CountX(ctx); count != 5 {
		t.Fatalf("播种后等级规则应为 5 条：得到 %d 条", count)
	}

	want := []struct {
		code string
		name string
	}{
		{"normal", "普通"}, {"silver", "银卡"}, {"gold", "金卡"},
		{"platinum", "铂金"}, {"diamond", "钻石"},
	}
	var lastThreshold int64 = -1
	for _, item := range want {
		rule, err := db.EsportsMemberLevelRule.Query().
			Where(esportsmemberlevelrule.CodeEQ(item.code)).Only(ctx)
		if err != nil {
			t.Fatalf("缺少等级 %s：%v", item.code, err)
		}
		if rule.Name != item.name || rule.Status != esportsmemberlevelrule.StatusEnabled {
			t.Fatalf("等级 %s 的名称/状态不正确：%+v", item.code, rule)
		}
		if rule.GrowthThreshold <= lastThreshold {
			t.Fatalf("等级门槛未按档递增：%s=%d（上一档 %d）", item.code, rule.GrowthThreshold, lastThreshold)
		}
		lastThreshold = rule.GrowthThreshold
	}
	if lowest, err := memberLevelForGrowth(ctx, db, 0); err != nil || lowest == nil {
		t.Fatalf("最低档门槛应为 0，任意客户都应命中等级：%+v %v", lowest, err)
	}

	// 幂等：手工改一档名称/门槛并停用另一档，重复播种不得新增或覆盖。
	normal := db.EsportsMemberLevelRule.Query().Where(esportsmemberlevelrule.CodeEQ("normal")).OnlyX(ctx)
	if _, err := updateMemberLevel(ctx, db, normal.ID, normal.Version, memberLevelInput{
		Code: "normal", Name: "普通（改）", GrowthThreshold: 0, DiscountBps: 10000,
	}); err != nil {
		t.Fatalf("运营改动等级失败：%v", err)
	}
	diamond := db.EsportsMemberLevelRule.Query().Where(esportsmemberlevelrule.CodeEQ("diamond")).OnlyX(ctx)
	if _, err := setMemberLevelStatus(ctx, db, diamond.ID, diamond.Version, "disabled"); err != nil {
		t.Fatalf("停用等级失败：%v", err)
	}
	again, err := SeedMemberLevels(ctx, db)
	if err != nil || again != 0 {
		t.Fatalf("重复播种应为空操作：新建 %d 档 err=%v", again, err)
	}
	if count := db.EsportsMemberLevelRule.Query().CountX(ctx); count != 5 {
		t.Fatalf("重复播种后等级规则应仍为 5 条：得到 %d 条", count)
	}
	kept := db.EsportsMemberLevelRule.Query().Where(esportsmemberlevelrule.CodeEQ("normal")).OnlyX(ctx)
	if kept.Name != "普通（改）" {
		t.Fatalf("重复播种覆盖了运营改动：%+v", kept)
	}
	keptDiamond := db.EsportsMemberLevelRule.Query().Where(esportsmemberlevelrule.CodeEQ("diamond")).OnlyX(ctx)
	if keptDiamond.Status != esportsmemberlevelrule.StatusDisabled {
		t.Fatalf("重复播种未尊重运维启停：%+v", keptDiamond)
	}
}

// 播种后新客户结单可被等级命中（§10.1-01 验收：不出现「等级表为空 → 永不定级」）。
func TestSeedMemberLevelsLevelsUpFreshCustomerOnClose(t *testing.T) {
	db := openRound4MemberDB(t, "r4wm_member_seed_close")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	if _, err := SeedMemberLevels(ctx, db); err != nil {
		t.Fatalf("播种会员等级失败：%v", err)
	}
	customer := saveCustomerOrFail(t, db, 11)
	order := seedCloseableMemberOrder(t, db, customer.ID, "r4wm-seed-close-1", 10000)
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "播种后结单"}, now); err != nil {
		t.Fatalf("结单失败：%v", err)
	}
	got := db.EsportsCustomer.GetX(ctx, customer.ID)
	if got.MemberLevelCode != "silver" {
		t.Fatalf("新客户结单后未被播种等级命中：%+v", got)
	}
}

package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// 阶梯费率（§11.2）：手续费与代扣税费按金额命中档位（取「min_cents ≤ 金额」的最高档）。
func TestWithdrawalFeeTiers(t *testing.T) {
	db := openWithdrawalDB(t, "esports_withdrawal_tiers")
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9101, 300000)
	now := time.Now()

	// 手续费：≥0 收 1%，≥50000 收 0.5%；税费：≥0 收 0.2%。
	setWithdrawalConfig(t, db, withdrawalFeeTiersKey,
		`[{"min_cents":0,"bp":100},{"min_cents":50000,"bp":50}]`)
	setWithdrawalConfig(t, db, withdrawalTaxTiersKey, `[{"min_cents":0,"bp":20}]`)

	// 10000 → 手续费 100（1%）、税费 20（0.2%）、到账 9880。
	low, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-tier-0001", 10000), now)
	if err != nil {
		t.Fatalf("申请应成功，err=%v", err)
	}
	if low.FeeCents != 100 || low.TaxCents != 20 || low.NetCents != 9880 {
		t.Fatalf("低档费率不正确：%+v", low)
	}

	// 100000 → 手续费 500（命中 0.5% 档）、税费 200、到账 99300。
	high, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-tier-0002", 100000), now)
	if err != nil {
		t.Fatalf("申请应成功，err=%v", err)
	}
	if high.FeeCents != 500 || high.TaxCents != 200 || high.NetCents != 99300 {
		t.Fatalf("高档费率不正确：%+v", high)
	}
}

// 阶梯配置缺失或非法时回落单一基点；tierBpFor / parseWithdrawalTiers 纯函数口径。
func TestWithdrawalTierFallbackAndParse(t *testing.T) {
	db := openWithdrawalDB(t, "esports_withdrawal_tier_fallback")
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9102, 300000)
	now := time.Now()

	setWithdrawalConfig(t, db, withdrawalFeeBpKey, "100")
	// 非法 JSON、越界基点、空数组、负金额都应视为未配置 → 回落单一 1%。
	for i, raw := range []string{"not-json", `[{"min_cents":0,"bp":11000}]`, "[]", `[{"min_cents":-1,"bp":10}]`} {
		setWithdrawalConfig(t, db, withdrawalFeeTiersKey, raw)
		key := "wd-tier-fallback-" + string(rune('a'+i)) + "-0001"
		item, err := applyWithdrawal(ctx, db, player.ID, validApply(key, 10000), now)
		if err != nil {
			t.Fatalf("第 %d 组非法阶梯应回落单一费率，err=%v", i+1, err)
		}
		if item.FeeCents != 100 {
			t.Fatalf("第 %d 组应回落单一费率 1%%，got fee=%d", i+1, item.FeeCents)
		}
	}

	// 纯函数：取「min_cents ≤ 金额」的最高档。
	tiers := []withdrawalTier{{MinCents: 0, Bp: 100}, {MinCents: 50000, Bp: 50}, {MinCents: 200000, Bp: 10}}
	for _, c := range []struct {
		amount int64
		want   int
	}{
		{0, 100}, {49999, 100}, {50000, 50}, {199999, 50}, {200000, 10}, {500000, 10},
	} {
		got, ok := tierBpFor(tiers, c.amount)
		if !ok || got != c.want {
			t.Fatalf("金额 %d 期望基点 %d，实际 %d/%v", c.amount, c.want, got, ok)
		}
	}
	if _, ok := tierBpFor(nil, 10000); ok {
		t.Fatal("空阶梯不应命中")
	}
	if parseWithdrawalTiers("  ") != nil {
		t.Fatal("空串应视为未配置")
	}
	if len(parseWithdrawalTiers(`[{"min_cents":0,"bp":100}]`)) != 1 {
		t.Fatal("合法阶梯应解析为一档")
	}
}

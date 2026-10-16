package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportscommissionrule"
	"go-admin/internal/ent/esportscscommissionrule"
)

// 陪玩分成规则预设生效（§12.4）：未来时间激活为 scheduled，旧版保持 active；
// 到点前解析仍取旧版；到点后即使激活任务未跑也直接命中新版本；任务补写状态并归档旧版。
func TestCommissionRuleScheduledEffectiveAt(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_schedule")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	v1 := seedActiveRule(t, db, ruleInput{
		RuleNo: "SC", Scope: "global", CalcMode: "percent", ValueBp: 1000, UnitBasis: "sku_quantity",
	})
	future := now.Add(time.Hour)
	v2draft, err := createCommissionRule(ctx, db, ruleInput{
		RuleNo: "SC", Scope: "global", CalcMode: "percent", ValueBp: 1500, UnitBasis: "sku_quantity",
		EffectiveAt: &future,
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := activateCommissionRule(ctx, db, v2draft.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Status != esportscommissionrule.StatusScheduled {
		t.Fatalf("未来生效时间应转 scheduled，got=%s", v2.Status)
	}
	if got := db.EsportsCommissionRule.GetX(ctx, v1.ID); got.Status != esportscommissionrule.StatusActive {
		t.Fatalf("旧版应保持 active，got=%s", got.Status)
	}

	order := seedCloseableOrder(t, db, "sched-rule-1", 2000, []segSeed{{1, 1, 600}})
	resolve := func(at time.Time) int {
		t.Helper()
		rule, err := resolveCommissionRuleForPlayerIn(ctx, db.EsportsCommissionRule, db.EsportsSku,
			db.EsportsProductCategory, order, 1, at)
		if err != nil {
			t.Fatal(err)
		}
		if rule == nil {
			t.Fatal("应解析到全局规则，got=nil")
		}
		return rule.ID
	}
	if id := resolve(now.Add(30 * time.Minute)); id != v1.ID {
		t.Fatalf("生效前应解析到旧版 v1，got=%d", id)
	}
	if id := resolve(now.Add(2 * time.Hour)); id != v2.ID {
		t.Fatalf("到点后应直接命中 scheduled 新版 v2，got=%d", id)
	}

	count, err := activateDueCommissionRules(ctx, db, now.Add(2*time.Hour), 200)
	if err != nil || count != 1 {
		t.Fatalf("激活任务应处理 1 条，count=%d err=%v", count, err)
	}
	if got := db.EsportsCommissionRule.GetX(ctx, v1.ID); got.Status != esportscommissionrule.StatusHistory {
		t.Fatalf("任务执行后旧版应转 history，got=%s", got.Status)
	}
	if got := db.EsportsCommissionRule.GetX(ctx, v2.ID); got.Status != esportscommissionrule.StatusActive {
		t.Fatalf("任务执行后新版应转 active，got=%s", got.Status)
	}
	if count, err = activateDueCommissionRules(ctx, db, now.Add(3*time.Hour), 200); err != nil || count != 0 {
		t.Fatalf("重复执行应无新增，count=%d err=%v", count, err)
	}
}

// 防御：缺生效时间的 scheduled 版本不参与解析，也不会被激活任务处理。
func TestCommissionRuleScheduledWithoutEffectiveAtIsIgnored(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_schedule_nil")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	v1 := seedActiveRule(t, db, ruleInput{
		RuleNo: "SCN", Scope: "global", CalcMode: "percent", ValueBp: 1000, UnitBasis: "sku_quantity",
	})
	db.EsportsCommissionRule.Create().SetRuleNo("SCN").SetVersion(2).
		SetStatus(esportscommissionrule.StatusScheduled).
		SetScope(esportscommissionrule.ScopeGlobal).
		SetCalcMode(esportscommissionrule.CalcModePercent).
		SetValueBp(2000).SetUnitBasis(esportscommissionrule.UnitBasisSkuQuantity).
		SetCreatedByAdminID(11).SaveX(ctx)

	order := seedCloseableOrder(t, db, "sched-rule-nil", 2000, []segSeed{{1, 1, 600}})
	rule, err := resolveCommissionRuleForPlayerIn(ctx, db.EsportsCommissionRule, db.EsportsSku,
		db.EsportsProductCategory, order, 1, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rule == nil || rule.ID != v1.ID {
		t.Fatalf("缺生效时间的 scheduled 版本不应命中，got=%v", rule)
	}
	if count, err := activateDueCommissionRules(ctx, db, now.Add(time.Hour), 200); err != nil || count != 0 {
		t.Fatalf("激活任务不应处理缺生效时间的版本，count=%d err=%v", count, err)
	}
}

// 客服提成规则预设生效（§12.4）：与陪玩分成使用相同的 scheduled 语义。
func TestCsCommissionRuleScheduledEffectiveAt(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_commission_schedule")
	ctx := t.Context()
	admin := seedAdmin(t, db, "cs-sched-admin")
	now := time.Now().UTC()

	v1, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-SC", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 500, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, v1.ID, admin.ID, now); err != nil {
		t.Fatal(err)
	}
	future := now.Add(time.Hour)
	v2draft, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-SC", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 800, Period: "monthly", EffectiveAt: &future,
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := activateCsCommissionRule(ctx, db, v2draft.ID, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Status != esportscscommissionrule.StatusScheduled {
		t.Fatalf("未来生效时间应转 scheduled，got=%s", v2.Status)
	}
	if got := db.EsportsCsCommissionRule.GetX(ctx, v1.ID); got.Status != esportscscommissionrule.StatusActive {
		t.Fatalf("旧版应保持 active，got=%s", got.Status)
	}

	resolve := func(at time.Time) int {
		t.Helper()
		rule, err := resolveCsCommissionRuleIn(ctx, db.EsportsCsCommissionRule, admin.ID, at)
		if err != nil {
			t.Fatal(err)
		}
		if rule == nil {
			t.Fatal("应解析到全局规则，got=nil")
		}
		return rule.ID
	}
	if id := resolve(now.Add(30 * time.Minute)); id != v1.ID {
		t.Fatalf("生效前应解析到旧版 v1，got=%d", id)
	}
	if id := resolve(now.Add(2 * time.Hour)); id != v2.ID {
		t.Fatalf("到点后应直接命中 scheduled 新版 v2，got=%d", id)
	}

	count, err := activateDueCsCommissionRules(ctx, db, now.Add(2*time.Hour), 200)
	if err != nil || count != 1 {
		t.Fatalf("激活任务应处理 1 条，count=%d err=%v", count, err)
	}
	if got := db.EsportsCsCommissionRule.GetX(ctx, v1.ID); got.Status != esportscscommissionrule.StatusHistory {
		t.Fatalf("任务执行后旧版应转 history，got=%s", got.Status)
	}
	if got := db.EsportsCsCommissionRule.GetX(ctx, v2.ID); got.Status != esportscscommissionrule.StatusActive {
		t.Fatalf("任务执行后新版应转 active，got=%s", got.Status)
	}
}

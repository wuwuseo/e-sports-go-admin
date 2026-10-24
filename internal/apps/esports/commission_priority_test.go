package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionrule"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
)

// 五级优先级：单笔手动指定 > 陪玩个体约定(player) > 陪玩等级(level) > 游戏或服务类型(game) > 全局默认。
func TestCommissionRuleFiveLevelPriority(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_priority")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	sku := seedGameSku(t, db, "PRIO", "LOL")

	global := seedActiveRule(t, db, ruleInput{
		RuleNo: "P-G", Scope: "global", CalcMode: "percent", ValueBp: 1000, UnitBasis: "sku_quantity"})
	game := seedActiveRule(t, db, ruleInput{
		RuleNo: "P-GAME", Scope: "game", ScopeKey: "LOL", CalcMode: "percent", ValueBp: 1100, UnitBasis: "sku_quantity"})
	level := seedActiveRule(t, db, ruleInput{
		RuleNo: "P-LV", Scope: "level", ScopeKey: "senior", CalcMode: "percent", ValueBp: 1200, UnitBasis: "sku_quantity"})
	player := seedActiveRule(t, db, ruleInput{
		RuleNo: "P-PL", Scope: "player", ScopeKey: "7", CalcMode: "percent", ValueBp: 1300, UnitBasis: "sku_quantity"})
	manual := seedActiveRule(t, db, ruleInput{
		RuleNo: "P-MAN", Scope: "global", CalcMode: "fixed_per_order", ValueCents: 900, UnitBasis: "sku_quantity"})

	order := seedCloseableOrder(t, db, "prio-1", 2000, []segSeed{{7, 1, 600}})
	order = db.EsportsOrder.UpdateOneID(order.ID).SetSkuID(sku.ID).SaveX(ctx)

	resolve := func(o *ent.EsportsOrder, playerID int, playerLevel string) *ent.EsportsCommissionRule {
		t.Helper()
		rule, err := resolveCommissionRuleForPlayerIn(ctx, db.EsportsCommissionRule, db.EsportsSku,
			db.EsportsProductCategory, o, playerID, playerLevel, now)
		if err != nil {
			t.Fatal(err)
		}
		return rule
	}

	// 第二级：陪玩个体约定压过等级 / 游戏 / 全局。
	if got := resolve(order, 7, "senior"); got == nil || got.ID != player.ID {
		t.Fatalf("有个体约定时应命中 player 级，got=%v", got)
	}
	// 第三级：该陪玩无个体约定时按等级自动命中（等级与分成联动）。
	if got := resolve(order, 8, "senior"); got == nil || got.ID != level.ID {
		t.Fatalf("无个体约定时应命中 level 级，got=%v", got)
	}
	// 第四级：等级未配规则时回落游戏分区。
	if got := resolve(order, 8, "novice"); got == nil || got.ID != game.ID {
		t.Fatalf("无等级规则时应命中 game 级，got=%v", got)
	}
	// 第五级：SKU 无分类（解析不到游戏）时回落全局默认。
	plainSku, err := saveSku(ctx, db, skuInput{
		Code: "PRIO-PLAIN", Title: "商品", ServiceType: "play",
		UnitPriceCents: 1000, ServiceUnits: 1, ServiceUnitName: "局",
		RequiredSeats: 1, QuotaTotal: 10, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	plain := seedCloseableOrder(t, db, "prio-plain", 2000, []segSeed{{8, 1, 600}})
	plain = db.EsportsOrder.UpdateOneID(plain.ID).SetSkuID(plainSku.ID).SaveX(ctx)
	if got := resolve(plain, 8, "novice"); got == nil || got.ID != global.ID {
		t.Fatalf("解析不到游戏时应回落 global，got=%v", got)
	}

	// 第一级：单笔手动指定覆盖其余四级。
	if _, err := setOrderManualCommissionRule(ctx, db, order.ID, manual.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	order = db.EsportsOrder.GetX(ctx, order.ID)
	if got := resolve(order, 7, "senior"); got == nil || got.ID != manual.ID {
		t.Fatalf("手动指定应压过个体约定，got=%v", got)
	}

	// 清除手动指定后恢复逐级回落。
	if _, err := setOrderManualCommissionRule(ctx, db, order.ID, 0, 11, now); err != nil {
		t.Fatal(err)
	}
	order = db.EsportsOrder.GetX(ctx, order.ID)
	if got := resolve(order, 7, "senior"); got == nil || got.ID != player.ID {
		t.Fatalf("清除后应回落到个体约定，got=%v", got)
	}
}

// 手动指定只在成员锁定规则前生效，且不接受草稿 / 历史版本与终态订单。
func TestManualCommissionRuleGuards(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_manual_guard")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	active := seedActiveRule(t, db, ruleInput{
		RuleNo: "M-A", Scope: "global", CalcMode: "percent", ValueBp: 1000, UnitBasis: "sku_quantity"})
	draft, err := createCommissionRule(ctx, db, ruleInput{
		RuleNo: "M-D", Scope: "global", CalcMode: "percent", ValueBp: 1100, UnitBasis: "sku_quantity"}, 11, now)
	if err != nil {
		t.Fatal(err)
	}

	order := seedCloseableOrder(t, db, "manual-guard-1", 2000, []segSeed{{7, 1, 600}})
	if _, err := setOrderManualCommissionRule(ctx, db, order.ID, draft.ID, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("草稿版本不应可作为手动指定，err=%v", err)
	}
	if _, err := setOrderManualCommissionRule(ctx, db, order.ID, 99999, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("不存在的规则应被拒，err=%v", err)
	}
	if _, err := setOrderManualCommissionRule(ctx, db, order.ID, active.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.ManualCommissionRuleID == nil ||
		*got.ManualCommissionRuleID != active.ID || got.ManualCommissionRuleSetByAdminID == nil {
		t.Fatalf("手动指定未连同操作人写入订单：%+v", got)
	}

	// 已有锁定席位的订单不再允许改（关键时点锁定）。
	db.EsportsDispatchSeat.Create().SetOrderID(order.ID).SetSeatIndex(1).
		SetStatus(esportsdispatchseat.StatusAccepted).SetPlayerID(7).
		SetCommissionRuleLockedAt(now).SaveX(ctx)
	if _, err := setOrderManualCommissionRule(ctx, db, order.ID, 0, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("已锁定席位的订单应拒绝改动手动指定，err=%v", err)
	}

	// 终态订单直接拒绝。
	done := seedCloseableOrder(t, db, "manual-guard-done", 2000, []segSeed{{7, 1, 600}})
	db.EsportsOrder.UpdateOneID(done.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).SaveX(ctx)
	if _, err := setOrderManualCommissionRule(ctx, db, done.ID, active.ID, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("已完成订单应拒绝改动手动指定，err=%v", err)
	}
}

// level 级规则的 scope_key 必须是陪玩等级枚举值，否则永远解析不到。
func TestCommissionRuleLevelScopeValidation(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_level_scope")
	defer db.Close()
	ctx := t.Context()

	if _, err := createCommissionRule(ctx, db, ruleInput{
		RuleNo: "LV-BAD", Scope: "level", ScopeKey: "gold",
		CalcMode: "percent", ValueBp: 1000, UnitBasis: "sku_quantity"}, 11, time.Now()); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法等级值应被拒，err=%v", err)
	}
	rule, err := createCommissionRule(ctx, db, ruleInput{
		RuleNo: "LV-OK", Scope: "level", ScopeKey: "mentor",
		CalcMode: "percent", ValueBp: 1000, UnitBasis: "sku_quantity"}, 11, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rule.Scope != esportscommissionrule.ScopeLevel || rule.ScopeKey != "mentor" {
		t.Fatalf("等级规则未按预期保存：%+v", rule)
	}
	// 非 global 必须带键：level 缺 scope_key 同样非法。
	if _, err := createCommissionRule(ctx, db, ruleInput{
		RuleNo: "LV-EMPTY", Scope: "level",
		CalcMode: "percent", ValueBp: 1000, UnitBasis: "sku_quantity"}, 11, time.Now()); !errors.Is(err, errInvalidInput) {
		t.Fatalf("缺等级键应被拒，err=%v", err)
	}
}

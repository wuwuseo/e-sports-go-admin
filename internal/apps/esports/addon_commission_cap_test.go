package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscommissionrule"
	"go-admin/internal/ent/esportsorderaddon"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsplayer"
)

// addonEntriesOfOrder 取某订单的加钟分成明细。
func addonEntriesOfOrder(t *testing.T, db *ent.Client, orderID int) []*ent.EsportsCommissionEntry {
	t.Helper()
	return db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(orderID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindAddon),
	).AllX(t.Context())
}

// P0-5 回归：加钟固定额分成超过本次加钟本金时，按本金截断（不拒绝、不透支），
// 截断留痕在 order.addon_confirmed 事件里，收款计提额同样不超过加钟本金。
func TestAddonFixedCommissionCappedAtAddedCents(t *testing.T) {
	db := openAddonDB(t, "esports_addon_commission_cap")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)
	player := seedPlayer(t, db, 9501, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedInServiceOrder(t, db, "addon-cap-1", player.ID)
	// 每单固定 50 元：加钟只收 40 元时原口径会算出 50 元分成（超本金 10 元）。
	db.EsportsCommissionRule.Create().SetRuleNo("R-ADDON-CAP").
		SetStatus(esportscommissionrule.StatusActive).SetScope(esportscommissionrule.ScopeGlobal).
		SetCalcMode(esportscommissionrule.CalcModeFixedPerOrder).SetValueCents(5000).SaveX(ctx)

	addon, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{AddedCents: 4000}, now)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := confirmOrderAddon(ctx, db, player.ID, addon.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.CommissionCents != confirmed.AddedCents || confirmed.CommissionCents != 4000 {
		t.Fatalf("加钟分成未按本金截断：commission %d, added %d",
			confirmed.CommissionCents, confirmed.AddedCents)
	}
	// 截断留痕：标记被截断并保留规则原值，便于对账区分「规则本来就低」与「被截断」。
	event := db.EsportsOrderEvent.Query().
		Where(esportsorderevent.EventTypeEQ("order.addon_confirmed")).OnlyX(ctx)
	if !boolField(event.Details, "commission_capped") || intField(event.Details, "commission_uncapped_cents") != 5000 {
		t.Fatalf("加钟截断未在事件中留痕：%+v", event.Details)
	}

	// 收款后计提额同样不超过加钟本金。
	if _, err = walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 4000, Note: "加钟收款测试充值"}, "addon-cap-recharge-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = payOrderAddonByBalance(ctx, db, 11, addon.ID, now); err != nil {
		t.Fatal(err)
	}
	entries := addonEntriesOfOrder(t, db, order.ID)
	if len(entries) != 1 || entries[0].AmountCents != 4000 || entries[0].BaseCents != 4000 ||
		entries[0].Kind != esportscommissionentry.KindAddon {
		t.Fatalf("加钟分成明细超出本金或口径不正确：%+v", entries)
	}
	if entries[0].AmountCents > db.EsportsOrder.GetX(ctx, order.ID).AddonCents {
		t.Fatalf("加钟分成超过了该订单已收加钟本金：%+v", entries[0])
	}
}

// 每单位固定额与「只有工时没有金额」的加钟同样受本金上限约束：
// 前者按本金截断，后者没有可分配收入因而计提 0。
func TestAddonCommissionCapCoversUnitsAndFreeAddons(t *testing.T) {
	db := openAddonDB(t, "esports_addon_commission_cap_units")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)
	player := seedPlayer(t, db, 9502, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedInServiceOrder(t, db, "addon-cap-2", player.ID)
	// 每单位 8 元：加钟 3 个单位只收 10 元 → 原口径 24 元，须截断到 10 元。
	db.EsportsCommissionRule.Create().SetRuleNo("R-ADDON-CAP-UNIT").
		SetStatus(esportscommissionrule.StatusActive).SetScope(esportscommissionrule.ScopeGlobal).
		SetCalcMode(esportscommissionrule.CalcModeFixedPerUnit).SetValueCents(800).SaveX(ctx)

	first, err := requestOrderAddon(ctx, db, order.ID, 11,
		addonInput{AddedCents: 1000, AddedUnits: 3}, now)
	if err != nil {
		t.Fatal(err)
	}
	firstConfirmed, err := confirmOrderAddon(ctx, db, player.ID, first.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if firstConfirmed.CommissionCents != 1000 {
		t.Fatalf("每单位固定额加钟分成未按本金截断：%d", firstConfirmed.CommissionCents)
	}
	if _, err = walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 1000, Note: "加钟收款测试充值"}, "addon-cap-recharge-2"); err != nil {
		t.Fatal(err)
	}
	if _, err = payOrderAddonByBalance(ctx, db, 11, first.ID, now); err != nil {
		t.Fatal(err)
	}

	// 只有追加服务量、没有追加金额的加钟：无营收基数，不产生加钟分成。
	second, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{AddedUnits: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	secondConfirmed, err := confirmOrderAddon(ctx, db, player.ID, second.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if secondConfirmed.CommissionCents != 0 {
		t.Fatalf("无金额加钟不应产生加钟分成：%d", secondConfirmed.CommissionCents)
	}
	if _, err = payOrderAddonByBalance(ctx, db, 11, second.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("无金额加钟不应可收款：%v", err)
	}
	entries := addonEntriesOfOrder(t, db, order.ID)
	if len(entries) != 1 || entries[0].AmountCents != 1000 {
		t.Fatalf("加钟分成明细不正确：%+v", entries)
	}
	if entries[0].AmountCents > db.EsportsOrder.GetX(ctx, order.ID).AddonCents {
		t.Fatalf("加钟分成超过已收加钟本金：%+v", entries[0])
	}
}

// 历史数据兜底：修复前已确认、快照分成超过本金的加钟单，收款计提时仍按本金收紧。
func TestAddonAccrualClampsLegacyOverCapSnapshot(t *testing.T) {
	db := openAddonDB(t, "esports_addon_legacy_cap")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)
	player := seedPlayer(t, db, 9503, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedInServiceOrder(t, db, "addon-cap-legacy", player.ID)
	db.EsportsCommissionRule.Create().SetRuleNo("R-ADDON-LEGACY").
		SetStatus(esportscommissionrule.StatusActive).SetScope(esportscommissionrule.ScopeGlobal).
		SetCalcMode(esportscommissionrule.CalcModeFixedPerOrder).SetValueCents(5000).SaveX(ctx)

	addon, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{AddedCents: 3000}, now)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟旧数据：确认时未截断，快照分成 9000 > 本金 3000。
	db.EsportsOrderAddon.UpdateOneID(addon.ID).
		SetStatus(esportsorderaddon.StatusConfirmed).
		SetConfirmedPlayerID(player.ID).SetConfirmedAt(now).
		SetCommissionCents(9000).SetRuleNo("R-ADDON-LEGACY").ExecX(ctx)
	if _, err = walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 3000, Note: "历史加钟收款测试充值"}, "addon-cap-recharge-legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err = payOrderAddonByBalance(ctx, db, 11, addon.ID, now); err != nil {
		t.Fatal(err)
	}
	entries := addonEntriesOfOrder(t, db, order.ID)
	if len(entries) != 1 || entries[0].AmountCents != 3000 {
		t.Fatalf("历史超额快照未被收紧到加钟本金：%+v", entries)
	}
	if !strings.Contains(entries[0].Note, "加钟本金上限截断") {
		t.Fatalf("截断未在分成明细备注留痕：%q", entries[0].Note)
	}
}

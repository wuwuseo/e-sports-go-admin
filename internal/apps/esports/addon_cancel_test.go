package esports

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscommissionrule"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderaddon"
	"go-admin/internal/ent/esportsplayer"
)

func seedAddonCommissionRule(t *testing.T, db *ent.Client, ruleNo string) *ent.EsportsCommissionRule {
	t.Helper()
	return db.EsportsCommissionRule.Create().SetRuleNo(ruleNo).
		SetStatus(esportscommissionrule.StatusActive).
		SetScope(esportscommissionrule.ScopeGlobal).
		SetCalcMode(esportscommissionrule.CalcModePercent).SetValueBp(5000).
		SaveX(t.Context())
}

// 收款后按快照计提一次；历史数据（旧版确认即计提）已有同一幂等键明细时跳过，不重复计提。
func TestPayAddonCommissionSkipsExistingHistoryEntry(t *testing.T) {
	db := openAddonDB(t, "esports_addon_commission_history")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := saveCustomerOrFail(t, db, 11)
	player := seedPlayer(t, db, 9411, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedInServiceOrder(t, db, "addon-commission-history", player.ID)
	seedAddonCommissionRule(t, db, "R-ADDON-HIST")

	addon, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{AddedCents: 4000}, now)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := confirmOrderAddon(ctx, db, player.ID, addon.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟修复前的历史明细：确认时已经按同一幂等键计提过。
	idem := "commission:addon:" + addon.AddonNo + ":" + strconv.Itoa(player.ID)
	historical := db.EsportsCommissionEntry.Create().
		SetIdemKey(idem).SetOrderID(order.ID).SetPlayerID(player.ID).
		SetKind(esportscommissionentry.KindAddon).SetSeatIndex(1).
		SetBaseCents(4000).SetAmountCents(confirmed.CommissionCents).
		SetStatus(esportscommissionentry.StatusPendingSettlement).
		SetNote("历史加钟分成").SaveX(ctx)
	if _, err = walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 4000, Note: "测试充值"}, "addon-hist-seed-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = payOrderAddonByBalance(ctx, db, 11, addon.ID, now); err != nil {
		t.Fatalf("收款应成功：%v", err)
	}
	if n := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(esportscommissionentry.KindAddon)).CountX(ctx); n != 1 {
		t.Fatalf("历史已计提时不得重复计提：%d", n)
	}
	if got := db.EsportsCommissionEntry.GetX(ctx, historical.ID); got.Note != "历史加钟分成" {
		t.Fatalf("历史明细不应被改写：%+v", got)
	}
}

// 已确认未收款的加钟可撤销：回滚订单加钟累加、历史分成置 void、写撤销事件；重复撤销冲突。
func TestCancelConfirmedUnpaidAddon(t *testing.T) {
	db := openAddonDB(t, "esports_addon_cancel_unpaid")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedPlayer(t, db, 9412, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedInServiceOrder(t, db, "addon-cancel-unpaid", player.ID)
	seedAddonCommissionRule(t, db, "R-ADDON-CANCEL")
	addon, err := requestOrderAddon(ctx, db, order.ID, 11,
		addonInput{AddedCents: 4000, AddedUnits: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := confirmOrderAddon(ctx, db, player.ID, addon.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟历史数据：确认即计提的待结算分成明细。
	entry := db.EsportsCommissionEntry.Create().
		SetIdemKey("commission:addon:" + addon.AddonNo + ":" + strconv.Itoa(player.ID)).
		SetOrderID(order.ID).SetPlayerID(player.ID).
		SetKind(esportscommissionentry.KindAddon).SetSeatIndex(1).
		SetBaseCents(4000).SetAmountCents(confirmed.CommissionCents).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)

	cancelled, err := cancelOrderAddon(ctx, db, addon.ID, 12, now)
	if err != nil || cancelled.Status != esportsorderaddon.StatusCancelled {
		t.Fatalf("已确认未收款加钟应可撤销：%v %+v", err, cancelled)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.AddonCents != 0 || got.AddonUnits != 0 {
		t.Fatalf("撤销已确认加钟应回滚订单累加：%+v", got)
	}
	if reloaded := db.EsportsCommissionEntry.GetX(ctx, entry.ID); reloaded.Status != esportscommissionentry.StatusVoid {
		t.Fatalf("待结算加钟分成应置 void：%+v", reloaded)
	}
	if n := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal)).CountX(ctx); n != 0 {
		t.Fatalf("待结算分成置 void 时不应另建冲回：%d", n)
	}
	if countEventsByType(t, db)["order.addon_cancelled"] != 1 {
		t.Fatal("缺少 order.addon_cancelled 事件")
	}
	if _, err := cancelOrderAddon(ctx, db, addon.ID, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复撤销应冲突：%v", err)
	}
}

// 已收款的加钟不可撤销，分成明细保持有效。
func TestCancelPaidAddonForbidden(t *testing.T) {
	db := openAddonDB(t, "esports_addon_cancel_paid")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := saveCustomerOrFail(t, db, 11)
	player := seedPlayer(t, db, 9413, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedInServiceOrder(t, db, "addon-cancel-paid", player.ID)
	seedAddonCommissionRule(t, db, "R-ADDON-PAID")
	addon, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{AddedCents: 4000}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = confirmOrderAddon(ctx, db, player.ID, addon.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 4000, Note: "测试充值"}, "addon-paid-seed-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = payOrderAddonByBalance(ctx, db, 11, addon.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = cancelOrderAddon(ctx, db, addon.ID, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("已收款加钟不应可撤销：%v", err)
	} else if !strings.Contains(err.Error(), "已收款") {
		t.Fatalf("错误文案应说明已收款不可撤销：%v", err)
	}
	if got := db.EsportsOrderAddon.GetX(ctx, addon.ID); got.Status != esportsorderaddon.StatusConfirmed ||
		got.PaymentStatus != esportsorderaddon.PaymentStatusPaid {
		t.Fatalf("已收款加钟状态不应变化：%+v", got)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.AddonCents != 4000 {
		t.Fatalf("已收款加钟不应回滚订单累加：%+v", got)
	}
	if n := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(esportscommissionentry.KindAddon),
		esportscommissionentry.StatusEQ(esportscommissionentry.StatusPendingSettlement),
	).CountX(ctx); n != 1 {
		t.Fatalf("已收款加钟分成应保持待结算：%d", n)
	}
}

// 已确认未收款的加钟阻止结单；撤销后才能结单。
func TestConfirmedUnpaidAddonBlocksClose(t *testing.T) {
	db := openAddonDB(t, "esports_addon_close_unpaid")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedPlayer(t, db, 9414, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedSimpleCloseableOrder(t, db, "addon-close-unpaid", 1000, 11)
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusInService).SaveX(ctx)
	db.EsportsDispatchSeat.Create().SetOrderID(order.ID).SetSeatIndex(1).
		SetPlayerID(player.ID).SetStatus(esportsdispatchseat.StatusAccepted).SaveX(ctx)
	seedAddonCommissionRule(t, db, "R-ADDON-CLOSE")
	addon, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{AddedCents: 1000}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = confirmOrderAddon(ctx, db, player.ID, addon.ID, now); err != nil {
		t.Fatal(err)
	}
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	if _, err := closeOrder(ctx, db, order.ID, 12, closeOrderInput{Note: "结单"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("已确认未收款加钟应阻止结单：%v", err)
	} else if !strings.Contains(err.Error(), "加钟") {
		t.Fatalf("拦截原因应说明加钟未处理：%v", err)
	}
	if _, err := cancelOrderAddon(ctx, db, addon.ID, 12, now); err != nil {
		t.Fatalf("撤销已确认未收款加钟失败：%v", err)
	}
	if _, err := closeOrder(ctx, db, order.ID, 12, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("撤销未收款加钟后应可结单：%v", err)
	}
}

// 纯加钟量（无追加金额）不存在收款动作，不应永久阻止结单。
func TestUnitsOnlyConfirmedAddonDoesNotBlockClose(t *testing.T) {
	db := openAddonDB(t, "esports_addon_close_units")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedPlayer(t, db, 9415, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedSimpleCloseableOrder(t, db, "addon-close-units", 1000, 11)
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusInService).SaveX(ctx)
	db.EsportsDispatchSeat.Create().SetOrderID(order.ID).SetSeatIndex(1).
		SetPlayerID(player.ID).SetStatus(esportsdispatchseat.StatusAccepted).SaveX(ctx)
	seedAddonCommissionRule(t, db, "R-ADDON-UNITS")
	addon, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{AddedUnits: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = confirmOrderAddon(ctx, db, player.ID, addon.ID, now); err != nil {
		t.Fatal(err)
	}
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	if _, err := closeOrder(ctx, db, order.ID, 12, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("无追加金额的加钟不应阻止结单：%v", err)
	}
}

package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscommissionrule"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderaddon"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/migrate"
)

// seedInServiceOrder 构造一张服务中订单，并把指定陪玩占用唯一席位（accepted）。
func seedInServiceOrder(t *testing.T, db *ent.Client, orderNo string, playerID int) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	order := seedPaidOrder(t, db, orderNo, 1)
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusInService).SaveX(ctx)
	db.EsportsDispatchSeat.Create().SetOrderID(order.ID).SetSeatIndex(1).
		SetPlayerID(playerID).SetStatus(esportsdispatchseat.StatusAccepted).SaveX(ctx)
	return order
}

// 加钟全流程：仅服务中订单可发起、金额或工时至少一项为正、同一订单只允许一条待确认；
// 本单陪玩确认后累加订单并不改写报价快照、按确认时点规则保存分成快照但不立即计提。
func TestOrderAddonLifecycle(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_addon?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	player := seedPlayer(t, db, 9001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	other := seedPlayer(t, db, 9002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedInServiceOrder(t, db, "addon-1", player.ID)
	db.EsportsCommissionRule.Create().SetRuleNo("R-ADDON").
		SetStatus(esportscommissionrule.StatusActive).
		SetScope(esportscommissionrule.ScopeGlobal).
		SetCalcMode(esportscommissionrule.CalcModePercent).SetValueBp(5000).SaveX(ctx)

	notService := seedPaidOrder(t, db, "addon-pending", 1)
	if _, err := requestOrderAddon(ctx, db, notService.ID, 11, addonInput{AddedCents: 1000}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非服务中订单发起加钟未拦截：%v", err)
	}
	if _, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("金额与工时都为零的加钟未拦截：%v", err)
	}

	addon, err := requestOrderAddon(ctx, db, order.ID, 11,
		addonInput{AddedCents: 4000, AddedUnits: 2, Note: "客户要求加钟"}, now)
	if err != nil || addon.Status != esportsorderaddon.StatusPending {
		t.Fatalf("发起加钟失败：%v %+v", err, addon)
	}
	if countEventsByType(t, db)["order.addon_requested"] != 1 {
		t.Fatal("缺少 order.addon_requested 事件")
	}
	if _, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{AddedCents: 1000}, now); err == nil {
		t.Fatal("同一订单重复发起加钟未拦截")
	}
	if _, err := confirmOrderAddon(ctx, db, other.ID, addon.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非本单陪玩确认加钟未拦截：%v", err)
	}

	confirmed, err := confirmOrderAddon(ctx, db, player.ID, addon.ID, now)
	if err != nil || confirmed.Status != esportsorderaddon.StatusConfirmed ||
		confirmed.CommissionCents != 2000 || confirmed.RuleNo != "R-ADDON" ||
		confirmed.ConfirmedPlayerID == nil || *confirmed.ConfirmedPlayerID != player.ID ||
		confirmed.ConfirmedAt == nil {
		t.Fatalf("确认加钟失败：%v %+v", err, confirmed)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.AddonCents != 4000 || got.AddonUnits != 2 || got.PayableCents != order.PayableCents {
		t.Fatalf("加钟未累加到订单或改写了报价快照：%+v", got)
	}
	// 确认只保存快照，不立即计提分成（未收款不产生陪玩应付）。
	if n := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(esportscommissionentry.KindAddon)).CountX(ctx); n != 0 {
		t.Fatalf("确认加钟不应立即生成分成明细：%d", n)
	}
	// 规则快照已保存到加钟单，供收款后按快照计提。
	if confirmed.RuleID == nil || confirmed.RuleVersion != 1 || confirmed.CommissionCents != 2000 {
		t.Fatalf("加钟规则快照不完整：%+v", confirmed)
	}
	if _, err := confirmOrderAddon(ctx, db, player.ID, addon.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复确认加钟未拦截：%v", err)
	}
	if items, err := listPlayerPendingAddons(ctx, db, player.ID); err != nil || len(items) != 0 {
		t.Fatalf("已确认加钟仍在待确认列表：%v %+v", err, items)
	}
}

// 拒绝加钟留痕且不累加订单；未处理加钟阻止结单（结单条件默认项）。
func TestOrderAddonRejectAndCloseGuard(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_addon_reject?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	player := seedPlayer(t, db, 9101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedInServiceOrder(t, db, "addon-2", player.ID)
	addon, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{AddedCents: 2000, AddedUnits: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rejectOrderAddon(ctx, db, player.ID, addon.ID, "  ", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未填拒绝原因未拦截：%v", err)
	}
	rejected, err := rejectOrderAddon(ctx, db, player.ID, addon.ID, "客户已下线", now)
	if err != nil || rejected.Status != esportsorderaddon.StatusRejected ||
		rejected.RejectReason != "客户已下线" {
		t.Fatalf("拒绝加钟失败：%v %+v", err, rejected)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.AddonCents != 0 || got.AddonUnits != 0 {
		t.Fatalf("被拒绝的加钟不应累加到订单：%+v", got)
	}
	if countEventsByType(t, db)["order.addon_rejected"] != 1 {
		t.Fatal("缺少 order.addon_rejected 事件")
	}

	// 结单条件：存在待确认加钟时不允许结单。
	closeGuardPlayer := seedPlayer(t, db, 9102, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order2 := seedInServiceOrder(t, db, "addon-3", closeGuardPlayer.ID)
	db.EsportsServiceSegment.Create().SetOrderID(order2.ID).SetPlayerID(closeGuardPlayer.ID).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).
		SetStartedAt(now).SetEndedAt(now.Add(time.Minute)).
		SetSystemSeconds(600).SetEvidenceRef("shot-1").SaveX(ctx)
	db.EsportsOrder.UpdateOneID(order2.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	if _, err := requestOrderAddon(ctx, db, order2.ID, 11, addonInput{AddedCents: 1000}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("待结单订单发起加钟未拦截：%v", err)
	}
	db.EsportsOrder.UpdateOneID(order2.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusInService).SaveX(ctx)
	addon2, err := requestOrderAddon(ctx, db, order2.ID, 11, addonInput{AddedCents: 1000}, now)
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsOrder.UpdateOneID(order2.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	if _, err := closeOrder(ctx, db, order2.ID, 12, closeOrderInput{Note: "结单"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("存在未处理加钟时结单未被拦截：%v", err)
	}
	// 客服取消加钟后即可结单。
	if _, err := cancelOrderAddon(ctx, db, addon2.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if _, err := closeOrder(ctx, db, order2.ID, 12, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("取消加钟后结单失败：%v", err)
	}
}

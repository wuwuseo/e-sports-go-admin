package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportsfulfillmentmakeup"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsplayer"
)

// P1-15 补履约的账务出口：完成验收时在同一事务内为承接陪玩生成一条分成明细
// （source 标记 makeup，幂等键唯一）并计入结算，同时把公司承担报酬计入公司成本侧；
// 重复完成不产生第二条分成明细。
func TestFulfillmentMakeupCompletionAccruesCommissionOnce(t *testing.T) {
	db := openAfterSaleDB(t, "esports_makeup_accounting")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	const adminID = 12

	player := seedPlayer(t, db, 4301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := db.EsportsOrder.Create().SetOrderNo("makeup-accounting-1").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(10000).SetPayableCents(10000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).SaveX(ctx)
	task := db.EsportsFulfillmentMakeup.Create().SetMakeupNo("MK-ACCOUNTING-1").
		SetAfterSaleID(1).SetOrderID(order.ID).SetKind(esportsfulfillmentmakeup.KindMakeupTime).
		SetPlayerID(player.ID).SetSeatIndex(1).SetUnits(2).SetCompanyPayCents(800).
		SetStatus(esportsfulfillmentmakeup.StatusServicing).
		SetStartedAt(now.Add(-time.Hour)).SaveX(ctx)

	completed, err := completeFulfillmentMakeup(ctx, db, task.ID, adminID, "已补足 2 局", now)
	if err != nil {
		t.Fatalf("补履约验收失败：%v", err)
	}
	if completed.Status != esportsfulfillmentmakeup.StatusCompleted {
		t.Fatalf("补履约状态不正确：%+v", completed)
	}

	wantKey := "commission:makeup:MK-ACCOUNTING-1:" + strconv.Itoa(player.ID)
	entries := db.EsportsCommissionEntry.Query().AllX(ctx)
	if len(entries) != 1 {
		t.Fatalf("补履约完成应有且仅有一条分成明细，实际 %d 条", len(entries))
	}
	entry := entries[0]
	if entry.IdemKey != wantKey {
		t.Fatalf("分成明细幂等键未标记 makeup 来源：%q，期望 %q", entry.IdemKey, wantKey)
	}
	if entry.Kind != esportscommissionentry.KindService ||
		entry.Status != esportscommissionentry.StatusPendingSettlement ||
		entry.AmountCents != 800 || entry.BaseCents != 800 ||
		entry.PlayerID != player.ID || entry.SeatIndex != 1 ||
		entry.OrderID == nil || *entry.OrderID != order.ID {
		t.Fatalf("分成明细内容不正确：%+v", entry)
	}

	// 公司成本侧：公司承担报酬计入订单 channel_cost_cents（工作室剩余反算口径）并留痕。
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.ChannelCostCents != 800 {
		t.Fatalf("公司承担报酬未计入订单公司成本：%d", got.ChannelCostCents)
	}
	// 补履约不增加原订单实收与成交额。
	if got.PayableCents != 10000 {
		t.Fatalf("补履约不应改原订单应付：%d", got.PayableCents)
	}
	event := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.makeup_fulfilled"),
	).OnlyX(ctx)
	if event.ActorType != "admin" || event.ActorID != strconv.Itoa(adminID) ||
		event.Details["company_pay_cents"] != float64(800) ||
		event.Details["makeup_no"] != "MK-ACCOUNTING-1" ||
		event.Details["commission_source"] != makeupCommissionSource {
		t.Fatalf("补履约成本留痕不完整：%+v", event.Details)
	}

	// 重复完成：状态冲突，且不产生第二条分成明细、不重复计公司成本。
	if _, err = completeFulfillmentMakeup(ctx, db, task.ID, adminID, "重复验收", now.Add(time.Minute)); !errors.Is(err, errConflict) {
		t.Fatalf("重复验收应冲突：%v", err)
	}
	if count := db.EsportsCommissionEntry.Query().CountX(ctx); count != 1 {
		t.Fatalf("重复验收后分成明细应仍为 1 条：%d", count)
	}
	if cost := db.EsportsOrder.GetX(ctx, order.ID).ChannelCostCents; cost != 800 {
		t.Fatalf("重复验收不应重复计公司成本：%d", cost)
	}

	// 进入结算汇总：常规结算把待结算明细挂到结算单，锁定结算单后明细转 settled 并写入猫粮流水。
	settlement, err := settlePlayerCommission(ctx, db, player.ID, now.Add(-time.Hour), now.Add(time.Hour), adminID, now)
	if err != nil {
		t.Fatalf("补履约分成应可结算：%v", err)
	}
	if settlement.NetCents != 800 || settlement.GrossCents != 800 || settlement.EntryCount != 1 {
		t.Fatalf("结算单未汇总补履约分成：%+v", settlement)
	}
	linked := db.EsportsCommissionEntry.GetX(ctx, entry.ID)
	if linked.SettlementID == nil || *linked.SettlementID != settlement.ID {
		t.Fatalf("分成明细未挂到结算单：%+v", linked)
	}
	if _, err = lockSettlement(ctx, db, settlement.ID, adminID, now); err != nil {
		t.Fatalf("锁定结算单失败：%v", err)
	}
	settled := db.EsportsCommissionEntry.GetX(ctx, entry.ID)
	if settled.Status != esportscommissionentry.StatusSettled {
		t.Fatalf("锁定后分成明细应为 settled：%+v", settled)
	}
	catfood, err := walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if catfood.PrincipalCents != 800 {
		t.Fatalf("补履约报酬应结算进承接陪玩猫粮：%+v", catfood)
	}
}

// P1-15 幂等键本身兜底：任务状态已是 completed 之外，直接重复执行计提函数也不会产生第二条明细。
func TestMakeupCommissionIdempotentByKey(t *testing.T) {
	db := openAfterSaleDB(t, "esports_makeup_commission_idem")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedPlayer(t, db, 4302, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := db.EsportsOrder.Create().SetOrderNo("makeup-idem-1").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).SaveX(ctx)
	task := db.EsportsFulfillmentMakeup.Create().SetMakeupNo("MK-IDEM-1").
		SetAfterSaleID(2).SetOrderID(order.ID).SetKind(esportsfulfillmentmakeup.KindReplacePlayer).
		SetPlayerID(player.ID).SetSeatIndex(1).SetUnits(1).SetCompanyPayCents(500).
		SetStatus(esportsfulfillmentmakeup.StatusServicing).SaveX(ctx)

	for i := 0; i < 2; i++ {
		tx, err := db.Tx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = accrueMakeupCommissionIn(ctx, tx, task); err != nil {
			_ = tx.Rollback()
			t.Fatalf("第 %d 次计提失败：%v", i+1, err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if count := db.EsportsCommissionEntry.Query().CountX(ctx); count != 1 {
		t.Fatalf("幂等键应保证只产生一条补履约分成：%d", count)
	}
}

package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
)

func seedBackfillSku(t *testing.T, db *ent.Client, code string) *ent.EsportsSku {
	t.Helper()
	sku, err := saveSku(t.Context(), db, skuInput{
		Code: code, Title: "补录测试商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 2, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 10, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	return sku
}

// 管理员历史订单补录（§3.3 / §4.1）：入口固定、不占名额、默认不计提、留痕审计且幂等。
func TestAdminBackfillOrderIsolatesCommissionAndQuota(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_backfill")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settled := now.AddDate(0, 0, -30)
	customer := saveCustomerOrFail(t, db, 11)
	sku := seedBackfillSku(t, db, "BACKFILL-1")

	order, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 2,
		SettledAt: settled.Format(time.RFC3339), Reason: "线下微信成交漏报，按客户流水补录",
	}, "backfill-key-0001", now)
	if err != nil {
		t.Fatal(err)
	}
	// 入口、成交金额与历史时间：订单创建时间即业务发生时间，报表按发生期归集。
	if order.EntryPoint != esportsorder.EntryPointAdminBackfill ||
		order.PayableCents != 2000 || order.PromisedServiceUnits != 4 ||
		!order.CreatedAt.Equal(settled) || !order.PaidAt.Equal(settled) {
		t.Fatalf("补录单基本信息不正确：%+v", order)
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted ||
		order.PaymentStatus != esportsorder.PaymentStatusPaid ||
		order.ClosedAt == nil || !order.ClosedAt.Equal(settled) ||
		order.CloseNetServiceCents == nil || *order.CloseNetServiceCents != 2000 {
		t.Fatalf("补录单履约与结单快照不正确：%+v", order)
	}
	// 审计留痕：补录原因、补录人、补录时刻。
	if order.BackfillReason == "" || order.BackfilledByAdminID == nil ||
		*order.BackfilledByAdminID != 11 || order.BackfilledAt == nil ||
		order.BackfilledAt.Before(now) || order.BackfillCountCommission {
		t.Fatalf("补录审计字段不正确：%+v", order)
	}
	// 隔离规则：不占 SKU 名额。
	got := db.EsportsSku.GetX(ctx, sku.ID)
	if got.QuotaReserved != 0 || got.QuotaSold != 0 {
		t.Fatalf("补录单占用了 SKU 名额：%+v", got)
	}
	// 隔离规则：默认不计提分成 / 提成。
	if count := db.EsportsCommissionEntry.Query().CountX(ctx); count != 0 {
		t.Fatalf("补录单默认计提了陪玩分成：%d", count)
	}
	if count := db.EsportsCsCommissionEntry.Query().CountX(ctx); count != 0 {
		t.Fatalf("补录单默认计提了客服提成：%d", count)
	}
	if n := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.commission_excluded"),
	).CountX(ctx); n != 1 {
		t.Fatalf("缺少「排除计提」留痕外盒：%d", n)
	}
	events := countEventsByType(t, db)
	if events["order.backfilled"] != 1 {
		t.Fatalf("缺少 order.backfilled 事件：%+v", events)
	}
	// 幂等：同键同体返回同一张单；同键不同体视为冲突。
	again, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 2,
		SettledAt: settled.Format(time.RFC3339), Reason: "线下微信成交漏报，按客户流水补录",
	}, "backfill-key-0001", now)
	if err != nil || again.ID != order.ID {
		t.Fatalf("补录幂等失效：%v %+v", err, again)
	}
	if _, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 3,
		SettledAt: settled.Format(time.RFC3339), Reason: "线下微信成交漏报，按客户流水补录",
	}, "backfill-key-0001", now); !errors.Is(err, errConflict) {
		t.Fatalf("同键不同体未判冲突：%v", err)
	}
}

// 补录入参与数据边界校验：原因、成交时间、状态枚举、客户归属、单价都被约束。
func TestAdminBackfillOrderGuards(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_backfill_guards")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settled := now.AddDate(0, 0, -3).Format(time.RFC3339)
	customer := saveCustomerOrFail(t, db, 11)
	other := saveCustomerOrFail(t, db, 22)
	sku := seedBackfillSku(t, db, "BACKFILL-2")

	base := backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		SettledAt: settled, Reason: "线下成交漏报补录",
	}
	cases := []struct {
		name string
		in   backfillOrderInput
	}{
		{"原因为空", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, SettledAt: settled}},
		{"成交时间缺失", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, Reason: "线下成交漏报补录"}},
		{"成交时间为未来", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
			SettledAt: now.Add(time.Hour).Format(time.RFC3339), Reason: "线下成交漏报补录"}},
		{"成交时间超追溯年限", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
			SettledAt: now.AddDate(-6, 0, 0).Format(time.RFC3339), Reason: "线下成交漏报补录"}},
		{"状态不在终态白名单", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
			SettledAt: settled, Reason: "线下成交漏报补录", FulfillmentStatus: "pending_dispatch"}},
		{"未收款", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
			SettledAt: settled, Reason: "线下成交漏报补录", PaymentStatus: "unpaid"}},
		{"单价非法", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
			SettledAt: settled, Reason: "线下成交漏报补录", UnitPriceCents: -1}},
		{"数量超上限", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1001,
			SettledAt: settled, Reason: "线下成交漏报补录"}},
	}
	for i, tc := range cases {
		if _, err := backfillOrder(ctx, db, 11, tc.in, "backfill-guard-0001", now); !errors.Is(err, errInvalidInput) {
			t.Fatalf("第 %d 组（%s）未拦截：%v", i+1, tc.name, err)
		}
	}
	// 客服只能给本人负责的客户补录。
	if _, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: other.ID, SkuID: sku.ID, Quantity: 1, SettledAt: settled, Reason: "线下成交漏报补录",
	}, "backfill-guard-0002", now); !errors.Is(err, errForbidden) {
		t.Fatalf("跨客服补录未被拦截：%v", err)
	}
	// 幂等键长度受约束（与常规报单同口径）。
	if _, err := backfillOrder(ctx, db, 11, base, "short", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("过短幂等键未被拦截：%v", err)
	}
	// 未传单价时回落 SKU 规格价；显式单价按成交价落账。
	order, err := backfillOrder(ctx, db, 11, base, "backfill-guard-0003", now)
	if err != nil || order.PayableCents != 1000 {
		t.Fatalf("未传单价未回落规格价：%v %+v", err, order)
	}
	priced, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, UnitPriceCents: 666,
		SettledAt: settled, Reason: "线下成交漏报补录",
	}, "backfill-guard-0004", now)
	if err != nil || priced.PayableCents != 666 || priced.UnitPriceCents != 666 {
		t.Fatalf("显式成交价未落账：%v %+v", err, priced)
	}
	// 已取消的历史单不写结单快照。
	cancelled, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, SettledAt: settled,
		Reason: "历史取消单补录", FulfillmentStatus: "cancelled",
	}, "backfill-guard-0005", now)
	if err != nil || cancelled.ClosedAt != nil || cancelled.CloseNetServiceCents != nil {
		t.Fatalf("历史取消单不应写结单快照：%v %+v", err, cancelled)
	}
}

// 显式选择计入当期业绩时输出「已计提」结论，且不再写排除留痕。
func TestAdminBackfillOptInCommission(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_backfill_optin")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settled := now.AddDate(0, 0, -1).Format(time.RFC3339)
	customer := saveCustomerOrFail(t, db, 11)
	sku := seedBackfillSku(t, db, "BACKFILL-3")

	order, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, SettledAt: settled,
		Reason: "历史成交补录并计入当期业绩", CountCommission: true,
	}, "backfill-optin-0001", now)
	if err != nil {
		t.Fatal(err)
	}
	if !orderCountsCommission(order) {
		t.Fatalf("显式计入业绩后仍被判为不计提：%+v", order)
	}
	view := backfillOrderView(order)
	if counted, ok := view["counted_commission"].(bool); !ok || !counted {
		t.Fatalf("补录结果视图未给出计提结论：%+v", view)
	}
	if n := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.commission_excluded"),
	).CountX(ctx); n != 0 {
		t.Fatalf("计入业绩的单不应写排除留痕：%d", n)
	}
}

// 计提口径判断：只有补录单且显式开启才不计入「排除」。
func TestOrderCountsCommissionOnlyForBackfill(t *testing.T) {
	normal := &ent.EsportsOrder{EntryPoint: esportsorder.EntryPointCsPc}
	if !orderCountsCommission(normal) {
		t.Fatal("常规订单不应被排除计提")
	}
	backfill := &ent.EsportsOrder{EntryPoint: esportsorder.EntryPointAdminBackfill}
	if orderCountsCommission(backfill) {
		t.Fatal("补录单默认应排除计提")
	}
	optedIn := &ent.EsportsOrder{
		EntryPoint: esportsorder.EntryPointAdminBackfill, BackfillCountCommission: true,
	}
	if !orderCountsCommission(optedIn) {
		t.Fatal("显式开启后补录单应参与计提")
	}
}

// 补录单与常规单在报表口径上区分：entry_point 与创建时间都按业务发生期落库。
func TestAdminBackfillOrderAppearsInSourceReports(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_backfill_report")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settled := now.AddDate(0, 0, -2)
	customer := saveCustomerOrFail(t, db, 11)
	sku := seedBackfillSku(t, db, "BACKFILL-4")
	if _, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		SettledAt: settled.Format(time.RFC3339), Reason: "线下成交漏报补录",
	}, "backfill-report-0001", now); err != nil {
		t.Fatal(err)
	}
	matched, err := db.EsportsOrder.Query().Where(
		esportsorder.EntryPointEQ(esportsorder.EntryPointAdminBackfill),
		esportsorder.CreatedAtGTE(settled.Add(-time.Hour)),
		esportsorder.CreatedAtLT(settled.Add(time.Hour)),
	).Count(ctx)
	if err != nil || matched != 1 {
		t.Fatalf("补录单未按业务发生期进入渠道来源筛选：%d %v", matched, err)
	}
}

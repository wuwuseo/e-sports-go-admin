package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportsorder"
)

// ：逐最小服务单位分摊「折后明细金额 ÷ 承诺服务量」，
// **尾差固定记在最后一单位**（裁决：台账举例 [34,33,33] 与它自己的规则矛盾，
// 以规则为准，实现 [33,33,34]；本仓资金分摊的既有约定同为「尾差归最后一条」）。
func TestServiceUnitAllocationTailOnLastUnit(t *testing.T) {
	full := unitReversalTargets(100, 3, 100, 100)
	if len(full) != 3 || full[0] != 33 || full[1] != 33 || full[2] != 34 {
		t.Fatalf("承诺量 3、折后 100 分、全额退款应逐单位为 33/33/34（尾差在最后一单位）：%v", full)
	}
	half := unitReversalTargets(100, 3, 50, 100)
	if len(half) != 3 || half[0] != 16 || half[1] != 16 || half[2] != 18 {
		t.Fatalf("退款 50%% 时逐单位冲回应为 16/16/18（尾差在最后一单位）：%v", half)
	}
	cases := []struct {
		amount, units, refund, base int64
	}{
		{100, 3, 100, 100}, {100, 3, 50, 100}, {100, 1, 30, 100},
		{7, 3, 3, 7}, {1, 3, 1, 3}, {5000, 3, 1250, 5000}, {80, 6, 1, 80},
	}
	for _, item := range cases {
		parts := unitReversalTargets(item.amount, item.units, item.refund, item.base)
		var total int64
		for _, part := range parts {
			total += part
		}
		if want := item.amount * item.refund / item.base; total != want {
			t.Fatalf("逐单位冲回合计必须恒等于 折后金额×退款÷统一可退基数：amount=%d units=%d refund=%d base=%d got=%d want=%d（%v）",
				item.amount, item.units, item.refund, item.base, total, want, parts)
		}
	}
}

// ：单位级分摊须真正参与退款金额除法——退款冲回明细的金额即由
// 「逐单位金额 × 退款额 ÷ 统一可退基数」逐单位求和得出，且逐单位明细留痕可查。
func TestServiceUnitRefundAllocationDrivesReversalEntries(t *testing.T) {
	db := r6OpenDB(t, "round6_unit_level_refund")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := db.EsportsOrder.Create().SetOrderNo("R6-UNIT-2").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(100).SetPayableCents(100).
		SetPromisedServiceUnits(3).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).SaveX(ctx)
	db.EsportsCommissionEntry.Create().SetIdemKey("r6unit:service:2").
		SetOrderID(order.ID).SetPlayerID(7).
		SetKind(esportscommissionentry.KindService).SetSeatIndex(1).
		SetBaseCents(100).SetAmountCents(100).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)

	item := &ent.EsportsAfterSale{AfterSaleNo: "AS-R6-UNIT-2", RefundCents: 50}
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = reverseOrderCommission(ctx, tx, order, item, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("退款冲回失败：%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	views, err := listCommissionEntryViews(ctx, db, 0, order.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range views {
		view := views[i]
		if view.Kind != esportscommissionentry.KindReversal {
			continue
		}
		found = true
		var total int64
		for _, part := range view.ServiceUnitAmounts {
			total += part
		}
		if total != -view.AmountCents {
			t.Fatalf("逐单位冲回合计必须恒等于冲回明细金额 %d：%v", -view.AmountCents, view.ServiceUnitAmounts)
		}
		if len(view.ServiceUnitAmounts) != 3 ||
			view.ServiceUnitAmounts[0] != 16 || view.ServiceUnitAmounts[1] != 16 ||
			view.ServiceUnitAmounts[2] != 18 {
			t.Fatalf("退款 50 分 / 承诺量 3 的逐单位冲回应为 16/16/18：%v", view.ServiceUnitAmounts)
		}
	}
	if !found {
		t.Fatalf("缺少退款冲回明细：%+v", views)
	}
}

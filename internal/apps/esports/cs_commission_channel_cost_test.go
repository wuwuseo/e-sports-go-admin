package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportsorder"
)

// 渠道成本与工作室剩余反算（§12.1）：可分基数 = 服务应付 − 陪玩分成 − 渠道成本，
// 工作室剩余 = 可分基数 − 客服提成，随明细快照。
func TestCsCommissionChannelCostAndStudioRemainder(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_commission_channel_cost")
	ctx := t.Context()
	admin := seedAdmin(t, db, "cc-channel-admin")
	now := time.Now()

	if err := createAndActivatePlayerRule(t, db, admin.ID, 3000, now); err != nil {
		t.Fatal(err)
	}
	csRule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-CH", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 1000, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, csRule.ID, admin.ID, now); err != nil {
		t.Fatal(err)
	}

	// 渠道成本 2000：可分基数 = 10000 − 3000 − 2000 = 5000；10% 提成 1000；工作室剩余 4000。
	order := seedCloseableOrder(t, db, "CC-CH-1", 10000, []segSeed{{playerID: 1, seatIndex: 1, seconds: 600}})
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(admin.ID).SetChannelCostCents(2000).SaveX(ctx)
	if _, err = closeOrder(ctx, db, order.ID, admin.ID, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	entry := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(order.ID)).OnlyX(ctx)
	if entry.AmountCents != 1000 || entry.ChannelCostCents != 2000 || entry.StudioRemainderCents != 4000 {
		t.Fatalf("渠道成本/工作室剩余不正确：%+v", entry)
	}

	// 提成上限被渠道成本进一步压缩：90% 提成被可分基数 5000 截断，工作室剩余为 0。
	capRule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-CH", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 9000, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, capRule.ID, admin.ID, now); err != nil {
		t.Fatal(err)
	}
	second := seedCloseableOrder(t, db, "CC-CH-2", 10000, []segSeed{{playerID: 1, seatIndex: 1, seconds: 600}})
	db.EsportsOrder.UpdateOneID(second.ID).SetResponsibleAdminID(admin.ID).SetChannelCostCents(2000).SaveX(ctx)
	if _, err = closeOrder(ctx, db, second.ID, admin.ID, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	capped := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(second.ID)).OnlyX(ctx)
	if capped.AmountCents != 5000 || capped.StudioRemainderCents != 0 {
		t.Fatalf("渠道成本压缩后的上限不正确：%+v", capped)
	}
}

// 登记渠道成本：非负、不超过应付、结单后不可改。
func TestSetOrderChannelCost(t *testing.T) {
	db := openCsCommissionDB(t, "esports_set_channel_cost")
	ctx := t.Context()
	admin := seedAdmin(t, db, "cc-set-cost-admin")
	now := time.Now()

	order := seedCloseableOrder(t, db, "CH-COST-1", 10000, []segSeed{{playerID: 1, seatIndex: 1, seconds: 600}})
	updated, err := setOrderChannelCost(ctx, db, order.ID, 2000, admin.ID, now)
	if err != nil || updated.ChannelCostCents != 2000 {
		t.Fatalf("登记渠道成本失败：%v %+v", err, updated)
	}
	if _, err = setOrderChannelCost(ctx, db, order.ID, -1, admin.ID, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("负渠道成本未拦截：%v", err)
	}
	if _, err = setOrderChannelCost(ctx, db, order.ID, 10001, admin.ID, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超应付渠道成本未拦截：%v", err)
	}
	if _, err = closeOrder(ctx, db, order.ID, admin.ID, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = setOrderChannelCost(ctx, db, order.ID, 3000, admin.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("结单后修改渠道成本未拦截：%v", err)
	}
}

// 退款冲回的舍入尾差单列（§12.1）：多条原明细按比例截断后，尾差归最后一条冲回明细，
// 使冲回合计与「总原明细金额 × 退款比例」的精确值一致。
func TestPlayerCommissionReversalTail(t *testing.T) {
	db := openCsCommissionDB(t, "esports_reversal_tail")
	ctx := t.Context()
	now := time.Now()

	order := db.EsportsOrder.Create().SetOrderNo("TAIL-1").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(10000).SetPayableCents(10000).
		SetPromisedServiceUnits(1).SetRequiredSeats(2).
		SetPaymentDeadline(now.UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)

	// 两条 3 分服务分成；退款 50%（5000/10000）。
	for i, pid := range []int{1, 2} {
		db.EsportsCommissionEntry.Create().SetIdemKey("tail:service:" + strconv.Itoa(i)).
			SetOrderID(order.ID).SetPlayerID(pid).
			SetKind(esportscommissionentry.KindService).SetSeatIndex(pid).
			SetBaseCents(10000).SetAmountCents(3).
			SetRuleNo("CR").SetRuleVersion(1).
			SetCalcMode(esportscommissionentry.CalcModePercent).
			SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)
	}

	item := &ent.EsportsAfterSale{AfterSaleNo: "AS-TAIL-1", RefundCents: 5000}
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = reverseOrderCommission(ctx, tx, order, item, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var total int64
	for _, entry := range db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal),
	).AllX(ctx) {
		total += entry.AmountCents
	}
	// 精确值 = -(6 × 5000 / 10000) = -3；逐条截断各 -1、尾差 -1 归最后一条 → 合计 -3。
	if total != -3 {
		t.Fatalf("尾差单列后冲回合计应为 -3，got=%d", total)
	}
}

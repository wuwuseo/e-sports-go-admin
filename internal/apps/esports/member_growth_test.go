package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportswallettx"
)

// 成长值按核销明细的本金计提（§10.2-01）：余额本金 5000 + 赠送权益 5000 支付的
// 10000 应付订单只产生 5000 成长值；退款只冲回对应部分——按原核销构成退还的本金份额冲回，
// 退满后成长值恰好归零且不多冲。
func TestOrderGrowthAccruesPrincipalOnlyAndReversesPrincipalShare(t *testing.T) {
	db := openRound4MemberDB(t, "r4wm_member_growth_principal")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	seedMemberLevel(t, db, memberLevelInput{
		Code: "growth-silver", Name: "银卡", GrowthThreshold: 0, DiscountBps: 10000,
	})
	customer := saveCustomerOrFail(t, db, 11)
	order := seedCloseableMemberOrder(t, db, customer.ID, "r4wm-growth-principal-1", 10000)
	// 核销明细：本金 5000 + 赠送权益 5000（与钱包流水的实际构成一致）。
	db.EsportsWalletTx.Create().SetTxNo("r4wm-growth-principal-tx-1").
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(customer.ID).
		SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-5000).SetBonusDeltaCents(-5000).
		SetSourceType("order").SetSourceID(order.OrderNo).
		SetOperatorID(11).SetNote("订单余额核销").SaveX(ctx)

	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "本金口径结单"}, now); err != nil {
		t.Fatalf("结单失败：%v", err)
	}
	if growth := customerGrowthTotalX(t, db, customer.ID); growth != 5000 {
		t.Fatalf("赠送权益支付部分不应计入成长值：得到 %d，期望 5000", growth)
	}

	// 部分退款 4000：原核销构成本金 5000 / 赠送 5000，只冲回对应本金 2000。
	first := newRefundAfterSale(t, db, order.ID, "refund_request", 4000, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, first.ID, 12, now); err != nil {
		t.Fatalf("部分退款审批失败：%v", err)
	}
	if _, err := executeAfterSale(ctx, db, first.ID, 12, now); err != nil {
		t.Fatalf("部分退款执行失败：%v", err)
	}
	if growth := customerGrowthTotalX(t, db, customer.ID); growth != 3000 {
		t.Fatalf("部分退款应只冲回对应本金 2000：得到 %d，期望 3000", growth)
	}

	// 退剩余 6000：累计冲回恰好为本金 5000，成长值归零，不得多冲。
	second := newRefundAfterSale(t, db, order.ID, "duration_short", 6000, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, second.ID, 12, now); err != nil {
		t.Fatalf("尾款审批失败：%v", err)
	}
	if _, err := executeAfterSale(ctx, db, second.ID, 12, now); err != nil {
		t.Fatalf("尾款执行失败：%v", err)
	}
	if growth := customerGrowthTotalX(t, db, customer.ID); growth != 0 {
		t.Fatalf("退满后成长值应恰好归零：得到 %d", growth)
	}
}

package esports

import (
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsledgerentry"
	"go-admin/internal/ent/esportsrechargerefundexecution"
	"go-admin/internal/ent/esportswallettx"
)

// 按充值批次退款执行（§4.5）：两个充值批次未消费时，按批次生成执行明细；
// 幂等键按「售后单来源 + 批次来源」拆分，重复执行不重复落账。
func TestRechargeRefundExecGeneratesBatchItems(t *testing.T) {
	db := openStoredValueDB(t, "esports_recharge_refund_exec")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)

	// 两个充值批次：批次一纯本金 10000；批次二本金 5000 + 赠送 1000。
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 10000, Note: "首次充值"}, "rre-recharge-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 5000, BonusCents: 1000, Note: "二次充值"}, "rre-recharge-2"); err != nil {
		t.Fatal(err)
	}
	// 充值流水作为售后来源（source_type=recharge）。
	rechargeTx := db.EsportsWalletTx.Query().Where(esportswallettx.SourceIDEQ("rre-recharge-1")).OnlyX(ctx)
	item, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "recharge", SourceID: rechargeTx.ID, Kind: "refund_request",
		Channel: "admin", Demand: "客户要求按批次退款", Contact: "wx-1",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}

	rows, err := executeRechargeRefundByBatch(ctx, db, item.ID, 11, now)
	if err != nil {
		t.Fatalf("按批次退款执行应成功：%v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应生成两条执行明细：%+v", rows)
	}
	// 未消费：批次一可退 10000，批次二可退本金 5000（赠送 1000 作废不折现）。
	if rows[0].AmountCents != 10000 || rows[1].AmountCents != 5000 {
		t.Fatalf("逐批退款金额不正确：%+v", rows)
	}
	// 幂等键按「售后单来源 + 批次来源」拆分，两个批次来源不同。
	for _, r := range rows {
		want := "refund:recharge:" + strconv.Itoa(item.SourceID) + ":" + r.BatchSourceType + ":" + r.BatchSourceID
		if r.IdemKey != want {
			t.Fatalf("幂等键格式不正确：got=%s want=%s", r.IdemKey, want)
		}
		if r.Status != esportsrechargerefundexecution.StatusPending {
			t.Fatalf("新生成明细应为 pending：%+v", r)
		}
		if r.AfterSaleID != item.ID || r.AccountType != walletCustomerBalance ||
			r.AccountID != strconv.Itoa(customer.ID) {
			t.Fatalf("执行明细未正确引用售后单 / 账户：%+v", r)
		}
	}
	if rows[0].BatchSourceID == rows[1].BatchSourceID {
		t.Fatalf("两个批次应有不同来源：%+v", rows)
	}

	// 重复执行不重复落账：幂等复用既有明细，总数不变。
	again, err := executeRechargeRefundByBatch(ctx, db, item.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 2 {
		t.Fatalf("重复执行应返回两条既有明细：%+v", again)
	}
	if total := db.EsportsRechargeRefundExecution.Query().CountX(ctx); total != 2 {
		t.Fatalf("重复执行不应重复落账，总数=%d", total)
	}
}

// 异常批次不落账：分摊额超过批次额的批次退款金额不可信，跳过生成明细，只落健康批次。
func TestRechargeRefundExecSkipsAnomalousBatch(t *testing.T) {
	db := openStoredValueDB(t, "esports_recharge_refund_exec_anomaly")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)

	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 10000, Note: "健康批次"}, "rre-recharge-ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 5000, Note: "异常批次"}, "rre-recharge-bad"); err != nil {
		t.Fatal(err)
	}
	// 伪造第二条批次的分摊额超过批次金额，制造 over_allocated 异常。
	entries := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.AccountTypeEQ(walletCustomerBalance),
		esportsledgerentry.AccountIDEQ(strconv.Itoa(customer.ID)),
		esportsledgerentry.EntryRoleEQ("recharge_batch"),
	).Order(ent.Asc(esportsledgerentry.FieldID)).AllX(ctx)
	if len(entries) != 2 {
		t.Fatalf("应有两个批次账本条目：%+v", entries)
	}
	bad := entries[1]
	db.EsportsLedgerAllocation.Create().
		SetAccountType(walletCustomerBalance).SetAccountID(strconv.Itoa(customer.ID)).
		SetConsumeSourceType("order").SetConsumeSourceID("RRE-FORGED-OVER").
		SetBatchSourceType(bad.SourceType).SetBatchSourceID(bad.SourceID).
		SetAmountCents(bad.DeltaCents + 2000).SaveX(ctx)

	rechargeTx := db.EsportsWalletTx.Query().Where(esportswallettx.SourceIDEQ("rre-recharge-ok")).OnlyX(ctx)
	item, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "recharge", SourceID: rechargeTx.ID, Kind: "refund_request",
		Channel: "admin", Demand: "客户要求按批次退款",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}

	rows, err := executeRechargeRefundByBatch(ctx, db, item.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("异常批次不应落账，只应落健康批次：%+v", rows)
	}
	if rows[0].BatchSourceID != entries[0].SourceID {
		t.Fatalf("应只落健康批次，got=%+v want=%s", rows, entries[0].SourceID)
	}
	if rows[0].AmountCents != 10000 {
		t.Fatalf("健康批次退款金额不正确：%+v", rows)
	}
}

package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportswallettx"
)

// 审核临时验证：储值卡账户在「钱包流水求和 == 批次账本求和」维度的表现。
func TestAuditTmpReconcileStoredValueCard(t *testing.T) {
	db := openStoredValueDB(t, "esports_audit_tmp_sv")
	defer db.Close()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()

	in := storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "AUDIT-RECEIPT-1", PrincipalCents: 10000, BonusCents: 2000,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "audit-sv-open-1",
	}
	if _, err := openVerifiedStoredValueCard(t, ctx, db, 11, in, now); err != nil {
		t.Fatal(err)
	}
	result, err := reconcileLedgers(ctx, db, ledgerReconcileScanCap)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("【外部收款开卡】异常 %d 条：", len(result.Anomalies))
	for _, item := range result.Anomalies {
		t.Logf("  kind=%s account=%s#%s detail=%s", item.Kind, item.AccountType, item.AccountID, item.Detail)
	}
}

// 审核临时验证：用余额购卡（账户间划转）后的核验表现。
func TestAuditTmpReconcileBalancePurchase(t *testing.T) {
	db := openStoredValueDB(t, "esports_audit_tmp_bp")
	defer db.Close()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()

	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 10000, Note: "充值"}, "audit-bp-recharge"); err != nil {
		t.Fatal(err)
	}
	if _, err := openStoredValueCard(ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "customer_balance",
		PrincipalCents: 3000, ContractConfirmed: true, ValidityDays: 30,
		IdempotencyKey: "audit-bp-open-1",
	}, now); err != nil {
		t.Fatal(err)
	}
	result, err := reconcileLedgers(ctx, db, ledgerReconcileScanCap)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("【余额购卡】异常 %d 条：", len(result.Anomalies))
	for _, item := range result.Anomalies {
		t.Logf("  kind=%s account=%s#%s detail=%s", item.Kind, item.AccountType, item.AccountID, item.Detail)
	}
}

// 审核临时验证：活动充值（source_id 为幂等键而非流水号）后的核验表现。
func TestAuditTmpReconcileCampaignRecharge(t *testing.T) {
	db := openStoredValueDB(t, "esports_audit_tmp_cr")
	defer db.Close()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()

	if _, err := rechargeWithCampaigns(ctx, db, 11, walletCustomerBalance, customer.ID,
		10000, "活动充值", "audit-cr-key-1", now); err != nil {
		t.Fatal(err)
	}
	result, err := reconcileLedgers(ctx, db, ledgerReconcileScanCap)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("【活动充值】异常 %d 条：", len(result.Anomalies))
	for _, item := range result.Anomalies {
		t.Logf("  kind=%s account=%s#%s detail=%s", item.Kind, item.AccountType, item.AccountID, item.Detail)
	}
	rows := db.EsportsWalletTx.Query().Where(esportswallettx.SourceIDEQ("audit-cr-key-1")).AllX(ctx)
	for _, row := range rows {
		t.Logf("  流水 tx_no=%s id=%d source=%s/%s", row.TxNo, row.ID, row.SourceType, row.SourceID)
	}
}

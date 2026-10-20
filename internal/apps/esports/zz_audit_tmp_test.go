package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportswallettx"
)

// 审核临时验证：活动充值（source_id 为幂等键而非流水号）后的核验表现。
func TestAuditTmpReconcileCampaignRecharge(t *testing.T) {
	db := openWalletTestDB(t, "esports_audit_tmp_cr")
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

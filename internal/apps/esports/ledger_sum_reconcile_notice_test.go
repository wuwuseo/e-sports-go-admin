package esports

import (
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/role"
)

// P1-21 账本对账异常的通知出口：ledger. 前缀事件 + 收件人字段（财务优先、回落超管），
// 异常持续存在时同一小时窗口内不重复轰炸，且异常仍可经查询接口读到。
func TestLedgerReconcileAnomalyNotifiesFinanceOrSuperAdmin(t *testing.T) {
	db := openStoredValueDB(t, "esports_ledger_reconcile_notice")
	defer db.Close()
	ctx := t.Context()

	super := seedAdmin(t, db, "ledger-super")
	db.Admin.UpdateOneID(super.ID).SetIsSuperAdmin(true).ExecX(ctx)

	// 账户 501 只有批次账本 500 分、没有钱包流水 → 报「钱包流水求和 != 批次账本求和」。
	db.EsportsWalletAccount.Create().SetAccountType(walletCustomerBalance).SetOwnerID(501).
		SetDisplayName("对账测试账户 501").SaveX(ctx)
	db.EsportsLedgerEntry.Create().SetAccountType(walletCustomerBalance).SetAccountID("501").
		SetSourceType("test").SetSourceID("batch-501").
		SetEntryRole("recharge_batch").SetDeltaCents(500).SaveX(ctx)

	result, err := reconcileLedgers(ctx, db, ledgerReconcileScanCap)
	if err != nil {
		t.Fatal(err)
	}
	if !hasLedgerAnomaly(result, ledgerAnomalyWalletBatchMismatch) {
		t.Fatalf("应报钱包流水与批次账本求和不一致：%+v", result.Anomalies)
	}

	notices := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ(ledgerReconcileNoticeEventType),
	).AllX(ctx)
	if len(notices) != 1 {
		t.Fatalf("未配置财务角色时应给超管写 1 条通知，实际 %d 条", len(notices))
	}
	notice := notices[0]
	if !strings.HasPrefix(notice.EventType, "ledger.") {
		t.Fatalf("对账异常通知事件类型必须带 ledger. 前缀以便通知分拣识别：%s", notice.EventType)
	}
	if notice.AggregateRef != "ledger" {
		t.Fatalf("账本通知的聚合键应为 ledger：%+v", notice)
	}
	if got := notice.Payload["admin_id"]; got != float64(super.ID) {
		t.Fatalf("通知收件人应为超管 %d：%+v", super.ID, notice.Payload)
	}
	roles, _ := notice.Payload[ledgerReconcileNoticeRolesField].([]interface{})
	if len(roles) != 1 || roles[0] != "super_admin" {
		t.Fatalf("通知未标注超管角色：%+v", notice.Payload)
	}
	if count, _ := notice.Payload["anomaly_count"].(float64); count < 1 {
		t.Fatalf("通知未带异常条数：%+v", notice.Payload)
	}
	if endpoint, _ := notice.Payload["query_endpoint"].(string); endpoint != ledgerReconcileNoticeQueryPath {
		t.Fatalf("通知未带异常查询入口：%+v", notice.Payload)
	}

	// 同一小时窗口内重复核验不重复通知。
	if _, err = reconcileLedgers(ctx, db, ledgerReconcileScanCap); err != nil {
		t.Fatal(err)
	}
	if count := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ(ledgerReconcileNoticeEventType),
	).CountX(ctx); count != 1 {
		t.Fatalf("同一小时窗口重复核验不应重复通知：%d", count)
	}

	// 配置财务角色后，新异常（账户 502）通知财务角色成员，而不是只投给超管。
	financeRole := db.Role.Create().SetName("finance").SetDisplayName("财务").
		SetStatus(role.StatusActive).SaveX(ctx)
	finance := seedAdmin(t, db, "ledger-finance")
	db.AdminRole.Create().SetAdminID(finance.ID).SetRoleID(financeRole.ID).SaveX(ctx)
	db.SystemConfig.Create().SetKey(financeReportRoleNamesKey).SetName("财务角色名").
		SetValue("finance").SetType("string").SetGroup("esports").SetStatus(1).SaveX(ctx)

	db.EsportsWalletAccount.Create().SetAccountType(walletCustomerBalance).SetOwnerID(502).
		SetDisplayName("对账测试账户 502").SaveX(ctx)
	db.EsportsLedgerEntry.Create().SetAccountType(walletCustomerBalance).SetAccountID("502").
		SetSourceType("test").SetSourceID("batch-502").
		SetEntryRole("recharge_batch").SetDeltaCents(800).SaveX(ctx)
	if _, err = reconcileLedgers(ctx, db, ledgerReconcileScanCap); err != nil {
		t.Fatal(err)
	}
	financeNotices := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ(ledgerReconcileNoticeEventType),
	).AllX(ctx)
	toFinance := 0
	for _, item := range financeNotices {
		if item.Payload["admin_id"] == float64(finance.ID) {
			toFinance++
			roles, _ := item.Payload[ledgerReconcileNoticeRolesField].([]interface{})
			if len(roles) != 1 || roles[0] != "finance" {
				t.Fatalf("财务通知未标注 finance 角色：%+v", item.Payload)
			}
		}
		if item.Payload["admin_id"] == float64(super.ID) && item.Payload["account_id"] == "502" {
			t.Fatalf("已配置财务角色时不应再给超管写新通知：%+v", item.Payload)
		}
	}
	if toFinance == 0 {
		t.Fatalf("财务角色成员未收到对账异常通知")
	}
}

// P1-21 异常仍可查询：核验异常事件写入后，管理端查询接口能读到同一次核验的异常清单。
func TestLedgerReconcileAnomalyStillQueryable(t *testing.T) {
	db := openStoredValueDB(t, "esports_ledger_reconcile_query")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	db.EsportsWalletAccount.Create().SetAccountType(walletCustomerBalance).SetOwnerID(601).
		SetDisplayName("对账查询账户 601").SaveX(ctx)
	db.EsportsLedgerEntry.Create().SetAccountType(walletCustomerBalance).SetAccountID("601").
		SetSourceType("test").SetSourceID("batch-601").
		SetEntryRole("recharge_batch").SetDeltaCents(300).SaveX(ctx)

	result, err := reconcileLedgers(ctx, db, ledgerReconcileScanCap)
	if err != nil {
		t.Fatal(err)
	}
	if err = publishLedgerReconcileAnomalies(ctx, db, result, now); err != nil {
		t.Fatal(err)
	}
	views, err := listLedgerReconcileAnomalies(ctx, db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].AnomalyCount < 1 {
		t.Fatalf("核验异常应可查询：%+v", views)
	}
	found := false
	for _, item := range views[0].Anomalies {
		if item.Kind == ledgerAnomalyWalletBatchMismatch && item.AccountID == "601" {
			found = true
		}
	}
	if !found {
		t.Fatalf("查询结果缺少账户 601 的求和异常：%+v", views[0].Anomalies)
	}
}

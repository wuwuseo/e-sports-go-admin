package esports

import (
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/role"
)

// ：账本核验的全部异常种类（负余额 / 批次缺失 / 批次超额分摊 / 钱包流水未登记 /
// 钱包与批次求和不一致）必须经同一个通知出口写成 ledger. 前缀事件，并带 admin_id 收件人与
// 小时级幂等键。本文件是该出口的回归用例。

// w8LedgerNoticesOfAccount 取某账户的对账异常通知事件（按 ID 升序）。
func w8LedgerNoticesOfAccount(t *testing.T, db *ent.Client, accountType, accountID string) []*ent.EsportsOutbox {
	t.Helper()
	rows := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ(ledgerReconcileNoticeEventType),
	).Order(ent.Asc(esportsoutbox.FieldID)).AllX(t.Context())
	matched := make([]*ent.EsportsOutbox, 0, len(rows))
	for _, row := range rows {
		if row.Payload["account_type"] == accountType && row.Payload["account_id"] == accountID {
			matched = append(matched, row)
		}
	}
	return matched
}

// w8LedgerNoticeKinds 取通知载荷里列出的异常种类。
func w8LedgerNoticeKinds(t *testing.T, notice *ent.EsportsOutbox) []string {
	t.Helper()
	raw, ok := notice.Payload["anomalies"].([]interface{})
	if !ok {
		t.Fatalf("通知未携带异常清单：%+v", notice.Payload)
	}
	kinds := make([]string, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if kind, _ := entry["kind"].(string); kind != "" {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func w8ContainsKind(kinds []string, want string) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

// 五类异常全部走同一通知出口：每个账户一条事件、种类齐全、收件人与幂等口径一致，
// 且同一小时窗口内重复核验不重复发送。
func TestLedgerReconcileAllAnomalyKindsShareNoticeExit(t *testing.T) {
	db := openWalletTestDB(t, "esports_ledger_notice_exit")
	defer db.Close()
	ctx := t.Context()

	super := seedAdmin(t, db, "ledger-notice-exit-super")
	db.Admin.UpdateOneID(super.ID).SetIsSuperAdmin(true).ExecX(ctx)

	// ① 账户 701：本金被透支 → 负余额；出账无批次账本 / 分摊 → 未登记出账。
	db.EsportsWalletAccount.Create().SetAccountType(walletCustomerBalance).SetOwnerID(701).
		SetDisplayName("通知出口账户 701").SaveX(ctx)
	db.EsportsWalletTx.Create().SetTxNo("notice-neg-701").
		SetAccountType(esportswallettx.AccountType(walletCustomerBalance)).SetOwnerID(701).
		SetEntryType(esportswallettx.EntryTypeConsume).SetPrincipalDeltaCents(-2000).
		SetSourceType("order").SetSourceID("NOTICE-NEG-701").SaveX(ctx)

	// ② 账户 702：批次账本 1000 分，分摊 3000 分 → 批次超额分摊；
	//    同时该账户没有钱包流水而批次账本有 1000 分 → 求和不一致。
	db.EsportsWalletAccount.Create().SetAccountType(walletCustomerBalance).SetOwnerID(702).
		SetDisplayName("通知出口账户 702").SaveX(ctx)
	db.EsportsLedgerEntry.Create().SetAccountType(walletCustomerBalance).SetAccountID("702").
		SetSourceType("wallet_tx").SetSourceID("batch-702").
		SetEntryRole("recharge_batch").SetDeltaCents(1000).SaveX(ctx)
	db.EsportsLedgerAllocation.Create().
		SetAccountType(walletCustomerBalance).SetAccountID("702").
		SetConsumeSourceType("order").SetConsumeSourceID("NOTICE-OVER-702").
		SetBatchSourceType("wallet_tx").SetBatchSourceID("batch-702").
		SetAmountCents(3000).SaveX(ctx)

	// ③ 账户 703：分摊指向不存在的批次 → 批次缺失。
	db.EsportsLedgerAllocation.Create().
		SetAccountType(walletCustomerBalance).SetAccountID("703").
		SetConsumeSourceType("order").SetConsumeSourceID("NOTICE-MISS-703").
		SetBatchSourceType("wallet_tx").SetBatchSourceID("NO-SUCH-BATCH-703").
		SetAmountCents(500).SaveX(ctx)

	result, err := reconcileLedgers(ctx, db, ledgerReconcileScanCap)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{
		ledgerAnomalyNegativeBalance, ledgerAnomalyWalletOutflowUnbooked, ledgerAnomalyWalletBatchMismatch,
		ledgerAnomalyBatchOverAllocated, ledgerAnomalyBatchMissing,
	} {
		if !hasLedgerAnomaly(result, kind) {
			t.Fatalf("核验结果缺少异常 %s：%+v", kind, result.Anomalies)
		}
	}

	cases := []struct {
		accountID string
		wantKinds []string
	}{
		{"701", []string{ledgerAnomalyNegativeBalance, ledgerAnomalyWalletOutflowUnbooked}},
		{"702", []string{ledgerAnomalyBatchOverAllocated, ledgerAnomalyWalletBatchMismatch}},
		{"703", []string{ledgerAnomalyBatchMissing}},
	}
	for _, item := range cases {
		notices := w8LedgerNoticesOfAccount(t, db, walletCustomerBalance, item.accountID)
		if len(notices) != 1 {
			t.Fatalf("账户 %s 应产生且仅产生 1 条对账异常通知，实际 %d 条", item.accountID, len(notices))
		}
		notice := notices[0]
		if !strings.HasPrefix(notice.EventType, "ledger.") {
			t.Fatalf("通知事件类型必须带 ledger. 前缀以便通知分拣识别：%s", notice.EventType)
		}
		if notice.AggregateRef != "ledger" {
			t.Fatalf("账本通知的聚合键应为 ledger：%+v", notice)
		}
		if got := notice.Payload["admin_id"]; got != float64(super.ID) {
			t.Fatalf("账户 %s 的通知收件人应为超管 %d：%+v", item.accountID, super.ID, notice.Payload)
		}
		roles, _ := notice.Payload[ledgerReconcileNoticeRolesField].([]interface{})
		if len(roles) != 1 || roles[0] != "super_admin" {
			t.Fatalf("账户 %s 的通知未标注超管角色：%+v", item.accountID, notice.Payload)
		}
		if endpoint, _ := notice.Payload["query_endpoint"].(string); endpoint != ledgerReconcileNoticeQueryPath {
			t.Fatalf("账户 %s 的通知未带异常查询入口：%+v", item.accountID, notice.Payload)
		}
		kinds := w8LedgerNoticeKinds(t, notice)
		for _, want := range item.wantKinds {
			if !w8ContainsKind(kinds, want) {
				t.Fatalf("账户 %s 的通知缺少异常种类 %s：%v", item.accountID, want, kinds)
			}
		}
	}

	// 幂等：同一小时窗口内重复核验不再新增通知。
	if _, err = reconcileLedgers(ctx, db, ledgerReconcileScanCap); err != nil {
		t.Fatal(err)
	}
	if count := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ(ledgerReconcileNoticeEventType)).CountX(ctx); count != len(cases) {
		t.Fatalf("同一小时窗口重复核验不应重复通知：实际 %d 条，期望 %d 条", count, len(cases))
	}
}

// 幂等键 = 类型:小时窗口:收件人:账户类型:账户 ID：同小时去重、跨小时重新提醒。
func TestLedgerReconcileNoticeHourlyIdempotencyKey(t *testing.T) {
	db := openWalletTestDB(t, "esports_ledger_notice_hour_bucket")
	defer db.Close()
	ctx := t.Context()

	super := seedAdmin(t, db, "ledger-notice-bucket-super")
	db.Admin.UpdateOneID(super.ID).SetIsSuperAdmin(true).ExecX(ctx)

	anomalies := []ledgerAnomaly{{
		Kind: ledgerAnomalyBatchOverAllocated, AccountType: walletCustomerBalance,
		AccountID: "801", Detail: "批次超额分摊测试",
	}}
	now := time.Date(2026, 10, 19, 10, 5, 0, 0, time.UTC)
	for _, at := range []time.Time{now, now.Add(30 * time.Minute)} {
		if err := publishLedgerReconcileNotices(ctx, db, walletCustomerBalance, "801", anomalies, at); err != nil {
			t.Fatal(err)
		}
	}
	notices := w8LedgerNoticesOfAccount(t, db, walletCustomerBalance, "801")
	if len(notices) != 1 {
		t.Fatalf("同一小时窗口内应只写 1 条通知，实际 %d 条", len(notices))
	}
	wantKey := ledgerReconcileNoticeEventType + ":2026101910:" + strconv.Itoa(super.ID) +
		":" + walletCustomerBalance + ":801:" + ledgerAnomalyBatchOverAllocated
	if notices[0].EventKey != wantKey {
		t.Fatalf("通知幂等键口径不符：%s，期望 %s", notices[0].EventKey, wantKey)
	}
	// 跨小时窗口重新提醒。
	if err := publishLedgerReconcileNotices(ctx, db, walletCustomerBalance, "801", anomalies,
		now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := len(w8LedgerNoticesOfAccount(t, db, walletCustomerBalance, "801")); got != 2 {
		t.Fatalf("跨小时窗口应重新提醒一次，实际 %d 条", got)
	}
}

// 新接入出口的「批次超额分摊」类异常同样投递给财务角色成员（收件人口径与既有两类一致）。
func TestLedgerReconcileOverAllocatedNotifiesFinanceRole(t *testing.T) {
	db := openWalletTestDB(t, "esports_ledger_notice_finance_over")
	defer db.Close()
	ctx := t.Context()

	super := seedAdmin(t, db, "ledger-over-super")
	db.Admin.UpdateOneID(super.ID).SetIsSuperAdmin(true).ExecX(ctx)
	financeRole := db.Role.Create().SetName("finance").SetDisplayName("财务").
		SetStatus(role.StatusActive).SaveX(ctx)
	finance := seedAdmin(t, db, "ledger-over-finance")
	db.AdminRole.Create().SetAdminID(finance.ID).SetRoleID(financeRole.ID).SaveX(ctx)
	db.SystemConfig.Create().SetKey(financeReportRoleNamesKey).SetName("财务角色名").
		SetValue("finance").SetType("string").SetGroup("esports").SetStatus(1).SaveX(ctx)

	db.EsportsWalletAccount.Create().SetAccountType(walletCustomerBalance).SetOwnerID(901).
		SetDisplayName("财务对账账户 901").SaveX(ctx)
	db.EsportsLedgerEntry.Create().SetAccountType(walletCustomerBalance).SetAccountID("901").
		SetSourceType("wallet_tx").SetSourceID("batch-901").
		SetEntryRole("recharge_batch").SetDeltaCents(500).SaveX(ctx)
	db.EsportsLedgerAllocation.Create().
		SetAccountType(walletCustomerBalance).SetAccountID("901").
		SetConsumeSourceType("order").SetConsumeSourceID("FINANCE-OVER-901").
		SetBatchSourceType("wallet_tx").SetBatchSourceID("batch-901").
		SetAmountCents(2000).SaveX(ctx)

	if _, err := reconcileLedgers(ctx, db, ledgerReconcileScanCap); err != nil {
		t.Fatal(err)
	}
	notices := w8LedgerNoticesOfAccount(t, db, walletCustomerBalance, "901")
	if len(notices) != 1 {
		t.Fatalf("账户 901 应产生 1 条通知，实际 %d 条", len(notices))
	}
	if got := notices[0].Payload["admin_id"]; got != float64(finance.ID) {
		t.Fatalf("已配置财务角色时通知应投给财务 %d：%+v", finance.ID, notices[0].Payload)
	}
	roles, _ := notices[0].Payload[ledgerReconcileNoticeRolesField].([]interface{})
	if len(roles) != 1 || roles[0] != "finance" {
		t.Fatalf("财务通知未标注 finance 角色：%+v", notices[0].Payload)
	}
	if !w8ContainsKind(w8LedgerNoticeKinds(t, notices[0]), ledgerAnomalyBatchOverAllocated) {
		t.Fatalf("财务通知缺少批次超额分摊种类：%+v", notices[0].Payload)
	}
}

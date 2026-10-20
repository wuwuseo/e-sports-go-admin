package esports

import (
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsledgerentry"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportswallettx"
)

func hasLedgerAnomaly(result ledgerReconcileResult, kind string) bool {
	for _, item := range result.Anomalies {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

// 账本全量核验（§11.6）：健康账本无异常，异常账本按不变量分类报出，异常落外盒事件。
func TestLedgerReconcileDetectsAnomalies(t *testing.T) {
	db := openWalletTestDB(t, "esports_ledger_reconcile")
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)

	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 10000, Note: "充值"}, "reconcile-recharge-1"); err != nil {
		t.Fatal(err)
	}
	sku, err := saveSku(ctx, db, skuInput{
		Code: "RECON", Title: "核验商品", ServiceType: "play", UnitPriceCents: 10000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"reconcile-order-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := payOrderWithBalance(ctx, db, 11, order.ID, now); err != nil {
		t.Fatal(err)
	}

	clean, err := reconcileLedgers(ctx, db, ledgerReconcileScanCap)
	if err != nil {
		t.Fatal(err)
	}
	if len(clean.Anomalies) != 0 {
		t.Fatalf("正常账本不应报异常：%+v", clean.Anomalies)
	}
	if clean.ScannedAccounts == 0 || clean.ScannedAllocations == 0 || clean.ScannedBatches == 0 {
		t.Fatalf("应扫描到账户、分摊与批次：%+v", clean)
	}
	if err := publishLedgerReconcileAnomalies(ctx, db, clean, now); err != nil {
		t.Fatal(err)
	}
	if n := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("ledger.reconcile_anomaly")).CountX(ctx); n != 0 {
		t.Fatalf("无异常时不应落外盒事件：%d", n)
	}

	// 超额分摊：直接伪造一条大于批次金额的分摊（正常业务路径有 available 判定，写不出来）。
	batch := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.EntryRoleEQ("recharge_batch")).OnlyX(ctx)
	db.EsportsLedgerAllocation.Create().
		SetAccountType(walletCustomerBalance).SetAccountID(strconv.Itoa(customer.ID)).
		SetConsumeSourceType("order").SetConsumeSourceID("FORGED-OVER").
		SetBatchSourceType(batch.SourceType).SetBatchSourceID(batch.SourceID).
		SetAmountCents(batch.DeltaCents + 5000).SaveX(ctx)

	result, err := reconcileLedgers(ctx, db, ledgerReconcileScanCap)
	if err != nil {
		t.Fatal(err)
	}
	if !hasLedgerAnomaly(result, ledgerAnomalyBatchOverAllocated) {
		t.Fatalf("应报批次超额分摊：%+v", result.Anomalies)
	}
	if err := publishLedgerReconcileAnomalies(ctx, db, result, now); err != nil {
		t.Fatal(err)
	}
	events := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("ledger.reconcile_anomaly")).AllX(ctx)
	if len(events) != 1 {
		t.Fatalf("异常应落一条外盒事件：%d", len(events))
	}
	if count, ok := events[0].Payload["anomaly_count"].(float64); !ok || count < 1 {
		t.Fatalf("事件 payload 应带异常条数：%+v", events[0].Payload)
	}
	// 重复触发同一时刻只保留一条，避免同一轮核验重复告警。
	if err := publishLedgerReconcileAnomalies(ctx, db, result, now); err != nil {
		t.Fatal(err)
	}
	if n := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("ledger.reconcile_anomaly")).CountX(ctx); n != 1 {
		t.Fatalf("同一时刻重复发布应幂等：%d", n)
	}
}

// 分摊引用的批次条目不存在（或不是正向条目）时按「批次缺失」报出。
func TestLedgerReconcileDetectsMissingBatch(t *testing.T) {
	db := openWalletTestDB(t, "esports_ledger_reconcile_batch")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 5000, Note: "充值"}, "reconcile-recharge-2"); err != nil {
		t.Fatal(err)
	}
	db.EsportsLedgerAllocation.Create().
		SetAccountType(walletCustomerBalance).SetAccountID(strconv.Itoa(customer.ID)).
		SetConsumeSourceType("order").SetConsumeSourceID("FORGED-BATCH").
		SetBatchSourceType("wallet_tx").SetBatchSourceID("NO-SUCH-BATCH").
		SetAmountCents(1000).SaveX(ctx)

	result, err := reconcileLedgers(ctx, db, ledgerReconcileScanCap)
	if err != nil {
		t.Fatal(err)
	}
	if !hasLedgerAnomaly(result, ledgerAnomalyBatchMissing) {
		t.Fatalf("应报批次账本缺失：%+v", result.Anomalies)
	}
	if hasLedgerAnomaly(result, ledgerAnomalyBatchOverAllocated) {
		t.Fatalf("批次不存在时不应同时报超额：%+v", result.Anomalies)
	}
}

// 账户流水求和为负（超扣或重复冲正）时按「负余额」报出。
func TestLedgerReconcileDetectsNegativeBalance(t *testing.T) {
	db := openWalletTestDB(t, "esports_ledger_reconcile_negative")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 10000, Note: "充值"}, "reconcile-recharge-3"); err != nil {
		t.Fatal(err)
	}
	db.EsportsWalletTx.Create().
		SetTxNo("forged-negative-1").
		SetAccountType(esportswallettx.AccountType(walletCustomerBalance)).
		SetOwnerID(customer.ID).SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-20000).
		SetSourceType("order").SetSourceID("FORGED-NEGATIVE").SaveX(ctx)

	result, err := reconcileLedgers(ctx, db, ledgerReconcileScanCap)
	if err != nil {
		t.Fatal(err)
	}
	if !hasLedgerAnomaly(result, ledgerAnomalyNegativeBalance) {
		t.Fatalf("应报账户余额为负：%+v", result.Anomalies)
	}
}

// 达单轮扫描上限时标记截断，提示下轮继续，不静默漏检。
func TestLedgerReconcileMarksScanTruncated(t *testing.T) {
	db := openWalletTestDB(t, "esports_ledger_reconcile_cap")
	ctx := t.Context()
	first := saveCustomerOrFail(t, db, 11)
	second, err := saveCustomer(ctx, db, 11, customerInput{DisplayName: "第二个客户"})
	if err != nil {
		t.Fatal(err)
	}
	for index, ownerID := range []int{first.ID, second.ID} {
		if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, ownerID,
			walletInput{PrincipalCents: 1000, Note: "充值"}, "reconcile-cap-"+strconv.Itoa(index)); err != nil {
			t.Fatal(err)
		}
	}

	result, err := reconcileLedgers(ctx, db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ScanTruncated {
		t.Fatalf("扫描上限为 1 且有剩余账户时应标记截断：%+v", result)
	}
	if result.ScannedAccounts != 1 {
		t.Fatalf("应只扫描一个账户：%d", result.ScannedAccounts)
	}
	// 放开上限后，本轮应从持久游标继续，只扫剩余的一个账户（不重复扫已扫部分），
	// 扫完整圈后不再标记截断。
	full, err := reconcileLedgers(ctx, db, ledgerReconcileScanCap)
	if err != nil {
		t.Fatal(err)
	}
	if full.ScanTruncated || full.ScannedAccounts != 1 {
		t.Fatalf("放开上限后应扫完剩余一个账户且不再截断：%+v", full)
	}
}

// 持久游标持续推进：账户数超过单轮上限时，连续多轮运行覆盖全部账户，且发现后半段的负余额异常。
func TestLedgerReconcileCursorCoversTailAccounts(t *testing.T) {
	db := openWalletTestDB(t, "esports_ledger_reconcile_cursor_accounts")
	ctx := t.Context()
	const accountCount = 25
	const scanCap = 10

	var tail *ent.EsportsCustomer
	for i := 0; i < accountCount; i++ {
		customer := saveCustomerOrFail(t, db, 11)
		if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
			walletInput{PrincipalCents: 1000, Note: "充值"}, "cursor-account-"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
		tail = customer
	}
	// 后半段异常：给最后一个账户伪造一笔超额消费，使流水求和为负。
	db.EsportsWalletTx.Create().
		SetTxNo("forged-negative-tail").
		SetAccountType(esportswallettx.AccountType(walletCustomerBalance)).
		SetOwnerID(tail.ID).SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-20000).
		SetSourceType("order").SetSourceID("FORGED-NEGATIVE-TAIL").SaveX(ctx)

	var rounds []int
	var total int
	found := false
	for round := 0; round < 10; round++ {
		res, err := reconcileLedgers(ctx, db, scanCap)
		if err != nil {
			t.Fatal(err)
		}
		rounds = append(rounds, res.ScannedAccounts)
		total += res.ScannedAccounts
		if hasLedgerAnomaly(res, ledgerAnomalyNegativeBalance) {
			found = true
		}
		if !res.ScanTruncated {
			break
		}
	}
	if total != accountCount {
		t.Fatalf("连续运行应覆盖全部账户：扫了 %d，期望 %d（各轮 %v）", total, accountCount, rounds)
	}
	if !found {
		t.Fatalf("应发现后半段账户的负余额异常：各轮 %v", rounds)
	}
	// 单轮不重复扫已扫部分：25 个账户按 cap=10 应为 10 / 10 / 5。
	if len(rounds) != 3 || rounds[0] != 10 || rounds[1] != 10 || rounds[2] != 5 {
		t.Fatalf("各轮扫描分布应为 10/10/5：%v", rounds)
	}
}

// 持久游标持续推进：分摊数超过单轮上限时，连续多轮运行覆盖全部分摊，且发现后半段的批次缺失异常。
func TestLedgerReconcileCursorCoversTailAllocations(t *testing.T) {
	db := openWalletTestDB(t, "esports_ledger_reconcile_cursor_allocations")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 1000, Note: "充值"}, "cursor-alloc-recharge"); err != nil {
		t.Fatal(err)
	}
	batch := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.EntryRoleEQ("recharge_batch")).OnlyX(ctx)

	// 前 20 条分摊指向真实批次（正常不超），后 5 条指向不存在的批次（后半段异常）。
	for i := 0; i < 20; i++ {
		db.EsportsLedgerAllocation.Create().
			SetAccountType(walletCustomerBalance).SetAccountID(strconv.Itoa(customer.ID)).
			SetConsumeSourceType("order").SetConsumeSourceID("OK-" + strconv.Itoa(i)).
			SetBatchSourceType(batch.SourceType).SetBatchSourceID(batch.SourceID).
			SetAmountCents(1).SaveX(ctx)
	}
	for i := 0; i < 5; i++ {
		db.EsportsLedgerAllocation.Create().
			SetAccountType(walletCustomerBalance).SetAccountID(strconv.Itoa(customer.ID)).
			SetConsumeSourceType("order").SetConsumeSourceID("MISS-" + strconv.Itoa(i)).
			SetBatchSourceType("wallet_tx").SetBatchSourceID("NO-SUCH-BATCH-" + strconv.Itoa(i)).
			SetAmountCents(1).SaveX(ctx)
	}

	const scanCap = 10
	var rounds []int
	var total int
	found := false
	for round := 0; round < 10; round++ {
		res, err := reconcileLedgers(ctx, db, scanCap)
		if err != nil {
			t.Fatal(err)
		}
		rounds = append(rounds, res.ScannedAllocations)
		total += res.ScannedAllocations
		if hasLedgerAnomaly(res, ledgerAnomalyBatchMissing) {
			found = true
		}
		if !res.ScanTruncated {
			break
		}
	}
	if total != 25 {
		t.Fatalf("连续运行应覆盖全部分摊：扫了 %d，期望 25（各轮 %v）", total, rounds)
	}
	if !found {
		t.Fatalf("应发现后半段分摊的批次缺失异常：各轮 %v", rounds)
	}
	if len(rounds) != 3 || rounds[0] != 10 || rounds[1] != 10 || rounds[2] != 5 {
		t.Fatalf("各轮扫描分布应为 10/10/5：%v", rounds)
	}
}

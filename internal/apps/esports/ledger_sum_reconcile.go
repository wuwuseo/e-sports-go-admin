package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsledgerallocation"
	"go-admin/internal/ent/esportsledgerentry"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportswallettx"
)

// 账本核验新增维度：「钱包流水求和 == 批次账本求和」（§11.6-01）。
//
// 口径（逐账户比对；登记账户 = customer_balance / player_catfood，储值卡 = 每张卡一个账户）：
//
//	钱包净额   = Σ wallet_tx(principal_delta + bonus_delta)   （该钱包账户）
//	批次净额   = Σ ledger_entry.delta                          （对应批次账本账户）
//	跨账户消费 = Σ ledger_entry.delta（account_type=customer_order_prepayment，account_id=同一归属人）
//	             余额核销订单 / 加钟时，钱从余额转到「订单预付」账户，批次账户本身不再记一条；
//	             该项把这笔跨账户记账还原回比对口径（仅客户余额适用）。
//	未登记净额 = Σ wallet_tx(principal_delta + bonus_delta)，仅取「在全部账本里都找不到对应条目」的流水
//	             （按 source_id = 流水号 或 流水主键匹配）；这些是尚未落批次账本的资金流动，
//	             对预付账户的出账另行作为 wallet_outflow_unbooked 报出，避免把口径差异伪装成账不平。
//
// 报错条件：钱包净额 != 批次净额 − 跨账户消费 + 未登记净额。
// 正常数据下恒等；一旦出现「有流水无账本」「有账本无流水」「两侧金额不等」都会报出。
//
// 两处必须记住的映射差异：
//   - 储值卡钱包账户的 account_type 是 `stored_value_card`，其批次账本账户是 `stored_value_prepay`；
//     两侧靠 owner_id == account_id == 卡 ID 对齐（schema/esports_wallet.go 的账户口径）。
//   - 客户余额核销订单的批次记账落在 `customer_order_prepayment`，故需要跨账户还原项。

// ledgerAnomalyWalletBatchMismatch 钱包流水求和与批次账本求和不一致。
const ledgerAnomalyWalletBatchMismatch = "wallet_batch_sum_mismatch"

// ledgerAnomalyWalletOutflowUnbooked 预付账户的出账没有登记批次账本 / 分摊明细。
const ledgerAnomalyWalletOutflowUnbooked = "wallet_outflow_unbooked"

// ledgerPrepayAccountType 余额核销订单时承接资金的账本账户类型。
const ledgerPrepayAccountType = "customer_order_prepayment"

// ledgerWalletSumScan 是单个账户的一次比对结果。
type ledgerWalletSumScan struct {
	accountType   string
	accountID     string
	walletCents   int64
	batchCents    int64
	crossCents    int64
	unbookedCents int64
	unbooked      []string
}

// ledgerBatchAccountType 把钱包账户类型映射到其批次账本账户类型（储值卡是唯一不一致的一处）。
func ledgerBatchAccountType(walletAccountType string) string {
	if walletAccountType == walletStoredValueCard {
		return storedValuePrepayAccount
	}
	return walletAccountType
}

// scanWalletLedgerSum 对单个账户执行「钱包流水求和 == 批次账本求和」核验（§11.6-01）。
// 由余额核验的同一轮账户扫描顺带调用：两者账户集合完全一致（登记钱包账户 + 逐张储值卡），
// 复用同一游标与单轮上限，不新增配置键、不重复扫表。
func scanWalletLedgerSum(ctx context.Context, db *ent.Client,
	result *ledgerReconcileResult, accountType, accountID string) error {
	result.ScannedWalletSums++
	return checkWalletLedgerSum(ctx, db, result, ledgerWalletSumScan{
		accountType: accountType, accountID: accountID,
	})
}

// checkWalletLedgerSum 汇总单个账户的两侧口径、比对并输出异常。
func checkWalletLedgerSum(ctx context.Context, db *ent.Client,
	result *ledgerReconcileResult, scan ledgerWalletSumScan) error {
	rows, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountType(scan.accountType)),
		esportswallettx.OwnerIDEQ(atoiOrZero(scan.accountID)),
	).All(ctx)
	if err != nil {
		return err
	}
	booked, err := walletRowsBookedInLedger(ctx, db, rows)
	if err != nil {
		return err
	}
	prepaid := ledgerPrepayTracker(scan.accountType)
	for i, row := range rows {
		delta := row.PrincipalDeltaCents + row.BonusDeltaCents
		scan.walletCents += delta
		if booked[i] {
			continue
		}
		scan.unbookedCents += delta
		if delta >= 0 || !prepaid {
			continue
		}
		// 预付性质账户的出账必须能在批次层追溯到（负向账本条目或分摊明细至少有一处）。
		allocated, err := walletRowHasAllocation(ctx, db, scan, row)
		if err != nil {
			return err
		}
		if allocated {
			continue
		}
		scan.unbooked = append(scan.unbooked, fmt.Sprintf(
			"流水 %s（%s#%s）出账 %d 分未登记批次账本或分摊明细",
			row.TxNo, row.SourceType, row.SourceID, -delta))
	}
	batchAccount := ledgerBatchAccountType(scan.accountType)
	entries, err := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.AccountTypeEQ(batchAccount),
		esportsledgerentry.AccountIDEQ(scan.accountID),
	).Select(esportsledgerentry.FieldDeltaCents).All(ctx)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		scan.batchCents += entry.DeltaCents
	}
	if scan.accountType == walletCustomerBalance {
		cross, err := db.EsportsLedgerEntry.Query().Where(
			esportsledgerentry.AccountTypeEQ(ledgerPrepayAccountType),
			esportsledgerentry.AccountIDEQ(scan.accountID),
		).Select(esportsledgerentry.FieldDeltaCents).All(ctx)
		if err != nil {
			return err
		}
		for _, entry := range cross {
			scan.crossCents += entry.DeltaCents
		}
	}
	expected := scan.batchCents - scan.crossCents + scan.unbookedCents
	if scan.walletCents != expected {
		result.add(ledgerAnomalyWalletBatchMismatch, scan.accountType, scan.accountID,
			fmt.Sprintf("钱包流水求和 %d 分，批次账本求和 %d 分（跨账户消费 %d 分，未登记 %d 分），差额 %d 分",
				scan.walletCents, scan.batchCents, scan.crossCents, scan.unbookedCents,
				scan.walletCents-expected))
	}
	for _, detail := range scan.unbooked {
		result.add(ledgerAnomalyWalletOutflowUnbooked, scan.accountType, scan.accountID, detail)
	}
	return nil
}

// ledgerPrepayTracker 只有预付性质账户参与「出账必须批次可追溯」的判定：
// 客户余额与储值卡是预付款，陪玩猫粮是结算发放（不走预付款批次）。
func ledgerPrepayTracker(accountType string) bool {
	return accountType == walletCustomerBalance || accountType == walletStoredValueCard
}

// walletRowHasAllocation 判断某笔出账是否有对应的批次分摊明细。
func walletRowHasAllocation(ctx context.Context, db *ent.Client, scan ledgerWalletSumScan,
	row *ent.EsportsWalletTx) (bool, error) {
	return db.EsportsLedgerAllocation.Query().Where(
		esportsledgerallocation.AccountTypeEQ(ledgerBatchAccountType(scan.accountType)),
		esportsledgerallocation.AccountIDEQ(scan.accountID),
		esportsledgerallocation.ConsumeSourceTypeEQ(row.SourceType),
		esportsledgerallocation.ConsumeSourceIDEQ(row.SourceID),
	).Exist(ctx)
}

// walletRowsBookedInLedger 标记哪些钱包流水在账本里有对应条目（按 source_id = 流水号 或 流水主键匹配，
// 不限定账户类型：余额核销订单的条目记在 customer_order_prepayment 账户下）。
func walletRowsBookedInLedger(ctx context.Context, db *ent.Client, rows []*ent.EsportsWalletTx) ([]bool, error) {
	booked := make([]bool, len(rows))
	if len(rows) == 0 {
		return booked, nil
	}
	indexBySource := map[string][]int{}
	keys := make([]string, 0, len(rows)*2)
	for i, row := range rows {
		for _, key := range []string{row.TxNo, strconv.Itoa(row.ID)} {
			if key == "" {
				continue
			}
			if _, ok := indexBySource[key]; !ok {
				keys = append(keys, key)
			}
			indexBySource[key] = append(indexBySource[key], i)
		}
	}
	const chunkSize = 200
	for start := 0; start < len(keys); start += chunkSize {
		end := min(start+chunkSize, len(keys))
		entries, err := db.EsportsLedgerEntry.Query().
			Where(esportsledgerentry.SourceIDIn(keys[start:end]...)).
			Select(esportsledgerentry.FieldSourceID).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			for _, i := range indexBySource[entry.SourceID] {
				booked[i] = true
			}
		}
	}
	return booked, nil
}

// atoiOrZero 解析账户 ID（账户 ID 一律为非负整数，解析失败按 0 处理，不会命中任何流水）。
func atoiOrZero(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

// ledgerReconcileAnomalyView 是管理端可查的核验异常事件视图（§11.6-01）。
type ledgerReconcileAnomalyView struct {
	EventKey           string          `json:"event_key"`
	OccurredAt         string          `json:"occurred_at"`
	ScannedAccounts    int             `json:"scanned_accounts"`
	ScannedAllocations int             `json:"scanned_allocations"`
	ScannedBatches     int             `json:"scanned_batches"`
	ScannedWalletSums  int             `json:"scanned_wallet_sums"`
	ScanTruncated      bool            `json:"scan_truncated"`
	AnomalyOverflow    bool            `json:"anomaly_overflow"`
	AnomalyCount       int             `json:"anomaly_count"`
	Anomalies          []ledgerAnomaly `json:"anomalies"`
}

// listLedgerReconcileAnomalies 列出最近的账本核验异常事件（§11.6-01 管理端可查）。
// 数据来源是核验任务写下的 ledger.reconcile_anomaly 外盒事件，按时间倒序。
func listLedgerReconcileAnomalies(ctx context.Context, db *ent.Client, limit int) ([]ledgerReconcileAnomalyView, error) {
	if limit < 1 || limit > 200 {
		limit = 20
	}
	events, err := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("ledger.reconcile_anomaly"),
	).Order(ent.Desc(esportsoutbox.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]ledgerReconcileAnomalyView, 0, len(events))
	for _, event := range events {
		payload := event.Payload
		view := ledgerReconcileAnomalyView{
			EventKey:           event.EventKey,
			OccurredAt:         stringField(payload, "occurred_at"),
			ScannedAccounts:    intField(payload, "scanned_accounts"),
			ScannedAllocations: intField(payload, "scanned_allocations"),
			ScannedBatches:     intField(payload, "scanned_batches"),
			ScannedWalletSums:  intField(payload, "scanned_wallet_sums"),
			ScanTruncated:      boolField(payload, "scan_truncated"),
			AnomalyOverflow:    boolField(payload, "anomaly_overflow"),
			AnomalyCount:       intField(payload, "anomaly_count"),
		}
		if raw, ok := payload["anomalies"].([]interface{}); ok {
			for _, item := range raw {
				entry, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				view.Anomalies = append(view.Anomalies, ledgerAnomaly{
					Kind: stringField(entry, "kind"), AccountType: stringField(entry, "account_type"),
					AccountID: stringField(entry, "account_id"), Detail: stringField(entry, "detail"),
				})
			}
		}
		views = append(views, view)
	}
	return views, nil
}

func stringField(payload map[string]interface{}, key string) string {
	if value, ok := payload[key].(string); ok {
		return value
	}
	return ""
}

func intField(payload map[string]interface{}, key string) int {
	switch value := payload[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return 0
}

func boolField(payload map[string]interface{}, key string) bool {
	value, _ := payload[key].(bool)
	return value
}

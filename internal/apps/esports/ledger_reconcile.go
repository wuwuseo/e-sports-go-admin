package esports

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsledgerallocation"
	"go-admin/internal/ent/esportsledgerentry"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/esportswalletaccount"
	"go-admin/internal/ent/systemconfig"
)

// 账本定时全量核验（§11.6 / §4.3 / §11.3）。
//
// 账户余额不落库、一律由流水求和派生（见 schema/esports_wallet.go），所以「余额与流水对不上」
// 在结构上不会发生；真正会静默错账的是批次级扣减与分摊引用。本任务只读扫描三类不变量：
//  1. 逐账户：本金与赠送的流水求和不为负（超扣、重复冲正会在这里暴露）；
//  2. 逐批次：被分摊总额不超过该正向批次条目的金额（`allocateLedgerConsumption` 的 available 判定
//     在并发或历史数据异常时可能被突破）；
//  3. 分摊引用完整性：分摊引用的批次条目必须存在且为正向，否则分摊指向了不存在的资金。
//
// 刻意不核验的两条（按现状推不出，做了只会误报）：
//   - 「钱包流水求和 == 批次账本求和」：两张表平行记账，余额核销写的是
//     account_type=customer_order_prepayment 的正向条目，与 wallet_tx 不是一一对应；
//   - 「分摊引用的消费条目存在」：`EsportsLedgerAllocation.consume_source_*` 记录的是消费业务来源
//     （如 order / 订单号），并不指向一条 `EsportsLedgerEntry`，按四元组检索必然落空。
//
// 核验结果只写外盒事件、储值卡一致性异常队列（§4.6-06）与任务日志，不修改任何账本数据。

const (
	// ledgerReconcileBatchSize 游标分页每批读取条数。
	ledgerReconcileBatchSize = 200
	// ledgerReconcileScanCap 单轮最多扫描的实体条数，防止大表把定时任务拖垮；超出时下轮继续。
	ledgerReconcileScanCap = 5000
	// ledgerReconcileMaxAnomalies 单轮最多输出的异常条数，避免外盒事件 payload 过大。
	ledgerReconcileMaxAnomalies = 200

	// 各扫描维度的持久游标键：记录「上一轮已扫到的最大 ID」，下一轮从该 ID 继续，
	// 扫完一整圈后重置为 0 从头开始，保证连续多轮运行覆盖全部数据而不重复扫。
	ledgerCursorKeyWalletAccount   = "app.esports.ledger_reconcile_cursor_wallet_account"
	ledgerCursorKeyStoredValueCard = "app.esports.ledger_reconcile_cursor_stored_value_card"
	ledgerCursorKeyAllocation      = "app.esports.ledger_reconcile_cursor_allocation"
)

// 异常类型。命名与事件 key 一起构成对外契约，勿随意改动。
const (
	ledgerAnomalyNegativeBalance    = "account_negative_balance"
	ledgerAnomalyBatchMissing       = "allocation_batch_missing"
	ledgerAnomalyBatchOverAllocated = "batch_over_allocated"
)

// ledgerAnomaly 单条账本对账异常。
type ledgerAnomaly struct {
	Kind        string `json:"kind"`
	AccountType string `json:"account_type"`
	AccountID   string `json:"account_id"`
	Detail      string `json:"detail"`
}

// ledgerReconcileResult 一轮核验的结果。
type ledgerReconcileResult struct {
	ScannedAccounts    int
	ScannedAllocations int
	ScannedBatches     int
	// ScannedWalletSums 是「钱包流水求和 == 批次账本求和」维度已核验的账户数（§11.6-01）。
	ScannedWalletSums int
	// ScanTruncated 表示已达单轮扫描上限、仍有数据未核验，下轮继续。
	ScanTruncated bool
	// AnomalyOverflow 表示异常条数超过输出上限，清单被截断（计数仍可反映异常存在）。
	AnomalyOverflow bool
	Anomalies       []ledgerAnomaly
}

func (r *ledgerReconcileResult) add(kind, accountType, accountID, detail string) {
	if len(r.Anomalies) >= ledgerReconcileMaxAnomalies {
		r.AnomalyOverflow = true
		return
	}
	r.Anomalies = append(r.Anomalies, ledgerAnomaly{
		Kind: kind, AccountType: accountType, AccountID: accountID, Detail: detail,
	})
}

// reconcileLedgers 全量核验账本，返回异常清单。只读，可重复执行。
func reconcileLedgers(ctx context.Context, db *ent.Client, scanCap int) (ledgerReconcileResult, error) {
	result := ledgerReconcileResult{}
	if scanCap < 1 || scanCap > ledgerReconcileScanCap {
		scanCap = ledgerReconcileScanCap
	}
	if err := reconcileWalletBalances(ctx, db, scanCap, &result); err != nil {
		return result, err
	}
	if err := reconcileLedgerAllocations(ctx, db, scanCap, &result); err != nil {
		return result, err
	}
	return result, nil
}

// reconcileWalletBalances 逐账户核验余额不为负：登记型钱包账户（客户余额 / 陪玩猫粮）
// 与储值卡（每张卡一个独立账户，account_id 为卡 ID）。两类账户各自独立持久游标与单轮上限，
// 互不挤占配额，连续多轮运行后各自扫完整圈。
func reconcileWalletBalances(ctx context.Context, db *ent.Client, scanCap int, result *ledgerReconcileResult) error {
	truncated, err := ledgerCursorScan(ctx, db, ledgerCursorKeyWalletAccount, scanCap,
		func(lastID, limit int) (int, int, error) {
			accounts, err := db.EsportsWalletAccount.Query().
				Where(esportswalletaccount.IDGT(lastID)).
				Order(ent.Asc(esportswalletaccount.FieldID)).
				Limit(limit).All(ctx)
			if err != nil {
				return 0, 0, err
			}
			for _, account := range accounts {
				result.ScannedAccounts++
				balance, err := walletBalanceOf(ctx, db, account.AccountType.String(), account.OwnerID)
				if err != nil {
					return 0, 0, err
				}
				checkWalletBalanceNonNegative(result, account.AccountType.String(), strconv.Itoa(account.OwnerID), balance)
				// §11.6-01：同一账户顺带核验「钱包流水求和 == 批次账本求和」（账户集合相同，不额外扫描）。
				if err = scanWalletLedgerSum(ctx, db, result,
					account.AccountType.String(), strconv.Itoa(account.OwnerID)); err != nil {
					return 0, 0, err
				}
			}
			if len(accounts) == 0 {
				return 0, 0, nil
			}
			return accounts[len(accounts)-1].ID, len(accounts), nil
		})
	if err != nil {
		return err
	}
	if truncated {
		result.ScanTruncated = true
	}
	return reconcileStoredValueCardBalances(ctx, db, scanCap, result)
}

func reconcileStoredValueCardBalances(ctx context.Context, db *ent.Client, scanCap int, result *ledgerReconcileResult) error {
	truncated, err := ledgerCursorScan(ctx, db, ledgerCursorKeyStoredValueCard, scanCap,
		func(lastID, limit int) (int, int, error) {
			cards, err := db.EsportsStoredValueCard.Query().
				Where(esportsstoredvaluecard.IDGT(lastID)).
				Order(ent.Asc(esportsstoredvaluecard.FieldID)).
				Limit(limit).All(ctx)
			if err != nil {
				return 0, 0, err
			}
			for _, card := range cards {
				result.ScannedAccounts++
				// §4.6-06 修正：卡的钱包流水 account_type 是 stored_value_card，stored_value_prepay
				// 只是它的批次账本账户——按预付款账户名求和恒为 0，导致「卡余额为负」这条不变量
				// 此前从未被真正核验过（同时让卡标记 / 异常队列拿不到真实派生余额）。
				balance, err := walletBalanceOf(ctx, db, walletStoredValueCard, card.ID)
				if err != nil {
					return 0, 0, err
				}
				// §4.6-06：本卡新产生的账本异常（余额为负）与卡标记不符都要落进可跟进的异常队列，
				// 而不是只写只增事件。
				firstAnomaly := len(result.Anomalies)
				checkWalletBalanceNonNegative(result, walletStoredValueCard, strconv.Itoa(card.ID), balance)
				// §11.6-01：逐卡核验「钱包流水求和 == 开卡批次账本求和」（卡账户集合相同，不额外扫描）。
				if err = scanWalletLedgerSum(ctx, db, result,
					walletStoredValueCard, strconv.Itoa(card.ID)); err != nil {
					return 0, 0, err
				}
				now := time.Now()
				if err = recordStoredValueCardReconcileExceptions(ctx, db, card, balance,
					result.Anomalies[firstAnomaly:], now); err != nil {
					return 0, 0, err
				}
				// §4.6-06：逐卡复检其名下储值卡订单的关联完整性（闸口之外的第二道检测）。
				if err = recordStoredValueOrderReconcileExceptions(ctx, db, card.ID, now); err != nil {
					return 0, 0, err
				}
			}
			if len(cards) == 0 {
				return 0, 0, nil
			}
			return cards[len(cards)-1].ID, len(cards), nil
		})
	if err != nil {
		return err
	}
	if truncated {
		result.ScanTruncated = true
	}
	return nil
}

// readLedgerCursor 读取某扫描维度的持久游标（上一轮已扫到的最大 ID）。缺失或非法时从 0 开始。
func readLedgerCursor(ctx context.Context, db *ent.Client, key string) (int, error) {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if ent.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cursor, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || cursor < 0 {
		return 0, nil
	}
	return cursor, nil
}

// writeLedgerCursor 持久化某扫描维度的游标。cursor 为 0 表示已扫完整圈、下一轮从头开始。
func writeLedgerCursor(ctx context.Context, db *ent.Client, key string, cursor int) error {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if ent.IsNotFound(err) {
		_, err = db.SystemConfig.Create().SetKey(key).SetName("账本核验游标").
			SetValue(strconv.Itoa(cursor)).SetType("number").SetGroup("esports").SetStatus(1).Save(ctx)
		return err
	}
	if err != nil {
		return err
	}
	_, err = db.SystemConfig.UpdateOneID(item.ID).SetValue(strconv.Itoa(cursor)).Save(ctx)
	return err
}

// ledgerCursorScan 以持久游标推进方式扫描某维度：从游标位置起按 ID 升序逐批读取并交给
// process 处理，单轮最多 scanCap 条。process 返回本批最大 ID 与条数；count 为 0 或小于
// limit 表示已扫到表尾。整圈扫完时游标重置为 0（返回 truncated=false），否则写回本轮
// 扫到的最大 ID 并返回 true（被单轮上限截断，下轮继续）。
func ledgerCursorScan(ctx context.Context, db *ent.Client, cursorKey string, scanCap int,
	process func(lastID, limit int) (maxID, count int, err error)) (bool, error) {
	cursor, err := readLedgerCursor(ctx, db, cursorKey)
	if err != nil {
		return false, err
	}
	lastID := cursor
	scanned := 0
	for scanned < scanCap {
		limit := ledgerReconcileBatchSize
		if remain := scanCap - scanned; remain < limit {
			limit = remain
		}
		maxID, count, err := process(lastID, limit)
		if err != nil {
			return false, err
		}
		if count == 0 {
			return false, writeLedgerCursor(ctx, db, cursorKey, 0)
		}
		lastID = maxID
		scanned += count
		if count < limit {
			return false, writeLedgerCursor(ctx, db, cursorKey, 0)
		}
	}
	return true, writeLedgerCursor(ctx, db, cursorKey, lastID)
}

func checkWalletBalanceNonNegative(result *ledgerReconcileResult, accountType, accountID string, balance walletBalance) {
	if balance.PrincipalCents < 0 {
		result.add(ledgerAnomalyNegativeBalance, accountType, accountID,
			fmt.Sprintf("本金余额为负：%d 分（流水求和）", balance.PrincipalCents))
	}
	if balance.BonusCents < 0 {
		result.add(ledgerAnomalyNegativeBalance, accountType, accountID,
			fmt.Sprintf("赠送余额为负：%d 分（流水求和）", balance.BonusCents))
	}
}

// ledgerAccountKey 账户键，用于把分摊按账户 + 批次 / 账户 + 消费去重聚合。
func ledgerAccountKey(accountType, accountID string) string {
	return accountType + "\x00" + accountID
}

// ledgerQuad 批次四元组标识（账户 + 来源），与 ledgerBatchAllocatedTx 的查询口径一致。
type ledgerQuad [4]string

// reconcileLedgerAllocations 逐批次核验分摊：先按「账户 + 批次来源」聚合分摊额，
// 再对每个批次做一次账本查询，避免逐条重复聚合。批次条目不存在（batchSum 为 0）或
// 分摊额超过批次金额都会报出。分摊维度用持久游标推进，连续多轮运行扫完整圈。
func reconcileLedgerAllocations(ctx context.Context, db *ent.Client, scanCap int, result *ledgerReconcileResult) error {
	allocatedByBatch := map[string]int64{}
	batchQuads := map[string]ledgerQuad{}

	truncated, err := ledgerCursorScan(ctx, db, ledgerCursorKeyAllocation, scanCap,
		func(lastID, limit int) (int, int, error) {
			allocs, err := db.EsportsLedgerAllocation.Query().
				Where(esportsledgerallocation.IDGT(lastID)).
				Order(ent.Asc(esportsledgerallocation.FieldID)).
				Limit(limit).All(ctx)
			if err != nil {
				return 0, 0, err
			}
			for _, item := range allocs {
				result.ScannedAllocations++
				batchKey := ledgerAccountKey(item.AccountType, item.AccountID) +
					"\x00" + item.BatchSourceType + "\x00" + item.BatchSourceID
				allocatedByBatch[batchKey] += item.AmountCents
				if _, ok := batchQuads[batchKey]; !ok {
					batchQuads[batchKey] = ledgerQuad{item.AccountType, item.AccountID, item.BatchSourceType, item.BatchSourceID}
				}
			}
			if len(allocs) == 0 {
				return 0, 0, nil
			}
			return allocs[len(allocs)-1].ID, len(allocs), nil
		})
	if err != nil {
		return err
	}
	if truncated {
		result.ScanTruncated = true
	}

	// batchSum 为 0 说明引用的批次条目不存在或不是正向条目；分摊额超过批次金额即超扣。
	batchKeys := make([]string, 0, len(allocatedByBatch))
	for key := range allocatedByBatch {
		batchKeys = append(batchKeys, key)
	}
	sort.Strings(batchKeys)
	for _, key := range batchKeys {
		quad := batchQuads[key]
		batchSum, err := ledgerBatchDeltaSum(ctx, db, quad)
		if err != nil {
			return err
		}
		result.ScannedBatches++
		if batchSum == 0 {
			result.add(ledgerAnomalyBatchMissing, quad[0], quad[1],
				fmt.Sprintf("分摊引用的批次账本不存在或非正向：%s / %s", quad[2], quad[3]))
			continue
		}
		if allocated := allocatedByBatch[key]; allocated > batchSum {
			result.add(ledgerAnomalyBatchOverAllocated, quad[0], quad[1],
				fmt.Sprintf("批次 %s / %s 金额 %d 分，已分摊 %d 分，超额 %d 分",
					quad[2], quad[3], batchSum, allocated, allocated-batchSum))
		}
	}
	return nil
}

// ledgerBatchDeltaSum 汇总某批次条目的正向金额（只算 delta_cents > 0 的批次条目）。
func ledgerBatchDeltaSum(ctx context.Context, db *ent.Client, quad ledgerQuad) (int64, error) {
	rows, err := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.AccountTypeEQ(quad[0]),
		esportsledgerentry.AccountIDEQ(quad[1]),
		esportsledgerentry.SourceTypeEQ(quad[2]),
		esportsledgerentry.SourceIDEQ(quad[3]),
		esportsledgerentry.DeltaCentsGT(0),
	).Select(esportsledgerentry.FieldDeltaCents).All(ctx)
	if err != nil {
		return 0, err
	}
	var sum int64
	for _, row := range rows {
		sum += row.DeltaCents
	}
	return sum, nil
}

// publishLedgerReconcileAnomalies 把核验异常写成一条外盒事件（供告警与人工复核消费）。
// 无异常时不落事件；同一秒重复触发只保留一条。
func publishLedgerReconcileAnomalies(ctx context.Context, db *ent.Client, result ledgerReconcileResult, now time.Time) error {
	if len(result.Anomalies) == 0 {
		return nil
	}
	payload := map[string]interface{}{
		"scanned_accounts":    result.ScannedAccounts,
		"scanned_allocations": result.ScannedAllocations,
		"scanned_batches":     result.ScannedBatches,
		"scanned_wallet_sums": result.ScannedWalletSums,
		"anomaly_count":       len(result.Anomalies),
		"scan_truncated":      result.ScanTruncated,
		"anomaly_overflow":    result.AnomalyOverflow,
		"anomalies":           result.Anomalies,
		"occurred_at":         now.UTC().Format(time.RFC3339Nano),
	}
	_, err := db.EsportsOutbox.Create().
		SetEventKey("ledger.reconcile_anomaly:" + now.UTC().Format("20060102150405")).
		SetEventType("ledger.reconcile_anomaly").SetAggregateRef("ledger").
		SetPayload(payload).Save(ctx)
	if ent.IsConstraintError(err) {
		return nil
	}
	return err
}

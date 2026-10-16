package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsrechargerefundexecution"
	"go-admin/internal/ent/esportswallettx"
)

// 退款 / 退卡按充值批次还原的外部退款执行器（§4.5 执行落账编排）。
//
// 试算（rechargeRefundQuoteFor）只读，给出逐批可退本金 / 作废赠送与异常；本文件做「执行落账」：
// 按每个可退批次生成一条外部退款执行明细（EsportsRechargeRefundExecution），引用批次来源、
// 退款金额与状态 pending/executed，幂等键按「售后单来源 + 批次来源」拆分，重复执行不重复落账。
//
// 与既有模块的边界（保持兼容，不改 stored_value.go）：
//   - 退卡仍走 stored_value.go 的 refund-request / refund-decision，本执行器只针对「充值批次还原」
//     这条链路（售后单 source_type=recharge / stored_value_card），两者互不侵入。
//   - 本执行器不接银行通道，真实外部打款仍由财务登记后把明细置为 executed 并回填外部凭证号。
//
// 异常批次不落账：分摊额超批次额（OverAllocated）或本金 / 赠送拆分失败（!SplitKnown）的批次，
// 其退款金额不可信，跳过生成明细，交由人工处理；试算里的 unbatched_balance 异常不阻断可追溯批次。

// rechargeRefundExecutionNo 外部退款执行明细编号，前缀 RRE。
func rechargeRefundExecutionNo() string { return "RRE" + uuid.NewString() }

// rechargeRefundExecutionIdemKey 幂等键：售后单来源 + 批次来源。
// 格式 refund:<source_type>:<source_id>:<batch_source_type>:<batch_source_id>。
func rechargeRefundExecutionIdemKey(sourceType string, sourceID int, batchSourceType, batchSourceID string) string {
	return fmt.Sprintf("refund:%s:%d:%s:%s", sourceType, sourceID, batchSourceType, batchSourceID)
}

// refundExecutionAccount 由售后单来源推导退款账户的账本身份。
// 储值卡来源退预付款批次；充值来源按充值流水落到客户余额或储值卡预付款账户。
func refundExecutionAccount(ctx context.Context, db *ent.Client, item *ent.EsportsAfterSale) (accountType, accountID string, err error) {
	switch item.SourceType {
	case esportsaftersale.SourceTypeStoredValueCard:
		return storedValuePrepayAccount, strconv.Itoa(item.SourceID), nil
	case esportsaftersale.SourceTypeRecharge:
		row, err := db.EsportsWalletTx.Get(ctx, item.SourceID)
		if err != nil {
			return "", "", err
		}
		switch row.AccountType {
		case esportswallettx.AccountTypeCustomerBalance:
			return walletCustomerBalance, strconv.Itoa(row.OwnerID), nil
		case esportswallettx.AccountTypeStoredValueCard:
			return storedValuePrepayAccount, strconv.Itoa(row.OwnerID), nil
		}
	}
	return "", "", fmt.Errorf("%w：仅充值 / 储值卡来源支持按批次退款执行", errInvalidInput)
}

// executeRechargeRefundByBatch 按充值批次生成外部退款执行明细（§4.5）：
// 由售后单推导账户 → 逐批还原试算 → 按可退批次落账执行明细。幂等可重复执行，返回该售后单的全部明细。
func executeRechargeRefundByBatch(ctx context.Context, db *ent.Client, afterSaleID, adminID int, now time.Time) ([]*ent.EsportsRechargeRefundExecution, error) {
	if afterSaleID < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsAfterSale.Get(ctx, afterSaleID)
	if err != nil {
		return nil, err
	}
	accountType, accountID, err := refundExecutionAccount(ctx, db, item)
	if err != nil {
		return nil, err
	}
	quote, err := rechargeRefundQuoteFor(ctx, db, accountType, accountID, now)
	if err != nil {
		return nil, err
	}
	return executeRechargeRefundQuote(ctx, db, item, quote)
}

// executeRechargeRefundQuote 试算已备好时直接按批次落账执行明细（核心编排）：
// 只对「可退本金为正、分摊未超批次额、本金 / 赠送拆分可信」的批次生成明细，异常批次跳过；
// 幂等键唯一索引兜底，重复执行复用既有明细不重复落账。
func executeRechargeRefundQuote(ctx context.Context, db *ent.Client, item *ent.EsportsAfterSale,
	quote *rechargeRefundQuote) ([]*ent.EsportsRechargeRefundExecution, error) {
	if item == nil || quote == nil {
		return nil, errInvalidInput
	}
	plans := make([]rechargeRefundBatch, 0, len(quote.Batches))
	for _, batch := range quote.Batches {
		if batch.RefundableCents <= 0 || batch.OverAllocated || !batch.SplitKnown {
			continue
		}
		plans = append(plans, batch)
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	existing, err := tx.EsportsRechargeRefundExecution.Query().Where(
		esportsrechargerefundexecution.AfterSaleIDEQ(item.ID),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(existing))
	for _, row := range existing {
		seen[row.IdemKey] = struct{}{}
	}
	for _, batch := range plans {
		idem := rechargeRefundExecutionIdemKey(item.SourceType.String(), item.SourceID,
			batch.BatchSourceType, batch.BatchSourceID)
		if _, ok := seen[idem]; ok {
			continue
		}
		if _, err = tx.EsportsRechargeRefundExecution.Create().
			SetExecutionNo(rechargeRefundExecutionNo()).
			SetIdemKey(idem).SetAfterSaleID(item.ID).
			SetAccountType(quote.AccountType).SetAccountID(quote.AccountID).
			SetBatchSourceType(batch.BatchSourceType).SetBatchSourceID(batch.BatchSourceID).
			SetAmountCents(batch.RefundableCents).
			SetStatus(esportsrechargerefundexecution.StatusPending).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				// 并发重放：唯一索引兜底，跳过本次插入，事务提交后返回既有明细。
				continue
			}
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsRechargeRefundExecution.Query().Where(
		esportsrechargerefundexecution.AfterSaleIDEQ(item.ID),
	).Order(ent.Asc(esportsrechargerefundexecution.FieldID)).All(ctx)
}

// markRechargeRefundExecutionExecuted 财务登记外部打款后把执行明细置为 executed（§4.5）：
// 回填外部凭证号与执行人，不接银行通道；状态只能 pending → executed。
func markRechargeRefundExecutionExecuted(ctx context.Context, db *ent.Client, id, adminID int,
	financeRef string, now time.Time) (*ent.EsportsRechargeRefundExecution, error) {
	financeRef = strings.TrimSpace(financeRef)
	if id < 1 || adminID < 1 || financeRef == "" || len([]rune(financeRef)) > 128 {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsRechargeRefundExecution.Update().Where(
		esportsrechargerefundexecution.IDEQ(id),
		esportsrechargerefundexecution.StatusEQ(esportsrechargerefundexecution.StatusPending),
	).SetStatus(esportsrechargerefundexecution.StatusExecuted).
		SetExecutedByAdminID(adminID).SetExecutedAt(now.UTC()).SetFinanceRef(financeRef).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsRechargeRefundExecution.Get(ctx, id)
}

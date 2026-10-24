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

// 退款 / 退卡按充值批次还原的外部退款执行器（执行落账编排）。
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

// rechargeRefundExecutionIdemKey 幂等键：售后单 + 来源 + 批次。必须含售后单号——
// 同一充值来源可有多张售后单分批退款，只按「来源 + 批次」会撞唯一索引被静默跳过，
// 而内部余额已按 RefundCents 冲减，造成「钱退了、外部打款明细缺失」。
func rechargeRefundExecutionIdemKey(afterSaleID int, sourceType string, sourceID int, batchSourceType, batchSourceID string) string {
	return fmt.Sprintf("refund:%d:%s:%d:%s:%s", afterSaleID, sourceType, sourceID, batchSourceType, batchSourceID)
}

// refundExecutionAccount 由售后单来源推导退款账户的账本身份：充值来源按充值流水落到客户余额，
// 历史储值卡账户的充值流水仍落到已废弃的预付款批次账户（卡表已 DROP，仅存量数据可达）。
func refundExecutionAccount(ctx context.Context, db *ent.Client, item *ent.EsportsAfterSale) (accountType, accountID string, err error) {
	switch item.SourceType {
	case esportsaftersale.SourceTypeRecharge:
		row, err := db.EsportsWalletTx.Get(ctx, item.SourceID)
		if err != nil {
			return "", "", err
		}
		switch row.AccountType {
		case esportswallettx.AccountTypeCustomerBalance:
			return walletCustomerBalance, strconv.Itoa(row.OwnerID), nil
		case esportswallettx.AccountTypeStoredValueCard:
			// 已废弃账户类型（历史储值卡充值流水）：批次账本账户按既有口径映射，仅用于存量还原。
			return storedValuePrepayAccount, strconv.Itoa(row.OwnerID), nil
		}
	}
	return "", "", fmt.Errorf("%w：仅充值来源支持按批次退款执行", errInvalidInput)
}

// executeRechargeRefundByBatch 按充值批次生成外部退款执行明细：
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
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = executeRechargeRefundQuoteTx(ctx, tx, item, quote); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsRechargeRefundExecution.Query().Where(
		esportsrechargerefundexecution.AfterSaleIDEQ(item.ID),
	).Order(ent.Asc(esportsrechargerefundexecution.FieldID)).All(ctx)
}

// executeRechargeRefundQuoteTx 在调用方事务内按试算生成外部退款执行明细（幂等）：
// 供售后执行链（充值来源）在同一事务内与内部退款一并落账。
//
// 累计口径（修复）：试算的 RefundableCents 只反映「按账户余额还原的批次剩余被退额」，
// 执行明细的 pending 状态并不扣减余额（真实打款后才扣），因此同一批次可能被多张售后单
// 重复按同一可退额生成明细。这里按 (account_type, account_id, batch_source_type, batch_source_id)
// 聚合既有执行明细，逐批扣减已占用金额：
//
//	本次批次可退额 = min(试算 RefundableCents, 批次 PrincipalCents − 该批次既有执行明细合计)
//
// 当前售后单自己的明细不计入「既有占用」（它们会在下面按幂等键跳过），并计入本次额度上限，
// 保证 item.RefundCents > 0（售后方案指定退款额）时本次生成的明细合计不超过该退款额。
// 可退额被占满的批次不再生成明细（超出部分不落账，交由人工核对）。
// 这样所有售后单的执行明细合计恒不超过批次原始本金。
func executeRechargeRefundQuoteTx(ctx context.Context, tx *ent.Tx, item *ent.EsportsAfterSale,
	quote *rechargeRefundQuote) error {
	if item == nil || quote == nil {
		return errInvalidInput
	}
	plans := make([]rechargeRefundBatch, 0, len(quote.Batches))
	for _, batch := range quote.Batches {
		if batch.RefundableCents <= 0 || batch.OverAllocated || !batch.SplitKnown {
			continue
		}
		plans = append(plans, batch)
	}
	existing, err := tx.EsportsRechargeRefundExecution.Query().Where(
		esportsrechargerefundexecution.AccountTypeEQ(quote.AccountType),
		esportsrechargerefundexecution.AccountIDEQ(quote.AccountID),
	).All(ctx)
	if err != nil {
		return err
	}
	// otherConsumedByBatch 其他售后单已占用金额；currentConsumedByBatch 当前售后单已落账金额。
	otherConsumedByBatch := make(map[string]int64, len(existing))
	currentConsumedByBatch := make(map[string]int64, len(existing))
	currentIdem := make(map[string]struct{}, len(existing))
	var currentAllocated int64
	for _, row := range existing {
		key := row.BatchSourceType + "\x00" + row.BatchSourceID
		if row.AfterSaleID == item.ID {
			currentConsumedByBatch[key] += row.AmountCents
			currentIdem[row.IdemKey] = struct{}{}
			currentAllocated += row.AmountCents
			continue
		}
		otherConsumedByBatch[key] += row.AmountCents
	}
	// 本次可生成明细的上限：售后方案指定了退款额时按该额度封顶；未指定（0）时不封顶，
	// 保持既有「按批次全量落账」路径不变。
	capRemaining := int64(1<<62 - 1)
	if item.RefundCents > 0 {
		capRemaining = item.RefundCents - currentAllocated
		if capRemaining < 0 {
			capRemaining = 0
		}
	}
	for _, batch := range plans {
		idem := rechargeRefundExecutionIdemKey(item.ID, item.SourceType.String(), item.SourceID,
			batch.BatchSourceType, batch.BatchSourceID)
		if _, ok := currentIdem[idem]; ok {
			continue
		}
		key := batch.BatchSourceType + "\x00" + batch.BatchSourceID
		// 批次本金扣除其他售后单与当前售后单已落账金额后的余额。
		remaining := batch.PrincipalCents - otherConsumedByBatch[key] - currentConsumedByBatch[key]
		if remaining <= 0 {
			continue
		}
		amount := min(batch.RefundableCents, remaining)
		amount = min(amount, capRemaining)
		if amount <= 0 {
			break
		}
		if _, err = tx.EsportsRechargeRefundExecution.Create().
			SetExecutionNo(rechargeRefundExecutionNo()).
			SetIdemKey(idem).SetAfterSaleID(item.ID).
			SetAccountType(quote.AccountType).SetAccountID(quote.AccountID).
			SetBatchSourceType(batch.BatchSourceType).SetBatchSourceID(batch.BatchSourceID).
			SetAmountCents(amount).
			SetStatus(esportsrechargerefundexecution.StatusPending).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				// 并发重放：唯一索引兜底，跳过本次插入。
				continue
			}
			return err
		}
		capRemaining -= amount
		currentConsumedByBatch[key] += amount
	}
	return nil
}

// markRechargeRefundExecutionExecuted 财务登记外部打款后把执行明细置为 executed：
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

// rechargeRefundExecutionView 是退款执行批次的列表项（审核报告 缺口）：
// 批次号 / 状态 / 金额 / 时间 / 关联售后单号 / 执行人，财务列表页据此直接渲染，无需再逐条查详情。
type rechargeRefundExecutionView struct {
	ID          int    `json:"id"`
	ExecutionNo string `json:"execution_no"`
	Status      string `json:"status"`
	// AmountCents 该批次本次外部应打款金额（分）。
	AmountCents int64 `json:"amount_cents"`
	// AfterSaleID / AfterSaleNo 关联售后单（单号缺失时为空字符串，不阻断列表）。
	AfterSaleID       int    `json:"after_sale_id"`
	AfterSaleNo       string `json:"after_sale_no"`
	AccountType       string `json:"account_type"`
	AccountID         string `json:"account_id"`
	BatchSourceType   string `json:"batch_source_type"`
	BatchSourceID     string `json:"batch_source_id"`
	ExecutedByAdminID *int   `json:"executed_by_admin_id"`
	// ExecutedAt 财务登记打款的时间（pending 时为空）。
	ExecutedAt *time.Time `json:"executed_at"`
	FinanceRef string     `json:"finance_ref"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// listRechargeRefundExecutions 列出按充值批次生成的外部退款执行明细（只读）：
// status 为空表示不过滤，否则只接受 pending / executed（非法值中文报错，不静默放行）；
// limit 夹在 1~200，缺省 100。关联售后单号按批次批量补齐，避免逐行查库。
func listRechargeRefundExecutions(ctx context.Context, db *ent.Client, status string,
	limit int) ([]rechargeRefundExecutionView, error) {
	status = strings.TrimSpace(status)
	if status != "" && status != string(esportsrechargerefundexecution.StatusPending) &&
		status != string(esportsrechargerefundexecution.StatusExecuted) {
		return nil, fmt.Errorf("%w：批次状态只能是 pending 或 executed", errInvalidInput)
	}
	if limit < 1 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	query := db.EsportsRechargeRefundExecution.Query()
	if status != "" {
		query = query.Where(esportsrechargerefundexecution.StatusEQ(esportsrechargerefundexecution.Status(status)))
	}
	rows, err := query.Order(ent.Desc(esportsrechargerefundexecution.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []rechargeRefundExecutionView{}, nil
	}
	afterSaleIDs := make([]int, 0, len(rows))
	seen := make(map[int]bool, len(rows))
	for _, row := range rows {
		if !seen[row.AfterSaleID] {
			seen[row.AfterSaleID] = true
			afterSaleIDs = append(afterSaleIDs, row.AfterSaleID)
		}
	}
	afterSales, err := db.EsportsAfterSale.Query().
		Where(esportsaftersale.IDIn(afterSaleIDs...)).All(ctx)
	if err != nil {
		return nil, err
	}
	noByID := make(map[int]string, len(afterSales))
	for _, item := range afterSales {
		noByID[item.ID] = item.AfterSaleNo
	}
	views := make([]rechargeRefundExecutionView, 0, len(rows))
	for _, row := range rows {
		views = append(views, rechargeRefundExecutionView{
			ID: row.ID, ExecutionNo: row.ExecutionNo, Status: string(row.Status),
			AmountCents: row.AmountCents, AfterSaleID: row.AfterSaleID,
			AfterSaleNo: noByID[row.AfterSaleID],
			AccountType: row.AccountType, AccountID: row.AccountID,
			BatchSourceType: row.BatchSourceType, BatchSourceID: row.BatchSourceID,
			ExecutedByAdminID: row.ExecutedByAdminID, ExecutedAt: row.ExecutedAt,
			FinanceRef: row.FinanceRef, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
		})
	}
	return views, nil
}

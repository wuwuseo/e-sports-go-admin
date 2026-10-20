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
	"go-admin/internal/ent/esportswallettx"
)

// 退款 / 退卡按充值批次还原试算（§4.5）。
//
// 目标：给出「以充值批次为粒度」的可审计试算——每一笔充值批次在退款 / 退卡时各自还剩多少、
// 其中可退本金与随退作废的赠送分别是多少，并把无法按批次还原的账本异常显式报出，
// 而不是静默给出一个对不上的结论。本文件只做试算，不落库；真实退款执行仍由既有模块承接
// （充值还原走售后执行链）。
//
// 口径（随结果一并返回 Basis，便于审计对照）：
//  1. 批次集合取账本 `EsportsLedgerEntry` 中该账户 `delta_cents > 0` 的正向批次，按 created_at FIFO；
//     批次的本金 / 赠送拆分由开启该批次的来源钱包流水给出（客户余额批次按 tx_no 命中充值流水；
//     批次按流水号命中开卡 / 充值流水），赠送到期时间同样取该流水。
//  2. 「已被消费分摊额」= `EsportsLedgerAllocation` 求和，原样展示（纯分摊视图），供审计对照；
//     该口径只覆盖走了 `allocateLedgerConsumption` 的消费，不含管理员调整、赠送作废、划转等冲减。
//  3. 批次剩余按「账户派生余额」口径还原：本金总消耗 = 批次本金合计 − 账户本金余额，
//     赠送总消耗 = 批次赠送合计 − 账户赠送余额；本金按批次 FIFO 扣减，赠送按「先到期先扣、
//     永久赠送最后」扣减（与 `rechargeBonusBatches` / `expireOwnerBonus` 的消耗次序一致）。
//     这样各批次剩余之和恒等于账户派生余额，可退本金 / 作废赠送合计与既有退卡试算
//     （未消费本金可退、未消费赠送不折现）是同一套结论。
//  4. 纯只读：只查询账本、分摊与钱包流水，不写任何账本 / 流水 / 卡状态，可重复执行。
//  5. 数据异常显式分类报出：分摊额超批次额、批次本金 / 赠送无法拆分、账户余额大于批次合计
//     （存在未落批次的正向资金，如未写批次的活动充值或管理员调整）。
//
// ponytail: 批次级的本金 / 赠送拆分是「解释性」口径——钱本身是可替代的，逐笔扣减只按批次 FIFO
// 记了总额（`EsportsLedgerAllocation` 不含本金 / 赠送分列），所以批内拆分按账户扣减顺序给出；
// 需要精确到「某一笔消费动了哪个批次的哪一部分」时得把分列写进分摊明细（需改 schema）。
// 另：逐账户全量加载流水与分摊为 O(n)，账户流水到十万级时改为 SQL 聚合。

// rechargeRefundQuoteVersion 试算口径版本，随口径变更递增，便于审计对照历史结论。
const rechargeRefundQuoteVersion = "recharge-batch-refund-quote/v1"

// 批次还原异常分类。与 ledger_reconcile.go 共用 `ledgerAnomaly` 输出结构与分类风格，
// 但 key 独立（前缀 refund_quote_），勿与账本全量核验的 key 混用。
const (
	// 分摊额大于批次额：正常业务路径有 available 判定写不出来，出现即历史或并发异常数据。
	refundQuoteAnomalyOverAllocated = "refund_quote_batch_over_allocated"
	// 批次金额无法拆成本金 / 赠送：来源钱包流水缺失或金额不一致，拆分结果不可信。
	refundQuoteAnomalySplitUnknown = "refund_quote_batch_split_unknown"
	// 账户派生余额大于批次合计：该部分资金没有批次可追溯，无法按批次还原。
	refundQuoteAnomalyUnbatchedFunds = "refund_quote_unbatched_balance"
)

// rechargeRefundBatch 单个充值批次的还原明细。
type rechargeRefundBatch struct {
	BatchSourceType string    `json:"batch_source_type"`
	BatchSourceID   string    `json:"batch_source_id"`
	FundBatch       string    `json:"fund_batch"`
	EntryRole       string    `json:"entry_role"`
	CreatedAt       time.Time `json:"created_at"`
	// OriginalCents 批次原始金额（账本正向条目 delta_cents）。
	OriginalCents int64 `json:"original_cents"`
	// PrincipalCents / BonusCents 批次里的本金与赠送占比（由来源钱包流水拆分得出）。
	PrincipalCents int64 `json:"principal_cents"`
	BonusCents     int64 `json:"bonus_cents"`
	// AllocatedCents 已被消费分摊额（EsportsLedgerAllocation 求和），纯分摊视图。
	AllocatedCents int64 `json:"allocated_cents"`
	// RemainingByAllocationCents 纯分摊视图的批次剩余 = 原始 − 已分摊（不足时归零并标记超额）。
	// 只覆盖走分摊记账的消费，权威剩余见 PrincipalRemainingCents / BonusRemainingCents。
	RemainingByAllocationCents int64 `json:"remaining_by_allocation_cents"`
	// PrincipalRemainingCents / BonusRemainingCents 按账户派生余额口径还原的批次剩余，
	// 各批次求和等于账户派生余额（本金 / 赠送分列）。
	PrincipalRemainingCents int64 `json:"principal_remaining_cents"`
	BonusRemainingCents     int64 `json:"bonus_remaining_cents"`
	// RefundableCents 该批次可退本金 = 该批次剩余本金。
	RefundableCents int64 `json:"refundable_cents"`
	// BonusForfeitedCents 该批次随退作废的赠送 = 该批次剩余赠送（赠送一律不折现）。
	BonusForfeitedCents int64 `json:"bonus_forfeited_cents"`
	// BonusExpiresAt / BonusExpired 该批次赠送权益的到期时间与当前是否已过期。
	BonusExpiresAt *time.Time `json:"bonus_expires_at,omitempty"`
	BonusExpired   bool       `json:"bonus_expired"`
	// OverAllocated 分摊额超过批次原始金额。
	OverAllocated bool `json:"over_allocated"`
	// SplitKnown 是否成功把批次金额拆成本金 / 赠送；false 时按全额本金计并落异常。
	SplitKnown bool `json:"split_known"`
}

// rechargeRefundQuoteBasis 试算口径说明（随结果返回，供审计对照与前端展示）。
type rechargeRefundQuoteBasis struct {
	Version string `json:"version"`
	// BatchScope 批次集合口径。
	BatchScope string `json:"batch_scope"`
	// AllocationRule 「已分摊额」口径。
	AllocationRule string `json:"allocation_rule"`
	// RemainingRule 批次剩余口径。
	RemainingRule string `json:"remaining_rule"`
	// RefundRule 可退 / 作废口径。
	RefundRule string `json:"refund_rule"`
	// 账户派生余额与批次合计，用于核对「明细之和 == 余额」。
	WalletPrincipalCents  int64 `json:"wallet_principal_cents"`
	WalletBonusCents      int64 `json:"wallet_bonus_cents"`
	BatchPrincipalInCents int64 `json:"batch_principal_in_cents"`
	BatchBonusInCents     int64 `json:"batch_bonus_in_cents"`
}

// rechargeRefundQuote 按充值批次还原的退款 / 退卡试算结果。
type rechargeRefundQuote struct {
	AccountType string `json:"account_type"`
	AccountID   string `json:"account_id"`
	// QuotedAt 试算生成的时刻（UTC），保证结论可审计到具体时点。
	QuotedAt time.Time                `json:"quoted_at"`
	Basis    rechargeRefundQuoteBasis `json:"basis"`
	// Batches 逐批明细，按批次 created_at FIFO 顺序。
	Batches []rechargeRefundBatch `json:"batches"`
	// RefundablePrincipalCents 可退本金合计（= 账户派生本金余额，与既有退卡试算同口径）。
	RefundablePrincipalCents int64 `json:"refundable_principal_cents"`
	// ForfeitedBonusCents 随退作废的赠送合计（= 账户派生赠送余额，不折现）。
	ForfeitedBonusCents int64 `json:"forfeited_bonus_cents"`
	// 批次维度还原合计，正常应与上面两项一致；不一致必伴随异常说明。
	BatchPrincipalRemainingCents int64 `json:"batch_principal_remaining_cents"`
	BatchBonusRemainingCents     int64 `json:"batch_bonus_remaining_cents"`
	// Anomalies 批次数据异常清单（分摊超批次额 / 拆分失败 / 余额无批次可追溯）。
	Anomalies []ledgerAnomaly `json:"anomalies"`
}

// refundQuoteAccount 试算账户的账本身份与钱包派生的余额账户。
type refundQuoteAccount struct {
	// ledgerAccountType / accountID 批次账本的账户键。
	ledgerAccountType string
	accountID         string
	// walletAccountType / walletOwnerID 余额派生的钱包账户键。
	walletAccountType string
	walletOwnerID     int
}

// rechargeRefundQuoteFor 按充值批次还原试算指定账户的退款金额。纯只读，可重复执行。
// accountType 仅支持 customer_balance（客户余额），accountID 为客户 ID；
// 储值卡预付款账户（stored_value_prepay）随储值卡功能下线（2026-10-20）不再支持。
func rechargeRefundQuoteFor(ctx context.Context, db *ent.Client, accountType, accountID string, now time.Time) (*rechargeRefundQuote, error) {
	account, err := resolveRefundQuoteAccount(ctx, db, accountType, accountID)
	if err != nil {
		return nil, err
	}
	utc := now.UTC()
	batches, err := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.AccountTypeEQ(account.ledgerAccountType),
		esportsledgerentry.AccountIDEQ(account.accountID),
		esportsledgerentry.DeltaCentsGT(0),
	).Order(ent.Asc(esportsledgerentry.FieldCreatedAt), ent.Asc(esportsledgerentry.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	allocatedByBatch, err := refundQuoteAllocatedByBatch(ctx, db, account.ledgerAccountType, account.accountID)
	if err != nil {
		return nil, err
	}
	balance, err := walletBalanceOf(ctx, db, account.walletAccountType, account.walletOwnerID)
	if err != nil {
		return nil, err
	}
	byTxNo, err := refundQuoteWalletIndex(ctx, db, account.walletAccountType, account.walletOwnerID)
	if err != nil {
		return nil, err
	}

	quote := &rechargeRefundQuote{
		AccountType: account.ledgerAccountType,
		AccountID:   account.accountID,
		QuotedAt:    utc,
		Anomalies:   []ledgerAnomaly{},
		Batches:     make([]rechargeRefundBatch, 0, len(batches)),
	}
	// 逐批还原：先补齐批次原始金额与本金 / 赠送拆分，再按余额口径回扣消耗。
	principalRemain := make([]int64, 0, len(batches))
	bonusRemain := make([]int64, 0, len(batches))
	expiries := make([]*time.Time, 0, len(batches))
	var principalIn, bonusIn int64
	for _, batch := range batches {
		principal, bonus, expiresAt, splitKnown := refundQuoteBatchSplit(batch, byTxNo)
		allocated := allocatedByBatch[batch.SourceType+"\x00"+batch.SourceID]
		detail := rechargeRefundBatch{
			BatchSourceType: batch.SourceType, BatchSourceID: batch.SourceID,
			FundBatch: batch.FundBatch, EntryRole: batch.EntryRole, CreatedAt: batch.CreatedAt.UTC(),
			OriginalCents: batch.DeltaCents, PrincipalCents: principal, BonusCents: bonus,
			AllocatedCents: allocated, BonusExpiresAt: expiresAt, SplitKnown: splitKnown,
		}
		if allocated > batch.DeltaCents {
			detail.OverAllocated = true
			detail.RemainingByAllocationCents = 0
			quote.Anomalies = append(quote.Anomalies, ledgerAnomaly{
				Kind: refundQuoteAnomalyOverAllocated, AccountType: account.ledgerAccountType, AccountID: account.accountID,
				Detail: fmt.Sprintf("批次 %s / %s 金额 %d 分，已分摊 %d 分，超额 %d 分；该批次按余额口径的剩余仍以账户余额为准",
					batch.SourceType, batch.SourceID, batch.DeltaCents, allocated, allocated-batch.DeltaCents),
			})
		} else {
			detail.RemainingByAllocationCents = batch.DeltaCents - allocated
		}
		if !splitKnown {
			quote.Anomalies = append(quote.Anomalies, ledgerAnomaly{
				Kind: refundQuoteAnomalySplitUnknown, AccountType: account.ledgerAccountType, AccountID: account.accountID,
				Detail: fmt.Sprintf("批次 %s / %s 金额 %d 分无法从来源钱包流水拆出本金 / 赠送，已保守按全额本金计",
					batch.SourceType, batch.SourceID, batch.DeltaCents),
			})
		}
		principalIn += principal
		bonusIn += bonus
		principalRemain = append(principalRemain, principal)
		bonusRemain = append(bonusRemain, bonus)
		expiries = append(expiries, expiresAt)
		quote.Batches = append(quote.Batches, detail)
	}

	// 余额口径还原：本金 FIFO 扣减，赠送先到期先扣（永久赠送最后）。
	principalUsed := principalIn - balance.PrincipalCents
	if principalUsed < 0 {
		principalUsed = 0
	}
	bonusUsed := bonusIn - balance.BonusCents
	if bonusUsed < 0 {
		bonusUsed = 0
	}
	refundQuoteConsume(refundQuoteFIFO(len(batches)), principalRemain, principalUsed)
	refundQuoteConsume(refundQuoteBonusOrder(expiries), bonusRemain, bonusUsed)
	for index := range quote.Batches {
		detail := &quote.Batches[index]
		detail.PrincipalRemainingCents = principalRemain[index]
		detail.BonusRemainingCents = bonusRemain[index]
		detail.RefundableCents = principalRemain[index]
		detail.BonusForfeitedCents = bonusRemain[index]
		detail.BonusExpired = expiries[index] != nil && !expiries[index].After(utc)
		quote.BatchPrincipalRemainingCents += principalRemain[index]
		quote.BatchBonusRemainingCents += bonusRemain[index]
	}

	// 顶层合计：可退本金取派生本金余额，未消费赠送随退作废（不折现）。
	refundRule := "可退本金 = 账户本金余额；未消费赠送不折现、随退作废"
	quote.RefundablePrincipalCents = max(balance.PrincipalCents, 0)
	quote.ForfeitedBonusCents = max(balance.BonusCents, 0)

	// 账户余额大于批次合计：多出的资金没有批次可追溯，逐批明细无法覆盖，显式报出而不是静默截断。
	if balance.PrincipalCents > principalIn {
		quote.Anomalies = append(quote.Anomalies, ledgerAnomaly{
			Kind: refundQuoteAnomalyUnbatchedFunds, AccountType: account.ledgerAccountType, AccountID: account.accountID,
			Detail: fmt.Sprintf("账户本金余额 %d 分大于批次本金合计 %d 分，差额 %d 分未落批次（如未写批次的活动充值、管理员调整），该部分无法按批次还原",
				balance.PrincipalCents, principalIn, balance.PrincipalCents-principalIn),
		})
	}
	if balance.BonusCents > bonusIn {
		quote.Anomalies = append(quote.Anomalies, ledgerAnomaly{
			Kind: refundQuoteAnomalyUnbatchedFunds, AccountType: account.ledgerAccountType, AccountID: account.accountID,
			Detail: fmt.Sprintf("账户赠送余额 %d 分大于批次赠送合计 %d 分，差额 %d 分未落批次，该部分无法按批次还原",
				balance.BonusCents, bonusIn, balance.BonusCents-bonusIn),
		})
	}
	quote.Basis = rechargeRefundQuoteBasis{
		Version:               rechargeRefundQuoteVersion,
		BatchScope:            "账本正向批次（delta_cents > 0）按 created_at FIFO",
		AllocationRule:        "已分摊额取 EsportsLedgerAllocation 求和（仅覆盖走批次分摊的消费）",
		RemainingRule:         "批次剩余按账户派生余额还原：本金按批次 FIFO 扣减，赠送按先到期先扣、永久赠送最后扣减",
		RefundRule:            refundRule,
		WalletPrincipalCents:  balance.PrincipalCents,
		WalletBonusCents:      balance.BonusCents,
		BatchPrincipalInCents: principalIn,
		BatchBonusInCents:     bonusIn,
	}
	return quote, nil
}

// resolveRefundQuoteAccount 校验账户类型与账户 ID，并解析其账本账户与钱包账户。
// 账户不存在时透出 NotFound，类型不支持返回 errInvalidInput。
func resolveRefundQuoteAccount(ctx context.Context, db *ent.Client, accountType, accountID string) (*refundQuoteAccount, error) {
	id, err := strconv.Atoi(strings.TrimSpace(accountID))
	if err != nil || id < 1 {
		return nil, errInvalidInput
	}
	switch strings.TrimSpace(accountType) {
	case walletCustomerBalance:
		if _, err := db.EsportsCustomer.Get(ctx, id); err != nil {
			return nil, err
		}
		return &refundQuoteAccount{
			ledgerAccountType: walletCustomerBalance, accountID: strconv.Itoa(id),
			walletAccountType: walletCustomerBalance, walletOwnerID: id,
		}, nil
	default:
		return nil, errInvalidInput
	}
}

// refundQuoteAllocatedByBatch 按「批次来源类型 + 来源 ID」聚合该账户的消费分摊额。
func refundQuoteAllocatedByBatch(ctx context.Context, db *ent.Client, accountType, accountID string) (map[string]int64, error) {
	rows, err := db.EsportsLedgerAllocation.Query().Where(
		esportsledgerallocation.AccountTypeEQ(accountType),
		esportsledgerallocation.AccountIDEQ(accountID),
	).Select(
		esportsledgerallocation.FieldBatchSourceType,
		esportsledgerallocation.FieldBatchSourceID,
		esportsledgerallocation.FieldAmountCents,
	).All(ctx)
	if err != nil {
		return nil, err
	}
	allocated := make(map[string]int64, len(rows))
	for _, row := range rows {
		allocated[row.BatchSourceType+"\x00"+row.BatchSourceID] += row.AmountCents
	}
	return allocated, nil
}

// refundQuoteWalletIndex 建钱包流水索引：按 tx_no 命中批次（客户余额批次口径）。
func refundQuoteWalletIndex(ctx context.Context, db *ent.Client, accountType string, ownerID int) (
	map[string]*ent.EsportsWalletTx, error) {
	rows, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountType(accountType)),
		esportswallettx.OwnerIDEQ(ownerID),
	).Order(ent.Asc(esportswallettx.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	byTxNo := make(map[string]*ent.EsportsWalletTx, len(rows))
	for _, row := range rows {
		byTxNo[row.TxNo] = row
	}
	return byTxNo, nil
}

// refundQuoteBatchSplit 把批次金额拆成本金 / 赠送，并带回赠送到期时间。
// 拆分依据是开启该批次的来源钱包流水（金额须与批次一致）；拿不到可信拆分时
// 保守按全额本金计并返回 splitKnown=false，由调用方落异常，不静默按本金处理。
func refundQuoteBatchSplit(batch *ent.EsportsLedgerEntry,
	byTxNo map[string]*ent.EsportsWalletTx) (principal, bonus int64, expiresAt *time.Time, splitKnown bool) {
	row := byTxNo[batch.SourceID]
	if row != nil && row.PrincipalDeltaCents >= 0 && row.BonusDeltaCents >= 0 &&
		row.PrincipalDeltaCents+row.BonusDeltaCents == batch.DeltaCents {
		return row.PrincipalDeltaCents, row.BonusDeltaCents, row.BonusExpiresAt, true
	}
	return batch.DeltaCents, 0, nil, false
}

// refundQuoteFIFO 批次顺序（按 load 顺序即 created_at FIFO）。
func refundQuoteFIFO(count int) []int {
	order := make([]int, count)
	for index := range order {
		order[index] = index
	}
	return order
}

// refundQuoteBonusOrder 赠送消耗次序：先到期的先被消耗，永久赠送（无到期时间）最后。
func refundQuoteBonusOrder(expiries []*time.Time) []int {
	order := refundQuoteFIFO(len(expiries))
	sort.SliceStable(order, func(i, j int) bool {
		left, right := expiries[order[i]], expiries[order[j]]
		switch {
		case left == nil:
			return false
		case right == nil:
			return true
		default:
			return left.Before(*right)
		}
	})
	return order
}

// refundQuoteConsume 按给定次序从各批次可扣额中扣减 total，扣完即止（total 大于可扣额时忽略尾差）。
func refundQuoteConsume(sequence []int, remain []int64, total int64) {
	for _, index := range sequence {
		if total <= 0 {
			return
		}
		take := remain[index]
		if take > total {
			take = total
		}
		remain[index] -= take
		total -= take
	}
}

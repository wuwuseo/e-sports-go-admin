package esports

import (
	"context"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsledgerallocation"
	"go-admin/internal/ent/esportsledgerentry"
)

// ledgerBatchAllocatedTx 汇总某批次已被分摊的金额（用于计算批次剩余可扣额）。
func ledgerBatchAllocatedTx(ctx context.Context, tx *ent.Tx, accountType, accountID, batchSourceType, batchSourceID string) (int64, error) {
	var rows []struct {
		Sum int64 `json:"sum"`
	}
	if err := tx.EsportsLedgerAllocation.Query().Where(
		esportsledgerallocation.AccountTypeEQ(accountType),
		esportsledgerallocation.AccountIDEQ(accountID),
		esportsledgerallocation.BatchSourceTypeEQ(batchSourceType),
		esportsledgerallocation.BatchSourceIDEQ(batchSourceID),
	).Aggregate(ent.Sum(esportsledgerallocation.FieldAmountCents)).Scan(ctx, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Sum, nil
}

// ledgerBatchAllocatedCompositionTx 汇总某批次已被分摊的本金 / 赠送金额（§4.5-04）。
// 直接取回分摊行在 Go 里求和：分摊明细条数受批次金额与单笔消费数限制，量级很小，
// 且避免 ent 聚合结果同名字段互相覆盖。
func ledgerBatchAllocatedCompositionTx(ctx context.Context, tx *ent.Tx, accountType, accountID,
	batchSourceType, batchSourceID string) (principal, bonus int64, err error) {
	rows, err := tx.EsportsLedgerAllocation.Query().Where(
		esportsledgerallocation.AccountTypeEQ(accountType),
		esportsledgerallocation.AccountIDEQ(accountID),
		esportsledgerallocation.BatchSourceTypeEQ(batchSourceType),
		esportsledgerallocation.BatchSourceIDEQ(batchSourceID),
	).Select(esportsledgerallocation.FieldPrincipalCents, esportsledgerallocation.FieldBonusCents).All(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, row := range rows {
		principal += row.PrincipalCents
		bonus += row.BonusCents
	}
	return principal, bonus, nil
}

// allocateLedgerConsumption 把一笔消费按批次顺序分摊到该账户的正向批次账本（§4.3 / §11.3）。
// 本金 / 赠送的拆分按「批次剩余构成 + 先赠送后本金」推断（与 §4.5 默认扣减顺序同口径）。
func allocateLedgerConsumption(ctx context.Context, tx *ent.Tx, accountType, accountID,
	consumeSourceType, consumeSourceID string, amountCents int64) error {
	return allocateLedgerConsumptionSplit(ctx, tx, accountType, accountID, consumeSourceType, consumeSourceID,
		amountCents, 0, false)
}

// allocateLedgerConsumptionWithComposition 以调用方已算好的本金 / 赠送构成分摊（§4.5-04）：
// 余额与储值卡核销已按扣减顺序拆出本金 / 赠送，批次分摊必须沿用同一构成，
// 否则「礼物只扣本金」「先赠送后本金」在批次层无法自证。
func allocateLedgerConsumptionWithComposition(ctx context.Context, tx *ent.Tx, accountType, accountID,
	consumeSourceType, consumeSourceID string, principalCents, bonusCents int64) error {
	return allocateLedgerConsumptionSplit(ctx, tx, accountType, accountID, consumeSourceType, consumeSourceID,
		principalCents, bonusCents, true)
}

// allocateLedgerConsumptionSplit 分摊实现。given 为 true 时按给定构成分摊（金额为两者之和）；
// 为 false 时按批次剩余构成推断，先扣赠送后扣本金（§4.5 默认扣减顺序）。
// 批次取该账户下 delta_cents > 0 的账本条目，按 created_at FIFO 逐个扣减，记录每笔分摊明细，
// 以便审计「每笔消费动用了哪个批次、动用了多少本金 / 赠送」。
func allocateLedgerConsumptionSplit(ctx context.Context, tx *ent.Tx, accountType, accountID,
	consumeSourceType, consumeSourceID string, principalCents, bonusCents int64, given bool) error {
	amountCents := principalCents + bonusCents
	if amountCents <= 0 {
		return nil
	}
	batches, err := tx.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.AccountTypeEQ(accountType),
		esportsledgerentry.AccountIDEQ(accountID),
		esportsledgerentry.DeltaCentsGT(0),
	).Order(ent.Asc(esportsledgerentry.FieldCreatedAt), ent.Asc(esportsledgerentry.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	remainingPrincipal, remainingBonus := principalCents, bonusCents
	remaining := amountCents
	for _, batch := range batches {
		if remaining <= 0 {
			break
		}
		allocated, err := ledgerBatchAllocatedTx(ctx, tx, accountType, accountID, batch.SourceType, batch.SourceID)
		if err != nil {
			return err
		}
		available := batch.DeltaCents - allocated
		if available <= 0 {
			continue
		}
		take := available
		if take > remaining {
			take = remaining
		}
		batchPrincipal, batchBonus := batch.PrincipalDeltaCents, batch.BonusDeltaCents
		if batchPrincipal+batchBonus != batch.DeltaCents {
			// 历史批次未分列：整批按本金看待（不虚增赠送动用量）。
			batchPrincipal, batchBonus = batch.DeltaCents, 0
		}
		allocatedPrincipal, allocatedBonus, err := ledgerBatchAllocatedCompositionTx(
			ctx, tx, accountType, accountID, batch.SourceType, batch.SourceID)
		if err != nil {
			return err
		}
		availablePrincipal := max(batchPrincipal-allocatedPrincipal, 0)
		availableBonus := max(batchBonus-allocatedBonus, 0)
		takePrincipal, takeBonus := int64(0), int64(0)
		if given {
			takePrincipal = min(min(remainingPrincipal, availablePrincipal), take)
			takeBonus = min(min(remainingBonus, availableBonus), take-takePrincipal)
			// 给定构成本身可能超出批次剩余构成（历史批次未分列）：剩余部分记在本金侧。
			if takePrincipal+takeBonus < take {
				takePrincipal = take - takeBonus
			}
		} else {
			takeBonus = min(availableBonus, take)
			takePrincipal = min(availablePrincipal, take-takeBonus)
			if takePrincipal+takeBonus < take {
				takeBonus = take - takePrincipal
			}
		}
		if _, err = tx.EsportsLedgerAllocation.Create().
			SetAccountType(accountType).SetAccountID(accountID).
			SetConsumeSourceType(consumeSourceType).SetConsumeSourceID(consumeSourceID).
			SetBatchSourceType(batch.SourceType).SetBatchSourceID(batch.SourceID).
			SetAmountCents(take).
			SetPrincipalCents(takePrincipal).SetBonusCents(takeBonus).Save(ctx); err != nil {
			return err
		}
		remaining -= take
		remainingPrincipal -= takePrincipal
		remainingBonus -= takeBonus
	}
	return nil
}

// listLedgerAllocations 列出某账户下批次分摊明细（供对账 / 审计查看）。
func listLedgerAllocations(ctx context.Context, db *ent.Client, accountType, accountID string, limit int) ([]*ent.EsportsLedgerAllocation, error) {
	if accountType == "" || accountID == "" {
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 500 {
		limit = 200
	}
	return db.EsportsLedgerAllocation.Query().Where(
		esportsledgerallocation.AccountTypeEQ(accountType),
		esportsledgerallocation.AccountIDEQ(accountID),
	).Order(ent.Desc(esportsledgerallocation.FieldID)).Limit(limit).All(ctx)
}

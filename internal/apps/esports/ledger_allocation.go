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

// allocateLedgerConsumption 把一笔消费按批次顺序分摊到该账户的正向批次账本（§4.3 / §11.3）。
// 批次取该账户下 delta_cents > 0 的账本条目，按 created_at FIFO 逐个扣减，记录每笔分摊明细，
// 以便审计「每笔消费动用了哪个批次」。金额未覆盖全部批次时静默结束（余额校验已在业务侧完成）。
func allocateLedgerConsumption(ctx context.Context, tx *ent.Tx, accountType, accountID,
	consumeSourceType, consumeSourceID string, amountCents int64) error {
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
		if _, err = tx.EsportsLedgerAllocation.Create().
			SetAccountType(accountType).SetAccountID(accountID).
			SetConsumeSourceType(consumeSourceType).SetConsumeSourceID(consumeSourceID).
			SetBatchSourceType(batch.SourceType).SetBatchSourceID(batch.SourceID).
			SetAmountCents(take).Save(ctx); err != nil {
			return err
		}
		remaining -= take
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

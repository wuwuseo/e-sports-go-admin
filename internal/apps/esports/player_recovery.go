package esports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayerrecovery"
	"go-admin/internal/ent/esportswallettx"
)

// 陪玩追偿明细：客户额外赔付由陪玩承担时另建的扣回单。
// 与「退款冲回分成」相互独立：冲回是分成账上的负向明细（影响后续结算），
// 追偿则直接从陪玩猫粮（已结算可提现余额）扣回。
// 流程：售后执行时按方案追偿金额生成（pending）→ 由非申请人审批（approved）→ 扣回（recovered）或驳回（rejected）。

// recoveryNo 生成追偿单号，前缀 PR。
func recoveryNo() string { return "PR" + uuid.NewString() }

// createPlayerRecoveryIn 事务内创建追偿明细（待审批）；供售后执行路径调用。
func createPlayerRecoveryIn(ctx context.Context, tx *ent.Tx, playerID, afterSaleID, orderID int,
	amountCents int64, reason string, adminID int) (*ent.EsportsPlayerRecovery, error) {
	reason = strings.TrimSpace(reason)
	if playerID < 1 || amountCents <= 0 || reason == "" || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	return tx.EsportsPlayerRecovery.Create().SetRecoveryNo(recoveryNo()).
		SetPlayerID(playerID).SetAfterSaleID(afterSaleID).SetOrderID(orderID).
		SetAmountCents(amountCents).SetReason(reason).
		SetRequestedByAdminID(adminID).Save(ctx)
}

// createRecoveryFromAfterSale 售后执行时按方案生成追偿明细：仅在方案填了追偿金额时创建。
func createRecoveryFromAfterSale(ctx context.Context, tx *ent.Tx, item *ent.EsportsAfterSale,
	adminID int) error {
	if item.RecoveryCents <= 0 || item.RecoveryPlayerID == nil {
		return nil
	}
	_, err := createPlayerRecoveryIn(ctx, tx, *item.RecoveryPlayerID, item.ID, item.SourceID,
		item.RecoveryCents, "售后赔付由陪玩承担（"+item.AfterSaleNo+"）", adminID)
	return err
}

// listPlayerRecoveries 列出追偿明细，可按陪玩与状态过滤，按 ID 倒序。
func listPlayerRecoveries(ctx context.Context, db *ent.Client, playerID int, status string,
	limit int) ([]*ent.EsportsPlayerRecovery, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsPlayerRecovery.Query()
	if playerID > 0 {
		q.Where(esportsplayerrecovery.PlayerIDEQ(playerID))
	}
	if status = strings.TrimSpace(status); status != "" {
		switch status {
		case "pending", "approved", "recovered", "rejected":
			q.Where(esportsplayerrecovery.StatusEQ(esportsplayerrecovery.Status(status)))
		default:
			return nil, errInvalidInput
		}
	}
	return q.Order(ent.Desc(esportsplayerrecovery.FieldID)).Limit(limit).All(ctx)
}

// approvePlayerRecovery 审批追偿：pending → approved。审批人不得为申请人（不得自审）。
func approvePlayerRecovery(ctx context.Context, db *ent.Client, id, adminID int,
	now time.Time) (*ent.EsportsPlayerRecovery, error) {
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsPlayerRecovery.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsplayerrecovery.StatusPending {
		return nil, errConflict
	}
	if item.RequestedByAdminID != nil && *item.RequestedByAdminID == adminID {
		return nil, fmt.Errorf("%w：追偿不得自审（申请人不能审批本人的追偿单）", errForbidden)
	}
	changed, err := db.EsportsPlayerRecovery.Update().Where(
		esportsplayerrecovery.IDEQ(id),
		esportsplayerrecovery.VersionEQ(item.Version),
		esportsplayerrecovery.StatusEQ(esportsplayerrecovery.StatusPending),
	).SetStatus(esportsplayerrecovery.StatusApproved).
		SetApprovedByAdminID(adminID).SetApprovedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsPlayerRecovery.Get(ctx, id)
}

// rejectPlayerRecovery 驳回追偿：pending → rejected，须填原因；审批人不得为申请人。
func rejectPlayerRecovery(ctx context.Context, db *ent.Client, id, adminID int, reason string,
	now time.Time) (*ent.EsportsPlayerRecovery, error) {
	reason = strings.TrimSpace(reason)
	if id < 1 || adminID < 1 || reason == "" || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsPlayerRecovery.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsplayerrecovery.StatusPending {
		return nil, errConflict
	}
	if item.RequestedByAdminID != nil && *item.RequestedByAdminID == adminID {
		return nil, fmt.Errorf("%w：追偿不得自审（申请人不能驳回本人的追偿单）", errForbidden)
	}
	changed, err := db.EsportsPlayerRecovery.Update().Where(
		esportsplayerrecovery.IDEQ(id),
		esportsplayerrecovery.VersionEQ(item.Version),
		esportsplayerrecovery.StatusEQ(esportsplayerrecovery.StatusPending),
	).SetStatus(esportsplayerrecovery.StatusRejected).
		SetApprovedByAdminID(adminID).SetApprovedAt(now.UTC()).
		SetRejectReason(reason).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsPlayerRecovery.Get(ctx, id)
}

// executePlayerRecovery 执行扣回：从陪玩猫粮扣回追偿金额（余额不足则拒绝），approved → recovered。
// 猫粮余额由流水求和派生，扣回写一条负数 adjust 流水（只增不改）。
func executePlayerRecovery(ctx context.Context, db *ent.Client, id, adminID int,
	now time.Time) (*ent.EsportsPlayerRecovery, error) {
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsPlayerRecovery.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsplayerrecovery.StatusApproved {
		return nil, errConflict
	}
	if item.RequestedByAdminID != nil && *item.RequestedByAdminID == adminID {
		return nil, fmt.Errorf("%w：追偿不得自办（申请人不能执行本人的追偿单）", errForbidden)
	}
	// 追偿扣猫粮与提现/结算共用同一把猫粮锁，避免并发把猫粮扣成负数。
	unlockCatfood := lockMoneyAccount(fmt.Sprintf("catfood:%d", item.PlayerID))
	defer unlockCatfood()
	balance, err := walletBalanceTx(ctx, tx, walletPlayerCatfood, item.PlayerID)
	if err != nil {
		return nil, err
	}
	if balance.total() < item.AmountCents {
		return nil, fmt.Errorf("%w：陪玩猫粮余额不足（当前 %d 分，需扣回 %d 分），可待结算后再执行",
			errInsufficientBalance, balance.total(), item.AmountCents)
	}
	walletTx, err := tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypePlayerCatfood).SetOwnerID(item.PlayerID).
		SetEntryType(esportswallettx.EntryTypeAdjust).
		SetPrincipalDeltaCents(-item.AmountCents).
		SetSourceType("player_recovery").SetSourceID(item.RecoveryNo).
		SetBeneficiaryType(esportswallettx.BeneficiaryTypePlayer).SetBeneficiaryID(item.PlayerID).
		SetBalanceBeforeCents(balance.total()).SetBalanceAfterCents(balance.total() - item.AmountCents).
		SetOperatorID(adminID).SetNote("陪玩追偿扣回").Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	changed, err := tx.EsportsPlayerRecovery.Update().Where(
		esportsplayerrecovery.IDEQ(item.ID),
		esportsplayerrecovery.VersionEQ(item.Version),
		esportsplayerrecovery.StatusEQ(esportsplayerrecovery.StatusApproved),
	).SetStatus(esportsplayerrecovery.StatusRecovered).
		SetRecoveredAt(now.UTC()).SetWalletTxID(walletTx.ID).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsPlayerRecovery.Get(ctx, id)
}

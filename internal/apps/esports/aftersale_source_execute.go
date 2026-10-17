package esports

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsgiftorder"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/esportswallettx"
)

// 非订单来源售后执行（§13.3 / §13.6）：
// 礼物 / 储值卡 / 充值三类来源从「待执行」落账并完成，各按自身退款口径原路退回、
// 冲回分成 / 权益，幂等靠售后单状态流转 + 下游唯一索引兜底。

// completeSourceAfterSale 事务内把非订单售后单置为已完成并写外盒留痕（§13.6）。
func completeSourceAfterSale(ctx context.Context, tx *ent.Tx, id, adminID int, eventType string,
	payload map[string]interface{}, now time.Time) error {
	changed, err := tx.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(esportsaftersale.StatusPendingExecution),
	).SetStatus(esportsaftersale.StatusCompleted).SetExecutorAdminID(adminID).
		SetResolvedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	if payload == nil {
		payload = map[string]interface{}{}
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("aftersale." + eventType + ":" + strconv.Itoa(id)).
		SetEventType("aftersale." + eventType).
		SetAggregateRef(strconv.Itoa(id)).
		SetPayload(payload).Save(ctx); err != nil && !ent.IsConstraintError(err) {
		return err
	}
	return nil
}

// executeGiftAfterSale 礼物售后执行：按原付款来源退回（余额本金或储值卡本金），
// 复用 refundGiftIn 冲回礼物分成（kind=gift_reversal）与成长值。
func executeGiftAfterSale(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsAfterSale, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsaftersale.StatusPendingExecution {
		return nil, errConflict
	}
	if item.PlanAction != esportsaftersale.PlanActionRefundOriginal || item.RefundCents <= 0 {
		return nil, errInvalidInput
	}
	gift, err := tx.EsportsGiftOrder.Get(ctx, item.SourceID)
	if err != nil {
		return nil, err
	}
	if gift.Status != esportsgiftorder.StatusPaid {
		return nil, errConflict
	}
	reason := item.Resolution
	if reason == "" {
		reason = "礼物售后退款"
	}
	if err = refundGiftIn(ctx, tx, gift, item.RefundCents, "after_sale:"+item.AfterSaleNo, reason, adminID, now); err != nil {
		return nil, err
	}
	if err = completeSourceAfterSale(ctx, tx, id, adminID, "gift_refunded", map[string]interface{}{
		"after_sale_no": item.AfterSaleNo, "gift_no": gift.GiftNo, "refund_cents": item.RefundCents,
	}, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// executeStoredValueCardAfterSale 储值卡售后执行：退卡退款按原路（与 decideStoredValueRefund 同口径），
// 冲减卡内本金与赠送、冲回预付款批次账本并把卡置为 refunded。
func executeStoredValueCardAfterSale(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsAfterSale, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsaftersale.StatusPendingExecution {
		return nil, errConflict
	}
	if item.PlanAction != esportsaftersale.PlanActionRefundOriginal || item.RefundCents <= 0 {
		return nil, errInvalidInput
	}
	card, err := tx.EsportsStoredValueCard.Get(ctx, item.SourceID)
	if err != nil {
		return nil, err
	}
	switch card.Status {
	case esportsstoredvaluecard.StatusActive, esportsstoredvaluecard.StatusExhausted, esportsstoredvaluecard.StatusExpired:
	default:
		return nil, errStoredValueUnusable
	}
	balance, err := storedValueCardBalanceTx(ctx, tx, card.ID)
	if err != nil {
		return nil, err
	}
	_, refundable := storedValueRefundQuote(card, balance, now)
	if refundable <= 0 {
		return nil, fmt.Errorf("%w：该卡已无未消费本金，无需退卡", errInvalidInput)
	}
	if item.RefundCents != refundable {
		return nil, fmt.Errorf("%w：退款金额与储值卡未消费本金不一致（应退 %d）", errConflict, refundable)
	}
	forfeited := balance.BonusCents
	// 退卡：本金退回、赠送作废，两者分列记账（§4.4）。
	if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypeStoredValueCard).SetOwnerID(card.ID).
		SetEntryType(esportswallettx.EntryTypeRefundReturn).
		SetPrincipalDeltaCents(-balance.PrincipalCents).SetBonusDeltaCents(-balance.BonusCents).
		SetSourceType("after_sale").SetSourceID(item.AfterSaleNo).
		SetOperatorID(adminID).SetNote("售后退卡：本金退回，赠送作废").Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, fmt.Errorf("%w：退款流水写入失败：%v", errExecutionFailed, err)
	}
	if _, err = tx.EsportsLedgerEntry.Create().
		SetAccountType(storedValuePrepayAccount).SetAccountID(strconv.Itoa(card.ID)).
		SetSourceType("after_sale").SetSourceID(item.AfterSaleNo).
		SetEntryRole("card_refund").SetDeltaCents(-balance.total()).
		SetFundBatch(card.CardNo).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, fmt.Errorf("%w：批次账本冲回写入失败：%v", errExecutionFailed, err)
	}
	if forfeited > 0 {
		if _, err = tx.EsportsLedgerEntry.Create().
			SetAccountType(storedValuePrepayAccount).SetAccountID(strconv.Itoa(card.ID)).
			SetSourceType("after_sale").SetSourceID(item.AfterSaleNo).
			SetEntryRole("card_bonus_forfeit").SetDeltaCents(0).
			SetFundBatch(card.CardNo).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, fmt.Errorf("%w：赠送作废留痕写入失败：%v", errExecutionFailed, err)
		}
	}
	changed, err := tx.EsportsStoredValueCard.Update().Where(
		esportsstoredvaluecard.IDEQ(card.ID),
		esportsstoredvaluecard.VersionEQ(card.Version),
	).SetStatus(esportsstoredvaluecard.StatusRefunded).
		SetRefundedCents(refundable).
		SetRefundDecidedByAdminID(adminID).SetRefundedAt(now.UTC()).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = completeSourceAfterSale(ctx, tx, id, adminID, "card_refunded", map[string]interface{}{
		"after_sale_no": item.AfterSaleNo, "card_no": card.CardNo, "refund_cents": refundable,
	}, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// executeRechargeAfterSale 充值售后执行：按充值批次还原可退本金 / 作废赠送 / 已消费金额，
// 同一事务内写内部退款流水冲减余额、按批次生成外部退款执行明细（§4.5）。
func executeRechargeAfterSale(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsAfterSale, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsaftersale.StatusPendingExecution {
		return nil, errConflict
	}
	if item.PlanAction != esportsaftersale.PlanActionRefundOriginal || item.RefundCents <= 0 {
		return nil, errInvalidInput
	}
	row, err := tx.EsportsWalletTx.Get(ctx, item.SourceID)
	if err != nil {
		return nil, err
	}
	if row.EntryType != esportswallettx.EntryTypeRecharge {
		return nil, errInvalidInput
	}
	accountType, accountID, err := refundExecutionAccount(ctx, tx.Client(), item)
	if err != nil {
		return nil, err
	}
	quote, err := rechargeRefundQuoteFor(ctx, tx.Client(), accountType, accountID, now)
	if err != nil {
		return nil, err
	}
	refundable := quote.RefundablePrincipalCents
	forfeited := quote.ForfeitedBonusCents
	if refundable <= 0 {
		return nil, fmt.Errorf("%w：该充值已无可退本金", errInvalidInput)
	}
	if item.RefundCents != refundable {
		return nil, fmt.Errorf("%w：退款金额与充值批次可退本金不一致（应退 %d）", errConflict, refundable)
	}
	// 内部返还：按原账户原路冲减本金与赠送（先写流水，余额由流水求和派生）。
	if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(row.AccountType).SetOwnerID(row.OwnerID).
		SetEntryType(esportswallettx.EntryTypeRefundReturn).
		SetPrincipalDeltaCents(-refundable).SetBonusDeltaCents(-forfeited).
		SetSourceType("after_sale").SetSourceID(item.AfterSaleNo).
		SetOperatorID(adminID).SetNote("充值售后退款").Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, fmt.Errorf("%w：退款流水写入失败：%v", errExecutionFailed, err)
	}
	// 冲回批次账本（与卡退卡 card_refund 同口径）。
	if _, err = tx.EsportsLedgerEntry.Create().
		SetAccountType(accountType).SetAccountID(accountID).
		SetSourceType("after_sale").SetSourceID(item.AfterSaleNo).
		SetEntryRole("recharge_refund").SetDeltaCents(-(refundable + forfeited)).
		SetFundBatch(item.AfterSaleNo).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, fmt.Errorf("%w：批次账本冲回写入失败：%v", errExecutionFailed, err)
	}
	// 外部退款执行明细（§4.5）：按批次还原实付本金 / 赠送权益 / 已消费金额，幂等落账。
	if err = executeRechargeRefundQuoteTx(ctx, tx, item, quote); err != nil {
		return nil, fmt.Errorf("%w：按批次退款执行明细写入失败：%v", errExecutionFailed, err)
	}
	if err = completeSourceAfterSale(ctx, tx, id, adminID, "recharge_refunded", map[string]interface{}{
		"after_sale_no": item.AfterSaleNo, "refund_cents": refundable, "forfeited_bonus_cents": forfeited,
	}, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

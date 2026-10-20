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
	"go-admin/internal/ent/esportswallettx"
)

// 非订单来源售后执行（§13.3 / §13.6）：
// 礼物 / 充值两类来源从「待执行」落账并完成，各按自身退款口径原路退回、
// 冲回分成 / 权益，幂等靠售后单状态流转 + 下游唯一索引兜底。

// completeSourceAfterSale 事务内把非订单售后单置为已完成并写外盒留痕（§13.6）。
func completeSourceAfterSale(ctx context.Context, tx *ent.Tx, id, adminID int, eventType string,
	payload map[string]interface{}, now time.Time) error {
	changed, err := tx.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(esportsaftersale.StatusPendingExecution),
	).SetStatus(esportsaftersale.StatusCompleted).SetExecutorAdminID(adminID).
		SetResolvedAt(now.UTC()).AddVersion(1).
		// §13.2-09：落终态释放进行中唯一键。
		ClearActiveDedupKey().Save(ctx)
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

// executeGiftAfterSale 礼物售后执行：按原付款来源退回（余额本金），
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

// executeRechargeAfterSale 充值售后执行：按充值批次还原可退本金 / 作废赠送 / 已消费金额，
// 同一事务内写内部退款流水冲减余额、按批次生成外部退款执行明细（§4.5）。
//
// 部分退款口径：允许 item.RefundCents <= 试算可退本金（剩余可返本金），本金按实际退款额冲减；
// 赠送按「退款本金 / 可退本金」同比例作废（全额退款时比例 = 100%，与既有「未消费赠送随退作废」
// 口径完全一致，不改变全额退款路径）。外部退款执行明细同样按该退款额封顶。
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
	// 充值售后退款与订单核销/购卡共用同一账户锁，避免并发把余额扣成负数。
	unlockAccount := lockMoneyAccount(moneyAccountLockKey(row.AccountType.String(), strconv.Itoa(row.OwnerID)))
	defer unlockAccount()
	quote, err := rechargeRefundQuoteFor(ctx, tx.Client(), accountType, accountID, now)
	if err != nil {
		return nil, err
	}
	refundable := quote.RefundablePrincipalCents
	if refundable <= 0 {
		return nil, fmt.Errorf("%w：该充值已无可退本金", errInvalidInput)
	}
	if item.RefundCents > refundable {
		return nil, fmt.Errorf("%w：退款金额不得超过充值批次剩余可退本金（可退 %d）", errConflict, refundable)
	}
	// 赠送按退款本金比例作废：全额退款时比例 100%，等价于原「未消费赠送随退作废」规则。
	forfeited := quote.ForfeitedBonusCents
	if item.RefundCents < refundable {
		forfeited = quote.ForfeitedBonusCents * item.RefundCents / refundable
	}
	// 内部返还：按原账户原路冲减本金与赠送（先写流水，余额由流水求和派生）。
	if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(row.AccountType).SetOwnerID(row.OwnerID).
		SetEntryType(esportswallettx.EntryTypeRefundReturn).
		SetPrincipalDeltaCents(-item.RefundCents).SetBonusDeltaCents(-forfeited).
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
		SetEntryRole("recharge_refund").SetDeltaCents(-(item.RefundCents + forfeited)).
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
		"after_sale_no": item.AfterSaleNo, "refund_cents": item.RefundCents, "forfeited_bonus_cents": forfeited,
	}, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

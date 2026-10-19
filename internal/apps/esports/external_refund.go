package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsexternalrefund"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/systemconfig"
)

// 外部渠道退款（§13 补全）：针对已核销的外部付款订单退款，余额 / 储值卡原路退之外的场景。
// 真实通道退款仍走人工登记：财务登记外部退款凭证（registered），经财务核验后执行（executed），
// 执行时把订单累计已退额与退款状态对齐留痕。幂等：同一外部退款流水号只能登记一次。

// externalRefundNo 生成外部退款登记号，前缀 ER。
func externalRefundNo() string { return "ER" + uuid.NewString() }

// registerExternalRefundInput 财务登记外部退款凭证入参。
type registerExternalRefundInput struct {
	Channel       string `json:"channel"`
	TransactionNo string `json:"transaction_no"`
	AmountCents   int64  `json:"amount_cents"`
	OrderID       int    `json:"order_id"`
	AfterSaleID   *int   `json:"aftersale_id"`
	Reason        string `json:"reason"`
}

// externalRefundRequireAfterSaleKey 是否强制外部退款关联售后单（§13.7-04 赔付收口到售后单）：
// 1 = 拒绝未关联售后单的对外付款登记；0 / 未配置 = 保留既有登记路径，但一律写私下赔付疑似留痕。
const externalRefundRequireAfterSaleKey = "app.esports.external_refund_require_aftersale"

// externalRefundRequireAfterSale 读取「外部退款须关联售后单」开关；缺失 / 非法一律读作关闭。
func externalRefundRequireAfterSale(ctx context.Context, client *ent.Client) bool {
	item, err := client.SystemConfig.Query().Where(
		systemconfig.KeyEQ(externalRefundRequireAfterSaleKey),
	).Only(ctx)
	if err != nil {
		return false
	}
	value, err := strconv.Atoi(strings.TrimSpace(item.Value))
	return err == nil && value == 1
}

// registerExternalRefund 财务登记外部渠道退款凭证：登记渠道 + 流水号 + 金额 + 关联售后/订单，
// 状态 registered。只记账留痕，不改订单退款状态（订单退款状态在执行时对齐）。
// 幂等：同一外部退款流水号只能登记一次（唯一索引兜底）。
//
// 赔付依据（§13.7-04）：赔付须收口到售后单，`app.esports.external_refund_require_aftersale=1`
// 时未关联售后单的对外付款直接拒绝（无责任判定 / 无方案的私下赔付不落账）；
// 未开启该开关时保留既有登记路径，但同一事务内额外写 `payout.offbook_suspected` 事务外盒留痕，
// 供风控 / 财务复核疑似私下赔付。
//
// 额度口径（§13）：登记阶段只做「登记不虚占/不透支」的基本校验，避免大量待执行登记
// 叠加起来超过订单可退额：
//   - 关联售后单（AfterSaleID != nil）时，该金额已在审批阶段预占进 refund_reserved_cents，
//     登记阶段不再重复计入，也不再额外校验（执行时按预占额度核销）；
//   - 未关联售后单时，按 refunded_cents + refund_reserved_cents + 该订单已登记未执行的外部退款
//     合计 + 本次 <= 服务应付 + 加钟已收款（§12.5-02 可退上界）校验，不得侵占已被售后预占的额度。
func registerExternalRefund(ctx context.Context, db *ent.Client, adminID int,
	in registerExternalRefundInput, now time.Time) (*ent.EsportsExternalRefund, error) {
	in.Channel = strings.TrimSpace(in.Channel)
	in.TransactionNo = strings.TrimSpace(in.TransactionNo)
	in.Reason = strings.TrimSpace(in.Reason)
	if adminID < 1 || in.Channel == "" || len(in.Channel) > 48 || in.TransactionNo == "" ||
		len(in.TransactionNo) > 128 || in.AmountCents < 1 || in.OrderID < 1 ||
		len([]rune(in.Reason)) > 255 {
		return nil, errInvalidInput
	}
	if in.AfterSaleID != nil && *in.AfterSaleID < 1 {
		return nil, errInvalidInput
	}
	// §13.7-04：赔付须收口到售后单（严格模式），无售后依据的对外付款不得登记。
	requireAfterSale := in.AfterSaleID == nil && externalRefundRequireAfterSale(ctx, db)
	if requireAfterSale {
		return nil, fmt.Errorf("%w：外部退款须关联售后单（赔付须有售后方案与责任判定依据），请先在售后单内审批后再登记",
			errInvalidInput)
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, in.OrderID)
	if err != nil {
		return nil, err
	}
	if in.AfterSaleID != nil {
		if _, err = tx.EsportsAfterSale.Get(ctx, *in.AfterSaleID); err != nil {
			return nil, err
		}
	} else {
		// 未关联售后单：除已落账 / 已预占额度外，还要扣掉同订单其他「已登记未执行」的外部退款，
		// 否则多笔待执行登记会合计透支订单可退额（§12.5-02：服务应付 + 加钟已收款）。
		pending, err := externalRefundPendingCents(ctx, tx, order.ID)
		if err != nil {
			return nil, err
		}
		addonReceived, err := addonReceivedCents(ctx, tx.Client(), order.ID)
		if err != nil {
			return nil, err
		}
		refundableUpper := order.PayableCents + addonReceived
		if order.RefundedCents+order.RefundReservedCents+pending+in.AmountCents > refundableUpper {
			return nil, fmt.Errorf("%w：外部退款登记金额超出订单可退额度（已退 %d，已预占 %d，待执行登记 %d，可退上界 %d）",
				errConflict, order.RefundedCents, order.RefundReservedCents, pending, refundableUpper)
		}
	}
	create := tx.EsportsExternalRefund.Create().SetRefundNo(externalRefundNo()).
		SetChannel(in.Channel).SetTransactionNo(in.TransactionNo).
		SetAmountCents(in.AmountCents).SetOrderID(in.OrderID).
		SetStatus(esportsexternalrefund.StatusRegistered).
		SetReason(in.Reason).SetRegisteredByAdminID(adminID)
	if in.AfterSaleID != nil {
		create.SetAftersaleID(*in.AfterSaleID)
	}
	refund, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.external_refund_registered").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"refund_no": refund.RefundNo, "channel": in.Channel,
			"transaction_no": in.TransactionNo, "amount_cents": in.AmountCents,
			"linked_aftersale": in.AfterSaleID != nil,
		}).Save(ctx); err != nil {
		return nil, err
	}
	// §13.7-04 私下赔付检测留痕：未关联售后单的对外付款额外写一条非通知类事务外盒
	// （payout.* 前缀不属于通知事件，由业务事件投递器定向投给超管），供风控 / 财务复核。
	if in.AfterSaleID == nil {
		if _, err = tx.EsportsOutbox.Create().
			SetEventKey("payout.offbook_suspected:" + refund.RefundNo).
			SetEventType("payout.offbook_suspected").SetAggregateRef(order.OrderNo).
			SetPayload(map[string]interface{}{
				"order_id": order.ID, "order_no": order.OrderNo, "refund_no": refund.RefundNo,
				"amount_cents": in.AmountCents, "reason": in.Reason,
				"registered_by_admin_id": adminID,
			}).Save(ctx); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsExternalRefund.Get(ctx, refund.ID)
}

// executeExternalRefund 财务核验并执行外部退款（§3.2-03 / §13）：
// 先做只读前置校验（状态 / 登记人 / 冻结），再把订单退款状态置 executing（独立事务提交，
// 结果未定的对外付款可被对账识别），最后在单事务内完成「registered → executed + 额度落账 + 冲回」。
// 执行阶段任何失败都把订单退款状态落 failed 并留原因——外部渠道的钱已经出账，
// 落账失败必须显式交人工处理，不能回退成「没发生过」。
//
// 额度口径（§13）：
//   - 关联售后单（AfterSaleID != nil）：金额在售后审批时已预占，执行时要求
//     refund_reserved_cents >= 本次金额，扣减预占并累加已退额，不再要求额外预占；
//   - 未关联售后单：按 refunded_cents + refund_reserved_cents + 本次 <= 服务应付 + 加钟已收款
//     （§12.5-02 可退上界）校验，只累加已退额，不得侵占已被售后占用的额度。
//
// 两种情况都用条件表达式防并发，保证累计 refunded_cents 不超过可退上界（服务应付 + 加钟已收款）。
//
// 禁止登记人自行执行（与售后「不得自审」同口径）。
func executeExternalRefund(ctx context.Context, db *ent.Client, adminID, id int, now time.Time) (*ent.EsportsExternalRefund, error) {
	if adminID < 1 || id < 1 {
		return nil, errInvalidInput
	}
	// 只读前置校验：不在「状态不对 / 自己登记 / 订单冻结」的单上写 executing 与 failed。
	refund, err := db.EsportsExternalRefund.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if refund.Status != esportsexternalrefund.StatusRegistered {
		return nil, errConflict
	}
	if refund.RegisteredByAdminID != nil && *refund.RegisteredByAdminID == adminID {
		return nil, errForbidden
	}
	order, err := db.EsportsOrder.Get(ctx, refund.OrderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	// §3.2-03：外部渠道退款已受理、结果未定 → 订单退款状态置 executing
	// （独立事务提交，便于对账识别「已出款但未落账」的未决单）；已是 executing 时幂等跳过。
	if order.RefundStatus != esportsorder.RefundStatusExecuting {
		if err = markOrderRefundExecuting(ctx, db, order.ID, adminID,
			"外部渠道退款执行中："+refund.RefundNo, now); err != nil {
			return nil, err
		}
	}
	item, err := executeExternalRefundOnce(ctx, db, adminID, id, now)
	if err != nil {
		if markErr := markOrderRefundFailed(ctx, db, order.ID, adminID, err.Error(), now); markErr != nil {
			return nil, fmt.Errorf("%w（另：退款失败留痕写入失败：%v）", err, markErr)
		}
		return nil, err
	}
	return item, nil
}

func executeExternalRefundOnce(ctx context.Context, db *ent.Client, adminID, id int, now time.Time) (*ent.EsportsExternalRefund, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	refund, err := tx.EsportsExternalRefund.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if refund.Status != esportsexternalrefund.StatusRegistered {
		return nil, errConflict
	}
	// 登记人不得自行核验执行，避免同一人既登记又放款。
	if refund.RegisteredByAdminID != nil && *refund.RegisteredByAdminID == adminID {
		return nil, errForbidden
	}
	order, err := tx.EsportsOrder.Get(ctx, refund.OrderID)
	if err != nil {
		return nil, err
	}
	// 存单冻结期间禁止执行外部退款：冻结单的退款额度与资金状态不得变化，先恢复再操作。
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	changed, err := tx.EsportsExternalRefund.Update().Where(
		esportsexternalrefund.IDEQ(id),
		esportsexternalrefund.StatusEQ(esportsexternalrefund.StatusRegistered),
		esportsexternalrefund.VersionEQ(refund.Version),
	).SetStatus(esportsexternalrefund.StatusExecuted).
		SetExecutedByAdminID(adminID).SetExecutedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	// 订单退款状态对齐：超退靠条件表达式拦截，累计已退额与退款状态同一事务更新。
	targetRefunded := order.RefundedCents + refund.AmountCents
	// §12.5-02：可退上界 = 服务应付 + 加钟已收款（只增流水求和派生），两条谓词都必须带上同一上界。
	addonReceived, err := addonReceivedCents(ctx, tx.Client(), order.ID)
	if err != nil {
		return nil, err
	}
	refundableUpper := order.PayableCents + addonReceived
	if refund.AftersaleID != nil {
		// 售后审批已把该笔金额预占在 refund_reserved_cents 上：执行时核销预占，不再重复占用额度。
		changed, err = tx.EsportsOrder.Update().Where(
			esportsorder.IDEQ(order.ID),
			func(s *sql.Selector) {
				s.Where(sql.ExprP("refund_reserved_cents >= ?", refund.AmountCents))
				s.Where(sql.ExprP("refunded_cents + refund_reserved_cents <= payable_cents + ?", addonReceived))
			},
		).AddRefundReservedCents(-refund.AmountCents).AddRefundedCents(refund.AmountCents).
			SetRefundStatus(refundStatusFor(refundableUpper, targetRefunded)).
			AddVersion(1).Save(ctx)
	} else {
		changed, err = tx.EsportsOrder.Update().Where(
			esportsorder.IDEQ(order.ID),
			func(s *sql.Selector) {
				s.Where(sql.ExprP("refunded_cents + refund_reserved_cents + ? <= payable_cents + ?",
					refund.AmountCents, addonReceived))
			},
		).AddRefundedCents(refund.AmountCents).
			SetRefundStatus(refundStatusFor(refundableUpper, targetRefunded)).
			AddVersion(1).Save(ctx)
	}
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	refundedOrder, err := tx.EsportsOrder.Get(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	// 退款额度落账后、同事务冲回陪玩分成 / 客服提成 / 客户成长值（幂等键用退款单号）。
	rev := refundReversalContext{
		RefundCents:  refund.AmountCents,
		RefKey:       refund.RefundNo,
		AfterSaleID:  refund.AftersaleID,
		ActorAdminID: adminID,
		Note:         "外部退款冲回",
	}
	if err = reverseOrderCommissionFor(ctx, tx, refundedOrder, rev, now); err != nil {
		return nil, err
	}
	if err = reverseOrderCsCommissionFor(ctx, tx, refundedOrder, rev, now); err != nil {
		return nil, err
	}
	if err = reverseOrderGrowthFor(ctx, tx, refundedOrder, rev, now); err != nil {
		return nil, err
	}
	// 订单更新已抬升一个版本，事件直接落在这个新版本上。
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.external_refund_executed").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"refund_no": refund.RefundNo, "channel": refund.Channel,
			"transaction_no": refund.TransactionNo, "amount_cents": refund.AmountCents,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsExternalRefund.Get(ctx, id)
}

// externalRefundPendingCents 该订单已登记未执行、且未关联售后单的外部退款合计。
// 关联售后单的登记金额已包含在 refund_reserved_cents 中，不重复计入。
func externalRefundPendingCents(ctx context.Context, tx *ent.Tx, orderID int) (int64, error) {
	rows, err := tx.EsportsExternalRefund.Query().Where(
		esportsexternalrefund.OrderIDEQ(orderID),
		esportsexternalrefund.StatusEQ(esportsexternalrefund.StatusRegistered),
		esportsexternalrefund.AftersaleIDIsNil(),
	).Select(esportsexternalrefund.FieldAmountCents).All(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, row := range rows {
		total += row.AmountCents
	}
	return total, nil
}

// listExternalRefunds 查询外部退款登记，orderID 为 0 表示不按订单过滤，status 可选。
func listExternalRefunds(ctx context.Context, db *ent.Client, orderID int, status string, limit int) ([]*ent.EsportsExternalRefund, error) {
	if orderID < 0 {
		return nil, errInvalidInput
	}
	status = strings.TrimSpace(status)
	switch status {
	case "", esportsexternalrefund.StatusRegistered.String(), esportsexternalrefund.StatusExecuted.String():
	default:
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	q := db.EsportsExternalRefund.Query()
	if orderID > 0 {
		q.Where(esportsexternalrefund.OrderIDEQ(orderID))
	}
	if status != "" {
		q.Where(esportsexternalrefund.StatusEQ(esportsexternalrefund.Status(status)))
	}
	return q.Order(ent.Desc(esportsexternalrefund.FieldID)).Limit(limit).All(ctx)
}

package esports

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsexternalrefund"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
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
	// P1：订单支付状态异常（渠道撤销 / 拒付）时原支付款去向已不确定，
	// 再执行退款等于二次放款，一律禁止并交人工处置（与取消路径同口径）。
	if err = ensureOrderPaymentStatusRefundableForExternalRefund(order); err != nil {
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
	// P1：渠道撤销 / 拒付（payment_status=exception）的订单一律不得再放款，转人工处置。
	if err = ensureOrderPaymentStatusRefundableForExternalRefund(order); err != nil {
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
	// §8.2-11：财务执行成功后，把对应的售后动作项从 awaiting_execution 转 succeeded；
	// 其余项已无未结项时同一事务内把售后单结案（详见 aftersale_actions.go）。
	if err = afterSaleActionAwaitingExternalRefundExecuted(ctx, tx, refund, adminID, now); err != nil {
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

// ensureOrderPaymentStatusRefundableForExternalRefund 外部退款执行前的订单支付状态守卫（P1）：
// 渠道撤销 / 拒付后订单落 payment_status=exception，原支付款去向已不确定，
// 此时再放款等于二次赔付，一律拒绝并要求人工处置（与已付款取消路径同口径）。
func ensureOrderPaymentStatusRefundableForExternalRefund(order *ent.EsportsOrder) error {
	if order == nil {
		return errInvalidInput
	}
	if order.PaymentStatus == esportsorder.PaymentStatusException {
		return fmt.Errorf("%w：订单支付状态异常（渠道撤销 / 拒付），禁止执行退款，请先人工处置支付异常", errConflict)
	}
	return nil
}

// externalRefundChannelSupported 订单支付渠道是否为外部渠道（§13 补全）：
// 微信 / 商户码 / 对公转账这类非预付账户渠道的退款必须走通道退款单，不能直接改钱包。
func externalRefundChannelSupported(channel esportsorder.PaymentChannel) bool {
	switch channel {
	case esportsorder.PaymentChannelWechatWork, esportsorder.PaymentChannelMerchantQr,
		esportsorder.PaymentChannelCorporateTransfer:
		return true
	}
	return false
}

// afterSaleNeedsExternalRefund 判断售后「退原核销款」是否必须生成外部渠道退款单（P0-10）：
// 订单支付渠道为外部渠道，且订单没有任何余额 / 储值卡核销流水（预付账户原路退回不成立）。
// 有核销流水时仍按原账户原路退回，避免把已进预付账户的钱当通道款再退一次。
func afterSaleNeedsExternalRefund(ctx context.Context, client *ent.Client,
	item *ent.EsportsAfterSale, order *ent.EsportsOrder) (bool, error) {
	if item == nil || order == nil || item.SourceType != esportsaftersale.SourceTypeOrder ||
		item.RefundCents <= 0 {
		return false, nil
	}
	if !externalRefundChannelSupported(order.PaymentChannel) {
		return false, nil
	}
	exists, err := client.EsportsWalletTx.Query().Where(
		esportswallettx.SourceTypeEQ("order"),
		esportswallettx.SourceIDEQ(order.OrderNo),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeConsume),
	).Exist(ctx)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

// afterSaleExternalRefundTransactionNo 售后自动生成的外部退款登记流水号：
// 以「售后单号 / 按项来源号」为幂等键——单动作售后退原核销款用售后单号，
// 多动作方案用按项来源号（<售后单号>:a<序号>），使同一售后单的多条外部退款动作项各自成单、
// 又能靠 transaction_no 唯一索引兜底重复执行不重复登记。
func afterSaleExternalRefundTransactionNo(item *ent.EsportsAfterSale) string {
	return "AS-EXT-" + item.AfterSaleNo
}

// createAfterSaleExternalRefundIn 在售后执行事务内登记一笔待执行的外部渠道退款单（P0-10）：
// 复用既有 EsportsExternalRefund 实体与 registered → executed 状态机（不新造平行结构），
// 由财务用 executeExternalRefund 核验执行；执行时按售后预占额度落账，而不是直接改钱包。
// 幂等：同一幂等键（transaction_no）已有待执行登记时直接复用，不重复登记。
func createAfterSaleExternalRefundIn(ctx context.Context, tx *ent.Tx, item *ent.EsportsAfterSale,
	order *ent.EsportsOrder, adminID int) (*ent.EsportsExternalRefund, error) {
	if item == nil || order == nil || item.RefundCents <= 0 || adminID < 1 {
		return nil, errInvalidInput
	}
	transactionNo := afterSaleExternalRefundTransactionNo(item)
	existing, err := tx.EsportsExternalRefund.Query().Where(
		esportsexternalrefund.TransactionNoEQ(transactionNo),
	).First(ctx)
	if err == nil {
		if existing.Status == esportsexternalrefund.StatusRegistered {
			return existing, nil
		}
		// 已执行过的幂等键不再复用（旧单已出账），明确冲突交人工核对，避免重复放款。
		return nil, errConflict
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	refund, err := tx.EsportsExternalRefund.Create().
		SetRefundNo(externalRefundNo()).
		// 渠道沿用订单支付渠道快照：退原核销款只能退回到原通道。
		SetChannel(order.PaymentChannel.String()).
		SetTransactionNo(transactionNo).
		SetAmountCents(item.RefundCents).
		SetOrderID(order.ID).SetAftersaleID(item.ID).
		SetStatus(esportsexternalrefund.StatusRegistered).
		SetReason(truncateRunes("售后退原核销款（外部渠道待财务核验）："+item.AfterSaleNo, 255)).
		SetRegisteredByAdminID(adminID).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	return refund, nil
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

// ── 外部退款待执行超期督办（审核报告 §8.2-12） ──
//
// 外部渠道退款登记成 registered 之后，待执行期间额度一直被占着：关联售后单时它压在售后审批
// 预占的 refund_reserved_cents 上，未关联时它按「已登记未执行」继续占住订单可退额。只要有单
// 长期滞留在 registered，就会一直挂单并压住同订单后续退款额度，所以必须有督办兜底。
//
// 督办只做「提醒 + 标记」，绝不代办：
//   - 不自动执行：外部渠道的钱只能由财务人工核验后出账，任务代执行等于凭空放款；
//   - 不自动取消：已登记的外部退款可能已经出款，自动取消会把已出款单变成账外纠纷。
//
// 督办手段是写一条事务外盒事件 external_refund.overdue（业务事件，投递时以超管兜底，
// 即财务 / 超管可见），载荷显式带 overdue=true 供财务一眼识别「已超期」。
// 幂等：同一单同一小时桶只督办一次（event_key 唯一索引 + 先查后写），避免任务每次扫描刷屏。

// externalRefundOverdueEventType 待执行超期督办事件类型。
const externalRefundOverdueEventType = "external_refund.overdue"

// externalRefundOverdueHours 待执行外部退款的超期阈值（小时）：登记时间早于 now-该值、且仍为
// registered 的单即视为超期待督办。
//
// 刻意做成代码内常量而不是新增系统配置项：§8.2-12 只要求「有督办」，不为它扩张后台配置面
// （新增 app.esports.* 键必须同时进 app.go 的 Config 声明，且会让运维多一个必配项）。
const externalRefundOverdueHours = 24

// externalRefundOverdueScanCap 单轮督办扫描上限，避免积压时一次任务把全表拉进内存。
const externalRefundOverdueScanCap = 200

// externalRefundOverdueResult 是一轮督办扫描的统计结果，供任务日志与回归测试断言。
type externalRefundOverdueResult struct {
	// Scanned 本轮扫描到的「registered 且登记时间早于阈值」的单数。
	Scanned int
	// Overdue 其中复核后仍判定为超期的单数（与 Scanned 一致，复核用于防驱动时区差异）。
	Overdue int
	// Notified 本轮新写入督办事件的单数。
	Notified int
	// Skipped 本轮因「同一小时桶已督办过」而跳过的单数。
	Skipped int
	// Cutoff 本轮使用的超期判定线（now - 阈值）。
	Cutoff time.Time
}

// externalRefundOverdueEventKey 督办事件的幂等键：同一退款单同一小时桶只产生一条事件。
// esports_outbox.event_key 有唯一索引（并发跑同一轮时由它兜底）。
func externalRefundOverdueEventKey(refundNo string, at time.Time) string {
	return externalRefundOverdueEventType + ":" + refundNo + ":" + at.UTC().Format("2006010215")
}

// externalRefundOverdueCutoff 返回超期判定线（now - 阈值）：登记时间早于该判定线的 registered 单
// 才算「已超期未执行」。督办扫描与售后列表 / 详情侧的「外部退款已超期」标记共用本函数，
// 阈值只在 externalRefundOverdueHours 定义一次，不允许在查询条件里另写一份小时数。
func externalRefundOverdueCutoff(now time.Time) time.Time {
	return now.UTC().Add(-externalRefundOverdueHours * time.Hour)
}

// externalRefundOverdue 判断一条外部退款登记是否已超期待督办：仍为 registered 且登记时间
// 距 now 已达阈值。实体上没有「超期」字段（schema 不改），该判定是超期口径的唯一事实来源，
// 由任务扫描与列表 / 详情侧补「已超期」标记共同使用。
func externalRefundOverdue(refund *ent.EsportsExternalRefund, now time.Time) bool {
	if refund == nil || refund.Status != esportsexternalrefund.StatusRegistered {
		return false
	}
	return now.UTC().Sub(refund.CreatedAt.UTC()) >= externalRefundOverdueHours*time.Hour
}

// writeExternalRefundOverdueEvent 为一条超期单写督办事件；同一单同一小时桶已写过时返回 false。
// 只写事件，不改退款单状态、不改额度、不动订单——督办不代办。
func writeExternalRefundOverdueEvent(ctx context.Context, db *ent.Client, refund *ent.EsportsExternalRefund, now time.Time) (bool, error) {
	if db == nil || refund == nil || refund.ID < 1 || refund.RefundNo == "" {
		return false, errInvalidInput
	}
	eventKey := externalRefundOverdueEventKey(refund.RefundNo, now)
	exists, err := db.EsportsOutbox.Query().Where(esportsoutbox.EventKeyEQ(eventKey)).Exist(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	payload := map[string]interface{}{
		"refund_id":      refund.ID,
		"refund_no":      refund.RefundNo,
		"order_id":       refund.OrderID,
		"amount_cents":   refund.AmountCents,
		"channel":        refund.Channel,
		"transaction_no": refund.TransactionNo,
		"status":         refund.Status.String(),
		"registered_at":  refund.CreatedAt.UTC().Format(time.RFC3339),
		"overdue":        true,
		"overdue_hours":  externalRefundOverdueHours,
		"note":           "外部退款登记后长期未执行，请财务核验后人工执行；任务不自动执行、不自动取消",
	}
	if refund.AftersaleID != nil {
		payload["aftersale_id"] = *refund.AftersaleID
	}
	if refund.RegisteredByAdminID != nil {
		payload["registered_by_admin_id"] = *refund.RegisteredByAdminID
	}
	if _, err = db.EsportsOutbox.Create().SetEventKey(eventKey).
		SetEventType(externalRefundOverdueEventType).SetAggregateRef(refund.RefundNo).
		// 督办事件应立即进入投递队列：显式按 UTC 写首次投递时间，与投递循环的时钟口径一致。
		SetNextAttemptAt(now.UTC()).
		SetPayload(payload).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			// 并发跑同一轮时唯一索引兜底：重复写入按「本轮已督办」处理，不算失败。
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// expirePendingExternalRefunds 扫描「registered 且登记时间早于 now-阈值」的外部退款单，
// 逐单写督办事件。只读退款单、只追加事件：既不执行退款、也不取消登记、更不动额度。
func expirePendingExternalRefunds(ctx context.Context, db *ent.Client, now time.Time, limit int) (externalRefundOverdueResult, error) {
	var result externalRefundOverdueResult
	if db == nil {
		return result, errInvalidInput
	}
	if limit < 1 || limit > 1000 {
		return result, errInvalidInput
	}
	result.Cutoff = externalRefundOverdueCutoff(now)
	rows, err := db.EsportsExternalRefund.Query().Where(
		esportsexternalrefund.StatusEQ(esportsexternalrefund.StatusRegistered),
		esportsexternalrefund.CreatedAtLT(result.Cutoff),
	).Order(ent.Asc(esportsexternalrefund.FieldCreatedAt), ent.Asc(esportsexternalrefund.FieldID)).
		Limit(limit).All(ctx)
	if err != nil {
		return result, err
	}
	result.Scanned = len(rows)
	for _, refund := range rows {
		// 复核一次超期口径：查询条件已按阈值过滤，这里再按同一函数判一遍，
		// 避免驱动返回的时间与阈值口径不一致时误督办未超期单。
		if !externalRefundOverdue(refund, now) {
			continue
		}
		result.Overdue++
		written, err := writeExternalRefundOverdueEvent(ctx, db, refund, now)
		if err != nil {
			return result, err
		}
		if written {
			result.Notified++
			continue
		}
		result.Skipped++
	}
	return result, nil
}

// expirePendingExternalRefundTask 定时任务入口（签名同同包既有任务：func(ctx, logger) error，
// 可直接被 tasks.Register 注册；注册名 esports_expire_pending_external_refunds 与 cron 种子由
// app.go / cron_seeds.go 负责，两者必须成对）。
//
// 只督办、不执行、不取消：把钱留在「等人核验」状态，由财务在退款单上人工执行。
func expirePendingExternalRefundTask(ctx context.Context, logger io.Writer) error {
	result, err := expirePendingExternalRefunds(ctx, global.ORM, time.Now(), externalRefundOverdueScanCap)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(logger,
		"外部退款待执行超期督办：扫描 %d 单，超期 %d 单，本轮督办 %d 单，本小时已督办跳过 %d 单"+
			"（阈值 %d 小时，判定线 %s）\n",
		result.Scanned, result.Overdue, result.Notified, result.Skipped,
		externalRefundOverdueHours, result.Cutoff.Format(time.RFC3339))
	return nil
}

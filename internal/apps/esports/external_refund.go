package esports

import (
	"context"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsexternalrefund"
	"go-admin/internal/ent/esportsorder"
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

// registerExternalRefund 财务登记外部渠道退款凭证：登记渠道 + 流水号 + 金额 + 关联售后/订单，
// 状态 registered。只记账留痕，不改订单退款状态（订单退款状态在执行时对齐）。
// 幂等：同一外部退款流水号只能登记一次（唯一索引兜底）。
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
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsExternalRefund.Get(ctx, refund.ID)
}

// executeExternalRefund 财务核验并执行外部退款：registered → executed，同一事务把订单退款状态对齐：
// 累计已退额累加本次金额、退款状态推导为 partial/full，超退拦截（退款总额不得超过应付额）。
func executeExternalRefund(ctx context.Context, db *ent.Client, adminID, id int, now time.Time) (*ent.EsportsExternalRefund, error) {
	if adminID < 1 || id < 1 {
		return nil, errInvalidInput
	}
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
	order, err := tx.EsportsOrder.Get(ctx, refund.OrderID)
	if err != nil {
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
	changed, err = tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID),
		func(s *sql.Selector) {
			s.Where(sql.ExprP("refunded_cents + ? <= payable_cents", refund.AmountCents))
		},
	).AddRefundedCents(refund.AmountCents).
		SetRefundStatus(refundStatusFor(order.PayableCents, order.RefundedCents+refund.AmountCents)).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
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

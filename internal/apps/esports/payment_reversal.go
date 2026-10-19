package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentreversal"
)

// 处置动作：订单资金异常转人工 / 同时限制客户自助下单 / 认定为误报。
const (
	reversalActionMarkException   = "mark_exception"
	reversalActionSuspendCustomer = "suspend_customer"
	reversalActionIgnore          = "ignore"
)

type paymentReversalInput struct {
	ReceiptID   int    `json:"receipt_id"`
	Kind        string `json:"kind"`
	AmountCents int64  `json:"amount_cents"`
	Reason      string `json:"reason"`
	EvidenceRef string `json:"evidence_ref"`
}

type paymentReversalDecisionInput struct {
	Action     string `json:"action"`
	Resolution string `json:"resolution"`
	RiskReason string `json:"risk_reason"`
}

func paymentReversalNo() string { return "RV" + uuid.NewString() }

// registerPaymentReversal 财务登记已核销收款的渠道撤销 / 拒付（§3.2）：
// 必须指向一笔已核销回执，单笔金额不得超过该回执实际到账额；同一回执可多次登记部分撤销，
// 但累计（未 rejected 的）登记金额不得超过实际到账额。登记只记账与告警，不改订单资金状态；
// 订单如何处置由非登记人的管理员在 decision 中明确。
func registerPaymentReversal(ctx context.Context, db *ent.Client, adminID int,
	in paymentReversalInput, now time.Time) (*ent.EsportsPaymentReversal, error) {
	in.Kind = strings.TrimSpace(in.Kind)
	in.Reason = strings.TrimSpace(in.Reason)
	in.EvidenceRef = strings.TrimSpace(in.EvidenceRef)
	if in.Kind == "" {
		in.Kind = "revoked"
	}
	if adminID < 1 || in.ReceiptID < 1 || in.AmountCents < 1 ||
		(in.Kind != "revoked" && in.Kind != "chargeback") ||
		len([]rune(in.Reason)) < 4 || len([]rune(in.Reason)) > 255 ||
		len([]rune(in.EvidenceRef)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	receipt, err := tx.EsportsPaymentReceipt.Get(ctx, in.ReceiptID)
	if err != nil {
		return nil, err
	}
	// 撤销金额以实际到账为上限：多撤部分没有对应的已核销收款，属于账目错误，直接拦截。
	if in.AmountCents > receipt.ActualCents {
		return nil, fmt.Errorf("%w：撤销金额不得超过该笔收款实际到账额", errInvalidInput)
	}
	// 同一回执允许分多次部分撤销，但累计不得超过实际到账额；误报（rejected）不占用额度。
	prior, err := tx.EsportsPaymentReversal.Query().Where(
		esportspaymentreversal.ReceiptIDEQ(receipt.ID),
		esportspaymentreversal.StatusNEQ(esportspaymentreversal.StatusRejected),
	).Select(esportspaymentreversal.FieldAmountCents).All(ctx)
	if err != nil {
		return nil, err
	}
	var registered int64
	for _, row := range prior {
		registered += row.AmountCents
	}
	if registered+in.AmountCents > receipt.ActualCents {
		return nil, fmt.Errorf("%w：该笔收款累计撤销金额不得超过实际到账额（已登记 %d，本次 %d，实际到账 %d）",
			errConflict, registered, in.AmountCents, receipt.ActualCents)
	}
	intent, err := tx.EsportsPaymentIntent.Get(ctx, receipt.IntentID)
	if err != nil {
		return nil, err
	}
	order, err := tx.EsportsOrder.Get(ctx, intent.OrderID)
	if err != nil {
		return nil, err
	}
	// 存单冻结期间不登记新的渠道撤销 / 拒付：登记会抬升订单版本并留痕，属订单写操作。
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	reversal, err := tx.EsportsPaymentReversal.Create().SetReversalNo(paymentReversalNo()).
		SetReceiptID(receipt.ID).SetOrderID(order.ID).SetChannel(receipt.Channel).
		SetTransactionNo(receipt.TransactionNo).SetAmountCents(in.AmountCents).
		SetKind(esportspaymentreversal.Kind(in.Kind)).SetReason(in.Reason).
		SetEvidenceRef(in.EvidenceRef).SetSubmittedByAdminID(adminID).
		SetStatus(esportspaymentreversal.StatusPending).Save(ctx)
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
		SetOrderVersion(newVersion).SetEventType("order.payment_reversal_registered").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"reversal_no": reversal.ReversalNo, "kind": in.Kind,
			"amount_cents": in.AmountCents, "transaction_no": receipt.TransactionNo,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("payment.reversal_registered:" + reversal.ReversalNo).
		SetEventType("payment.reversal_registered").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"reversal_no": reversal.ReversalNo, "order_no": order.OrderNo,
			"amount_cents": in.AmountCents, "kind": in.Kind,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsPaymentReversal.Get(ctx, reversal.ID)
}

// decidePaymentReversal 处置渠道撤销 / 拒付（§3.2）。登记人不得自审：
// mark_exception 把订单置为资金异常（paid → exception）转人工；
// suspend_customer 在其上再把客户置为受限，阻断客户自助下单；
// ignore 认定为误报，只留结论不动订单。
func decidePaymentReversal(ctx context.Context, db *ent.Client, id, adminID int,
	in paymentReversalDecisionInput, now time.Time) (*ent.EsportsPaymentReversal, error) {
	in.Action = strings.TrimSpace(in.Action)
	in.Resolution = strings.TrimSpace(in.Resolution)
	in.RiskReason = strings.TrimSpace(in.RiskReason)
	if id < 1 || adminID < 1 || len([]rune(in.Resolution)) > 255 ||
		len([]rune(in.RiskReason)) > 255 {
		return nil, errInvalidInput
	}
	switch in.Action {
	case reversalActionMarkException:
		if in.Resolution == "" {
			return nil, fmt.Errorf("%w：请填写处置说明", errInvalidInput)
		}
	case reversalActionSuspendCustomer:
		if in.Resolution == "" || in.RiskReason == "" {
			return nil, fmt.Errorf("%w：限制客户须同时填写处置说明与风控原因", errInvalidInput)
		}
	case reversalActionIgnore:
		// 误报不需要说明。
	default:
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	reversal, err := tx.EsportsPaymentReversal.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if reversal.Status != esportspaymentreversal.StatusPending {
		return nil, errConflict
	}
	if reversal.SubmittedByAdminID != nil && *reversal.SubmittedByAdminID == adminID {
		return nil, errForbidden
	}
	// 先取订单做冻结守卫，再改撤销单与订单资金状态：冻结期间任何处置都不落账。
	order, err := tx.EsportsOrder.Get(ctx, reversal.OrderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	status := esportspaymentreversal.StatusHandled
	if in.Action == reversalActionIgnore {
		status = esportspaymentreversal.StatusRejected
	}
	changed, err := tx.EsportsPaymentReversal.Update().Where(
		esportspaymentreversal.IDEQ(id),
		esportspaymentreversal.StatusEQ(esportspaymentreversal.StatusPending),
		esportspaymentreversal.VersionEQ(reversal.Version),
	).SetStatus(status).SetAction(esportspaymentreversal.Action(in.Action)).
		SetHandledByAdminID(adminID).SetHandledAt(now.UTC()).
		SetResolution(in.Resolution).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if in.Action != reversalActionIgnore {
		// 资金异常：只有仍被当作已收款的订单才需要改判，已异常或未收款的直接跳过。
		// 审核报告 §B.3-4：partial（部分收款）无写入方，真实可改判的状态只有 paid。
		if order.PaymentStatus == esportsorder.PaymentStatusPaid {
			updated, err := tx.EsportsOrder.Update().Where(
				esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
			).SetPaymentStatus(esportsorder.PaymentStatusException).AddVersion(1).Save(ctx)
			if err != nil {
				return nil, err
			}
			if updated != 1 {
				return nil, errConflict
			}
			order, err = tx.EsportsOrder.Get(ctx, order.ID)
			if err != nil {
				return nil, err
			}
		}
		if in.Action == reversalActionSuspendCustomer {
			if _, err = tx.EsportsCustomer.Update().Where(
				esportscustomer.IDEQ(order.CustomerID),
			).SetRiskStatus(esportscustomer.RiskStatusRestricted).SetRiskReason(in.RiskReason).
				SetRiskMarkedAt(now.UTC()).AddVersion(1).Save(ctx); err != nil {
				return nil, err
			}
		}
	}
	newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.payment_reversal_handled").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"reversal_no": reversal.ReversalNo, "action": in.Action,
			"resolution": in.Resolution, "amount_cents": reversal.AmountCents,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsPaymentReversal.Get(ctx, id)
}

// listPaymentReversals 查询渠道撤销 / 拒付事件（§3.2）。orderID 为 0 表示不按订单过滤。
func listPaymentReversals(ctx context.Context, db *ent.Client, orderID int, status string,
	limit int) ([]*ent.EsportsPaymentReversal, error) {
	if orderID < 0 {
		return nil, errInvalidInput
	}
	status = strings.TrimSpace(status)
	switch status {
	case "", esportspaymentreversal.StatusPending.String(),
		esportspaymentreversal.StatusHandled.String(),
		esportspaymentreversal.StatusRejected.String():
	default:
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	query := db.EsportsPaymentReversal.Query()
	if orderID > 0 {
		query = query.Where(esportspaymentreversal.OrderIDEQ(orderID))
	}
	if status != "" {
		query = query.Where(esportspaymentreversal.StatusEQ(esportspaymentreversal.Status(status)))
	}
	return query.Order(ent.Desc(esportspaymentreversal.FieldID)).Limit(limit).All(ctx)
}

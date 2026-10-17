package esports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsidempotency"
	"go-admin/internal/ent/esportsorder"
)

// 补录单一律写成「已成交」的历史单，因此只允许这两个终态：
// completed（历史已完结）/ cancelled（历史已取消）。在途状态不开放，
// 避免补出来的单进入派单 / 服务流程却没有名额占用、支付意图与席位。
const (
	backfillStatusCompleted = "completed"
	backfillStatusCancelled = "cancelled"
	// 补录时间不得早于该年限，避免误填出跨期异常数据。
	backfillMaxAgeYears = 5
)

type backfillOrderInput struct {
	CustomerID int `json:"customer_id"`
	// SkuID 可选：有 SKU 时按 SKU 快照价格 / 服务量；0 表示无 SKU 的历史补录（不绑定 SKU），
	// 此时 unit_price_cents 必填且 >0（§15.6 无 SKU 历史补录走管理员专用入口）。
	SkuID    int   `json:"sku_id"`
	Quantity int64 `json:"quantity"`
	// UnitPriceCents 为实际成交单价；有 SKU 时留 0 表示按 SKU 当前规格价补录，无 SKU 时必填。
	UnitPriceCents int64 `json:"unit_price_cents"`
	// SettledAt 实际成交时间（RFC3339），必填且不得晚于当前时间。
	SettledAt string `json:"settled_at"`
	// FulfillmentStatus 历史履约结果，completed / cancelled，默认 completed。
	FulfillmentStatus string `json:"fulfillment_status"`
	// PaymentStatus 历史收款结果，paid / unpaid，默认 paid；已完结单必须 paid。
	PaymentStatus string `json:"payment_status"`
	Reason        string `json:"reason"`
	// CountCsCommission 显式把补录单计入客服提成；默认 false 即不计提客服提成。
	// 陪玩分成与成长值始终正常计提，不受本开关影响。
	CountCsCommission bool `json:"count_cs_commission"`
}

// orderCountsCsCommission 是否计提客服提成：常规单始终计提；补录单默认不计，仅显式开启才计入（§3.3 / §12.5）。
// 陪玩分成与成长值对补录单也始终正常计提，不再经过该守卫。
func orderCountsCsCommission(order *ent.EsportsOrder) bool {
	if order.EntryPoint != esportsorder.EntryPointAdminBackfill {
		return true
	}
	return order.BackfillCountCommission
}

func backfillRequestHash(in backfillOrderInput) string {
	data, _ := json.Marshal(in)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// parseBackfillSettledAt 解析并校验实际成交时间：必填、不得晚于当前、不得早于 backfillMaxAgeYears 年。
func parseBackfillSettledAt(raw string, now time.Time) (time.Time, error) {
	settled, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w：实际成交时间须为 RFC3339 格式", errInvalidInput)
	}
	settled = settled.UTC()
	if settled.After(now.UTC()) {
		return time.Time{}, fmt.Errorf("%w：实际成交时间不得晚于当前时间", errInvalidInput)
	}
	if settled.Before(now.UTC().AddDate(-backfillMaxAgeYears, 0, 0)) {
		return time.Time{}, fmt.Errorf("%w：实际成交时间不得早于 %d 年前", errInvalidInput, backfillMaxAgeYears)
	}
	return settled, nil
}

// backfillOrder 管理员补录历史订单（§3.3 / §4.1 / §12.5）。隔离规则：
// 1. 入口固定 admin_backfill，必须留补录原因 / 补录人 / 补录时刻，写 order.backfilled 事件与外盒审计；
// 2. 不占 SKU 名额、不创建支付意图与支付回执、不建派单席位（历史单不消耗当期容量）；
// 3. 陪玩分成与成长值默认正常计提；客服提成默认不计，仅 count_cs_commission=true 时按结单口径同事务计提；
// 4. completed 单写入 closed_at 与 close_net_service_cents 快照，保证后续退款冲回口径正确。
func backfillOrder(ctx context.Context, db *ent.Client, adminID int, in backfillOrderInput,
	key string, now time.Time) (*ent.EsportsOrder, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	in.FulfillmentStatus = strings.TrimSpace(in.FulfillmentStatus)
	in.PaymentStatus = strings.TrimSpace(in.PaymentStatus)
	if in.FulfillmentStatus == "" {
		in.FulfillmentStatus = backfillStatusCompleted
	}
	if in.PaymentStatus == "" {
		in.PaymentStatus = esportsorder.PaymentStatusPaid.String()
	}
	if in.CustomerID < 1 || in.SkuID < 0 || in.Quantity < 1 || in.Quantity > 1000 ||
		len(key) < 8 || len(key) > 128 ||
		len([]rune(in.Reason)) < 4 || len([]rune(in.Reason)) > 255 ||
		(in.FulfillmentStatus != backfillStatusCompleted && in.FulfillmentStatus != backfillStatusCancelled) ||
		(in.PaymentStatus != esportsorder.PaymentStatusPaid.String() &&
			in.PaymentStatus != esportsorder.PaymentStatusUnpaid.String()) {
		return nil, errInvalidInput
	}
	settledAt, err := parseBackfillSettledAt(in.SettledAt, now)
	if err != nil {
		return nil, err
	}
	// 已完结 / 已取消的历史单都要有收款记录，否则「已履约但没收钱」会把账算错。
	if in.PaymentStatus != esportsorder.PaymentStatusPaid.String() {
		return nil, fmt.Errorf("%w：历史补录单默认为已收款，未收款请先补录为已取消", errInvalidInput)
	}
	principal := fmt.Sprintf("admin:%d", adminID)
	hash := backfillRequestHash(in)
	if previous, err := existingBackfillOrder(ctx, db, principal, key, hash); err != nil || previous != nil {
		return previous, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	customer, err := tx.EsportsCustomer.Get(ctx, in.CustomerID)
	if err != nil {
		return nil, err
	}
	// 与常规报单同口径的数据边界：客服只能给自己的客户补录。
	if customer.OwnerAdminID == nil || *customer.OwnerAdminID != adminID {
		return nil, errForbidden
	}
	var sku *ent.EsportsSku
	unitPrice := in.UnitPriceCents
	if in.SkuID > 0 {
		sku, err = tx.EsportsSku.Get(ctx, in.SkuID)
		if err != nil {
			return nil, err
		}
		if unitPrice == 0 {
			unitPrice = sku.UnitPriceCents
		}
	}
	if unitPrice < 1 || unitPrice > 100_000_000 {
		return nil, fmt.Errorf("%w：补录成交单价不合法", errInvalidInput)
	}
	payable := unitPrice * in.Quantity
	// 有 SKU 走现有快照逻辑；无 SKU 不绑定 SKU，快照字段给合理默认（服务量按数量 1:1 兜底）。
	skuID := in.SkuID
	minUnitPrice := int64(0)
	adjustmentBp := 2000
	promisedUnits := in.Quantity
	requiredSeats := 1
	requiredLevel := esportsorder.RequiredLevelNovice
	serviceDuration := 0
	if sku != nil {
		minUnitPrice = sku.MinUnitPriceCents
		adjustmentBp = sku.AdjustmentApprovalBp
		promisedUnits = sku.ServiceUnits * in.Quantity
		requiredSeats = sku.RequiredSeats
		requiredLevel = esportsorder.RequiredLevel(sku.RequiredLevel)
		serviceDuration = sku.ServiceDurationMinutes
	}
	orderNo := uuid.NewString()
	creator := tx.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(customer.ID).
		SetSkuID(skuID).SetCreatedByAdminID(adminID).SetResponsibleAdminID(adminID).
		SetChannelSource(esportsorder.ChannelSource(customer.Source.String())).
		SetEntryPoint(esportsorder.EntryPointAdminBackfill).
		SetQuantity(in.Quantity).SetUnitPriceCents(unitPrice).
		SetMinUnitPriceCents(minUnitPrice).SetAdjustmentApprovalBp(adjustmentBp).
		SetPayableCents(payable).SetPromisedServiceUnits(promisedUnits).
		SetRequiredSeats(requiredSeats).SetRequiredLevel(requiredLevel).
		SetServiceDurationMinutes(serviceDuration).
		SetFulfillmentStatus(esportsorder.FulfillmentStatus(in.FulfillmentStatus)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).SetPaidAt(settledAt).
		// 补录单不参与支付时限：写补录时刻，避免历史单立刻被判超时。
		SetPaymentDeadline(now.UTC()).
		SetBackfillReason(in.Reason).SetBackfillCountCommission(in.CountCsCommission).
		SetBackfilledByAdminID(adminID).SetBackfilledAt(now.UTC()).
		// 历史成交时间即订单创建时间，报表按业务发生期归集。
		SetCreatedAt(settledAt)
	if in.FulfillmentStatus == backfillStatusCompleted {
		creator = creator.SetClosedAt(settledAt).SetCloseNetServiceCents(payable)
	}
	order, err := creator.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).SetOrderVersion(1).
		SetEventType("order.backfilled").SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"reason": in.Reason, "settled_at": settledAt, "payable_cents": payable,
			"unit_price_cents": unitPrice, "count_cs_commission": in.CountCsCommission,
			"fulfillment_status": in.FulfillmentStatus,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if in.FulfillmentStatus == backfillStatusCompleted {
		// 陪玩分成与成长值默认正常计提（与普通订单一致，§12.5 / §10.2）。
		if err = accrueOrderCommission(ctx, tx, order, now); err != nil {
			return nil, err
		}
		if err = accrueOrderGrowth(ctx, tx, order, now); err != nil {
			return nil, err
		}
		// 客服提成默认不计入：写「客服提成排除」外盒留痕，便于财务审计与申诉核对；显式开启才计提。
		if in.CountCsCommission {
			if err = accrueOrderCsCommission(ctx, tx, order, now); err != nil {
				return nil, err
			}
		} else {
			if _, err = tx.EsportsOutbox.Create().SetEventKey("order.cs_commission_excluded:" + orderNo).
				SetEventType("order.cs_commission_excluded").SetAggregateRef(orderNo).
				SetPayload(map[string]interface{}{
					"order_no": orderNo, "reason": "admin_backfill",
				}).Save(ctx); err != nil {
				return nil, err
			}
		}
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("order.backfilled:" + orderNo).
		SetEventType("order.backfilled").SetAggregateRef(orderNo).
		SetPayload(map[string]interface{}{
			"order_no": orderNo, "customer_id": customer.ID, "payable_cents": payable,
			"settled_at": settledAt, "count_cs_commission": in.CountCsCommission,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsIdempotency.Create().SetOperation("backfill_order").SetPrincipal(principal).
		SetRequestKey(key).SetRequestHash(hash).SetResultRef(orderNo).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, order.ID)
}

// existingBackfillOrder 幂等命中时返回既有补录单；请求体不一致视为冲突。
func existingBackfillOrder(ctx context.Context, db *ent.Client, principal, key, hash string) (*ent.EsportsOrder, error) {
	record, err := db.EsportsIdempotency.Query().Where(
		esportsidempotency.OperationEQ("backfill_order"),
		esportsidempotency.PrincipalEQ(principal),
		esportsidempotency.RequestKeyEQ(key),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if record.RequestHash != hash {
		return nil, errConflict
	}
	return db.EsportsOrder.Query().Where(esportsorder.OrderNoEQ(record.ResultRef)).Only(ctx)
}

// backfillOrderView 补录结果视图：把「是否计入客服提成」的结论一并返回，便于前端明确提示。
func backfillOrderView(order *ent.EsportsOrder) map[string]any {
	return map[string]any{
		"order": order, "counted_cs_commission": orderCountsCsCommission(order),
	}
}

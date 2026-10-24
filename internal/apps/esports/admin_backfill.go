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
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsidempotency"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsservicesegment"
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
	// 此时 unit_price_cents 必填且 >0（无 SKU 历史补录走管理员专用入口）。
	SkuID    int   `json:"product_id"`
	Quantity int64 `json:"quantity"`
	// UnitPriceCents 为实际成交单价；有 SKU 时留 0 表示按 SKU 当前规格价补录，无 SKU 时必填。
	UnitPriceCents int64 `json:"unit_price_cents"`
	// SettledAt 实际成交时间（RFC3339），必填且不得晚于当前时间。
	SettledAt string `json:"settled_at"`
	// FulfillmentStatus 历史履约结果，completed / cancelled，默认 completed。
	FulfillmentStatus string `json:"fulfillment_status"`
	// PaymentStatus 历史收款结果，默认 paid；补录单一律按已收款登记，
	// unpaid 会被拒绝（未收款的历史单应补录为 cancelled）。
	PaymentStatus string `json:"payment_status"`
	Reason        string `json:"reason"`
	// CountCsCommission 显式把补录单计入客服提成；默认 false 即不计提客服提成。
	// 陪玩分成与成长值始终正常计提，不受本开关影响；已取消单不允许开启（见 backfillOrder 校验）。
	// 字段名与管理端提交的 count_cs_commission 一一对应。
	CountCsCommission bool `json:"count_cs_commission"`
	// PlayerIDs 补录单的到场陪玩：可指定 1..required_seats 名陪玩，补录时按他们生成
	// 派单席位与服务段骨架，使陪玩分成能正常计提。留空表示不登记成员（历史单没有陪玩记录）。
	PlayerIDs []int `json:"player_ids,omitempty"`
	// LegacyCountCommission 是已废弃的字段名（旧管理端曾提交 count_commission）。
	// 指针只用于识别「字段是否出现」：一旦出现即返回参数错误，绝不再静默当成 false ——
	// 否则前后端字段名错配会变成「开关点了没用」的隐性财务错误。
	LegacyCountCommission *bool `json:"count_commission,omitempty"`
}

// orderCountsCsCommission 是否计提客服提成：常规单始终计提；补录单默认不计，仅显式开启才计入。
// 陪玩分成与成长值对补录单也始终正常计提，不再经过该守卫。
func orderCountsCsCommission(order *ent.EsportsOrder) bool {
	if order.EntryPoint != esportsorder.EntryPointAdminBackfill {
		return true
	}
	return order.BackfillCountCommission
}

func backfillRequestHash(in backfillOrderInput) string {
	// 废弃字段不参与哈希：它在入口即被拒绝，不属于「同一逻辑请求」的组成部分。
	in.LegacyCountCommission = nil
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

// backfillOrder 非超管口径的补录入口：等价于 isSuper=false。仅供服务内部与非超管用例调用；
// HTTP 入口必须走 backfillOrderWithRole，把登录用户的超管标识透传下来（P0-13）。
func backfillOrder(ctx context.Context, db *ent.Client, adminID int, in backfillOrderInput,
	key string, now time.Time) (*ent.EsportsOrder, error) {
	return backfillOrderWithRole(ctx, db, adminID, false, in, key, now)
}

// backfillOrderWithRole 管理员补录历史订单，isSuper 表示当前操作人是否超管。
// 隔离规则：
//  1. 入口固定 admin_backfill，必须留补录原因 / 补录人 / 补录时刻，写 order.backfilled 事件与外盒审计；
//  2. 不占 SKU 名额、不创建支付意图与支付回执、不建派单席位（历史单不消耗当期容量）；
//  3. 陪玩分成与成长值默认正常计提；客服提成默认不计，仅 count_cs_commission=true 时按结单口径同事务计提；
//     已取消单不计提任何提成（不写结单快照、不分成分成），也不接受 count_cs_commission=true；
//  4. completed 单写入 closed_at 与 close_net_service_cents 快照，保证后续退款冲回口径正确；
//  5. 数据边界：非超管只能为本人负责的客户补录；超管可为任意客户补录 —— 这正是「历史订单补录」
//     的功能定位。跨客户补录仍按「客户归属客服」记责任客服，且事件 / 外盒都记录操作人与归属差异。
func backfillOrderWithRole(ctx context.Context, db *ent.Client, adminID int, isSuper bool,
	in backfillOrderInput, key string, now time.Time) (*ent.EsportsOrder, error) {
	// 旧字段名出现即拒绝：宁可让旧页面提交失败并提示刷新，也不能把它静默当成 false 计提错误。
	if in.LegacyCountCommission != nil {
		return nil, fmt.Errorf("%w：字段 count_commission 已更名为 count_cs_commission，请刷新页面后重试", errInvalidInput)
	}
	in.Reason = strings.TrimSpace(in.Reason)
	in.FulfillmentStatus = strings.TrimSpace(in.FulfillmentStatus)
	in.PaymentStatus = strings.TrimSpace(in.PaymentStatus)
	if in.FulfillmentStatus == "" {
		in.FulfillmentStatus = backfillStatusCompleted
	}
	if in.PaymentStatus == "" {
		in.PaymentStatus = esportsorder.PaymentStatusPaid.String()
	}
	// 补录属高危写操作：补录原因必填（4–255 字），缺失时给出明确中文提示而不是笼统的参数错误。
	if n := len([]rune(in.Reason)); n < 4 || n > 255 {
		return nil, fmt.Errorf("%w：补录原因必填，长度须为 4–255 个字符", errInvalidInput)
	}
	if in.CustomerID < 1 || in.SkuID < 0 || in.Quantity < 1 || in.Quantity > 1000 ||
		len(key) < 8 || len(key) > 128 ||
		(in.FulfillmentStatus != backfillStatusCompleted && in.FulfillmentStatus != backfillStatusCancelled) ||
		(in.PaymentStatus != esportsorder.PaymentStatusPaid.String() &&
			in.PaymentStatus != esportsorder.PaymentStatusUnpaid.String()) {
		return nil, errInvalidInput
	}
	// 已取消的历史单不产生任何提成与结单快照；此时再要求计入客服提成属于口径冲突，
	// 直接拒绝而不是静默忽略，避免管理员以为开关生效、实际未计提。
	if in.FulfillmentStatus == backfillStatusCancelled && in.CountCsCommission {
		return nil, fmt.Errorf("%w：已取消的历史补录单不计提任何提成，不能计入客服提成", errInvalidInput)
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
	// 数据边界：非超管只能给自己的客户补录；超管可为任意客户补录（「历史订单补录」的功能定位），
	// 因此只在非超管时校验客户归属，而不是统一硬校验。
	crossOwner := customer.OwnerAdminID == nil || *customer.OwnerAdminID != adminID
	if !isSuper && crossOwner {
		return nil, errForbidden
	}
	// 补录单的责任客服始终取「客户归属客服」：否则超管代补录他人客户时，业绩归属、客服提成计提对象
	// 与数据域可见性都会错算到补录人名下。客户无归属时才回落为操作人。
	responsibleAdminID := adminID
	if customer.OwnerAdminID != nil {
		responsibleAdminID = *customer.OwnerAdminID
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
	requiredLevel := skillLevelLowest(skillLevels(ctx, db))
	serviceDuration := 0
	if sku != nil {
		minUnitPrice = sku.MinUnitPriceCents
		adjustmentBp = sku.AdjustmentApprovalBp
		promisedUnits = sku.ServiceUnits * in.Quantity
		requiredSeats = sku.RequiredSeats
		requiredLevel = sku.RequiredLevel
		serviceDuration = sku.ServiceDurationMinutes
	}
	orderNo := uuid.NewString()
	creator := tx.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(customer.ID).
		SetSkuID(skuID).SetCreatedByAdminID(adminID).SetResponsibleAdminID(responsibleAdminID).
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
			// ：记录按哪几名陪玩生成席位 / 服务段骨架，供分成核对。
			"player_ids": in.PlayerIDs,
			// 高危写操作留痕：操作人、被补录客户、责任客服，以及本次是否为「超管代补录他人客户」。
			"operator_admin_id":       adminID,
			"customer_id":             customer.ID,
			"customer_owner_admin_id": customer.OwnerAdminID,
			"responsible_admin_id":    responsibleAdminID,
			"is_super_admin":          isSuper,
			"cross_owner":             crossOwner,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if in.FulfillmentStatus == backfillStatusCompleted {
		// ：按指定陪玩生成席位 / 服务段骨架，使陪玩分成可以正常计提。
		if err = createBackfillMembers(ctx, tx, order, in.PlayerIDs, requiredSeats, promisedUnits,
			settledAt, adminID); err != nil {
			return nil, err
		}
		// 陪玩分成与成长值默认正常计提（与普通订单一致，）。
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
			// 跨客户补录的审计要素：谁补的、客户归谁、是否超管放行，便于事后追责。
			"operator_admin_id":    adminID,
			"responsible_admin_id": responsibleAdminID,
			"is_super_admin":       isSuper,
			"cross_owner":          crossOwner,
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

// createBackfillMembers 为补录单按指定陪玩生成席位与服务段骨架。
// 补录单不占当期 SKU 名额（历史单不消耗容量），但必须有成员才能计提陪玩分成；
// 因此只写「已接受席位 + 已结束服务段」的历史事实，不进派单池、不发通知。
// 未指定陪玩时不做任何事（保持既有「历史单无成员」的口径）。
func createBackfillMembers(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder,
	playerIDs []int, requiredSeats int, promisedUnits int64, settledAt time.Time, adminID int) error {
	if len(playerIDs) == 0 {
		return nil
	}
	if requiredSeats < 1 {
		requiredSeats = 1
	}
	if len(playerIDs) > requiredSeats {
		return fmt.Errorf("%w：补录单最多登记 %d 名陪玩（与席位数一致），当前 %d 名",
			errInvalidInput, requiredSeats, len(playerIDs))
	}
	seen := make(map[int]struct{}, len(playerIDs))
	unitsPerSeat := int64(0)
	if len(playerIDs) > 0 && promisedUnits > 0 {
		unitsPerSeat = promisedUnits / int64(len(playerIDs))
	}
	for i, playerID := range playerIDs {
		if playerID < 1 {
			return fmt.Errorf("%w：补录单登记的陪玩不合法", errInvalidInput)
		}
		if _, dup := seen[playerID]; dup {
			return fmt.Errorf("%w：补录单登记的陪玩重复", errInvalidInput)
		}
		seen[playerID] = struct{}{}
		if _, err := tx.EsportsPlayer.Get(ctx, playerID); err != nil {
			return fmt.Errorf("%w：补录单登记的陪玩 %d 不存在", errInvalidInput, playerID)
		}
		seatIndex := i + 1
		if _, err := tx.EsportsDispatchSeat.Create().SetOrderID(order.ID).SetSeatIndex(seatIndex).
			SetPlayerID(playerID).SetSource(esportsdispatchseat.SourceAssign).
			SetStatus(esportsdispatchseat.StatusAccepted).SetRespondedAt(settledAt).
			SetInvitedByAdminID(adminID).Save(ctx); err != nil {
			return err
		}
		if _, err := tx.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(playerID).
			SetSeatIndex(seatIndex).SetStatus(esportsservicesegment.StatusFinished).
			SetStartedAt(settledAt).SetEndedAt(settledAt).
			SetReportedUnits(unitsPerSeat).SetSystemSeconds(0).
			SetEvidenceRef("backfill:" + order.OrderNo).Save(ctx); err != nil {
			return err
		}
	}
	// 成员登记明细并入既有 order.backfilled 事件（同订单同版本只能有一条事件）。
	return nil
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

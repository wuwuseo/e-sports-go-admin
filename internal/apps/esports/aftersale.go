package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportswallettx"
)

// errExecutionFailed 标记"售后方案执行失败"（区别于可重试的并发冲突 / 配置缺失），
// 触发回滚后把售后单落为 execution_failed。
var errExecutionFailed = errors.New("售后执行失败")

var (
	// 外部渠道退款未接入：仅支持原订单以客户余额核销的退款。
	errRefundChannelUnavailable = fmt.Errorf("%w：外部渠道退款未接入，仅支持原订单余额核销退回", errNotConfigured)
)

type afterSaleInput struct {
	SourceType   string `json:"source_type"`
	SourceID     int    `json:"source_id"`
	Kind         string `json:"kind"`
	Channel      string `json:"channel"`
	Demand       string `json:"demand"`
	ExpectedPlan string `json:"expected_plan"`
	EvidenceRef  string `json:"evidence_ref"`
	Contact      string `json:"contact"`
	CustomerID   *int   `json:"customer_id"`
}

type afterSalePlanInput struct {
	FaultParty  string `json:"fault_party"`
	PlanAction  string `json:"plan_action"`
	RefundCents int64  `json:"refund_cents"`
	Resolution  string `json:"resolution"`
	// 补履约方案参数（§13.6）：plan_action=makeup_service 时必填，其余动作应留空。
	MakeupKind            string `json:"makeup_kind"`
	MakeupPlayerID        int    `json:"makeup_player_id"`
	MakeupSeatIndex       int    `json:"makeup_seat_index"`
	MakeupUnits           int64  `json:"makeup_units"`
	MakeupCompanyPayCents int64  `json:"makeup_company_pay_cents"`
}

type afterSaleDecisionInput struct {
	Reason string `json:"reason"`
}

// afterSaleNo 生成售后单号，前缀 AS，仿订单号生成器。
func afterSaleNo() string { return "AS" + uuid.NewString() }

func validAfterSaleKind(kind string) bool {
	switch kind {
	case "service_mismatch", "duration_short", "no_show", "wrong_item",
		"overcharge", "player_appeal", "refund_request", "other":
		return true
	}
	return false
}

func validAfterSaleChannel(channel string) bool {
	switch channel {
	case "customer", "admin", "player", "system":
		return true
	}
	return false
}

func validFaultParty(party string) bool {
	switch party {
	case "undetermined", "player", "admin", "customer", "platform", "none":
		return true
	}
	return false
}

func validPlanAction(action string) bool {
	switch action {
	case "none", "refund_original", "compensate", "makeup_service":
		return true
	}
	return false
}

func validAfterSaleStatus(status string) bool {
	switch status {
	case "pending_accept", "processing", "pending_customer_confirm", "pending_approval",
		"pending_execution", "execution_failed", "completed", "rejected", "withdrawn":
		return true
	}
	return false
}

// activeAfterSaleStatuses 进行中状态集合，用于"同来源同问题只保留一张进行中售后"的查重。
func activeAfterSaleStatuses() []esportsaftersale.Status {
	return []esportsaftersale.Status{
		esportsaftersale.StatusPendingAccept,
		esportsaftersale.StatusProcessing,
		esportsaftersale.StatusPendingCustomerConfirm,
		esportsaftersale.StatusPendingApproval,
		esportsaftersale.StatusPendingExecution,
		esportsaftersale.StatusExecutionFailed,
	}
}

// createAfterSale 受理售后申请：校验来源与枚举、订单存在性、同来源同问题查重，
// 创建后占用一个订单版本写 order.after_sale_created 事件留痕。
func createAfterSale(ctx context.Context, db *ent.Client, in afterSaleInput, operatorID int, now time.Time) (*ent.EsportsAfterSale, error) {
	in.SourceType = strings.TrimSpace(in.SourceType)
	in.Demand = strings.TrimSpace(in.Demand)
	in.ExpectedPlan = strings.TrimSpace(in.ExpectedPlan)
	in.EvidenceRef = strings.TrimSpace(in.EvidenceRef)
	in.Contact = strings.TrimSpace(in.Contact)
	if in.SourceType == "" {
		in.SourceType = "order"
	}
	if in.SourceType != "order" || in.SourceID < 1 ||
		!validAfterSaleKind(in.Kind) || !validAfterSaleChannel(in.Channel) ||
		in.Demand == "" || len([]rune(in.Demand)) > 500 ||
		len([]rune(in.ExpectedPlan)) > 255 || len([]rune(in.EvidenceRef)) > 255 ||
		len([]rune(in.Contact)) > 120 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, in.SourceID)
	if err != nil {
		return nil, err
	}
	duplicated, err := tx.EsportsAfterSale.Query().Where(
		esportsaftersale.SourceTypeEQ(esportsaftersale.SourceTypeOrder),
		esportsaftersale.SourceIDEQ(in.SourceID),
		esportsaftersale.KindEQ(esportsaftersale.Kind(in.Kind)),
		esportsaftersale.StatusIn(activeAfterSaleStatuses()...),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if duplicated {
		return nil, errConflict
	}
	create := tx.EsportsAfterSale.Create().SetAfterSaleNo(afterSaleNo()).
		SetSourceType(esportsaftersale.SourceTypeOrder).SetSourceID(in.SourceID).
		SetKind(esportsaftersale.Kind(in.Kind)).SetChannel(esportsaftersale.Channel(in.Channel)).
		SetDemand(in.Demand).SetExpectedPlan(in.ExpectedPlan).SetEvidenceRef(in.EvidenceRef).
		SetContact(in.Contact).SetStatus(esportsaftersale.StatusPendingAccept)
	if in.CustomerID != nil {
		create.SetCustomerID(*in.CustomerID)
	}
	item, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	// 写订单事件需要占用一个订单版本，用 bumpOrder 抬一版承载事件（不改履约状态）。
	newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.after_sale_created").
		SetActorType("admin").SetActorID(strconv.Itoa(operatorID)).
		SetDetails(map[string]interface{}{
			"after_sale_no": item.AfterSaleNo, "kind": item.Kind.String(),
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSale.Get(ctx, item.ID)
}

// listAfterSales 列出售后单，status 为空则返回全部，按 ID 倒序。
func listAfterSales(ctx context.Context, db *ent.Client, status string, limit int) ([]*ent.EsportsAfterSale, error) {
	status = strings.TrimSpace(status)
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsAfterSale.Query()
	if status != "" {
		if !validAfterSaleStatus(status) {
			return nil, errInvalidInput
		}
		q.Where(esportsaftersale.StatusEQ(esportsaftersale.Status(status)))
	}
	return q.Order(ent.Desc(esportsaftersale.FieldID)).Limit(limit).All(ctx)
}

// getAfterSale 按 ID 读取售后单。
func getAfterSale(ctx context.Context, db *ent.Client, id int) (*ent.EsportsAfterSale, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// acceptAfterSale 受理：pending_accept → processing，记录处理客服。
func acceptAfterSale(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsAfterSale, error) {
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(esportsaftersale.StatusPendingAccept),
	).SetStatus(esportsaftersale.StatusProcessing).SetHandlerAdminID(adminID).
		SetAcceptedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// submitAfterSalePlan 提交处理方案：processing → pending_customer_confirm（§13.3 方案提交 → 客户确认 → 审批 → 执行）。
// 方案动作项首版为 退原核销款 / 发放补偿权益 / 补钟或换人履约（§13.6）。
// 补履约方案在提交时校验承接陪玩、席位与补足量；其余动作清空补履约参数，避免脏数据。
// 每次提交都会清空上一轮的客户确认 / 拒绝留痕，确保「待客户确认」节点重新走一遍。
func submitAfterSalePlan(ctx context.Context, db *ent.Client, id, adminID int, in afterSalePlanInput, now time.Time) (*ent.EsportsAfterSale, error) {
	in.Resolution = strings.TrimSpace(in.Resolution)
	if id < 1 || !validFaultParty(in.FaultParty) || !validPlanAction(in.PlanAction) ||
		in.RefundCents < 0 || len([]rune(in.Resolution)) > 255 {
		return nil, errInvalidInput
	}
	makeupKind := esportsaftersale.MakeupKindNone
	refundCents := in.RefundCents
	var makeupPlayerID, makeupSeatIndex *int
	if in.PlanAction == "makeup_service" {
		item, err := db.EsportsAfterSale.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if item.Status != esportsaftersale.StatusProcessing {
			return nil, errConflict
		}
		order, err := db.EsportsOrder.Get(ctx, item.SourceID)
		if err != nil {
			return nil, err
		}
		if err = validateMakeupPlan(ctx, db, order, in); err != nil {
			return nil, err
		}
		makeupKind = esportsaftersale.MakeupKind(in.MakeupKind)
		playerID, seatIndex := in.MakeupPlayerID, in.MakeupSeatIndex
		makeupPlayerID, makeupSeatIndex = &playerID, &seatIndex
		// 补履约不产生退款金额。
		refundCents = 0
	}
	update := db.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(esportsaftersale.StatusProcessing),
	).SetFaultParty(esportsaftersale.FaultParty(in.FaultParty)).
		SetPlanAction(esportsaftersale.PlanAction(in.PlanAction)).
		SetRefundCents(refundCents).
		SetMakeupKind(makeupKind).
		SetMakeupUnits(in.MakeupUnits).
		SetMakeupCompanyPayCents(in.MakeupCompanyPayCents).
		SetResolution(in.Resolution).
		SetPlannedAt(now.UTC()).
		SetStatus(esportsaftersale.StatusPendingCustomerConfirm).AddVersion(1).
		ClearCustomerConfirmedAt().ClearCustomerConfirmedByAdminID().ClearCustomerDeclinedAt().
		ClearApprovedAt().
		SetCustomerConfirmChannel(esportsaftersale.CustomerConfirmChannelNone).
		SetCustomerDeclineReason("")
	if makeupPlayerID != nil && makeupSeatIndex != nil {
		update = update.SetMakeupPlayerID(*makeupPlayerID).SetMakeupSeatIndex(*makeupSeatIndex)
	} else {
		update = update.ClearMakeupPlayerID().ClearMakeupSeatIndex()
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// confirmAfterSalePlanIn 客户确认方案：pending_customer_confirm → pending_approval（§13.3）。
// channel 为 self_service（客户自助）或 admin_proxy（客服代确认，客户线下已同意）。
func confirmAfterSalePlanIn(ctx context.Context, db *ent.Client, id, adminID int,
	channel string, now time.Time) (*ent.EsportsAfterSale, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	update := db.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(esportsaftersale.StatusPendingCustomerConfirm),
	).SetStatus(esportsaftersale.StatusPendingApproval).
		SetCustomerConfirmedAt(now.UTC()).
		SetCustomerConfirmChannel(esportsaftersale.CustomerConfirmChannel(channel)).
		SetCustomerDeclineReason("").AddVersion(1)
	if channel == "admin_proxy" {
		if adminID < 1 {
			return nil, errInvalidInput
		}
		update = update.SetCustomerConfirmedByAdminID(adminID)
	} else {
		update = update.ClearCustomerConfirmedByAdminID()
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// confirmAfterSaleByCustomer 客户自助确认方案（§13.3）：须为售后单归属客户本人。
func confirmAfterSaleByCustomer(ctx context.Context, db *ent.Client, id, customerID int,
	now time.Time) (*ent.EsportsAfterSale, error) {
	if id < 1 || customerID < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.CustomerID == nil || *item.CustomerID != customerID {
		return nil, errForbidden
	}
	return confirmAfterSalePlanIn(ctx, db, id, 0, "self_service", now)
}

// confirmAfterSaleByAdmin 客服代客户确认方案（客户线下已同意）：同样进入待审批，并记录代确认人。
func confirmAfterSaleByAdmin(ctx context.Context, db *ent.Client, id, adminID int,
	now time.Time) (*ent.EsportsAfterSale, error) {
	return confirmAfterSalePlanIn(ctx, db, id, adminID, "admin_proxy", now)
}

// declineAfterSaleByCustomer 客户拒绝方案（§13.3）：pending_customer_confirm → processing，
// 客服可修改方案后重新提交（重新提交会再次进入待客户确认）。须填原因且为归属客户本人。
func declineAfterSaleByCustomer(ctx context.Context, db *ent.Client, id, customerID int,
	reason string, now time.Time) (*ent.EsportsAfterSale, error) {
	reason = strings.TrimSpace(reason)
	if id < 1 || customerID < 1 || reason == "" || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.CustomerID == nil || *item.CustomerID != customerID {
		return nil, errForbidden
	}
	changed, err := db.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(esportsaftersale.StatusPendingCustomerConfirm),
	).SetStatus(esportsaftersale.StatusProcessing).
		SetCustomerDeclinedAt(now.UTC()).SetCustomerDeclineReason(reason).
		SetCustomerConfirmChannel(esportsaftersale.CustomerConfirmChannelNone).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// approveAfterSale 审批：pending_approval → pending_execution。
// 不得自审（审批人不得为该单处理人 / 方案提交人）；同时在同一事务内按条件表达式预占订单退款额度，
// 即 refunded_cents + refund_reserved_cents + 本次金额 <= payable_cents，超退或并发冲突均 errConflict。
func approveAfterSale(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsAfterSale, error) {
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsaftersale.StatusPendingApproval {
		return nil, errConflict
	}
	if item.HandlerAdminID != nil && *item.HandlerAdminID == adminID {
		return nil, errForbidden
	}
	if item.RefundCents > 0 {
		changed, err := tx.EsportsOrder.Update().Where(
			esportsorder.IDEQ(item.SourceID),
			func(s *sql.Selector) {
				s.Where(sql.ExprP("refunded_cents + refund_reserved_cents + ? <= payable_cents", item.RefundCents))
			},
		).AddRefundReservedCents(item.RefundCents).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
	}
	changed, err := tx.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(esportsaftersale.StatusPendingApproval),
	).SetStatus(esportsaftersale.StatusPendingExecution).SetApproverAdminID(adminID).
		SetApprovedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// rejectAfterSale 驳回：pending_approval → rejected，记录审批人与驳回原因后终态。
func rejectAfterSale(ctx context.Context, db *ent.Client, id, adminID int, in afterSaleDecisionInput, now time.Time) (*ent.EsportsAfterSale, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	if id < 1 || adminID < 1 || in.Reason == "" || len([]rune(in.Reason)) > 255 {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(esportsaftersale.StatusPendingApproval),
	).SetStatus(esportsaftersale.StatusRejected).SetApproverAdminID(adminID).
		SetResolution(in.Reason).SetResolvedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// withdrawAfterSale 撤回：非终态（completed/rejected/withdrawn 之外）→ withdrawn。
// 若已审批进入 pending_execution，必须原路释放预占的退款额度，否则该订单的退款上限会被永久占住。
func withdrawAfterSale(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsAfterSale, error) {
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status == esportsaftersale.StatusCompleted || item.Status == esportsaftersale.StatusRejected ||
		item.Status == esportsaftersale.StatusWithdrawn {
		return nil, errConflict
	}
	if item.Status == esportsaftersale.StatusPendingExecution && item.RefundCents > 0 {
		released, err := tx.EsportsOrder.Update().Where(
			esportsorder.IDEQ(item.SourceID),
			func(s *sql.Selector) {
				s.Where(sql.ExprP("refund_reserved_cents >= ?", item.RefundCents))
			},
		).AddRefundReservedCents(-item.RefundCents).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if released != 1 {
			return nil, errConflict
		}
	}
	changed, err := tx.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(item.Status),
	).SetStatus(esportsaftersale.StatusWithdrawn).SetResolvedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// executeAfterSale 执行售后方案：pending_execution → completed。
// 钱包流水写入与订单退款额度扣减必须在同一事务，任一失败整体回滚；
// 真正的执行失败（非并发 / 配置类）回滚后单独把售后单落为 execution_failed，且不吞错。
func executeAfterSale(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsAfterSale, error) {
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	item, err := executeAfterSaleOnce(ctx, db, id, adminID, now)
	if err != nil && errors.Is(err, errExecutionFailed) {
		if _, markErr := db.EsportsAfterSale.Update().Where(
			esportsaftersale.IDEQ(id),
			esportsaftersale.StatusEQ(esportsaftersale.StatusPendingExecution),
		).SetStatus(esportsaftersale.StatusExecutionFailed).
			SetResolution(err.Error()).AddVersion(1).Save(ctx); markErr != nil {
			return nil, markErr
		}
		return nil, err
	}
	return item, err
}

func executeAfterSaleOnce(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsAfterSale, error) {
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
	order, err := tx.EsportsOrder.Get(ctx, item.SourceID)
	if err != nil {
		return nil, err
	}
	switch item.PlanAction {
	case esportsaftersale.PlanActionRefundOriginal:
		if item.RefundCents <= 0 {
			return nil, errInvalidInput
		}
		if err = executeRefundOriginal(ctx, tx, item, order, adminID); err != nil {
			return nil, err
		}
	case esportsaftersale.PlanActionCompensate:
		if item.RefundCents <= 0 {
			return nil, errInvalidInput
		}
		if err = executeCompensate(ctx, tx, item, order, adminID); err != nil {
			return nil, err
		}
	case esportsaftersale.PlanActionMakeupService:
		// 补履约（§13.6）：创建补履约任务；不退款、不冲回分成与成长值，也不改原订单实收与成交额。
		task, err := createFulfillmentMakeupIn(ctx, tx, item, order, adminID, now)
		if err != nil {
			return nil, err
		}
		newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
		if err != nil {
			return nil, err
		}
		if err = completeAfterSaleExecution(ctx, tx, id, order.ID, adminID, newVersion, "order.makeup_created",
			map[string]interface{}{
				"after_sale_no": item.AfterSaleNo, "makeup_no": task.MakeupNo,
				"makeup_kind": task.Kind.String(), "player_id": task.PlayerID,
				"seat_index": task.SeatIndex, "units": task.Units,
				"company_pay_cents": task.CompanyPayCents,
			}, now); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return db.EsportsAfterSale.Get(ctx, id)
	default:
		return nil, errInvalidInput
	}
	// 统一处理：凡 refund_cents>0 的已完成赔付，都从预占额度扣减并累加 refunded_cents。
	// 条件表达式再次校验预占充足，避免并发超退；refund_status 视累计已退额一并更新。
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID),
		func(s *sql.Selector) {
			s.Where(sql.ExprP("refund_reserved_cents >= ?", item.RefundCents))
		},
	).AddRefundReservedCents(-item.RefundCents).AddRefundedCents(item.RefundCents).
		SetRefundStatus(refundStatusFor(order.PayableCents, order.RefundedCents+item.RefundCents)).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	// 退款额度落账后、同事务按比例冲回陪玩分成（新增 reversal 负数明细，幂等靠 idem_key 唯一索引）。
	if err = reverseOrderCommission(ctx, tx, order, item, now); err != nil {
		return nil, err
	}
	// 同事务冲回客服提成（§12.2）：结单后部分退款按比例冲回，全额退款冲至零。
	if err = reverseOrderCsCommission(ctx, tx, order, item, now); err != nil {
		return nil, err
	}
	// 退款额度落账后、同事务按退款金额冲回客户成长值并重算等级（§10.2/§10.5）。
	if err = reverseOrderGrowth(ctx, tx, order, item, adminID, now); err != nil {
		return nil, err
	}
	newVersion := order.Version + 1
	if err = completeAfterSaleExecution(ctx, tx, id, order.ID, adminID, newVersion, "order.refund_executed",
		map[string]interface{}{
			"after_sale_no": item.AfterSaleNo, "plan_action": item.PlanAction.String(),
			"refund_cents": item.RefundCents,
		}, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// completeAfterSaleExecution 事务内把售后单置为已完成并写订单事件（§13.6 执行结果留痕）。
// 退款类动作与补履约动作共用：两者都在同一事务内完成状态流转与留痕。
func completeAfterSaleExecution(ctx context.Context, tx *ent.Tx, id, orderID, adminID, newVersion int,
	eventType string, details map[string]interface{}, now time.Time) error {
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
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType(eventType).
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(details).Save(ctx); err != nil {
		return err
	}
	return nil
}

// refundStatusFor 按累计已退额与应付额推导退款状态：退满为 full，否则 partial。
func refundStatusFor(payableCents, refundedCents int64) esportsorder.RefundStatus {
	if refundedCents >= payableCents {
		return esportsorder.RefundStatusFull
	}
	return esportsorder.RefundStatusPartial
}

// executeRefundOriginal 退原核销款：支持原订单以客户余额或储值卡核销的退款。
// 查原核销流水，先本金后赠送还原本金 / 赠送构成，按原账户「原路退回」写一条 refund_return 流水；
// 幂等靠 (source_type, source_id, entry_type) 唯一索引兜底。
// 原路退回是储值卡的关键约束：订单退款把预付款还回原卡，与「退卡」是两层独立资金动作（§4.4）。
func executeRefundOriginal(ctx context.Context, tx *ent.Tx, item *ent.EsportsAfterSale, order *ent.EsportsOrder, adminID int) error {
	consume, err := tx.EsportsWalletTx.Query().Where(
		esportswallettx.SourceTypeEQ("order"),
		esportswallettx.SourceIDEQ(order.OrderNo),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeConsume),
	).Order(ent.Desc(esportswallettx.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return errRefundChannelUnavailable
	}
	if err != nil {
		return err
	}
	// 先本金后赠送，与原扣减「先赠送后本金」互为镜像。
	principalBack := min(item.RefundCents, -consume.PrincipalDeltaCents)
	if principalBack < 0 {
		principalBack = 0
	}
	bonusBack := item.RefundCents - principalBack
	if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(consume.AccountType).SetOwnerID(consume.OwnerID).
		SetEntryType(esportswallettx.EntryTypeRefundReturn).
		SetPrincipalDeltaCents(principalBack).SetBonusDeltaCents(bonusBack).
		SetSourceType("after_sale").SetSourceID(item.AfterSaleNo).
		SetOperatorID(adminID).SetNote("售后退款").Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errConflict
		}
		return fmt.Errorf("%w：退款流水写入失败：%v", errExecutionFailed, err)
	}
	// 储值卡订单退款冲回对应预付款批次，保持批次账本与卡片余额一致。
	if consume.AccountType == esportswallettx.AccountTypeStoredValueCard {
		if _, err = tx.EsportsLedgerEntry.Create().
			SetAccountType(storedValuePrepayAccount).SetAccountID(strconv.Itoa(consume.OwnerID)).
			SetSourceType("after_sale").SetSourceID(item.AfterSaleNo).
			SetEntryRole("card_refund_return").SetDeltaCents(item.RefundCents).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return errConflict
			}
			return fmt.Errorf("%w：储值卡退款批次写入失败：%v", errExecutionFailed, err)
		}
	}
	return nil
}

// executeCompensate 发放补偿权益：写一条赠送流水（entry_type=adjust）。
// 补偿权益与退原核销款的差异只在钱包科目，订单额度扣减走统一处理。
func executeCompensate(ctx context.Context, tx *ent.Tx, item *ent.EsportsAfterSale, order *ent.EsportsOrder, adminID int) error {
	if _, err := tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(order.CustomerID).
		SetEntryType(esportswallettx.EntryTypeAdjust).
		SetBonusDeltaCents(item.RefundCents).
		SetSourceType("after_sale_compensate").SetSourceID(item.AfterSaleNo).
		SetOperatorID(adminID).SetNote("售后补偿权益").Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errConflict
		}
		return fmt.Errorf("%w：补偿流水写入失败：%v", errExecutionFailed, err)
	}
	return nil
}

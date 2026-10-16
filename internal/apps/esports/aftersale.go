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
	"go-admin/internal/ent/esportsaftersaleaction"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsservicesegment"
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
	// 追偿（§13.4）：赔付由陪玩承担时填承接陪玩与追偿金额，执行售后时另建待审批的追偿明细。
	RecoveryPlayerID int   `json:"recovery_player_id"`
	RecoveryCents    int64 `json:"recovery_cents"`
	// 争议涉及陪玩（§13.3 部分退款成员明细冲回）：退款类方案可指定该单实际服务成员，
	// 执行退款时优先冲回该成员的服务与加钟分成，剩余额度再按比例分摊其他成员；
	// 留空则维持按订单整体比例冲回。
	DisputePlayerID int `json:"dispute_player_id"`
	// 多动作方案（§13.3 / §13.6）：非空时改走「多动作方案 + 逐项执行」，忽略上面的单动作字段，
	// 由动作项推导聚合摘要（refund_cents = 资金类动作金额之和，plan_action 取主导类型）。
	Actions []afterSaleActionInput `json:"actions"`
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

// validAfterSaleSourceType 统一售后支持的来源类型（§13.1 / §13.3）。
func validAfterSaleSourceType(sourceType string) bool {
	switch sourceType {
	case "order", "gift", "stored_value_card", "recharge", "assessment":
		return true
	}
	return false
}

// validAfterSaleKindForSource 按来源限制问题类型，避免「储值卡提考核申诉」这类无意义组合。
func validAfterSaleKindForSource(sourceType, kind string) bool {
	switch sourceType {
	case "order":
		return validAfterSaleKind(kind)
	case "gift":
		return kind == "refund_request" || kind == "wrong_item" ||
			kind == "overcharge" || kind == "other"
	case "stored_value_card", "recharge":
		return kind == "refund_request" || kind == "overcharge" || kind == "other"
	case "assessment":
		return kind == "player_appeal" || kind == "other"
	}
	return false
}

// afterSaleSource 描述一条来源单据解析出的归属与留痕摘要。
type afterSaleSource struct {
	// CustomerID 客户归属；考核处罚等无客户来源为 nil。
	CustomerID *int
	// PlayerID 来源关联陪玩（礼物收礼人 / 考核对象）。
	PlayerID *int
	// Order 有订单上下文时用于写订单事件；其余来源改走事务外盒留痕。
	Order *ent.EsportsOrder
	// Ref 并入事件明细的来源摘要。
	Ref map[string]interface{}
}

// resolveAfterSaleSource 解析来源单据并校验存在性，返回归属与留痕摘要（§13.1 / §13.3）。
// 只做读查询：事务内传入 tx.Client()，事务外直接传 db，避免在外层挂未提交读事务造成写锁等待。
func resolveAfterSaleSource(ctx context.Context, client *ent.Client, sourceType string, sourceID int) (afterSaleSource, error) {
	switch sourceType {
	case "order":
		order, err := client.EsportsOrder.Get(ctx, sourceID)
		if err != nil {
			return afterSaleSource{}, err
		}
		return afterSaleSource{
			CustomerID: &order.CustomerID, Order: order,
			Ref: map[string]interface{}{"order_no": order.OrderNo},
		}, nil
	case "gift":
		gift, err := client.EsportsGiftOrder.Get(ctx, sourceID)
		if err != nil {
			return afterSaleSource{}, err
		}
		return afterSaleSource{
			CustomerID: &gift.CustomerID, PlayerID: &gift.PlayerID,
			Ref: map[string]interface{}{"gift_no": gift.GiftNo, "amount_cents": gift.AmountCents},
		}, nil
	case "stored_value_card":
		card, err := client.EsportsStoredValueCard.Get(ctx, sourceID)
		if err != nil {
			return afterSaleSource{}, err
		}
		return afterSaleSource{
			CustomerID: &card.CustomerID,
			Ref:        map[string]interface{}{"card_no": card.CardNo},
		}, nil
	case "recharge":
		row, err := client.EsportsWalletTx.Get(ctx, sourceID)
		if err != nil {
			return afterSaleSource{}, err
		}
		if row.EntryType != esportswallettx.EntryTypeRecharge {
			return afterSaleSource{}, errInvalidInput
		}
		customerID, err := walletTxCustomerID(ctx, client, row)
		if err != nil {
			return afterSaleSource{}, err
		}
		return afterSaleSource{
			CustomerID: customerID,
			Ref: map[string]interface{}{
				"tx_no": row.TxNo, "account_type": row.AccountType.String(),
				"principal_delta_cents": row.PrincipalDeltaCents,
				"bonus_delta_cents":     row.BonusDeltaCents,
			},
		}, nil
	case "assessment":
		item, err := client.EsportsAssessment.Get(ctx, sourceID)
		if err != nil {
			return afterSaleSource{}, err
		}
		return afterSaleSource{
			PlayerID: &item.PlayerID,
			Ref: map[string]interface{}{
				"task_no": item.TaskNo, "assessment_type": item.AssessmentType.String(),
			},
		}, nil
	}
	return afterSaleSource{}, errInvalidInput
}

// walletTxCustomerID 由充值流水反查客户：余额账户 owner 即客户；储值卡账户 owner 是卡 ID，再取卡上客户。
func walletTxCustomerID(ctx context.Context, client *ent.Client, row *ent.EsportsWalletTx) (*int, error) {
	switch row.AccountType {
	case esportswallettx.AccountTypeCustomerBalance:
		owner := row.OwnerID
		return &owner, nil
	case esportswallettx.AccountTypeStoredValueCard:
		card, err := client.EsportsStoredValueCard.Get(ctx, row.OwnerID)
		if err != nil {
			return nil, err
		}
		return &card.CustomerID, nil
	}
	// 陪玩猫粮充值不属于客户来源，不进客户售后。
	return nil, errInvalidInput
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

// afterSaleCreateOptions 控制事务内创建售后单时的留痕身份：
// 客服入口记为 admin，客户自助记为 customer，陪玩申诉记为 player，系统自动建单记为 system。
type afterSaleCreateOptions struct {
	ActorType string
	ActorID   string
	EventType string
	// PlayerID 仅陪玩申诉 / 系统按成员自动建单时写入，用于把来源陪玩落到售后单上。
	PlayerID *int
	// Details 额外并入 order.after_sale_created 事件明细（如取消政策快照）。
	Details map[string]interface{}
}

// createAfterSaleTx 在调用方事务内受理售后：校验来源与枚举、订单存在性、同来源同问题查重，
// 创建后占用一个订单版本写 order.after_sale_created 事件留痕。
// 抽成事务内版本是为了让「取消已付款订单」「服务时长差异自动建单」等入口与各自的资金动作同事务。
func createAfterSaleTx(ctx context.Context, tx *ent.Tx, in afterSaleInput, opts afterSaleCreateOptions) (*ent.EsportsAfterSale, error) {
	in.SourceType = strings.TrimSpace(in.SourceType)
	in.Demand = strings.TrimSpace(in.Demand)
	in.ExpectedPlan = strings.TrimSpace(in.ExpectedPlan)
	in.EvidenceRef = strings.TrimSpace(in.EvidenceRef)
	in.Contact = strings.TrimSpace(in.Contact)
	if in.SourceType == "" {
		in.SourceType = "order"
	}
	if !validAfterSaleSourceType(in.SourceType) || in.SourceID < 1 ||
		!validAfterSaleChannel(in.Channel) ||
		in.Demand == "" || len([]rune(in.Demand)) > 500 ||
		len([]rune(in.ExpectedPlan)) > 255 || len([]rune(in.EvidenceRef)) > 255 ||
		len([]rune(in.Contact)) > 120 {
		return nil, errInvalidInput
	}
	if !validAfterSaleKindForSource(in.SourceType, in.Kind) {
		return nil, errInvalidInput
	}
	if opts.ActorType == "" {
		opts.ActorType = "admin"
	}
	if opts.EventType == "" {
		opts.EventType = "order.after_sale_created"
	}
	source, err := resolveAfterSaleSource(ctx, tx.Client(), in.SourceType, in.SourceID)
	if err != nil {
		return nil, err
	}
	if in.CustomerID != nil && (source.CustomerID == nil || *in.CustomerID != *source.CustomerID) {
		return nil, errInvalidInput
	}
	duplicated, err := tx.EsportsAfterSale.Query().Where(
		esportsaftersale.SourceTypeEQ(esportsaftersale.SourceType(in.SourceType)),
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
		SetSourceType(esportsaftersale.SourceType(in.SourceType)).SetSourceID(in.SourceID).
		SetKind(esportsaftersale.Kind(in.Kind)).SetChannel(esportsaftersale.Channel(in.Channel)).
		SetDemand(in.Demand).SetExpectedPlan(in.ExpectedPlan).SetEvidenceRef(in.EvidenceRef).
		SetContact(in.Contact).SetStatus(esportsaftersale.StatusPendingAccept)
	if source.CustomerID != nil {
		create.SetCustomerID(*source.CustomerID)
	}
	if opts.PlayerID != nil {
		create.SetPlayerID(*opts.PlayerID)
	} else if source.PlayerID != nil {
		create.SetPlayerID(*source.PlayerID)
	}
	item, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	details := map[string]interface{}{
		"after_sale_no": item.AfterSaleNo, "kind": item.Kind.String(),
		"source_type": in.SourceType, "source_id": in.SourceID,
	}
	for key, value := range source.Ref {
		details[key] = value
	}
	for key, value := range opts.Details {
		details[key] = value
	}
	if source.Order != nil {
		// 订单来源：占用一个订单版本写订单事件（bumpOrder 不改履约状态）。
		newVersion, err := bumpOrder(ctx, tx, source.Order.ID, source.Order.Version, "")
		if err != nil {
			return nil, err
		}
		if _, err = tx.EsportsOrderEvent.Create().SetOrderID(source.Order.ID).
			SetOrderVersion(newVersion).SetEventType(opts.EventType).
			SetActorType(opts.ActorType).SetActorID(opts.ActorID).
			SetDetails(details).Save(ctx); err != nil {
			return nil, err
		}
		return item, nil
	}
	// 非订单来源没有订单版本可承载，改写成一条事务外盒留痕。
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("aftersale.source_created:" + item.AfterSaleNo).
		SetEventType("aftersale.source_created").SetAggregateRef(item.AfterSaleNo).
		SetPayload(details).Save(ctx); err != nil {
		return nil, err
	}
	return item, nil
}

// createAfterSale 受理售后申请（客服入口）：独立事务包装 createAfterSaleTx。
func createAfterSale(ctx context.Context, db *ent.Client, in afterSaleInput, operatorID int, now time.Time) (*ent.EsportsAfterSale, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := createAfterSaleTx(ctx, tx, in, afterSaleCreateOptions{
		ActorType: "admin", ActorID: strconv.Itoa(operatorID),
	})
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSale.Get(ctx, item.ID)
}

// listAfterSales 列出售后单，status 为空则返回全部，按 ID 倒序。
func listAfterSales(ctx context.Context, db *ent.Client, adminID int, isSuper bool, status string, limit int) ([]*ent.EsportsAfterSale, error) {
	if adminID < 1 {
		return nil, errForbidden
	}
	status = strings.TrimSpace(status)
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsAfterSale.Query()
	if !isSuper {
		ids, err := db.EsportsOrder.Query().Where(esportsorder.ResponsibleAdminIDEQ(adminID)).IDs(ctx)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []*ent.EsportsAfterSale{}, nil
		}
		q.Where(esportsaftersale.SourceIDIn(ids...))
	}
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
// 受理即冻结该订单待结算的分成明细（§12.6 争议冻结），争议终结时解冻。
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
	item, err := db.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = freezeAfterSaleCommission(ctx, db, item, adminID, now); err != nil {
		return nil, err
	}
	return item, nil
}

// freezeAfterSaleCommission 冻结售后来源订单的待结算分成明细；非订单来源当前不冻结。
func freezeAfterSaleCommission(ctx context.Context, db *ent.Client, item *ent.EsportsAfterSale, adminID int, now time.Time) error {
	if item.SourceType != esportsaftersale.SourceTypeOrder {
		return nil
	}
	if _, err := freezeOrderCommissionForDispute(ctx, db, item.SourceID, nil,
		"售后争议："+item.AfterSaleNo, adminID, now); err != nil {
		return err
	}
	return nil
}

// unfreezeAfterSaleCommission 争议终结（完成 / 驳回 / 撤回）后解冻对应分成明细。
func unfreezeAfterSaleCommission(ctx context.Context, db *ent.Client, item *ent.EsportsAfterSale) error {
	if item.SourceType != esportsaftersale.SourceTypeOrder {
		return nil
	}
	_, err := unfreezeOrderCommission(ctx, db, item.SourceID)
	return err
}

// submitAfterSalePlan 提交处理方案：processing → pending_customer_confirm（§13.3 方案提交 → 客户确认 → 审批 → 执行）。
// 方案动作项首版为 退原核销款 / 发放补偿权益 / 补钟或换人履约（§13.6）。
// 补履约方案在提交时校验承接陪玩、席位与补足量；其余动作清空补履约参数，避免脏数据。
// 每次提交都会清空上一轮的客户确认 / 拒绝留痕，确保「待客户确认」节点重新走一遍。
func submitAfterSalePlan(ctx context.Context, db *ent.Client, id, adminID int, in afterSalePlanInput, now time.Time) (*ent.EsportsAfterSale, error) {
	in.Resolution = strings.TrimSpace(in.Resolution)
	// §13.3 / §13.6 多动作方案：传了动作项即改走逐项执行口径，与既有单动作路径完全隔离。
	if len(in.Actions) > 0 {
		return submitAfterSaleActionsPlan(ctx, db, id, adminID, in, now)
	}
	if id < 1 || !validFaultParty(in.FaultParty) || !validPlanAction(in.PlanAction) ||
		in.RefundCents < 0 || in.RecoveryCents < 0 || len([]rune(in.Resolution)) > 255 {
		return nil, errInvalidInput
	}
	if in.RecoveryCents > 0 && (in.FaultParty != "player" || in.RecoveryPlayerID < 1) {
		return nil, fmt.Errorf("%w：向陪玩追偿时须把责任方判为陪玩并指定承接陪玩", errInvalidInput)
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
	// §13.4：追偿金额不得超过本次赔付金额；不产生赔付的方案不允许追偿。
	if in.RecoveryCents > refundCents {
		return nil, fmt.Errorf("%w：追偿金额不得超过本次赔付金额", errInvalidInput)
	}
	if in.DisputePlayerID < 0 {
		return nil, errInvalidInput
	}
	if in.DisputePlayerID > 0 {
		item, err := db.EsportsAfterSale.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if item.SourceType != esportsaftersale.SourceTypeOrder {
			return nil, fmt.Errorf("%w：仅订单来源售后可指定争议陪玩", errInvalidInput)
		}
		order, err := db.EsportsOrder.Get(ctx, item.SourceID)
		if err != nil {
			return nil, err
		}
		served, err := playerServedOrder(ctx, db, order.ID, in.DisputePlayerID)
		if err != nil {
			return nil, err
		}
		if !served {
			return nil, fmt.Errorf("%w：争议陪玩须为该订单实际服务成员", errInvalidInput)
		}
	}
	update := db.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(esportsaftersale.StatusProcessing),
	).SetFaultParty(esportsaftersale.FaultParty(in.FaultParty)).
		SetPlanAction(esportsaftersale.PlanAction(in.PlanAction)).
		SetRefundCents(refundCents).
		SetRecoveryCents(in.RecoveryCents).
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
	if in.RecoveryCents > 0 {
		update = update.SetRecoveryPlayerID(in.RecoveryPlayerID)
	} else {
		update = update.ClearRecoveryPlayerID()
	}
	if in.DisputePlayerID > 0 {
		update = update.SetPlayerID(in.DisputePlayerID)
	} else {
		update = update.ClearPlayerID()
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

// playerServedOrder 判断陪玩是否为订单实际服务成员（有服务段，或已接受的席位）。
func playerServedOrder(ctx context.Context, db *ent.Client, orderID, playerID int) (bool, error) {
	count, err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
		esportsservicesegment.PlayerIDEQ(playerID),
	).Count(ctx)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	count, err = db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
	).Count(ctx)
	return count > 0, err
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
	item, err := db.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.HandlerAdminID != nil && *item.HandlerAdminID == adminID {
		return nil, errForbidden
	}
	changed, err := db.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(esportsaftersale.StatusPendingApproval),
		esportsaftersale.VersionEQ(item.Version),
	).SetStatus(esportsaftersale.StatusRejected).SetApproverAdminID(adminID).
		SetResolution(in.Reason).SetResolvedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	// §12.6：驳回后争议终结，解冻该订单待结算分成明细。
	if err = unfreezeAfterSaleCommission(ctx, db, item); err != nil {
		return nil, err
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
	// §12.6：撤回后争议终结，解冻该订单待结算分成明细。
	if item.SourceType == esportsaftersale.SourceTypeOrder {
		if _, err = unfreezeCommissionForOrder(ctx, tx.EsportsCommissionEntry, item.SourceID); err != nil {
			return nil, err
		}
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
	// §13.3 / §13.6：挂了动作项的售后单改走「逐项执行」，总状态由动作项汇总推导（允许部分成功）。
	if hasActions, qerr := db.EsportsAfterSaleAction.Query().Where(
		esportsaftersaleaction.AfterSaleIDEQ(id),
	).Exist(ctx); qerr != nil {
		return nil, qerr
	} else if hasActions {
		return executeAfterSaleActions(ctx, db, id, adminID, now)
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
		// §12.6：补履约方案不产生退款，执行完成即争议终结并解冻分成明细。
		if item.SourceType == esportsaftersale.SourceTypeOrder {
			if _, err = unfreezeCommissionForOrder(ctx, tx.EsportsCommissionEntry, item.SourceID); err != nil {
				return nil, err
			}
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
	refundedOrder, err := tx.EsportsOrder.Get(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	// 退款额度落账后、同事务按比例冲回陪玩分成（新增 reversal 负数明细，幂等靠 idem_key 唯一索引）。
	if err = reverseOrderCommission(ctx, tx, refundedOrder, item, now); err != nil {
		return nil, err
	}
	// 同事务冲回客服提成（§12.2）：结单后部分退款按比例冲回，全额退款冲至零。
	if err = reverseOrderCsCommission(ctx, tx, refundedOrder, item, now); err != nil {
		return nil, err
	}
	// 退款额度落账后、同事务按退款金额冲回客户成长值并重算等级（§10.2/§10.5）。
	if err = reverseOrderGrowth(ctx, tx, order, item, adminID, now); err != nil {
		return nil, err
	}
	// §13.4：赔付由陪玩承担时，同事务另建待审批的陪玩追偿明细。
	if err = createRecoveryFromAfterSale(ctx, tx, item, adminID); err != nil {
		return nil, err
	}
	// §12.6：执行完成即争议终结，解冻该订单待结算分成明细（退款冲回已单列在退款当期）。
	if item.SourceType == esportsaftersale.SourceTypeOrder {
		if _, err = unfreezeCommissionForOrder(ctx, tx.EsportsCommissionEntry, item.SourceID); err != nil {
			return nil, err
		}
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
	// §13.5：责任方判定为陪玩的售后执行完成后自动记载陪玩信用（幂等，不自动处罚）。
	return recordPlayerFaultCredit(ctx, tx, id)
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
	// §13.4：补偿由陪玩承担时同样另建待审批的追偿明细。
	return createRecoveryFromAfterSale(ctx, tx, item, adminID)
}

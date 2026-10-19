package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersaleaction"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsexternalrefund"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/esportswalletaccount"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/predicate"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

// errExecutionFailed 标记"售后方案执行失败"（区别于可重试的并发冲突 / 配置缺失），
// 触发回滚后把售后单落为 execution_failed。
var errExecutionFailed = errors.New("售后执行失败")

var (
	// 没有可原路退回的预付账户核销流水：外部渠道付款走「待执行外部退款单」（见
	// registerExternalChannelAfterSaleRefund），其余无法识别的付款方式明确失败。
	errRefundChannelUnavailable = fmt.Errorf("%w：该订单没有余额 / 储值卡核销流水，无法按原账户原路退回", errNotConfigured)
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

// activeAfterSaleDedupKey 是「同来源同问题至多一张进行中售后单」的唯一索引兜底键（§13.2-09）：
// 建单时写入 <source_type>:<source_id>:<kind>，落终态时清空为 NULL 释放额度。
// 先查后建只能挡住顺序提交，并发下两张事务都会通过查重，必须由 DB 唯一索引兜底。
func activeAfterSaleDedupKey(sourceType string, sourceID int, kind string) string {
	return sourceType + ":" + strconv.Itoa(sourceID) + ":" + kind
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
		SetContact(in.Contact).SetStatus(esportsaftersale.StatusPendingAccept).
		// §13.2-09：进行中唯一键，DB 唯一索引兜底并发重复建单。
		SetActiveDedupKey(activeAfterSaleDedupKey(in.SourceType, in.SourceID, in.Kind))
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
		owned, err := afterSaleOwnedPredicate(ctx, db, adminID)
		if err != nil {
			return nil, err
		}
		if owned == nil {
			return []*ent.EsportsAfterSale{}, nil
		}
		q.Where(owned)
	}
	if status != "" {
		if !validAfterSaleStatus(status) {
			return nil, errInvalidInput
		}
		q.Where(esportsaftersale.StatusEQ(esportsaftersale.Status(status)))
	}
	return q.Order(ent.Desc(esportsaftersale.FieldID)).Limit(limit).All(ctx)
}

// afterSaleOwnedPredicate 构造非超级管理员的售后可见范围（§13.1 / §13.3）：
// order 按负责客服的订单 ID 过滤；gift / stored_value_card / recharge 按归属客服的客户 ID 过滤
// （售后单上的 customer_id 已由 resolveAfterSaleSource 落为真实归属客户）；assessment 无客户归属，任意管理员可见。
// 归属集合为空时返回 nil，调用方直接返回空列表。
func afterSaleOwnedPredicate(ctx context.Context, db *ent.Client, adminID int) (predicate.EsportsAfterSale, error) {
	orderIDs, err := db.EsportsOrder.Query().Where(esportsorder.ResponsibleAdminIDEQ(adminID)).IDs(ctx)
	if err != nil {
		return nil, err
	}
	customerIDs, err := db.EsportsCustomer.Query().Where(esportscustomer.OwnerAdminIDEQ(adminID)).IDs(ctx)
	if err != nil {
		return nil, err
	}
	preds := []predicate.EsportsAfterSale{
		esportsaftersale.And(
			esportsaftersale.SourceTypeEQ(esportsaftersale.SourceTypeOrder),
			esportsaftersale.SourceIDIn(orderIDs...),
		),
		esportsaftersale.And(
			esportsaftersale.SourceTypeIn(
				esportsaftersale.SourceTypeGift,
				esportsaftersale.SourceTypeStoredValueCard,
				esportsaftersale.SourceTypeRecharge,
			),
			esportsaftersale.CustomerIDIn(customerIDs...),
		),
		// 陪玩申诉无客户归属，任意管理员可见。
		esportsaftersale.SourceTypeEQ(esportsaftersale.SourceTypeAssessment),
	}
	return esportsaftersale.Or(preds...), nil
}

// getAfterSale 按 ID 读取售后单。
func getAfterSale(ctx context.Context, db *ent.Client, id int) (*ent.EsportsAfterSale, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// ── 售后列表 / 详情的外部执行信号（审核报告 §8.2-24） ──
//
// 客服在售后列表要能一眼看出两件事，不必逐单点进「动作项」明细：
//  1. 这单还在等财务执行外部退款（存在 awaiting_execution 动作项）；
//  2. 这单关联的外部退款单已超过督办阈值仍未执行（registered 且登记时间早于判定线）。
//
// 两者都是「钱还没出账、单还挂着」的信号，所以随列表 / 详情一起返回，字段只增不改。

// afterSaleExternalSignal 单张售后单的外部执行信号。零值即「没有待执行项、没有超期退款」。
type afterSaleExternalSignal struct {
	// AwaitingExternalExecutionCount 当前处于 awaiting_execution 的动作项数量。
	AwaitingExternalExecutionCount int
	// ExternalRefundOverdue 关联的外部退款单是否存在已超期未执行（registered 且超过阈值）。
	ExternalRefundOverdue bool
}

// afterSaleAdminView 管理端列表 / 详情视图：在既有 SLA 视图（§13.5）之上追加外部执行信号。
// 既有字段（内嵌的售后单字段与 sla_*）名称 / 结构一律不变，前端可增量消费；
// 无相关数据时两个新字段为 0 / false。
type afterSaleAdminView struct {
	afterSaleView
	AwaitingExternalExecutionCount int  `json:"awaiting_external_execution_count"`
	ExternalRefundOverdue          bool `json:"external_refund_overdue"`
}

// afterSaleAdminViewsOf 批量包装售后单（列表用）：限时信息复用既有 SLA 批量视图（一次读策略），
// 外部执行信号按「一批」聚合，两者都与售后单数量无关——不逐单查库。
func afterSaleAdminViewsOf(ctx context.Context, db *ent.Client, items []*ent.EsportsAfterSale,
	now time.Time) ([]afterSaleAdminView, error) {
	ids := make([]int, 0, len(items))
	for _, item := range items {
		if item == nil {
			return nil, errInvalidInput
		}
		ids = append(ids, item.ID)
	}
	signals, err := afterSaleExternalSignalsOf(ctx, db, ids, now)
	if err != nil {
		return nil, err
	}
	base := afterSaleViewsOf(ctx, db, items, now)
	views := make([]afterSaleAdminView, 0, len(base))
	for i, view := range base {
		signal := signals[items[i].ID]
		views = append(views, afterSaleAdminView{
			afterSaleView:                  view,
			AwaitingExternalExecutionCount: signal.AwaitingExternalExecutionCount,
			ExternalRefundOverdue:          signal.ExternalRefundOverdue,
		})
	}
	return views, nil
}

// afterSaleAdminViewOf 包装单条售后单（详情用）：与列表复用同一聚合函数，避免两套口径。
func afterSaleAdminViewOf(ctx context.Context, db *ent.Client, item *ent.EsportsAfterSale,
	now time.Time) (afterSaleAdminView, error) {
	views, err := afterSaleAdminViewsOf(ctx, db, []*ent.EsportsAfterSale{item}, now)
	if err != nil {
		return afterSaleAdminView{}, err
	}
	if len(views) == 0 {
		return afterSaleAdminView{}, errInvalidInput
	}
	return views[0], nil
}

// afterSaleExternalSignalsOf 一次算出一批售后单的外部执行信号（key = 售后单 ID）：
//   - 动作项：一条 after_sale_id IN (...) 的查询取出所有 awaiting_execution 动作项，内存按售后单计数；
//   - 外部退款：一条 after_sale_id IN (...) 的查询取出 registered 且登记时间早于超期判定线的单，
//     再用督办任务同一个判定函数 externalRefundOverdue 复核一遍，命中即标记该售后单「已超期」。
//
// 两条查询与售后单数量无关（列表一页最多 200 单也只发这两条），不构造 N+1。
// 超期口径只来自 externalRefundOverdueHours / externalRefundOverdueCutoff，不在此重复硬编码。
func afterSaleExternalSignalsOf(ctx context.Context, db *ent.Client, afterSaleIDs []int,
	now time.Time) (map[int]afterSaleExternalSignal, error) {
	signals := make(map[int]afterSaleExternalSignal, len(afterSaleIDs))
	ids := make([]int, 0, len(afterSaleIDs))
	seen := make(map[int]struct{}, len(afterSaleIDs))
	for _, id := range afterSaleIDs {
		if id < 1 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return signals, nil
	}
	actions, err := db.EsportsAfterSaleAction.Query().Where(
		esportsaftersaleaction.AfterSaleIDIn(ids...),
		esportsaftersaleaction.StatusEQ(esportsaftersaleaction.StatusAwaitingExecution),
	).Select(esportsaftersaleaction.FieldAfterSaleID).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("统计售后待执行动作项失败: %w", err)
	}
	for _, action := range actions {
		signal := signals[action.AfterSaleID]
		signal.AwaitingExternalExecutionCount++
		signals[action.AfterSaleID] = signal
	}
	refunds, err := db.EsportsExternalRefund.Query().Where(
		esportsexternalrefund.AftersaleIDIn(ids...),
		esportsexternalrefund.StatusEQ(esportsexternalrefund.StatusRegistered),
		esportsexternalrefund.CreatedAtLT(externalRefundOverdueCutoff(now)),
	).Select(esportsexternalrefund.FieldAftersaleID, esportsexternalrefund.FieldStatus,
		esportsexternalrefund.FieldCreatedAt).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("统计售后关联的外部退款超期失败: %w", err)
	}
	for _, refund := range refunds {
		// 查询条件已按阈值过滤，这里再按同一判定函数复核，避免驱动时区口径差异误标超期。
		if refund.AftersaleID == nil || !externalRefundOverdue(refund, now) {
			continue
		}
		signal := signals[*refund.AftersaleID]
		signal.ExternalRefundOverdue = true
		signals[*refund.AftersaleID] = signal
	}
	return signals, nil
}

// ensureAfterSaleSourceOrderNotFrozen 售后写操作统一存单守卫（§4.6 存单冻结）：
// 订单来源售后在受理 / 方案 / 审批 / 驳回 / 执行 / 撤回等会改订单或资金的入口，
// 先按来源加载订单并复用 ensureOrderNotFrozen，冻结期间一律拒绝，避免绕过订单侧冻结。
// 非订单来源（礼物 / 储值卡 / 充值 / 考核）没有订单可冻结，直接放行。
func ensureAfterSaleSourceOrderNotFrozen(ctx context.Context, client *ent.Client,
	item *ent.EsportsAfterSale) error {
	if item == nil || item.SourceType != esportsaftersale.SourceTypeOrder {
		return nil
	}
	order, err := client.EsportsOrder.Get(ctx, item.SourceID)
	if err != nil {
		return err
	}
	return ensureOrderNotFrozen(order)
}

// afterSaleRefundReserveCents 计算售后审批应在订单上预占的退款额度（分）：
//   - 单动作 refund_original：预占方案退款额；
//   - 单动作 compensate / makeup_service：不占退款额度（补偿只发权益，补履约不退款）；
//   - 多动作方案：只汇总 `action_type=refund` 的动作项；补偿/补履约不占退款额度。
//
// 审批预占与撤回释放共用本函数，避免两边口径漂移把预占额度永久占住。
func afterSaleRefundReserveCents(ctx context.Context, client *ent.Client,
	item *ent.EsportsAfterSale) (int64, error) {
	if item == nil || item.SourceType != esportsaftersale.SourceTypeOrder || item.RefundCents <= 0 {
		return 0, nil
	}
	actions, err := client.EsportsAfterSaleAction.Query().Where(
		esportsaftersaleaction.AfterSaleIDEQ(item.ID),
	).All(ctx)
	if err != nil {
		return 0, err
	}
	if len(actions) == 0 {
		if item.PlanAction == esportsaftersale.PlanActionRefundOriginal {
			return item.RefundCents, nil
		}
		return 0, nil
	}
	var total int64
	for _, action := range actions {
		if action.ActionType == esportsaftersaleaction.ActionTypeRefund {
			total += action.AmountCents
		}
	}
	return total, nil
}

// 退款大额复核的门槛与财务角色配置键（§3.3-09 / §13.5-02）。
const (
	afterSaleRefundReviewThresholdKey = "app.esports.aftersale_refund_review_threshold_cents"
	afterSaleRefundFinanceRolesKey    = "app.esports.aftersale_refund_finance_role_names"
)

// afterSaleRefundPolicy 退款金额分流门槛与财务复核角色（§3.3-09 / §13.5-02）。
type afterSaleRefundPolicy struct {
	// ReviewThresholdCents 达该金额（分）的退款审批 / 执行须财务复核；≤0（未配置或非法）表示不启用金额分流。
	ReviewThresholdCents int64
	// FinanceRoleNames 具备财务复核资格的角色名（逗号分隔配置）；为空表示未配置，
	// 此时大额退款明确降级为「仅超级管理员可复核」，不静默放行。
	FinanceRoleNames []string
}

// afterSaleRefundPolicyOf 读取退款复核门槛与财务角色配置；缺失 / 非法回落「不启用分流 + 未配置角色」。
func afterSaleRefundPolicyOf(ctx context.Context, db *ent.Client) afterSaleRefundPolicy {
	items, err := db.SystemConfig.Query().Where(systemconfig.KeyIn(
		afterSaleRefundReviewThresholdKey, afterSaleRefundFinanceRolesKey,
	)).Select(systemconfig.FieldKey, systemconfig.FieldValue).All(ctx)
	if err != nil {
		return afterSaleRefundPolicy{}
	}
	values := map[string]string{}
	for _, item := range items {
		values[item.Key] = strings.TrimSpace(item.Value)
	}
	policy := afterSaleRefundPolicy{}
	if value, err := strconv.ParseInt(values[afterSaleRefundReviewThresholdKey], 10, 64); err == nil && value > 0 {
		policy.ReviewThresholdCents = value
	}
	for _, part := range strings.Split(values[afterSaleRefundFinanceRolesKey], ",") {
		if name := strings.TrimSpace(part); name != "" {
			policy.FinanceRoleNames = append(policy.FinanceRoleNames, name)
		}
	}
	return policy
}

// afterSaleRefundReviewer 当前操作人的资金复核资格：由 handler 从登录态解析；
// 内部无身份入口（测试 / 任务）给零值 = 未解析，一律按「非财务角色」处理，不静默放行。
type afterSaleRefundReviewer struct {
	IsSuper   bool
	RoleNames []string
}

// financeQualified 是否具备财务复核资格：超级管理员直接放行，否则活跃角色名须命中配置角色。
func (r afterSaleRefundReviewer) financeQualified(policy afterSaleRefundPolicy) bool {
	if r.IsSuper {
		return true
	}
	for _, name := range r.RoleNames {
		for _, allowed := range policy.FinanceRoleNames {
			if name == allowed {
				return true
			}
		}
	}
	return false
}

// ensureAfterSaleRefundFinanceReview 退款审批 / 执行的金额门槛与财务角色绑定（§3.3-09 / §13.5-02）：
//   - 未配置门槛（≤0）或未达门槛：直接放行（既有流程不变）；
//   - 达门槛：须超级管理员或配置的财务角色，否则 errForbidden（不把大额资金动作交给普通客服）。
func ensureAfterSaleRefundFinanceReview(policy afterSaleRefundPolicy, amountCents int64,
	reviewer afterSaleRefundReviewer) error {
	if policy.ReviewThresholdCents <= 0 || amountCents < policy.ReviewThresholdCents {
		return nil
	}
	if reviewer.financeQualified(policy) {
		return nil
	}
	return fmt.Errorf("%w：退款金额 %d 分已达 %d 分的财务复核门槛，须管理员 / 财务角色复核",
		errForbidden, amountCents, policy.ReviewThresholdCents)
}

// afterSaleReviewAmountCents 资金复核门槛判定的金额口径：单动作取方案赔付额（退原款 / 补偿权益同样计入），
// 多动作方案取资金类动作项（refund + compensate）金额之和；补履约 / 无资金动作返回 0。
// 不限来源类型：礼物 / 储值卡 / 充值的售后赔付同样是大额资金动作，一并纳入财务复核门槛。
func afterSaleReviewAmountCents(ctx context.Context, client *ent.Client,
	item *ent.EsportsAfterSale) (int64, error) {
	if item == nil {
		return 0, nil
	}
	actions, err := client.EsportsAfterSaleAction.Query().Where(
		esportsaftersaleaction.AfterSaleIDEQ(item.ID),
	).All(ctx)
	if err != nil {
		return 0, err
	}
	if len(actions) == 0 {
		if item.PlanAction == esportsaftersale.PlanActionNone {
			return 0, nil
		}
		return max(item.RefundCents, 0), nil
	}
	var total int64
	for _, action := range actions {
		switch action.ActionType {
		case esportsaftersaleaction.ActionTypeRefund, esportsaftersaleaction.ActionTypeCompensate:
			total += action.AmountCents
		}
	}
	return total, nil
}

// afterSaleShortfallBp 计算订单未履约比例（基点，0~10000）：(承诺量 − 已履约量) / 承诺量。
// 承诺量缺失或已履约量达标时返回 0（不折算）；已履约量超过承诺量按 0 收口。
func afterSaleShortfallBp(promisedUnits, servedUnits int64) int {
	if promisedUnits <= 0 {
		return 0
	}
	unfulfilled := promisedUnits - servedUnits
	if unfulfilled <= 0 {
		return 0
	}
	if unfulfilled > promisedUnits {
		unfulfilled = promisedUnits
	}
	return int(unfulfilled * 10000 / promisedUnits)
}

// estimateProratedRefundCents 按未履约比例折算应退金额（分）：向下取整（尾差归平台），不超过可退基数。
func estimateProratedRefundCents(refundableCents int64, shortfallBp int) int64 {
	if refundableCents <= 0 || shortfallBp <= 0 {
		return 0
	}
	if shortfallBp > 10000 {
		shortfallBp = 10000
	}
	return refundableCents * int64(shortfallBp) / 10000
}

// afterSalePlanNeedsRefundAmount 方案动作是否属于「资金赔付」口径（退原核销款 / 补偿权益），
// 未手填金额时按不足比例折算（§13.4-02）。
func afterSalePlanNeedsRefundAmount(action string) bool {
	return action == "refund_original" || action == "compensate"
}

// proratedRefundForOrderSource 按订单服务量不足比例折算应退金额（§13.4-02）：
// 已履约量取订单全部服务段 reported_units 之和，承诺量含加钟（与 service_terminate.go 同口径）；
// 可退基数 = 应付额 − 已退 − 已预占（未审批时为应付额全额）。非订单来源返回 0（无服务量可比）。
func proratedRefundForOrderSource(ctx context.Context, client *ent.Client,
	item *ent.EsportsAfterSale) (int64, error) {
	if item == nil || item.SourceType != esportsaftersale.SourceTypeOrder {
		return 0, nil
	}
	order, err := client.EsportsOrder.Get(ctx, item.SourceID)
	if err != nil {
		return 0, err
	}
	segments, err := client.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(order.ID),
	).All(ctx)
	if err != nil {
		return 0, err
	}
	var servedUnits int64
	for _, segment := range segments {
		servedUnits += segment.ReportedUnits
	}
	promisedUnits := order.PromisedServiceUnits + order.AddonUnits
	// §12.5-02：可退上界 = 服务应付 + 加钟已收款（加钟已收由只增流水求和派生）。
	addonReceived, err := addonReceivedCents(ctx, client, order.ID)
	if err != nil {
		return 0, err
	}
	refundable := order.PayableCents + addonReceived - order.RefundedCents - order.RefundReservedCents
	if refundable < 0 {
		refundable = 0
	}
	return estimateProratedRefundCents(refundable, afterSaleShortfallBp(promisedUnits, servedUnits)), nil
}

// acceptAfterSale 受理：pending_accept → processing，记录处理客服。
// 受理即冻结该订单待结算的分成明细（§12.6 争议冻结），争议终结时解冻。
func acceptAfterSale(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsAfterSale, error) {
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = ensureAfterSaleSourceOrderNotFrozen(ctx, db, item); err != nil {
		return nil, err
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
	if item, err = db.EsportsAfterSale.Get(ctx, id); err != nil {
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
	if id < 1 {
		return nil, errInvalidInput
	}
	// 提交方案会改写售后单并影响后续资金动作，冻结订单同样禁止推进。
	item, err := db.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = ensureAfterSaleSourceOrderNotFrozen(ctx, db, item); err != nil {
		return nil, err
	}
	// §13.3 / §13.6 多动作方案：传了动作项即改走逐项执行口径，与既有单动作路径完全隔离。
	if len(in.Actions) > 0 {
		return submitAfterSaleActionsPlan(ctx, db, id, adminID, in, now)
	}
	if !validFaultParty(in.FaultParty) || !validPlanAction(in.PlanAction) ||
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
	// §13.4-02：未手填退款金额的资金类方案按「服务量不足比例」折算应退额；
	// 折算不出（履约量已达标或没有服务上报）时明确失败，绝不静默落 0 让方案形同不赔。
	if refundCents == 0 && afterSalePlanNeedsRefundAmount(in.PlanAction) {
		current, err := db.EsportsAfterSale.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		derived, err := proratedRefundForOrderSource(ctx, db, current)
		if err != nil {
			return nil, err
		}
		if derived <= 0 {
			return nil, fmt.Errorf("%w：未手填退款金额，且按服务量不足比例折算为 0（服务量未不足或缺少服务上报），请手工填写退款金额",
				errInvalidInput)
		}
		refundCents = derived
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
// 两个入口共用本函数，因此存单冻结守卫必须收在这里：冻结订单不得被推进到待审批（否则下一步审批
// 会预占退款额度），冻结期间一律返回 errOrderFrozen（HTTP 层映射 409 并给中文提示）。
func confirmAfterSalePlanIn(ctx context.Context, db *ent.Client, id, adminID int,
	channel string, now time.Time) (*ent.EsportsAfterSale, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = ensureAfterSaleSourceOrderNotFrozen(ctx, db, item); err != nil {
		return nil, err
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

// approveAfterSale 审批：pending_approval → pending_execution（无身份入口，见下方 WithReviewer 版本）。
// 不得自审（审批人不得为该单处理人 / 方案提交人）；同时在同一事务内按条件表达式预占订单退款额度，
// 即 refunded_cents + refund_reserved_cents + 本次金额 <= payable_cents + 加钟已收款（§12.5-02），
// 超退或并发冲突均 errConflict。
func approveAfterSale(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsAfterSale, error) {
	return approveAfterSaleWithReviewer(ctx, db, id, adminID, afterSaleRefundReviewer{}, now)
}

// approveAfterSaleWithReviewer 审批（带资金复核资格）：除自审校验与额度预占外，
// 退款金额达配置门槛时须管理员 / 财务角色复核（§3.3-09 / §13.5-02），
// 门槛判定放在额度预占之前，避免不通过的门槛先改了订单额度。
func approveAfterSaleWithReviewer(ctx context.Context, db *ent.Client, id, adminID int,
	reviewer afterSaleRefundReviewer, now time.Time) (*ent.EsportsAfterSale, error) {
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
	if err = ensureAfterSaleSourceOrderNotFrozen(ctx, tx.Client(), item); err != nil {
		return nil, err
	}
	if item.HandlerAdminID != nil && *item.HandlerAdminID == adminID {
		return nil, errForbidden
	}
	// §3.3-09 / §13.5-02：大额退款审批须财务复核资格（同一事务内判定，不通过直接回滚）。
	reviewCents, err := afterSaleReviewAmountCents(ctx, tx.Client(), item)
	if err != nil {
		return nil, err
	}
	if err = ensureAfterSaleRefundFinanceReview(afterSaleRefundPolicyOf(ctx, db), reviewCents, reviewer); err != nil {
		return nil, err
	}
	// 退款额度预占只对订单来源的「退原核销款」有效（§13.6）；compensate / makeup_service
	// 不占原单退款额度。多动作方案沿用聚合额预占，待逐项执行器拆分补偿落账后再收窄。
	reserveCents, err := afterSaleRefundReserveCents(ctx, tx.Client(), item)
	if err != nil {
		return nil, err
	}
	if reserveCents > 0 {
		// P1：退原核销款的审批会预占订单退款额度，订单支付状态异常（渠道撤销 / 拒付）时
		// 资金去向未定，必须与取消路径同口径拦截并转人工处置；补偿 / 补履约不占原单额度，不受此限。
		if err = ensureAfterSalePaymentStatusRefundable(ctx, tx.Client(), item); err != nil {
			return nil, err
		}
		// §12.5-02：可退上界 = 服务应付 + 加钟已收款（由只增流水求和派生），
		// 并发条件谓词必须带上同一上界，否则会打开超退缺口（D34 不变量的证明前提）。
		addonReceived, err := addonReceivedCents(ctx, tx.Client(), item.SourceID)
		if err != nil {
			return nil, err
		}
		changed, err := tx.EsportsOrder.Update().Where(
			esportsorder.IDEQ(item.SourceID),
			func(s *sql.Selector) {
				s.Where(sql.ExprP("refunded_cents + refund_reserved_cents + ? <= payable_cents + ?",
					reserveCents, addonReceived))
			},
		).AddRefundReservedCents(reserveCents).AddVersion(1).Save(ctx)
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
	if err = ensureAfterSaleSourceOrderNotFrozen(ctx, db, item); err != nil {
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
		SetResolution(in.Reason).SetResolvedAt(now.UTC()).AddVersion(1).
		// §13.2-09：落终态必须释放进行中唯一键，否则该来源同问题再也建不了新售后单。
		ClearActiveDedupKey().Save(ctx)
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
	if err = ensureAfterSaleSourceOrderNotFrozen(ctx, tx.Client(), item); err != nil {
		return nil, err
	}
	if item.Status == esportsaftersale.StatusCompleted || item.Status == esportsaftersale.StatusRejected ||
		item.Status == esportsaftersale.StatusWithdrawn {
		return nil, errConflict
	}
	if item.Status == esportsaftersale.StatusPendingExecution ||
		item.Status == esportsaftersale.StatusExecutionFailed {
		// P0-9：已审批预占的额度必须在售后终结时原路释放，否则该订单的可退额度会被永久占住、
		// 后续退款全部冲突。执行失败终结（execution_failed）与待执行（pending_execution）同一口径，
		// 释放额按「尚未成功执行的退款动作项」计算，多动作方案里已成功项不会重复释放。
		if err = releaseAfterSaleRefundReserve(ctx, tx, item); err != nil {
			return nil, err
		}
	}
	changed, err := tx.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(item.Status),
	).SetStatus(esportsaftersale.StatusWithdrawn).SetResolvedAt(now.UTC()).AddVersion(1).
		// §13.2-09：落终态释放进行中唯一键。
		ClearActiveDedupKey().Save(ctx)
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

// executeAfterSale 执行售后方案：pending_execution → completed（无身份入口，见下方 WithReviewer 版本）。
// 钱包流水写入与订单退款额度扣减必须在同一事务，任一失败整体回滚；
// 真正的执行失败（非并发 / 配置类）回滚后单独把售后单落为 execution_failed，且不吞错。
func executeAfterSale(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsAfterSale, error) {
	return executeAfterSaleWithReviewer(ctx, db, id, adminID, afterSaleRefundReviewer{}, now)
}

// executeAfterSaleWithReviewer 执行售后方案（带资金复核资格）：
// 退款金额达配置门槛时，执行人与审批人一样须管理员 / 财务角色（§13.5-02 大额门槛与财务角色绑定），
// 避免「审批卡住了、执行随手放行」绕过门槛。
func executeAfterSaleWithReviewer(ctx context.Context, db *ent.Client, id, adminID int,
	reviewer afterSaleRefundReviewer, now time.Time) (*ent.EsportsAfterSale, error) {
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	source, err := db.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// 执行会改订单退款额度 / 分成 / 成长值；冻结订单在进入任何执行分支（含多动作逐项执行）前拦截。
	if err = ensureAfterSaleSourceOrderNotFrozen(ctx, db, source); err != nil {
		return nil, err
	}
	// P1：退原核销款的执行入口同样先看订单支付状态——渠道撤销 / 拒付（payment_status=exception）
	// 时原支付款去向未定，与取消路径同口径禁止执行，交人工处置。
	if refundReserveCents, rerr := afterSaleRefundReserveCents(ctx, db, source); rerr != nil {
		return nil, rerr
	} else if refundReserveCents > 0 {
		if err = ensureAfterSalePaymentStatusRefundable(ctx, db, source); err != nil {
			return nil, err
		}
	}
	reviewCents, err := afterSaleReviewAmountCents(ctx, db, source)
	if err != nil {
		return nil, err
	}
	if err = ensureAfterSaleRefundFinanceReview(afterSaleRefundPolicyOf(ctx, db), reviewCents, reviewer); err != nil {
		return nil, err
	}
	// §13.3 / §13.6：挂了动作项的售后单改走「逐项执行」，总状态由动作项汇总推导（允许部分成功）。
	if hasActions, qerr := db.EsportsAfterSaleAction.Query().Where(
		esportsaftersaleaction.AfterSaleIDEQ(id),
	).Exist(ctx); qerr != nil {
		return nil, qerr
	} else if hasActions {
		return executeAfterSaleActions(ctx, db, id, adminID, now)
	}
	var item *ent.EsportsAfterSale
	switch source.SourceType {
	case esportsaftersale.SourceTypeOrder:
		item, err = executeAfterSaleOnce(ctx, db, id, adminID, now)
	case esportsaftersale.SourceTypeGift:
		item, err = executeGiftAfterSale(ctx, db, id, adminID, now)
	case esportsaftersale.SourceTypeStoredValueCard:
		item, err = executeStoredValueCardAfterSale(ctx, db, id, adminID, now)
	case esportsaftersale.SourceTypeRecharge:
		item, err = executeRechargeAfterSale(ctx, db, id, adminID, now)
	default:
		// assessment（陪玩申诉）不产生退款 / 补履约执行动作。
		return nil, errNotConfigured
	}
	if err != nil {
		if errors.Is(err, errExecutionFailed) {
			if markErr := markAfterSaleExecutionFailed(ctx, db, id, source, err); markErr != nil {
				return nil, markErr
			}
		}
		// §3.2-03：退款未能执行时把订单退款状态落 failed 并留原因；
		// 并发冲突 / 入参不合法属可重试与可修正，不改订单退款状态。
		if source.SourceType == esportsaftersale.SourceTypeOrder && reviewCents > 0 &&
			shouldMarkOrderRefundFailed(err) {
			if markErr := markOrderRefundFailed(ctx, db, source.SourceID, adminID, err.Error(), now); markErr != nil {
				return nil, fmt.Errorf("%w（另：退款失败留痕写入失败：%v）", err, markErr)
			}
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
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	switch item.PlanAction {
	case esportsaftersale.PlanActionRefundOriginal:
		if item.RefundCents <= 0 {
			return nil, errInvalidInput
		}
		// P0-10：外部渠道付款（微信 / 商户码 / 对公转账）没有预付账户可原路退回。
		// 先登记一笔待执行的外部退款单，由财务核验后用既有 executeExternalRefund 执行，
		// 本次执行只结案留痕（不写钱包、不累加 refunded_cents），预占额度留到执行时核销。
		needsExternal, err := afterSaleNeedsExternalRefund(ctx, tx.Client(), item, order)
		if err != nil {
			return nil, err
		}
		if needsExternal {
			if err = registerExternalChannelAfterSaleRefund(ctx, tx, item, order, adminID, now); err != nil {
				return nil, err
			}
			if err = tx.Commit(); err != nil {
				return nil, err
			}
			return db.EsportsAfterSale.Get(ctx, id)
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
		// §13.6 补偿语义：补偿权益是额外发放，不是退原单款项。
		// 因此不得占用原单退款额度、不得累加 refunded_cents / 改 refund_status，
		// 也不得按退款口径冲回陪玩分成、客服提成与客户成长值；这里直接完成，跳过下方统一退款落账段。
		if err = createRecoveryFromAfterSale(ctx, tx, item, adminID); err != nil {
			return nil, err
		}
		// 争议随执行终结，仅解冻待结算分成，不做任何负数冲回。
		if _, err = unfreezeCommissionForOrder(ctx, tx.EsportsCommissionEntry, item.SourceID); err != nil {
			return nil, err
		}
		newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
		if err != nil {
			return nil, err
		}
		if err = completeAfterSaleExecution(ctx, tx, id, order.ID, adminID, newVersion, "order.compensate_executed",
			map[string]interface{}{
				"after_sale_no": item.AfterSaleNo, "plan_action": item.PlanAction.String(),
				"compensate_cents": item.RefundCents, "refund_cents": 0,
			}, now); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return db.EsportsAfterSale.Get(ctx, id)
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
	// 统一处理：仅「退原核销款」走到这里——从预占额度扣减并累加 refunded_cents。
	// 补偿动作已在上方提前完成，不会进入本段占用退款额度或触发冲回。
	// 条件表达式再次校验预占充足且累计不越可退上界（§12.5-02：服务应付 + 加钟已收款），
	// 避免并发超退；refund_status 视累计已退额一并更新。
	addonReceived, err := addonReceivedCents(ctx, tx.Client(), order.ID)
	if err != nil {
		return nil, err
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID),
		func(s *sql.Selector) {
			s.Where(sql.ExprP("refund_reserved_cents >= ?", item.RefundCents))
			s.Where(sql.ExprP("refunded_cents + refund_reserved_cents <= payable_cents + ?", addonReceived))
		},
	).AddRefundReservedCents(-item.RefundCents).AddRefundedCents(item.RefundCents).
		SetRefundStatus(refundStatusFor(order.PayableCents+addonReceived, order.RefundedCents+item.RefundCents)).
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
		SetResolvedAt(now.UTC()).AddVersion(1).
		// §13.2-09：落终态释放进行中唯一键。
		ClearActiveDedupKey().Save(ctx)
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

// refundStatusFor 按累计已退额与可退上界推导退款状态：退满为 full，否则 partial。
// §12.5-02：可退上界 = 服务应付 + 加钟已收款（加钟款可退后，「退满」不能再只按 payable_cents 判）。
func refundStatusFor(refundableUpperCents, refundedCents int64) esportsorder.RefundStatus {
	if refundedCents >= refundableUpperCents {
		return esportsorder.RefundStatusFull
	}
	return esportsorder.RefundStatusPartial
}

// shouldMarkOrderRefundFailed 判断执行返回的错误是否属于「退款执行失败」，需要把订单退款状态落 failed：
// 并发冲突（errConflict）、入参不合法（errInvalidInput）、越权（errForbidden）属可重试 / 可修正，
// 不改订单退款状态；其余（含 errExecutionFailed 与退款通道未配置）都视为退款未能执行，落 failed 留痕。
func shouldMarkOrderRefundFailed(err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, errConflict), errors.Is(err, errInvalidInput), errors.Is(err, errForbidden):
		return false
	}
	return true
}

// markOrderRefundExecuting 把订单退款状态置为 executing（退款已受理、结果未定）并写
// order.refund_executing 事件留痕（§3.2-03 六态枚举中的 executing 不再是无写入方的死值）。
// 已退满（full）的订单不改写，避免覆盖「已全额退还」这一既成事实。
func markOrderRefundExecuting(ctx context.Context, client *ent.Client, orderID, adminID int,
	reason string, now time.Time) error {
	if orderID < 1 {
		return errInvalidInput
	}
	reason = truncateRunes(strings.TrimSpace(reason), 255)
	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return err
	}
	if order.RefundStatus == esportsorder.RefundStatusFull {
		return nil
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID),
		esportsorder.RefundStatusNEQ(esportsorder.RefundStatusFull),
	).SetRefundStatus(esportsorder.RefundStatusExecuting).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return nil
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(order.Version + 1).SetEventType("order.refund_executing").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"reason": reason, "refunded_cents": order.RefundedCents,
			"refund_reserved_cents": order.RefundReservedCents,
		}).Save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

// markOrderRefundFailed 把订单退款状态置为 failed 并写带原因的 order.refund_failed 事件（§3.2-03）：
// 退款执行失败时订单不再停在旧的金额派生态，失败原因随时序留痕可查。已退满的订单不改写。
func markOrderRefundFailed(ctx context.Context, client *ent.Client, orderID, adminID int,
	reason string, now time.Time) error {
	if orderID < 1 {
		return errInvalidInput
	}
	reason = truncateRunes(strings.TrimSpace(reason), 500)
	if reason == "" {
		reason = "退款执行失败，未记录具体原因"
	}
	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return err
	}
	if order.RefundStatus == esportsorder.RefundStatusFull {
		return nil
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID),
		esportsorder.RefundStatusNEQ(esportsorder.RefundStatusFull),
	).SetRefundStatus(esportsorder.RefundStatusFailed).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return nil
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(order.Version + 1).SetEventType("order.refund_failed").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"reason": reason, "refunded_cents": order.RefundedCents,
			"refund_reserved_cents": order.RefundReservedCents,
		}).Save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

// refundComposition 计算本次「退原核销款」应退本金 / 赠送构成（分）。
// 分摊基数取原核销流水的实际构成 C = P + B（不能按本次退款额或订单应付额重新「先本金」）：
//   - 原本金 P、赠送 B、累计已退 R（order.refunded_cents 为本次执行前累计）、本次退款 A；
//   - 累计应退本金 = P*(R+A)/C（整数、封顶 P），本次本金 = 累计应退本金 - P*R/C；
//   - 本次赠送 = A - 本次本金，最后一步自然补齐取整尾差，不做本金单边优先。
//
// 边界：P+B 与订单应付额不一致时以实际核销构成 C 为分摊基数，仅用应付额限制剩余可退额，
// 避免按虚高应付额放大本金；T<=0 时以 C 兜底；A 超过剩余应付额或原核销无构成时拒绝执行。
// §12.5-02：服务与加钟两部分分别调用本函数，各自传该类原核销构成与剩余可退额
// （服务传 payable_cents，加钟传加钟已收款），因此「已退额」也按类分别传入。
func refundComposition(principalCents, bonusCents, payableCents, refundedBefore, amountCents int64) (int64, int64, error) {
	principal := max(principalCents, 0)
	bonus := max(bonusCents, 0)
	composition := principal + bonus
	if amountCents <= 0 {
		return 0, 0, errInvalidInput
	}
	if composition <= 0 {
		return 0, 0, fmt.Errorf("%w：原核销流水无本金/赠送构成，无法按原路退款", errConflict)
	}
	refundable := payableCents
	if refundable <= 0 {
		refundable = composition
	}
	already := max(refundedBefore, 0)
	if already >= refundable {
		return 0, 0, fmt.Errorf("%w：订单可退额度已用尽", errConflict)
	}
	remaining := refundable - already
	if amountCents > remaining {
		return 0, 0, fmt.Errorf("%w：本次退款 %d 分超过剩余可退 %d 分", errConflict, amountCents, remaining)
	}
	cumulativePrincipal := principal * (already + amountCents) / composition
	if cumulativePrincipal > principal {
		cumulativePrincipal = principal
	}
	priorPrincipal := principal * already / composition
	if priorPrincipal > principal {
		priorPrincipal = principal
	}
	principalBack := cumulativePrincipal - priorPrincipal
	if principalBack < 0 {
		principalBack = 0
	}
	if principalBack > amountCents {
		principalBack = amountCents
	}
	return principalBack, amountCents - principalBack, nil
}

// addonRefundWalletSource 是加钟退款流水的来源类型（§12.5-02）：
// 加钟首版只能由客户余额收单（addon.go），与订单原核销账户可能不同，
// 故加钟部分的原路退回单独成行、单独来源类型，继续由 (source_type, source_id, entry_type) 唯一索引兜底幂等。
const addonRefundWalletSource = "after_sale_addon"

// executeRefundOriginal 退原核销款：支持原订单以客户余额或储值卡核销的退款。
// 查原核销流水，按原流水本金 / 赠送构成与订单累计已退额计算本次本金 / 赠送，按原账户「原路退回」
// 写一条 refund_return 流水；幂等靠 (source_type, source_id, entry_type) 唯一索引兜底。
// 原路退回是储值卡的关键约束：订单退款把预付款还回原卡，与「退卡」是两层独立资金动作（§4.4）。
//
// §12.5-02 加钟款原路退回：可退上界含加钟已收款，本次退款先冲服务部分（沿用原核销构成与账户），
// 超出服务可退额的部分按加钟余额收款流水的构成退回加钟原账户（首版为客户余额），
// 使「加钟已收」可经原路退回而不是被当作服务赠送退回。
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
	addonRows, err := orderAddonConsumeRows(ctx, tx.Client(), order.ID)
	if err != nil {
		return err
	}
	var addonPrincipal, addonBonus int64
	var addonAccount esportswallettx.AccountType
	addonOwner := order.CustomerID
	for _, row := range addonRows {
		addonPrincipal += -row.PrincipalDeltaCents
		addonBonus += -row.BonusDeltaCents
		addonAccount, addonOwner = row.AccountType, row.OwnerID
	}
	addonTotal := addonPrincipal + addonBonus
	// 先冲服务部分（不超过剩余服务可退额），剩余部分退加钟已收款。
	serviceRemaining := max(order.PayableCents-max(order.RefundedCents, 0), 0)
	serviceRefund := min(item.RefundCents, serviceRemaining)
	addonRefund := item.RefundCents - serviceRefund
	if addonRefund > 0 && addonTotal <= 0 {
		return fmt.Errorf("%w：本次退款 %d 分超出服务可退 %d 分，且该订单没有可原路退回的加钟已收款",
			errConflict, item.RefundCents, serviceRefund)
	}
	if serviceRefund > 0 {
		// 分次退款必须扣除「已退本金」：按原核销构成计算累计应退本金，再减去此前累计，不能每次都重新先本金。
		principalBack, bonusBack, err := refundComposition(
			-consume.PrincipalDeltaCents, -consume.BonusDeltaCents,
			order.PayableCents, min(max(order.RefundedCents, 0), order.PayableCents), serviceRefund)
		if err != nil {
			return err
		}
		// P0-4：原支付是储值卡时，写回前先查卡状态——卡已退卡 / 作废 / 冻结 / 过期 / 耗尽时
		// 写回该卡等于把钱打进一张再也用不掉的卡，改为退回客户余额或转人工处置。
		refundAccountType, refundOwnerID, writeCardLedger := consume.AccountType, consume.OwnerID, false
		if consume.AccountType == esportswallettx.AccountTypeStoredValueCard {
			refundAccountType, refundOwnerID, writeCardLedger, err = afterSaleRefundAccountForCard(ctx, tx, order, consume.OwnerID)
			if err != nil {
				return err
			}
		}
		refundNote := "售后退款"
		if consume.AccountType == esportswallettx.AccountTypeStoredValueCard && !writeCardLedger {
			refundNote = "售后退款（原储值卡已不可用，退回客户余额）"
		}
		if err = createAfterSaleRefundTx(ctx, tx, item, adminID, refundAccountType, refundOwnerID,
			"after_sale", principalBack, bonusBack, refundNote); err != nil {
			return err
		}
		// 储值卡订单退款冲回对应预付款批次，保持批次账本与卡片余额一致。
		if writeCardLedger {
			if _, err = tx.EsportsLedgerEntry.Create().
				SetAccountType(storedValuePrepayAccount).SetAccountID(strconv.Itoa(consume.OwnerID)).
				SetSourceType("after_sale").SetSourceID(item.AfterSaleNo).
				SetEntryRole("card_refund_return").SetDeltaCents(serviceRefund).Save(ctx); err != nil {
				if ent.IsConstraintError(err) {
					return errConflict
				}
				return fmt.Errorf("%w：储值卡退款批次写入失败：%v", errExecutionFailed, err)
			}
		}
	}
	if addonRefund > 0 {
		// 加钟部分：构成取加钟余额收款流水（本金 / 赠送），按加钟口径的「已退额」扣除后原路退回。
		priorAddonRefunded := max(order.RefundedCents-order.PayableCents, 0)
		principalBack, bonusBack, err := refundComposition(
			addonPrincipal, addonBonus, addonTotal, priorAddonRefunded, addonRefund)
		if err != nil {
			return err
		}
		if err = createAfterSaleRefundTx(ctx, tx, item, adminID, addonAccount, addonOwner,
			addonRefundWalletSource, principalBack, bonusBack, "售后退款（加钟原路退回）"); err != nil {
			return err
		}
	}
	return nil
}

// createAfterSaleRefundTx 写一条原路退款流水（本金 / 赠送分列）；
// 幂等靠 (source_type, source_id, entry_type) 唯一索引兜底，重复执行返回冲突。
func createAfterSaleRefundTx(ctx context.Context, tx *ent.Tx, item *ent.EsportsAfterSale, adminID int,
	accountType esportswallettx.AccountType, ownerID int, sourceType string,
	principalBack, bonusBack int64, note string) error {
	if principalBack == 0 && bonusBack == 0 {
		return nil
	}
	if _, err := tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(accountType).SetOwnerID(ownerID).
		SetEntryType(esportswallettx.EntryTypeRefundReturn).
		SetPrincipalDeltaCents(principalBack).SetBonusDeltaCents(bonusBack).
		SetSourceType(sourceType).SetSourceID(item.AfterSaleNo).
		SetOperatorID(adminID).SetNote(note).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errConflict
		}
		return fmt.Errorf("%w：退款流水写入失败：%v", errExecutionFailed, err)
	}
	return nil
}

// executeCompensate 发放补偿权益：写一条赠送流水（entry_type=adjust）。
// 补偿不是退原单款项：只发权益 + 按需建追偿，不占用订单退款额度、不累加 refunded_cents、
// 也不冲回分成 / 客服提成 / 成长值（由 executeAfterSaleOnce 的 compensate 分支直接完成，跳过统一退款落账）。
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

// ensureAfterSalePaymentStatusRefundable 售后资金入口（审批 / 执行）的订单支付状态守卫（P1）：
// 订单 payment_status=exception 表示渠道撤销 / 拒付，原支付款去向已不确定，
// 此时审批预占与退款执行都必须停手并交人工处置（与已付款取消路径同口径）。
// 仅对订单来源生效；补偿 / 补履约不涉及原支付款回退，调用方按「是否退原核销款」决定是否调用。
func ensureAfterSalePaymentStatusRefundable(ctx context.Context, client *ent.Client,
	item *ent.EsportsAfterSale) error {
	if item == nil || item.SourceType != esportsaftersale.SourceTypeOrder {
		return nil
	}
	order, err := client.EsportsOrder.Get(ctx, item.SourceID)
	if err != nil {
		return err
	}
	return ensureOrderPaymentStatusRefundableForExternalRefund(order)
}

// afterSaleHeldRefundReserveCents 该售后单当前仍占用订单 refund_reserved_cents 的金额（P0-9）：
// 单动作 refund_original 为方案退款额；多动作方案为「尚未成功 / 跳过」的退款动作项金额之和，
// 已执行成功的动作项在落账时已扣减预占，不能再次释放。
func afterSaleHeldRefundReserveCents(ctx context.Context, client *ent.Client,
	item *ent.EsportsAfterSale) (int64, error) {
	if item == nil || item.SourceType != esportsaftersale.SourceTypeOrder {
		return 0, nil
	}
	actions, err := client.EsportsAfterSaleAction.Query().Where(
		esportsaftersaleaction.AfterSaleIDEQ(item.ID),
	).All(ctx)
	if err != nil {
		return 0, err
	}
	if len(actions) == 0 {
		if item.PlanAction != esportsaftersale.PlanActionRefundOriginal {
			return 0, nil
		}
		return max(item.RefundCents, 0), nil
	}
	var total int64
	for _, action := range actions {
		if action.ActionType != esportsaftersaleaction.ActionTypeRefund {
			continue
		}
		// 已执行成功的动作项在落账时已扣减预占，不再计入待释放额度。
		if action.Status == esportsaftersaleaction.StatusSucceeded {
			continue
		}
		total += action.AmountCents
	}
	return total, nil
}

// markAfterSaleExecutionFailed 单动作执行失败终结（P0-9）：售后单落 execution_failed，并在同一事务内
// 释放该单仍占用的预占额度——单动作售后从 execution_failed 无法再次执行（只能撤回重开），
// 不释放就会把订单可退额度永久占住、后续退款全部冲突。
// 多动作方案（有动作项）可由人工续跑，预占额度保留，不在此释放。
func markAfterSaleExecutionFailed(ctx context.Context, db *ent.Client, id int,
	item *ent.EsportsAfterSale, cause error) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed, err := tx.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(esportsaftersale.StatusPendingExecution),
	).SetStatus(esportsaftersale.StatusExecutionFailed).
		SetResolution(truncateRunes(cause.Error(), 255)).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	hasActions, err := tx.EsportsAfterSaleAction.Query().Where(
		esportsaftersaleaction.AfterSaleIDEQ(id),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if !hasActions {
		if err = releaseAfterSaleRefundReserve(ctx, tx, item); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// releaseAfterSaleRefundReserve 售后终结（撤回 / 执行失败终结）时原路释放预占额度（P0-9）：
// 释放额取「仍占用的预占额」，并用 refund_reserved_cents >= 释放额 的条件表达式兜底，
// 重复调用幂等，绝不把 refund_reserved_cents 释放成负数；refunded_cents 不受影响。
func releaseAfterSaleRefundReserve(ctx context.Context, tx *ent.Tx, item *ent.EsportsAfterSale) error {
	held, err := afterSaleHeldRefundReserveCents(ctx, tx.Client(), item)
	if err != nil {
		return err
	}
	if held <= 0 {
		return nil
	}
	order, err := tx.EsportsOrder.Get(ctx, item.SourceID)
	if err != nil {
		return err
	}
	// 二次收口：即便并发下订单已被别的路径释放过一部分，也只释放当前仍占用的额度。
	releaseCents := min(held, max(order.RefundReservedCents, 0))
	if releaseCents <= 0 {
		return nil
	}
	released, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID),
		func(s *sql.Selector) {
			s.Where(sql.ExprP("refund_reserved_cents >= ?", releaseCents))
		},
	).AddRefundReservedCents(-releaseCents).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if released != 1 {
		return errConflict
	}
	return nil
}

// registerExternalChannelAfterSaleRefund 外部渠道「退原核销款」的执行（P0-10）：
// 在售后执行事务内登记一笔待执行的外部退款单，本次执行即结案——
// 不写钱包、不累加 refunded_cents（钱还没出账），预占额度保留到财务执行外部退款时核销扣除。
// 退款冲回陪玩分成 / 客服提成 / 客户成长值随外部退款执行同事务完成（见 executeExternalRefund）。
func registerExternalChannelAfterSaleRefund(ctx context.Context, tx *ent.Tx, item *ent.EsportsAfterSale,
	order *ent.EsportsOrder, adminID int, now time.Time) error {
	refund, err := createAfterSaleExternalRefundIn(ctx, tx, item, order, adminID)
	if err != nil {
		return err
	}
	// §12.6：争议随本单执行终结，解冻该订单待结算分成明细。
	if _, err = unfreezeCommissionForOrder(ctx, tx.EsportsCommissionEntry, item.SourceID); err != nil {
		return err
	}
	// §13.4：赔付由陪玩承担时，仍按方案建待审批的追偿明细。
	if err = createRecoveryFromAfterSale(ctx, tx, item, adminID); err != nil {
		return err
	}
	newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
	if err != nil {
		return err
	}
	message := fmt.Sprintf("已生成外部渠道退款待执行：退款单 %s（渠道 %s），须由财务核验后执行",
		refund.RefundNo, order.PaymentChannel.String())
	if err = completeAfterSaleExecution(ctx, tx, item.ID, order.ID, adminID, newVersion,
		"order.refund_external_registered", map[string]interface{}{
			"after_sale_no": item.AfterSaleNo, "plan_action": item.PlanAction.String(),
			"refund_cents": item.RefundCents, "external_refund_no": refund.RefundNo,
			"external_refund_channel": order.PaymentChannel.String(), "message": message,
		}, now); err != nil {
		return err
	}
	return appendAfterSaleResolution(ctx, tx, item, message)
}

// appendAfterSaleResolution 把执行结论追加进售后单的处理 / 执行说明（255 字上限），
// 让管理端在售后详情直接看到「已生成外部退款待执行」这类提示，而不是只留在订单事件里。
func appendAfterSaleResolution(ctx context.Context, tx *ent.Tx, item *ent.EsportsAfterSale, note string) error {
	note = strings.TrimSpace(note)
	if note == "" {
		return nil
	}
	resolution := strings.TrimSpace(item.Resolution)
	if resolution == "" {
		resolution = note
	} else {
		resolution = resolution + "；" + note
	}
	_, err := tx.EsportsAfterSale.UpdateOneID(item.ID).
		SetResolution(truncateRunes(resolution, 255)).Save(ctx)
	return err
}

// afterSaleRefundAccountForCard 决定储值卡订单「退原核销款」的入账账户（§4.4 / P0-4）。
// 只有仍为 active 的卡才把钱写回原卡（active 是唯一可再次消费的状态，见
// schema/esports_stored_value.go 的状态枚举：pending_verification / rejected / active /
// exhausted / refund_pending / refunded / expired / frozen）。卡已退出可消费状态时：
//   - 卡属该订单客户且客户余额账户可用 → 退回客户余额（第三个返回值 false，不再写卡片批次冲回）；
//   - 否则返回 errExecutionFailed，由售后单落 execution_failed（既有异常态）交人工处置，
//     绝不把钱写回一张已退卡 / 作废的卡。
func afterSaleRefundAccountForCard(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder,
	cardID int) (esportswallettx.AccountType, int, bool, error) {
	card, err := tx.EsportsStoredValueCard.Get(ctx, cardID)
	if err != nil {
		return "", 0, false, err
	}
	if card.Status == esportsstoredvaluecard.StatusActive {
		return esportswallettx.AccountTypeStoredValueCard, card.ID, true, nil
	}
	if card.CustomerID == order.CustomerID {
		usable, err := customerBalanceAccountUsable(ctx, tx.Client(), order.CustomerID)
		if err != nil {
			return "", 0, false, err
		}
		if usable {
			return esportswallettx.AccountTypeCustomerBalance, order.CustomerID, false, nil
		}
	}
	return "", 0, false, fmt.Errorf(
		"%w：原支付储值卡已不可用（状态 %s），且无法退回客户余额，退款需人工处置",
		errExecutionFailed, card.Status.String())
}

// customerBalanceAccountUsable 客户余额账户是否可入账：未建账户（余额完全由流水求和派生）视为可用，
// 已建账户但被冻结时不可入账。
func customerBalanceAccountUsable(ctx context.Context, client *ent.Client, customerID int) (bool, error) {
	account, err := client.EsportsWalletAccount.Query().Where(
		esportswalletaccount.AccountTypeEQ(esportswalletaccount.AccountTypeCustomerBalance),
		esportswalletaccount.OwnerIDEQ(customerID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return account.Status == esportswalletaccount.StatusActive, nil
}

// ── 售后「代客确认」HTTP 入口（审核报告 §3.1：confirmAfterSaleByAdmin 此前无路由） ──

// adminConfirmAfterSaleHandler 客服代客户确认方案（§13.3「待客户确认」→「待审批」）：
// 客户线下已同意时由客服代为确认，写入 customer_confirm_channel=admin_proxy 与
// customer_confirmed_by_admin_id，并清除上一次的客户拒绝原因。
//
// 与「审批」严格区分，不得复用审批 handler：
//   - 本入口只做 pending_customer_confirm → pending_approval：不写 approver_admin_id、
//     不预占订单退款额度、不产生任何资金动作；
//   - 非「待客户确认」状态（含已进入待审批、终态）一律 409；代确认前先过存单冻结守卫
//     （confirmAfterSalePlanIn 内的 ensureAfterSaleSourceOrderNotFrozen），冻结订单同样返回 409
//     与「订单已存单冻结，请先恢复后再操作」的中文提示——因此它不能被当作审批的替代品；
//   - 既有职责分离不变：审批仍须由非该单处理人（handler_admin_id）执行，代确认人不会
//     因代确认而获得审批资格；重复代确认同样 409，不会重复推进状态。
func adminConfirmAfterSaleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	item, err := confirmAfterSaleByAdmin(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

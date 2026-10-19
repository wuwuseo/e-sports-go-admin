package esports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	entadmin "go-admin/internal/ent/admin"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/esportsstoredvalueexception"
	"go-admin/internal/ent/esportswallettx"
)

// 储值卡一致性异常队列（§4.6-06）。
//
// 缺口：储值卡「写入不一致」过去只在检测点落只增事件（账本核验外盒事件），没有可跟进的异常记录实体。
// 本文件把「检测 → 入队」串起来：
//
//   - 结单 / 结算闸口拦下储值卡订单 → kind=settlement_blocked（「结算被拦截」事件，occurrences 反映被拦次数），
//     detail.mismatch_kind 带上具体不一致类型，跟进人不必重放流程即可看清差异；
//   - 定时全量核验（账本对账任务）逐卡复检 → 卡标记与流水不符 card_flag_mismatch、卡账户账本不变式被破坏
//     write_off_mismatch、订单与卡的关联缺核销流水 card_link_mismatch。
//
// 幂等口径：dedup_key = <card_id>:<order_id>:<kind>，仅待跟进（open / following）时写入，
// 靠可空唯一索引兜底；落终态时清空，同一组合再次不一致会重新入队一条新记录。
// 闸口已入队的订单，全量核验不重复登记（避免一次不一致出现两条待跟进）。

// storedValueExceptionInput 是一次「不一致入队」的输入。
type storedValueExceptionInput struct {
	CardID     int
	OrderID    int
	CustomerID int
	Kind       esportsstoredvalueexception.Kind
	Reason     string
	Detail     map[string]interface{}
}

// storedValueExceptionReasonMax 与 schema 的 reason 列长度（255）保持一致。
const storedValueExceptionReasonMax = 255

// validStoredValueExceptionKind 判断不一致类型是否为 schema 已声明取值。
func validStoredValueExceptionKind(kind esportsstoredvalueexception.Kind) bool {
	switch kind {
	case esportsstoredvalueexception.KindWriteOffMismatch,
		esportsstoredvalueexception.KindCardFlagMismatch,
		esportsstoredvalueexception.KindCardLinkMismatch,
		esportsstoredvalueexception.KindSettlementBlocked:
		return true
	}
	return false
}

// storedValueExceptionDedupKey 待跟进去重键：同一（卡 + 订单 + 不一致类型）至多一条待跟进。
func storedValueExceptionDedupKey(cardID, orderID int, kind esportsstoredvalueexception.Kind) string {
	return fmt.Sprintf("%d:%d:%s", cardID, orderID, kind)
}

// storedValueExceptionTerminal 判断异常记录是否已落终态（终态必须让出 dedup_key）。
func storedValueExceptionTerminal(status esportsstoredvalueexception.Status) bool {
	return status == esportsstoredvalueexception.StatusResolved ||
		status == esportsstoredvalueexception.StatusIgnored
}

// clipStoredValueExceptionReason 按 rune 截断说明，避免超出 schema 的 255 列宽而写库失败。
func clipStoredValueExceptionReason(reason string) string {
	reason = strings.TrimSpace(reason)
	runes := []rune(reason)
	if len(runes) <= storedValueExceptionReasonMax {
		return reason
	}
	return string(runes[:storedValueExceptionReasonMax])
}

// recordStoredValueException 幂等入队一条储值卡一致性异常（§4.6-06）：
//   - 已有同 dedup_key 的待跟进记录 → 只累加 occurrences、刷新 last_hit_at 与明细，不新增行；
//   - 终态记录若仍占着 dedup_key（历史数据），先让出键，再按新记录重新入队；
//   - 并发入队靠 dedup_key 唯一索引兜底：命中唯一冲突时回读并累加。
func recordStoredValueException(ctx context.Context, db *ent.Client,
	in storedValueExceptionInput, now time.Time) (*ent.EsportsStoredValueException, error) {
	if in.CardID < 1 || !validStoredValueExceptionKind(in.Kind) {
		return nil, errInvalidInput
	}
	detail := in.Detail
	if detail == nil {
		detail = map[string]interface{}{}
	}
	reason := clipStoredValueExceptionReason(in.Reason)
	key := storedValueExceptionDedupKey(in.CardID, in.OrderID, in.Kind)
	for attempt := 0; attempt < 2; attempt++ {
		existing, err := db.EsportsStoredValueException.Query().
			Where(esportsstoredvalueexception.DedupKeyEQ(key)).Only(ctx)
		switch {
		case err == nil:
			if storedValueExceptionTerminal(existing.Status) {
				if _, err = db.EsportsStoredValueException.UpdateOneID(existing.ID).
					ClearDedupKey().AddVersion(1).Save(ctx); err != nil {
					return nil, err
				}
				continue
			}
			return bumpStoredValueException(ctx, db, existing, detail, reason, now)
		case !ent.IsNotFound(err):
			return nil, err
		}
		created, err := db.EsportsStoredValueException.Create().
			SetExceptionNo(storedValueExceptionNo(now)).
			SetCardID(in.CardID).SetOrderID(in.OrderID).SetCustomerID(in.CustomerID).
			SetKind(in.Kind).SetReason(reason).SetDetail(detail).
			SetOccurrences(1).SetLastHitAt(now.UTC()).SetDedupKey(key).Save(ctx)
		if err == nil {
			return created, nil
		}
		if !ent.IsConstraintError(err) {
			return nil, err
		}
		// 唯一索引冲突（并发入队 / 历史终态残留键）：下一轮回读并累加。
	}
	return nil, errConflict
}

// bumpStoredValueException 累加既有待跟进异常的命中次数并刷新明细。
func bumpStoredValueException(ctx context.Context, db *ent.Client, existing *ent.EsportsStoredValueException,
	detail map[string]interface{}, reason string, now time.Time) (*ent.EsportsStoredValueException, error) {
	update := db.EsportsStoredValueException.UpdateOneID(existing.ID).
		AddOccurrences(1).SetLastHitAt(now.UTC()).AddVersion(1)
	if reason != "" {
		update.SetReason(reason)
	}
	if len(detail) > 0 {
		update.SetDetail(detail)
	}
	return update.Save(ctx)
}

// storedValueExceptionNo 生成异常单号（对外可见，前缀 SVE + 时间 + 随机段）。
func storedValueExceptionNo(now time.Time) string {
	return "SVE" + now.UTC().Format("20060102150405") + strings.ToUpper(uuid.NewString()[:8])
}

// storedValueOrderLinkMissing 判定储值卡订单「标记 / 关联卡 / 核销流水」的关联缺失（§4.6）：
// 三者一致时返回空类型。这是结单 / 结算硬拦截的口径（不随结单规则变化），
// 也是异常队列的判定来源：未关联卡 / 关联卡不存在 / 卡名下没有该订单的核销流水。
func storedValueOrderLinkMissing(ctx context.Context, db *ent.Client, order *ent.EsportsOrder) (
	esportsstoredvalueexception.Kind, string, map[string]interface{}, error) {
	if order.PaymentChannel != esportsorder.PaymentChannelStoredValueCard {
		return "", "", nil, nil
	}
	if order.StoredValueCardID == nil {
		return esportsstoredvalueexception.KindCardLinkMismatch,
			"订单标记为储值卡支付但未关联储值卡，无法结单",
			map[string]interface{}{
				"mismatch":             "stored_value_card_id_missing",
				"payment_channel":      order.PaymentChannel.String(),
				"stored_value_card_id": nil,
			}, nil
	}
	cardID := *order.StoredValueCardID
	if _, err := db.EsportsStoredValueCard.Get(ctx, cardID); err != nil {
		if !ent.IsNotFound(err) {
			return "", "", nil, err
		}
		return esportsstoredvalueexception.KindCardLinkMismatch,
			"订单关联的储值卡不存在，无法结单",
			map[string]interface{}{
				"mismatch": "stored_value_card_not_found", "stored_value_card_id": cardID,
			}, nil
	}
	exists, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypeStoredValueCard),
		esportswallettx.OwnerIDEQ(cardID),
		esportswallettx.SourceTypeEQ("order"),
		esportswallettx.SourceIDEQ(order.OrderNo),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeConsume),
	).Exist(ctx)
	if err != nil {
		return "", "", nil, err
	}
	if !exists {
		return esportsstoredvalueexception.KindCardLinkMismatch,
			"订单缺少储值卡核销流水，无法结单",
			map[string]interface{}{
				"mismatch": "write_off_flow_missing", "stored_value_card_id": cardID,
				"order_no": order.OrderNo, "payable_cents": order.PayableCents,
			}, nil
	}
	return "", "", nil, nil
}

// enqueueStoredValueOrderException 结单 / 结算闸口拦下储值卡订单时把不一致落进异常队列（§4.6-06）。
// 返回是否入队（无不一致时为 false，调用方据此仍按既有中文错误拒绝）。
//
// 订单完全未关联卡时没有任何卡 ID 可归属（schema 要求 card_id > 0），此时只保留既有硬拦截、不入队。
func enqueueStoredValueOrderException(ctx context.Context, db *ent.Client,
	order *ent.EsportsOrder, adminID int, now time.Time) (bool, error) {
	kind, reason, detail, err := storedValueOrderLinkMissing(ctx, db, order)
	if err != nil {
		return false, err
	}
	if kind == "" {
		return false, nil
	}
	cardID := 0
	if order.StoredValueCardID != nil {
		cardID = *order.StoredValueCardID
	}
	if cardID < 1 {
		return false, nil
	}
	if detail == nil {
		detail = map[string]interface{}{}
	}
	detail["mismatch_kind"] = string(kind)
	detail["blocked_stage"] = "close"
	detail["blocked_by_admin_id"] = adminID
	if _, err = recordStoredValueException(ctx, db, storedValueExceptionInput{
		CardID: cardID, OrderID: order.ID, CustomerID: order.CustomerID,
		Kind:   esportsstoredvalueexception.KindSettlementBlocked,
		Reason: "结单被拦截：" + reason,
		Detail: detail,
	}, now); err != nil {
		return false, err
	}
	return true, nil
}

// recordStoredValueCardReconcileExceptions 把全量核验在单张卡上发现的不一致落进异常队列（§4.6-06）：
//   - 卡标记（status）与流水派生余额不符 → card_flag_mismatch；
//   - 卡账户余额为负（超扣 / 重复冲正，流水求和派生，恒为真实损坏）→ write_off_mismatch。
//
// 刻意**不**把 `wallet_batch_sum_mismatch` 映射进队列：卡账户的核销 / 开卡流水以业务键（订单号、卡号）
// 作为 source_id，而 walletRowsBookedInLedger 只按流水号 / 主键匹配，导致每张健康卡都被判成
// 「未登记」并算出差额（实测：钱包 3000 / 批次 3000 / 未登记 3000 → 差额 -3000）。
// 这是既有 §11.6-01 口径问题（已单独上报），在它修好之前把该异常灌进队列只会制造噪音；
// 修好后可把 ledgerAnomalyWalletBatchMismatch 一并纳入。
func recordStoredValueCardReconcileExceptions(ctx context.Context, db *ent.Client,
	card *ent.EsportsStoredValueCard, balance walletBalance, anomalies []ledgerAnomaly, now time.Time) error {
	if card.Status == esportsstoredvaluecard.StatusExhausted && balance.total() > 0 {
		if _, err := recordStoredValueException(ctx, db, storedValueExceptionInput{
			CardID: card.ID, CustomerID: card.CustomerID,
			Kind:   esportsstoredvalueexception.KindCardFlagMismatch,
			Reason: fmt.Sprintf("卡标记为已用尽但流水派生余额仍为 %d 分", balance.total()),
			Detail: map[string]interface{}{
				"card_no": card.CardNo, "card_status": card.Status.String(),
				"derived_balance_cents": balance.total(), "detected_by": "ledger_reconcile",
			},
		}, now); err != nil {
			return err
		}
	}
	for _, anomaly := range anomalies {
		if anomaly.Kind != ledgerAnomalyNegativeBalance {
			continue
		}
		if _, err := recordStoredValueException(ctx, db, storedValueExceptionInput{
			CardID: card.ID, CustomerID: card.CustomerID,
			Kind:   esportsstoredvalueexception.KindWriteOffMismatch,
			Reason: anomaly.Detail,
			Detail: map[string]interface{}{
				"anomaly_kind": anomaly.Kind, "account_type": anomaly.AccountType,
				"account_id": anomaly.AccountID, "detected_by": "ledger_reconcile",
			},
		}, now); err != nil {
			return err
		}
	}
	return nil
}

// recordStoredValueOrderReconcileExceptions 全量核验逐卡复检其名下储值卡订单（§4.6-06）：
// 订单已关联该卡却缺少核销流水时入队 card_link_mismatch；闸口已入队的订单不重复登记。
// 闸口只在「有人尝试结单」时才发现问题，这里兜住「数据已坏但订单再无人结单」的情形。
func recordStoredValueOrderReconcileExceptions(ctx context.Context, db *ent.Client, cardID int, now time.Time) error {
	orders, err := db.EsportsOrder.Query().Where(
		esportsorder.PaymentChannelEQ(esportsorder.PaymentChannelStoredValueCard),
		esportsorder.StoredValueCardIDEQ(cardID),
	).All(ctx)
	if err != nil || len(orders) == 0 {
		return err
	}
	orderIDs := make([]int, 0, len(orders))
	for _, order := range orders {
		orderIDs = append(orderIDs, order.ID)
	}
	queued, err := db.EsportsStoredValueException.Query().Where(
		esportsstoredvalueexception.CardIDEQ(cardID),
		esportsstoredvalueexception.OrderIDIn(orderIDs...),
		esportsstoredvalueexception.StatusIn(esportsstoredvalueexception.StatusOpen,
			esportsstoredvalueexception.StatusFollowing),
	).Select(esportsstoredvalueexception.FieldOrderID).All(ctx)
	if err != nil {
		return err
	}
	alreadyQueued := make(map[int]bool, len(queued))
	for _, item := range queued {
		alreadyQueued[item.OrderID] = true
	}
	for _, order := range orders {
		if alreadyQueued[order.ID] {
			continue
		}
		kind, reason, detail, err := storedValueOrderLinkMissing(ctx, db, order)
		if err != nil {
			return err
		}
		if kind == "" {
			continue
		}
		if detail == nil {
			detail = map[string]interface{}{}
		}
		detail["detected_by"] = "ledger_reconcile"
		if _, err = recordStoredValueException(ctx, db, storedValueExceptionInput{
			CardID: cardID, OrderID: order.ID, CustomerID: order.CustomerID,
			Kind: kind, Reason: reason + "（全量核验发现）", Detail: detail,
		}, now); err != nil {
			return err
		}
	}
	return nil
}

// storedValueExceptionView 是异常队列的管理端视图（字段与 schema 一一对应，时间用 RFC3339 输出）。
type storedValueExceptionView struct {
	ID                int                    `json:"id"`
	ExceptionNo       string                 `json:"exception_no"`
	CardID            int                    `json:"card_id"`
	OrderID           int                    `json:"order_id"`
	CustomerID        int                    `json:"customer_id"`
	Kind              string                 `json:"kind"`
	Status            string                 `json:"status"`
	Reason            string                 `json:"reason"`
	Detail            map[string]interface{} `json:"detail"`
	AssigneeAdminID   *int                   `json:"assignee_admin_id,omitempty"`
	AssignedAt        *time.Time             `json:"assigned_at,omitempty"`
	Occurrences       int                    `json:"occurrences"`
	LastHitAt         *time.Time             `json:"last_hit_at,omitempty"`
	ResolvedByAdminID *int                   `json:"resolved_by_admin_id,omitempty"`
	ResolvedAt        *time.Time             `json:"resolved_at,omitempty"`
	Resolution        string                 `json:"resolution"`
	Version           int                    `json:"version"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
}

func storedValueExceptionViewOf(item *ent.EsportsStoredValueException) *storedValueExceptionView {
	if item == nil {
		return nil
	}
	detail := item.Detail
	if detail == nil {
		detail = map[string]interface{}{}
	}
	return &storedValueExceptionView{
		ID: item.ID, ExceptionNo: item.ExceptionNo, CardID: item.CardID,
		OrderID: item.OrderID, CustomerID: item.CustomerID,
		Kind: item.Kind.String(), Status: item.Status.String(), Reason: item.Reason,
		Detail: detail, AssigneeAdminID: item.AssigneeAdminID, AssignedAt: item.AssignedAt,
		Occurrences: item.Occurrences, LastHitAt: item.LastHitAt,
		ResolvedByAdminID: item.ResolvedByAdminID, ResolvedAt: item.ResolvedAt,
		Resolution: item.Resolution, Version: item.Version,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

// storedValueExceptionFilter 是异常队列的查询过滤（空值表示不限）。
type storedValueExceptionFilter struct {
	// Status 为空表示只看待跟进（open / following）；显式传值才查对应状态。
	Status          string
	Kind            string
	CardID          int
	OrderID         int
	AssigneeAdminID int
	// Mine 只看指派给自己的；Unassigned 只看未指派的。
	Mine       bool
	Unassigned bool
}

func validStoredValueExceptionStatus(status string) bool {
	switch esportsstoredvalueexception.Status(status) {
	case esportsstoredvalueexception.StatusOpen, esportsstoredvalueexception.StatusFollowing,
		esportsstoredvalueexception.StatusResolved, esportsstoredvalueexception.StatusIgnored:
		return true
	}
	return false
}

// visibleStoredValueExceptionsQuery 收敛非超管的可见范围（与储值卡列表同口径）：
// 指派给自己、尚未指派、或自己负责客户的卡。超管不过滤。
func visibleStoredValueExceptionsQuery(ctx context.Context, db *ent.Client,
	adminID int, isSuper bool) (*ent.EsportsStoredValueExceptionQuery, error) {
	q := db.EsportsStoredValueException.Query()
	if isSuper {
		return q, nil
	}
	cardIDs, err := storedValueCardIDsOfAdminCustomers(ctx, db, adminID)
	if err != nil {
		return nil, err
	}
	if len(cardIDs) == 0 {
		return q.Where(esportsstoredvalueexception.Or(
			esportsstoredvalueexception.AssigneeAdminIDEQ(adminID),
			esportsstoredvalueexception.AssigneeAdminIDIsNil(),
		)), nil
	}
	return q.Where(esportsstoredvalueexception.Or(
		esportsstoredvalueexception.AssigneeAdminIDEQ(adminID),
		esportsstoredvalueexception.AssigneeAdminIDIsNil(),
		esportsstoredvalueexception.CardIDIn(cardIDs...),
	)), nil
}

// storedValueCardIDsOfAdminCustomers 取某客服名下客户的储值卡 ID（数据域收敛用）。
func storedValueCardIDsOfAdminCustomers(ctx context.Context, db *ent.Client, adminID int) ([]int, error) {
	customerIDs, err := db.EsportsCustomer.Query().
		Where(esportscustomer.OwnerAdminIDEQ(adminID)).IDs(ctx)
	if err != nil || len(customerIDs) == 0 {
		return nil, err
	}
	return db.EsportsStoredValueCard.Query().
		Where(esportsstoredvaluecard.CustomerIDIn(customerIDs...)).IDs(ctx)
}

// listStoredValueExceptions 列出异常队列（§4.6-06）：默认只看待跟进，按 ID 倒序（新的在前）。
func listStoredValueExceptions(ctx context.Context, db *ent.Client, adminID int, isSuper bool,
	filter storedValueExceptionFilter, limit int) ([]*storedValueExceptionView, error) {
	if adminID < 1 {
		return nil, errForbidden
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q, err := visibleStoredValueExceptionsQuery(ctx, db, adminID, isSuper)
	if err != nil {
		return nil, err
	}
	statuses := []esportsstoredvalueexception.Status{
		esportsstoredvalueexception.StatusOpen, esportsstoredvalueexception.StatusFollowing,
	}
	if filter.Status != "" {
		if !validStoredValueExceptionStatus(filter.Status) {
			return nil, fmt.Errorf("%w：不支持的异常状态 %q", errInvalidInput, filter.Status)
		}
		statuses = []esportsstoredvalueexception.Status{esportsstoredvalueexception.Status(filter.Status)}
	}
	q.Where(esportsstoredvalueexception.StatusIn(statuses...))
	if filter.Kind != "" {
		kind := esportsstoredvalueexception.Kind(filter.Kind)
		if !validStoredValueExceptionKind(kind) {
			return nil, fmt.Errorf("%w：不支持的不一致类型 %q", errInvalidInput, filter.Kind)
		}
		q.Where(esportsstoredvalueexception.KindEQ(kind))
	}
	if filter.CardID > 0 {
		q.Where(esportsstoredvalueexception.CardIDEQ(filter.CardID))
	}
	if filter.OrderID > 0 {
		q.Where(esportsstoredvalueexception.OrderIDEQ(filter.OrderID))
	}
	if filter.AssigneeAdminID > 0 {
		q.Where(esportsstoredvalueexception.AssigneeAdminIDEQ(filter.AssigneeAdminID))
	}
	if filter.Unassigned {
		q.Where(esportsstoredvalueexception.AssigneeAdminIDIsNil())
	}
	if filter.Mine {
		q.Where(esportsstoredvalueexception.AssigneeAdminIDEQ(adminID))
	}
	items, err := q.Order(ent.Desc(esportsstoredvalueexception.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]*storedValueExceptionView, 0, len(items))
	for _, item := range items {
		views = append(views, storedValueExceptionViewOf(item))
	}
	return views, nil
}

// getStoredValueException 读取单条异常（§4.6-06）：不存在返回 not found，存在但不在可见范围返回无权。
func getStoredValueException(ctx context.Context, db *ent.Client, adminID int, isSuper bool,
	id int) (*storedValueExceptionView, error) {
	if adminID < 1 {
		return nil, errForbidden
	}
	if id < 1 {
		return nil, errInvalidInput
	}
	q, err := visibleStoredValueExceptionsQuery(ctx, db, adminID, isSuper)
	if err != nil {
		return nil, err
	}
	item, err := q.Where(esportsstoredvalueexception.IDEQ(id)).Only(ctx)
	if ent.IsNotFound(err) {
		exists, existsErr := db.EsportsStoredValueException.Query().
			Where(esportsstoredvalueexception.IDEQ(id)).Exist(ctx)
		if existsErr != nil {
			return nil, existsErr
		}
		if exists {
			return nil, errForbidden
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return storedValueExceptionViewOf(item), nil
}

// assignStoredValueException 指派 / 改派异常跟进人（§4.6-06）：open / following 才可指派，
// 指派后状态转 following 并记录跟进人与时间；落终态的异常不可再指派。
func assignStoredValueException(ctx context.Context, db *ent.Client, id, operatorID int, isSuper bool,
	assigneeAdminID int, now time.Time) (*storedValueExceptionView, error) {
	if assigneeAdminID < 1 {
		return nil, fmt.Errorf("%w：必须指定跟进人", errInvalidInput)
	}
	if _, err := getStoredValueException(ctx, db, operatorID, isSuper, id); err != nil {
		return nil, err
	}
	exists, err := db.Admin.Query().Where(entadmin.IDEQ(assigneeAdminID)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("%w：跟进人不存在", errInvalidInput)
	}
	changed, err := db.EsportsStoredValueException.Update().Where(
		esportsstoredvalueexception.IDEQ(id),
		esportsstoredvalueexception.StatusIn(esportsstoredvalueexception.StatusOpen,
			esportsstoredvalueexception.StatusFollowing),
	).SetAssigneeAdminID(assigneeAdminID).SetAssignedAt(now.UTC()).
		SetStatus(esportsstoredvalueexception.StatusFollowing).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return getStoredValueException(ctx, db, operatorID, isSuper, id)
}

// resolveStoredValueException 处置异常（§4.6-06）：action=resolve 转已处理、action=ignore 转已忽略。
// 已处理必须写处置说明；落终态同时清空 dedup_key（让出待跟进去重键，允许同一组合再次入队）。
func resolveStoredValueException(ctx context.Context, db *ent.Client, id, operatorID int, isSuper bool,
	action, resolution string, expectedVersion int, now time.Time) (*storedValueExceptionView, error) {
	var status esportsstoredvalueexception.Status
	switch strings.TrimSpace(action) {
	case "resolve":
		status = esportsstoredvalueexception.StatusResolved
	case "ignore":
		status = esportsstoredvalueexception.StatusIgnored
	default:
		return nil, fmt.Errorf("%w：处置动作只支持 resolve / ignore", errInvalidInput)
	}
	resolution = strings.TrimSpace(resolution)
	if status == esportsstoredvalueexception.StatusResolved && resolution == "" {
		return nil, fmt.Errorf("%w：置为已处理时必须填写处置说明", errInvalidInput)
	}
	if len([]rune(resolution)) > 500 {
		return nil, fmt.Errorf("%w：处置说明过长（上限 500 字）", errInvalidInput)
	}
	if _, err := getStoredValueException(ctx, db, operatorID, isSuper, id); err != nil {
		return nil, err
	}
	update := db.EsportsStoredValueException.Update().Where(
		esportsstoredvalueexception.IDEQ(id),
		esportsstoredvalueexception.StatusIn(esportsstoredvalueexception.StatusOpen,
			esportsstoredvalueexception.StatusFollowing),
	)
	if expectedVersion > 0 {
		update.Where(esportsstoredvalueexception.VersionEQ(expectedVersion))
	}
	changed, err := update.SetStatus(status).SetResolvedByAdminID(operatorID).
		SetResolvedAt(now.UTC()).SetResolution(resolution).ClearDedupKey().AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return getStoredValueException(ctx, db, operatorID, isSuper, id)
}

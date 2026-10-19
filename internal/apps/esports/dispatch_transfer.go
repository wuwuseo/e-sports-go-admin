package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsordercollaborator"
	"go-admin/internal/ent/esportsseattransferrequest"
	"go-admin/internal/ent/esportsservicesegment"
)

// 陪玩转单（§5.7）：接单后无法服务时由陪玩发起，**必须经客服确认才生效**。
// 确认前不改动席位与订单状态（避免陪玩单方面退出占位）；确认通过时释放该席位为待补位，
// 按剩余席位把订单退回待接单（还有队友占位）或待派单（无人占位，并重新走优先通知）。

// seatTransferInput 陪玩转单申请入参。
type seatTransferInput struct {
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

// requestSeatTransfer 陪玩发起转单申请。仅本人在该订单上「已接单且未开始服务」的席位可发起；
// 同一席位同时只允许一条待确认申请；已开始服务的转单属补履约范畴（§13.6），首版不支持。
// 幂等靠 idem_key 唯一索引兜底，重复提交返回既有申请。
func requestSeatTransfer(ctx context.Context, db *ent.Client, playerID, orderID int,
	in seatTransferInput, now time.Time) (*ent.EsportsSeatTransferRequest, error) {
	key := strings.TrimSpace(in.IdempotencyKey)
	in.Reason = strings.TrimSpace(in.Reason)
	if playerID < 1 || orderID < 1 || in.Reason == "" || len([]rune(in.Reason)) > 255 ||
		len(key) < 8 || len(key) > 128 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance &&
		order.FulfillmentStatus != esportsorder.FulfillmentStatusAccepted &&
		order.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		return nil, fmt.Errorf("%w：仅已接单且未完结的订单可发起转单", errInvalidInput)
	}
	// §5.9-07：改期提案进行中时锁定订单档期与派单，转单会让提案快照失效。
	if locked, err := pendingRescheduleProposalExists(ctx, tx, orderID, now); err != nil {
		return nil, err
	} else if locked {
		return nil, fmt.Errorf("%w：该订单存在进行中的改期提案，请先确认或撤回提案", errConflict)
	}
	seat, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
	).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, errForbidden
		}
		return nil, err
	}
	// §5.7-02：服务中同样允许转单 —— 确认通过时先结算原陪玩的服务段，再由新陪玩接手。
	// 只有「该陪玩在本单的服务段已经结算」时才拒绝，避免对已结算的服务段重复发起。
	serviceInProgress, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
		esportsservicesegment.PlayerIDEQ(playerID),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusInService),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	settled, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
		esportsservicesegment.PlayerIDEQ(playerID),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusFinished),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if settled && !serviceInProgress {
		return nil, fmt.Errorf("%w：该陪玩在本单的服务段已结算，不可再发起转单", errInvalidInput)
	}
	// 先判幂等重放：同一幂等键重复提交应返回既有申请，而不是被「已有待确认申请」拦成冲突。
	replayed, err := tx.EsportsSeatTransferRequest.Query().Where(
		esportsseattransferrequest.IdemKeyEQ(key),
	).Only(ctx)
	if err == nil {
		return replayed, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	duplicated, err := tx.EsportsSeatTransferRequest.Query().Where(
		esportsseattransferrequest.SeatIDEQ(seat.ID),
		esportsseattransferrequest.StatusEQ(esportsseattransferrequest.StatusPending),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if duplicated {
		return nil, fmt.Errorf("%w：该席位已有待确认的转单申请", errConflict)
	}
	created, err := tx.EsportsSeatTransferRequest.Create().
		SetOrderID(orderID).SetSeatID(seat.ID).SetSeatIndex(seat.SeatIndex).
		SetPlayerID(playerID).SetReason(in.Reason).SetIdemKey(key).
		SetStatus(esportsseattransferrequest.StatusPending).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			// 并发重放：唯一索引兜底，返回事务内可见的既有申请。
			existing, lookupErr := tx.EsportsSeatTransferRequest.Query().Where(
				esportsseattransferrequest.IdemKeyEQ(key),
			).Only(ctx)
			if lookupErr != nil {
				return nil, errConflict
			}
			return existing, nil
		}
		return nil, err
	}
	// 转单申请影响派单，留订单事件；事件按 (order_id, order_version) 唯一，故抬升版本承载。
	if _, err = bumpOrder(ctx, tx, orderID, order.Version, ""); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(order.Version + 1).SetEventType("order.seat_transfer_requested").
		SetActorType("player").SetActorID(strconv.Itoa(playerID)).
		SetDetails(map[string]interface{}{
			"seat_index": seat.SeatIndex, "reason": in.Reason,
			"service_in_progress": serviceInProgress,
		}).Save(ctx); err != nil {
		return nil, err
	}
	// §5.7-02 缺口二：转单申请必须写 outbox 定向通知责任客服（order.* 收件人分支已含责任客服）。
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("order.seat_transfer_requested:" + strconv.Itoa(created.ID)).
		SetEventType("order.seat_transfer_requested").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "seat_index": seat.SeatIndex,
			"player_id": playerID, "reason": in.Reason,
			"service_in_progress": serviceInProgress,
		}).Save(ctx); err != nil {
		if !ent.IsConstraintError(err) {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsSeatTransferRequest.Get(ctx, created.ID)
}

// cancelSeatTransfer 发起方在客服确认前撤回申请。
func cancelSeatTransfer(ctx context.Context, db *ent.Client, playerID, id int, now time.Time) (*ent.EsportsSeatTransferRequest, error) {
	if playerID < 1 || id < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsSeatTransferRequest.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.PlayerID != playerID {
		return nil, errForbidden
	}
	changed, err := db.EsportsSeatTransferRequest.Update().Where(
		esportsseattransferrequest.IDEQ(id),
		esportsseattransferrequest.VersionEQ(item.Version),
		esportsseattransferrequest.StatusEQ(esportsseattransferrequest.StatusPending),
	).SetStatus(esportsseattransferrequest.StatusCancelled).
		SetDecidedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsSeatTransferRequest.Get(ctx, id)
}

// ensureSeatTransferDecider 校验转单确认人：订单负责人或超级管理员。
// 订单尚未指派负责人（公共池）时，任一客服都可确认；已指派负责人时其他客服返回 403。
// 说明：路由只做了管理员鉴权，业务层必须再按订单归属收紧，避免普通客服批准他人订单的转单。
func ensureSeatTransferDecider(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, adminID int) error {
	if order.ResponsibleAdminID == nil || *order.ResponsibleAdminID == adminID {
		return nil
	}
	account, err := tx.Admin.Get(ctx, adminID)
	if err != nil {
		if ent.IsNotFound(err) {
			return errForbidden
		}
		return err
	}
	if account.IsSuperAdmin {
		return nil
	}
	return errForbidden
}

// decideSeatTransfer 客服确认转单：通过则释放席位为待补位并把订单退回待接单 / 待派单；
// 驳回则保持原状。两种结论都要求申请仍为待确认（条件更新保证只生效一次）。
// 确认人必须是订单负责人或超级管理员（公共池无负责人时任一客服可确认）。
func decideSeatTransfer(ctx context.Context, db *ent.Client, adminID, id int,
	approve bool, note string, now time.Time) (*ent.EsportsSeatTransferRequest, error) {
	note = strings.TrimSpace(note)
	if id < 1 || len([]rune(note)) > 255 {
		return nil, errInvalidInput
	}
	if !approve && note == "" {
		return nil, fmt.Errorf("%w：驳回转单须填写原因", errInvalidInput)
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsSeatTransferRequest.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	order, err := tx.EsportsOrder.Get(ctx, item.OrderID)
	if err != nil {
		return nil, err
	}
	if err = ensureSeatTransferDecider(ctx, tx, order, adminID); err != nil {
		return nil, err
	}
	if item.Status != esportsseattransferrequest.StatusPending {
		return nil, errConflict
	}
	next := esportsseattransferrequest.StatusApproved
	if !approve {
		next = esportsseattransferrequest.StatusRejected
	}
	changed, err := tx.EsportsSeatTransferRequest.Update().Where(
		esportsseattransferrequest.IDEQ(id),
		esportsseattransferrequest.VersionEQ(item.Version),
		esportsseattransferrequest.StatusEQ(esportsseattransferrequest.StatusPending),
	).SetStatus(next).SetDecidedByAdminID(adminID).SetDecidedAt(now.UTC()).
		SetDecisionNote(note).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if approve {
		if err = releaseSeatForTransfer(ctx, tx, item, adminID, now); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsSeatTransferRequest.Get(ctx, id)
}

// releaseSeatForTransfer 事务内释放该席位为待补位：席位回到 open 并清空陪玩，
// 按剩余有效席位把订单退回待接单（还有队友占位）或待派单（无人占位，并按 §5.4 重开优先通知）。
func releaseSeatForTransfer(ctx context.Context, tx *ent.Tx, item *ent.EsportsSeatTransferRequest,
	adminID int, now time.Time) error {
	order, err := tx.EsportsOrder.Get(ctx, item.OrderID)
	if err != nil {
		return err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return err
	}
	seat, err := tx.EsportsDispatchSeat.Get(ctx, item.SeatID)
	if err != nil {
		return err
	}
	if seat.Status != esportsdispatchseat.StatusAccepted {
		return fmt.Errorf("%w：席位状态已变化，转单无法生效", errConflict)
	}
	// §5.7-02：服务中转单先把原陪玩在该席位的进行中服务段结算（保留历史）再释放席位。
	wasInService := order.FulfillmentStatus == esportsorder.FulfillmentStatusInService
	if err = settleSeatServiceSegment(ctx, tx, order.ID, item.PlayerID, seat.SeatIndex, now); err != nil {
		return err
	}
	changed, err := tx.EsportsDispatchSeat.Update().Where(
		esportsdispatchseat.IDEQ(seat.ID),
		esportsdispatchseat.VersionEQ(seat.Version),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
	).SetStatus(esportsdispatchseat.StatusOpen).ClearPlayerID().
		SetRespondedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	remaining, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(order.ID),
		esportsdispatchseat.StatusIn(esportsdispatchseat.StatusInvited, esportsdispatchseat.StatusAccepted),
	).Exist(ctx)
	if err != nil {
		return err
	}
	target := esportsorder.FulfillmentStatusPendingAcceptance
	if !remaining {
		target = esportsorder.FulfillmentStatusPendingDispatch
	} else if wasInService {
		// 服务中换人：还有其他成员在开工，订单保持服务中，只把空缺席位交回补位流程。
		stillInService, err := orderHasInServiceSegment(ctx, tx, order.ID)
		if err != nil {
			return err
		}
		if stillInService {
			target = esportsorder.FulfillmentStatusInService
		}
	}
	newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, target)
	if err != nil {
		return err
	}
	if target == esportsorder.FulfillmentStatusPendingDispatch {
		// 复用 §5.4 的开窗 / 释放逻辑，避免转单后绕开优先通知。
		if err = onOrderEnteredPendingDispatch(ctx, tx, order.ID, now); err != nil {
			return err
		}
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.seat_transfer_approved").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"seat_index": seat.SeatIndex, "player_id": item.PlayerID,
		}).Save(ctx); err != nil {
		return err
	}
	// 转单通过同样通知责任客服（order.* 收件人分支已含责任客服），让「谁被换下」可追溯。
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("order.seat_transfer_approved:" + strconv.Itoa(item.ID)).
		SetEventType("order.seat_transfer_approved").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "seat_index": seat.SeatIndex,
			"player_id": item.PlayerID, "order_status": target.String(),
		}).Save(ctx); err != nil {
		if !ent.IsConstraintError(err) {
			return err
		}
	}
	return nil
}

// listSeatTransfers 列出转单申请，可按状态 / 订单 / 陪玩过滤，按 ID 倒序。
func listSeatTransfers(ctx context.Context, db *ent.Client, status string,
	orderID, playerID, limit int) ([]*ent.EsportsSeatTransferRequest, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsSeatTransferRequest.Query()
	if status = strings.TrimSpace(status); status != "" {
		switch status {
		case "pending", "approved", "rejected", "cancelled":
			q.Where(esportsseattransferrequest.StatusEQ(esportsseattransferrequest.Status(status)))
		default:
			return nil, errInvalidInput
		}
	}
	if orderID > 0 {
		q.Where(esportsseattransferrequest.OrderIDEQ(orderID))
	}
	if playerID > 0 {
		q.Where(esportsseattransferrequest.PlayerIDEQ(playerID))
	}
	return q.Order(ent.Desc(esportsseattransferrequest.FieldID)).Limit(limit).All(ctx)
}

// listSeatTransfersForAdmin 管理端按订单归属列出转单申请（§3.5-02 越权读修复）：
// 非超管只能看到「本人负责」「本人被授权协作」或「订单尚无负责客服（公共池）」的申请，
// 归属口径与 ensureSeatTransferDecider 的确认边界一致。显式指定 order_id 且无权时返回
// errForbidden，而不是静默返回空列表（避免调用方把越权当成「没有数据」）。
func listSeatTransfersForAdmin(ctx context.Context, db *ent.Client, adminID int, isSuper bool,
	status string, orderID, playerID, limit int) ([]*ent.EsportsSeatTransferRequest, error) {
	if adminID < 1 {
		return nil, errInvalidInput
	}
	if orderID > 0 && !isSuper {
		if _, err := loadOwnedOrder(ctx, db, adminID, isSuper, orderID); err != nil {
			return nil, err
		}
	}
	items, err := listSeatTransfers(ctx, db, status, orderID, playerID, limit)
	if err != nil || isSuper || len(items) == 0 {
		return items, err
	}
	orderIDs := make([]int, 0, len(items))
	seen := make(map[int]bool, len(items))
	for _, item := range items {
		if !seen[item.OrderID] {
			seen[item.OrderID] = true
			orderIDs = append(orderIDs, item.OrderID)
		}
	}
	orders, err := db.EsportsOrder.Query().Where(
		esportsorder.IDIn(orderIDs...),
	).Select(esportsorder.FieldID, esportsorder.FieldResponsibleAdminID).All(ctx)
	if err != nil {
		return nil, err
	}
	owned := make(map[int]bool, len(orders))
	for _, order := range orders {
		if order.ResponsibleAdminID == nil || *order.ResponsibleAdminID == adminID {
			owned[order.ID] = true
		}
	}
	collaborations, err := db.EsportsOrderCollaborator.Query().Where(
		esportsordercollaborator.AdminIDEQ(adminID),
		esportsordercollaborator.OrderIDIn(orderIDs...),
	).Select(esportsordercollaborator.FieldOrderID).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range collaborations {
		owned[row.OrderID] = true
	}
	scoped := make([]*ent.EsportsSeatTransferRequest, 0, len(items))
	for _, item := range items {
		if owned[item.OrderID] {
			scoped = append(scoped, item)
		}
	}
	return scoped, nil
}

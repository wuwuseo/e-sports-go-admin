package esports

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/global"
)

// 抢单后客户确认：陪玩抢单成功先进入 pending_customer_confirm，
// 客户确认后转 accepted（此时才允许开工）；客户更换则释放席位回抢单大厅重新抢。

// grabbedSeatIn 取当前占位的已接单席位（单席订单只会有一条），返回席位与陪玩。
func grabbedSeatIn(ctx context.Context, tx *ent.Tx, orderID int) (*ent.EsportsDispatchSeat, int, error) {
	seat, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
		esportsdispatchseat.PlayerIDNotNil(),
	).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, 0, errConflict
	}
	if err != nil {
		return nil, 0, err
	}
	return seat, *seat.PlayerID, nil
}

// confirmGrabbedPlayerByCustomer 客户确认抢单陪玩：pending_customer_confirm → accepted，
// 同时写入「可以开工」时间点。归属、状态、冻结存单均在事务内校验。
func confirmGrabbedPlayerByCustomer(ctx context.Context, db *ent.Client, customerID, orderID int, now time.Time) (*ent.EsportsOrder, error) {
	if customerID < 1 || orderID < 1 {
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
	if order.CustomerID != customerID {
		return nil, errForbidden
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCustomerConfirm {
		return nil, errConflict
	}
	seat, playerID, err := grabbedSeatIn(ctx, tx, orderID)
	if err != nil {
		return nil, err
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID),
		esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingCustomerConfirm),
	).SetFulfillmentStatus(esportsorder.FulfillmentStatusAccepted).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	newVersion := order.Version + 1
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.customer_confirmed_player").
		SetActorType("customer").SetActorID(strconv.Itoa(customerID)).
		SetDetails(map[string]interface{}{"player_id": playerID, "seat_index": seat.SeatIndex}).Save(ctx); err != nil {
		return nil, err
	}
	// 客户确认后才算「可以开工」，与 ready_to_start_at 一并写一次性时间线事件。
	if _, err = markOrderReadyToStartIn(ctx, tx, orderID, newVersion, now); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("order.customer_confirmed_player:" + order.OrderNo + ":" + strconv.Itoa(newVersion)).
		SetEventType("order.customer_confirmed_player").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "player_id": playerID, "seat_index": seat.SeatIndex,
		}).Save(ctx); err != nil {
		if !ent.IsConstraintError(err) {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// declineGrabbedPlayerByCustomer 客户更换陪玩：当前席位回收并重置为 open，
// 订单直接回 grabbing 重新上架抢单大厅（不经过待派单、无需客服确认）。
func declineGrabbedPlayerByCustomer(ctx context.Context, db *ent.Client, customerID, orderID int, now time.Time) (*ent.EsportsOrder, error) {
	if customerID < 1 || orderID < 1 {
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
	if order.CustomerID != customerID {
		return nil, errForbidden
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCustomerConfirm {
		return nil, errConflict
	}
	seat, playerID, err := grabbedSeatIn(ctx, tx, orderID)
	if err != nil {
		return nil, err
	}
	changed, err := tx.EsportsDispatchSeat.Update().Where(
		esportsdispatchseat.IDEQ(seat.ID),
		esportsdispatchseat.VersionEQ(seat.Version),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
	).SetStatus(esportsdispatchseat.StatusRevoked).ClearExpiresAt().AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	changed, err = tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID),
		esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingCustomerConfirm),
	).SetFulfillmentStatus(esportsorder.FulfillmentStatusGrabbing).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	newVersion := order.Version + 1
	// 被客户更换的席位立即回到 open，保证大厅里仍可再次被抢。
	if err = reopenDispatchSeatsForGrab(ctx, tx, orderID); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.customer_declined_player").
		SetActorType("customer").SetActorID(strconv.Itoa(customerID)).
		SetDetails(map[string]interface{}{"player_id": playerID, "seat_index": seat.SeatIndex}).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("order.customer_declined_player:" + order.OrderNo + ":" + strconv.Itoa(newVersion)).
		SetEventType("order.customer_declined_player").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "player_id": playerID, "seat_index": seat.SeatIndex,
		}).Save(ctx); err != nil {
		if !ent.IsConstraintError(err) {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	// 订单重新上架大厅，广播让陪玩端立即刷新可抢列表。
	broadcastHallOrder(orderID)
	return db.EsportsOrder.Get(ctx, orderID)
}

// autoConfirmCustomerGrabs 扫描超时未确认的抢单订单并自动确认（幂等收敛），返回实际确认数。
func autoConfirmCustomerGrabs(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	orders, err := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingCustomerConfirm),
		esportsorder.CustomerConfirmDeadlineNotNil(),
		esportsorder.CustomerConfirmDeadlineLTE(now),
	).Order(ent.Asc(esportsorder.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, order := range orders {
		if _, err := confirmGrabbedPlayerByCustomer(ctx, db, order.CustomerID, order.ID, now); err != nil {
			if errors.Is(err, errConflict) {
				continue
			}
			return count, fmt.Errorf("自动确认抢单订单 %s 失败：%w", order.OrderNo, err)
		}
		count++
	}
	return count, nil
}

// autoConfirmCustomerGrabsTask 定时任务外壳：超时未确认的抢单由系统自动确认。
func autoConfirmCustomerGrabsTask(ctx context.Context, logger io.Writer) error {
	count, err := autoConfirmCustomerGrabs(ctx, global.ORM, time.Now(), 100)
	_, _ = fmt.Fprintf(logger, "自动确认超时未确认的抢单订单：%d\n", count)
	return err
}

package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsmemberlevelrule"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/systemconfig"
)

// 派单邀请默认有效期（分钟）对应的系统配置键。
const dispatchInviteTTLKey = "app.esports.dispatch_invite_ttl_minutes"

// ensureDispatchSeats 若订单尚无席位，按 order.RequiredSeats 生成 seat_index=1..N 的 open 席位；
// 已存在则直接返回。并发下靠 (order_id, seat_index) 唯一索引兜底，重复插入视为已生成。
func ensureDispatchSeats(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder) error {
	exists, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(order.ID),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	for i := 1; i <= order.RequiredSeats; i++ {
		if _, err := tx.EsportsDispatchSeat.Create().
			SetOrderID(order.ID).SetSeatIndex(i).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil // 唯一索引兜底：并发事务已建好席位
			}
			return err
		}
	}
	return nil
}

// seatInviteTTL 读系统配置得到邀请有效期，缺失或非法时回落默认 10 分钟。
// ponytail: 默认值与合法区间（1..24h）写死在代码里，后续可改为再读一条配置项。
func seatInviteTTL(ctx context.Context, db *ent.Client) time.Duration {
	const fallback = 10 * time.Minute
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(dispatchInviteTTLKey)).Only(ctx)
	if err != nil {
		return fallback
	}
	minutes, err := strconv.Atoi(item.Value)
	if err != nil || minutes < 1 || minutes > 24*60 {
		return fallback
	}
	return time.Duration(minutes) * time.Minute
}

// bumpOrder 按当前版本做一次条件更新并抬升版本号，占用一个新的订单版本供事件留痕使用。
// targetStatus 为空表示不改履约状态。返回订单新版本号。
func bumpOrder(ctx context.Context, tx *ent.Tx, orderID, expectedVersion int, targetStatus esportsorder.FulfillmentStatus) (int, error) {
	update := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(expectedVersion),
	)
	if targetStatus != "" {
		update.SetFulfillmentStatus(targetStatus)
	}
	changed, err := update.AddVersion(1).Save(ctx)
	if err != nil {
		return 0, err
	}
	if changed != 1 {
		return 0, errConflict
	}
	return expectedVersion + 1, nil
}

// dispatchServiceWindow 返回订单的预约服务时段（§5.5），半开区间 [start, start+duration)。
// 无预约开始时间或未配置服务时长时返回 false，调用方回落「未结束即占位」的保守口径：
// 历史单与即时单没有档期与时长快照，无法做重叠判定。
func dispatchServiceWindow(order *ent.EsportsOrder) (time.Time, time.Time, bool) {
	if order == nil || order.RequestedStartAt == nil || order.ServiceDurationMinutes <= 0 {
		return time.Time{}, time.Time{}, false
	}
	start := order.RequestedStartAt.UTC()
	return start, start.Add(time.Duration(order.ServiceDurationMinutes) * time.Minute), true
}

// dispatchSeatServiceStart 返回席位占位归属的「服务开始时刻」（§5.5）：预约单取预约开始时间，
// 即时单取回应时刻，仍在等待回应的邀请按当前时刻保守归集。第二个返回值为 false 表示该席位
// 没有可归集的服务开始时刻（历史已接单但缺回应时间），不计入每日上限。
func dispatchSeatServiceStart(order *ent.EsportsOrder, seat *ent.EsportsDispatchSeat, now time.Time) (time.Time, bool) {
	if order != nil && order.RequestedStartAt != nil {
		return order.RequestedStartAt.UTC(), true
	}
	if seat.RespondedAt != nil {
		return seat.RespondedAt.UTC(), true
	}
	if seat.Status == esportsdispatchseat.StatusInvited {
		return now.UTC(), true
	}
	return time.Time{}, false
}

// checkPlayerDispatchLimit 统计当前有效邀请及未完成服务占位。接受邀请时排除本次已预占席位（§5.5）。
// 已完成订单或本人已结束的服务段不占并发容量。
// 并发口径：双方都有明确预约时段（预约开始时间 + 服务时长快照）时按区间重叠判定，
// 否则回落「未结束即占位」的保守口径；每日口径按「服务开始自然日」（业务时区）归集，
// 而不是接单 UTC 日，避免不同日期的预约互相误挡。
// targetOrder 为本次要接入的订单；nil 表示只做「是否还有容量」的粗筛。
func checkPlayerDispatchLimit(ctx context.Context, tx *ent.Tx, player *ent.EsportsPlayer,
	targetOrder *ent.EsportsOrder, now time.Time, exceptSeatID int) error {
	if player.ConcurrentLimit <= 0 && player.DailyLimit <= 0 {
		return nil
	}
	seats, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.PlayerIDEQ(player.ID),
		esportsdispatchseat.StatusIn(esportsdispatchseat.StatusInvited, esportsdispatchseat.StatusAccepted),
	).All(ctx)
	if err != nil {
		return err
	}
	finished, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.PlayerIDEQ(player.ID),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusFinished),
	).Select(esportsservicesegment.FieldOrderID).All(ctx)
	if err != nil {
		return err
	}
	finishedOrders := make(map[int]bool, len(finished))
	for _, segment := range finished {
		finishedOrders[segment.OrderID] = true
	}
	targetStart, targetEnd, targetTimed := dispatchServiceWindow(targetOrder)
	// 目标订单的归集日：有预约开始时间按预约日，否则按当前时刻（即时单）。
	targetAt := now.UTC()
	if targetOrder != nil && targetOrder.RequestedStartAt != nil {
		targetAt = targetOrder.RequestedStartAt.UTC()
	}
	targetDay := businessDayStart(targetAt)
	orders := map[int]*ent.EsportsOrder{}
	concurrent, daily := 0, 0
	for _, seat := range seats {
		if seat.ID == exceptSeatID {
			continue
		}
		invited := seat.Status == esportsdispatchseat.StatusInvited
		if invited && (seat.ExpiresAt == nil || !seat.ExpiresAt.After(now.UTC())) {
			continue
		}
		order := orders[seat.OrderID]
		if order == nil {
			order, err = tx.EsportsOrder.Get(ctx, seat.OrderID)
			if err != nil {
				return err
			}
			orders[seat.OrderID] = order
		}
		active := order.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted &&
			order.FulfillmentStatus != esportsorder.FulfillmentStatusCancelled &&
			order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose &&
			order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCloseReview
		// 时段重叠：只有双方都有明确时段才精确判定，否则按 active 保守占位。
		overlap := active
		if targetTimed {
			if start, end, ok := dispatchServiceWindow(order); ok {
				overlap = start.Before(targetEnd) && targetStart.Before(end)
			}
		}
		seatStart, hasStart := dispatchSeatServiceStart(order, seat, now)
		sameDay := hasStart && businessDayStart(seatStart).Equal(targetDay)
		if invited {
			if overlap {
				concurrent++
			}
			if sameDay {
				daily++
			}
			continue
		}
		if overlap && !finishedOrders[seat.OrderID] {
			concurrent++
		}
		if sameDay {
			daily++
		}
	}
	if player.ConcurrentLimit > 0 && concurrent >= player.ConcurrentLimit {
		return fmt.Errorf("%w：该陪玩在此时段并发接单已达上限（%d），无法再接单", errInvalidInput, player.ConcurrentLimit)
	}
	if player.DailyLimit > 0 && daily >= player.DailyLimit {
		return fmt.Errorf("%w：该陪玩当日服务档期已达上限（%d），无法再接单", errInvalidInput, player.DailyLimit)
	}
	return nil
}

// reservePlayerDispatchCapacity 用陪玩版本条件更新串行化不同订单的占位竞争。
// 后续校验或派单失败会回滚本次版本更新；并发双方不能都基于同一旧版本通过校验。
// targetOrder 为本次要接入的订单，用于时段重叠与归集日判定；nil 表示粗筛。
func reservePlayerDispatchCapacity(ctx context.Context, tx *ent.Tx, player *ent.EsportsPlayer,
	targetOrder *ent.EsportsOrder, now time.Time, exceptSeatID int) error {
	changed, err := tx.EsportsPlayer.Update().Where(
		esportsplayer.IDEQ(player.ID), esportsplayer.VersionEQ(player.Version),
	).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	return checkPlayerDispatchLimit(ctx, tx, player, targetOrder, now, exceptSeatID)
}

// assignDispatchSeat 指派陪玩到某一席位：校验资质、分区、等级及容量，条件更新抢下 open 席位并邀请。
// 订单、席位、陪玩版本和事件同事务写入；服务时段重叠校验仍待预约时长快照。
func assignDispatchSeat(ctx context.Context, db *ent.Client, orderID, seatIndex, playerID, adminID int, now time.Time) (*ent.EsportsDispatchSeat, error) {
	if seatIndex < 1 || playerID < 1 {
		return nil, errInvalidInput
	}
	ttl := seatInviteTTL(ctx, db)
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.PaymentStatus != esportsorder.PaymentStatusPaid ||
		(order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch &&
			order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance) {
		return nil, errConflict
	}
	if err = ensureAppointmentConfirmed(order); err != nil {
		return nil, err
	}
	player, err := tx.EsportsPlayer.Get(ctx, playerID)
	if err != nil {
		return nil, err
	}
	if player.Qualification != esportsplayer.QualificationQualified ||
		player.Presence != esportsplayer.PresenceOnline {
		return nil, errForbidden
	}
	if err = reservePlayerDispatchCapacity(ctx, tx, player, order, now, 0); err != nil {
		return nil, err
	}
	game, err := orderGameIn(ctx, tx, order.SkuID)
	if err != nil {
		return nil, err
	}
	if err = checkPlayerGameLevel(player, game, order.RequiredLevel.String()); err != nil {
		return nil, err
	}
	if err = ensureDispatchSeats(ctx, tx, order); err != nil {
		return nil, err
	}
	changed, err := tx.EsportsDispatchSeat.Update().Where(
		esportsdispatchseat.OrderIDEQ(orderID), esportsdispatchseat.SeatIndexEQ(seatIndex),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusOpen),
	).SetPlayerID(playerID).SetStatus(esportsdispatchseat.StatusInvited).
		SetSource(esportsdispatchseat.SourceAssign).SetInvitedByAdminID(adminID).
		SetExpiresAt(now.UTC().Add(ttl)).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, order.Version, esportsorder.FulfillmentStatusPendingAcceptance)
	if err != nil {
		return nil, err
	}
	// §5.9：首次派出邀请的时间点（只写一次）。
	if err = markOrderTimelineIn(ctx, tx, orderID, timelineFirstInvited, now); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.seat_invited").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{"seat_index": seatIndex, "player_id": playerID}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID), esportsdispatchseat.SeatIndexEQ(seatIndex),
	).Only(ctx)
}

// rollbackIfNoActiveSeats 在订单再无 invited/accepted 席位时把履约状态从待接单退回待派单。
// 返回是否发生了回退（用于决定订单版本抬升的目标状态）。
func rollbackIfNoActiveSeats(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder) (bool, error) {
	remaining, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(order.ID),
		esportsdispatchseat.StatusIn(esportsdispatchseat.StatusInvited, esportsdispatchseat.StatusAccepted),
	).Exist(ctx)
	if err != nil {
		return false, err
	}
	return !remaining && order.FulfillmentStatus == esportsorder.FulfillmentStatusPendingAcceptance, nil
}

// revokeDispatchSeat 撤回邀请中的席位：席位转 revoked，若订单再无有效席位则退回待派单。
func revokeDispatchSeat(ctx context.Context, db *ent.Client, orderID, seatIndex, adminID int, reason string) (*ent.EsportsDispatchSeat, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 255 {
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
	seat, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID), esportsdispatchseat.SeatIndexEQ(seatIndex),
	).Only(ctx)
	if err != nil {
		return nil, err
	}
	if seat.Status != esportsdispatchseat.StatusInvited {
		return nil, errConflict
	}
	changed, err := tx.EsportsDispatchSeat.Update().Where(
		esportsdispatchseat.IDEQ(seat.ID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusInvited),
	).SetStatus(esportsdispatchseat.StatusRevoked).
		SetRespondedAt(time.Now().UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	target := esportsorder.FulfillmentStatus("")
	if back, err := rollbackIfNoActiveSeats(ctx, tx, order); err != nil {
		return nil, err
	} else if back {
		target = esportsorder.FulfillmentStatusPendingDispatch
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, order.Version, target)
	if err != nil {
		return nil, err
	}
	// 退回待派单：优先通知已开过窗时直接释放到大厅，否则按名单重新开窗（§5.4 业务口径）。
	if target == esportsorder.FulfillmentStatusPendingDispatch {
		if err = onOrderEnteredPendingDispatch(ctx, tx, orderID, time.Now()); err != nil {
			return nil, err
		}
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.seat_revoked").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{"seat_index": seatIndex, "reason": reason}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID), esportsdispatchseat.SeatIndexEQ(seatIndex),
	).Only(ctx)
}

// claimOrder 公共池认领：仅待派单且无负责客服的订单可被认领，条件 UPDATE 抢单。
func claimOrder(ctx context.Context, db *ent.Client, orderID, adminID int) (*ent.EsportsOrder, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch ||
		order.ResponsibleAdminID != nil {
		return nil, errConflict
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingDispatch),
		esportsorder.ResponsibleAdminIDIsNil(),
	).SetResponsibleAdminID(adminID).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(order.Version + 1).SetEventType("order.claimed").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{"admin_id": adminID}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// transferOrder 转单：把负责客服改为目标客服，未结束的订单均可转。
func transferOrder(ctx context.Context, db *ent.Client, orderID, toAdminID, adminID int, reason string) (*ent.EsportsOrder, error) {
	reason = strings.TrimSpace(reason)
	if toAdminID < 1 || reason == "" || len([]rune(reason)) > 255 {
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
	if order.FulfillmentStatus == esportsorder.FulfillmentStatusCompleted ||
		order.FulfillmentStatus == esportsorder.FulfillmentStatusCancelled {
		return nil, errConflict
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusNotIn(
			esportsorder.FulfillmentStatusCompleted, esportsorder.FulfillmentStatusCancelled),
	).SetResponsibleAdminID(toAdminID).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	from := 0
	if order.ResponsibleAdminID != nil {
		from = *order.ResponsibleAdminID
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(order.Version + 1).SetEventType("order.transferred").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{"from": from, "to": toAdminID, "reason": reason}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// remindOrder 催单：仅留痕，不改履约状态；每次催单抬升订单版本以承载唯一事件。
// ponytail: 催单时间阶梯、升级自动通知与企微/电话等多渠道提醒未实现。
func remindOrder(ctx context.Context, db *ent.Client, orderID, adminID int, level int, note string) error {
	note = strings.TrimSpace(note)
	if level < 1 || level > 3 || len([]rune(note)) > 255 {
		return errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return err
	}
	if order.FulfillmentStatus == esportsorder.FulfillmentStatusCompleted ||
		order.FulfillmentStatus == esportsorder.FulfillmentStatusCancelled {
		return errConflict
	}
	if _, err = bumpOrder(ctx, tx, orderID, order.Version, ""); err != nil {
		return err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(order.Version + 1).SetEventType("order.reminded").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{"level": level, "note": note}).Save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

// publishOrderToGrabHall 客服把已付款的待派单订单发布到抢单大厅（§5.2 / §5.6）。
// 这是普通待派单订单进入抢单大厅的唯一入口：没有优先名单的游戏分区订单，
// 此前只能靠指派推进，抢单大厅对它不可达。
//
//   - 首版抢单大厅只开放单陪玩订单（required_seats=1），多人单返回参数错误并提示走指派；
//   - 订单仍处于优先通知窗口内时等价于「提前释放」：闭合窗口后再对所有人开放；
//   - 否则直接把订单从待派单转 grabbing，并留订单事件与事务外盒通知；
//   - 发布前把被回收的席位（revoked / expired）重置为 open，保证大厅一定有可抢席位。
func publishOrderToGrabHall(ctx context.Context, db *ent.Client, orderID, adminID int, reason string, now time.Time) (*ent.EsportsOrder, error) {
	reason = strings.TrimSpace(reason)
	if orderID < 1 || adminID < 1 || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	if reason == "" {
		reason = dispatchReleaseReasonPublish
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
	if order.PaymentStatus != esportsorder.PaymentStatusPaid ||
		order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		return nil, errConflict
	}
	if err = ensureAppointmentConfirmed(order); err != nil {
		return nil, err
	}
	if order.RequiredSeats != 1 {
		return nil, fmt.Errorf("%w：首版抢单大厅仅开放单陪玩订单，多人订单请改用指派", errInvalidInput)
	}
	if order.PriorityNoticeStatus == esportsorder.PriorityNoticeStatusPending {
		// 窗口仍开着：走提前释放，顺带把窗口置 released、订单置 grabbing。
		if _, err = releaseDispatchPriorityNoticeIn(ctx, tx, order, reason, now); err != nil {
			return nil, err
		}
	} else {
		changed, err := tx.EsportsOrder.Update().Where(
			esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
			esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingDispatch),
		).SetFulfillmentStatus(esportsorder.FulfillmentStatusGrabbing).
			SetPriorityReleaseReason(reason).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
		newVersion := order.Version + 1
		if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
			SetOrderVersion(newVersion).SetEventType("order.published_to_grab_hall").
			SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
			SetDetails(map[string]interface{}{"reason": reason}).Save(ctx); err != nil {
			return nil, err
		}
		if _, err = tx.EsportsOutbox.Create().
			SetEventKey("order.published_to_grab_hall:" + order.OrderNo + ":" + strconv.Itoa(newVersion)).
			SetEventType("order.published_to_grab_hall").SetAggregateRef(order.OrderNo).
			SetPayload(map[string]interface{}{"order_no": order.OrderNo, "reason": reason}).Save(ctx); err != nil {
			if !ent.IsConstraintError(err) {
				return nil, err
			}
		}
	}
	// §5.9：兜底确保「首次进入待派单池」的时间点已落（订单处于待派单即视为已入池，只写一次）。
	if err = markOrderTimelineIn(ctx, tx, orderID, timelineFirstPooled, now); err != nil {
		return nil, err
	}
	if err = reopenDispatchSeatsForGrab(ctx, tx, orderID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// reopenDispatchSeatsForGrab 把订单下已被回收的席位（revoked / expired）重置为 open，
// 清空陪玩与邀请信息，使订单发布到抢单大厅后一定存在可抢席位。
func reopenDispatchSeatsForGrab(ctx context.Context, tx *ent.Tx, orderID int) error {
	_, err := tx.EsportsDispatchSeat.Update().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.StatusIn(esportsdispatchseat.StatusRevoked, esportsdispatchseat.StatusExpired),
	).ClearPlayerID().ClearExpiresAt().ClearRespondedAt().
		SetStatus(esportsdispatchseat.StatusOpen).AddVersion(1).Save(ctx)
	return err
}

// expireDispatchInvite 过期单个邀请席位：席位转 expired，订单必要时退回待派单，写系统事件。
func expireDispatchInvite(ctx context.Context, db *ent.Client, seatID int, now time.Time) (bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	seat, err := tx.EsportsDispatchSeat.Get(ctx, seatID)
	if err != nil {
		return false, err
	}
	if seat.Status != esportsdispatchseat.StatusInvited ||
		seat.ExpiresAt == nil || seat.ExpiresAt.After(now.UTC()) {
		return false, nil
	}
	changed, err := tx.EsportsDispatchSeat.Update().Where(
		esportsdispatchseat.IDEQ(seat.ID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusInvited),
		esportsdispatchseat.ExpiresAtLTE(now.UTC()),
	).SetStatus(esportsdispatchseat.StatusExpired).
		SetRespondedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return false, err
	}
	if changed != 1 {
		return false, errConflict
	}
	order, err := tx.EsportsOrder.Get(ctx, seat.OrderID)
	if err != nil {
		return false, err
	}
	target := esportsorder.FulfillmentStatus("")
	if back, err := rollbackIfNoActiveSeats(ctx, tx, order); err != nil {
		return false, err
	} else if back {
		target = esportsorder.FulfillmentStatusPendingDispatch
	}
	newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, target)
	if err != nil {
		return false, err
	}
	// 退回待派单：优先通知已开过窗时直接释放到大厅，否则按名单重新开窗（§5.4 业务口径）。
	if target == esportsorder.FulfillmentStatusPendingDispatch {
		if err = onOrderEnteredPendingDispatch(ctx, tx, order.ID, now); err != nil {
			return false, err
		}
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.seat_expired").
		SetActorType("system").SetActorID("dispatch_expiry").
		SetDetails(map[string]interface{}{"seat_index": seat.SeatIndex}).Save(ctx); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// expireDispatchInvites 批量处理过期邀请，仿 expireDueOrders：逐个在独立事务内处理，
// 版本竞争失败（errConflict）时跳过，交由下一轮或并发方收敛。
func expireDispatchInvites(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errInvalidInput
	}
	ids, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusInvited),
		esportsdispatchseat.ExpiresAtLTE(now.UTC()),
	).Order(ent.Asc(esportsdispatchseat.FieldExpiresAt)).Limit(limit).IDs(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		expired, err := expireDispatchInvite(ctx, db, id, now)
		if errors.Is(err, errConflict) {
			continue
		}
		if err != nil {
			return count, err
		}
		if expired {
			count++
		}
	}
	return count, nil
}

// listOrderSeats 按席位序号返回订单的全部派单席位。
func listOrderSeats(ctx context.Context, db *ent.Client, orderID int) ([]*ent.EsportsDispatchSeat, error) {
	if orderID < 1 {
		return nil, errInvalidInput
	}
	return db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
	).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).All(ctx)
}

// dispatchBoard 排单看板：按履约状态分桶的订单摘要与在线陪玩数。
type dispatchBoard struct {
	PendingDispatch   []*ent.EsportsOrder `json:"pending_dispatch"`
	PublicPool        []*ent.EsportsOrder `json:"public_pool"`
	Grabbing          []*ent.EsportsOrder `json:"grabbing"`
	PendingAcceptance []*ent.EsportsOrder `json:"pending_acceptance"`
	Accepted          []*ent.EsportsOrder `json:"accepted"`
	InService         []*ent.EsportsOrder `json:"in_service"`
	PendingClose      []*ent.EsportsOrder `json:"pending_close"`
	Abnormal          []*ent.EsportsOrder `json:"abnormal"`
	OnlinePlayers     int                 `json:"online_players"`
	// 会员订单优先（§10.4）：待派单池 / 公共池中属于「会员优先等级」客户的订单 ID，已置顶，供前端标记。
	MemberPriorityOrderIDs []int `json:"member_priority_order_ids"`
	// 公共池脱敏（§5.1 / §8.2）：非超管在公共池只看到脱敏摘要，客户身份与金额字段被掩码；
	// 此字段列出被掩码的订单 ID，供前端以「***」展示。超管为空（看完整数据）。
	MaskedPublicPoolIDs []int `json:"masked_public_pool_ids"`
}

// loadDispatchBoard 组装看板。每桶按 ID 倒序取前 100 条；非超管只看自己负责的订单，
// 但公共池始终返回作为脱敏摘要（非超管掩码客户身份与金额字段）。
// ponytail: 异常桶暂按支付异常汇总。
func loadDispatchBoard(ctx context.Context, db *ent.Client, adminID int, isSuper bool) (*dispatchBoard, error) {
	bucket := func(status esportsorder.FulfillmentStatus) ([]*ent.EsportsOrder, error) {
		q := db.EsportsOrder.Query().Where(esportsorder.FulfillmentStatusEQ(status))
		if !isSuper {
			q.Where(esportsorder.ResponsibleAdminIDEQ(adminID))
		}
		return q.Order(ent.Desc(esportsorder.FieldID)).Limit(100).All(ctx)
	}
	var err error
	board := &dispatchBoard{}
	if board.PendingDispatch, err = bucket(esportsorder.FulfillmentStatusPendingDispatch); err != nil {
		return nil, err
	}
	// 会员订单优先（§10.4）：会员优先等级客户的待派单订单置顶并标记；只影响展示顺序，
	// 不绕过资质、档期与接单上限等硬性条件。
	if board.MemberPriorityOrderIDs, board.PendingDispatch, err = prioritizeMemberOrders(ctx, db, board.PendingDispatch); err != nil {
		return nil, err
	}
	if board.Grabbing, err = bucket(esportsorder.FulfillmentStatusGrabbing); err != nil {
		return nil, err
	}
	if board.PendingAcceptance, err = bucket(esportsorder.FulfillmentStatusPendingAcceptance); err != nil {
		return nil, err
	}
	if board.Accepted, err = bucket(esportsorder.FulfillmentStatusAccepted); err != nil {
		return nil, err
	}
	if board.InService, err = bucket(esportsorder.FulfillmentStatusInService); err != nil {
		return nil, err
	}
	if board.PendingClose, err = bucket(esportsorder.FulfillmentStatusPendingClose); err != nil {
		return nil, err
	}
	// 公共池：待派单且尚无负责客服，任何客服都可见（脱敏摘要）。
	pool := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingDispatch),
		esportsorder.ResponsibleAdminIDIsNil(),
	)
	if board.PublicPool, err = pool.Order(ent.Desc(esportsorder.FieldID)).Limit(100).All(ctx); err != nil {
		return nil, err
	}
	pubMemberIDs, sortedPool, err := prioritizeMemberOrders(ctx, db, board.PublicPool)
	if err != nil {
		return nil, err
	}
	board.PublicPool = sortedPool
	// 公共池是待派单池的子集，标记按订单去重（§10.4）。
	board.MemberPriorityOrderIDs = appendUniqueIDs(board.MemberPriorityOrderIDs, pubMemberIDs)
	// 公共池脱敏（§5.1 / §8.2）：非超管只看到脱敏摘要，掩码客户身份与金额字段。
	if !isSuper {
		for _, o := range board.PublicPool {
			maskPublicPoolOrder(o)
			board.MaskedPublicPoolIDs = append(board.MaskedPublicPoolIDs, o.ID)
		}
	}
	abnormal := db.EsportsOrder.Query().Where(
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusException),
		esportsorder.FulfillmentStatusNotIn(
			esportsorder.FulfillmentStatusCompleted, esportsorder.FulfillmentStatusCancelled),
	)
	if !isSuper {
		abnormal.Where(esportsorder.ResponsibleAdminIDEQ(adminID))
	}
	if board.Abnormal, err = abnormal.Order(ent.Desc(esportsorder.FieldID)).Limit(100).All(ctx); err != nil {
		return nil, err
	}
	online, err := db.EsportsPlayer.Query().Where(
		esportsplayer.QualificationEQ(esportsplayer.QualificationQualified),
		esportsplayer.PresenceEQ(esportsplayer.PresenceOnline),
	).Count(ctx)
	if err != nil {
		return nil, err
	}
	board.OnlinePlayers = online
	return board, nil
}

// maskPublicPoolOrder 对公共池订单做脱敏（§5.1 / §8.2）：清零客户身份与金额字段，
// 保留订单号、商品、席位、状态与时间等可供认领判断的非敏感字段。
// 仅用于看板响应的内存对象，不回写数据库；调用方须在会员优先排序（依赖 customer_id）之后执行。
func maskPublicPoolOrder(o *ent.EsportsOrder) {
	o.CustomerID = 0
	o.UnitPriceCents = 0
	o.DiscountCents = 0
	o.ManualAdjustCents = 0
	o.PayableCents = 0
	o.AddonCents = 0
	o.RefundedCents = 0
	o.RefundReservedCents = 0
}

// prioritizeMemberOrders 标记「会员优先等级」客户的订单并把它们稳定置顶（§10.4）。
// 会员优先由客户当前 member_level_code 指向的等级规则决定，规则须 enabled 且 priority_dispatch=true。
// 返回被置顶的订单 ID（供前端标记）与排序后的订单切片；只影响展示顺序，不改变任何校验。
func prioritizeMemberOrders(ctx context.Context, db *ent.Client, orders []*ent.EsportsOrder) ([]int, []*ent.EsportsOrder, error) {
	if len(orders) == 0 {
		return nil, orders, nil
	}
	customerIDs := make([]int, 0, len(orders))
	seen := make(map[int]bool, len(orders))
	for _, order := range orders {
		if !seen[order.CustomerID] {
			seen[order.CustomerID] = true
			customerIDs = append(customerIDs, order.CustomerID)
		}
	}
	customers, err := db.EsportsCustomer.Query().Where(
		esportscustomer.IDIn(customerIDs...),
	).Select(esportscustomer.FieldID, esportscustomer.FieldMemberLevelCode).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	levelByCustomer := make(map[int]string, len(customers))
	codes := make([]string, 0, len(customers))
	for _, customer := range customers {
		levelByCustomer[customer.ID] = customer.MemberLevelCode
		if customer.MemberLevelCode != "" {
			codes = append(codes, customer.MemberLevelCode)
		}
	}
	if len(codes) == 0 {
		return nil, orders, nil
	}
	rules, err := db.EsportsMemberLevelRule.Query().Where(
		esportsmemberlevelrule.CodeIn(codes...),
		esportsmemberlevelrule.StatusEQ(esportsmemberlevelrule.StatusEnabled),
		esportsmemberlevelrule.PriorityDispatchEQ(true),
	).Select(esportsmemberlevelrule.FieldCode).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(rules) == 0 {
		return nil, orders, nil
	}
	priorityCodes := make(map[string]bool, len(rules))
	for _, rule := range rules {
		priorityCodes[rule.Code] = true
	}
	ids := make([]int, 0, len(orders))
	head := make([]*ent.EsportsOrder, 0, len(orders))
	tail := make([]*ent.EsportsOrder, 0, len(orders))
	for _, order := range orders {
		if priorityCodes[levelByCustomer[order.CustomerID]] {
			ids = append(ids, order.ID)
			head = append(head, order)
			continue
		}
		tail = append(tail, order)
	}
	if len(ids) == 0 {
		return nil, orders, nil
	}
	return ids, append(head, tail...), nil
}

// appendUniqueIDs 追加去重后的 ID，保持既有顺序不变。
func appendUniqueIDs(dst, ids []int) []int {
	seen := make(map[int]bool, len(dst)+len(ids))
	for _, id := range dst {
		seen[id] = true
	}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			dst = append(dst, id)
		}
	}
	return dst
}

package esports

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	entadmin "go-admin/internal/ent/admin"
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

// orderEnteredPendingCloseEvent 是「订单进入待结单」的只增事件类型（P1-02 的席位移除收敛路径复用）。
const orderEnteredPendingCloseEvent = "order.pending_close_entered"

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

// dispatchServiceWindow 返回订单的预约服务时段，半开区间 [start, start+duration)。
// 无预约开始时间或未配置服务时长时返回 false，调用方回落「未结束即占位」的保守口径：
// 历史单与即时单没有档期与时长快照，无法做重叠判定。
func dispatchServiceWindow(order *ent.EsportsOrder) (time.Time, time.Time, bool) {
	if order == nil || order.RequestedStartAt == nil || order.ServiceDurationMinutes <= 0 {
		return time.Time{}, time.Time{}, false
	}
	start := order.RequestedStartAt.UTC()
	return start, start.Add(time.Duration(order.ServiceDurationMinutes) * time.Minute), true
}

// applySeatServiceSlot 把订单档期写入席位的服务时段：
// 预约单（有预约开始时间 + 服务时长快照）落 slot_start / slot_end；
// 即时单或历史单没有档期，显式清空，避免复用席位时残留上一轮的时段。
func applySeatServiceSlot(update *ent.EsportsDispatchSeatUpdate,
	order *ent.EsportsOrder) *ent.EsportsDispatchSeatUpdate {
	start, end, ok := dispatchServiceWindow(order)
	if !ok {
		return update.ClearSlotStart().ClearSlotEnd()
	}
	return update.SetSlotStart(start).SetSlotEnd(end)
}

// dispatchSeatServiceStart 返回席位占位归属的「服务开始时刻」：预约单取预约开始时间，
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

// checkPlayerDispatchLimit 统计当前有效邀请及未完成服务占位。接受邀请时排除本次已预占席位。
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

// playerConnectionHealthy 判定陪玩「连接正常」（硬性条件）：
//   - 连接异常（protected_abnormal）一律视为断开；
//   - 无心跳基线的存量档案不判失联（与 autoOfflineStalePlayers 的存量口径一致）；
//   - 有心跳基线时按 app.esports.player_heartbeat_timeout_minutes 判新鲜度。
func playerConnectionHealthy(ctx context.Context, db *ent.Client, player *ent.EsportsPlayer, now time.Time) bool {
	return playerConnectionHealthyWith(player, now, playerHeartbeatTimeout(ctx, db))
}

// playerConnectionHealthyWith 与 playerConnectionHealthy 完全同口径，但由调用方传入已解析的
// 心跳超时阈值：供批量场景（看板逐人明细）复用同一判定而不产生 N 次配置查询。
func playerConnectionHealthyWith(player *ent.EsportsPlayer, now time.Time, timeout time.Duration) bool {
	if player == nil || player.Presence == esportsplayer.PresenceProtectedAbnormal {
		return false
	}
	if player.LastHeartbeatAt == nil {
		return true
	}
	return player.LastHeartbeatAt.After(now.UTC().Add(-timeout))
}

// playerEligibleForDispatch 是人工指派 / 派单候选 / 优先通知开窗共用的派单资格（+ ）：
//   - 资质必须 qualified；
//   - 在线且连接正常（心跳新鲜度）；
//   - **连接异常（protected_abnormal）一律不具备派单资格**（P1-01-03「连接异常暂缓新派单」）：
//     心跳超时但有在途订单的陪玩只会保留履约与结算，绝不再进入派单候选、优先通知名单或被人工指派。
//     抢新单另走 playerCanGrabNewOrder，同样排除 protected_abnormal。
func playerEligibleForDispatch(ctx context.Context, db *ent.Client, player *ent.EsportsPlayer, now time.Time) (bool, error) {
	if player == nil || player.Qualification != esportsplayer.QualificationQualified {
		return false, nil
	}
	if player.Presence != esportsplayer.PresenceOnline {
		return false, nil
	}
	return playerConnectionHealthy(ctx, db, player, now), nil
}

// ensureRequiredSeatsIntact 显式守护「不得下调已售必需席位」：
// 订单已有派单席位时，席位条数不得少于 required_seats（席位被删/缺失即视为被下调）；
// 尚无席位时放行，由 ensureDispatchSeats 按 required_seats 补齐。
func ensureRequiredSeatsIntact(ctx context.Context, db *ent.Client, orderID int) error {
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return err
	}
	count, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
	).Count(ctx)
	if err != nil {
		return err
	}
	if count > 0 && count < order.RequiredSeats {
		return fmt.Errorf("%w：该订单已售必需席位 %d 个，现存席位仅 %d 个，不得下调",
			errConflict, order.RequiredSeats, count)
	}
	return nil
}

// orderHasInServiceSegment 判断订单是否仍有进行中的服务段（用于服务中换人后的落点判定）。
func orderHasInServiceSegment(ctx context.Context, tx *ent.Tx, orderID int) (bool, error) {
	return tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusInService),
	).Exist(ctx)
}

// settleSeatServiceSegment 结算某陪玩在某席位上的**进行中**服务段（P1-03）：
// 把 in_service 与 paused 两种进行中状态的服务段统一置 finished、写 ended_at 与
// system_seconds（按**有效时长**：墙钟 − 累计暂停 − 在途暂停），陪玩上报量保留原值由客服核实。
//
// 为什么必须含 paused（P1-03）：改派 / 换人时被换下的陪玩可能正处于暂停中，只匹配 in_service
// 会让这段服务完全不结算；更糟的是该段仍是 paused，被换下的人还能 resume 它继续累加时长、
// 污染结算基数。因此这里一次性把该陪玩在本席位的进行中服务段全部终结，且状态落到 finished，
// 恢复入口（applySegmentPauseChange 要求 status=paused）随即拒绝任何 resume / 恢复。
//
// 没有进行中服务段时是空操作（幂等）。
func settleSeatServiceSegment(ctx context.Context, tx *ent.Tx, orderID, playerID, seatIndex int, now time.Time) error {
	segments, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
		esportsservicesegment.PlayerIDEQ(playerID),
		esportsservicesegment.SeatIndexEQ(seatIndex),
		esportsservicesegment.StatusIn(
			esportsservicesegment.StatusInService, esportsservicesegment.StatusPaused),
	).All(ctx)
	if err != nil {
		return err
	}
	for _, segment := range segments {
		// 暂停中的服务段：在途暂停时长要先落库（paused_seconds），有效秒数才能扣干净。
		pausedSeconds := segment.PausedSeconds
		if segment.Status == esportsservicesegment.StatusPaused && segment.PausedAt != nil {
			if pending := int64(now.UTC().Sub(segment.PausedAt.UTC()).Seconds()); pending > 0 {
				pausedSeconds += pending
			}
		}
		systemSeconds := int64(now.UTC().Sub(segment.StartedAt.UTC()).Seconds()) - pausedSeconds
		if systemSeconds < 0 {
			systemSeconds = 0
		}
		changed, err := tx.EsportsServiceSegment.Update().Where(
			esportsservicesegment.IDEQ(segment.ID),
			esportsservicesegment.VersionEQ(segment.Version),
			esportsservicesegment.StatusIn(
				esportsservicesegment.StatusInService, esportsservicesegment.StatusPaused),
		).SetStatus(esportsservicesegment.StatusFinished).SetEndedAt(now.UTC()).
			SetSystemSeconds(systemSeconds).SetPausedSeconds(pausedSeconds).
			ClearPausedAt().AddVersion(1).Save(ctx)
		if err != nil {
			return err
		}
		if changed != 1 {
			return errConflict
		}
	}
	return nil
}

// refillVacantMultiSeatSeats 多席订单缺员自动补位，由派单升级扫描任务周期调用。
//
// 口径（可验收断言）：
//   - 只处理 required_seats > 1 且仍在途（待派单 / 待接单 / 已接单 / 服务中）的订单；
//   - 只重开「已超时」的失效席位：席位状态为 rejected / expired / revoked 且 responded_at
//     早于 app.esports.dispatch_refill_minutes（默认 10）之前 —— 未超时前保留原状态作为历史
//     （与既有「多席部分拒绝保留 rejected」口径一致）；
//   - 重开时跳过已产生服务段的席位（实际开过工不能清空陪玩）；
//   - 每次实际重开写一条 order.seat_refill_required 事件（抬订单版本承载唯一事件），
//     客服据此可立即重新指派；缺员本身还会由 runDispatchEscalations 升级为异常待办。
//
// 返回本次补位的席位数。
func refillVacantMultiSeatSeats(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	minutes := dispatchRefillMinutes(ctx, db)
	if minutes <= 0 {
		return 0, nil
	}
	if limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	cutoff := now.UTC().Add(-time.Duration(minutes) * time.Minute)
	orderIDs, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.StatusIn(
			esportsdispatchseat.StatusRejected, esportsdispatchseat.StatusExpired,
			esportsdispatchseat.StatusRevoked),
		esportsdispatchseat.RespondedAtNotNil(),
		esportsdispatchseat.RespondedAtLTE(cutoff),
	).Order(ent.Asc(esportsdispatchseat.FieldOrderID)).
		Limit(limit * 4).Select(esportsdispatchseat.FieldOrderID).All(ctx)
	if err != nil {
		return 0, err
	}
	if len(orderIDs) == 0 {
		return 0, nil
	}
	ids := make([]int, 0, len(orderIDs))
	seen := map[int]bool{}
	for _, row := range orderIDs {
		if !seen[row.OrderID] {
			seen[row.OrderID] = true
			ids = append(ids, row.OrderID)
		}
	}
	total := 0
	for _, orderID := range ids {
		if total >= limit {
			break
		}
		changed, err := refillMultiSeatOrder(ctx, db, orderID, cutoff, now)
		if err != nil {
			if err == errConflict {
				continue
			}
			return total, err
		}
		total += changed
	}
	return total, nil
}

// refillMultiSeatOrder 在独立事务内为一张多席订单重开超时失效的席位并留事件。
func refillMultiSeatOrder(ctx context.Context, db *ent.Client, orderID int, cutoff, now time.Time) (int, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return 0, err
	}
	if order.RequiredSeats <= 1 {
		return 0, nil
	}
	switch order.FulfillmentStatus {
	case esportsorder.FulfillmentStatusPendingDispatch, esportsorder.FulfillmentStatusPendingAcceptance,
		esportsorder.FulfillmentStatusAccepted, esportsorder.FulfillmentStatusInService:
	default:
		return 0, nil
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return 0, nil
	}
	stale, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.StatusIn(
			esportsdispatchseat.StatusRejected, esportsdispatchseat.StatusExpired,
			esportsdispatchseat.StatusRevoked),
		esportsdispatchseat.RespondedAtNotNil(),
		esportsdispatchseat.RespondedAtLTE(cutoff),
	).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).All(ctx)
	if err != nil {
		return 0, err
	}
	if len(stale) == 0 {
		return 0, nil
	}
	reopened := make([]int, 0, len(stale))
	for _, seat := range stale {
		if seat.PlayerID != nil {
			started, err := tx.EsportsServiceSegment.Query().Where(
				esportsservicesegment.OrderIDEQ(orderID),
				esportsservicesegment.PlayerIDEQ(*seat.PlayerID),
				esportsservicesegment.SeatIndexEQ(seat.SeatIndex),
			).Exist(ctx)
			if err != nil {
				return 0, err
			}
			if started {
				continue // 实际开过工的席位不能清空陪玩
			}
		}
		changed, err := tx.EsportsDispatchSeat.Update().Where(
			esportsdispatchseat.IDEQ(seat.ID),
			esportsdispatchseat.VersionEQ(seat.Version),
			esportsdispatchseat.StatusIn(
				esportsdispatchseat.StatusRejected, esportsdispatchseat.StatusExpired,
				esportsdispatchseat.StatusRevoked),
		).ClearPlayerID().ClearExpiresAt().ClearRespondedAt().
			ClearCommissionRuleID().ClearCommissionRuleLockedAt().
			// ：补位席位回到 open，上一轮的发出时间与时段一并清空。
			ClearInvitedAt().ClearSlotStart().ClearSlotEnd().
			SetStatus(esportsdispatchseat.StatusOpen).AddVersion(1).Save(ctx)
		if err != nil {
			return 0, err
		}
		if changed == 1 {
			reopened = append(reopened, seat.SeatIndex)
		}
	}
	if len(reopened) == 0 {
		return 0, nil
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, order.Version, "")
	if err != nil {
		return 0, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.seat_refill_required").
		SetActorType("system").SetActorID("seat_refill").
		SetDetails(map[string]interface{}{"refilled_seat_indexes": reopened}).Save(ctx); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(reopened), nil
}

// assignDispatchSeat 指派陪玩到某一席位：校验 SKU 快照、资质、分区、等级、连接与容量，
// 条件更新抢下 open 席位并邀请（服务中订单则换人并结算原服务段）。订单、席位、陪玩版本和事件同事务写入。
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
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.PaymentStatus != esportsorder.PaymentStatusPaid {
		return nil, errConflict
	}
	// 服务中换人：服务中订单允许改派（先结算原陪玩服务段再换人）；
	// 其余订单仍只允许在待派单 / 待接单阶段派单。
	inServiceReplace := order.FulfillmentStatus == esportsorder.FulfillmentStatusInService
	if !inServiceReplace &&
		order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch &&
		order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance {
		return nil, errConflict
	}
	if err = ensureAppointmentConfirmed(order); err != nil {
		return nil, err
	}
	if err = ensureRequiredSeatsIntact(ctx, tx.Client(), orderID); err != nil {
		return nil, err
	}
	// ：改期提案进行中时订单档期待切换，任何新派单 / 改派都会让提案快照失效。
	if locked, err := pendingRescheduleProposalExists(ctx, tx, orderID, now); err != nil {
		return nil, err
	} else if locked {
		return nil, fmt.Errorf("%w：该订单存在进行中的改期提案，请先确认或撤回提案", errConflict)
	}
	// ：SKU 快照必须仍可售，否则会静默跳过分区权限校验。
	if err = ensureOrderSkuDispatchable(ctx, tx, order); err != nil {
		return nil, err
	}
	player, err := tx.EsportsPlayer.Get(ctx, playerID)
	if err != nil {
		return nil, err
	}
	eligible, err := playerEligibleForDispatch(ctx, tx.Client(), player, now)
	if err != nil {
		return nil, err
	}
	if !eligible {
		return nil, errForbidden
	}
	if err = reservePlayerDispatchCapacity(ctx, tx, player, order, now, 0); err != nil {
		return nil, err
	}
	game, err := orderGameIn(ctx, tx, order.SkuID)
	if err != nil {
		return nil, err
	}
	if err = checkPlayerGameLevel(skillLevels(ctx, tx.Client()), player, game, order.RequiredLevel); err != nil {
		return nil, err
	}
	// ：服务时段外 / 可接单价范围外的陪玩不得被指派（给出明确中文原因）。
	if err = playerAcceptsDispatchTerms(player, order, now); err != nil {
		return nil, err
	}
	if err = ensureDispatchSeats(ctx, tx, order); err != nil {
		return nil, err
	}
	// 服务中换人：记下被替换的原陪玩，待席位改派成功后结算其进行中的服务段。
	replacedPlayerID := 0
	reusableStatuses := []esportsdispatchseat.Status{
		esportsdispatchseat.StatusOpen, esportsdispatchseat.StatusRejected,
		esportsdispatchseat.StatusExpired, esportsdispatchseat.StatusRevoked,
	}
	if inServiceReplace {
		reusableStatuses = append(reusableStatuses, esportsdispatchseat.StatusAccepted)
		existing, err := tx.EsportsDispatchSeat.Query().Where(
			esportsdispatchseat.OrderIDEQ(orderID), esportsdispatchseat.SeatIndexEQ(seatIndex),
		).Only(ctx)
		if err != nil {
			return nil, err
		}
		if existing.Status == esportsdispatchseat.StatusAccepted && existing.PlayerID != nil &&
			*existing.PlayerID != playerID {
			replacedPlayerID = *existing.PlayerID
		}
	}
	// 可复用 open 席位，也可接管此前被收回的 rejected / expired / revoked 席位：
	// 这些席位未开工，派单回退后若不允许重新占用，单席订单会永久卡死。
	// ：发出邀请时落 invited_at，并按订单档期落本席服务时段（即时单无档期则留空）。
	inviteUpdate := tx.EsportsDispatchSeat.Update().Where(
		esportsdispatchseat.OrderIDEQ(orderID), esportsdispatchseat.SeatIndexEQ(seatIndex),
		esportsdispatchseat.StatusIn(reusableStatuses...),
	).SetPlayerID(playerID).SetStatus(esportsdispatchseat.StatusInvited).
		SetSource(esportsdispatchseat.SourceAssign).SetInvitedByAdminID(adminID).
		SetExpiresAt(now.UTC().Add(ttl)).ClearRespondedAt().
		ClearCommissionRuleID().ClearCommissionRuleLockedAt().AddVersion(1)
	inviteUpdate = applySeatServiceSlot(inviteUpdate, order)
	changed, err := inviteUpdate.SetInvitedAt(now.UTC()).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	// ：服务中换人先把原陪玩的服务段结算（保留历史），再让新陪玩接单。
	if replacedPlayerID > 0 {
		if err = settleSeatServiceSegment(ctx, tx, orderID, replacedPlayerID, seatIndex, now); err != nil {
			return nil, err
		}
	}
	// 服务中换人后的订单落点：仍有其他进行中服务段则保持服务中，否则回到待接单等新陪玩接单。
	targetStatus := esportsorder.FulfillmentStatusPendingAcceptance
	if inServiceReplace {
		remainingInService, err := orderHasInServiceSegment(ctx, tx, orderID)
		if err != nil {
			return nil, err
		}
		if !remainingInService {
			targetStatus = esportsorder.FulfillmentStatusPendingAcceptance
		} else {
			targetStatus = esportsorder.FulfillmentStatusInService
		}
	}
	// P1-05-01：人工指派一旦确定陪玩，立即闭合该订单的优先通知窗口（状态置结束、
	// 名单内其他人的待回应通知作废），既不再计入看板「通知中」，也不会再有人看到
	// 注定接不了的通知。闭合会占用一个订单版本，后续 bumpOrder 必须基于闭合后的版本。
	orderVersion := order.Version
	if closedVersion, err := closeDispatchPriorityWindowOnDispatch(ctx, tx, order,
		dispatchReleaseReasonAssigned, now); err != nil {
		return nil, err
	} else if closedVersion > 0 {
		orderVersion = closedVersion
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, orderVersion, targetStatus)
	if err != nil {
		return nil, err
	}
	eventType := "order.seat_invited"
	details := map[string]interface{}{"seat_index": seatIndex, "player_id": playerID}
	if replacedPlayerID > 0 {
		// 席位版本唯一约束下每次派单只承载一个订单事件，服务中换人合并为一条换人事件。
		eventType = "order.service_player_replaced"
		details["replaced_player_id"] = replacedPlayerID
	}
	// ：首次派出邀请的时间点（只写一次）。
	if _, err = markOrderTimelineIn(ctx, tx, orderID, timelineFirstInvited, now); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType(eventType).
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(details).Save(ctx); err != nil {
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

// revokeDispatchSeat 撤回席位：席位转 revoked，若订单再无有效席位则退回待派单。
//
// P1-02：除「邀请中」的席位外，**已接单但从未开工**（该席位没有任何服务段）的席位也允许由客服移除——
// 这是「从未开工的席位须有明确处置」的处置入口。已开工的席位必须走「终止服务段」（登记责任与已履约量），
// 否则会绕过责任认定直接抹掉在途履约。
// 移除后若整单再无未了结席位且在服务中，订单直接收敛到待结单（P1-02 的判定口径见 orderAllSeatsServiceSettled）。
func revokeDispatchSeat(ctx context.Context, db *ent.Client, orderID, seatIndex, adminID int, reason string) (*ent.EsportsDispatchSeat, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	now := time.Now().UTC()
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
	seat, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID), esportsdispatchseat.SeatIndexEQ(seatIndex),
	).Only(ctx)
	if err != nil {
		return nil, err
	}
	switch seat.Status {
	case esportsdispatchseat.StatusInvited:
	case esportsdispatchseat.StatusAccepted:
		// 已接单席位：只有「从未开工」的才允许移除；已开工必须走终止服务段。
		hasSegment, err := tx.EsportsServiceSegment.Query().Where(
			esportsservicesegment.OrderIDEQ(orderID),
			esportsservicesegment.SeatIndexEQ(seatIndex),
		).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if hasSegment {
			return nil, fmt.Errorf("%w：该席位的陪玩已开工，不能直接撤回，请先用「终止服务段」登记责任与已履约量", errConflict)
		}
	default:
		return nil, errConflict
	}
	update := tx.EsportsDispatchSeat.Update().Where(
		esportsdispatchseat.IDEQ(seat.ID),
		esportsdispatchseat.VersionEQ(seat.Version),
		esportsdispatchseat.StatusEQ(seat.Status),
	).SetStatus(esportsdispatchseat.StatusRevoked).
		SetRespondedAt(now).AddVersion(1)
	if seat.Status == esportsdispatchseat.StatusAccepted {
		// 移除已接单席位：清空陪玩与接单时锁定的分成规则快照。
		update = update.ClearPlayerID().ClearCommissionRuleID().ClearCommissionRuleLockedAt()
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	target := esportsorder.FulfillmentStatus("")
	remainingActive, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.StatusIn(
			esportsdispatchseat.StatusInvited, esportsdispatchseat.StatusAccepted),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !remainingActive && (order.FulfillmentStatus == esportsorder.FulfillmentStatusPendingAcceptance ||
		order.FulfillmentStatus == esportsorder.FulfillmentStatusAccepted) {
		target = esportsorder.FulfillmentStatusPendingDispatch
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, order.Version, target)
	if err != nil {
		return nil, err
	}
	// 退回待派单：优先通知已开过窗时直接释放到大厅，否则按名单重新开窗（业务口径）。
	if target == esportsorder.FulfillmentStatusPendingDispatch {
		if err = onOrderEnteredPendingDispatch(ctx, tx, orderID, now); err != nil {
			return nil, err
		}
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.seat_revoked").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"seat_index": seatIndex, "reason": reason, "previous_status": seat.Status.String(),
		}).Save(ctx); err != nil {
		return nil, err
	}
	// P1-02：移除从未开工的席位后，若整单已无未了结席位且在服务中，直接转入待结单。
	if order.FulfillmentStatus == esportsorder.FulfillmentStatusInService {
		settled, _, err := orderAllSeatsServiceSettled(ctx, tx, orderID)
		if err != nil {
			return nil, err
		}
		if settled {
			current, err := tx.EsportsOrder.Get(ctx, orderID)
			if err != nil {
				return nil, err
			}
			closeVersion, err := bumpOrder(ctx, tx, orderID, current.Version,
				esportsorder.FulfillmentStatusPendingClose)
			if err != nil {
				return nil, err
			}
			if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
				SetOrderVersion(closeVersion).SetEventType(orderEnteredPendingCloseEvent).
				SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
				SetDetails(map[string]interface{}{
					"reason": "seat_removed", "seat_index": seatIndex,
				}).Save(ctx); err != nil {
				return nil, err
			}
		}
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
	if err = ensureOrderNotFrozen(order); err != nil {
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
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.FulfillmentStatus == esportsorder.FulfillmentStatusCompleted ||
		order.FulfillmentStatus == esportsorder.FulfillmentStatusCancelled {
		return nil, errConflict
	}
	// ：改期提案进行中时锁定订单档期，转单会改变负责人并让提案快照失效。
	if locked, err := pendingRescheduleProposalExists(ctx, tx, orderID, time.Now()); err != nil {
		return nil, err
	} else if locked {
		return nil, fmt.Errorf("%w：该订单存在进行中的改期提案，请先确认或撤回提案", errConflict)
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

// remindOrder 催单：把优先级（1~3）、备注与催单时间落到**订单字段**（，可检索、可排序），
// 同时抬升订单版本写 order.reminded 事件留痕；不改履约状态。
// 同一订单多次催单以「最新一次」为准（档位 / 备注 / 时间一并覆盖）。
// ponytail: 催单时间阶梯、升级自动通知与企微/电话等多渠道提醒未实现。
func remindOrder(ctx context.Context, db *ent.Client, orderID, adminID int, level int, note string) error {
	note = strings.TrimSpace(note)
	if level < 1 || level > 3 || len([]rune(note)) > 255 {
		return errInvalidInput
	}
	now := time.Now().UTC()
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
	// ：催单会改订单字段（remind_priority_level / remind_note / reminded_at）并抬升订单版本，
	// 属订单写操作，存单冻结期间必须拒绝；与同文件的转单路径保持同一守卫口径。
	if err = ensureOrderNotFrozen(order); err != nil {
		return err
	}
	// 一次条件更新同时抬版本与落催单字段，避免「事件写了字段没写」的半成品状态。
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
	).SetRemindPriorityLevel(level).SetRemindNote(note).SetRemindedAt(now).
		AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(order.Version + 1).SetEventType("order.reminded").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{"level": level, "note": note}).Save(ctx); err != nil {
		return err
	}
	// P1-04：催单必须**真的触达**被催方，不能只写订单字段与订单事件。
	// 在同一事务内写事务外盒：事件类型沿用通知分拣已注册的 dispatch.remind_player /
	// dispatch.remind_admin（见 notification_outbox.go / notification_recipients.go），
	// 由投递器落成站内信 + 推送；未配置通道时至少留可查的投递事件。
	// 事件键含订单 ID 与本次催单占用的订单版本，天然幂等：同一次催单重复投递只留一条。
	remindedVersion := order.Version + 1
	if err = writeManualRemindOutbox(ctx, tx, order, adminID, level, note, remindedVersion, now); err != nil {
		return err
	}
	return tx.Commit()
}

// writeManualRemindOutbox 在催单事务内写手动催单的通知外盒（P1-04）：
//   - dispatch.remind_player：逐席定向通知被催的陪玩（payload 带 player_id / seat_index）；
//   - dispatch.remind_admin：通知责任客服（无负责客服时回落本次操作客服）。
//
// 事件键 dispatch_remind_manual:<订单ID>:<订单版本>:player:<席位|admin>，同一次催单幂等。
func writeManualRemindOutbox(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder,
	adminID, level int, note string, version int, now time.Time) error {
	seats, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(order.ID),
		esportsdispatchseat.StatusIn(
			esportsdispatchseat.StatusInvited, esportsdispatchseat.StatusAccepted),
		esportsdispatchseat.PlayerIDNotNil(),
	).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).All(ctx)
	if err != nil {
		return err
	}
	keyPrefix := "dispatch_remind_manual:" + strconv.Itoa(order.ID) + ":" + strconv.Itoa(version)
	for _, seat := range seats {
		if seat.PlayerID == nil {
			continue
		}
		if _, err = tx.EsportsOutbox.Create().
			SetEventKey(keyPrefix + ":player:" + strconv.Itoa(seat.SeatIndex)).
			SetEventType("dispatch.remind_player").SetAggregateRef(order.OrderNo).
			SetPayload(map[string]interface{}{
				"order_no": order.OrderNo, "order_id": order.ID,
				"player_id": *seat.PlayerID, "seat_index": seat.SeatIndex,
				"level": level, "note": note, "source": "manual",
				"reminded_at": now.UTC(),
			}).Save(ctx); err != nil {
			if !ent.IsConstraintError(err) {
				return err
			}
		}
	}
	// 责任客服优先；订单无负责客服时退回本次催单的操作客服，保证催单一定有人收到。
	adminTarget := adminID
	if order.ResponsibleAdminID != nil && *order.ResponsibleAdminID > 0 {
		adminTarget = *order.ResponsibleAdminID
	}
	if adminTarget < 1 {
		return nil
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey(keyPrefix + ":admin").
		SetEventType("dispatch.remind_admin").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "order_id": order.ID, "admin_id": adminTarget,
			"level": level, "note": note, "source": "manual", "reminded_at": now.UTC(),
		}).Save(ctx); err != nil {
		if !ent.IsConstraintError(err) {
			return err
		}
	}
	return nil
}

// publishOrderToGrabHall 客服把已付款的待派单订单发布到抢单大厅。
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
	if err = ensureOrderNotFrozen(order); err != nil {
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
	// ：兜底确保「首次进入待派单池」的时间点已落（订单处于待派单即视为已入池，只写一次）。
	// 此处只补时间点：存量订单直接发布到大厅时不再补写入池事件（订单已离开待派单池，避免事件语义错位）。
	if _, err = markOrderTimelineIn(ctx, tx, orderID, timelineFirstPooled, now); err != nil {
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

// reopenDeadDispatchSeatsForRedispatch 把订单下尚未开工的 rejected / expired / revoked 席位
// 统一重置为 open，并清空陪玩、邀请、回应时间与分成规则快照，返回被重置的席位序号。
//
// 调用时机是订单层面的「回到可派单状态」：回退待派单、释放到抢单大厅、发布到大厅。
// 这样单席订单被拒/过期/撤回后不会永久无 open 席位；多席订单回退时也能整体补位。
// 只更新 dead 状态，accepted / invited / open 席位不受影响；历史仍保留在原有的
// order.seat_rejected / order.seat_expired / order.seat_revoked 订单事件里。
// 已产生服务段的旧关联不重置，避免把实际开过工的陪玩错误清空。
func reopenDeadDispatchSeatsForRedispatch(ctx context.Context, tx *ent.Tx, orderID int) ([]int, error) {
	deadStatuses := []esportsdispatchseat.Status{
		esportsdispatchseat.StatusRejected,
		esportsdispatchseat.StatusExpired,
		esportsdispatchseat.StatusRevoked,
	}
	seats, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.StatusIn(deadStatuses...),
	).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).All(ctx)
	if err != nil {
		return nil, err
	}
	reopened := make([]int, 0, len(seats))
	for _, seat := range seats {
		if seat.PlayerID != nil {
			started, err := tx.EsportsServiceSegment.Query().Where(
				esportsservicesegment.OrderIDEQ(orderID),
				esportsservicesegment.PlayerIDEQ(*seat.PlayerID),
				esportsservicesegment.SeatIndexEQ(seat.SeatIndex),
			).Exist(ctx)
			if err != nil {
				return nil, err
			}
			if started {
				continue
			}
		}
		changed, err := tx.EsportsDispatchSeat.Update().Where(
			esportsdispatchseat.IDEQ(seat.ID),
			esportsdispatchseat.VersionEQ(seat.Version),
			esportsdispatchseat.StatusIn(deadStatuses...),
		).ClearPlayerID().ClearExpiresAt().ClearRespondedAt().
			ClearCommissionRuleID().ClearCommissionRuleLockedAt().
			// ：席位回到 open 即清空上一轮的发出时间与时段，重新邀请时再写。
			ClearInvitedAt().ClearSlotStart().ClearSlotEnd().
			SetStatus(esportsdispatchseat.StatusOpen).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed == 1 {
			reopened = append(reopened, seat.SeatIndex)
		}
	}
	return reopened, nil
}

// reopenDispatchSeatsForGrab 保留旧入口语义：把订单下已被回收的席位重置为 open，
// 使订单发布到抢单大厅后一定存在可抢席位。
func reopenDispatchSeatsForGrab(ctx context.Context, tx *ent.Tx, orderID int) error {
	_, err := reopenDeadDispatchSeatsForRedispatch(ctx, tx, orderID)
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
	order, err := tx.EsportsOrder.Get(ctx, seat.OrderID)
	if err != nil {
		return false, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		// 冻结订单禁止席位/履约状态变更：跳过本次过期，待恢复后再由下一轮收敛。
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
	order, err = tx.EsportsOrder.Get(ctx, seat.OrderID)
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
	// 退回待派单：优先通知已开过窗时直接释放到大厅，否则按名单重新开窗（业务口径）。
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

// dispatchBoardAdmin 是看板顶部的客服在线摘要：客服清单 + 在线状态 + 最近活跃时间。
type dispatchBoardAdmin struct {
	AdminID  int    `json:"admin_id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	// Presence 取 presence 表口径（online / busy / offline）；无 presence 档案时按最近操作推导为 online / offline。
	Presence string `json:"presence"`
	Online   bool   `json:"online"`
	// LastActiveAt 是「presence 表 last_active_at」与「Admin.last_login / 最近一次管理员订单操作」的较大者。
	LastActiveAt *time.Time `json:"last_active_at"`
}

// dispatchBoardPlayer 是看板的逐人上下线明细条目。
type dispatchBoardPlayer struct {
	PlayerID int    `json:"player_id"`
	Nickname string `json:"nickname"`
	// Presence 取 EsportsPlayer.presence 原文（online / offline / protected_abnormal），
	// 与 online_players 计数**同一字段、同一口径**，不另立在线判定。
	Presence string `json:"presence"`
	// PresenceLabel 是 presence 的可直接展示文案（P1-01）：只有 offline 才叫「已下线」，
	// protected_abnormal 展示为「连接异常（受保护）」，避免看板把在途履约者显示成已下线。
	PresenceLabel string `json:"presence_label"`
	// LastActiveAt 是最近一次心跳时间（无心跳基线的存量档案为 null）。
	LastActiveAt *time.Time `json:"last_active_at"`
	// ConnectionHealthy 复用既有 playerConnectionHealthy 口径，仅供客服识别
	// 「presence=online 但心跳已失联」的挂机档案；presence 非 online 时该值无业务含义。
	ConnectionHealthy bool `json:"connection_healthy"`
}

// dispatchBoardPlayerLimit 是看板逐人明细的条数上限（防止响应体随陪玩规模膨胀）；
// 超出时 players_truncated=true，online_players 计数仍为全量真实值。
const dispatchBoardPlayerLimit = 200

// dispatchBoard 排单看板：按履约状态分桶的订单摘要、在线陪玩数与客服在线清单。
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
	// Players 是逐人上下线明细：在线优先、同类按 ID 升序。
	Players []dispatchBoardPlayer `json:"players"`
	// PlayersTruncated 表示合格陪玩总数超过明细上限，明细不全（计数仍为全量真实值）。
	PlayersTruncated bool `json:"players_truncated"`
	// Admins 是各客服及其在线状态，供看板顶部展示「谁在值班」。
	Admins []dispatchBoardAdmin `json:"admins"`
	// 会员订单优先：待派单池 / 公共池中属于「会员优先等级」客户的订单 ID，已置顶，供前端标记。
	MemberPriorityOrderIDs []int `json:"member_priority_order_ids"`
	// 会员专属通道（P1-11 消费点）：待派单池 / 公共池中**因「专属通道」权益被二级置顶**的订单 ID
	// （排在会员优先档之后、普通订单之前）。同时命中会员优先派单的订单只出现在 member_priority_order_ids。
	MemberDedicatedChannelOrderIDs []int `json:"member_dedicated_channel_order_ids"`
	// 公共池脱敏：非超管在公共池只看到脱敏摘要，客户身份与金额字段被掩码；
	// 此字段列出被掩码的订单 ID，供前端以「***」展示。超管为空（看完整数据）。
	MaskedPublicPoolIDs []int `json:"masked_public_pool_ids"`
	// CustomerNames 是各订单桶里出现过的客户 ID → 客户显示名，供看板直接展示客户名、免二次查询。
	// 在脱敏之后生成：公共池订单的 customer_id 已被置 0，带不出被掩码客户的名字。
	CustomerNames map[int]string `json:"customer_names"`
}

// loadDispatchBoardPlayers 组装看板的逐人上下线明细：
//   - 只列合格陪玩（与 online_players 计数的资格口径一致）；
//   - 在线（presence=online，与计数同一字段）优先，其余合格陪玩补足，同类按 ID 升序；
//   - 最多 dispatchBoardPlayerLimit 条，超出时第二个返回值为 true。
func loadDispatchBoardPlayers(ctx context.Context, db *ent.Client, now time.Time) ([]dispatchBoardPlayer, bool, error) {
	qualified := esportsplayer.QualificationEQ(esportsplayer.QualificationQualified)
	total, err := db.EsportsPlayer.Query().Where(qualified).Count(ctx)
	if err != nil {
		return nil, false, err
	}
	if total == 0 {
		return []dispatchBoardPlayer{}, false, nil
	}
	out := make([]dispatchBoardPlayer, 0, dispatchBoardPlayerLimit)
	// 心跳超时阈值只解析一次，避免逐人判定时反复查配置（口径仍由 playerConnectionHealthyWith 统一）。
	heartbeatTimeout := playerHeartbeatTimeout(ctx, db)
	appendRows := func(rows []*ent.EsportsPlayer) {
		for _, player := range rows {
			nickname := strings.TrimSpace(player.DisplayName)
			out = append(out, dispatchBoardPlayer{
				PlayerID:          player.ID,
				Nickname:          nickname,
				Presence:          player.Presence.String(),
				PresenceLabel:     playerPresenceLabel(player.Presence),
				LastActiveAt:      player.LastHeartbeatAt,
				ConnectionHealthy: playerConnectionHealthyWith(player, now, heartbeatTimeout),
			})
		}
	}
	// ① 在线（与 online_players 计数完全同一过滤条件）。
	online, err := db.EsportsPlayer.Query().Where(
		qualified, esportsplayer.PresenceEQ(esportsplayer.PresenceOnline),
	).Order(ent.Asc(esportsplayer.FieldID)).Limit(dispatchBoardPlayerLimit).All(ctx)
	if err != nil {
		return nil, false, err
	}
	appendRows(online)
	// ② 其余合格陪玩（离线 / 连接异常）补足剩余名额。
	if remaining := dispatchBoardPlayerLimit - len(out); remaining > 0 {
		rest, err := db.EsportsPlayer.Query().Where(
			qualified, esportsplayer.PresenceNEQ(esportsplayer.PresenceOnline),
		).Order(ent.Asc(esportsplayer.FieldID)).Limit(remaining).All(ctx)
		if err != nil {
			return nil, false, err
		}
		appendRows(rest)
	}
	return out, total > len(out), nil
}

// loadDispatchBoardAdmins 组装客服在线清单：列出启用态管理员，合并 presence 表
// 与「最近操作时间」推导出在线状态与最近活跃时间。presence 表（EsportsCsPresence）是权威数据源；
// 无档案的客服回落「最近一次管理员操作」口径，让看板在 presence 写入方上线前也可用。
func loadDispatchBoardAdmins(ctx context.Context, db *ent.Client, now time.Time) ([]dispatchBoardAdmin, error) {
	accounts, err := db.Admin.Query().Where(
		entadmin.StatusEQ(entadmin.StatusActive),
	).Order(ent.Asc(entadmin.FieldID)).Limit(200).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, nil
	}
	ids := make([]int, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	activities, err := csAdminActivities(ctx, db, ids, now)
	if err != nil {
		return nil, err
	}
	out := make([]dispatchBoardAdmin, 0, len(accounts))
	for _, account := range accounts {
		activity := activities[account.ID]
		nickname := strings.TrimSpace(account.Nickname)
		if nickname == "" {
			nickname = strings.TrimSpace(account.Name)
		}
		out = append(out, dispatchBoardAdmin{
			AdminID: account.ID, Username: account.Username, Nickname: nickname,
			Presence: activity.Presence, Online: activity.Online,
			LastActiveAt: activity.LastActiveAt,
		})
	}
	return out, nil
}

// dispatchBoardSortRemindPriority 看板排序参数取值：按催单优先级降序。
const dispatchBoardSortRemindPriority = "remind_priority"

// loadDispatchBoard 组装看板（默认顺序）。每桶按 ID 倒序取前 100 条；非超管只看自己负责的订单，
// 但公共池始终返回作为脱敏摘要（非超管掩码客户身份与金额字段）。
func loadDispatchBoard(ctx context.Context, db *ent.Client, adminID int, isSuper bool) (*dispatchBoard, error) {
	return loadDispatchBoardSorted(ctx, db, adminID, isSuper, "")
}

// loadDispatchBoardSorted 组装看板，支持 sort 参数：
//   - ""（默认）：各桶按订单 ID 倒序（既有行为，逐位不变）；
//   - "remind_priority"：各订单桶按「催单优先级降序 → 订单 ID 倒序」重排，便于客服先处理被催的单；
//   - 其他取值：明确参数错误（不得静默忽略，避免调用方误以为已排序）。
//
// 排序只影响展示顺序：可见性（非超管只看本人名下）、会员订单置顶标记、脱敏口径都不受影响。
func loadDispatchBoardSorted(ctx context.Context, db *ent.Client, adminID int, isSuper bool,
	sortBy string) (*dispatchBoard, error) {
	sortBy = strings.TrimSpace(sortBy)
	if sortBy != "" && sortBy != dispatchBoardSortRemindPriority {
		return nil, fmt.Errorf("%w：sort 仅支持 %s", errInvalidInput, dispatchBoardSortRemindPriority)
	}
	bucket := func(status esportsorder.FulfillmentStatus) ([]*ent.EsportsOrder, error) {
		q := db.EsportsOrder.Query().Where(esportsorder.FulfillmentStatusEQ(status))
		if !isSuper {
			q.Where(esportsorder.ResponsibleAdminIDEQ(adminID))
		}
		return q.Order(ent.Desc(esportsorder.FieldID)).Limit(100).All(ctx)
	}
	now := time.Now()
	var err error
	board := &dispatchBoard{}
	if board.Admins, err = loadDispatchBoardAdmins(ctx, db, now); err != nil {
		return nil, err
	}
	if board.PendingDispatch, err = bucket(esportsorder.FulfillmentStatusPendingDispatch); err != nil {
		return nil, err
	}
	// 会员订单优先+ 专属通道二级置顶：只影响展示顺序，
	// 不绕过资质、档期与接单上限等硬性条件。
	pendingPriorityIDs, pendingChannelIDs, sortedPending, err := prioritizeMemberOrders(ctx, db, board.PendingDispatch)
	if err != nil {
		return nil, err
	}
	board.MemberPriorityOrderIDs, board.PendingDispatch = pendingPriorityIDs, sortedPending
	board.MemberDedicatedChannelOrderIDs = pendingChannelIDs
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
	pubPriorityIDs, pubChannelIDs, sortedPool, err := prioritizeMemberOrders(ctx, db, board.PublicPool)
	if err != nil {
		return nil, err
	}
	board.PublicPool = sortedPool
	// 公共池是待派单池的子集，标记按订单去重。
	board.MemberPriorityOrderIDs = appendUniqueIDs(board.MemberPriorityOrderIDs, pubPriorityIDs)
	board.MemberDedicatedChannelOrderIDs = appendUniqueIDs(
		board.MemberDedicatedChannelOrderIDs, pubChannelIDs)
	// 公共池脱敏：非超管只看到脱敏摘要，掩码客户身份与金额字段。
	if !isSuper {
		for _, o := range board.PublicPool {
			maskPublicPoolOrder(o)
			board.MaskedPublicPoolIDs = append(board.MaskedPublicPoolIDs, o.ID)
		}
	}
	// 异常桶：支付异常 ∪ 非支付类派单异常（临近开始未满员 / 无人接单超时）。
	if board.Abnormal, err = dispatchAbnormalOrders(ctx, db, adminID, isSuper, now); err != nil {
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
	// ：逐人上下线明细（与上面的在线计数同一 presence 口径）。
	if board.Players, board.PlayersTruncated, err = loadDispatchBoardPlayers(ctx, db, now); err != nil {
		return nil, err
	}
	// ：显式要求按催单优先级排序时，重排各订单桶（默认不动，保持既有顺序）。
	if sortBy == dispatchBoardSortRemindPriority {
		sortBoardByRemindPriority(board)
	}
	// 客户名批量补全（一次查询）；公共池脱敏订单的 customer_id 已为 0，安全跳过。
	if board.CustomerNames, err = dispatchBoardCustomerNames(ctx, db, board); err != nil {
		return nil, err
	}
	return board, nil
}

// dispatchBoardCustomerNames 收集各订单桶的客户 ID 并批量取显示名
// （必须在脱敏之后调用，被掩码订单的 customer_id 已是 0）。
func dispatchBoardCustomerNames(ctx context.Context, db *ent.Client,
	board *dispatchBoard) (map[int]string, error) {
	buckets := [][]*ent.EsportsOrder{
		board.PendingDispatch, board.PublicPool, board.Grabbing, board.PendingAcceptance,
		board.Accepted, board.InService, board.PendingClose, board.Abnormal,
	}
	ids := make([]int, 0, 64)
	for _, bucket := range buckets {
		for _, order := range bucket {
			ids = append(ids, order.CustomerID)
		}
	}
	return customerNamesOf(ctx, db, ids)
}

// sortBoardByRemindPriority 按催单优先级降序重排看板各订单桶。
// 未催单（level=0）的订单排在已催单之后；同档按订单 ID 倒序，与默认顺序一致，避免顺序抖动。
func sortBoardByRemindPriority(board *dispatchBoard) {
	if board == nil {
		return
	}
	buckets := [][]*ent.EsportsOrder{
		board.PendingDispatch, board.PublicPool, board.Grabbing, board.PendingAcceptance,
		board.Accepted, board.InService, board.PendingClose, board.Abnormal,
	}
	for _, bucket := range buckets {
		sort.SliceStable(bucket, func(i, j int) bool {
			a, b := bucket[i], bucket[j]
			if a.RemindPriorityLevel != b.RemindPriorityLevel {
				return a.RemindPriorityLevel > b.RemindPriorityLevel
			}
			return a.ID > b.ID
		})
	}
}

// dispatchAbnormalOrders 组装看板异常桶：除既有支付异常外，
// 把「非支付类派单异常」也纳入客服异常待办。判定按可观测数据**实时派生**，不依赖升级任务是否跑过：
//
//   - 临近开始仍未满员：预约开始时间落在 app.esports.dispatch_start_escalate_minutes 之内，
//     且已接单席位数不足 required_seats；
//   - 无人接单超时：进入待派单池（first_pooled_at）已超过 app.esports.dispatch_no_taker_minutes，
//     且席位仍未接满。
//
// 终态订单（completed / cancelled）一律不在异常桶内。返回上限 100 条（与其余桶一致）。
func dispatchAbnormalOrders(ctx context.Context, db *ent.Client, adminID int, isSuper bool, now time.Time) ([]*ent.EsportsOrder, error) {
	startLead := dispatchStartEscalateMinutes(ctx, db)
	noTaker := dispatchNoTakerMinutes(ctx, db)
	q := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusNotIn(
			esportsorder.FulfillmentStatusCompleted, esportsorder.FulfillmentStatusCancelled),
		esportsorder.Or(
			esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusException),
			esportsorder.FulfillmentStatusIn(
				esportsorder.FulfillmentStatusPendingDispatch,
				esportsorder.FulfillmentStatusPendingAcceptance),
		),
	)
	if !isSuper {
		q.Where(esportsorder.ResponsibleAdminIDEQ(adminID))
	}
	items, err := q.Order(ent.Desc(esportsorder.FieldID)).Limit(300).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return []*ent.EsportsOrder{}, nil
	}
	ids := make([]int, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	accepted := map[int]int{}
	seats, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDIn(ids...),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
	).Select(esportsdispatchseat.FieldOrderID).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, seat := range seats {
		accepted[seat.OrderID]++
	}
	startLeadBefore := now.UTC().Add(time.Duration(startLead) * time.Minute)
	noTakerBefore := now.UTC().Add(-time.Duration(noTaker) * time.Minute)
	out := make([]*ent.EsportsOrder, 0, len(items))
	for _, item := range items {
		if item.PaymentStatus == esportsorder.PaymentStatusException {
			out = append(out, item)
			continue
		}
		// 席位已接满的正常在途订单不算异常。
		if item.RequiredSeats > 0 && accepted[item.ID] >= item.RequiredSeats {
			continue
		}
		if item.RequestedStartAt != nil && !item.RequestedStartAt.After(startLeadBefore) {
			out = append(out, item)
			continue
		}
		if item.FirstPooledAt != nil && !item.FirstPooledAt.After(noTakerBefore) {
			out = append(out, item)
			continue
		}
	}
	if len(out) > 100 {
		out = out[:100]
	}
	return out, nil
}

// maskPublicPoolOrder 对公共池订单做脱敏：清零客户身份与金额字段，
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

// prioritizeMemberOrders 标记会员权益客户的订单并分档置顶。
//
// 会员权益由客户当前 member_level_code 指向的等级规则决定，规则须 enabled；本次接入两个
// **派单侧消费点**，按档位排序（只影响展示顺序，不改变任何校验）：
//   - 第一档 priorityIDs：命中「会员优先派单」（priority_dispatch）的订单，置顶最前；
//   - 第二档 dedicatedIDs：「专属通道」（dedicated_channel）且未命中优先派单的订单，
//     排在会员优先档之后、普通订单之前（二级置顶，不与第一档混排）；
//   - 普通订单按原顺序跟在最后。
//
// 同时命中两个权益的等级只计入第一档（避免同一单被重复标记两次）。
func prioritizeMemberOrders(ctx context.Context, db *ent.Client, orders []*ent.EsportsOrder) (
	priorityIDs, dedicatedIDs []int, sorted []*ent.EsportsOrder, err error) {
	if len(orders) == 0 {
		return nil, nil, orders, nil
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
		return nil, nil, nil, err
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
		return nil, nil, orders, nil
	}
	rules, err := db.EsportsMemberLevelRule.Query().Where(
		esportsmemberlevelrule.CodeIn(codes...),
		esportsmemberlevelrule.StatusEQ(esportsmemberlevelrule.StatusEnabled),
		// 两个权益都参与派单侧分档：优先派单（第一档）与专属通道（第二档）。
		esportsmemberlevelrule.Or(
			esportsmemberlevelrule.PriorityDispatchEQ(true),
			esportsmemberlevelrule.DedicatedChannelEQ(true),
		),
	).Select(
		esportsmemberlevelrule.FieldCode,
		esportsmemberlevelrule.FieldPriorityDispatch,
		esportsmemberlevelrule.FieldDedicatedChannel,
	).All(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(rules) == 0 {
		return nil, nil, orders, nil
	}
	priorityCodes := make(map[string]bool, len(rules))
	channelCodes := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if rule.PriorityDispatch {
			// 第一档：会员优先派单。同时开专属通道的等级不再进第二档。
			priorityCodes[rule.Code] = true
			continue
		}
		if rule.DedicatedChannel {
			channelCodes[rule.Code] = true
		}
	}
	priorityIDs = make([]int, 0, len(orders))
	dedicatedIDs = make([]int, 0, len(orders))
	head := make([]*ent.EsportsOrder, 0, len(orders))
	middle := make([]*ent.EsportsOrder, 0, len(orders))
	tail := make([]*ent.EsportsOrder, 0, len(orders))
	for _, order := range orders {
		code := levelByCustomer[order.CustomerID]
		switch {
		case priorityCodes[code]:
			priorityIDs = append(priorityIDs, order.ID)
			head = append(head, order)
		case channelCodes[code]:
			dedicatedIDs = append(dedicatedIDs, order.ID)
			middle = append(middle, order)
		default:
			tail = append(tail, order)
		}
	}
	if len(head) == 0 && len(middle) == 0 {
		// 无会员权益：保持原始顺序（与未接入分档前的行为逐位一致）。
		return nil, nil, orders, nil
	}
	merged := make([]*ent.EsportsOrder, 0, len(orders))
	merged = append(merged, head...)
	merged = append(merged, middle...)
	merged = append(merged, tail...)
	return priorityIDs, dedicatedIDs, merged, nil
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

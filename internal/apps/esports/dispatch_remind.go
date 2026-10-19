package esports

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/systemconfig"
)

// 催单时间阶梯与服务超时提醒（§5.7）。两个阈值都可配置，缺失或非法时回落默认值。
// 提醒一律「写事务外盒 + 订单事件」，幂等靠事务外盒 event_key 唯一索引兜底，
// 因此任务可以安全地重复运行，不会重复轰炸。
const (
	// 催单时间阶梯（分钟，升序）：如 5/15/30 分钟三级提醒。
	remindLadderKey     = "app.esports.remind_ladder_minutes"
	defaultRemindLadder = "5,15,30"
	// 服务超时提醒阈值（分钟）：服务段有效时长超过该值即提醒客服确认是否加钟；0 表示关闭。
	serviceOvertimeKey     = "app.esports.service_overtime_minutes"
	defaultServiceOvertime = 0
	// 无人接单升级阈值（分钟）：订单进入待派单池后超过该时长仍无任何有效席位即升级。
	dispatchNoTakerMinutesKey     = "app.esports.dispatch_no_taker_minutes"
	defaultDispatchNoTakerMinutes = 60
	// 临近开始升级提前量（分钟）：预约开始时间落在该窗口内且席位未接满即升级。
	dispatchStartEscalateMinutesKey     = "app.esports.dispatch_start_escalate_minutes"
	defaultDispatchStartEscalateMinutes = 60
	// 多席缺员自动补位阈值（分钟）：失效席位超过该时长未补齐才自动重开为待补位（§5.3-06）；
	// 未超时前保留 rejected / expired 原状态作为历史。
	dispatchRefillMinutesKey     = "app.esports.dispatch_refill_minutes"
	defaultDispatchRefillMinutes = 10
)

// dispatchRefillMinutes 多席缺员自动补位阈值（分钟）；<1 或 >1440 或非法回落 10。
func dispatchRefillMinutes(ctx context.Context, db *ent.Client) int {
	value := csConfigInt(ctx, db, dispatchRefillMinutesKey)
	if value < 1 || value > 1440 {
		return defaultDispatchRefillMinutes
	}
	return value
}

// dispatchNoTakerMinutes 无人接单升级阈值（分钟）；<1 或 >1440 或非法回落 60。
func dispatchNoTakerMinutes(ctx context.Context, db *ent.Client) int {
	value := csConfigInt(ctx, db, dispatchNoTakerMinutesKey)
	if value < 1 || value > 1440 {
		return defaultDispatchNoTakerMinutes
	}
	return value
}

// dispatchStartEscalateMinutes 临近开始升级提前量（分钟）；<1 或 >1440 或非法回落 60。
func dispatchStartEscalateMinutes(ctx context.Context, db *ent.Client) int {
	value := csConfigInt(ctx, db, dispatchStartEscalateMinutesKey)
	if value < 1 || value > 1440 {
		return defaultDispatchStartEscalateMinutes
	}
	return value
}

// remindLadderMinutes 读催单时间阶梯（分钟，升序去重）；缺失或非法时回落 5/15/30。
func remindLadderMinutes(ctx context.Context, db *ent.Client) []int {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(remindLadderKey)).Only(ctx)
	if err != nil {
		return parseRemindLadder(defaultRemindLadder)
	}
	if levels := parseRemindLadder(item.Value); len(levels) > 0 {
		return levels
	}
	return parseRemindLadder(defaultRemindLadder)
}

// parseRemindLadder 解析「5,15,30」形式的阶梯：逐项取合法正整数，去重后升序。
func parseRemindLadder(raw string) []int {
	seen := map[int]bool{}
	levels := make([]int, 0, 3)
	for _, part := range strings.Split(raw, ",") {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value < 1 || value > 24*60 || seen[value] {
			continue
		}
		seen[value] = true
		levels = append(levels, value)
	}
	sort.Ints(levels)
	return levels
}

// matchedRemindLevel 返回已满足的最大档位（1 起）；未达到第一档返回 0。
func matchedRemindLevel(levels []int, elapsedMinutes int) int {
	matched := 0
	for i, minutes := range levels {
		if elapsedMinutes >= minutes {
			matched = i + 1
		}
	}
	return matched
}

// serviceOvertimeMinutes 服务超时阈值（分钟）；缺失、0 或非法表示关闭。
func serviceOvertimeMinutes(ctx context.Context, db *ent.Client) int {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(serviceOvertimeKey)).Only(ctx)
	if err != nil {
		return defaultServiceOvertime
	}
	value, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || value < 0 || value > 24*60 {
		return defaultServiceOvertime
	}
	return value
}

// runRemindLadder 对「待接单」订单的邀请席位按可配置时间阶梯逐级催单（§5.7）：
// 第 1 档先提醒在线陪玩，第 2 档起升级提醒客服。返回本次新增的提醒条数。
func runRemindLadder(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	levels := remindLadderMinutes(ctx, db)
	ttl := seatInviteTTL(ctx, db)
	seats, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusInvited),
		esportsdispatchseat.ExpiresAtNotNil(),
	).Order(ent.Asc(esportsdispatchseat.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, seat := range seats {
		order, err := db.EsportsOrder.Get(ctx, seat.OrderID)
		if err != nil {
			if ent.IsNotFound(err) {
				continue
			}
			return total, err
		}
		if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance {
			continue
		}
		// 席位未单独记录发出时间，由「邀请到期时间 − 邀请时长」反推。
		invitedAt := seat.ExpiresAt.Add(-ttl)
		elapsed := int(now.UTC().Sub(invitedAt).Minutes())
		for level := 1; level <= matchedRemindLevel(levels, elapsed); level++ {
			sent, err := sendLadderReminder(ctx, db, order.ID, seat, level, now)
			if err != nil {
				return total, err
			}
			if sent {
				total++
			}
		}
	}
	return total, nil
}

// sendLadderReminder 事务内发一条阶梯提醒；同一 (订单, 席位, 档位) 只发一次。
// 订单在事务内重新读取，避免同一轮补发多档时用到上一档已抬升过的旧版本。
func sendLadderReminder(ctx context.Context, db *ent.Client, orderID int,
	seat *ent.EsportsDispatchSeat, level int, now time.Time) (bool, error) {
	eventKey := fmt.Sprintf("dispatch_remind:%d:%d:%d", orderID, seat.SeatIndex, level)
	exists, err := db.EsportsOutbox.Query().Where(esportsoutbox.EventKeyEQ(eventKey)).Exist(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return false, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance {
		return false, nil
	}
	// 第 1 档提醒在线陪玩，第 2 档起升级提醒客服（§5.7 逐级提醒）。
	eventType := "dispatch.remind_player"
	payload := map[string]interface{}{
		"order_no": order.OrderNo, "seat_index": seat.SeatIndex, "level": level,
	}
	if level > 1 {
		eventType = "dispatch.remind_admin"
		if order.ResponsibleAdminID != nil {
			payload["admin_id"] = *order.ResponsibleAdminID
		}
	}
	if seat.PlayerID != nil {
		payload["player_id"] = *seat.PlayerID
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey(eventKey).SetEventType(eventType).
		SetAggregateRef(order.OrderNo).SetPayload(payload).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return false, nil
		}
		return false, err
	}
	if _, err = bumpOrder(ctx, tx, orderID, order.Version, ""); err != nil {
		if err == errConflict {
			// 订单被并发改动，本轮跳过，下一轮由新版本收敛。
			return false, nil
		}
		return false, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(order.Version + 1).SetEventType("order.reminded").
		SetActorType("system").SetActorID("dispatch_ladder").
		SetDetails(map[string]interface{}{
			"level": level, "seat_index": seat.SeatIndex, "auto": true,
		}).Save(ctx); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// checkServiceOvertime 检查在服务中的服务段有效时长是否超过配置阈值，超过则提醒客服确认是否加钟（§5.7）。
// 返回本次新增的提醒条数；阈值为 0 时不检查。
// ponytail: 订单与 SKU 未声明每单位时长，「超过订单约定」暂用可配置阈值近似；
// 加钟确认流程（§6.2 / §12.5）与差异化时长口径未实现，此处只提醒客服核实。
func checkServiceOvertime(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	minutes := serviceOvertimeMinutes(ctx, db)
	if minutes <= 0 {
		return 0, nil
	}
	if limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	segments, err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusInService),
	).Order(ent.Asc(esportsservicesegment.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, segment := range segments {
		elapsed := int(now.UTC().Sub(segment.StartedAt.UTC()).Seconds()) - int(segment.PausedSeconds)
		if elapsed < minutes*60 {
			continue
		}
		order, err := db.EsportsOrder.Get(ctx, segment.OrderID)
		if err != nil {
			if ent.IsNotFound(err) {
				continue
			}
			return total, err
		}
		if order.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
			continue
		}
		sent, err := sendServiceOvertimeReminder(ctx, db, order.ID, segment, minutes, now)
		if err != nil {
			return total, err
		}
		if sent {
			total++
		}
	}
	return total, nil
}

// sendServiceOvertimeReminder 事务内发一条服务超时提醒；同一 (订单, 席位, 阈值) 只发一次。
// 订单在事务内重新读取，避免同一订单多个服务段连续提醒时用到旧版本。
func sendServiceOvertimeReminder(ctx context.Context, db *ent.Client, orderID int,
	segment *ent.EsportsServiceSegment, minutes int, now time.Time) (bool, error) {
	eventKey := fmt.Sprintf("service_overtime:%d:%d:%d", orderID, segment.SeatIndex, minutes)
	exists, err := db.EsportsOutbox.Query().Where(esportsoutbox.EventKeyEQ(eventKey)).Exist(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return false, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		return false, nil
	}
	payload := map[string]interface{}{
		"order_no": order.OrderNo, "seat_index": segment.SeatIndex,
		"player_id": segment.PlayerID, "overtime_minutes": minutes,
	}
	if order.ResponsibleAdminID != nil {
		payload["admin_id"] = *order.ResponsibleAdminID
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey(eventKey).
		SetEventType("dispatch.service_overtime").SetAggregateRef(order.OrderNo).
		SetPayload(payload).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return false, nil
		}
		return false, err
	}
	if _, err = bumpOrder(ctx, tx, orderID, order.Version, ""); err != nil {
		if err == errConflict {
			return false, nil
		}
		return false, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(order.Version + 1).SetEventType("order.service_overtime").
		SetActorType("system").SetActorID("service_overtime").
		SetDetails(map[string]interface{}{
			"seat_index": segment.SeatIndex, "player_id": segment.PlayerID, "threshold_minutes": minutes,
		}).Save(ctx); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// 派单升级触发类型（§5.9-06）。
const (
	escalationNoTaker          = "no_taker"
	escalationMemberAbsent     = "member_absent"
	escalationStartingUnfilled = "starting_unfilled"
)

// runDispatchEscalations 三类逐级升级（§5.9-06）：
//
//	① no_taker          无人接单：进入待派单池超过 dispatch_no_taker_minutes 且已无任何
//	                    有效席位（invited/accepted），阶梯催单已用尽；
//	② member_absent     多人缺席：多席订单既有已接单成员又存在空缺席位（自动补位后仍缺员）；
//	③ starting_unfilled 临近开始未满员：预约开始时间落在 dispatch_start_escalate_minutes
//	                    内且席位未接满。
//
// 每类各写一条 dispatch.escalation_<trigger> 外盒（定向通知责任客服），event_key 含订单号与
// 触发类型保证幂等（任务可安全重复运行）。返回本次新增的升级条数。
//
// 说明：本函数**不改订单版本**（不写订单事件），避免让进行中的改期提案的 order_version
// 快照失效；升级留痕由外盒与看板异常桶承载。
func runDispatchEscalations(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	noTakerMinutes := dispatchNoTakerMinutes(ctx, db)
	startLeadMinutes := dispatchStartEscalateMinutes(ctx, db)
	// §5.3-06：先做多席订单缺员自动补位（超时失效席位重开为待补位），再评估升级。
	// 同一任务驱动，避免客服既要等补位又要等升级两套定时。
	if _, err := refillVacantMultiSeatSeats(ctx, db, now, limit); err != nil {
		return 0, err
	}
	orders, err := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusIn(
			esportsorder.FulfillmentStatusPendingDispatch,
			esportsorder.FulfillmentStatusPendingAcceptance,
		),
	).Order(ent.Asc(esportsorder.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, order := range orders {
		if order.RequiredSeats < 1 {
			continue
		}
		seats, err := db.EsportsDispatchSeat.Query().Where(
			esportsdispatchseat.OrderIDEQ(order.ID),
		).All(ctx)
		if err != nil {
			return total, err
		}
		accepted, active, vacant, dead := 0, 0, 0, 0
		for _, seat := range seats {
			switch seat.Status {
			case esportsdispatchseat.StatusAccepted:
				accepted++
				active++
			case esportsdispatchseat.StatusInvited:
				active++
			case esportsdispatchseat.StatusOpen:
				vacant++
			case esportsdispatchseat.StatusRejected, esportsdispatchseat.StatusExpired,
				esportsdispatchseat.StatusRevoked:
				dead++
			}
		}
		if accepted >= order.RequiredSeats {
			continue // 已接满，无需升级。
		}
		triggers := make([]string, 0, 3)
		elapsedFrom := order.FirstPooledAt
		if elapsedFrom == nil {
			elapsedFrom = order.FirstInvitedAt
		}
		if active == 0 && elapsedFrom != nil &&
			!elapsedFrom.After(now.UTC().Add(-time.Duration(noTakerMinutes)*time.Minute)) {
			triggers = append(triggers, escalationNoTaker)
		}
		if order.RequiredSeats > 1 && accepted > 0 && (vacant > 0 || dead > 0) {
			// 多席订单已有成员接单但仍有空缺 / 失效席位（成员缺席或自动补位后的待补位状态）。
			triggers = append(triggers, escalationMemberAbsent)
		}
		if order.RequestedStartAt != nil &&
			!order.RequestedStartAt.After(now.UTC().Add(time.Duration(startLeadMinutes)*time.Minute)) {
			triggers = append(triggers, escalationStartingUnfilled)
		}
		if len(triggers) == 0 {
			continue
		}
		for _, trigger := range triggers {
			sent, err := sendDispatchEscalation(ctx, db, order, trigger, accepted)
			if err != nil {
				return total, err
			}
			if sent {
				total++
			}
		}
	}
	return total, nil
}

// sendDispatchEscalation 写一条派单升级外盒；同一 (订单, 触发类型) 只发一次（event_key 唯一索引兜底）。
func sendDispatchEscalation(ctx context.Context, db *ent.Client, order *ent.EsportsOrder, trigger string, accepted int) (bool, error) {
	eventType := "dispatch.escalation_" + trigger
	eventKey := "dispatch_escalation:" + trigger + ":" + order.OrderNo
	exists, err := db.EsportsOutbox.Query().Where(esportsoutbox.EventKeyEQ(eventKey)).Exist(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	payload := map[string]interface{}{
		"order_no": order.OrderNo, "trigger": trigger,
		"required_seats": order.RequiredSeats, "accepted_seats": accepted,
	}
	if order.ResponsibleAdminID != nil {
		payload["admin_id"] = *order.ResponsibleAdminID
	}
	if order.RequestedStartAt != nil {
		payload["requested_start_at"] = order.RequestedStartAt.UTC().Format(time.RFC3339)
	}
	if _, err = db.EsportsOutbox.Create().SetEventKey(eventKey).
		SetEventType(eventType).SetAggregateRef(order.OrderNo).
		SetPayload(payload).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

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
)

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

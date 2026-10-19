package esports

import (
	"context"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
)

// §5.9 订单时间线埋点：把「进入待派单池」「首次派出邀请」「首次有效接单」「开工」作为不同时间点，
// 各自只写一次、永不改写。各类等待时长（入池→派出、派出→接单、接单→开工、入池→开工）由报表按
// 这些时间戳派生；配合 `priority_notice_until` / `priority_released_at` 可区分「名单内响应慢」与
// 「无人可派」。派单候选池与接单上限等硬性条件不在本埋点内。
type orderTimelineMark int

const (
	// timelineFirstPooled 首次进入待派单池。
	timelineFirstPooled orderTimelineMark = iota
	// timelineFirstInvited 首次派出邀请（人工指派或抢单发布）。
	timelineFirstInvited
	// timelineFirstAccepted 首次有效接单。
	timelineFirstAccepted
	// timelineServiceStarted 开工（开始服务）。
	timelineServiceStarted
)

// markOrderTimelineIn 事务内按「只写一次」记录订单时间线埋点（§5.9）：
// 仅当该字段仍为空时写入，重复调用与并发都不会改写已落下的首个时间点；失败不阻断主流程。
// 返回本次是否真的写入（false = 已有值或并发已抢先），供调用方决定是否补写一次性事件。
func markOrderTimelineIn(ctx context.Context, tx *ent.Tx, orderID int, mark orderTimelineMark, now time.Time) (bool, error) {
	if orderID < 1 {
		return false, nil
	}
	update := tx.EsportsOrder.Update().Where(esportsorder.IDEQ(orderID))
	switch mark {
	case timelineFirstPooled:
		update = update.Where(esportsorder.FirstPooledAtIsNil()).SetFirstPooledAt(now.UTC())
	case timelineFirstInvited:
		update = update.Where(esportsorder.FirstInvitedAtIsNil()).SetFirstInvitedAt(now.UTC())
	case timelineFirstAccepted:
		update = update.Where(esportsorder.FirstAcceptedAtIsNil()).SetFirstAcceptedAt(now.UTC())
	case timelineServiceStarted:
		update = update.Where(esportsorder.ServiceStartedAtIsNil()).SetServiceStartedAt(now.UTC())
	default:
		return false, nil
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return false, err
	}
	return changed == 1, nil
}

// orderEnteredPendingDispatchEvent 「进入待派单池」独立事件类型（§5.9-01）。
const orderEnteredPendingDispatchEvent = "order.entered_pending_dispatch"

// orderReadyToStartEvent 「可以开工」独立事件类型（§5.9-01）。
const orderReadyToStartEvent = "order.ready_to_start"

// markOrderReadyToStartIn 事务内首次把订单标记为「可以开工」（§5.9-01）：全部席位就绪后由业务侧
// 调用一次，写 ready_to_start_at 时间戳与 order.ready_to_start 独立事件。
//
// 幂等：仅在 ready_to_start_at 仍为空且订单版本未变时写入；返回 false 表示已写过或并发抢先，
// 调用方**不应**把它当成业务失败（主流程继续）。事件占用一个全新订单版本，与调用方自己的事件
// 版本互不冲突（形成 V+1 → V+2 的连续占用）。
func markOrderReadyToStartIn(ctx context.Context, tx *ent.Tx, orderID, expectedVersion int, now time.Time) (bool, error) {
	if orderID < 1 || expectedVersion < 1 {
		return false, nil
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID),
		esportsorder.VersionEQ(expectedVersion),
		esportsorder.ReadyToStartAtIsNil(),
	).SetReadyToStartAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return false, err
	}
	if changed != 1 {
		return false, nil
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(expectedVersion + 1).SetEventType(orderReadyToStartEvent).
		SetActorType("system").SetActorID("dispatch_ready").
		SetDetails(map[string]interface{}{"ready_to_start_at": now.UTC()}).Save(ctx); err != nil {
		return false, err
	}
	return true, nil
}

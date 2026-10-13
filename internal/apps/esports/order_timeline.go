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
func markOrderTimelineIn(ctx context.Context, tx *ent.Tx, orderID int, mark orderTimelineMark, now time.Time) error {
	if orderID < 1 {
		return nil
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
		return nil
	}
	_, err := update.Save(ctx)
	return err
}

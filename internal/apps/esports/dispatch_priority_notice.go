package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchprioritygrant"
	"go-admin/internal/ent/esportsdispatchprioritynotice"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/systemconfig"
)

// 优先派单权「独占优先通知」窗口（§5.4 业务口径）。
//
// 与功能列表原文（「排序加权、不是独占、抢单大厅不受影响」）不同，业务口径为：
//   - 订单进入待派单时，若该游戏分区存在生效名单，则**只通知名单内陪玩**（其他人在抢单大厅看不到这张单）；
//   - 名单内有人接下即成交；名单内**全部拒绝**，或**超过配置的通知时长**，订单才**释放到抢单大厅**给所有人抢；
//   - 释放后订单转 grabbing，与普通抢单单完全一致，名单不再有特权。
const (
	// 优先通知时长（分钟）：窗口内只有名单内陪玩可见并可接单。
	dispatchPriorityNoticeKey     = "app.esports.dispatch_priority_notice_minutes"
	dispatchPriorityNoticeDefault = 3
	// 释放到大厅的原因。
	dispatchReleaseReasonExpired  = "优先通知超时"
	dispatchReleaseReasonDeclined = "名单内全部拒绝"
	dispatchReleaseReasonCallback = "名单内拒绝后回退待派单"
	dispatchReleaseReasonStaff    = "客服提前释放"
	// 没有优先名单（或窗口已闭合）的普通待派单订单，由客服发布到抢单大厅（§5.2 / §5.6）。
	dispatchReleaseReasonPublish = "客服发布到抢单大厅"
	// 名单内陪玩接单：窗口闭合，订单直接进入已接单。
	dispatchReleaseReasonAccepted = "名单内陪玩已接单"
)

// errDispatchPriorityHold 订单正处于优先通知窗口（名单内独占），尚未对抢单大厅开放。
var errDispatchPriorityHold = errors.New("该订单正在优先通知名单内陪玩，暂未对大厅开放")

// dispatchPriorityNoticeMinutes 读取优先通知时长（分钟）。
func dispatchPriorityNoticeMinutes(ctx context.Context, db *ent.Client) int {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(dispatchPriorityNoticeKey)).Only(ctx)
	if err != nil {
		return dispatchPriorityNoticeDefault
	}
	minutes := 0
	if _, scanErr := fmt.Sscanf(strings.TrimSpace(item.Value), "%d", &minutes); scanErr != nil ||
		minutes < 1 || minutes > 24*60 {
		return dispatchPriorityNoticeDefault
	}
	return minutes
}

// dispatchPriorityWindowOpen 判断订单当前是否处于「优先通知中」：窗口已开且未到截止时间。
// 纯函数比较，避免不同数据库驱动对带时区时间字面量的比较差异。
func dispatchPriorityWindowOpen(order *ent.EsportsOrder, now time.Time) bool {
	return order.PriorityNoticeStatus == esportsorder.PriorityNoticeStatusPending &&
		order.PriorityNoticeUntil != nil && now.UTC().Before(*order.PriorityNoticeUntil)
}

// onOrderEnteredPendingDispatch 订单进入待派单时的统一入口（§5.4）：
//   - 订单有客户指定陪玩（preferred_player_id）→ 不开窗，直接留在待派单池由客服指派；
//   - 该游戏分区有生效名单且该订单此前未通知过 → 开窗，只通知名单内陪玩；
//   - 该订单此前已通知过（名单内拒绝后回退到待派单）→ 直接释放到大厅，避免名单内反复拒绝来回循环；
//   - 解析不到游戏分区或该分区没有生效名单 → 不动（订单留在待派单池，由客服指派或人工发布抢单）。
//
// 幂等：仅在订单仍为待派单、且窗口未开过时生效；并发变更时放弃，不阻断主流程。
func onOrderEnteredPendingDispatch(ctx context.Context, tx *ent.Tx, orderID int, now time.Time) error {
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		return nil
	}
	// §5.9：首次进入待派单池的时间点（只写一次，后续回退不覆盖）。
	if err = markOrderTimelineIn(ctx, tx, orderID, timelineFirstPooled, now); err != nil {
		return err
	}
	// 回到待派单即可重新派单：把此前被拒/过期/撤回的席位统一恢复为 open，
	// 否则单席订单会因为没有 open 席位而永久卡在待派单。既有席位事件保留历史。
	if _, err = reopenDeadDispatchSeatsForRedispatch(ctx, tx, order.ID); err != nil {
		return err
	}
	// 客户指定陪玩：客户/客服已明确人选，直接走指派流程，不进入优先通知窗口（§5.4）。
	if order.PreferredPlayerID != nil && *order.PreferredPlayerID > 0 {
		return nil
	}
	noticed, err := tx.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(orderID),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if noticed {
		// 回退到待派单：不再二次开窗，直接释放到大厅。
		_, err = releaseDispatchPriorityNoticeIn(ctx, tx, order, dispatchReleaseReasonCallback, now)
		return err
	}
	game, err := orderGameIn(ctx, tx, order.SkuID)
	if err != nil {
		return err
	}
	if game == "" {
		return nil
	}
	playerIDs, err := activePriorityPlayerIDs(ctx, tx, game, now)
	if err != nil {
		return err
	}
	if len(playerIDs) == 0 {
		return nil
	}
	deadline := now.UTC().Add(time.Duration(dispatchPriorityNoticeMinutes(ctx, tx.Client())) * time.Minute)
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID),
		esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingDispatch),
	).SetPriorityNoticeStatus(esportsorder.PriorityNoticeStatusPending).
		SetPriorityNoticeUntil(deadline).SetPriorityGame(game).
		AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		// 订单已被并发改动，放弃开窗，不阻断支付/派单主流程。
		return nil
	}
	newVersion := order.Version + 1
	for _, playerID := range playerIDs {
		if _, err = tx.EsportsDispatchPriorityNotice.Create().
			SetOrderID(order.ID).SetPlayerID(playerID).SetGame(game).
			SetStatus(esportsdispatchprioritynotice.StatusNotified).
			SetNotifiedAt(now.UTC()).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				continue
			}
			return err
		}
		// 通知通道：写事务外盒事件，由投递器推送；当前未配置投递器时由陪玩端轮询「我的优先通知」。
		if _, err = tx.EsportsOutbox.Create().
			SetEventKey("order.priority_notice:" + order.OrderNo + ":" + strconv.Itoa(playerID)).
			SetEventType("order.priority_notice").SetAggregateRef(order.OrderNo).
			SetPayload(map[string]interface{}{
				"order_no": order.OrderNo, "player_id": playerID,
				"game": game, "notice_until": deadline,
			}).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				continue
			}
			return err
		}
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.priority_notice_opened").
		SetActorType("system").SetActorID("").
		SetDetails(map[string]interface{}{
			"game": game, "notice_until": deadline, "player_ids": playerIDs,
		}).Save(ctx); err != nil {
		return err
	}
	return nil
}

// activePriorityPlayerIDs 取某游戏分区当前生效名单里**当前可接单**的陪玩 ID。
// 开窗前过滤不可接单者：离线、未合格、并发/每日接单上限已满的名单成员不产生通知；
// 若全部不可接则返回空列表，调用方不开窗，订单按常规待派单流程处理。
func activePriorityPlayerIDs(ctx context.Context, tx *ent.Tx, game string, now time.Time) ([]int, error) {
	grants, err := tx.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.GameEQ(game),
		esportsdispatchprioritygrant.StatusEQ(esportsdispatchprioritygrant.StatusActive),
		esportsdispatchprioritygrant.PeriodStartLTE(now.UTC()),
		esportsdispatchprioritygrant.PeriodEndGT(now.UTC()),
	).Order(ent.Asc(esportsdispatchprioritygrant.FieldRank)).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(grants) == 0 {
		return nil, nil
	}
	candidateIDs := make([]int, 0, len(grants))
	for _, grant := range grants {
		candidateIDs = append(candidateIDs, grant.PlayerID)
	}
	players, err := tx.EsportsPlayer.Query().Where(
		esportsplayer.IDIn(candidateIDs...),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]*ent.EsportsPlayer, len(players))
	for _, player := range players {
		byID[player.ID] = player
	}
	ids := make([]int, 0, len(grants))
	seen := make(map[int]bool, len(grants))
	for _, grant := range grants {
		if seen[grant.PlayerID] {
			continue
		}
		player := byID[grant.PlayerID]
		if player == nil || player.Qualification != esportsplayer.QualificationQualified ||
			player.Presence != esportsplayer.PresenceOnline {
			continue
		}
		// 容量粗筛：已满则本次不通知，避免名单成员收到注定接不了的通知。
		if err = checkPlayerDispatchLimit(ctx, tx, player, nil, now, 0); err != nil {
			if errors.Is(err, errInvalidInput) {
				continue
			}
			return nil, err
		}
		seen[grant.PlayerID] = true
		ids = append(ids, grant.PlayerID)
	}
	return ids, nil
}

// releaseDispatchPriorityNoticeIn 事务内把优先通知窗口释放到抢单大厅：订单转 grabbing、窗口置 released、
// 未回应的通知置 expired，并留事件与事务外盒通知。幂等：仅 pending/accepted → released 生效一次。
// 允许 accepted → released：名单内接单后若因转单/退回待派单需要重新释放，窗口已闭合不应再判冲突。
func releaseDispatchPriorityNoticeIn(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, reason string, now time.Time) (*ent.EsportsOrder, error) {
	if order.PriorityNoticeStatus == esportsorder.PriorityNoticeStatusReleased {
		return order, nil
	}
	if order.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusPending &&
		order.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusAccepted {
		return nil, errConflict
	}
	// 释放到大厅前先补回 open 席位，避免只有 revoked/expired/rejected 席位时无人可抢。
	if _, err := reopenDeadDispatchSeatsForRedispatch(ctx, tx, order.ID); err != nil {
		return nil, err
	}
	previousStatus := order.PriorityNoticeStatus.String()
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID),
		esportsorder.VersionEQ(order.Version),
		esportsorder.PriorityNoticeStatusIn(
			esportsorder.PriorityNoticeStatusPending, esportsorder.PriorityNoticeStatusAccepted),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingDispatch),
	).SetPriorityNoticeStatus(esportsorder.PriorityNoticeStatusReleased).
		SetPriorityReleaseReason(reason).
		SetPriorityReleasedAt(now.UTC()).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusGrabbing).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	newVersion := order.Version + 1
	// 只作废仍待回应的通知；已接受的通知保留 accepted 作为历史。
	if _, err = tx.EsportsDispatchPriorityNotice.Update().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID),
		esportsdispatchprioritynotice.StatusEQ(esportsdispatchprioritynotice.StatusNotified),
	).SetStatus(esportsdispatchprioritynotice.StatusExpired).AddVersion(1).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.dispatch_released").
		SetActorType("system").SetActorID("").
		SetDetails(map[string]interface{}{
			"reason": reason, "game": order.PriorityGame, "from_priority_status": previousStatus,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("order.dispatch_released:" + order.OrderNo).
		SetEventType("order.dispatch_released").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{"order_no": order.OrderNo, "reason": reason}).Save(ctx); err != nil {
		if !ent.IsConstraintError(err) {
			return nil, err
		}
	}
	return tx.EsportsOrder.Get(ctx, order.ID)
}

// releaseDispatchPriorityNotice 管理端提前释放：把优先通知窗口释放到抢单大厅（§5.2 客服可随时人工干预）。
func releaseDispatchPriorityNotice(ctx context.Context, db *ent.Client, orderID int, reason string, now time.Time) (*ent.EsportsOrder, error) {
	reason = strings.TrimSpace(reason)
	if orderID < 1 || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	if reason == "" {
		reason = dispatchReleaseReasonStaff
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
	if order.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusPending {
		return nil, errConflict
	}
	if _, err = releaseDispatchPriorityNoticeIn(ctx, tx, order, reason, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// declineDispatchPriorityNotice 名单内陪玩拒绝优先通知；名单内再无「待回应」的人时，订单释放到大厅。
func declineDispatchPriorityNotice(ctx context.Context, db *ent.Client, playerID, noticeID int, reason string, now time.Time) (*ent.EsportsDispatchPriorityNotice, error) {
	reason = strings.TrimSpace(reason)
	if playerID < 1 || noticeID < 1 || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	notice, err := tx.EsportsDispatchPriorityNotice.Get(ctx, noticeID)
	if err != nil {
		return nil, err
	}
	if notice.PlayerID != playerID {
		return nil, errForbidden
	}
	if notice.Status != esportsdispatchprioritynotice.StatusNotified {
		return nil, errConflict
	}
	order, err := tx.EsportsOrder.Get(ctx, notice.OrderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	// 窗口已结束（超时已释放或已被客服释放）时不再接受拒绝。
	if !dispatchPriorityWindowOpen(order, now) {
		return nil, errConflict
	}
	changed, err := tx.EsportsDispatchPriorityNotice.Update().Where(
		esportsdispatchprioritynotice.IDEQ(notice.ID),
		esportsdispatchprioritynotice.VersionEQ(notice.Version),
		esportsdispatchprioritynotice.StatusEQ(esportsdispatchprioritynotice.StatusNotified),
	).SetStatus(esportsdispatchprioritynotice.StatusDeclined).
		SetDeclineReason(reason).SetRespondedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	// 名单内是否还有人待回应？没有则整单释放到大厅。
	remaining, err := tx.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID),
		esportsdispatchprioritynotice.StatusEQ(esportsdispatchprioritynotice.StatusNotified),
	).Count(ctx)
	if err != nil {
		return nil, err
	}
	if remaining == 0 {
		if _, err = releaseDispatchPriorityNoticeIn(ctx, tx, order, dispatchReleaseReasonDeclined, now); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsDispatchPriorityNotice.Get(ctx, notice.ID)
}

// acceptDispatchPriorityNotice 事务内把该陪玩的优先通知置为已接受（抢单/接单成功后调用）。
func acceptDispatchPriorityNotice(ctx context.Context, tx *ent.Tx, orderID, playerID int, now time.Time) error {
	if _, err := tx.EsportsDispatchPriorityNotice.Update().Where(
		esportsdispatchprioritynotice.OrderIDEQ(orderID),
		esportsdispatchprioritynotice.PlayerIDEQ(playerID),
		esportsdispatchprioritynotice.StatusEQ(esportsdispatchprioritynotice.StatusNotified),
	).SetStatus(esportsdispatchprioritynotice.StatusAccepted).
		SetRespondedAt(now.UTC()).AddVersion(1).Save(ctx); err != nil {
		return err
	}
	return nil
}

// closeDispatchPriorityWindowOnAccept 事务内闭合优先通知窗口：订单置 accepted、其余待回应通知作废、
// 留事件。名单内陪玩接单后不再对大厅开放，也不再重复通知。
func closeDispatchPriorityWindowOnAccept(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, playerID int, now time.Time) error {
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID),
		esportsorder.VersionEQ(order.Version),
		esportsorder.PriorityNoticeStatusEQ(esportsorder.PriorityNoticeStatusPending),
	).SetPriorityNoticeStatus(esportsorder.PriorityNoticeStatusAccepted).
		SetPriorityReleaseReason(dispatchReleaseReasonAccepted).
		AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	if err = acceptDispatchPriorityNotice(ctx, tx, order.ID, playerID, now); err != nil {
		return err
	}
	if _, err = tx.EsportsDispatchPriorityNotice.Update().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID),
		esportsdispatchprioritynotice.StatusEQ(esportsdispatchprioritynotice.StatusNotified),
	).SetStatus(esportsdispatchprioritynotice.StatusExpired).AddVersion(1).Save(ctx); err != nil {
		return err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.priority_notice_accepted").
		SetActorType("player").SetActorID(strconv.Itoa(playerID)).
		SetDetails(map[string]interface{}{"game": order.PriorityGame}).Save(ctx); err != nil {
		return err
	}
	return nil
}

// playerNotifiedForOrderTx 事务内判断该陪玩是否在优先通知窗口内被通知过。
func playerNotifiedForOrderTx(ctx context.Context, tx *ent.Tx, orderID, playerID int) (bool, error) {
	return tx.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(orderID),
		esportsdispatchprioritynotice.PlayerIDEQ(playerID),
		esportsdispatchprioritynotice.StatusEQ(esportsdispatchprioritynotice.StatusNotified),
	).Exist(ctx)
}

// listPlayerDispatchNotices 陪玩端的「我的优先通知」：待回应且窗口未结束的通知。
func listPlayerDispatchNotices(ctx context.Context, db *ent.Client, playerID int, now time.Time) ([]*ent.EsportsDispatchPriorityNotice, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	rows, err := db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.PlayerIDEQ(playerID),
		esportsdispatchprioritynotice.StatusEQ(esportsdispatchprioritynotice.StatusNotified),
	).Order(ent.Desc(esportsdispatchprioritynotice.FieldID)).Limit(100).All(ctx)
	if err != nil {
		return nil, err
	}
	orderIDs := make([]int, 0, len(rows))
	for _, row := range rows {
		orderIDs = append(orderIDs, row.OrderID)
	}
	open, err := openWindowOrderIDs(ctx, db, orderIDs, now)
	if err != nil {
		return nil, err
	}
	items := make([]*ent.EsportsDispatchPriorityNotice, 0, len(rows))
	for _, row := range rows {
		if open[row.OrderID] {
			items = append(items, row)
		}
	}
	return items, nil
}

// openWindowOrderIDs 过滤出仍处于优先通知窗口内的订单 ID 集合（Go 侧比较时间，口径与列表一致）。
func openWindowOrderIDs(ctx context.Context, db *ent.Client, orderIDs []int, now time.Time) (map[int]bool, error) {
	result := map[int]bool{}
	if len(orderIDs) == 0 {
		return result, nil
	}
	orders, err := db.EsportsOrder.Query().Where(
		esportsorder.IDIn(orderIDs...),
		esportsorder.PriorityNoticeStatusEQ(esportsorder.PriorityNoticeStatusPending),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, order := range orders {
		if dispatchPriorityWindowOpen(order, now) {
			result[order.ID] = true
		}
	}
	return result, nil
}

// noticedPendingOrderIDs 该陪玩在窗口内被通知的待派单订单 ID（用于抢单大厅过滤）。
func noticedPendingOrderIDs(ctx context.Context, db *ent.Client, playerID int, now time.Time) ([]int, error) {
	rows, err := db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.PlayerIDEQ(playerID),
		esportsdispatchprioritynotice.StatusEQ(esportsdispatchprioritynotice.StatusNotified),
	).Select(esportsdispatchprioritynotice.FieldOrderID).Limit(200).All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.OrderID)
	}
	open, err := openWindowOrderIDs(ctx, db, ids, now)
	if err != nil {
		return nil, err
	}
	openIDs := make([]int, 0, len(open))
	for id := range open {
		openIDs = append(openIDs, id)
	}
	return openIDs, nil
}

// listOrderDispatchNotices 某订单的优先通知明细（客服查看谁被通知、谁拒绝、谁接受）。
func listOrderDispatchNotices(ctx context.Context, db *ent.Client, orderID int) ([]*ent.EsportsDispatchPriorityNotice, error) {
	if orderID < 1 {
		return nil, errInvalidInput
	}
	return db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(orderID),
	).Order(ent.Asc(esportsdispatchprioritynotice.FieldID)).All(ctx)
}

// expireDispatchPriorityNotices 超时释放：窗口已过截止时间的订单释放到抢单大厅。返回处理单数。
func expireDispatchPriorityNotices(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	orders, err := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingDispatch),
		esportsorder.PriorityNoticeStatusEQ(esportsorder.PriorityNoticeStatusPending),
		esportsorder.PriorityNoticeUntilLTE(now.UTC()),
	).Order(ent.Asc(esportsorder.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, order := range orders {
		if _, err := releaseDispatchPriorityNotice(ctx, db, order.ID, dispatchReleaseReasonExpired, now); err != nil {
			if err == errConflict {
				continue
			}
			return total, err
		}
		total++
	}
	return total, nil
}

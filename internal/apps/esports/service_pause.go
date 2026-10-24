package esports

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsservicesegment"
)

// playerServiceTiming 是陪玩端「我的服务段」视图。
//
// 时间口径（EffectiveSeconds / PausedSeconds）与履约量口径（ReportedUnits / *Units）同时下发：
// 前者用于展示「服务了多久、被暂停多久」，后者用于展示「做了多少、还差多少」。
type playerServiceTiming struct {
	OrderID   int        `json:"order_id"`
	OrderNo   string     `json:"order_no"`
	PlayerID  int        `json:"player_id"`
	SeatIndex int        `json:"seat_index"`
	Status    string     `json:"status"`
	StartedAt time.Time  `json:"started_at"`
	PausedAt  *time.Time `json:"paused_at"`
	// EffectiveSeconds 本次服务段的有效服务秒数（暂停期间冻结）。
	EffectiveSeconds int64 `json:"effective_seconds"`
	// PausedSeconds 本次服务段的累计暂停秒数（的「暂停量」，时间口径）。
	PausedSeconds int64 `json:"paused_seconds"`
	// ReportedUnits 本人**已服务量**（已上报的服务量，只增）。
	ReportedUnits int64 `json:"reported_units"`
	// PromisedUnits 本人所在席位的承诺量（多人单取每席承诺量，否则取整单承诺量）。
	PromisedUnits int64 `json:"promised_service_units"`
	// OrderPromisedUnits / OrderServedUnits / UnfulfilledUnits 是订单级履约量：
	// 承诺量（含加钟量）、已上报量合计、**仍需履约量** = max(0, 承诺量 + 加钟量 − 已上报量)。
	OrderPromisedUnits int64 `json:"order_promised_service_units"`
	OrderServedUnits   int64 `json:"order_served_units"`
	OrderTotalUnits    int64 `json:"order_total_units"`
	UnfulfilledUnits   int64 `json:"unfulfilled_units"`
}

// orderFulfillmentSummary 是订单级履约量汇总。
type orderFulfillmentSummary struct {
	// PromisedUnits 订单承诺量（单价快照）。
	PromisedUnits int64 `json:"promised_service_units"`
	// AddonUnits 累计加钟量（由已确认的加钟单累加）。
	AddonUnits int64 `json:"addon_units"`
	// TotalUnits 承诺量 + 加钟量。
	TotalUnits int64 `json:"total_units"`
	// ServedUnits 订单全部服务段已上报量之和（与结单条件 service_units 同口径）。
	ServedUnits int64 `json:"served_units"`
	// UnfulfilledUnits 仍需履约量 = max(0, TotalUnits − ServedUnits)。
	UnfulfilledUnits int64 `json:"unfulfilled_units"`
}

// summarizeOrderFulfillment 由订单与服务段算出履约量汇总（纯函数，避免额外查询）。
func summarizeOrderFulfillment(order *ent.EsportsOrder, segments []*ent.EsportsServiceSegment) orderFulfillmentSummary {
	summary := orderFulfillmentSummary{}
	if order != nil {
		summary.PromisedUnits = order.PromisedServiceUnits
		summary.AddonUnits = order.AddonUnits
	}
	summary.TotalUnits = summary.PromisedUnits + summary.AddonUnits
	for _, segment := range segments {
		if segment != nil {
			summary.ServedUnits += segment.ReportedUnits
		}
	}
	summary.UnfulfilledUnits = summary.TotalUnits - summary.ServedUnits
	if summary.UnfulfilledUnits < 0 {
		summary.UnfulfilledUnits = 0
	}
	return summary
}

// seatPromisedUnits 单席承诺量：多人单取每席承诺量，未配置（0）时回落整单承诺量。
func seatPromisedUnits(order *ent.EsportsOrder) int64 {
	if order == nil {
		return 0
	}
	if order.PerSeatServiceUnits > 0 {
		return order.PerSeatServiceUnits
	}
	return order.PromisedServiceUnits
}

// loadOrderFulfillment 读订单与其全部服务段并算出履约量汇总。
func loadOrderFulfillment(ctx context.Context, db *ent.Client, orderID int) (*ent.EsportsOrder, []*ent.EsportsServiceSegment, orderFulfillmentSummary, error) {
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, nil, orderFulfillmentSummary{}, err
	}
	segments, err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
	).All(ctx)
	if err != nil {
		return nil, nil, orderFulfillmentSummary{}, err
	}
	return order, segments, summarizeOrderFulfillment(order, segments), nil
}

// serviceTimingView 组装服务段视图：时间口径来自服务段本身，履约量口径来自订单与全部服务段。
func serviceTimingView(segment *ent.EsportsServiceSegment, order *ent.EsportsOrder,
	summary orderFulfillmentSummary, now time.Time) playerServiceTiming {
	view := serviceTimingAt(segment, now)
	view.PlayerID = segment.PlayerID
	view.SeatIndex = segment.SeatIndex
	view.ReportedUnits = segment.ReportedUnits
	view.PromisedUnits = seatPromisedUnits(order)
	view.OrderPromisedUnits = summary.PromisedUnits
	view.OrderServedUnits = summary.ServedUnits
	view.OrderTotalUnits = summary.TotalUnits
	view.UnfulfilledUnits = summary.UnfulfilledUnits
	if order != nil {
		view.OrderNo = order.OrderNo
	}
	return view
}

// serviceTimingAt 只算时间口径（无订单上下文时使用，例如终止服务段的内部计算）。
func serviceTimingAt(segment *ent.EsportsServiceSegment, now time.Time) playerServiceTiming {
	end := now.UTC()
	if segment.EndedAt != nil {
		end = segment.EndedAt.UTC()
	}
	paused := segment.PausedSeconds
	if segment.Status == esportsservicesegment.StatusPaused && segment.PausedAt != nil {
		elapsed := int64(end.Sub(segment.PausedAt.UTC()).Seconds())
		if elapsed > 0 {
			paused += elapsed
		}
	}
	effective := int64(end.Sub(segment.StartedAt.UTC()).Seconds()) - paused
	if effective < 0 {
		effective = 0
	}
	return playerServiceTiming{
		OrderID: segment.OrderID, PlayerID: segment.PlayerID, SeatIndex: segment.SeatIndex,
		Status: string(segment.Status), StartedAt: segment.StartedAt, PausedAt: segment.PausedAt,
		EffectiveSeconds: effective, PausedSeconds: paused,
		ReportedUnits: segment.ReportedUnits,
	}
}

// listPlayerServiceTimings 陪玩端进行中（in_service / paused）服务段视图，
// 逐条带该成员已服务量与订单级仍需履约量。订单级汇总按订单批量计算，避免逐条查库。
func listPlayerServiceTimings(ctx context.Context, db *ent.Client, playerID int, now time.Time) ([]playerServiceTiming, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	segments, err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.PlayerIDEQ(playerID),
		esportsservicesegment.StatusIn(esportsservicesegment.StatusInService, esportsservicesegment.StatusPaused),
	).Order(ent.Desc(esportsservicesegment.FieldID)).Limit(100).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		return []playerServiceTiming{}, nil
	}
	orderIDs := make([]int, 0, len(segments))
	seen := map[int]bool{}
	for _, segment := range segments {
		if !seen[segment.OrderID] {
			seen[segment.OrderID] = true
			orderIDs = append(orderIDs, segment.OrderID)
		}
	}
	orders, err := db.EsportsOrder.Query().Where(esportsorder.IDIn(orderIDs...)).All(ctx)
	if err != nil {
		return nil, err
	}
	byOrder := make(map[int]*ent.EsportsOrder, len(orders))
	for _, order := range orders {
		byOrder[order.ID] = order
	}
	allSegments, err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDIn(orderIDs...),
	).Select(esportsservicesegment.FieldOrderID, esportsservicesegment.FieldReportedUnits).All(ctx)
	if err != nil {
		return nil, err
	}
	served := map[int]int64{}
	for _, segment := range allSegments {
		served[segment.OrderID] += segment.ReportedUnits
	}
	summaryOf := func(orderID int) orderFulfillmentSummary {
		order := byOrder[orderID]
		summary := orderFulfillmentSummary{PromisedUnits: 0, AddonUnits: 0}
		if order != nil {
			summary.PromisedUnits = order.PromisedServiceUnits
			summary.AddonUnits = order.AddonUnits
		}
		summary.TotalUnits = summary.PromisedUnits + summary.AddonUnits
		summary.ServedUnits = served[orderID]
		summary.UnfulfilledUnits = summary.TotalUnits - summary.ServedUnits
		if summary.UnfulfilledUnits < 0 {
			summary.UnfulfilledUnits = 0
		}
		return summary
	}
	result := make([]playerServiceTiming, 0, len(segments))
	for _, segment := range segments {
		result = append(result, serviceTimingView(segment, byOrder[segment.OrderID], summaryOf(segment.OrderID), now))
	}
	return result, nil
}

// applySegmentPauseChange 在事务内做一次服务段暂停/恢复的状态迁移，返回事件类型与本段新增暂停秒数。
// 仅供 setPlayerServicePause（本人操作）与 setServicePauseByAdmin（客服代操作）复用，保证两种入口的
// 计时语义完全一致，避免代操作路径出现与本人操作不同的暂停秒数口径。
func applySegmentPauseChange(ctx context.Context, tx *ent.Tx, segment *ent.EsportsServiceSegment,
	pause bool, now time.Time) (string, int64, error) {
	var eventType string
	var pausedDelta int64
	if pause {
		if segment.Status != esportsservicesegment.StatusInService {
			return "", 0, errConflict
		}
		changed, err := tx.EsportsServiceSegment.Update().Where(
			esportsservicesegment.IDEQ(segment.ID), esportsservicesegment.VersionEQ(segment.Version),
			esportsservicesegment.StatusEQ(esportsservicesegment.StatusInService),
		).SetStatus(esportsservicesegment.StatusPaused).SetPausedAt(now).AddVersion(1).Save(ctx)
		if err != nil {
			return "", 0, err
		}
		if changed != 1 {
			return "", 0, errConflict
		}
		eventType = "order.service_paused"
	} else {
		if segment.Status != esportsservicesegment.StatusPaused || segment.PausedAt == nil || now.Before(segment.PausedAt.UTC()) {
			return "", 0, errConflict
		}
		pausedDelta = int64(now.Sub(segment.PausedAt.UTC()).Seconds())
		changed, err := tx.EsportsServiceSegment.Update().Where(
			esportsservicesegment.IDEQ(segment.ID), esportsservicesegment.VersionEQ(segment.Version),
			esportsservicesegment.StatusEQ(esportsservicesegment.StatusPaused),
		).SetStatus(esportsservicesegment.StatusInService).ClearPausedAt().
			AddPausedSeconds(pausedDelta).AddVersion(1).Save(ctx)
		if err != nil {
			return "", 0, err
		}
		if changed != 1 {
			return "", 0, errConflict
		}
		eventType = "order.service_resumed"
	}
	return eventType, pausedDelta, nil
}

func setPlayerServicePause(ctx context.Context, db *ent.Client, playerID, orderID int, pause bool, reason string, now time.Time) (playerServiceTiming, error) {
	reason = strings.TrimSpace(reason)
	if playerID < 1 || orderID < 1 || len([]rune(reason)) > 255 {
		return playerServiceTiming{}, errInvalidInput
	}
	now = now.UTC()
	tx, err := db.Tx(ctx)
	if err != nil {
		return playerServiceTiming{}, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return playerServiceTiming{}, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return playerServiceTiming{}, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		return playerServiceTiming{}, errConflict
	}
	segment, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID), esportsservicesegment.PlayerIDEQ(playerID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return playerServiceTiming{}, errForbidden
	}
	if err != nil {
		return playerServiceTiming{}, err
	}
	if now.Before(segment.StartedAt.UTC()) {
		return playerServiceTiming{}, errInvalidInput
	}
	eventType, pausedDelta, err := applySegmentPauseChange(ctx, tx, segment, pause, now)
	if err != nil {
		return playerServiceTiming{}, err
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, order.Version, "")
	if err != nil {
		return playerServiceTiming{}, err
	}
	if _, err := tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType(eventType).
		SetActorType("player").SetActorID(strconv.Itoa(playerID)).
		SetDetails(map[string]interface{}{
			"seat_index": segment.SeatIndex, "reason": reason, "paused_seconds_added": pausedDelta,
		}).Save(ctx); err != nil {
		return playerServiceTiming{}, err
	}
	if err := tx.Commit(); err != nil {
		return playerServiceTiming{}, err
	}
	segment, err = db.EsportsServiceSegment.Get(ctx, segment.ID)
	if err != nil {
		return playerServiceTiming{}, err
	}
	// ：暂停/恢复的响应同样带逐成员履约量与订单级仍需履约量。
	_, _, summary, err := loadOrderFulfillment(ctx, db, orderID)
	if err != nil {
		return playerServiceTiming{}, err
	}
	return serviceTimingView(segment, order, summary, now), nil
}

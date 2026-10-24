package esports

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
)

// 陪玩可用时段查询：会员优先预约提交前先看「该陪玩某段日期里哪些时段可预约」。
//
// 已占用判定有两个来源，取并集：
//  1. 排单占位：EsportsDispatchSeat 上已邀请 / 已接单的档期（slot_start/slot_end，）；
//  2. 订单档期：该陪玩已接单的订单，其「预定开始时间或实际开始服务时间 + 服务时长」形成的服务窗口。
//
// 本查询只读订单 / 排单数据，不写任何状态，也不占用档期（占位由报单链路负责）。

const (
	// 单次查询最多返回的时段数，避免客户端传入超大区间把响应撑爆。
	playerAvailabilityMaxSlots = 96
	// 单次查询的最长时间跨度（天）。
	playerAvailabilityMaxDays = 14
	// 最小 / 最大时段粒度（分钟）。
	playerAvailabilityMinSlotMinutes = 15
	playerAvailabilityMaxSlotMinutes = 1440

	playerSlotStatusAvailable = "available"
	playerSlotStatusOccupied  = "occupied"

	playerSlotReasonDispatch  = "已排单占位"
	playerSlotReasonInService = "服务进行中"
	playerSlotReasonScheduled = "已预定档期"
)

// playerAvailabilitySlot 是单个时段的可预约判定结果。
type playerAvailabilitySlot struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Status   string    `json:"status"`
	Bookable bool      `json:"bookable"`
	Reason   string    `json:"reason,omitempty"`
	OrderNo  string    `json:"order_no,omitempty"`
}

// playerAvailabilityView 是陪玩可用时段视图。
type playerAvailabilityView struct {
	PlayerID       int                      `json:"player_id"`
	PlayerName     string                   `json:"player_name"`
	From           time.Time                `json:"from"`
	To             time.Time                `json:"to"`
	SlotMinutes    int                      `json:"slot_minutes"`
	OccupiedCount  int                      `json:"occupied_count"`
	AvailableCount int                      `json:"available_count"`
	Slots          []playerAvailabilitySlot `json:"slots"`
}

// playerBusyWindow 是被占用的一个时间窗口。
type playerBusyWindow struct {
	start   time.Time
	end     time.Time
	reason  string
	orderNo string
	orderID int
}

// playerAvailableSlots 计算某陪玩在 [from, to) 内按 slotMinutes 粒度切分的可用时段。
// 与订单 / 排单窗口有重叠的时段返回 occupied（bookable=false），其余 available（bookable=true）。
func playerAvailableSlots(ctx context.Context, db *ent.Client, playerID int,
	from, to time.Time, slotMinutes int) (*playerAvailabilityView, error) {
	if playerID < 1 || from.IsZero() || to.IsZero() {
		return nil, errInvalidInput
	}
	if slotMinutes == 0 {
		slotMinutes = 60
	}
	if slotMinutes < playerAvailabilityMinSlotMinutes || slotMinutes > playerAvailabilityMaxSlotMinutes {
		return nil, fmt.Errorf("%w：时段粒度须为 %d–%d 分钟",
			errInvalidInput, playerAvailabilityMinSlotMinutes, playerAvailabilityMaxSlotMinutes)
	}
	from, to = from.UTC(), to.UTC()
	if !from.Before(to) {
		return nil, fmt.Errorf("%w：查询区间须为「开始早于结束」", errInvalidInput)
	}
	if to.Sub(from) > time.Duration(playerAvailabilityMaxDays)*24*time.Hour {
		return nil, fmt.Errorf("%w：单次最多查询 %d 天档期", errInvalidInput, playerAvailabilityMaxDays)
	}
	player, err := db.EsportsPlayer.Query().Where(esportsplayer.IDEQ(playerID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	busy, err := playerBusyWindows(ctx, db, playerID, from, to)
	if err != nil {
		return nil, err
	}
	step := time.Duration(slotMinutes) * time.Minute
	view := &playerAvailabilityView{
		PlayerID: player.ID, PlayerName: player.DisplayName,
		From: from, To: to, SlotMinutes: slotMinutes,
		Slots: make([]playerAvailabilitySlot, 0, playerAvailabilityMaxSlots),
	}
	for cursor := from; cursor.Before(to) && len(view.Slots) < playerAvailabilityMaxSlots; cursor = cursor.Add(step) {
		end := cursor.Add(step)
		if end.After(to) {
			end = to
		}
		slot := playerAvailabilitySlot{
			Start: cursor, End: end,
			Status: playerSlotStatusAvailable, Bookable: true,
		}
		for _, window := range busy {
			if window.start.Before(end) && cursor.Before(window.end) {
				slot.Status, slot.Bookable = playerSlotStatusOccupied, false
				slot.Reason, slot.OrderNo = window.reason, window.orderNo
				break
			}
		}
		if slot.Bookable {
			view.AvailableCount++
		} else {
			view.OccupiedCount++
		}
		view.Slots = append(view.Slots, slot)
	}
	return view, nil
}

// playerBusyWindows 汇总陪玩在查询区间内的占用窗口：排单档期与服务窗口取并集，按开始时间升序。
func playerBusyWindows(ctx context.Context, db *ent.Client, playerID int, from, to time.Time) ([]playerBusyWindow, error) {
	seats, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusIn(esportsdispatchseat.StatusInvited, esportsdispatchseat.StatusAccepted),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	windows := make([]playerBusyWindow, 0, len(seats))
	orderIDs := make([]int, 0, len(seats))
	seenOrder := map[int]bool{}
	for _, seat := range seats {
		if seat.SlotStart != nil && seat.SlotEnd != nil && seat.SlotStart.Before(*seat.SlotEnd) {
			windows = append(windows, playerBusyWindow{
				start: seat.SlotStart.UTC(), end: seat.SlotEnd.UTC(),
				reason: playerSlotReasonDispatch, orderNo: "",
				orderID: seat.OrderID,
			})
		}
		if !seenOrder[seat.OrderID] {
			seenOrder[seat.OrderID] = true
			orderIDs = append(orderIDs, seat.OrderID)
		}
	}
	if len(orderIDs) > 0 {
		orders, err := db.EsportsOrder.Query().Where(esportsorder.IDIn(orderIDs...)).All(ctx)
		if err != nil {
			return nil, err
		}
		orderNos := make(map[int]string, len(orders))
		for _, order := range orders {
			orderNos[order.ID] = order.OrderNo
			window, ok := orderBusyWindow(order)
			if !ok {
				continue
			}
			reason := playerSlotReasonScheduled
			if order.FulfillmentStatus == esportsorder.FulfillmentStatusInService {
				reason = playerSlotReasonInService
			}
			windows = append(windows, playerBusyWindow{
				start: window.start, end: window.end, reason: reason, orderNo: order.OrderNo,
			})
		}
		for i := range windows {
			if windows[i].orderNo == "" {
				windows[i].orderNo = orderNos[windows[i].orderID]
			}
		}
	}
	filtered := make([]playerBusyWindow, 0, len(windows))
	for _, window := range windows {
		if window.start.Before(to) && from.Before(window.end) {
			filtered = append(filtered, window)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].start.Before(filtered[j].start) })
	return filtered, nil
}

// orderBusyWindow 由订单推出服务窗口：优先「实际开始服务时间 + 服务时长」，
// 其次「预定开始时间 + 服务时长」；两者都没有（如待接单）时返回 false 表示不占档。
func orderBusyWindow(order *ent.EsportsOrder) (playerBusyWindow, bool) {
	duration := time.Duration(order.ServiceDurationMinutes) * time.Minute
	if duration <= 0 {
		duration = time.Hour
	}
	if order.ServiceStartedAt != nil {
		start := order.ServiceStartedAt.UTC()
		return playerBusyWindow{start: start, end: start.Add(duration)}, true
	}
	if order.RequestedStartAt != nil {
		start := order.RequestedStartAt.UTC()
		return playerBusyWindow{start: start, end: start.Add(duration)}, true
	}
	return playerBusyWindow{}, false
}

// playerAvailabilityQueryFrom 解析可用时段查询的区间参数，缺省为「今天起 7 天」。
func playerAvailabilityQueryFrom(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		utc := now.UTC()
		return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	parsed, err := parseReportTime(raw)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

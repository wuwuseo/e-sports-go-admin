package esports

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/global"
)

// §3.3-06 终止服务段。
//
// 与「结束服务」（finishPlayerService，陪玩自己上报）的区别：终止是**客服侧提前结束**某个陪玩的服务段，
// 用于陪玩失联 / 违规 / 客户要求换人等场景，必须登记「已履约量、未履约量、责任方」三项，
// 供后续改派或售后取用（当前实现只留痕，改派与售后沿用既有入口）。
//
// 实现约束（§3.3-06）：**不改 schema**，全部留痕在只增事件 order.service_terminated 的 details 里；
// 责任方复用 serviceResponsibilityParties；服务段本身置 finished 并落已履约量（reported_units）。
// 订单层面沿用「全部席位服务段结束后转 pending_close」的既有口径（player.go:854-870），
// 因此「终止后订单保留已履约量」成立；未履约量由事件承载，交改派 / 售后处理。

// serviceTermination 终止服务段的结果，便于接口回显与二次核对。
type serviceTermination struct {
	OrderID          int    `json:"order_id"`
	PlayerID         int    `json:"player_id"`
	SeatIndex        int    `json:"seat_index"`
	TerminatedUnits  int64  `json:"terminated_units"`
	UnfulfilledUnits int64  `json:"unfulfilled_units"`
	PromisedUnits    int64  `json:"promised_service_units"`
	FaultParty       string `json:"fault_party"`
	EffectiveSeconds int64  `json:"effective_seconds"`
	PausedSeconds    int64  `json:"paused_seconds"`
	Reason           string `json:"reason"`
	Version          int    `json:"version"`
}

// terminatePlayerService 提前结束某陪玩的服务段，并登记已履约量 / 未履约量 / 责任方。
//
// 归属口径同客服代暂停：仅订单负责客服或超级管理员可操作，避免其他客服改写他人订单的履约与责任认定。
// 只接受进行中或已暂停的服务段；已结束的段属历史，应走「服务时长更正」（correctServiceTiming）。
func terminatePlayerService(ctx context.Context, db *ent.Client, adminID, orderID, playerID int,
	isSuper bool, terminatedUnits int64, responsibility, reason string, now time.Time) (serviceTermination, error) {
	responsibility = strings.TrimSpace(responsibility)
	reason = strings.TrimSpace(reason)
	if adminID < 1 || orderID < 1 || playerID < 1 || terminatedUnits < 0 ||
		reason == "" || len([]rune(reason)) > 255 {
		return serviceTermination{}, errInvalidInput
	}
	// 责任方必填且必须落在枚举内：终止的核心产出就是责任认定，留空等于没有结论。
	if _, ok := serviceResponsibilityParties[responsibility]; !ok {
		return serviceTermination{}, errInvalidInput
	}
	now = now.UTC()
	tx, err := db.Tx(ctx)
	if err != nil {
		return serviceTermination{}, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return serviceTermination{}, err
	}
	if err = ensureOrderResponsibleAdmin(order, adminID, isSuper); err != nil {
		return serviceTermination{}, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return serviceTermination{}, err
	}
	segment, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
		esportsservicesegment.PlayerIDEQ(playerID),
		esportsservicesegment.StatusIn(esportsservicesegment.StatusInService, esportsservicesegment.StatusPaused),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return serviceTermination{}, errConflict
	}
	if err != nil {
		return serviceTermination{}, err
	}
	before := serviceTimingAt(segment, now)
	// 暂停中终止要把在途暂停时长落库：服务段一旦不再是 paused，serviceTimingAt 就不会再补这段时长，
	// 否则该段的有效时长会凭空变长、暂停时长也会丢。
	pausedSeconds := segment.PausedSeconds
	if segment.Status == esportsservicesegment.StatusPaused {
		pausedSeconds = before.PausedSeconds
	}
	changed, err := tx.EsportsServiceSegment.Update().Where(
		esportsservicesegment.IDEQ(segment.ID),
		esportsservicesegment.VersionEQ(segment.Version),
		esportsservicesegment.StatusIn(esportsservicesegment.StatusInService, esportsservicesegment.StatusPaused),
	).SetStatus(esportsservicesegment.StatusFinished).SetEndedAt(now).
		SetReportedUnits(terminatedUnits).SetSystemSeconds(before.EffectiveSeconds).
		SetPausedSeconds(pausedSeconds).ClearPausedAt().AddVersion(1).Save(ctx)
	if err != nil {
		return serviceTermination{}, err
	}
	if changed != 1 {
		return serviceTermination{}, errConflict
	}
	// 已履约量按「订单全部服务段上报量之和」汇总，与 close_condition.go 的 service_units 同口径；
	// 未履约量 = 承诺量（含加钟）− 已履约量，不落库、只在事件与响应里给出。
	segments, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
	).All(ctx)
	if err != nil {
		return serviceTermination{}, err
	}
	servedUnits := int64(0)
	for _, item := range segments {
		servedUnits += item.ReportedUnits
	}
	promisedUnits := order.PromisedServiceUnits + order.AddonUnits
	unfulfilledUnits := promisedUnits - servedUnits
	if unfulfilledUnits < 0 {
		unfulfilledUnits = 0
	}
	// P1-02：待结单判定改为「全部在册席位都了结」（服务段已结束 / 席位已移除），
	// 不再用 finishedCount >= seatCount —— 有席位从未开工时该条件永远不成立，订单会永久卡在服务中。
	settled, pendingSeats, err := orderAllSeatsServiceSettled(ctx, tx, orderID)
	if err != nil {
		return serviceTermination{}, err
	}
	target := esportsorder.FulfillmentStatus("")
	if settled {
		target = esportsorder.FulfillmentStatusPendingClose
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, order.Version, target)
	if err != nil {
		return serviceTermination{}, err
	}
	details := map[string]interface{}{
		"seat_index":             segment.SeatIndex,
		"terminated_units":       terminatedUnits,
		"unfulfilled_units":      unfulfilledUnits,
		"promised_service_units": promisedUnits,
		"fault_party":            responsibility,
		"effective_seconds":      before.EffectiveSeconds,
		"paused_seconds":         pausedSeconds,
		"reason":                 reason,
	}
	if len(pendingSeats) > 0 {
		details["unsettled_seat_indexes"] = pendingSeats
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.service_terminated").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(details).Save(ctx); err != nil {
		return serviceTermination{}, err
	}
	if err = tx.Commit(); err != nil {
		return serviceTermination{}, err
	}
	return serviceTermination{
		OrderID: orderID, PlayerID: playerID, SeatIndex: segment.SeatIndex,
		TerminatedUnits: terminatedUnits, UnfulfilledUnits: unfulfilledUnits,
		PromisedUnits: promisedUnits, FaultParty: responsibility,
		EffectiveSeconds: before.EffectiveSeconds, PausedSeconds: pausedSeconds,
		Reason: reason, Version: segment.Version + 1,
	}, nil
}

// adminTerminatePlayerServiceHandler 客服侧提前终止某陪玩的服务段（§3.3-06）。
func adminTerminatePlayerServiceHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	playerID, err := parsePlayerPathID(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	var in struct {
		TerminatedUnits int64  `json:"terminated_units"`
		Responsibility  string `json:"responsibility"`
		Reason          string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	result, err := terminatePlayerService(c.Context(), global.ORM, adminID, orderID, playerID, isSuper,
		in.TerminatedUnits, in.Responsibility, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, result)
}

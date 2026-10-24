package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/systemconfig"
)

const (
	// serviceDisconnectPauseKey 断线自动暂停阈值（分钟，）：缺省或非法视为关闭，不自动暂停。
	serviceDisconnectPauseKey = "app.esports.service_disconnect_pause_minutes"
	// serviceDisconnectActor 断线自动暂停事件的系统操作人标识，便于与人工操作区分。
	serviceDisconnectActor = "disconnect_watchdog"
	// serviceDisconnectReason 断线自动暂停的事件原因文案。
	serviceDisconnectReason = "断线自动暂停"
	// serviceTimingCorrectionMaxRatioBp 服务时长更正上界的订单系数（万分比，P1-14）：12000 = 1.2 倍。
	// 更正后的有效秒数不得超过订单标准时长 × 1.2，给正常超时与补时留余量但不允许无限放大结单时长
	// 与按秒分摊的分成。
	// ponytail: 系数先用内置常量；若要按游戏 / 等级可配，需要功能集 Config 声明（app.go 归他人维护）。
	serviceTimingCorrectionMaxRatioBp = 12000
)

// serviceResponsibilityParties 停工责任方枚举：陪玩 / 客户 / 平台 / 不可抗力。
var serviceResponsibilityParties = map[string]struct{}{
	"player": {}, "customer": {}, "platform": {}, "force_majeure": {},
}

// serviceTimingCorrection 争议更正结果：记录更正前后值与停工量 / 责任方，便于接口回显与二次核对。
type serviceTimingCorrection struct {
	OrderID         int    `json:"order_id"`
	PlayerID        int    `json:"player_id"`
	SeatIndex       int    `json:"seat_index"`
	EffectiveBefore int64  `json:"effective_seconds_before"`
	EffectiveAfter  int64  `json:"effective_seconds_after"`
	PausedBefore    int64  `json:"paused_seconds_before"`
	PausedAfter     int64  `json:"paused_seconds_after"`
	DowntimeSeconds int64  `json:"downtime_seconds"`
	DowntimeCount   int    `json:"downtime_count"`
	Responsibility  string `json:"responsibility"`
	Reason          string `json:"reason"`
	Version         int    `json:"version"`
}

// serviceDisconnectPauseTimeout 断线自动暂停阈值：配置缺失或非法（非正整数 / 超出 24 小时）时返回
// enabled=false，表示关闭该自动化，避免误暂停全部服务中陪玩。
func serviceDisconnectPauseTimeout(ctx context.Context, db *ent.Client) (time.Duration, bool) {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(serviceDisconnectPauseKey)).Only(ctx)
	if err != nil {
		return 0, false
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || minutes < 1 || minutes > 24*60 {
		return 0, false
	}
	return time.Duration(minutes) * time.Minute, true
}

// ensureOrderResponsibleAdmin 校验写操作的订单归属：只有订单负责客服或超级管理员可代操作。
// 与 order_access.loadStrictOwnedOrder 同一口径，但这里在事务内已加载订单，避免重复查询；
// 协作客服只有读权限，不能借代暂停 / 时长更正改写他人订单。
func ensureOrderResponsibleAdmin(order *ent.EsportsOrder, adminID int, isSuper bool) error {
	if isSuper {
		return nil
	}
	if order == nil || order.ResponsibleAdminID == nil || *order.ResponsibleAdminID != adminID {
		return errForbidden
	}
	return nil
}

// setServicePauseByAdmin 客服代陪玩暂停 / 恢复服务段：校验订单归属（负责客服或超管）、订单在服务中、
// 目标陪玩为该单服务成员、理由必填；事件 actor_type=admin、actor_id=管理员 ID，details 用
// on_behalf_of_player_id 记录被代操作的陪玩，以区分「本人操作」与「客服代操作」。暂停 / 恢复的计时口径复用本人操作路径。
func setServicePauseByAdmin(ctx context.Context, db *ent.Client, adminID, orderID, playerID int,
	isSuper, pause bool, reason string, now time.Time) (playerServiceTiming, error) {
	reason = strings.TrimSpace(reason)
	if adminID < 1 || orderID < 1 || playerID < 1 || reason == "" || len([]rune(reason)) > 255 {
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
	if err = ensureOrderResponsibleAdmin(order, adminID, isSuper); err != nil {
		return playerServiceTiming{}, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return playerServiceTiming{}, err
	}
	// ：暂停要求订单在服务中；恢复额外允许订单已因「记名停工」进入待结单
	// （客服误操作或陪玩重新联系上后可回退到服务中），避免整单被永久卡在待结单。
	if pause {
		if order.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
			return playerServiceTiming{}, errConflict
		}
	} else if order.FulfillmentStatus != esportsorder.FulfillmentStatusInService &&
		order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		return playerServiceTiming{}, errConflict
	}
	// 服务段是订单服务成员的信用锚点：找不到服务段即非本单成员。
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
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType(eventType).
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"seat_index":             segment.SeatIndex,
			"reason":                 reason,
			"paused_seconds_added":   pausedDelta,
			"on_behalf_of_player_id": playerID,
		}).Save(ctx); err != nil {
		return playerServiceTiming{}, err
	}
	// ：客服记名停工后按剩余履约量判定整单是否进入待结单。
	if pause {
		if _, err = transitionToPendingCloseOnAdminStop(ctx, tx, orderID, newVersion, adminID, now); err != nil {
			return playerServiceTiming{}, err
		}
	} else if order.FulfillmentStatus == esportsorder.FulfillmentStatusPendingClose {
		// 恢复：整单从待结单回到服务中（服务段已复为 in_service）。
		if _, err = bumpOrder(ctx, tx, orderID, newVersion, esportsorder.FulfillmentStatusInService); err != nil {
			return playerServiceTiming{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return playerServiceTiming{}, err
	}
	segment, err = db.EsportsServiceSegment.Get(ctx, segment.ID)
	if err != nil {
		return playerServiceTiming{}, err
	}
	// ：代暂停/恢复的响应同样带逐成员履约量与订单级仍需履约量。
	_, _, summary, err := loadOrderFulfillment(ctx, db, orderID)
	if err != nil {
		return playerServiceTiming{}, err
	}
	return serviceTimingView(segment, order, summary, now), nil
}

// serviceTimingCorrectionBound 计算服务段有效秒数更正的上界（P1-14）：
// 返回墙钟跨度（服务段实际经过的秒数）与最终上界 min(墙钟跨度, 订单时长上限×系数)。
// 订单未配置标准时长（ServiceDurationMinutes<=0）时只受墙钟跨度约束；
// 服务段尚未结束时按 now 作为墙钟终点，与 serviceTimingAt 的口径一致。
func serviceTimingCorrectionBound(order *ent.EsportsOrder, segment *ent.EsportsServiceSegment,
	now time.Time) (span, upper int64) {
	if segment == nil {
		return 0, 0
	}
	end := now.UTC()
	if segment.EndedAt != nil {
		end = segment.EndedAt.UTC()
	}
	if diff := int64(end.Sub(segment.StartedAt.UTC()).Seconds()); diff > 0 {
		span = diff
	}
	upper = span
	if order != nil && order.ServiceDurationMinutes > 0 {
		limit := int64(order.ServiceDurationMinutes) * 60 * serviceTimingCorrectionMaxRatioBp / 10000
		if limit < upper {
			upper = limit
		}
	}
	return span, upper
}

// serviceTimingReadout 是服务段计时只读接口的视图（审核报告 缺口：时长更正无法预填）：
// 前端据此预填「当前有效时长」与「可填上限」，避免猜一个数再被更正校验拒绝。
// 上界与更正校验同源（serviceTimingCorrectionBound），两处口径不会漂移。
type serviceTimingReadout struct {
	OrderID   int    `json:"order_id"`
	PlayerID  int    `json:"player_id"`
	SeatIndex int    `json:"seat_index"`
	Status    string `json:"status"`
	// EffectiveSeconds 当前有效服务秒数（暂停期间冻结，已结束服务段取落库值）。
	EffectiveSeconds int64 `json:"effective_seconds"`
	// WallClockSeconds 服务段墙钟跨度（开始到结束 / 现在），是暂停秒数与有效秒数的共同上界。
	WallClockSeconds int64 `json:"wall_clock_seconds"`
	// UpperBoundSeconds 更正可填的有效秒数上限 = min(墙钟跨度, 订单标准时长×系数)。
	UpperBoundSeconds int64 `json:"upper_bound_seconds"`
	// PausedSeconds 当前累计暂停秒数（进行中的暂停按 now 续算）。
	PausedSeconds int64 `json:"paused_seconds"`
}

// serviceTimingForAdmin 读取某订单某陪玩服务段的计时口径（只读）：不写事件、不改状态，
// 取值全部复用既有 serviceTimingAt（有效 / 暂停秒数）与 serviceTimingCorrectionBound（墙钟跨度与上界）。
// 目标陪玩在本单没有服务段时返回 errForbidden，与代暂停 / 更正同一「非本单成员」口径。
func serviceTimingForAdmin(ctx context.Context, db *ent.Client, orderID, playerID int,
	now time.Time) (serviceTimingReadout, error) {
	if orderID < 1 || playerID < 1 {
		return serviceTimingReadout{}, errInvalidInput
	}
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return serviceTimingReadout{}, err
	}
	segment, err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID), esportsservicesegment.PlayerIDEQ(playerID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return serviceTimingReadout{}, errForbidden
	}
	if err != nil {
		return serviceTimingReadout{}, err
	}
	span, upper := serviceTimingCorrectionBound(order, segment, now)
	timing := serviceTimingAt(segment, now)
	// 已结束服务段的有效秒数以落库的 system_seconds 为准：时长更正就是该字段的唯一写入口，
	// 若此处仍按墙钟重算，更正后的值与接口读回值不一致，前端预填就会把上一次更正覆盖掉。
	// 进行中的服务段没有最终落库值，继续用 serviceTimingAt 的实时口径。
	effective := timing.EffectiveSeconds
	if segment.Status == esportsservicesegment.StatusFinished {
		effective = segment.SystemSeconds
	}
	return serviceTimingReadout{
		OrderID: orderID, PlayerID: playerID, SeatIndex: segment.SeatIndex,
		Status:            string(segment.Status),
		EffectiveSeconds:  effective,
		WallClockSeconds:  span,
		UpperBoundSeconds: upper,
		PausedSeconds:     timing.PausedSeconds,
	}, nil
}

// ensureServiceTimingCorrectionWithinBound 校验更正后的有效 / 暂停秒数（P1-14）：
// 有效秒数必须满足 0 < 新时长 <= min(服务段墙钟跨度, 订单时长上限×系数)，
// 暂停秒数不得超过墙钟跨度（暂停是墙钟时间内的一部分，超出即不可能发生）。
// span / upper 由 serviceTimingCorrectionBound 算好传入，避免重复推导同一对边界。
// 越界一律返回带具体上下界的中文错误，绝不静默截断后落库。
func ensureServiceTimingCorrectionWithinBound(order *ent.EsportsOrder, span, upper int64,
	effectiveSeconds, pausedSeconds int64) error {
	if upper <= 0 {
		return fmt.Errorf("%w：该服务段墙钟跨度为 0 秒，没有可更正的计时区间", errInvalidInput)
	}
	if effectiveSeconds <= 0 || effectiveSeconds > upper {
		limitMinutes := 0
		if order != nil {
			limitMinutes = order.ServiceDurationMinutes
		}
		return fmt.Errorf(
			"%w：更正后的有效秒数须在 1–%d 秒之间（服务段墙钟跨度 %d 秒，订单标准时长 %d 分钟×系数 %d%%）",
			errInvalidInput, upper, span, limitMinutes, serviceTimingCorrectionMaxRatioBp/100)
	}
	if pausedSeconds > span {
		return fmt.Errorf("%w：更正后的暂停秒数 %d 秒超过服务段墙钟跨度 %d 秒，请按实际停工时长填写",
			errInvalidInput, pausedSeconds, span)
	}
	return nil
}

// correctServiceTiming 争议更正：管理员对已结束服务段的有效 / 暂停秒数做留痕更正，并登记停工量
// （累计暂停秒数 + 停工次数）与责任方。更正必须写只增事件 order.service_timing_corrected，记录更正前后值、
// 原因与操作人，绝不静默改写。原始计时随更正一并变化，历史以事件为准。
// 仅对 status=finished 的服务段开放：进行中的服务段应通过客服代暂停 / 恢复（setServicePauseByAdmin）调整，
// 否则结束时按实际点击间隔重算 system_seconds 会覆盖更正结果。
// 归属口径同代暂停：仅订单负责客服或超级管理员可更正，避免其他客服改写他人订单的计时与停工量。
// P1-14：更正后的有效秒数受限（见 ensureServiceTimingCorrectionWithinBound），越界直接拒绝；
// 留痕口径不变——原因必填、操作人落 actor_id、前后值与上下界一并进事件。
func correctServiceTiming(ctx context.Context, db *ent.Client, adminID, orderID, playerID int,
	isSuper bool, effectiveSeconds, pausedSeconds int64, responsibility, reason string, now time.Time) (serviceTimingCorrection, error) {
	responsibility = strings.TrimSpace(responsibility)
	reason = strings.TrimSpace(reason)
	if adminID < 1 || orderID < 1 || playerID < 1 || effectiveSeconds < 0 || pausedSeconds < 0 ||
		reason == "" || len([]rune(reason)) > 255 {
		return serviceTimingCorrection{}, errInvalidInput
	}
	if responsibility != "" {
		if _, ok := serviceResponsibilityParties[responsibility]; !ok {
			return serviceTimingCorrection{}, errInvalidInput
		}
	}
	now = now.UTC()
	tx, err := db.Tx(ctx)
	if err != nil {
		return serviceTimingCorrection{}, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return serviceTimingCorrection{}, err
	}
	if err = ensureOrderResponsibleAdmin(order, adminID, isSuper); err != nil {
		return serviceTimingCorrection{}, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return serviceTimingCorrection{}, err
	}
	segment, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID), esportsservicesegment.PlayerIDEQ(playerID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return serviceTimingCorrection{}, errForbidden
	}
	if err != nil {
		return serviceTimingCorrection{}, err
	}
	if segment.Status != esportsservicesegment.StatusFinished {
		return serviceTimingCorrection{}, errConflict
	}
	// P1-14：有效秒数必须在 [1, min(墙钟跨度, 订单时长上限×系数)] 内，暂停秒数不得超过墙钟跨度。
	span, upper := serviceTimingCorrectionBound(order, segment, now)
	if err = ensureServiceTimingCorrectionWithinBound(order, span, upper, effectiveSeconds, pausedSeconds); err != nil {
		return serviceTimingCorrection{}, err
	}
	before := serviceTimingAt(segment, now)
	changed, err := tx.EsportsServiceSegment.Update().Where(
		esportsservicesegment.IDEQ(segment.ID), esportsservicesegment.VersionEQ(segment.Version),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusFinished),
	).SetSystemSeconds(effectiveSeconds).SetPausedSeconds(pausedSeconds).AddVersion(1).Save(ctx)
	if err != nil {
		return serviceTimingCorrection{}, err
	}
	if changed != 1 {
		return serviceTimingCorrection{}, errConflict
	}
	downtimeCount, err := countSeatServiceDowntime(ctx, tx.Client(), orderID, segment.SeatIndex)
	if err != nil {
		return serviceTimingCorrection{}, err
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, order.Version, "")
	if err != nil {
		return serviceTimingCorrection{}, err
	}
	// P1-14：更正上界一并进事件，复核时不需要再反查服务段与订单配置。
	details := map[string]interface{}{
		"seat_index":                    segment.SeatIndex,
		"effective_seconds_before":      before.EffectiveSeconds,
		"effective_seconds_after":       effectiveSeconds,
		"paused_seconds_before":         before.PausedSeconds,
		"paused_seconds_after":          pausedSeconds,
		"downtime_seconds":              pausedSeconds,
		"downtime_count":                downtimeCount,
		"responsibility":                responsibility,
		"reason":                        reason,
		"wall_clock_span_seconds":       span,
		"effective_seconds_upper_bound": upper,
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.service_timing_corrected").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(details).Save(ctx); err != nil {
		return serviceTimingCorrection{}, err
	}
	// ：登记停工量 / 责任方后同样按剩余履约量判定整单是否进入待结单
	// （若全部席位已结束，结单路径已经推进过，此处为幂等的补充判定）。
	if _, err = transitionToPendingCloseOnAdminStop(ctx, tx, orderID, newVersion, adminID, now); err != nil {
		return serviceTimingCorrection{}, err
	}
	if err = tx.Commit(); err != nil {
		return serviceTimingCorrection{}, err
	}
	return serviceTimingCorrection{
		OrderID: orderID, PlayerID: playerID, SeatIndex: segment.SeatIndex,
		EffectiveBefore: before.EffectiveSeconds, EffectiveAfter: effectiveSeconds,
		PausedBefore: before.PausedSeconds, PausedAfter: pausedSeconds,
		DowntimeSeconds: pausedSeconds, DowntimeCount: downtimeCount,
		Responsibility: responsibility, Reason: reason, Version: segment.Version + 1,
	}, nil
}

// countSeatServiceDowntime 统计某订单某席位的停工次数：以只增事件 order.service_paused 的条数为准，
// 与服务段累计暂停秒数（停工量）配合使用。details 中的 seat_index 以 JSON 反序列化为 float64。
func countSeatServiceDowntime(ctx context.Context, db *ent.Client, orderID, seatIndex int) (int, error) {
	events, err := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(orderID),
		esportsorderevent.EventTypeEQ("order.service_paused"),
	).All(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, event := range events {
		if seatIndexOf(event.Details) == seatIndex {
			count++
		}
	}
	return count, nil
}

// seatIndexOf 读取事件 details 中的 seat_index，兼容 JSON 数字的多种解码类型。
func seatIndexOf(details map[string]interface{}) int {
	if details == nil {
		return 0
	}
	switch v := details["seat_index"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// autoPauseDisconnectedPlayers 断线超阈值自动暂停：阈值由
// app.esports.service_disconnect_pause_minutes 配置（缺省 0 即关闭）。对「存在 in_service 服务段」
// 且心跳已超阈值未更新的陪玩，把其服务段置 paused 并写 order.service_paused 事件（原因注明「断线自动暂停」，
// actor_type=system）。幂等：服务段一旦转为 paused，后续运行不再匹配；心跳恢复并重新开始新服务段才会再次触发。
// 返回本次自动暂停的服务段数量，由定时任务负责注册与周期调用。
func autoPauseDisconnectedPlayers(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	timeout, enabled := serviceDisconnectPauseTimeout(ctx, db)
	if !enabled {
		return 0, nil
	}
	now = now.UTC()
	cutoff := now.Add(-timeout)
	players, err := db.EsportsPlayer.Query().Where(
		esportsplayer.LastHeartbeatAtNotNil(),
		esportsplayer.LastHeartbeatAtLT(cutoff),
	).Order(ent.Asc(esportsplayer.FieldLastHeartbeatAt)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, player := range players {
		segments, err := db.EsportsServiceSegment.Query().Where(
			esportsservicesegment.PlayerIDEQ(player.ID),
			esportsservicesegment.StatusEQ(esportsservicesegment.StatusInService),
		).All(ctx)
		if err != nil {
			return count, err
		}
		for _, segment := range segments {
			paused, err := pauseSegmentForDisconnect(ctx, db, segment, player, timeout, now)
			if err != nil && !ent.IsNotFound(err) && !errors.Is(err, errConflict) {
				return count, err
			}
			if paused {
				count++
			}
		}
	}
	return count, nil
}

// pauseSegmentForDisconnect 在独立事务内把一个服务段置 paused 并写断线自动暂停事件。
// 返回 false 表示并发已抢先处理（服务段状态或订单版本变化），跳过即可。
func pauseSegmentForDisconnect(ctx context.Context, db *ent.Client, segment *ent.EsportsServiceSegment,
	player *ent.EsportsPlayer, timeout time.Duration, now time.Time) (bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, segment.OrderID)
	if err != nil {
		return false, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		// 冻结订单禁止服务段状态变更：跳过本次自动暂停，恢复后再由后续周期处理。
		return false, nil
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		return false, nil
	}
	changed, err := tx.EsportsServiceSegment.Update().Where(
		esportsservicesegment.IDEQ(segment.ID), esportsservicesegment.VersionEQ(segment.Version),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusInService),
	).SetStatus(esportsservicesegment.StatusPaused).SetPausedAt(now).AddVersion(1).Save(ctx)
	if err != nil {
		return false, err
	}
	if changed != 1 {
		return false, nil
	}
	newVersion, err := bumpOrder(ctx, tx, segment.OrderID, order.Version, "")
	if err != nil {
		return false, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(segment.OrderID).
		SetOrderVersion(newVersion).SetEventType("order.service_paused").
		SetActorType("system").SetActorID(serviceDisconnectActor).
		SetDetails(map[string]interface{}{
			"seat_index":             segment.SeatIndex,
			"reason":                 serviceDisconnectReason,
			"auto":                   true,
			"timeout_minutes":        int(timeout.Minutes()),
			"last_heartbeat_at":      player.LastHeartbeatAt.UTC(),
			"on_behalf_of_player_id": player.ID,
			"paused_seconds_added":   int64(0),
		}).Save(ctx); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

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

type playerServiceTiming struct {
	OrderID          int        `json:"order_id"`
	Status           string     `json:"status"`
	StartedAt        time.Time  `json:"started_at"`
	PausedAt         *time.Time `json:"paused_at"`
	EffectiveSeconds int64      `json:"effective_seconds"`
	PausedSeconds    int64      `json:"paused_seconds"`
}

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
		OrderID: segment.OrderID, Status: string(segment.Status),
		StartedAt: segment.StartedAt, PausedAt: segment.PausedAt,
		EffectiveSeconds: effective, PausedSeconds: paused,
	}
}

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
	result := make([]playerServiceTiming, 0, len(segments))
	for _, segment := range segments {
		result = append(result, serviceTimingAt(segment, now))
	}
	return result, nil
}

// applySegmentPauseChange 在事务内做一次服务段暂停/恢复的状态迁移（§3.3），返回事件类型与本段新增暂停秒数。
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
	return serviceTimingAt(segment, now), nil
}

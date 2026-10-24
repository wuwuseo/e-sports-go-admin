package esports

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/systemconfig"
)

const (
	playerHeartbeatTimeoutKey     = "app.esports.player_heartbeat_timeout_minutes"
	defaultPlayerHeartbeatTimeout = 10
)

// playerHeartbeatTimeout 心跳超时阈值：配置缺失或非法时回落 10 分钟。
func playerHeartbeatTimeout(ctx context.Context, db *ent.Client) time.Duration {
	fallback := time.Duration(defaultPlayerHeartbeatTimeout) * time.Minute
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(playerHeartbeatTimeoutKey)).Only(ctx)
	if err != nil {
		return fallback
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || minutes < 1 || minutes > 24*60 {
		return fallback
	}
	return time.Duration(minutes) * time.Minute
}

// recordPlayerHeartbeat 陪玩端保活：只写心跳时戳，不改变在线状态，
// 避免客户端把一次保活误当作上线 / 下线操作。
//
// 例外（自动上线）：开启 auto_online 且心跳时「处于本人服务时段内」时，把 offline
// 自动置为 online——这就是「自动上线」的生效点。自动上线只负责上线，不在时段外把人踢下线
// （时段外由心跳超时任务按失联口径收敛），也不会覆盖 protected_abnormal。
func recordPlayerHeartbeat(ctx context.Context, db *ent.Client, playerID int, now time.Time) (*ent.EsportsPlayer, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	player, err := db.EsportsPlayer.Get(ctx, playerID)
	if err != nil {
		return nil, err
	}
	if player.AutoOnline && player.Presence == esportsplayer.PresenceOffline &&
		playerServiceSlotAllows(player, now) {
		changed, err := db.EsportsPlayer.Update().Where(
			esportsplayer.IDEQ(playerID), esportsplayer.VersionEQ(player.Version),
			esportsplayer.PresenceEQ(esportsplayer.PresenceOffline),
		).SetPresence(esportsplayer.PresenceOnline).SetLastHeartbeatAt(now.UTC()).
			AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
		return db.EsportsPlayer.Get(ctx, playerID)
	}
	changed, err := db.EsportsPlayer.Update().Where(esportsplayer.IDEQ(playerID)).
		SetLastHeartbeatAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsPlayer.Get(ctx, playerID)
}

// autoOfflineStalePlayers 心跳超时自动离线：只处理「在线且已有心跳基线」的陪玩，
// 无心跳记录的存量档案不动（避免升级后一次性把在线陪玩全部离线）。
// 存在在途订单（待接单 / 已接单 / 服务中订单上的已接单席位）时转 protected_abnormal 并写外盒提醒，
// 保证自动离线不会直接丢掉在途履约；其余转 offline。返回本次处理条数。
func autoOfflineStalePlayers(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	cutoff := now.UTC().Add(-playerHeartbeatTimeout(ctx, db))
	candidates, err := db.EsportsPlayer.Query().Where(
		esportsplayer.PresenceEQ(esportsplayer.PresenceOnline),
		esportsplayer.LastHeartbeatAtNotNil(),
		esportsplayer.LastHeartbeatAtLT(cutoff),
	).Order(ent.Asc(esportsplayer.FieldLastHeartbeatAt)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, player := range candidates {
		inflight, err := playerHasInflightOrder(ctx, db, player.ID)
		if err != nil {
			return count, err
		}
		target := esportsplayer.PresenceOffline
		reason := "心跳超时自动离线"
		if inflight {
			target = esportsplayer.PresenceProtectedAbnormal
			reason = "心跳超时且存在在途订单，转保护异常待客服核实"
		}
		changed, err := db.EsportsPlayer.Update().Where(
			esportsplayer.IDEQ(player.ID), esportsplayer.VersionEQ(player.Version),
			esportsplayer.PresenceEQ(esportsplayer.PresenceOnline),
		).SetPresence(target).AddVersion(1).Save(ctx)
		if err != nil {
			return count, err
		}
		if changed != 1 {
			continue
		}
		// 幂等键含心跳时戳：同一次「失联」只提醒一次，重新上线后再次失联才再提醒。
		eventKey := "player.heartbeat_timeout:" + strconv.Itoa(player.ID) + ":" +
			strconv.FormatInt(player.LastHeartbeatAt.Unix(), 10)
		if _, err = db.EsportsOutbox.Create().
			SetEventKey(eventKey).SetEventType("player.heartbeat_timeout").
			SetAggregateRef(strconv.Itoa(player.ID)).
			SetPayload(map[string]interface{}{
				"player_id": player.ID, "presence": target.String(),
				"reason": reason, "inflight_order": inflight,
			}).Save(ctx); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// playerCanGrabNewOrder 判定陪玩当前是否具备「抢新单」资格：
// 连接异常（protected_abnormal）一律禁止；online 视为可抢（失联 online 由超时任务收敛为
// offline / protected_abnormal）；人工下线（offline）仅在心跳新鲜（连接正常）时仍可抢，
// 断开后心跳陈旧自然失去抢单资格。
func playerCanGrabNewOrder(ctx context.Context, db *ent.Client, player *ent.EsportsPlayer, now time.Time) bool {
	if player == nil || player.Presence == esportsplayer.PresenceProtectedAbnormal {
		return false
	}
	if player.Presence == esportsplayer.PresenceOnline {
		return true
	}
	if player.LastHeartbeatAt == nil {
		return false
	}
	return player.LastHeartbeatAt.After(now.UTC().Add(-playerHeartbeatTimeout(ctx, db)))
}

// playerHasInflightOrder 判断陪玩是否有在途订单：已接单席位落在待接单 / 已接单 / 服务中的订单上。
func playerHasInflightOrder(ctx context.Context, db *ent.Client, playerID int) (bool, error) {
	seats, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
	).All(ctx)
	if err != nil || len(seats) == 0 {
		return false, err
	}
	ids := make([]int, 0, len(seats))
	for _, seat := range seats {
		ids = append(ids, seat.OrderID)
	}
	return db.EsportsOrder.Query().Where(
		esportsorder.IDIn(ids...),
		esportsorder.FulfillmentStatusIn(
			esportsorder.FulfillmentStatusPendingAcceptance,
			esportsorder.FulfillmentStatusAccepted,
			esportsorder.FulfillmentStatusInService,
		),
	).Exist(ctx)
}

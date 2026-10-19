package esports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportssku"
)

// playerLevelRank 等级 → 序数，用于比较（novice < skilled < senior < mentor）。
func playerLevelRank(level string) int {
	switch level {
	case "skilled":
		return 1
	case "senior":
		return 2
	case "mentor":
		return 3
	default:
		return 0
	}
}

// checkPlayerGameLevel 校验陪玩游戏分区权限与技能等级（§5.5 硬性条件）。
// games 为空表示不限分区；game 为空（订单解析不到分区）跳过权限校验；
// 陪玩等级低于订单所需等级时拒绝。
func checkPlayerGameLevel(player *ent.EsportsPlayer, game, requiredLevel string) error {
	if game != "" && len(player.Games) > 0 {
		found := false
		for _, g := range player.Games {
			if g == game {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w：该陪玩未开通「%s」游戏分区的接单权限", errForbidden, game)
		}
	}
	if playerLevelRank(player.Level.String()) < playerLevelRank(requiredLevel) {
		return fmt.Errorf("%w：该陪玩技能等级低于订单要求（%s）", errForbidden, requiredLevel)
	}
	return nil
}

// ensureOrderSkuDispatchable 校验订单绑定的 SKU 快照在派单时仍可售（§5.9-02）。
//
// 派单事务内必须先过这道关：SKU 缺失时 orderGameIn 会返回空游戏分区，进而让
// checkPlayerGameLevel 静默跳过分区权限校验，等于绕过 §5.5 的硬性条件。
//
//   - sku_id <= 0：管理员补录的无 SKU 历史单（§15.6），跳过校验；
//   - SKU 存在：status 必须为 on_sale；draft / off_sale 一律拒绝（停服走 SKU 停服流程单独处置）；
//   - SKU 缺失：仅当订单带下单时的 SKU 编号快照（sku_code 非空 = 常规绑定单）时拒绝；
//     无快照的历史单按其原有口径继续（无法回溯绑定关系）。
func ensureOrderSkuDispatchable(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder) error {
	if order == nil || order.SkuID <= 0 {
		return nil
	}
	sku, err := tx.EsportsSku.Get(ctx, order.SkuID)
	if ent.IsNotFound(err) {
		if strings.TrimSpace(order.SkuCode) == "" {
			return nil
		}
		return fmt.Errorf("%w：订单绑定的商品已不存在（SKU %d），请先恢复商品再派单", errInvalidInput, order.SkuID)
	}
	if err != nil {
		return err
	}
	if sku.Status != esportssku.StatusOnSale {
		return fmt.Errorf("%w：订单绑定的商品「%s」当前状态为 %s，未在售，无法派单（SKU %d）",
			errInvalidInput, sku.Title, sku.Status, sku.ID)
	}
	return nil
}

// ── §6.4-06 接单配置生效逻辑（服务时段 / 可接单价 / 固排开关与时段） ──

// playerMinuteOfDay 返回 now 在业务时区（UTC+8）的当日分钟数（0~1439）。
// schema 的服务时段字段即按「UTC+8 当日分钟数」定义，因此统一用 UTC+8，不读陪玩时区，
// 避免同一配置在不同时区档案上出现两种生效口径。
func playerMinuteOfDay(now time.Time) int {
	local := now.In(cstZone)
	return local.Hour()*60 + local.Minute()
}

// withinMinuteWindow 判断 minute 是否落在 [start, end) 窗口内：
//   - start == end → 不限时段（含 0/0 默认值）；
//   - end == 0 且 start > 0 → 视为 [start, 24:00)；
//   - start > end → 跨天窗口（如 22:00–02:00）。
func withinMinuteWindow(minute, start, end int) bool {
	if start == end {
		return true
	}
	if end == 0 {
		end = 24 * 60
	}
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}

// playerServiceSlotAllows 服务时段（§6.4-06）：未配置（0/0）恒为 true；
// 配置后只有落在时段内才允许被派单（候选池、优先通知、人工指派三处同口径）。
func playerServiceSlotAllows(player *ent.EsportsPlayer, now time.Time) bool {
	if player == nil {
		return false
	}
	return withinMinuteWindow(playerMinuteOfDay(now),
		player.ServiceSlotStartMinute, player.ServiceSlotEndMinute)
}

// playerFixedTeamSlotAllows 固排时段（§6.4-06）：
//   - fixed_team_enabled=false（schema 默认）→ 不限时段，沿用既有固排行为（存量陪玩不受影响）；
//   - 开启后按固排时段 [start, end) 限制，0/0 表示不限时段。
func playerFixedTeamSlotAllows(player *ent.EsportsPlayer, now time.Time) bool {
	if player == nil {
		return false
	}
	if !player.FixedTeamEnabled {
		return true
	}
	return withinMinuteWindow(playerMinuteOfDay(now),
		player.FixedTeamSlotStartMinute, player.FixedTeamSlotEndMinute)
}

// orderPerSeatPriceCents 订单每席应付金额（分）：总应付按席位数均摊。
// 这是陪玩侧「可接单价范围」的比较基准（多人单里每个陪玩分到的那一席的价）。
func orderPerSeatPriceCents(order *ent.EsportsOrder) int64 {
	if order == nil {
		return 0
	}
	seats := order.RequiredSeats
	if seats < 1 {
		seats = 1
	}
	return order.PayableCents / int64(seats)
}

// playerAcceptsOrderPrice 可接单价范围（§6.4-06）：0 表示该侧不限；两侧都为 0 即不限价。
func playerAcceptsOrderPrice(player *ent.EsportsPlayer, order *ent.EsportsOrder) bool {
	if player == nil || order == nil {
		return false
	}
	price := orderPerSeatPriceCents(order)
	if player.AcceptPriceMinCents > 0 && price < player.AcceptPriceMinCents {
		return false
	}
	if player.AcceptPriceMaxCents > 0 && price > player.AcceptPriceMaxCents {
		return false
	}
	return true
}

// playerAcceptsDispatchTerms 汇总 §6.4-06 的接单配置判定，返回中文业务错误便于客服定位：
// 服务时段外、可接单价范围外都不允许被派单。
func playerAcceptsDispatchTerms(player *ent.EsportsPlayer, order *ent.EsportsOrder, now time.Time) error {
	if !playerServiceSlotAllows(player, now) {
		return fmt.Errorf("%w：该陪玩当前不在其服务时段内（%02d:%02d-%02d:%02d），不可派单",
			errForbidden, player.ServiceSlotStartMinute/60, player.ServiceSlotStartMinute%60,
			player.ServiceSlotEndMinute/60, player.ServiceSlotEndMinute%60)
	}
	if !playerAcceptsOrderPrice(player, order) {
		return fmt.Errorf("%w：订单每席 %d 分不在该陪玩可接单价范围（%d~%d 分，0=不限）内",
			errForbidden, orderPerSeatPriceCents(order),
			player.AcceptPriceMinCents, player.AcceptPriceMaxCents)
	}
	return nil
}

package esports

import (
	"fmt"

	"go-admin/internal/ent"
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

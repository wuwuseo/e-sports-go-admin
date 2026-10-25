package esports

import (
	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// adminTeammateOption 管理端报单选人用的固排候选：只暴露选择所需的标识与展示名。
type adminTeammateOption struct {
	PlayerID    int    `json:"player_id"`
	DisplayName string `json:"display_name"`
	Game        string `json:"game"`
	Level       string `json:"level"`
	Presence    string `json:"presence"`
}

// adminPlayerTeammatesHandler 主陪玩的已确认固排列表（管理端只读）：
// 报单时选中主陪玩后，可一键把固排成员带入「指定陪玩」。
func adminPlayerTeammatesHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	playerID, err := parsePathID(c, "陪玩")
	if err != nil {
		return businessError(c, err)
	}
	if _, err := global.ORM.EsportsPlayer.Get(c.Context(), playerID); err != nil {
		return businessError(c, err)
	}
	views, err := listConfirmedTeammates(c.Context(), global.ORM, playerID)
	if err != nil {
		return businessError(c, err)
	}
	options := make([]adminTeammateOption, 0, len(views))
	for _, view := range views {
		options = append(options, adminTeammateOption{
			PlayerID:    view.TeammatePlayerID,
			DisplayName: view.TeammateName,
			Game:        view.Game,
			Level:       view.TeammateLevel,
			Presence:    view.TeammatePresence,
		})
	}
	return reply(c, options)
}

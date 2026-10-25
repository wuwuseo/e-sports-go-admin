package esports

import (
	"strings"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/predicate"
	"go-admin/internal/global"
)

// 客服列表（管理端选择器数据源）：客服即登录后台的员工（active 管理员），
// 供负责客服 / 处理客服 / 考官 / 转单目标等场景按姓名选择，只暴露 id 与用户名。
type csStaffItem struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
}

// csStaffListHandler 客服列表：keyword 模糊匹配用户名，limit 默认 50、上限 200。
func csStaffListHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	limit := queryLimit(c, 50)
	predicates := []predicate.Admin{admin.StatusEQ(admin.StatusActive)}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		predicates = append(predicates, admin.UsernameContainsFold(keyword))
	}
	rows, err := global.ORM.Admin.Query().
		Where(predicates...).
		Order(admin.ByID()).
		Limit(limit).
		All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	items := make([]csStaffItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, csStaffItem{ID: row.ID, Username: row.Username})
	}
	return reply(c, items)
}

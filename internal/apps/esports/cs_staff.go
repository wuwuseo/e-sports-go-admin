package esports

import (
	"strings"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/predicate"
	"go-admin/internal/global"
)

// 客服列表（管理端选择器数据源）：客服即登录后台的员工（active 管理员），
// 供负责客服 / 处理客服 / 考官 / 转单目标等场景按客服名选择。
// name 取姓名（name），缺姓名回落昵称（nickname），再回落登录账号（username）。
type csStaffItem struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func csStaffDisplayName(name, nickname, username string) string {
	if v := strings.TrimSpace(name); v != "" {
		return v
	}
	if v := strings.TrimSpace(nickname); v != "" {
		return v
	}
	return username
}

// csStaffListHandler 客服列表：keyword 模糊匹配姓名 / 昵称 / 用户名，limit 默认 50、上限 200。
func csStaffListHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	limit := queryLimit(c, 50)
	predicates := []predicate.Admin{admin.StatusEQ(admin.StatusActive)}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		kw := admin.UsernameContainsFold(keyword)
		predicates = append(predicates, admin.Or(
			kw,
			admin.NameContainsFold(keyword),
			admin.NicknameContainsFold(keyword),
		))
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
		items = append(items, csStaffItem{
			ID:   row.ID,
			Name: csStaffDisplayName(row.Name, row.Nickname, row.Username),
		})
	}
	return reply(c, items)
}

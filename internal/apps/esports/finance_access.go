package esports

import (
	"context"
	"strings"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

// financeReportRoleNamesKey 是允许查看财务资金报表的角色名配置（逗号分隔，如 "finance,财务"）。
// 缺省为空表示仅超级管理员可看，与既有口径一致。
const financeReportRoleNamesKey = "app.esports.finance_report_role_names"

// canAccessFinanceReport 判断当前请求操作人是否有权查看财务资金报表：
// 超级管理员直接放行；否则读取活跃角色名，命中配置角色即放行（实现「授权财务角色可看财报」）。
func canAccessFinanceReport(c fiber.Ctx) bool {
	if super, _ := c.Locals("isSuperAdmin").(bool); super {
		return true
	}
	uid, _ := c.Locals("uid").(string)
	if uid == "" {
		return false
	}
	roleNames := financeReportRoleNames(c.Context(), global.ORM)
	if len(roleNames) == 0 {
		return false
	}
	active, _, resolved := global.FilterAssignedActiveRoleNames(c.Context(), uid, nil)
	if !resolved {
		return false
	}
	for _, name := range active {
		for _, allowed := range roleNames {
			if name == allowed {
				return true
			}
		}
	}
	return false
}

// financeReportRoleNames 读取允许看财报的角色名配置（已去空白与去空项）。
func financeReportRoleNames(ctx context.Context, db *ent.Client) []string {
	return commaSeparatedRoleNames(ctx, db, financeReportRoleNamesKey)
}

// skuCostViewRolesKey 是允许查看 SKU 成本（cost_cents）的角色名配置（，逗号分隔）。
// 缺省为空表示仅超级管理员可见；成本对未授权角色既不可写也不可读。
const skuCostViewRolesKey = "app.esports.sku_cost_view_roles"

// skuCostViewRoleNames 读取允许看 SKU 成本的角色名（已去空白与去空项）。
func skuCostViewRoleNames(ctx context.Context, db *ent.Client) []string {
	return commaSeparatedRoleNames(ctx, db, skuCostViewRolesKey)
}

// canViewSkuCost 判断当前请求操作人是否有权查看 SKU 成本价：
// 超级管理员直接放行；否则命中 sku_cost_view_roles 配置角色才放行。
func canViewSkuCost(c fiber.Ctx) bool {
	if super, _ := c.Locals("isSuperAdmin").(bool); super {
		return true
	}
	uid, _ := c.Locals("uid").(string)
	if uid == "" {
		return false
	}
	roleNames := skuCostViewRoleNames(c.Context(), global.ORM)
	if len(roleNames) == 0 {
		return false
	}
	active, _, resolved := global.FilterAssignedActiveRoleNames(c.Context(), uid, nil)
	if !resolved {
		return false
	}
	for _, name := range active {
		for _, allowed := range roleNames {
			if name == allowed {
				return true
			}
		}
	}
	return false
}

// commaSeparatedRoleNames 读取逗号分隔的角色名配置，去空白与空项。
func commaSeparatedRoleNames(ctx context.Context, db *ent.Client, key string) []string {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if err != nil {
		return nil
	}
	raw := strings.TrimSpace(item.Value)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

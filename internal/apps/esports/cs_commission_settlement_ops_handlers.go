package esports

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 客服提成结算单的作废与重开（P0-6）：与生成、锁定、登记发放挂在同一个「结算」菜单下，
// 沿用既有资金权限口径（AuthAdmin + PermAPI）；「作废人 ≠ 登记人」由服务层统一校验。

// adminSettlementsFiltered 结算单列表（带状态过滤，P0-8）：默认视图不含已作废单；
// status=void 只看作废单（审计 / 重开追溯），status=draft|locked|paid 看指定状态，status=all 看全部。
func adminSettlementsFiltered(c fiber.Ctx) error {
	status := strings.TrimSpace(c.Query("status"))
	if status == "" {
		// 默认视图沿用既有实现：其内部 listSettlements 默认已排除作废单。
		return adminSettlements(c)
	}
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listSettlements(c.Context(), global.ORM, queryInt(c, "player_id"), queryLimit(c, 100), status)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// voidCsSettlementHandler 作废客服提成结算单：明细安全回退到待结算，原单转 void 后不可再发放。
func voidCsSettlementHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结算单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err = c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := voidCsCommissionSettlement(c.Context(), global.ORM, id, adminID, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// reopenCsSettlementHandler 重开客服提成结算单：按同一客服与同一周期重新生成草稿单，
// 仍须锁定并登记发放（发放人不得等于登记人），因此不会重复发钱。
func reopenCsSettlementHandler(c fiber.Ctx) error {
	operatorID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		AdminID     int    `json:"admin_id"`
		PeriodStart string `json:"period_start"`
		PeriodEnd   string `json:"period_end"`
		Reason      string `json:"reason"`
	}
	if err = c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	start, err := time.Parse(time.RFC3339, in.PeriodStart)
	if err != nil {
		return fail(c, fiber.StatusBadRequest, "周期起始时间格式无效")
	}
	end, err := time.Parse(time.RFC3339, in.PeriodEnd)
	if err != nil {
		return fail(c, fiber.StatusBadRequest, "周期结束时间格式无效")
	}
	item, err := reopenCsCommissionSettlement(c.Context(), global.ORM, in.AdminID, start, end,
		operatorID, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

package esports

import (
	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 结单审批流（§3.4）：节点串行编排、审批角色与超时策略，挂在「结单表单」菜单下。

func listCloseApprovalFlowsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listCloseApprovalFlows(c.Context(), global.ORM, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createCloseApprovalFlowHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in closeApprovalFlowInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createCloseApprovalFlow(c.Context(), global.ORM, in, adminID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func updateCloseApprovalFlowHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单审批流")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		ExpectedVersion int `json:"expected_version"`
		closeApprovalFlowInput
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateCloseApprovalFlow(c.Context(), global.ORM, id, in.ExpectedVersion, in.closeApprovalFlowInput)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func activateCloseApprovalFlowHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单审批流")
	if err != nil {
		return businessError(c, err)
	}
	item, err := activateCloseApprovalFlow(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func rollbackCloseApprovalFlowHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单审批流")
	if err != nil {
		return businessError(c, err)
	}
	item, err := rollbackCloseApprovalFlow(c.Context(), global.ORM, id, adminID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// orderCloseApprovalHandler 返回订单的结单审批进度与当前待审批节点（供复核弹窗说明）。
// 读取边界在 handler 内再校验一次：仅订单负责客服、被授权协作客服或超管可读，避免绕过路由包装读取他人订单。
func orderCloseApprovalHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	if _, err = loadOwnedOrder(c.Context(), global.ORM, operator.ID, isSuper, id); err != nil {
		return businessError(c, err)
	}
	view, err := orderCloseApproval(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

func closeOrderHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in closeOrderInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := closeOrder(c.Context(), global.ORM, id, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// approveCloseReviewHandler 复核通过结单（高额单管理员复核）。
func approveCloseReviewHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Note string `json:"note"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	order, err := approveCloseReview(c.Context(), global.ORM, id, adminID, isSuper, in.Note, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// rejectCloseReviewHandler 复核驳回结单（，回退到待结单）。
func rejectCloseReviewHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	order, err := rejectCloseReview(c.Context(), global.ORM, id, adminID, isSuper, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

func adminAfterSales(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	items, err := listAfterSales(c.Context(), global.ORM, adminID, isSuper, c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	// ：附带当前阶段时限与是否超时，前端据此高亮；
	// ：同时带出「等待财务执行外部退款的动作项数」与「关联外部退款是否已超期」。
	views, err := afterSaleAdminViewsOf(c.Context(), global.ORM, items, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, views)
}

func adminAfterSaleDetail(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	item, err := getAfterSale(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	// 限时信息 + 外部执行信号：与列表复用同一聚合函数，避免两套口径。
	view, err := afterSaleAdminViewOf(c.Context(), global.ORM, item, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

func createAfterSaleHandler(c fiber.Ctx) error {
	admin, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in adminCreateAfterSaleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	// 代建入口按来源类型解析真实归属：order 校验负责客服，gift/stored_value_card/recharge 校验客户归属客服。
	if err := loadOwnedAfterSaleSourceByRef(c.Context(), global.ORM, admin.ID, isSuper, in.SourceType, in.SourceID); err != nil {
		return businessError(c, err)
	}
	// ：客服代建售后支持引用本人上传的凭证附件（evidence_attachment_ids）。
	item, err := createAfterSaleWithEvidence(c.Context(), global.ORM, in.afterSaleInput, in.EvidenceAttachmentIDs, admin.ID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// userConfirmAfterSaleHandler 客户自助确认方案。
func userConfirmAfterSaleHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "售后单 ID 无效")
	}
	item, err := confirmAfterSaleByCustomer(c.Context(), global.ORM, id, customer.ID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// userDeclineAfterSaleHandler 客户自助拒绝方案：须填原因，退回处理中。
func userDeclineAfterSaleHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "售后单 ID 无效")
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := declineAfterSaleByCustomer(c.Context(), global.ORM, id, customer.ID, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func acceptAfterSaleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	item, err := acceptAfterSale(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func planAfterSaleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	var in afterSalePlanInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := submitAfterSalePlan(c.Context(), global.ORM, id, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// afterSaleRefundReviewerOf 从登录态解析当前操作人的资金复核资格：
// 超级管理员直接具备；否则读取活跃角色名，命中「退款财务复核角色」配置即具备。
// uid 缺失或角色未解析时返回零值 = 非财务角色，由服务层按门槛明确拒绝，不静默放行。
func afterSaleRefundReviewerOf(c fiber.Ctx) afterSaleRefundReviewer {
	reviewer := afterSaleRefundReviewer{}
	if super, _ := c.Locals("isSuperAdmin").(bool); super {
		reviewer.IsSuper = true
	}
	uid, _ := c.Locals("uid").(string)
	if uid == "" {
		return reviewer
	}
	if names, _, resolved := global.FilterAssignedActiveRoleNames(c.Context(), uid, nil); resolved {
		reviewer.RoleNames = names
	}
	return reviewer
}

func approveAfterSaleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	item, err := approveAfterSaleWithReviewer(c.Context(), global.ORM, id, adminID,
		afterSaleRefundReviewerOf(c), time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func rejectAfterSaleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	var in afterSaleDecisionInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := rejectAfterSale(c.Context(), global.ORM, id, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func executeAfterSaleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	item, err := executeAfterSaleWithReviewer(c.Context(), global.ORM, id, adminID,
		afterSaleRefundReviewerOf(c), time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func withdrawAfterSaleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	item, err := withdrawAfterSale(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// listFulfillmentMakeupsHandler 列出补履约任务。
func listFulfillmentMakeupsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listFulfillmentMakeups(c.Context(), global.ORM, c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	views, err := fulfillmentMakeupViewsOf(c.Context(), global.ORM, items)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, views)
}

// startFulfillmentMakeupHandler 开始补履约服务（pending → servicing）。
func startFulfillmentMakeupHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "补履约任务")
	if err != nil {
		return businessError(c, err)
	}
	task, err := startFulfillmentMakeup(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, task)
}

// completeFulfillmentMakeupHandler 结束补履约服务并由客服验收（servicing → completed）。
func completeFulfillmentMakeupHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "补履约任务")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Note string `json:"note"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	task, err := completeFulfillmentMakeup(c.Context(), global.ORM, id, adminID, in.Note, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, task)
}

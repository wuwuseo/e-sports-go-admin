package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 客户侧售后进度与补证提示接口。
//
// 建议接线（主控统一加）：
//   - GET  /user/after-sales/:id/progress          AuthUser（用户端无需 PermAPI）
//   - POST /admin/after-sales/:id/evidence-request AuthAdmin，需 PermAPI
//   - GET  /user/after-sales/:id/evidence-status   AuthUser（用户端无需 PermAPI）

// userAfterSaleProgressHandler 客户自助查询售后单逐项进度：
// 返回每个动作项的序号 / 类型 / 状态 / 结果摘要，以及「已到账」与「待执行」金额的区分。
func userAfterSaleProgressHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	progress, err := afterSaleCustomerProgressOf(c.Context(), global.ORM, customer.ID, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, progress)
}

// adminRequestAfterSaleEvidenceHandler 客服发起「提示补证」。
func adminRequestAfterSaleEvidenceHandler(c fiber.Ctx) error {
	admin, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	var in afterSaleEvidenceRequestInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	if _, err = loadOwnedAfterSale(c.Context(), global.ORM, admin.ID, isSuper, id); err != nil {
		return businessError(c, err)
	}
	message, err := requestAfterSaleEvidence(c.Context(), global.ORM, id, admin.ID,
		afterSaleAdminDisplayName(admin), in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, message)
}

// userAfterSaleEvidenceStatusHandler 客户自助查询「是否待补证」。
func userAfterSaleEvidenceStatusHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	status, err := afterSaleEvidenceStatusOf(c.Context(), global.ORM, customer.ID, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, status)
}

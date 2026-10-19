package esports

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/global"
)

// 售后单内沟通记录接口（§13.3-04）。
//
// 建议接线（主控统一加）：
//   - GET  /admin/aftersales/:id/messages  AuthAdmin，需 PermAPI
//   - POST /admin/aftersales/:id/messages  AuthAdmin，需 PermAPI
//   - GET  /user/aftersales/:id/messages   AuthUser（用户端无需 PermAPI）
//   - POST /user/aftersales/:id/messages   AuthUser（用户端无需 PermAPI）

// adminAfterSaleMessagesHandler 客服侧查看沟通记录（含内部备注），先校验售后单数据域归属。
func adminAfterSaleMessagesHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	if _, err = loadOwnedAfterSale(c.Context(), global.ORM, adminID, isSuper, id); err != nil {
		return businessError(c, err)
	}
	items, err := listAfterSaleMessages(c.Context(), global.ORM, id, true, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// adminPostAfterSaleMessageHandler 客服侧回复（visibility=internal 为内部备注，客户不可见）。
func adminPostAfterSaleMessageHandler(c fiber.Ctx) error {
	admin, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	var in afterSaleMessageInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	item, err := loadOwnedAfterSale(c.Context(), global.ORM, admin.ID, isSuper, id)
	if err != nil {
		return businessError(c, err)
	}
	message, err := postAdminAfterSaleMessage(c.Context(), global.ORM, item, admin.ID,
		afterSaleAdminDisplayName(admin), in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, message)
}

// userAfterSaleMessagesHandler 客户侧查看沟通记录：只返回客户可见消息（internal 备注不可见）。
func userAfterSaleMessagesHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	item, err := global.ORM.EsportsAfterSale.Get(c.Context(), id)
	if err != nil {
		return businessError(c, err)
	}
	if item.CustomerID == nil || *item.CustomerID != customer.ID {
		return businessError(c, errForbidden)
	}
	items, err := listAfterSaleMessages(c.Context(), global.ORM, id, false, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// userPostAfterSaleMessageHandler 客户侧回复售后单（可带附件引用）；已结束单据明确拒绝。
func userPostAfterSaleMessageHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	var in afterSaleMessageInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	message, err := postCustomerAfterSaleMessage(c.Context(), global.ORM, customer.ID, id, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, message)
}

// afterSaleAdminDisplayName 客服显示名快照：优先昵称，回落账号名。
func afterSaleAdminDisplayName(admin *ent.Admin) string {
	if admin == nil {
		return ""
	}
	if nickname := strings.TrimSpace(admin.Nickname); nickname != "" {
		return nickname
	}
	return strings.TrimSpace(admin.Username)
}

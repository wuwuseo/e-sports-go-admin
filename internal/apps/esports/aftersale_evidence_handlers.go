package esports

import (
	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// userAfterSaleEvidenceHandler 客户自助读取售后凭证附件清单（只读确认）。
// 建议接线：GET /user/after-sales/:id/evidence（前台用户态 Auth），供主代理接入路由。
func userAfterSaleEvidenceHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	attachments, err := listCustomerAfterSaleEvidence(c.Context(), global.ORM, customer.ID, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, attachments)
}

package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// ownedAfterSaleReviewHandler 复核路由的可见范围校验：由复核申请反查售后单再判归属。
func ownedAfterSaleReviewHandler(next fiber.Handler) fiber.Handler {
	return func(c fiber.Ctx) error {
		adminID, err := requireAdmin(c)
		if err != nil {
			return businessError(c, err)
		}
		id, err := parsePathID(c, "复核申请")
		if err != nil {
			return businessError(c, err)
		}
		review, err := global.ORM.EsportsAfterSaleReview.Get(c.Context(), id)
		if err != nil {
			return businessError(c, err)
		}
		isSuper, _ := c.Locals("isSuperAdmin").(bool)
		if _, err = loadOwnedAfterSale(c.Context(), global.ORM, adminID, isSuper, review.AfterSaleID); err != nil {
			return businessError(c, err)
		}
		return next(c)
	}
}

// requestAfterSaleReviewHandler 提交终态售后的新证据复核申请（§13.6）。
func requestAfterSaleReviewHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	var in afterSaleReviewInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	review, err := requestAfterSaleReview(c.Context(), global.ORM, id, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, review)
}

// listAfterSaleReviewsHandler 查看某售后单的复核历史（§13.6）。
func listAfterSaleReviewsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	reviews, err := listAfterSaleReviews(c.Context(), global.ORM, id, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, reviews)
}

// decideAfterSaleReviewHandler 异人审查复核申请（§13.6）：维持结论或发回重新处理。
func decideAfterSaleReviewHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "复核申请")
	if err != nil {
		return businessError(c, err)
	}
	var in afterSaleReviewDecisionInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	review, err := decideAfterSaleReview(c.Context(), global.ORM, id, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, review)
}

// withdrawAfterSaleReviewHandler 申请人撤回待审查的复核申请（§13.6）。
func withdrawAfterSaleReviewHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "复核申请")
	if err != nil {
		return businessError(c, err)
	}
	review, err := withdrawAfterSaleReview(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, review)
}

package esports

import (
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

// approveCloseReviewHandler 复核通过结单（§3.4 高额单管理员复核）。
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
	order, err := approveCloseReview(c.Context(), global.ORM, id, adminID, in.Note, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// rejectCloseReviewHandler 复核驳回结单（§3.4，回退到待结单）。
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
	order, err := rejectCloseReview(c.Context(), global.ORM, id, adminID, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

func adminAfterSales(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listAfterSales(c.Context(), global.ORM, c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
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
	return reply(c, item)
}

func createAfterSaleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in afterSaleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createAfterSale(c.Context(), global.ORM, in, adminID, time.Now())
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

func approveAfterSaleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	item, err := approveAfterSale(c.Context(), global.ORM, id, adminID, time.Now())
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
	item, err := executeAfterSale(c.Context(), global.ORM, id, adminID, time.Now())
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

// listFulfillmentMakeupsHandler 列出补履约任务（§13.6）。
func listFulfillmentMakeupsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listFulfillmentMakeups(c.Context(), global.ORM, c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
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

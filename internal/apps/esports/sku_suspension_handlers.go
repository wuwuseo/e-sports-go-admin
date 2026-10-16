package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// §15.7 停服申请 / 异人审批 / 受影响订单逐单处置的路由处理函数。
// 路由由 app.go 统一接线（本文件只提供 handler）：
//
//	POST /admin/skus/:id/suspensions                     发起停服申请
//	GET  /admin/sku-suspensions                          停服申请列表（可按 status 过滤）
//	GET  /admin/sku-suspensions/:id                      申请详情 + 逐单处置清单与进度
//	POST /admin/sku-suspensions/:id/decision             异人审批（approve / reject）
//	POST /admin/sku-suspensions/:id/cancel               撤回待审批的申请
//	POST /admin/sku-suspensions/:id/orders/:orderId/disposal  逐单处置
//
// 既有 SKU 直接上下架接口（PUT /admin/skus/:id 改 status）保持不变。

// requestSkuSuspensionHandler 发起 SKU 停服申请（§15.7）。
func requestSkuSuspensionHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	skuID, err := parsePathID(c, "商品")
	if err != nil {
		return businessError(c, err)
	}
	var in skuSuspensionRequestInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := requestSkuSuspension(c.Context(), global.ORM, skuID, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// adminSkuSuspensions 列出停服申请（§15.7）：非超级管理员只看自己发起的申请。
func adminSkuSuspensions(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	items, err := listSkuSuspensions(c.Context(), global.ORM, adminID, isSuper, c.Query("status"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// skuSuspensionDetailHandler 申请详情与逐单处置清单（含 total / pending / done / failed 进度）。
func skuSuspensionDetailHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "停服申请")
	if err != nil {
		return businessError(c, err)
	}
	report, err := loadSkuSuspensionReport(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, report)
}

// reviewSkuSuspensionHandler 异人审批（§15.7）：申请人不得自审，自审返回 403。
func reviewSkuSuspensionHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "停服申请")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Approve *bool  `json:"approve"`
		Note    string `json:"note"`
	}
	if err := c.Bind().Body(&in); err != nil || in.Approve == nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := reviewSkuSuspension(c.Context(), global.ORM, id, adminID,
		skuSuspensionReviewInput{Approve: *in.Approve, Note: in.Note}, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// cancelSkuSuspensionHandler 撤回尚未审批的停服申请：申请人本人或超级管理员可撤回。
func cancelSkuSuspensionHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "停服申请")
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	item, err := cancelSkuSuspension(c.Context(), global.ORM, id, adminID, isSuper, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// disposeSkuSuspensionItemHandler 逐单处置（§15.7）：取消并退款 / 改期 / 保留继续服务。
// 处置失败（例如已开工订单不可直接取消）也会落 failed 记录，客户端可在详情里看到阻塞原因。
func disposeSkuSuspensionItemHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	suspensionID, err := parsePathID(c, "停服申请")
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := strconv.Atoi(c.Params("orderId"))
	if err != nil || orderID < 1 {
		return fail(c, fiber.StatusBadRequest, "订单 ID 无效")
	}
	var in skuSuspensionDisposalInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	item, err := disposeSkuSuspensionItem(c.Context(), global.ORM, suspensionID, orderID, adminID, isSuper, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

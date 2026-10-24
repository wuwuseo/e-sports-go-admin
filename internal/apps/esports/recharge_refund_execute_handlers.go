package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 退款/退卡按充值批次还原的外部退款执行（人工核验分支）的 HTTP 入口。
//
//	POST /admin/after-sales/:id/refund-execute             按充值批次生成外部退款执行明细
//	GET  /admin/recharge-refund-executions                 批次列表（?status= pending / executed）
//	POST /admin/recharge-refund-executions/:id/execute     财务登记外部打款后置 executed

// adminRechargeRefundExecuteHandler 按充值批次为售后单生成外部退款执行明细：
// 复用 rechargeRefundQuoteFor 试算，只对可退且无异常、拆分已知的批次落明细，幂等键按
// 「售后单 + 批次来源」拆分，重复执行不重复落账。
// 建议接线：POST /admin/after-sales/:id/refund-execute（ownedAfterSaleHandler 包裹）。
func adminRechargeRefundExecuteHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	afterSaleID, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	items, err := executeRechargeRefundByBatch(c.Context(), global.ORM, afterSaleID, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// markRechargeRefundExecutionExecutedHandler 财务登记外部打款后置执行明细为 executed。
// 建议接线：POST /admin/recharge-refund-executions/:id/execute。
func markRechargeRefundExecutionExecutedHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "执行明细 ID 无效")
	}
	var in struct {
		FinanceRef string `json:"finance_ref"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := markRechargeRefundExecutionExecuted(c.Context(), global.ORM, id, adminID, in.FinanceRef, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// listRechargeRefundExecutionsHandler 退款执行批次列表（只读，支持 ?status=&limit=）：
// 支持 status=pending / executed 过滤，limit 缺省 100（夹在 1~200）。
// 用例：财务列表页按状态核对「还有多少批次待打款」，并看到每批关联的售后单号与执行人。
func listRechargeRefundExecutionsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listRechargeRefundExecutions(c.Context(), global.ORM, c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/global"
)

// adminCustomerProfile 客户档案汇总：基本信息 + 消费 / 充值 / 送礼 / 余额 / 成长值。
func adminCustomerProfile(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	customerID, err := strconv.Atoi(c.Params("id"))
	if err != nil || customerID < 1 {
		return fail(c, fiber.StatusBadRequest, "客户 ID 无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	view, err := customerProfileOverview(c.Context(), global.ORM, operator.ID, isSuper, customerID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// updateCustomerProfileHandler 更新客户档案扩展字段（手机号 / 消费偏好 / 标签 / 跟进状态）。
func updateCustomerProfileHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	customerID, err := strconv.Atoi(c.Params("id"))
	if err != nil || customerID < 1 {
		return fail(c, fiber.StatusBadRequest, "客户 ID 无效")
	}
	var in customerProfileInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	customer, err := updateCustomerProfile(c.Context(), global.ORM, operator.ID, isSuper, customerID, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, customer)
}

// adminCustomerFollowUps 客户跟进记录时间线。
func adminCustomerFollowUps(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	customerID, err := strconv.Atoi(c.Params("id"))
	if err != nil || customerID < 1 {
		return fail(c, fiber.StatusBadRequest, "客户 ID 无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	items, err := listCustomerFollowUps(c.Context(), global.ORM, operator.ID, isSuper, customerID, queryInt(c, "limit"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// createCustomerFollowUpHandler 新增跟进记录（写记录并刷新最近联系时间）。
func createCustomerFollowUpHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	customerID, err := strconv.Atoi(c.Params("id"))
	if err != nil || customerID < 1 {
		return fail(c, fiber.StatusBadRequest, "客户 ID 无效")
	}
	var in followUpInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	record, err := addCustomerFollowUp(c.Context(), global.ORM, operator.ID, isSuper, customerID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, record)
}

// transferCustomerOwnerHandler 客户归属转移。
// 需在 app.go 接线 + PermAPI：
//
//	POST /admin/customers/:id/owner-transfer → transferCustomerOwnerHandler
//	PermAPI: {Type: cmfapp.PermAPI, Name: "转移客户归属", Code: "esports:customers:transfer_owner",
//	          Path: "/api/v1/esports/admin/customers/{id}/owner-transfer", Method: "POST", Parent: "esports:cs:menu"}
func transferCustomerOwnerHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	customerID, err := strconv.Atoi(c.Params("id"))
	if err != nil || customerID < 1 {
		return fail(c, fiber.StatusBadRequest, "客户 ID 无效")
	}
	var in struct {
		TargetAdminID int    `json:"target_admin_id"`
		Reason        string `json:"reason"`
	}
	if err = c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	customer, err := transferCustomerOwner(c.Context(), global.ORM, operator.ID, isSuper,
		customerID, in.TargetAdminID, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, customer)
}

// wecomIntakeHandler 企微侧边栏自动建档：企业主体 + 外部联系人标识幂等。
func wecomIntakeHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in wecomIntakeInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	customer, created, err := wecomUpsertCustomer(c.Context(), global.ORM, operator.ID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"customer": customer, "created": created})
}

// adminScripts 话术库列表，可按分类过滤。
func adminScripts(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listScripts(c.Context(), global.ORM, c.Query("category"), queryInt(c, "limit"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createScriptHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in scriptInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createScript(c.Context(), global.ORM, operator.ID, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func updateScriptHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "话术 ID 无效")
	}
	var in scriptInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateScript(c.Context(), global.ORM, id, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func setScriptStatusHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "话术 ID 无效")
	}
	var in struct {
		Status          string `json:"status"`
		ExpectedVersion int    `json:"expected_version"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := setScriptStatus(c.Context(), global.ORM, id, in.ExpectedVersion, in.Status)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// adminResolvePriorityBookingHandler 客服处理会员优先预约意向（P1-11）：
// pending → processed（已处理）/ cancelled（已取消），处理后工作台「会员优先预约」待办随之下降。
//
// 需在 app.go 接线 + PermAPI（本文件不持有路由注册权）：
//
//	POST /admin/priority-bookings/:id/process → adminResolvePriorityBookingHandler
//	PermAPI: {Type: cmfapp.PermAPI, Name: "处理会员优先预约", Code: "esports:members:priority_booking_process",
//	          Path: "/api/v1/esports/admin/priority-bookings/{id}/process", Method: "POST", Parent: "esports:member:menu"}
func adminResolvePriorityBookingHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	intentID, err := strconv.Atoi(c.Params("id"))
	if err != nil || intentID < 1 {
		return fail(c, fiber.StatusBadRequest, "预约意向 ID 无效")
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	item, err := resolvePriorityBookingIntent(c.Context(), global.ORM, operator.ID, isSuper, intentID, in.Status, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	// 本路由未套 syncedRoutes 的广播包装（路由契约测试要求 handler 指向原始函数），成功后自行广播。
	notifyDispatchChanged(map[string]any{"method": c.Method(), "path": c.Route().Path})
	return reply(c, item)
}

// csWorkbenchHandler 客服工作台聚合：我的客户数、我的订单池（含公共池）、待办提醒、本人业绩。
// 三个客服入口（PC 工作台 / 移动客服端 / 企微侧边栏）共用本接口。
func csWorkbenchHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	filter, err := parseReportFilter(c)
	if err != nil {
		return businessError(c, err)
	}
	// 非超管强制收敛到本人负责的订单，避免通过参数越权查看他人业绩。
	if !isSuper {
		filter.ResponsibleAdmin = operator.ID
	}
	board, err := loadDispatchBoard(c.Context(), global.ORM, operator.ID, isSuper)
	if err != nil {
		return businessError(c, err)
	}
	todos, err := csWorkbenchTodos(c.Context(), global.ORM, operator.ID, isSuper, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	performance, err := csPerformanceReport(c.Context(), global.ORM, filter)
	if err != nil {
		return businessError(c, err)
	}
	customers := global.ORM.EsportsCustomer.Query()
	if !isSuper {
		customers.Where(esportscustomer.OwnerAdminIDEQ(operator.ID))
	}
	myCustomers, err := customers.Count(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{
		"my_customers": myCustomers,
		"order_pool":   board,
		"todos":        todos,
		"performance":  performance,
		"range":        map[string]any{"start": filter.Start, "end": filter.End, "period": filter.Period},
	})
}

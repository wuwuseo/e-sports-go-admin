package esports

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsordercollaborator"
	"go-admin/internal/ent/esportsproductcategory"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/global"
)

// 管理端列表分页口径：默认 100 条（保持旧接口的截断行为），单页上限 500，
// 避免客户端一次拉全量拖垮数据库；超上限一律截断而不是报错。
const (
	adminListDefaultLimit = 100
	adminListMaxLimit     = 500
)

// adminListWindow 是列表接口的取数窗口。paged=false 时沿用旧形态（直接返回 JSON 数组）。
type adminListWindow struct {
	limit    int
	offset   int
	paged    bool
	page     int
	pageSize int
}

// parseAdminListWindow 解析 limit / offset / page / page_size。
// 带 page 或 page_size 时按页码形态返回对象；两者都不带时保持数组形态（向后兼容）。
func parseAdminListWindow(c fiber.Ctx) (adminListWindow, error) {
	window := adminListWindow{limit: adminListDefaultLimit}
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return window, errInvalidInput
		}
		if n > adminListMaxLimit {
			n = adminListMaxLimit
		}
		window.limit = n
	}
	if raw := strings.TrimSpace(c.Query("offset")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return window, errInvalidInput
		}
		window.offset = n
	}
	pageRaw := strings.TrimSpace(c.Query("page"))
	sizeRaw := strings.TrimSpace(c.Query("page_size"))
	if pageRaw == "" && sizeRaw == "" {
		return window, nil
	}
	window.paged = true
	window.page = 1
	if pageRaw != "" {
		n, err := strconv.Atoi(pageRaw)
		if err != nil || n < 1 {
			return window, errInvalidInput
		}
		window.page = n
	}
	window.pageSize = window.limit
	if sizeRaw != "" {
		n, err := strconv.Atoi(sizeRaw)
		if err != nil || n < 1 {
			return window, errInvalidInput
		}
		if n > adminListMaxLimit {
			n = adminListMaxLimit
		}
		window.pageSize = n
	}
	window.limit = window.pageSize
	// 页码过大时直接拒绝，避免 offset 乘法溢出。
	if window.page > (1<<31-1)/window.pageSize {
		return window, errInvalidInput
	}
	window.offset = (window.page - 1) * window.pageSize
	return window, nil
}

// writeAdminList 统一输出：两种形态都写 X-Total-Count（符合筛选条件的总数，不受 limit/offset 影响）。
func writeAdminList(c fiber.Ctx, window adminListWindow, total int, items any) error {
	c.Set("X-Total-Count", strconv.FormatInt(int64(total), 10))
	if window.paged {
		return reply(c, fiber.Map{
			"items":     items,
			"total":     total,
			"page":      window.page,
			"page_size": window.pageSize,
		})
	}
	return reply(c, items)
}

var adminOrderFulfillmentStatuses = map[string]struct{}{
	string(esportsorder.FulfillmentStatusPendingPayment):     {},
	string(esportsorder.FulfillmentStatusPendingDispatch):    {},
	string(esportsorder.FulfillmentStatusPendingAcceptance):  {},
	string(esportsorder.FulfillmentStatusGrabbing):           {},
	string(esportsorder.FulfillmentStatusAccepted):           {},
	string(esportsorder.FulfillmentStatusInService):          {},
	string(esportsorder.FulfillmentStatusPendingClose):       {},
	string(esportsorder.FulfillmentStatusPendingCloseReview): {},
	string(esportsorder.FulfillmentStatusCompleted):          {},
	string(esportsorder.FulfillmentStatusCancelled):          {},
}

var adminOrderPaymentStatuses = map[string]struct{}{
	string(esportsorder.PaymentStatusUnpaid):    {},
	string(esportsorder.PaymentStatusPartial):   {},
	string(esportsorder.PaymentStatusPaid):      {},
	string(esportsorder.PaymentStatusException): {},
}

var adminCustomerFollowStatuses = map[string]struct{}{
	string(esportscustomer.FollowStatusNew):       {},
	string(esportscustomer.FollowStatusFollowing): {},
	string(esportscustomer.FollowStatusPending):   {},
	string(esportscustomer.FollowStatusDormant):   {},
	string(esportscustomer.FollowStatusClosed):    {},
}

var adminCustomerRiskStatuses = map[string]struct{}{
	string(esportscustomer.RiskStatusNormal):     {},
	string(esportscustomer.RiskStatusWatch):      {},
	string(esportscustomer.RiskStatusRestricted): {},
}

// adminOrderFilter 是订单列表的筛选条件（customer_id 精确、order_no 模糊、两个状态精确匹配）。
type adminOrderFilter struct {
	customerID        int
	keyword           string
	fulfillmentStatus string
	paymentStatus     string
}

func parseAdminOrderFilter(c fiber.Ctx) (adminOrderFilter, error) {
	var filter adminOrderFilter
	if raw := strings.TrimSpace(c.Query("customer_id")); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil || id < 1 {
			return filter, errInvalidInput
		}
		filter.customerID = id
	}
	filter.keyword = strings.TrimSpace(c.Query("keyword"))
	if len([]rune(filter.keyword)) > 64 {
		return filter, errInvalidInput
	}
	if raw := strings.TrimSpace(c.Query("fulfillment_status")); raw != "" {
		if _, ok := adminOrderFulfillmentStatuses[raw]; !ok {
			return filter, errInvalidInput
		}
		filter.fulfillmentStatus = raw
	}
	if raw := strings.TrimSpace(c.Query("payment_status")); raw != "" {
		if _, ok := adminOrderPaymentStatuses[raw]; !ok {
			return filter, errInvalidInput
		}
		filter.paymentStatus = raw
	}
	return filter, nil
}

func (filter adminOrderFilter) apply(q *ent.EsportsOrderQuery) *ent.EsportsOrderQuery {
	if filter.customerID > 0 {
		q.Where(esportsorder.CustomerIDEQ(filter.customerID))
	}
	if filter.keyword != "" {
		q.Where(esportsorder.OrderNoContainsFold(filter.keyword))
	}
	if filter.fulfillmentStatus != "" {
		q.Where(esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatus(filter.fulfillmentStatus)))
	}
	if filter.paymentStatus != "" {
		q.Where(esportsorder.PaymentStatusEQ(esportsorder.PaymentStatus(filter.paymentStatus)))
	}
	return q
}

// adminOrderScope 是订单的行级边界：非超管可见「本人负责」或「被授权协作」的订单。
func adminOrderScope(operatorID int, isSuper bool) *ent.EsportsOrderQuery {
	q := global.ORM.EsportsOrder.Query()
	if !isSuper {
		q.Where(esportsorder.Or(
			esportsorder.ResponsibleAdminIDEQ(operatorID),
			esportsorder.HasCollaboratorsWith(esportsordercollaborator.AdminIDEQ(operatorID)),
		))
	}
	return q
}

// adminCustomerFilter 是客户列表的筛选条件（display_name / phone 模糊、跟进状态与风险状态精确匹配）。
type adminCustomerFilter struct {
	keyword      string
	followStatus string
	riskStatus   string
}

func parseAdminCustomerFilter(c fiber.Ctx) (adminCustomerFilter, error) {
	var filter adminCustomerFilter
	filter.keyword = strings.TrimSpace(c.Query("keyword"))
	if len([]rune(filter.keyword)) > 64 {
		return filter, errInvalidInput
	}
	if raw := strings.TrimSpace(c.Query("follow_status")); raw != "" {
		if _, ok := adminCustomerFollowStatuses[raw]; !ok {
			return filter, errInvalidInput
		}
		filter.followStatus = raw
	}
	if raw := strings.TrimSpace(c.Query("risk_status")); raw != "" {
		if _, ok := adminCustomerRiskStatuses[raw]; !ok {
			return filter, errInvalidInput
		}
		filter.riskStatus = raw
	}
	return filter, nil
}

func (filter adminCustomerFilter) apply(q *ent.EsportsCustomerQuery) *ent.EsportsCustomerQuery {
	if filter.keyword != "" {
		q.Where(esportscustomer.Or(
			esportscustomer.DisplayNameContainsFold(filter.keyword),
			esportscustomer.PhoneContainsFold(filter.keyword),
		))
	}
	if filter.followStatus != "" {
		q.Where(esportscustomer.FollowStatusEQ(esportscustomer.FollowStatus(filter.followStatus)))
	}
	if filter.riskStatus != "" {
		q.Where(esportscustomer.RiskStatusEQ(esportscustomer.RiskStatus(filter.riskStatus)))
	}
	return q
}

func reply(c fiber.Ctx, value any) error {
	return c.JSON(fiber.Map{"code": 1, "message": "成功", "data": value})
}

func fail(c fiber.Ctx, status int, message string) error {
	return c.Status(status).JSON(fiber.Map{"code": status, "message": message})
}

func businessError(c fiber.Ctx, err error) error {
	switch {
	// §4.6 报单校验：重复报单提醒 / 客户欠款拦截 / 预约时段资源冲突都给前端明确中文提示与 409。
	// 这三类需排在 errInvalidInput 之前：预约资源冲突错误同时包裹了 errInvalidInput（容量判定口径）。
	case errors.Is(err, errDuplicateOrder):
		return fail(c, fiber.StatusConflict, err.Error())
	case errors.Is(err, errCustomerDebt):
		return fail(c, fiber.StatusConflict, err.Error())
	case errors.Is(err, errAppointmentResource):
		return fail(c, fiber.StatusConflict, err.Error())
	// 用 err.Error() 返回：裸 errInvalidInput 文案仍为「请求参数无效」，
	// 携带上下文的中文包装错误（如结单条件缺失）则原样透出，便于前端提示。
	case errors.Is(err, errInvalidInput):
		return fail(c, fiber.StatusBadRequest, err.Error())
	case ent.IsValidationError(err):
		return fail(c, fiber.StatusBadRequest, "请求参数无效")
	case errors.Is(err, errForbidden):
		return fail(c, fiber.StatusForbidden, "无权访问该客户或订单")
	case errors.Is(err, errDispatchPriorityHold):
		// 优先通知窗口内订单尚未对大厅开放，给陪玩明确原因而不是笼统的 403 文案。
		return fail(c, fiber.StatusForbidden, errDispatchPriorityHold.Error())
	case errors.Is(err, errAgeUnverified):
		return fail(c, fiber.StatusForbidden, errAgeUnverified.Error())
	case errors.Is(err, errInsufficientBalance):
		return fail(c, fiber.StatusBadRequest, errInsufficientBalance.Error())
	case errors.Is(err, errNotConfigured):
		return fail(c, fiber.StatusServiceUnavailable, err.Error())
	case errors.Is(err, errOrderFrozen):
		// 存单冻结拦截：给前端明确中文提示（而非笼统的「业务状态已变化」），状态码 409。
		return fail(c, fiber.StatusConflict, errOrderFrozen.Error())
	case errors.Is(err, errConflict), ent.IsConstraintError(err):
		return fail(c, fiber.StatusConflict, errConflict.Error())
	case ent.IsNotFound(err):
		return fail(c, fiber.StatusNotFound, "记录不存在")
	default:
		return fail(c, fiber.StatusInternalServerError, "陪玩业务操作失败")
	}
}

func requestAdmin(c fiber.Ctx) (*ent.Admin, error) {
	uid, _ := c.Locals("uid").(string)
	return currentAdmin(c.Context(), global.ORM, uid)
}

func publicSkus(c fiber.Ctx) error {
	items, err := global.ORM.EsportsSku.Query().Where(esportssku.StatusEQ(esportssku.StatusOnSale)).
		Order(ent.Desc(esportssku.FieldID)).Limit(100).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	public := make([]fiber.Map, 0, len(items))
	for _, item := range items {
		public = append(public, fiber.Map{
			"id": item.ID, "code": item.Code, "title": item.Title,
			"service_type": item.ServiceType, "unit_price_cents": item.UnitPriceCents,
			"service_units": item.ServiceUnits, "service_unit_name": item.ServiceUnitName,
			"required_seats": item.RequiredSeats,
		})
	}
	return reply(c, public)
}

func adminSkus(c fiber.Ctx) error {
	items, err := global.ORM.EsportsSku.Query().Order(ent.Desc(esportssku.FieldID)).Limit(100).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createSku(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in skuInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := saveSku(c.Context(), global.ORM, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func updateSkuHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "商品 ID 无效")
	}
	var in skuUpdateInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateSku(c.Context(), global.ORM, id, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func adminCategories(c fiber.Ctx) error {
	items, err := global.ORM.EsportsProductCategory.Query().
		Order(ent.Asc(esportsproductcategory.FieldSortOrder), ent.Asc(esportsproductcategory.FieldID)).
		Limit(200).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createCategory(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in categoryInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := saveCategory(c.Context(), global.ORM, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func updateCategoryHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "分类 ID 无效")
	}
	var in categoryInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateCategory(c.Context(), global.ORM, id, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func deleteCategoryHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "分类 ID 无效")
	}
	if err := deleteCategory(c.Context(), global.ORM, id); err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"id": id})
}

func adminCustomers(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	window, err := parseAdminListWindow(c)
	if err != nil {
		return businessError(c, err)
	}
	filter, err := parseAdminCustomerFilter(c)
	if err != nil {
		return businessError(c, err)
	}
	scope := func() *ent.EsportsCustomerQuery {
		q := global.ORM.EsportsCustomer.Query()
		if !isSuper {
			q.Where(esportscustomer.OwnerAdminIDEQ(operator.ID))
		}
		return q
	}
	total, err := filter.apply(scope()).Count(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	items, err := filter.apply(scope()).
		Order(ent.Desc(esportscustomer.FieldID)).
		Limit(window.limit).Offset(window.offset).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	return writeAdminList(c, window, total, items)
}

func createCustomer(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in customerInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := saveCustomer(c.Context(), global.ORM, operator.ID, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func adminOrders(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	window, err := parseAdminListWindow(c)
	if err != nil {
		return businessError(c, err)
	}
	filter, err := parseAdminOrderFilter(c)
	if err != nil {
		return businessError(c, err)
	}
	total, err := filter.apply(adminOrderScope(operator.ID, isSuper)).Count(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	items, err := filter.apply(adminOrderScope(operator.ID, isSuper)).
		Order(ent.Desc(esportsorder.FieldID)).
		Limit(window.limit).Offset(window.offset).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	return writeAdminList(c, window, total, items)
}

func adminOrder(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	item, err := adminOrderScope(operator.ID, isSuper).Where(esportsorder.IDEQ(id)).Only(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func createOrder(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	key := strings.TrimSpace(c.Get("Idempotency-Key"))
	var in orderInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := placeOrder(c.Context(), global.ORM, operator.ID, in, key, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func userOrderQuote(c fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "订单 ID 无效")
	}
	uid, _ := c.Locals("uid").(string)
	quote, err := orderQuote(c.Context(), global.ORM, uid, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, quote)
}

func userConfirmQuote(c fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "订单 ID 无效")
	}
	var in struct {
		ExpectedQuoteVersion int  `json:"expected_quote_version"`
		Accepted             bool `json:"accepted"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	uid, _ := c.Locals("uid").(string)
	confirmation, err := confirmQuoteRetry(c.Context(), global.ORM, uid, id, in.ExpectedQuoteVersion, in.Accepted, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{
		"confirmation_id": confirmation.ID, "quote_version": confirmation.SubjectVersion,
		"verified_at": confirmation.VerifiedAt,
	})
}

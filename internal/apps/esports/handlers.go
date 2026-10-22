package esports

import (
	"context"
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

// adminOrderPaymentStatuses 是管理端订单列表可筛选的支付状态白名单。
// 只列真实会被写入的状态：unpaid（待支付）→ paid（已收款），渠道撤销 / 拒付时为 exception。
// 审核报告 §B.3-4：partial（部分收款）全仓无写入方，故不再出现在筛选白名单里，避免一个永远 0 条的死筛选项。
var adminOrderPaymentStatuses = map[string]struct{}{
	string(esportsorder.PaymentStatusUnpaid):    {},
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
// allOrders 为 true 时不做收敛（超管，或命中 app.esports.finance_report_role_names 的财务角色，
// 见 §3.5-05：财务需要看到全部订单及其金额明细）。
func adminOrderScope(operatorID int, isSuper, allOrders bool) *ent.EsportsOrderQuery {
	q := global.ORM.EsportsOrder.Query()
	if isSuper || allOrders {
		return q
	}
	q.Where(esportsorder.Or(
		esportsorder.ResponsibleAdminIDEQ(operatorID),
		esportsorder.HasCollaboratorsWith(esportsordercollaborator.AdminIDEQ(operatorID)),
	))
	return q
}

// canViewAllOrders 判断当前操作人是否可看全部订单（§3.5-05）：超管，或命中财务角色配置。
// 财务角色沿用 finance_access.go 的同一配置键与判定，避免出现第二套财务口径。
func canViewAllOrders(c fiber.Ctx) bool {
	return canAccessFinanceReport(c)
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
	case errors.Is(err, errPriceDeviation):
		// §4.6-02：订单价明显偏离 SKU 标准价，需客服确认并填写偏离原因，状态码 409。
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
		// 用 err.Error()：裸哨兵仍是「无权访问该客户或订单」，带上下文的包装（如「退卡出账须由财务角色执行」）
		// 原样透出，前端才能提示具体原因；状态码与错误分类不变。
		return fail(c, fiber.StatusForbidden, err.Error())
	case errors.Is(err, errDispatchPriorityHold):
		// 优先通知窗口内订单尚未对大厅开放，给陪玩明确原因而不是笼统的 403 文案。
		return fail(c, fiber.StatusForbidden, errDispatchPriorityHold.Error())
	case errors.Is(err, errAgeUnverified):
		return fail(c, fiber.StatusForbidden, errAgeUnverified.Error())
	case errors.Is(err, errInsufficientBalance):
		// 用 err.Error()：裸哨兵仍为「钱包余额不足」，带上下文的包装（余额 / 储值卡）原样透出。
		return fail(c, fiber.StatusBadRequest, err.Error())
	case errors.Is(err, errNotConfigured):
		return fail(c, fiber.StatusServiceUnavailable, err.Error())
	case errors.Is(err, errOrderFrozen):
		// 存单冻结拦截：给前端明确中文提示（而非笼统的「业务状态已变化」），状态码 409。
		return fail(c, fiber.StatusConflict, errOrderFrozen.Error())
	case errors.Is(err, errConflict):
		// 用 err.Error()：裸哨兵仍为「业务状态已变化，请刷新后重试」，
		// 携带上下文的中文包装（如重复收款拦截「该订单已有待核验的转账凭证…」）原样透出给前端。
		return fail(c, fiber.StatusConflict, err.Error())
	case ent.IsConstraintError(err):
		// 唯一索引冲突不带业务上下文，只可能暴露底层约束细节，沿用统一中文文案。
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

// publicSkus 客户端商品列表（AuthPublic）：保持既有「仅在售」口径，
// 并按 product_list 组件 props 透传的查询参数收窄取数（§16.3-06 / §16.3-11）。
func publicSkus(c fiber.Ctx) error {
	props, err := parseSkuCatalogProps(c)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listPublicSkusByProps(c.Context(), global.ORM, props)
	if err != nil {
		return businessError(c, err)
	}
	public := make([]fiber.Map, 0, len(items))
	for _, item := range items {
		public = append(public, fiber.Map{
			"id": item.ID, "code": item.Code, "title": item.Title,
			"service_type": item.ServiceType, "unit_price_cents": item.UnitPriceCents,
			"service_units": item.ServiceUnits, "service_unit_name": item.ServiceUnitName,
			// §15.6-01：报单页需要一并看到结单要求与标准时长，避免再单独查后台接口。
			"close_rule_code": item.CloseRuleCode, "service_duration_minutes": item.ServiceDurationMinutes,
		})
	}
	return reply(c, public)
}

// skuOrderQuotePreview 是报单页的报价试算结果（§15.6-01）：不落单即可看到会员折扣金额、
// 应付金额、承诺服务量与该 SKU 适用的结单要求。
type skuOrderQuotePreview struct {
	SkuID                int    `json:"product_id"`
	Quantity             int64  `json:"quantity"`
	MemberLevelCode      string `json:"member_level_code"`
	MemberDiscountBps    int    `json:"member_discount_bps"`
	UnitPriceCents       int64  `json:"unit_price_cents"`
	SubtotalCents        int64  `json:"subtotal_cents"`
	MemberDiscountCents  int64  `json:"member_discount_cents"`
	PayableCents         int64  `json:"payable_cents"`
	PromisedServiceUnits int64  `json:"promised_service_units"`
	ServiceUnitName      string `json:"service_unit_name"`
	CloseRuleCode        string `json:"close_rule_code"`
	// §15.3-01 / §5.3-05：多人规格的每席承诺服务量与席位数（详情接口须能读到该字段）。
	PerSeatServiceUnits int64 `json:"-"`
	RequiredSeats       int   `json:"-"`
	// 会员权益消费点（P1-11）：报价页直接展示会员权益是否生效，以及本次订单是否会带
	// 「专属通道 / 会员优先」派单优先级标记（与 placeOrder 写进订单事件的标记同一口径）。
	MemberPrivilegeActive bool `json:"member_privilege_active"`
	DedicatedChannel      bool `json:"dedicated_channel"`
	DispatchPriority      bool `json:"dispatch_priority"`
	// P1-23：单个 SKU 的配置缺失（如多人规格没配每席承诺量）降级为「不可下单 + 中文原因」，
	// 不再让整张报单页的报价请求 400。
	NotOrderable       bool   `json:"not_orderable"`
	NotOrderableReason string `json:"not_orderable_reason,omitempty"`
}

// skuQuoteBaseline 是一次报价请求的客户侧基线（客户档案 + 会员权益），可跨多个 SKU 复用。
// 客户不存在 / 参数非法属于请求级错误，仍然整体失败（P1-23 只对单个 SKU 的配置缺失降级）。
type skuQuoteBaseline struct {
	customer  *ent.EsportsCustomer
	privilege *memberPrivilegeView
}

func loadSkuQuoteBaseline(ctx context.Context, db *ent.Client, customerID int, quantity int64) (*skuQuoteBaseline, error) {
	if customerID < 1 || quantity < 1 || quantity > 1000 {
		return nil, errInvalidInput
	}
	customer, err := db.EsportsCustomer.Get(ctx, customerID)
	if err != nil {
		return nil, err
	}
	privilege, err := memberPrivilegeFlags(ctx, db, customerID)
	if err != nil {
		return nil, err
	}
	return &skuQuoteBaseline{customer: customer, privilege: privilege}, nil
}

// buildSkuQuotePreview 按「商品规格价 → 会员折扣」口径组装单个 SKU 的报价快照。
func buildSkuQuotePreview(sku *ent.EsportsSku, baseline *skuQuoteBaseline,
	quantity, perSeatUnits, seatTotalUnits int64) *skuOrderQuotePreview {
	subtotal := sku.UnitPriceCents * quantity
	discount := memberDiscountCents(subtotal, baseline.privilege.DiscountBps)
	return &skuOrderQuotePreview{
		SkuID: sku.ID, Quantity: quantity, MemberLevelCode: baseline.customer.MemberLevelCode,
		MemberDiscountBps: baseline.privilege.DiscountBps, UnitPriceCents: sku.UnitPriceCents,
		SubtotalCents: subtotal, MemberDiscountCents: discount, PayableCents: subtotal - discount,
		PromisedServiceUnits: seatTotalUnits * quantity,
		ServiceUnitName:      sku.ServiceUnitName, CloseRuleCode: sku.CloseRuleCode,
		PerSeatServiceUnits: perSeatUnits, RequiredSeats: sku.RequiredSeats,
		MemberPrivilegeActive: baseline.privilege.Active,
		DedicatedChannel:      baseline.privilege.DedicatedChannel,
		DispatchPriority:      baseline.privilege.DedicatedChannel || baseline.privilege.PriorityDispatch,
	}
}

// buildSkuOrderQuote 用同一份客户基线计算单个 SKU 的报价；SKU 侧配置缺失时返回带上下文的中文错误。
func buildSkuOrderQuote(ctx context.Context, db *ent.Client, baseline *skuQuoteBaseline,
	sku *ent.EsportsSku, quantity int64) (*skuOrderQuotePreview, error) {
	if baseline == nil || sku == nil {
		return nil, errInvalidInput
	}
	perSeatUnits, seatTotalUnits, _, err := resolveSeatPromise(sku)
	if err != nil {
		return nil, err
	}
	return buildSkuQuotePreview(sku, baseline, quantity, perSeatUnits, seatTotalUnits), nil
}

// degradedSkuQuote 为配置缺失的单个 SKU 生成降级报价（P1-23）：价格与折扣口径照常展示，
// 但明确标记不可下单并给出中文原因，避免一个坏 SKU 拖垮整张报单页。
func degradedSkuQuote(sku *ent.EsportsSku, baseline *skuQuoteBaseline, quantity int64, cause error) *skuOrderQuotePreview {
	quote := buildSkuQuotePreview(sku, baseline, quantity, 0, 0)
	quote.NotOrderable = true
	quote.NotOrderableReason = skuNotOrderableReason(cause)
	return quote
}

// skuNotOrderableReason 把 SKU 侧配置错误转成面向报单页的中文原因：
// 去掉 errInvalidInput 哨兵文案，避免把「商品配置缺失」误报成客户输入问题。
func skuNotOrderableReason(cause error) string {
	if cause == nil {
		return "商品配置不完整，暂不可下单"
	}
	if ent.IsNotFound(cause) {
		return "商品关联的规格配置缺失，暂不可下单"
	}
	if !errors.Is(cause, errInvalidInput) {
		return "商品配置异常，暂不可下单"
	}
	message := strings.TrimSpace(strings.TrimPrefix(cause.Error(), errInvalidInput.Error()+"："))
	message = strings.TrimSpace(strings.TrimPrefix(message, errInvalidInput.Error()))
	if message == "" {
		return "商品配置不完整，暂不可下单"
	}
	return message
}

// skuOrderQuote 按（客户，SKU，份数）试算报价（§15.6-01）：只读计算，不落单、不占名额。
// 该入口只针对单个 SKU，配置缺失直接返回明确错误（P1-23 的「明确只针对该 SKU」分支）。
func skuOrderQuote(ctx context.Context, db *ent.Client, customerID, skuID int, quantity int64) (*skuOrderQuotePreview, error) {
	if skuID < 1 {
		return nil, errInvalidInput
	}
	baseline, err := loadSkuQuoteBaseline(ctx, db, customerID, quantity)
	if err != nil {
		return nil, err
	}
	sku, err := db.EsportsSku.Get(ctx, skuID)
	if err != nil {
		return nil, err
	}
	return buildSkuOrderQuote(ctx, db, baseline, sku, quantity)
}

// adminSkus 商品列表。带 quote_customer_id（可选 quote_quantity，默认 1）时按客户逐 SKU 试算报价，
// 响应由数组变为 {"items": [...], "quotes": {"<sku_id>": {...}}}，供报单页不落单即展示会员折扣与结单要求。
// P1-23：单个 SKU 配置缺失（如多人规格未配每席承诺量）只降级该 SKU（not_orderable + 中文原因），
// 不再让整张报单页的报价 400；显式传 quote_sku_id 只针对该 SKU 时仍返回明确错误。
func adminSkus(c fiber.Ctx) error {
	q := global.ORM.EsportsSku.Query()
	// 关键词同时匹配商品名与编码，供报单 / 补录页的可搜索商品下拉按需取数。
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		q.Where(esportssku.Or(
			esportssku.TitleContainsFold(keyword),
			esportssku.CodeContainsFold(keyword),
		))
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q.Where(esportssku.StatusEQ(esportssku.Status(status)))
	}
	items, err := q.Order(ent.Desc(esportssku.FieldID)).Limit(queryLimit(c, 100)).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	// §15.8-03：成本价只对授权角色（配置 sku_cost_view_roles，缺省仅超管）可见，其余角色按未登记返回。
	if !canViewSkuCost(c) {
		for _, item := range items {
			item.CostCents = nil
		}
	}
	customerID := queryInt(c, "quote_customer_id")
	if customerID < 1 {
		return reply(c, items)
	}
	quantity := int64(queryInt(c, "quote_quantity"))
	if quantity < 1 {
		quantity = 1
	}
	baseline, err := loadSkuQuoteBaseline(c.Context(), global.ORM, customerID, quantity)
	if err != nil {
		return businessError(c, err)
	}
	soleSkuID := queryInt(c, "quote_product_id")
	quotes := make(map[string]*skuOrderQuotePreview, len(items))
	for _, item := range items {
		quote, quoteErr := buildSkuOrderQuote(c.Context(), global.ORM, baseline, item, quantity)
		if quoteErr != nil {
			// 请求体明确只针对该 SKU 时仍返回明确错误，便于报单页定位具体原因。
			if soleSkuID == item.ID {
				return businessError(c, quoteErr)
			}
			quotes[strconv.Itoa(item.ID)] = degradedSkuQuote(item, baseline, quantity, quoteErr)
			continue
		}
		quotes[strconv.Itoa(item.ID)] = quote
	}
	return reply(c, fiber.Map{"items": items, "quotes": quotes})
}

func createSku(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in skuInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	in.RequiredSeats = 1
	in.PerSeatServiceUnits = 0
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
	in.RequiredSeats = 1
	in.PerSeatServiceUnits = 0
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
	allOrders := canViewAllOrders(c)
	total, err := filter.apply(adminOrderScope(operator.ID, isSuper, allOrders)).Count(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	items, err := filter.apply(adminOrderScope(operator.ID, isSuper, allOrders)).
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
	item, err := adminOrderScope(operator.ID, isSuper, canViewAllOrders(c)).
		Where(esportsorder.IDEQ(id)).Only(c.Context())
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

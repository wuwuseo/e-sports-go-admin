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
	"go-admin/internal/ent/esportsproductcategory"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/global"
)

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
	q := global.ORM.EsportsCustomer.Query()
	if super, _ := c.Locals("isSuperAdmin").(bool); !super {
		q.Where(esportscustomer.OwnerAdminIDEQ(operator.ID))
	}
	items, err := q.Order(ent.Desc(esportscustomer.FieldID)).Limit(100).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
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
	q := global.ORM.EsportsOrder.Query()
	if super, _ := c.Locals("isSuperAdmin").(bool); !super {
		q.Where(esportsorder.ResponsibleAdminIDEQ(operator.ID))
	}
	items, err := q.Order(ent.Desc(esportsorder.FieldID)).Limit(100).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func adminOrder(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "订单 ID 无效")
	}
	q := global.ORM.EsportsOrder.Query().Where(esportsorder.IDEQ(id))
	if super, _ := c.Locals("isSuperAdmin").(bool); !super {
		q.Where(esportsorder.ResponsibleAdminIDEQ(operator.ID))
	}
	item, err := q.Only(c.Context())
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

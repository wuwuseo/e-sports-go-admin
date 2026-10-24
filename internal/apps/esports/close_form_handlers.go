package esports

import (
	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 结单表单设计器：字段库与模板配置挂在「结算」菜单下。

func listCloseFormFieldsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listCloseFormFields(c.Context(), global.ORM, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createCloseFormFieldHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in closeFormFieldInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createCloseFormField(c.Context(), global.ORM, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func updateCloseFormFieldHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单表单字段")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		ExpectedVersion int `json:"expected_version"`
		closeFormFieldInput
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateCloseFormField(c.Context(), global.ORM, id, in.ExpectedVersion, in.closeFormFieldInput)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func setCloseFormFieldStatusHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单表单字段")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := setCloseFormFieldEnabled(c.Context(), global.ORM, id, in.Enabled)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func listCloseFormTemplatesHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listCloseFormTemplates(c.Context(), global.ORM, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createCloseFormTemplateHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in closeFormTemplateInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createCloseFormTemplate(c.Context(), global.ORM, in, adminID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func updateCloseFormTemplateHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单表单模板")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		ExpectedVersion int `json:"expected_version"`
		closeFormTemplateInput
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateCloseFormTemplate(c.Context(), global.ORM, id, in.ExpectedVersion, in.closeFormTemplateInput)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func activateCloseFormTemplateHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单表单模板")
	if err != nil {
		return businessError(c, err)
	}
	item, err := activateCloseFormTemplate(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func rollbackCloseFormTemplateHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单表单模板")
	if err != nil {
		return businessError(c, err)
	}
	item, err := rollbackCloseFormTemplate(c.Context(), global.ORM, id, adminID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// orderCloseFormHandler 返回订单结单表单的模板与字段定义（供结单弹窗渲染）。
func orderCloseFormHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	view, err := orderCloseForm(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

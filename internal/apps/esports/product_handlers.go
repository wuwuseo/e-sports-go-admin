package esports

import (
	"strconv"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsproduct"
	"go-admin/internal/ent/esportsspecgroup"
	"go-admin/internal/global"
)

// 商品父实体 / 规格组 / 逐席角色快照的 HTTP 入口。
// 路由与权限由主代理在 app.go 接线，这里只提供 handler 实现。

func adminProducts(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := global.ORM.EsportsProduct.Query().
		Order(ent.Desc(esportsproduct.FieldID)).Limit(200).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// adminProduct 商品详情：父商品 + 子 SKU + 被引用的规格组。
func adminProduct(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "商品 ID 无效")
	}
	view, err := productDetail(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

func createProductHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in productInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := saveProduct(c.Context(), global.ORM, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func updateProductHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "商品 ID 无效")
	}
	var in productInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateProduct(c.Context(), global.ORM, id, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func adminSpecGroups(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := global.ORM.EsportsSpecGroup.Query().
		Order(ent.Desc(esportsspecgroup.FieldID)).Limit(200).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createSpecGroupHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in specGroupInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := saveSpecGroup(c.Context(), global.ORM, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func updateSpecGroupHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "规格组 ID 无效")
	}
	var in specGroupInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateSpecGroup(c.Context(), global.ORM, id, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// orderSeatRolesHandler 返回订单逐席角色快照；历史单无快照返回空列表。
func orderSeatRolesHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "订单 ID 无效")
	}
	seats, err := orderSeatRoles(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"order_id": id, "seat_roles": seats})
}

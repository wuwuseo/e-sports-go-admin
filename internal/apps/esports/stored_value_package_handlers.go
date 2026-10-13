package esports

import (
	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// listStoredValuePackagesHandler 套餐列表（§4.4）：默认只返回在售档位（开卡下拉用），all=1 返回全部含已下架。
func listStoredValuePackagesHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	onlyOnSale := c.Query("all") != "1"
	items, err := listStoredValuePackages(c.Context(), global.ORM, onlyOnSale)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// createStoredValuePackageHandler 新建套餐（§4.4）。
func createStoredValuePackageHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in storedValuePackageInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	pkg, err := saveStoredValuePackage(c.Context(), global.ORM, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, pkg)
}

// updateStoredValuePackageHandler 编辑套餐（§4.4）：code 不可改，version 乐观锁。
func updateStoredValuePackageHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "套餐")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		storedValuePackageInput
		Version int `json:"version"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	pkg, err := updateStoredValuePackage(c.Context(), global.ORM, id, in.Version, in.storedValuePackageInput)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, pkg)
}

// setStoredValuePackageStatusHandler 上下架套餐（§4.4 档位可单独上下架）。
func setStoredValuePackageStatusHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "套餐")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	pkg, err := setStoredValuePackageStatus(c.Context(), global.ORM, id, in.Status)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, pkg)
}

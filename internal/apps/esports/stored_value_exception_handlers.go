package esports

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 储值卡一致性异常队列的管理端入口（§4.6-06）：列表 / 详情 / 指派 / 处置。
// 路由与 PermAPI 由主控按 ROUND4-WIRING-CHECKLIST 接线，本文件只提供 handler。

// sveQueryBool 解析管理端布尔型查询参数（1/true/yes 视为真，其余为空即假）。
func sveQueryBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y":
		return true
	}
	return false
}

// adminStoredValueExceptions 异常队列列表：默认只看待跟进，可按状态 / 类型 / 卡 / 订单 / 跟进人过滤。
func adminStoredValueExceptions(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	items, err := listStoredValueExceptions(c.Context(), global.ORM, operator.ID, isSuper,
		storedValueExceptionFilter{
			Status:          strings.TrimSpace(c.Query("status")),
			Kind:            strings.TrimSpace(c.Query("kind")),
			CardID:          queryInt(c, "card_id"),
			OrderID:         queryInt(c, "order_id"),
			AssigneeAdminID: queryInt(c, "assignee_admin_id"),
			Mine:            sveQueryBool(c.Query("mine")),
			Unassigned:      sveQueryBool(c.Query("unassigned")),
		}, queryInt(c, "limit"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// adminStoredValueExceptionDetail 异常详情（含三处「真相」的对比快照）。
func adminStoredValueExceptionDetail(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "异常记录")
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	view, err := getStoredValueException(c.Context(), global.ORM, operator.ID, isSuper, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// assignStoredValueExceptionHandler 指派 / 改派跟进人。
func assignStoredValueExceptionHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "异常记录")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		AssigneeAdminID int `json:"assignee_admin_id"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	view, err := assignStoredValueException(c.Context(), global.ORM, id, operator.ID, isSuper,
		in.AssigneeAdminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// resolveStoredValueExceptionHandler 处置异常：action=resolve / ignore，可带乐观锁版本。
func resolveStoredValueExceptionHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "异常记录")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Action          string `json:"action"`
		Resolution      string `json:"resolution"`
		ExpectedVersion int    `json:"expected_version"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	view, err := resolveStoredValueException(c.Context(), global.ORM, id, operator.ID, isSuper,
		in.Action, in.Resolution, in.ExpectedVersion, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

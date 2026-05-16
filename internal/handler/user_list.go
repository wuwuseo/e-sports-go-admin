package handler

import (
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// ListUsers 获取用户列表
func ListUsers(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "10"))
	roleID, _ := strconv.Atoi(c.Query("role_id", "0"))

	req := &service.UserListRequest{
		Page:      page,
		PageSize:  pageSize,
		Keyword:   c.Query("keyword"),
		Status:    c.Query("status"),
		RoleID:    roleID,
		Platform:  c.Query("platform"),
		StartTime: c.Query("start_time"),
		EndTime:   c.Query("end_time"),
		SortField: c.Query("sort_field", "created_at"),
		SortOrder: c.Query("sort_order", "desc"),
	}

	if isBlacklisted := c.Query("is_blacklisted"); isBlacklisted != "" {
		v, err := strconv.ParseBool(isBlacklisted)
		if err == nil {
			req.IsBlacklisted = &v
		}
	}

	svc := c.Locals("service.UserListService").(service.UserListServiceInterface)
	result, err := svc.ListUsers(c.Context(), req)
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, "查询用户列表失败").ToJSON(c)
	}

	return response.Success(result).ToJSON(c)
}

// GetUserDetail 获取用户详情
func GetUserDetail(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	userID, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "无效的用户ID").ToJSON(c)
	}

	svc := c.Locals("service.UserListService").(service.UserListServiceInterface)
	result, err := svc.GetUserDetail(c.Context(), userID)
	if err != nil {
		switch err {
		case service.ErrUserNotFound:
			return response.Fail(global.ResponseNotFound, "用户不存在").ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "获取用户详情失败").ToJSON(c)
		}
	}

	return response.Success(result).ToJSON(c)
}

// ListUserThirdParty 获取用户的第三方账号列表
func ListUserThirdParty(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	userID, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "无效的用户ID").ToJSON(c)
	}

	svc := c.Locals("service.ThirdPartyUserService").(service.ThirdPartyUserServiceInterface)
	result, err := svc.ListUserThirdPartyAccounts(c.Context(), userID)
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, "查询第三方账号失败").ToJSON(c)
	}

	return response.Success(result).ToJSON(c)
}

// ExportUsers 导出用户列表
func ExportUsers(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	return response.Fail(global.ResponseErrorCode, "导出功能开发中").ToJSON(c)
}

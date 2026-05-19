package handler

import (
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// ListUsers 获取用户列表
// @Summary 获取用户列表
// @Description 获取用户列表，支持分页和筛选
// @Tags 用户管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Param keyword query string false "搜索关键词"
// @Param status query string false "状态筛选"
// @Param role_id query int false "角色ID"
// @Param platform query string false "平台"
// @Param start_time query string false "开始时间"
// @Param end_time query string false "结束时间"
// @Param sort_field query string false "排序字段，默认created_at"
// @Param sort_order query string false "排序方向，默认desc"
// @Param is_blacklisted query bool false "是否黑名单"
// @Success 200 {object} utils.Response{data=service.UserListResponse} "获取成功"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Router /api/v1/admin/auth/users [get]
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
// @Summary 获取用户详情
// @Description 根据用户ID获取用户详细信息
// @Tags 用户管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "用户ID"
// @Success 200 {object} utils.Response{data=service.UserDetailResponse} "获取成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Failure 404 {object} utils.Response "用户不存在"
// @Router /api/v1/admin/auth/users/{id} [get]
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
// @Summary 获取用户第三方账号列表
// @Description 获取指定用户绑定的第三方账号列表
// @Tags 用户管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "用户ID"
// @Success 200 {object} utils.Response{data=[]service.ThirdPartyUserResponse} "获取成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Router /api/v1/admin/auth/users/{id}/third-party [get]
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
// @Summary 导出用户列表
// @Description 导出符合条件的用户列表为Excel
// @Tags 用户管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param keyword query string false "搜索关键词"
// @Param status query string false "状态筛选"
// @Param role_id query int false "角色ID"
// @Param platform query string false "平台"
// @Param start_time query string false "开始时间"
// @Param end_time query string false "结束时间"
// @Success 200 {file} file "导出成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Router /api/v1/admin/auth/users/export [post]
func ExportUsers(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	return response.Fail(global.ResponseErrorCode, "导出功能开发中").ToJSON(c)
}

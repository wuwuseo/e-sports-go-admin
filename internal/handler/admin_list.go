package handler

import (
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// CreateAdmin 超级管理员创建管理员账号
// @Summary 创建管理员账号
// @Description 超级管理员创建新的管理员账号
// @Tags 管理员管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body service.CreateAdminRequest true "创建管理员请求"
// @Success 200 {object} utils.Response{data=service.AdminResponse} "创建成功"
// @Failure 400 {object} utils.Response "请求参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Router /api/v1/admin/auth/admins [post]
func CreateAdmin(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	svc := c.Locals("service.SuperAdminService").(service.SuperAdminServiceInterface)

	var req service.CreateAdminRequest
	if err := c.Bind().Body(&req); err != nil {
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}

	if req.Email == "" || req.Password == "" || req.Username == "" {
		return response.Fail(global.ResponseErrorCode, "邮箱、密码、用户名不能为空").ToJSON(c)
	}

	result, err := svc.CreateAdmin(c.Context(), &req)
	if err != nil {
		switch err {
		case service.ErrAdminEmailExists:
			return response.Fail(global.ResponseErrorCode, "邮箱已存在").ToJSON(c)
		case service.ErrAdminUsernameExists:
			return response.Fail(global.ResponseErrorCode, "用户名已存在").ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
		}
	}

	return response.Success(result).ToJSON(c)
}

// ListAdmins 管理员列表
// @Summary 获取管理员列表
// @Description 获取管理员账号列表，支持分页
// @Tags 管理员管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认20"
// @Param keyword query string false "搜索关键词"
// @Param status query string false "状态筛选"
// @Success 200 {object} utils.Response{data=service.AdminListResponse} "获取成功"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Router /api/v1/admin/auth/admins [get]
func ListAdmins(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	svc := c.Locals("service.SuperAdminService").(service.SuperAdminServiceInterface)

	var req service.AdminListRequest
	if err := c.Bind().Query(&req); err != nil {
		return response.Fail(global.ResponseErrorCode, "Invalid query params").ToJSON(c)
	}

	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 20
	}

	result, err := svc.ListAdmins(c.Context(), &req)
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	return response.Success(result).ToJSON(c)
}

// GetAdminDetail 管理员详情
// @Summary 获取管理员详情
// @Description 根据UID获取管理员详细信息
// @Tags 管理员管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param uid path string true "管理员UID"
// @Success 200 {object} utils.Response{data=service.AdminResponse} "获取成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Failure 404 {object} utils.Response "管理员不存在"
// @Router /api/v1/admin/auth/admins/{uid} [get]
func GetAdminDetail(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	uid := c.Params("uid")
	if uid == "" {
		return response.Fail(global.ResponseErrorCode, "缺少管理员UID").ToJSON(c)
	}

	adminService := c.Locals("service.AdminService").(service.AdminServiceInterface)
	admin, err := adminService.GetAdminByUID(c.Context(), uid)
	if err != nil {
		if err == service.ErrAdminNotFound {
			return response.Fail(global.ResponseNotFound, "Admin not found").ToJSON(c)
		}
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	return response.Success(admin).ToJSON(c)
}

// ActivateAdmin 激活管理员
// @Summary 激活管理员账号
// @Description 激活指定的管理员账号，使其可以登录
// @Tags 管理员管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "管理员ID"
// @Success 200 {object} utils.Response "激活成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Router /api/v1/admin/auth/admins/{id}/activate [put]
func ActivateAdmin(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	adminID, err := strconv.Atoi(c.Params("id"))
	if err != nil || adminID <= 0 {
		return response.Fail(global.ResponseErrorCode, "无效的管理员ID").ToJSON(c)
	}

	svc := c.Locals("service.SuperAdminService").(service.SuperAdminServiceInterface)
	if err := svc.ActivateAdmin(c.Context(), adminID); err != nil {
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	return response.Success(nil).ToJSON(c)
}

// DeactivateAdmin 停用管理员
// @Summary 停用管理员账号
// @Description 停用指定的管理员账号，使其无法登录
// @Tags 管理员管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "管理员ID"
// @Success 200 {object} utils.Response "停用成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Router /api/v1/admin/auth/admins/{id}/deactivate [put]
func DeactivateAdmin(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	adminID, err := strconv.Atoi(c.Params("id"))
	if err != nil || adminID <= 0 {
		return response.Fail(global.ResponseErrorCode, "无效的管理员ID").ToJSON(c)
	}

	svc := c.Locals("service.SuperAdminService").(service.SuperAdminServiceInterface)
	if err := svc.DeactivateAdmin(c.Context(), adminID); err != nil {
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	return response.Success(nil).ToJSON(c)
}

package handler

import (
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// CreateAdmin 超级管理员创建管理员账号
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

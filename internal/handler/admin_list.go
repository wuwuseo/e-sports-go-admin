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
	dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)

	var req service.CreateAdminRequest
	if err := c.Bind().Body(&req); err != nil {
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}

	if req.Email == "" || req.Password == "" || req.Username == "" {
		return response.Fail(global.ResponseErrorCode, "邮箱、密码、用户名不能为空").ToJSON(c)
	}

	op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c))
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	result, err := svc.CreateAdmin(c.Context(), op, &req)
	if err != nil {
		return mapAdminCreateError(c, response, err)
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

	// 传入当前操作者UID用于数据权限过滤
	if uid, ok := c.Locals("uid").(string); ok {
		req.CurrentUID = uid
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
// @Description 根据 ID 获取管理员详细信息
// @Tags 管理员管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "管理员ID"
// @Success 200 {object} utils.Response{data=service.AdminResponse} "获取成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Failure 404 {object} utils.Response "管理员不存在"
// @Router /api/v1/admin/auth/admins/{id} [get]
func GetAdminDetail(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	adminID, err := strconv.Atoi(c.Params("id"))
	if err != nil || adminID <= 0 {
		return response.Fail(global.ResponseErrorCode, "无效的管理员ID").ToJSON(c)
	}

	svc := c.Locals("service.SuperAdminService").(service.SuperAdminServiceInterface)
	dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)

	op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c))
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	result, err := svc.GetAdminByID(c.Context(), op, adminID)
	if err != nil {
		return mapAdminScopeError(c, response, err)
	}
	return response.Success(result).ToJSON(c)
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
	dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
	op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c))
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	if err := svc.ActivateAdmin(c.Context(), op, adminID); err != nil {
		return mapAdminScopeError(c, response, err)
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
	dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
	op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c))
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	if err := svc.DeactivateAdmin(c.Context(), op, adminID); err != nil {
		return mapAdminScopeError(c, response, err)
	}

	return response.Success(nil).ToJSON(c)
}

// UpdateAdmin 更新管理员信息
// @Summary 更新管理员信息
// @Description 更新指定管理员的基本信息和密码
// @Tags 管理员管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "管理员ID"
// @Param request body service.UpdateAdminRequest true "更新管理员请求"
// @Success 200 {object} utils.Response{data=service.AdminResponse} "更新成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Failure 404 {object} utils.Response "管理员不存在"
// @Router /api/v1/admin/auth/admins/{id} [put]
func UpdateAdminByID(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	adminID, err := strconv.Atoi(c.Params("id"))
	if err != nil || adminID <= 0 {
		return response.Fail(global.ResponseErrorCode, "无效的管理员ID").ToJSON(c)
	}

	var req service.UpdateAdminRequest
	if err := c.Bind().Body(&req); err != nil {
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}

	svc := c.Locals("service.SuperAdminService").(service.SuperAdminServiceInterface)
	dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
	op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c))
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	result, err := svc.UpdateAdmin(c.Context(), op, adminID, &req)
	if err != nil {
		return mapAdminScopeError(c, response, err)
	}

	return response.Success(result).ToJSON(c)
}

// getOperatorUID 从 fiber.Ctx Locals 中读取当前操作者 UID
func getOperatorUID(c fiber.Ctx) string {
	if uid, ok := c.Locals("uid").(string); ok {
		return uid
	}
	return ""
}

// mapAdminScopeError 把 service 层数据权限/自我保护错误映射为 HTTP 响应
func mapAdminScopeError(c fiber.Ctx, response *utils.Response, err error) error {
	switch err {
	case service.ErrAdminOutOfScope:
		return response.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
	case service.ErrCannotToggleSelfStatus:
		return response.Fail(global.ResponseSelfToggleForbidden, global.ResponseMsg[global.ResponseSelfToggleForbidden]).ToJSON(c)
	case service.ErrCannotModifySelfRoles:
		return response.Fail(global.ResponseSelfRoleModifyForbidden, global.ResponseMsg[global.ResponseSelfRoleModifyForbidden]).ToJSON(c)
	case service.ErrDepartmentOutOfScope:
		return response.Fail(global.ResponseDepartmentOutOfScope, global.ResponseMsg[global.ResponseDepartmentOutOfScope]).ToJSON(c)
	// P1: 目标角色的数据范围超出当前操作者，拒绝越权
	case service.ErrRoleScopeEscalation:
		return response.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
	// P2: 目标部门不存在
	case service.ErrDepartmentNotFound:
		return response.Fail(global.ResponseDepartmentNotFound, global.ResponseMsg[global.ResponseDepartmentNotFound]).ToJSON(c)
	case service.ErrAdminNotFound:
		return response.Fail(global.ResponseNotFound, "管理员不存在").ToJSON(c)
	case service.ErrAdminEmailExists:
		return response.Fail(global.ResponseErrorCode, "邮箱已存在").ToJSON(c)
	default:
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}
}

// mapAdminCreateError 把 CreateAdmin 的 service 层错误映射为 HTTP 响应
func mapAdminCreateError(c fiber.Ctx, response *utils.Response, err error) error {
	switch err {
	case service.ErrAdminEmailExists:
		return response.Fail(global.ResponseErrorCode, "邮箱已存在").ToJSON(c)
	case service.ErrAdminUsernameExists:
		return response.Fail(global.ResponseErrorCode, "用户名已存在").ToJSON(c)
	case service.ErrSuperAdminRoleAssignmentForbidden:
		return response.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
	case service.ErrDepartmentOutOfScope:
		return response.Fail(global.ResponseDepartmentOutOfScope, global.ResponseMsg[global.ResponseDepartmentOutOfScope]).ToJSON(c)
	// P1: 目标角色的数据范围超出当前操作者，拒绝越权
	case service.ErrRoleScopeEscalation:
		return response.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
	// P2: 目标部门不存在
	case service.ErrDepartmentNotFound:
		return response.Fail(global.ResponseDepartmentNotFound, global.ResponseMsg[global.ResponseDepartmentNotFound]).ToJSON(c)
	case service.ErrInvalidRoleIDs:
		return response.Fail(global.ResponseErrorCode, "存在无效的角色 ID").ToJSON(c)
	case service.ErrPrimaryRoleInvalid:
		return response.Fail(global.ResponseErrorCode, "主角色必须在 role_ids 列表中").ToJSON(c)
	default:
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}
}

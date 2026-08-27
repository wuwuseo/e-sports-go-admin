package handler

import (
	"errors"
	"fmt"
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/log"
	"github.com/wuwuseo/cmf/validate"
	"go.uber.org/zap"
)

// CreatePermission 创建权限
// @Summary 创建权限
// @Description 创建新的权限
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body service.CreatePermissionRequest true "创建权限请求"
// @Success 200 {object} utils.Response{data=string}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/permissions [post]
func CreatePermission(c fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 解析请求体
	req, err := validate.ParseAndValidate[service.CreatePermissionRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}
	if !permissionDomainAllowed(c, req.Domain) {
		return resp.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
	}
	op, err := permissionOperatorScope(c)
	if err != nil {
		return mapOperatorContextError(c, resp, err, "创建权限失败")
	}

	// 创建权限
	uid, _ := c.Locals("uid").(string)
	err = permissionService.CreatePermission(c.Context(), uid, req, op)
	if err != nil {
		if errors.Is(err, service.ErrRoleScopeEscalation) {
			return resp.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
		}
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseNotFound, err.Error()).ToJSON(c)
		}
		if err == service.ErrSuperAdminProtected {
			return resp.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		}
		if err == service.ErrPermissionScopeEscalation {
			return resp.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		}
		if err == service.ErrSelfRolePermissionMutationForbidden {
			return resp.Fail(global.ResponseSelfRolePermissionForbidden, global.ResponseMsg[global.ResponseSelfRolePermissionForbidden]).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "创建权限失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "permission", "创建", fmt.Sprintf("创建权限: %s %s", req.Object, req.Action))

	return resp.Success("创建权限成功", global.ResponseSuccessCode).ToJSON(c)
}

// UpdatePermission 更新权限
// @Summary 更新权限
// @Description 更新权限信息
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body service.UpdatePermissionRequest true "更新权限请求"
// @Success 200 {object} utils.Response{data=string}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/permissions [put]
func UpdatePermission(c fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 解析请求体
	req, err := validate.ParseAndValidate[service.UpdatePermissionRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}
	if !permissionDomainAllowed(c, req.Domain) {
		return resp.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
	}
	op, err := permissionOperatorScope(c)
	if err != nil {
		return mapOperatorContextError(c, resp, err, "更新权限失败")
	}

	// 更新权限
	uid, _ := c.Locals("uid").(string)
	err = permissionService.UpdatePermission(c.Context(), uid, req, op)
	if err != nil {
		if errors.Is(err, service.ErrRoleScopeEscalation) {
			return resp.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
		}
		if err == service.ErrPermissionNotFound {
			return resp.Fail(global.ResponseNotFound, err.Error()).ToJSON(c)
		}
		if err == service.ErrSuperAdminProtected {
			return resp.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		}
		if err == service.ErrPermissionScopeEscalation {
			return resp.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		}
		if err == service.ErrSelfRolePermissionMutationForbidden {
			return resp.Fail(global.ResponseSelfRolePermissionForbidden, global.ResponseMsg[global.ResponseSelfRolePermissionForbidden]).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "更新权限失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "permission", "更新", fmt.Sprintf("更新权限: %s %s", req.Object, req.Action))

	return resp.Success("更新权限成功", global.ResponseSuccessCode).ToJSON(c)
}

// DeletePermission 删除权限
// @Summary 删除权限
// @Description 删除指定的权限
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param role_name query string true "角色名称"
// @Param domain query string true "域"
// @Param object query string true "资源对象"
// @Param action query string true "动作"
// @Success 200 {object} utils.Response{data=string}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/permissions [delete]
func DeletePermission(c fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 获取请求参数
	roleName := c.Query("role_name")
	domain := c.Query("domain")
	object := c.Query("object")
	action := c.Query("action")

	// 验证参数
	if roleName == "" || domain == "" || object == "" || action == "" {
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}
	if !permissionDomainAllowed(c, domain) {
		return resp.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
	}
	op, err := permissionOperatorScope(c)
	if err != nil {
		return mapOperatorContextError(c, resp, err, "删除权限失败")
	}

	// 删除权限
	uid, _ := c.Locals("uid").(string)
	err = permissionService.DeletePermission(c.Context(), uid, roleName, domain, object, action, op)
	if err != nil {
		if errors.Is(err, service.ErrRoleScopeEscalation) {
			return resp.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
		}
		if err == service.ErrPermissionNotFound {
			return resp.Fail(global.ResponseNotFound, err.Error()).ToJSON(c)
		}
		if err == service.ErrSuperAdminProtected {
			return resp.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		}
		if err == service.ErrPermissionScopeEscalation {
			return resp.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		}
		if err == service.ErrSelfRolePermissionMutationForbidden {
			return resp.Fail(global.ResponseSelfRolePermissionForbidden, global.ResponseMsg[global.ResponseSelfRolePermissionForbidden]).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "删除权限失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "permission", "删除", fmt.Sprintf("删除权限: %s %s", object, action))

	return resp.Success("删除权限成功", global.ResponseSuccessCode).ToJSON(c)
}

// GetPermission 获取权限详情
// @Summary 获取权限详情
// @Description 获取指定权限的详细信息
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param role_name query string true "角色名称"
// @Param domain query string true "域"
// @Param object query string true "资源对象"
// @Param action query string true "动作"
// @Success 200 {object} utils.Response{data=service.PermissionResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/permissions/detail [get]
func GetPermission(c fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 获取请求参数
	roleName := c.Query("role_name")
	domain := c.Query("domain")
	object := c.Query("object")
	action := c.Query("action")

	// 验证参数
	if roleName == "" || domain == "" || object == "" || action == "" {
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}
	if !permissionDomainAllowed(c, domain) {
		return resp.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
	}
	op, err := permissionOperatorScope(c)
	if err != nil {
		return mapOperatorContextError(c, resp, err, "获取权限失败")
	}

	// 获取权限详情
	permission, err := permissionService.GetPermission(c.Context(), roleName, domain, object, action, op)
	if err != nil {
		if errors.Is(err, service.ErrRoleScopeEscalation) {
			return resp.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
		}
		if err == service.ErrPermissionNotFound {
			return resp.Fail(global.ResponseNotFound, err.Error()).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取权限失败").ToJSON(c)
	}

	return resp.Success(permission, global.ResponseSuccessCode, "获取权限成功").ToJSON(c)
}

// ListPermissions 获取权限列表
// @Summary 获取权限列表
// @Description 获取权限列表，支持分页
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Param keyword query string false "关键字"
// @Param type query string false "权限类型"
// @Param domain query string false "域"
// @Param role_name query string false "角色名称"
// @Success 200 {object} utils.Response{data=service.ListPermissionsResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/permissions [get]
func ListPermissions(c fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 解析请求参数
	req, err := validate.ParseQueryAndValidate[service.ListPermissionsRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	// 设置默认值
	if req.Page == 0 {
		req.Page = 1
	}
	if req.PageSize == 0 {
		req.PageSize = 10
	}
	if req.Domain == "" {
		req.Domain = getOperatorDomain(c)
	}
	if !permissionDomainAllowed(c, req.Domain) {
		return resp.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
	}
	op, err := permissionOperatorScope(c)
	if err != nil {
		return mapOperatorContextError(c, resp, err, "获取权限列表失败")
	}

	// 获取权限列表
	uid, _ := c.Locals("uid").(string)
	permissions, err := permissionService.ListPermissions(c.Context(), uid, req, op)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取权限列表失败").ToJSON(c)
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取权限列表成功").ToJSON(c)
}

// ListAllPermissions 获取所有权限（不带分页）
// @Summary 获取所有权限
// @Description 获取所有权限，支持筛选，不带分页
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param keyword query string false "关键字"
// @Param type query string false "权限类型"
// @Param domain query string false "域"
// @Param role_name query string false "角色名称"
// @Success 200 {object} utils.Response{data=[]service.PermissionResponse}
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/permissions/all [get]
func ListAllPermissions(c fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 获取查询参数
	keyword := c.Query("keyword")
	permType := service.PermissionType(c.Query("type"))
	domain := c.Query("domain")
	roleName := c.Query("role_name")
	if domain == "" {
		domain = getOperatorDomain(c)
	}
	if !permissionDomainAllowed(c, domain) {
		return resp.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
	}
	op, err := permissionOperatorScope(c)
	if err != nil {
		return mapOperatorContextError(c, resp, err, "获取权限列表失败")
	}

	// 获取所有权限
	uid, _ := c.Locals("uid").(string)
	permissions, err := permissionService.ListAllPermissions(c.Context(), uid, keyword, permType, domain, roleName, op)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取权限列表失败").ToJSON(c)
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取权限列表成功").ToJSON(c)
}

// AssignPermissionsRequest 分配权限请求
type AssignPermissionsRequest struct {
	RoleName    string                   `json:"role_name" validate:"required"`
	Domain      string                   `json:"domain" validate:"required"`
	Permissions []service.PermissionRule `json:"permissions" validate:"required,min=1"`
}

// AssignPermissionsToRole 为角色分配权限
// @Summary 为角色分配权限
// @Description 为指定角色分配多个权限
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body AssignPermissionsRequest true "分配权限请求"
// @Success 200 {object} utils.Response{data=interface{}}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles/permissions/assign [post]
func AssignPermissionsToRole(c fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 解析请求体
	var req AssignPermissionsRequest
	if err := c.Bind().Body(&req); err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	// 验证参数
	if req.RoleName == "" || req.Domain == "" || len(req.Permissions) == 0 {
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}
	if !permissionDomainAllowed(c, req.Domain) {
		return resp.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
	}
	op, err := permissionOperatorScope(c)
	if err != nil {
		return mapOperatorContextError(c, resp, err, "分配权限失败")
	}

	// 为角色分配权限
	uid, _ := c.Locals("uid").(string)
	err = permissionService.AssignPermissionsToRole(c.Context(), uid, req.RoleName, req.Domain, req.Permissions, op)
	if err != nil {
		if errors.Is(err, service.ErrRoleScopeEscalation) {
			return resp.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
		}
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
		if err == service.ErrSuperAdminProtected {
			return resp.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		}
		if err == service.ErrPermissionScopeEscalation {
			return resp.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		}
		if err == service.ErrSelfRolePermissionMutationForbidden {
			return resp.Fail(global.ResponseSelfRolePermissionForbidden, global.ResponseMsg[global.ResponseSelfRolePermissionForbidden]).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "为角色分配权限失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "permission", "分配", fmt.Sprintf("为角色 %s 分配 %d 条权限", req.RoleName, len(req.Permissions)))

	return resp.Success("为角色分配权限成功", global.ResponseSuccessCode).ToJSON(c)
}

// RemovePermissionsFromRole 从角色移除权限
// @Summary 从角色移除权限
// @Description 从指定角色移除多个权限
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body AssignPermissionsRequest true "移除权限请求"
// @Success 200 {object} utils.Response{data=interface{}}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles/permissions/remove [post]
func RemovePermissionsFromRole(c fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 解析请求体
	var req AssignPermissionsRequest
	if err := c.Bind().Body(&req); err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	// 验证参数
	if req.RoleName == "" || req.Domain == "" || len(req.Permissions) == 0 {
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}
	if !permissionDomainAllowed(c, req.Domain) {
		return resp.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
	}
	op, err := permissionOperatorScope(c)
	if err != nil {
		return mapOperatorContextError(c, resp, err, "移除权限失败")
	}

	// 从角色移除权限
	uid, _ := c.Locals("uid").(string)
	err = permissionService.RemovePermissionsFromRole(c.Context(), uid, req.RoleName, req.Domain, req.Permissions, op)
	if err != nil {
		if errors.Is(err, service.ErrRoleScopeEscalation) {
			return resp.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
		}
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
		if err == service.ErrSuperAdminProtected {
			return resp.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		}
		if err == service.ErrPermissionScopeEscalation {
			return resp.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		}
		if err == service.ErrSelfRolePermissionMutationForbidden {
			return resp.Fail(global.ResponseSelfRolePermissionForbidden, global.ResponseMsg[global.ResponseSelfRolePermissionForbidden]).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "从角色移除权限失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "permission", "移除", fmt.Sprintf("从角色 %s 移除 %d 条权限", req.RoleName, len(req.Permissions)))

	return resp.Success("从角色移除权限成功", global.ResponseSuccessCode).ToJSON(c)
}

// GetRolePermissions 获取角色权限
// @Summary 获取角色权限
// @Description 获取指定角色的所有权限
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param role_name query string true "角色名称"
// @Param domain query string false "域，默认当前认证域"
// @Success 200 {object} utils.Response{data=[]service.PermissionResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles/permissions [get]
func GetRolePermissions(c fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 获取请求参数
	roleName := c.Query("role_name")
	domain := c.Query("domain", getOperatorDomain(c))
	if !permissionDomainAllowed(c, domain) {
		return resp.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
	}

	// 验证参数
	if roleName == "" {
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}
	op, err := permissionOperatorScope(c)
	if err != nil {
		return mapOperatorContextError(c, resp, err, "获取角色权限失败")
	}

	// 获取角色权限
	permissions, err := permissionService.GetRolePermissions(c.Context(), roleName, domain, op)
	if err != nil {
		if errors.Is(err, service.ErrRoleScopeEscalation) {
			return resp.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
		}
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取角色权限失败").ToJSON(c)
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取角色权限成功").ToJSON(c)
}

// GetUserMenuPermissions 获取用户菜单权限树
// @Summary 获取用户菜单权限树
// @Description 获取当前用户可访问的菜单权限树，仅返回 type=menu 的启用权限节点；普通用户按角色权限过滤，超级管理员返回当前域下全部启用菜单；当用户只拥有子菜单权限时会自动补齐父级菜单节点用于前端渲染。
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param domain query string false "权限域，使用当前认证域"
// @Success 200 {object} utils.Response{data=[]service.SystemPermissionTreeNode} "获取成功，data为菜单权限树数组，节点type固定为menu"
// @Failure 401 {object} utils.Response "未授权或JWT缺少域信息"
// @Failure 403 {object} utils.Response "无权限访问"
// @Failure 500 {object} utils.Response "获取用户菜单权限失败"
// @Router /api/v1/admin/auth/user/menu-permissions [get]
func GetUserMenuPermissions(c fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 获取当前用户ID
	userID, _ := c.Locals("uid").(string)

	// 获取域
	domain := getOperatorDomain(c)
	if domain == "" {
		return resp.Fail(global.ResponseUnauthorized, global.ResponseMsg[global.ResponseUnauthorized]).ToJSON(c)
	}

	// 获取用户菜单权限树
	permissions, err := permissionService.GetUserMenuPermissionTree(c.Context(), userID, domain)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取用户菜单权限失败: "+err.Error()).ToJSON(c)
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取用户菜单权限成功").ToJSON(c)
}

// GetUserButtonPermissions 获取用户按钮权限
// @Summary 获取用户按钮权限
// @Description 获取当前用户可访问的按钮权限列表（扁平数组），仅返回 type=button 的启用权限；普通用户按角色权限过滤，超级管理员返回当前域下全部启用按钮。
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param domain query string false "权限域，使用当前认证域"
// @Success 200 {object} utils.Response{data=[]service.PermissionResponse} "获取成功，data为按钮权限扁平数组"
// @Failure 401 {object} utils.Response "未授权或JWT缺少域信息"
// @Failure 403 {object} utils.Response "无权限访问"
// @Failure 500 {object} utils.Response "获取用户按钮权限失败"
// @Router /api/v1/admin/auth/user/button-permissions [get]
func GetUserButtonPermissions(c fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 获取当前用户ID
	userID, _ := c.Locals("uid").(string)

	// 获取域
	domain := getOperatorDomain(c)
	if domain == "" {
		return resp.Fail(global.ResponseUnauthorized, global.ResponseMsg[global.ResponseUnauthorized]).ToJSON(c)
	}

	// 获取用户按钮权限
	permissions, err := permissionService.GetUserButtonPermissions(c.Context(), userID, domain)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取用户按钮权限失败: "+err.Error()).ToJSON(c)
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取用户按钮权限成功").ToJSON(c)
}

// GetUserAPIPermissions 获取用户API权限
// @Summary 获取用户API权限
// @Description 获取当前用户可访问的API权限列表（扁平数组），仅返回 type=api 的权限规则，用于前端或调试场景查看当前用户接口访问范围。
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param domain query string false "权限域，使用当前认证域"
// @Success 200 {object} utils.Response{data=[]service.PermissionResponse} "获取成功，data为API权限扁平数组"
// @Failure 401 {object} utils.Response "未授权或JWT缺少域信息"
// @Failure 403 {object} utils.Response "无权限访问"
// @Failure 500 {object} utils.Response "获取用户API权限失败"
// @Router /api/v1/admin/auth/user/api-permissions [get]
func GetUserAPIPermissions(c fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 获取当前用户ID
	userID, _ := c.Locals("uid").(string)

	// 获取域
	domain := getOperatorDomain(c)
	if domain == "" {
		return resp.Fail(global.ResponseUnauthorized, global.ResponseMsg[global.ResponseUnauthorized]).ToJSON(c)
	}

	// 获取用户API权限
	permissions, err := permissionService.GetUserAPIPermissions(c.Context(), userID, domain)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取用户API权限失败: "+err.Error()).ToJSON(c)
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取用户API权限成功").ToJSON(c)
}

// GetUserPermissions 获取用户混合权限树
// @Summary 获取用户混合权限树
// @Description 获取当前用户可访问的混合权限树数组，包含 type=menu、type=button、type=api 的启用权限节点；普通用户按角色权限过滤，超级管理员返回当前域下全部启用权限；子节点有权限时会自动补齐父级节点。
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param domain query string false "权限域，使用当前认证域"
// @Success 200 {object} utils.Response{data=[]service.SystemPermissionTreeNode} "获取成功，data为混合权限树数组，节点type用于区分menu/button/api"
// @Failure 401 {object} utils.Response "未授权或JWT缺少域信息"
// @Failure 403 {object} utils.Response "无权限访问"
// @Failure 500 {object} utils.Response "获取用户权限失败"
// @Router /api/v1/admin/auth/user/permissions [get]
func GetUserPermissions(c fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 获取当前用户ID
	userID, _ := c.Locals("uid").(string)

	// 获取域
	domain := getOperatorDomain(c)
	if domain == "" {
		return resp.Fail(global.ResponseUnauthorized, global.ResponseMsg[global.ResponseUnauthorized]).ToJSON(c)
	}

	// 获取用户混合权限树
	permissions, err := permissionService.GetUserPermissionTree(c.Context(), userID, domain)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取用户权限失败: "+err.Error()).ToJSON(c)
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取用户权限成功").ToJSON(c)
}

// GetRolePermissionsByRoleID 通过角色ID获取权限
// @Summary 通过角色ID获取权限
// @Description 根据角色ID获取该角色的所有权限
// @Tags 角色-权限管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "角色ID"
// @Param domain query string false "域，默认当前认证域"
// @Success 200 {object} utils.Response{data=[]service.PermissionResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles/{id}/permissions [get]
func GetRolePermissionsByRoleID(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的角色ID").ToJSON(c)
	}

	domain := c.Query("domain", getOperatorDomain(c))
	if !permissionDomainAllowed(c, domain) {
		return resp.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
	}
	op, err := permissionOperatorScope(c)
	if err != nil {
		return mapOperatorContextError(c, resp, err, "获取角色权限失败")
	}

	permissions, err := permissionService.GetRolePermissionsByRoleID(c.Context(), id, domain, op)
	if err != nil {
		if errors.Is(err, service.ErrRoleScopeEscalation) {
			return resp.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
		}
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseNotFound, "角色不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取角色权限失败").ToJSON(c)
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取角色权限成功").ToJSON(c)
}

// SaveRolePermissions 覆盖式保存角色权限
// @Summary 覆盖式保存角色权限
// @Description 覆盖式保存角色权限，删除原有权限并添加新权限
// @Tags 角色-权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "角色ID"
// @Param body body service.SaveRolePermissionsRequest true "保存角色权限请求"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles/{id}/permissions [post]
func SaveRolePermissions(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的角色ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.SaveRolePermissionsRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}
	if !permissionDomainAllowed(c, req.Domain) {
		return resp.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
	}
	op, err := permissionOperatorScope(c)
	if err != nil {
		return mapOperatorContextError(c, resp, err, "保存角色权限失败")
	}

	uid, _ := c.Locals("uid").(string)
	err = permissionService.SaveRolePermissions(c.Context(), uid, id, req, op)
	if err != nil {
		if errors.Is(err, service.ErrRoleScopeEscalation) {
			return resp.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
		}
		log.Error("保存角色权限失败", zap.Int("role_id", id), zap.String("domain", req.Domain), zap.Error(err))
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseNotFound, "角色不存在").ToJSON(c)
		}
		if err == service.ErrSuperAdminProtected {
			return resp.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		}
		if err == service.ErrPermissionScopeEscalation {
			return resp.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		}
		if err == service.ErrSelfRolePermissionMutationForbidden {
			return resp.Fail(global.ResponseSelfRolePermissionForbidden, global.ResponseMsg[global.ResponseSelfRolePermissionForbidden]).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "保存角色权限失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "permission", "保存", fmt.Sprintf("保存角色 ID:%d 的权限，共 %d 条", id, len(req.Permissions)))

	return resp.Success(nil, global.ResponseSuccessCode, "保存角色权限成功").ToJSON(c)
}

func permissionDomainAllowed(c fiber.Ctx, requested string) bool {
	domain := getOperatorDomain(c)
	return domain != "" && requested == domain
}

func permissionOperatorScope(c fiber.Ctx) (*service.EffectiveDataScope, error) {
	dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
	return dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c), getOperatorDomain(c))
}

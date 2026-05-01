package handler

import (
	"fmt"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/wuwuseo/cmf/validate"
)

// CreatePermission 创建权限
// @Summary 创建权限
// @Description 为指定角色创建权限
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param permission body service.CreatePermissionRequest true "创建权限请求"
// @Success 200 {object} utils.Response{data=interface{}}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/permissions [post]
func CreatePermission(c *fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 解析并验证请求体
	req, err := validate.ParseAndValidate[service.CreatePermissionRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	// 创建权限
	err = permissionService.CreatePermission(c.Context(), req)
	if err != nil {
		if err == service.ErrPermissionAlreadyExists {
			return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "创建权限失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "权限管理", "创建", fmt.Sprintf("创建权限: %s/%s", req.Object, req.Action))

	return resp.Success("权限创建成功", global.ResponseSuccessCode).ToJSON(c)
}

// UpdatePermission 更新权限
// @Summary 更新权限
// @Description 更新指定权限
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param permission body service.UpdatePermissionRequest true "更新权限请求"
// @Success 200 {object} utils.Response{data=interface{}}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/permissions [put]
func UpdatePermission(c *fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 解析并验证请求体
	req, err := validate.ParseAndValidate[service.UpdatePermissionRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	// 更新权限
	err = permissionService.UpdatePermission(c.Context(), req)
	if err != nil {
		if err == service.ErrPermissionNotFound {
			return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "更新权限失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "权限管理", "更新", fmt.Sprintf("更新权限: %s/%s", req.NewObject, req.NewAction))

	return resp.Success("权限更新成功", global.ResponseSuccessCode).ToJSON(c)
}

// DeletePermission 删除权限
// @Summary 删除权限
// @Description 删除指定权限
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param role_name query string true "角色名称"
// @Param domain query string true "域"
// @Param object query string true "权限对象"
// @Param action query string true "权限操作"
// @Success 200 {object} utils.Response{data=interface{}}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/permissions [delete]
func DeletePermission(c *fiber.Ctx) error {
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

	// 删除权限
	err := permissionService.DeletePermission(c.Context(), roleName, domain, object, action)
	if err != nil {
		if err == service.ErrPermissionNotFound {
			return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "删除权限失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "权限管理", "删除", fmt.Sprintf("删除权限: %s %s/%s", roleName, object, action))

	return resp.Success("权限删除成功", global.ResponseSuccessCode).ToJSON(c)
}

// GetPermission 获取权限详情
// @Summary 获取权限详情
// @Description 获取指定权限的详情
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param role_name query string true "角色名称"
// @Param domain query string true "域"
// @Param object query string true "权限对象"
// @Param action query string true "权限操作"
// @Success 200 {object} utils.Response{data=service.PermissionResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/permissions/detail [get]
func GetPermission(c *fiber.Ctx) error {
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

	// 获取权限详情
	permission, err := permissionService.GetPermission(c.Context(), roleName, domain, object, action)
	if err != nil {
		if err == service.ErrPermissionNotFound {
			return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取权限详情失败").ToJSON(c)
	}

	return resp.Success(permission, global.ResponseSuccessCode, "获取权限详情成功").ToJSON(c)
}

// ListPermissions 获取权限列表
// @Summary 获取权限列表
// @Description 获取权限列表，支持分页和筛选
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
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/permissions [get]
func ListPermissions(c *fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 解析并验证查询参数
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

	// 获取权限列表
	permissions, err := permissionService.ListPermissions(c.Context(), req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取权限列表失败").ToJSON(c)
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取权限列表成功").ToJSON(c)
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
func AssignPermissionsToRole(c *fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 解析请求体
	var req struct {
		RoleName    string                   `json:"role_name" validate:"required"`
		Domain      string                   `json:"domain" validate:"required"`
		Permissions []service.PermissionRule `json:"permissions" validate:"required,min=1"`
	}
	if err := c.BodyParser(&req); err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	// 验证参数
	if req.RoleName == "" || req.Domain == "" || len(req.Permissions) == 0 {
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	// 为角色分配权限
	err := permissionService.AssignPermissionsToRole(c.Context(), req.RoleName, req.Domain, req.Permissions)
	if err != nil {
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "为角色分配权限失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "权限管理", "分配", fmt.Sprintf("为角色 %s 分配 %d 条权限", req.RoleName, len(req.Permissions)))

	return resp.Success("为角色分配权限成功", global.ResponseSuccessCode).ToJSON(c)
}

// RemovePermissionsFromRole 从角色移除权限
// @Summary 从角色移除权限
// @Description 从指定角色移除多个权限
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body RemovePermissionsRequest true "移除权限请求"
// @Success 200 {object} utils.Response{data=interface{}}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles/permissions/remove [post]
func RemovePermissionsFromRole(c *fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 解析请求体
	var req struct {
		RoleName    string                   `json:"role_name" validate:"required"`
		Domain      string                   `json:"domain" validate:"required"`
		Permissions []service.PermissionRule `json:"permissions" validate:"required,min=1"`
	}
	if err := c.BodyParser(&req); err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	// 验证参数
	if req.RoleName == "" || req.Domain == "" || len(req.Permissions) == 0 {
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	// 从角色移除权限
	err := permissionService.RemovePermissionsFromRole(c.Context(), req.RoleName, req.Domain, req.Permissions)
	if err != nil {
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "从角色移除权限失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "权限管理", "移除", fmt.Sprintf("从角色 %s 移除 %d 条权限", req.RoleName, len(req.Permissions)))

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
// @Param domain query string true "域"
// @Success 200 {object} utils.Response{data=[]service.PermissionResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles/permissions [get]
func GetRolePermissions(c *fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 获取请求参数
	roleName := c.Query("role_name")
	domain := c.Query("domain", "default")

	// 验证参数
	if roleName == "" {
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	// 获取角色权限
	permissions, err := permissionService.GetRolePermissions(c.Context(), roleName, domain)
	if err != nil {
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取角色权限失败").ToJSON(c)
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取角色权限成功").ToJSON(c)
}

// GetUserMenuPermissions 获取用户菜单权限
// @Summary 获取用户菜单权限
// @Description 获取当前用户的菜单权限
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param domain query string false "域，默认default"
// @Success 200 {object} utils.Response{data=[]service.PermissionResponse}
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/user/menu-permissions [get]
func GetUserMenuPermissions(c *fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 获取当前用户ID
	userID, _ := c.Locals("uid").(string)

	// 获取域
	domain := c.Query("domain", "default")

	// 获取用户菜单权限
	permissions, err := permissionService.GetUserMenuPermissions(c.Context(), userID, domain)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取用户菜单权限失败").ToJSON(c)
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取用户菜单权限成功").ToJSON(c)
}

// GetUserButtonPermissions 获取用户按钮权限
// @Summary 获取用户按钮权限
// @Description 获取当前用户的按钮权限
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param domain query string false "域，默认default"
// @Success 200 {object} utils.Response{data=[]service.PermissionResponse}
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/user/button-permissions [get]
func GetUserButtonPermissions(c *fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 获取当前用户ID
	userID, _ := c.Locals("uid").(string)

	// 获取域
	domain := c.Query("domain", "default")

	// 获取用户按钮权限
	permissions, err := permissionService.GetUserButtonPermissions(c.Context(), userID, domain)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取用户按钮权限失败").ToJSON(c)
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取用户按钮权限成功").ToJSON(c)
}

// GetUserAPIPermissions 获取用户API权限
// @Summary 获取用户API权限
// @Description 获取当前用户的API权限
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param domain query string false "域，默认default"
// @Success 200 {object} utils.Response{data=[]service.PermissionResponse}
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/user/api-permissions [get]
func GetUserAPIPermissions(c *fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 获取当前用户ID
	userID, _ := c.Locals("uid").(string)

	// 获取域
	domain := c.Query("domain", "default")

	// 获取用户API权限
	permissions, err := permissionService.GetUserAPIPermissions(c.Context(), userID, domain)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取用户API权限失败").ToJSON(c)
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取用户API权限成功").ToJSON(c)
}

// GetUserPermissions 获取用户所有权限（菜单和按钮）
// @Summary 获取用户所有权限
// @Description 获取当前用户的菜单和按钮权限
// @Tags 权限管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param domain query string false "域，默认default"
// @Success 200 {object} utils.Response{data=map[string][]service.PermissionResponse}
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/user/permissions [get]
func GetUserPermissions(c *fiber.Ctx) error {
	// 获取响应工具
	resp := c.Locals("utils.Response").(*utils.Response)

	// 获取权限服务
	permissionService := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

	// 获取当前用户ID
	userID, _ := c.Locals("uid").(string)

	// 获取域
	domain := c.Query("domain", "default")

	// 获取用户菜单权限
	menuPermissions, err := permissionService.GetUserMenuPermissions(c.Context(), userID, domain)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取用户菜单权限失败").ToJSON(c)
	}

	// 获取用户按钮权限
	buttonPermissions, err := permissionService.GetUserButtonPermissions(c.Context(), userID, domain)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取用户按钮权限失败").ToJSON(c)
	}

	// 聚合权限
	permissions := map[string][]service.PermissionResponse{
		"menu":   convertToPermissionResponseSlice(menuPermissions),
		"button": convertToPermissionResponseSlice(buttonPermissions),
	}

	return resp.Success(permissions, global.ResponseSuccessCode, "获取用户权限成功").ToJSON(c)
}

// convertToPermissionResponseSlice 将指针切片转换为值切片
func convertToPermissionResponseSlice(permissions []*service.PermissionResponse) []service.PermissionResponse {
	result := make([]service.PermissionResponse, len(permissions))
	for i, p := range permissions {
		result[i] = *p
	}
	return result
}

package handler

import (
	"fmt"
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/validate"
)

// CreateDepartment 创建部门
// @Summary 创建部门
// @Description 创建新部门
// @Tags 部门管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body service.CreateDepartmentRequest true "部门信息"
// @Success 200 {object} utils.Response{data=service.DepartmentResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/departments [post]
func CreateDepartment(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	departmentService := c.Locals("service.DepartmentService").(service.DepartmentServiceInterface)

	req, err := validate.ParseAndValidate[service.CreateDepartmentRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	dept, err := departmentService.Create(c.Context(), req)
	if err != nil {
		switch err {
		case service.ErrDepartmentNotFound:
			return resp.Fail(global.ResponseNotFound, "父部门不存在").ToJSON(c)
		case service.ErrInvalidDepartmentParent:
			return resp.Fail(global.ResponseErrorCode, "无效的父部门ID").ToJSON(c)
		case service.ErrDepartmentCycle:
			return resp.Fail(global.ResponseErrorCode, "父部门不能是自身或子部门").ToJSON(c)
		default:
			return resp.Fail(global.ResponseInternalServerError, "创建部门失败").ToJSON(c)
		}
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "department", "创建", "创建部门: "+req.Name)

	return resp.Success(dept, global.ResponseSuccessCode, "部门创建成功").ToJSON(c)
}

// ListDepartmentTree 获取部门树
// @Summary 获取部门树
// @Description 获取部门树形结构
// @Tags 部门管理
// @Produce json
// @Security BearerAuth
// @Param keyword query string false "关键字"
// @Param status query string false "状态"
// @Success 200 {object} utils.Response{data=[]service.DepartmentTreeNode}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/departments/tree [get]
func ListDepartmentTree(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	departmentService := c.Locals("service.DepartmentService").(service.DepartmentServiceInterface)

	req, err := validate.ParseQueryAndValidate[service.ListDepartmentsRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	tree, err := departmentService.Tree(c.Context(), req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取部门树失败").ToJSON(c)
	}

	return resp.Success(tree, global.ResponseSuccessCode, "获取部门树成功").ToJSON(c)
}

// GetDepartment 获取部门详情
// @Summary 获取部门详情
// @Description 根据ID获取部门详细信息
// @Tags 部门管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "部门ID"
// @Success 200 {object} utils.Response{data=service.DepartmentResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/departments/{id} [get]
func GetDepartment(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	departmentService := c.Locals("service.DepartmentService").(service.DepartmentServiceInterface)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的部门ID").ToJSON(c)
	}

	dept, err := departmentService.Get(c.Context(), id)
	if err != nil {
		if err == service.ErrDepartmentNotFound {
			return resp.Fail(global.ResponseNotFound, "部门不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取部门失败").ToJSON(c)
	}

	return resp.Success(dept, global.ResponseSuccessCode, "获取部门成功").ToJSON(c)
}

// UpdateDepartment 更新部门
// @Summary 更新部门
// @Description 根据ID更新部门信息
// @Tags 部门管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "部门ID"
// @Param body body service.UpdateDepartmentRequest true "部门信息"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/departments/{id} [put]
func UpdateDepartment(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	departmentService := c.Locals("service.DepartmentService").(service.DepartmentServiceInterface)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的部门ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.UpdateDepartmentRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if err := departmentService.Update(c.Context(), id, req); err != nil {
		switch err {
		case service.ErrDepartmentNotFound:
			return resp.Fail(global.ResponseNotFound, "部门不存在").ToJSON(c)
		case service.ErrDepartmentCycle:
			return resp.Fail(global.ResponseErrorCode, "父部门不能是自身或子部门").ToJSON(c)
		case service.ErrInvalidDepartmentParent:
			return resp.Fail(global.ResponseErrorCode, "无效的父部门ID").ToJSON(c)
		default:
			return resp.Fail(global.ResponseInternalServerError, "更新部门失败").ToJSON(c)
		}
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "department", "更新", fmt.Sprintf("更新部门 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "部门更新成功").ToJSON(c)
}

// DeleteDepartment 删除部门
// @Summary 删除部门
// @Description 根据ID删除部门
// @Tags 部门管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "部门ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/departments/{id} [delete]
func DeleteDepartment(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	departmentService := c.Locals("service.DepartmentService").(service.DepartmentServiceInterface)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的部门ID").ToJSON(c)
	}

	if err := departmentService.Delete(c.Context(), id); err != nil {
		switch err {
		case service.ErrDepartmentNotFound:
			return resp.Fail(global.ResponseNotFound, "部门不存在").ToJSON(c)
		case service.ErrDepartmentHasChildren:
			return resp.Fail(global.ResponseConflict, "部门下存在子部门，无法删除").ToJSON(c)
		default:
			return resp.Fail(global.ResponseInternalServerError, "删除部门失败").ToJSON(c)
		}
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "department", "删除", fmt.Sprintf("删除部门 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "部门删除成功").ToJSON(c)
}

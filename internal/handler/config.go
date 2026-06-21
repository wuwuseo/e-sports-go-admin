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

// CreateConfig 创建动态配置
// @Summary 创建动态配置
// @Description 创建新的系统配置项
// @Tags 动态配置
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body service.CreateConfigRequest true "配置信息"
// @Success 200 {object} utils.Response{data=service.ConfigResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/configs [post]
func CreateConfig(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.ConfigService").(*service.ConfigService)

	req, err := validate.ParseAndValidate[service.CreateConfigRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	item, err := svc.Create(c.Context(), req)
	if err != nil {
		if err == service.ErrConfigKeyExists {
			return resp.Fail(global.ResponseConflict, "配置键已存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "创建配置失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "system", "创建", fmt.Sprintf("创建配置: %s", req.Key), "value")

	return resp.Success(item, global.ResponseSuccessCode, "配置创建成功").ToJSON(c)
}

// ListConfigs 获取动态配置列表
// @Summary 获取动态配置列表
// @Description 获取动态配置列表，支持分页、关键字、分组和状态筛选
// @Tags 动态配置
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Param keyword query string false "关键字"
// @Param group query string false "分组"
// @Param status query int false "状态 1启用 0禁用"
// @Success 200 {object} utils.Response{data=service.ListConfigResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/configs [get]
func ListConfigs(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.ConfigService").(*service.ConfigService)

	req, err := validate.ParseQueryAndValidate[service.ListConfigRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if req.Page == 0 {
		req.Page = 1
	}
	if req.PageSize == 0 {
		req.PageSize = 10
	}

	result, err := svc.List(c.Context(), req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取配置列表失败").ToJSON(c)
	}

	return resp.Success(result, global.ResponseSuccessCode, "获取配置列表成功").ToJSON(c)
}

// GetConfig 获取动态配置详情
// @Summary 获取动态配置详情
// @Description 根据ID获取动态配置详细信息
// @Tags 动态配置
// @Produce json
// @Security BearerAuth
// @Param id path int true "配置ID"
// @Success 200 {object} utils.Response{data=service.ConfigResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/configs/{id} [get]
func GetConfig(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.ConfigService").(*service.ConfigService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的配置ID").ToJSON(c)
	}

	item, err := svc.Get(c.Context(), id)
	if err != nil {
		if err == service.ErrConfigNotFound {
			return resp.Fail(global.ResponseNotFound, "配置不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取配置失败").ToJSON(c)
	}

	return resp.Success(item, global.ResponseSuccessCode, "获取配置成功").ToJSON(c)
}

// GetConfigByKey 按键获取配置
// @Summary 按键获取配置
// @Description 根据配置键获取单个配置值（带缓存）
// @Tags 动态配置
// @Produce json
// @Security BearerAuth
// @Param key path string true "配置键"
// @Success 200 {object} utils.Response{data=service.ConfigResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/configs/by-key/{key} [get]
func GetConfigByKey(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.ConfigService").(*service.ConfigService)

	key := c.Params("key")
	if key == "" {
		return resp.Fail(global.ResponseErrorCode, "配置键不能为空").ToJSON(c)
	}

	item, err := svc.GetByKey(c.Context(), key)
	if err != nil {
		if err == service.ErrConfigNotFound {
			return resp.Fail(global.ResponseNotFound, "配置不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取配置失败").ToJSON(c)
	}

	return resp.Success(item, global.ResponseSuccessCode, "获取配置成功").ToJSON(c)
}

// GetConfigsByGroup 按分组获取配置
// @Summary 按分组获取配置
// @Description 根据分组名获取该分组下所有启用的配置列表（带缓存）
// @Tags 动态配置
// @Produce json
// @Security BearerAuth
// @Param group path string true "分组名"
// @Success 200 {object} utils.Response{data=[]service.ConfigResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/configs/group/{group} [get]
func GetConfigsByGroup(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.ConfigService").(*service.ConfigService)

	group := c.Params("group")
	if group == "" {
		return resp.Fail(global.ResponseErrorCode, "分组名不能为空").ToJSON(c)
	}

	items, err := svc.GetByGroup(c.Context(), group)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取配置失败").ToJSON(c)
	}

	return resp.Success(items, global.ResponseSuccessCode, "获取配置成功").ToJSON(c)
}

// GetConfigGroups 获取配置分组选项（作为tabs分组）
// @Summary 获取配置分组选项
// @Description 获取配置分组选项，用于前端tabs切换，来源为字典数据 sys_config_group，value 与配置的 group 字段关联
// @Tags 动态配置
// @Produce json
// @Security BearerAuth
// @Success 200 {object} utils.Response{data=[]service.ConfigGroup}
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/configs/groups [get]
func GetConfigGroups(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.ConfigService").(*service.ConfigService)

	groups, err := svc.GetGroups(c.Context())
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取配置分组失败").ToJSON(c)
	}

	return resp.Success(groups, global.ResponseSuccessCode, "获取配置分组成功").ToJSON(c)
}

// UpdateConfig 更新动态配置
// @Summary 更新动态配置
// @Description 根据ID更新动态配置信息
// @Tags 动态配置
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "配置ID"
// @Param body body service.UpdateConfigRequest true "更新信息"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/configs/{id} [put]
func UpdateConfig(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.ConfigService").(*service.ConfigService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的配置ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.UpdateConfigRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if err := svc.Update(c.Context(), id, req); err != nil {
		if err == service.ErrConfigNotFound {
			return resp.Fail(global.ResponseNotFound, "配置不存在").ToJSON(c)
		}
		if err == service.ErrConfigKeyExists {
			return resp.Fail(global.ResponseConflict, "配置键已存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "更新配置失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "system", "更新", fmt.Sprintf("更新配置 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "配置更新成功").ToJSON(c)
}

// DeleteConfig 删除动态配置
// @Summary 删除动态配置
// @Description 根据ID删除动态配置
// @Tags 动态配置
// @Produce json
// @Security BearerAuth
// @Param id path int true "配置ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/configs/{id} [delete]
func DeleteConfig(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.ConfigService").(*service.ConfigService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的配置ID").ToJSON(c)
	}

	if err := svc.Delete(c.Context(), id); err != nil {
		if err == service.ErrConfigNotFound {
			return resp.Fail(global.ResponseNotFound, "配置不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "删除配置失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "system", "删除", fmt.Sprintf("删除配置 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "配置删除成功").ToJSON(c)
}

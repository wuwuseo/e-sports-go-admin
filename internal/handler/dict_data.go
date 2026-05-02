package handler

import (
	"fmt"
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/wuwuseo/cmf/validate"
)

// CreateDictData 创建字典数据
// @Summary 创建字典数据
// @Description 创建新的字典数据项
// @Tags 数据字典
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body service.CreateDictDataRequest true "字典数据信息"
// @Success 200 {object} utils.Response{data=service.DictDataResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/dict-data [post]
func CreateDictData(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.DictDataService").(*service.DictDataService)

	req, err := validate.ParseAndValidate[service.CreateDictDataRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	item, err := svc.Create(c.Context(), req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "创建字典数据失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "system", "创建", fmt.Sprintf("创建字典项: %s/%s", req.DictType, req.Label))

	return resp.Success(item, global.ResponseSuccessCode, "字典数据创建成功").ToJSON(c)
}

// ListDictData 获取字典数据列表
// @Summary 获取字典数据列表
// @Description 获取字典数据列表，支持分页、关键字、字典类型和状态筛选
// @Tags 数据字典
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Param keyword query string false "关键字"
// @Param dict_type query string false "字典类型编码"
// @Param status query int false "状态 1启用 0禁用"
// @Success 200 {object} utils.Response{data=service.ListDictDataResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/dict-data [get]
func ListDictData(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.DictDataService").(*service.DictDataService)

	req, err := validate.ParseQueryAndValidate[service.ListDictDataRequest](c)
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
		return resp.Fail(global.ResponseInternalServerError, "获取字典数据列表失败").ToJSON(c)
	}

	return resp.Success(result, global.ResponseSuccessCode, "获取字典数据列表成功").ToJSON(c)
}

// GetDictData 获取字典数据详情
// @Summary 获取字典数据详情
// @Description 根据ID获取字典数据详细信息
// @Tags 数据字典
// @Produce json
// @Security BearerAuth
// @Param id path int true "字典数据ID"
// @Success 200 {object} utils.Response{data=service.DictDataResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/dict-data/{id} [get]
func GetDictData(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.DictDataService").(*service.DictDataService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的字典数据ID").ToJSON(c)
	}

	item, err := svc.Get(c.Context(), id)
	if err != nil {
		if err == service.ErrDictDataNotFound {
			return resp.Fail(global.ResponseNotFound, "字典数据不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取字典数据失败").ToJSON(c)
	}

	return resp.Success(item, global.ResponseSuccessCode, "获取字典数据成功").ToJSON(c)
}

// GetDictDataByType 按类型获取字典数据
// @Summary 按类型获取字典数据
// @Description 根据字典类型编码获取启用的字典数据列表，用于前端下拉框数据源
// @Tags 数据字典
// @Produce json
// @Security BearerAuth
// @Param type path string true "字典类型编码"
// @Success 200 {object} utils.Response{data=[]service.DictDataResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/dict-data/type/{type} [get]
func GetDictDataByType(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.DictDataService").(*service.DictDataService)

	dictType := c.Params("type")
	if dictType == "" {
		return resp.Fail(global.ResponseErrorCode, "字典类型编码不能为空").ToJSON(c)
	}

	items, err := svc.GetByType(c.Context(), dictType)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取字典数据失败").ToJSON(c)
	}

	return resp.Success(items, global.ResponseSuccessCode, "获取字典数据成功").ToJSON(c)
}

// UpdateDictData 更新字典数据
// @Summary 更新字典数据
// @Description 根据ID更新字典数据信息
// @Tags 数据字典
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "字典数据ID"
// @Param body body service.UpdateDictDataRequest true "更新信息"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/dict-data/{id} [put]
func UpdateDictData(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.DictDataService").(*service.DictDataService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的字典数据ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.UpdateDictDataRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if err := svc.Update(c.Context(), id, req); err != nil {
		if err == service.ErrDictDataNotFound {
			return resp.Fail(global.ResponseNotFound, "字典数据不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "更新字典数据失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "system", "更新", fmt.Sprintf("更新字典项 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "字典数据更新成功").ToJSON(c)
}

// DeleteDictData 删除字典数据
// @Summary 删除字典数据
// @Description 根据ID删除字典数据
// @Tags 数据字典
// @Produce json
// @Security BearerAuth
// @Param id path int true "字典数据ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/dict-data/{id} [delete]
func DeleteDictData(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.DictDataService").(*service.DictDataService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的字典数据ID").ToJSON(c)
	}

	if err := svc.Delete(c.Context(), id); err != nil {
		if err == service.ErrDictDataNotFound {
			return resp.Fail(global.ResponseNotFound, "字典数据不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "删除字典数据失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "system", "删除", fmt.Sprintf("删除字典项 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "字典数据删除成功").ToJSON(c)
}

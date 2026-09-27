package handler

import (
	"fmt"
	"strconv"

	"go-admin/internal/ent"
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/validate"
)

// CreateSlide 创建幻灯片
// @Summary 创建幻灯片
// @Description 创建新的幻灯片
// @Tags 幻灯片管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body service.CreateSlideRequest true "幻灯片信息"
// @Success 200 {object} utils.Response{data=service.SlideResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/slides [post]
func CreateSlide(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SlideService").(*service.SlideService)

	req, err := validate.ParseAndValidate[service.CreateSlideRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	item, err := svc.Create(c.Context(), req)
	if err != nil {
		if ent.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "创建幻灯片失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "system", "创建", fmt.Sprintf("创建幻灯片: %s", req.Title))

	return resp.Success(item, global.ResponseSuccessCode, "幻灯片创建成功").ToJSON(c)
}

// ListSlides 获取幻灯片列表
// @Summary 获取幻灯片列表
// @Description 获取幻灯片列表,支持分页、分组和状态筛选
// @Tags 幻灯片管理
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码,默认1"
// @Param page_size query int false "每页数量,默认10"
// @Param keyword query string false "关键字"
// @Param group_code query string false "分组编码"
// @Param status query int false "状态 1启用 0禁用"
// @Success 200 {object} utils.Response{data=service.ListSlideResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/slides [get]
func ListSlides(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SlideService").(*service.SlideService)

	req, err := validate.ParseQueryAndValidate[service.ListSlideRequest](c)
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
		return resp.Fail(global.ResponseInternalServerError, "获取幻灯片列表失败").ToJSON(c)
	}

	return resp.Success(result, global.ResponseSuccessCode, "获取幻灯片列表成功").ToJSON(c)
}

// GetSlide 获取幻灯片详情
// @Summary 获取幻灯片详情
// @Description 根据ID获取幻灯片详细信息
// @Tags 幻灯片管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "幻灯片ID"
// @Success 200 {object} utils.Response{data=service.SlideResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/slides/{id} [get]
func GetSlide(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SlideService").(*service.SlideService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的幻灯片ID").ToJSON(c)
	}

	item, err := svc.Get(c.Context(), id)
	if err != nil {
		if err == service.ErrSlideNotFound {
			return resp.Fail(global.ResponseNotFound, "幻灯片不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取幻灯片失败").ToJSON(c)
	}

	return resp.Success(item, global.ResponseSuccessCode, "获取幻灯片成功").ToJSON(c)
}

// UpdateSlide 更新幻灯片
// @Summary 更新幻灯片
// @Description 根据ID更新幻灯片信息
// @Tags 幻灯片管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "幻灯片ID"
// @Param body body service.UpdateSlideRequest true "更新信息"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/slides/{id} [put]
func UpdateSlide(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SlideService").(*service.SlideService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的幻灯片ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.UpdateSlideRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if err := svc.Update(c.Context(), id, req); err != nil {
		if err == service.ErrSlideNotFound {
			return resp.Fail(global.ResponseNotFound, "幻灯片不存在").ToJSON(c)
		}
		if ent.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "更新幻灯片失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "system", "更新", fmt.Sprintf("更新幻灯片 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "幻灯片更新成功").ToJSON(c)
}

// DeleteSlide 删除幻灯片
// @Summary 删除幻灯片
// @Description 根据ID删除幻灯片
// @Tags 幻灯片管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "幻灯片ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/slides/{id} [delete]
func DeleteSlide(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SlideService").(*service.SlideService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的幻灯片ID").ToJSON(c)
	}

	if err := svc.Delete(c.Context(), id); err != nil {
		if err == service.ErrSlideNotFound {
			return resp.Fail(global.ResponseNotFound, "幻灯片不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "删除幻灯片失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "system", "删除", fmt.Sprintf("删除幻灯片 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "幻灯片删除成功").ToJSON(c)
}

// ListPublicSlides 公开获取幻灯片(免登录)
// @Summary 公开幻灯片列表
// @Description 免登录获取启用中的幻灯片,可按分组过滤,按sort升序返回;空结果返回空数组
// @Tags 公开接口
// @Produce json
// @Param group query string false "分组编码,不传返回全部分组"
// @Success 200 {object} utils.Response{data=[]service.SlidePublicResponse}
// @Failure 500 {object} utils.Response
// @Router /api/v1/public/slides [get]
func ListPublicSlides(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SlideService").(*service.SlideService)

	items, err := svc.ListPublic(c.Context(), c.Query("group"))
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取幻灯片失败").ToJSON(c)
	}

	return resp.Success(items, global.ResponseSuccessCode, "获取幻灯片成功").ToJSON(c)
}

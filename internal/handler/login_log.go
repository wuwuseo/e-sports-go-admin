package handler

import (
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/validate"
)

// ListLoginLogs 获取登录日志列表
// @Summary 获取登录日志列表
// @Description 获取登录日志列表，支持分页、用户名、登录类型、状态、IP 和时间范围筛选
// @Tags 登录日志
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Param username query string false "登录账号"
// @Param login_type query string false "登录类型：user/admin"
// @Param status query string false "登录状态：success/fail"
// @Param ip query string false "客户端IP"
// @Param start_time query string false "开始时间（RFC3339格式）"
// @Param end_time query string false "结束时间（RFC3339格式）"
// @Success 200 {object} utils.Response{data=service.ListLoginLogsResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/login-logs [get]
func ListLoginLogs(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	logService := c.Locals("service.LoginLogService").(service.LoginLogServiceInterface)
	dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)

	req, err := validate.ParseQueryAndValidate[service.ListLoginLogsRequest](c)
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

	op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c), getOperatorDomain(c))
	if err != nil {
		return mapOperatorContextError(c, resp, err, "获取登录日志列表失败")
	}

	logs, err := logService.List(c.Context(), op, req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取登录日志列表失败").ToJSON(c)
	}

	return resp.Success(logs, global.ResponseSuccessCode, "获取登录日志列表成功").ToJSON(c)
}

// GetLoginLog 获取登录日志详情
// @Summary 获取登录日志详情
// @Description 根据ID获取登录日志详细信息
// @Tags 登录日志
// @Produce json
// @Security BearerAuth
// @Param id path int true "日志ID"
// @Success 200 {object} utils.Response{data=service.LoginLogResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/login-logs/{id} [get]
func GetLoginLog(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	logService := c.Locals("service.LoginLogService").(service.LoginLogServiceInterface)
	dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的日志ID").ToJSON(c)
	}

	op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c), getOperatorDomain(c))
	if err != nil {
		return mapOperatorContextError(c, resp, err, "获取登录日志失败")
	}

	row, err := logService.Get(c.Context(), op, id)
	if err != nil {
		if err == service.ErrLoginLogNotFound {
			return resp.Fail(global.ErrCodeOperationLogNotFound, "登录日志不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取登录日志失败").ToJSON(c)
	}

	return resp.Success(row, global.ResponseSuccessCode, "获取登录日志成功").ToJSON(c)
}

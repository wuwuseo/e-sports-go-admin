package handler

import (
	"context"
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/jwt"
	"github.com/wuwuseo/cmf/validate"
)

// logOperation 记录操作日志的辅助函数
func logOperation(c fiber.Ctx, logService service.OperationLogServiceInterface, module, action, description string) {
	claims := jwt.GetJWTUserData(c)
	userUID := ""
	username := ""
	if claims != nil {
		if id, ok := claims["id"].(string); ok {
			userUID = id
		}
		if name, ok := claims["subject"].(string); ok {
			username = name
		}
	}

	_ = logService.Log(context.Background(), &service.LogEntry{
		UserUID:     userUID,
		Username:    username,
		Module:      module,
		Action:      action,
		Description: description,
		RequestBody: utils.SafeGetBody(c),
		Method:      c.Method(),
		Path:        c.Path(),
		IP:          c.IP(),
		UserAgent:   c.Get("User-Agent"),
		StatusCode:  200,
	})
}

// ListOperationLogs 获取操作日志列表
// @Summary 获取操作日志列表
// @Description 获取操作日志列表，支持分页、用户名、模块、操作和时间范围筛选
// @Tags 操作日志
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Param username query string false "用户名"
// @Param module query string false "模块名"
// @Param action query string false "操作"
// @Param start_time query string false "开始时间（RFC3339格式）"
// @Param end_time query string false "结束时间（RFC3339格式）"
// @Success 200 {object} utils.Response{data=service.ListOperationLogsResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/operation-logs [get]
func ListOperationLogs(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)

	req, err := validate.ParseQueryAndValidate[service.ListOperationLogsRequest](c)
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

	logs, err := logService.List(c.Context(), req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取操作日志列表失败").ToJSON(c)
	}

	return resp.Success(logs, global.ResponseSuccessCode, "获取操作日志列表成功").ToJSON(c)
}

// GetOperationLog 获取操作日志详情
// @Summary 获取操作日志详情
// @Description 根据ID获取操作日志详细信息
// @Tags 操作日志
// @Produce json
// @Security BearerAuth
// @Param id path int true "日志ID"
// @Success 200 {object} utils.Response{data=service.OperationLogResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/operation-logs/{id} [get]
func GetOperationLog(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的日志ID").ToJSON(c)
	}

	opLog, err := logService.Get(c.Context(), id)
	if err != nil {
		if err == service.ErrOperationLogNotFound {
			return resp.Fail(global.ErrCodeOperationLogNotFound, "操作日志不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取操作日志失败").ToJSON(c)
	}

	return resp.Success(opLog, global.ResponseSuccessCode, "获取操作日志成功").ToJSON(c)
}

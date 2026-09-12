package handler

import (
	"context"
	"strconv"
	"time"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/jwt"
	"github.com/wuwuseo/cmf/validate"
)

// logOperation 记录操作日志的辅助函数
// extraRedactKeys 用于指定额外需要脱敏的字段名（如配置项的 value 字段）
//
// 状态码取自当前响应的真实状态码；耗时取自路由链上注入的 audit_started_at
// （router.go 在 adminAuthBase 的 locals 中间件中写入），两者皆缺省时保持旧默认。
func logOperation(c fiber.Ctx, logService service.OperationLogServiceInterface, module, action, description string, extraRedactKeys ...string) {
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
		RequestBody: utils.SafeGetBody(c, extraRedactKeys...),
		Method:      c.Method(),
		Path:        c.Path(),
		IP:          c.IP(),
		UserAgent:   c.Get("User-Agent"),
		StatusCode:  operationLogStatus(c),
		DurationMs:  operationLogDurationMs(c),
	})
}

// operationLogStatus 返回当前响应的真实状态码；响应尚未写出时回退 200
func operationLogStatus(c fiber.Ctx) int {
	if code := c.Response().StatusCode(); code > 0 {
		return code
	}
	return 200
}

// operationLogDurationMs 返回请求开始到当前的毫秒数；无法取到开始时间时返回 0
func operationLogDurationMs(c fiber.Ctx) int64 {
	if start, ok := c.Locals("audit_started_at").(time.Time); ok {
		return time.Since(start).Milliseconds()
	}
	return 0
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
	dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)

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

	op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c), getOperatorDomain(c))
	if err != nil {
		return mapOperatorContextError(c, resp, err, "获取操作日志列表失败")
	}

	logs, err := logService.List(c.Context(), op, req)
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
	dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的日志ID").ToJSON(c)
	}

	op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c), getOperatorDomain(c))
	if err != nil {
		return mapOperatorContextError(c, resp, err, "获取操作日志失败")
	}

	opLog, err := logService.Get(c.Context(), op, id)
	if err != nil {
		if err == service.ErrOperationLogNotFound {
			return resp.Fail(global.ErrCodeOperationLogNotFound, "操作日志不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取操作日志失败").ToJSON(c)
	}

	return resp.Success(opLog, global.ResponseSuccessCode, "获取操作日志成功").ToJSON(c)
}

package handler

import (
	"errors"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	cmfqueue "github.com/wuwuseo/cmf/queue"
	"github.com/wuwuseo/cmf/validate"
)

// queueService 从请求上下文取队列管理服务
func queueService(c fiber.Ctx) (*service.QueueJobService, error) {
	svc, ok := c.Locals("service.QueueJobService").(*service.QueueJobService)
	if !ok || svc == nil {
		return nil, service.ErrQueueUnavailable
	}
	return svc, nil
}

// respondQueueError 将队列相关错误映射为响应并直接发送 JSON。
// 必须返回 ToJSON 的结果（通常为 nil）；返回 typed-nil（如 *fiber.Error(nil)）
// 会让 Fiber 的 ErrorHandler 在 errors.As 解引用时 panic，且发生在
// recover 中间件保护范围之外，导致整个进程崩溃。
func respondQueueError(resp *utils.Response, c fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, service.ErrQueueUnavailable):
		return resp.Fail(global.ErrCodeQueueUnavailable, "任务队列不可用").ToJSON(c)
	case errors.Is(err, service.ErrQueueUnknownHandler):
		return resp.Fail(global.ErrCodeQueueUnknownHandler, "队列任务未注册").ToJSON(c)
	case errors.Is(err, service.ErrQueueInvalidPayload):
		return resp.Fail(global.ErrCodeQueueInvalidPayload, "任务载荷必须是合法 JSON").ToJSON(c)
	case errors.Is(err, service.ErrQueueUnknownQueue):
		return resp.Fail(global.ErrCodeQueueUnknownQueue, "目标队列未配置").ToJSON(c)
	case errors.Is(err, service.ErrQueueInvalidQueueName):
		return resp.Fail(global.ErrCodeQueueInvalidName, "队列名仅允许字母、数字、下划线和中划线").ToJSON(c)
	case errors.Is(err, cmfqueue.ErrQueueFull):
		return resp.Fail(global.ErrCodeQueueFull, "队列已达到容量阈值，请稍后重试").ToJSON(c)
	case errors.Is(err, cmfqueue.ErrPayloadTooLarge):
		return resp.Fail(global.ErrCodeQueuePayloadTooLarge, "任务载荷超过字节上限").ToJSON(c)
	case errors.Is(err, cmfqueue.ErrTaskNotFound):
		return resp.Fail(global.ErrCodeQueueTaskNotFound, "队列任务不存在").ToJSON(c)
	case errors.Is(err, cmfqueue.ErrQueueNotFound):
		return resp.Fail(global.ErrCodeQueueTaskNotFound, "队列不存在").ToJSON(c)
	case errors.Is(err, cmfqueue.ErrDuplicateTask):
		return resp.Fail(global.ErrCodeQueueDuplicateTask, "任务重复入队（去重窗口内）").ToJSON(c)
	case errors.Is(err, cmfqueue.ErrTaskStateConflict):
		return resp.Fail(global.ErrCodeQueueStateConflict, "任务状态不允许该操作").ToJSON(c)
	case errors.Is(err, cmfqueue.ErrInvalidTaskState):
		return resp.Fail(global.ErrCodeQueueInvalidState, "非法的任务状态").ToJSON(c)
	default:
		return resp.Fail(global.ResponseInternalServerError, "任务队列操作失败").ToJSON(c)
	}
}

// ListQueueTasks 获取队列任务列表
// @Summary 获取队列任务列表
// @Description 按状态分页列出队列任务
// @Tags 任务队列管理
// @Produce json
// @Security BearerAuth
// @Param state query string true "任务状态(pending/active/scheduled/retry/archived/completed)"
// @Param queue query string false "队列名，默认 default"
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Success 200 {object} utils.Response{data=service.ListResponse[*service.QueueTaskResponse]}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/queue-jobs [get]
func ListQueueTasks(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc, err := queueService(c)
	if err != nil {
		return respondQueueError(resp, c, err)
	}

	req, err := validate.ParseQueryAndValidate[service.ListQueueTasksRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	// 分页默认值（与既有列表接口保持一致）
	if req.Page < 1 {
		req.Page = 1
	}
	if req.PageSize < 1 {
		req.PageSize = 10
	}

	result, err := svc.ListTasks(c.Context(), req)
	if err != nil {
		return respondQueueError(resp, c, err)
	}
	return resp.Success(result, global.ResponseSuccessCode, "查询成功").ToJSON(c)
}

// GetQueueTask 获取队列任务详情
// @Summary 获取队列任务详情
// @Description 按队列名和任务 ID 查询任务详情
// @Tags 任务队列管理
// @Produce json
// @Security BearerAuth
// @Param queue path string true "队列名"
// @Param id path string true "任务 ID"
// @Success 200 {object} utils.Response{data=service.QueueTaskResponse}
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/queue-jobs/{queue}/{id} [get]
func GetQueueTask(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc, err := queueService(c)
	if err != nil {
		return respondQueueError(resp, c, err)
	}

	task, err := svc.GetTask(c.Context(), c.Params("queue"), c.Params("id"))
	if err != nil {
		return respondQueueError(resp, c, err)
	}
	return resp.Success(task, global.ResponseSuccessCode, "查询成功").ToJSON(c)
}

// EnqueueQueueTask 手动入队一个任务
// @Summary 手动入队任务
// @Description 手动向队列投递一个已注册的任务
// @Tags 任务队列管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body service.EnqueueTaskRequest true "任务信息"
// @Success 200 {object} utils.Response{data=service.EnqueueTaskResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/queue-jobs [post]
func EnqueueQueueTask(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc, err := queueService(c)
	if err != nil {
		return respondQueueError(resp, c, err)
	}

	req, err := validate.ParseAndValidate[service.EnqueueTaskRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	result, err := svc.Enqueue(c.Context(), req)
	if err != nil {
		return respondQueueError(resp, c, err)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "queue", "入队", "手动入队任务: "+req.Name+"，任务 ID: "+result.TaskID)

	return resp.Success(result, global.ResponseSuccessCode, "任务已入队").ToJSON(c)
}

// RetryQueueTask 立即执行一个 scheduled/retry/archived 状态的任务
// @Summary 重试队列任务
// @Description 立即执行一个处于 scheduled/retry/archived 状态的任务
// @Tags 任务队列管理
// @Produce json
// @Security BearerAuth
// @Param queue path string true "队列名"
// @Param id path string true "任务 ID"
// @Success 200 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/queue-jobs/{queue}/{id}/retry [post]
func RetryQueueTask(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc, err := queueService(c)
	if err != nil {
		return respondQueueError(resp, c, err)
	}

	queueName, taskID := c.Params("queue"), c.Params("id")
	if err := svc.RetryTask(c.Context(), queueName, taskID); err != nil {
		return respondQueueError(resp, c, err)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "queue", "重试", "重试队列任务: "+taskID)

	return resp.Success(nil, global.ResponseSuccessActionCode, "任务已重新入队").ToJSON(c)
}

// CancelQueueTask 取消执行中的任务
// @Summary 取消队列任务
// @Description 向执行中的任务发出取消信号
// @Tags 任务队列管理
// @Produce json
// @Security BearerAuth
// @Param queue path string true "队列名"
// @Param id path string true "任务 ID"
// @Success 200 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/queue-jobs/{queue}/{id}/cancel [post]
func CancelQueueTask(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc, err := queueService(c)
	if err != nil {
		return respondQueueError(resp, c, err)
	}

	taskID := c.Params("id")
	if err := svc.CancelTask(c.Context(), taskID); err != nil {
		return respondQueueError(resp, c, err)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "queue", "取消", "取消队列任务: "+taskID)

	return resp.Success(nil, global.ResponseSuccessActionCode, "已发出取消信号").ToJSON(c)
}

// DeleteQueueTask 删除队列任务
// @Summary 删除队列任务
// @Description 从队列中删除一个任务（执行中的任务不可删除）
// @Tags 任务队列管理
// @Produce json
// @Security BearerAuth
// @Param queue path string true "队列名"
// @Param id path string true "任务 ID"
// @Success 200 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/queue-jobs/{queue}/{id} [delete]
func DeleteQueueTask(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc, err := queueService(c)
	if err != nil {
		return respondQueueError(resp, c, err)
	}

	queueName, taskID := c.Params("queue"), c.Params("id")
	if err := svc.DeleteTask(c.Context(), queueName, taskID); err != nil {
		return respondQueueError(resp, c, err)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "queue", "删除", "删除队列任务: "+taskID)

	return resp.Success(nil, global.ResponseSuccessActionCode, "任务已删除").ToJSON(c)
}

// QueueStats 获取队列统计
// @Summary 获取队列统计
// @Description 返回所有队列的任务状态统计快照
// @Tags 任务队列管理
// @Produce json
// @Security BearerAuth
// @Success 200 {object} utils.Response{data=[]service.QueueStatsResponse}
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/queue-jobs/stats [get]
func QueueStats(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc, err := queueService(c)
	if err != nil {
		return respondQueueError(resp, c, err)
	}

	stats, err := svc.Stats(c.Context())
	if err != nil {
		return respondQueueError(resp, c, err)
	}
	return resp.Success(stats, global.ResponseSuccessCode, "查询成功").ToJSON(c)
}

// QueueHandlers 获取已注册的队列任务处理器
// @Summary 获取已注册任务处理器
// @Description 返回当前进程注册的全部队列任务处理器名，可作为手动入队的 name 候选
// @Tags 任务队列管理
// @Produce json
// @Security BearerAuth
// @Success 200 {object} utils.Response{data=[]string}
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/queue-jobs/handlers [get]
func QueueHandlers(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc, err := queueService(c)
	if err != nil {
		return respondQueueError(resp, c, err)
	}

	handlers, err := svc.Handlers(c.Context())
	if err != nil {
		return respondQueueError(resp, c, err)
	}
	return resp.Success(handlers, global.ResponseSuccessCode, "查询成功").ToJSON(c)
}

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

// CreateCronJob 创建定时任务
// @Summary 创建定时任务
// @Description 创建新的定时任务
// @Tags 定时任务管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body service.CreateCronJobRequest true "定时任务信息"
// @Success 200 {object} utils.Response{data=service.CronJobResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs [post]
func CreateCronJob(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	req, err := validate.ParseAndValidate[service.CreateCronJobRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	job, err := cronJobService.Create(c.Context(), req)
	if err != nil {
		switch err {
		case service.ErrCronJobNameExists:
			return resp.Fail(global.ErrCodeCronJobNameExists, "定时任务名称已存在").ToJSON(c)
		case service.ErrCronJobInvalidExpr:
			return resp.Fail(global.ErrCodeCronJobInvalidExpr, "无效的cron表达式").ToJSON(c)
		case service.ErrCronJobInvalidType:
			return resp.Fail(global.ErrCodeCronJobInvalidType, "无效的任务类型").ToJSON(c)
		case service.ErrCronJobTaskNotFound:
			return resp.Fail(global.ErrCodeCronJobTaskNotFound, "Go task未注册").ToJSON(c)
		default:
			return resp.Fail(global.ResponseInternalServerError, "创建定时任务失败").ToJSON(c)
		}
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "cron", "创建", "创建定时任务: "+job.Name)

	return resp.Success(job, global.ResponseSuccessCode, "定时任务创建成功").ToJSON(c)
}

// ListCronJobs 获取定时任务列表
// @Summary 获取定时任务列表
// @Description 获取定时任务列表，支持分页和关键字筛选
// @Tags 定时任务管理
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Param keyword query string false "关键字"
// @Param type query string false "任务类型(shell/curl/task)"
// @Param enabled query bool false "是否启用"
// @Success 200 {object} utils.Response{data=service.ListCronJobsResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs [get]
func ListCronJobs(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	req, err := validate.ParseQueryAndValidate[service.ListCronJobsRequest](c)
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

	result, err := cronJobService.List(c.Context(), req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取定时任务列表失败").ToJSON(c)
	}

	return resp.Success(result, global.ResponseSuccessCode, "获取定时任务列表成功").ToJSON(c)
}

// GetCronJob 获取定时任务详情
// @Summary 获取定时任务详情
// @Description 根据ID获取定时任务详细信息
// @Tags 定时任务管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "定时任务ID"
// @Success 200 {object} utils.Response{data=service.CronJobResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id} [get]
func GetCronJob(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的任务ID").ToJSON(c)
	}

	job, err := cronJobService.Get(c.Context(), id)
	if err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "定时任务不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取定时任务失败").ToJSON(c)
	}

	return resp.Success(job, global.ResponseSuccessCode, "获取定时任务成功").ToJSON(c)
}

// UpdateCronJob 更新定时任务
// @Summary 更新定时任务
// @Description 根据ID更新定时任务信息
// @Tags 定时任务管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "定时任务ID"
// @Param body body service.UpdateCronJobRequest true "更新信息"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id} [put]
func UpdateCronJob(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的任务ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.UpdateCronJobRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if err := cronJobService.Update(c.Context(), id, req); err != nil {
		switch err {
		case service.ErrCronJobNotFound:
			return resp.Fail(global.ErrCodeCronJobNotFound, "定时任务不存在").ToJSON(c)
		case service.ErrCronJobNameExists:
			return resp.Fail(global.ErrCodeCronJobNameExists, "定时任务名称已存在").ToJSON(c)
		case service.ErrCronJobInvalidExpr:
			return resp.Fail(global.ErrCodeCronJobInvalidExpr, "无效的cron表达式").ToJSON(c)
		case service.ErrCronJobTaskNotFound:
			return resp.Fail(global.ErrCodeCronJobTaskNotFound, "Go task未注册").ToJSON(c)
		default:
			return resp.Fail(global.ResponseInternalServerError, "更新定时任务失败").ToJSON(c)
		}
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "cron", "更新", fmt.Sprintf("更新定时任务 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "定时任务更新成功").ToJSON(c)
}

// DeleteCronJob 删除定时任务
// @Summary 删除定时任务
// @Description 根据ID删除定时任务
// @Tags 定时任务管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "定时任务ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id} [delete]
func DeleteCronJob(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的任务ID").ToJSON(c)
	}

	if err := cronJobService.Delete(c.Context(), id); err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "定时任务不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "删除定时任务失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "cron", "删除", fmt.Sprintf("删除定时任务 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "定时任务删除成功").ToJSON(c)
}

// EnableCronJob 启用定时任务
// @Summary 启用定时任务
// @Description 根据ID启用定时任务
// @Tags 定时任务管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "定时任务ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id}/enable [post]
func EnableCronJob(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的任务ID").ToJSON(c)
	}

	if err := cronJobService.Enable(c.Context(), id); err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "定时任务不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "启用定时任务失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "cron", "启用", fmt.Sprintf("启用定时任务 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "定时任务启用成功").ToJSON(c)
}

// DisableCronJob 禁用定时任务
// @Summary 禁用定时任务
// @Description 根据ID禁用定时任务
// @Tags 定时任务管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "定时任务ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id}/disable [post]
func DisableCronJob(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的任务ID").ToJSON(c)
	}

	if err := cronJobService.Disable(c.Context(), id); err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "定时任务不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "禁用定时任务失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "cron", "禁用", fmt.Sprintf("禁用定时任务 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "定时任务禁用成功").ToJSON(c)
}

// RunCronJob 立即执行定时任务
// @Summary 立即执行定时任务
// @Description 手动触发一次定时任务执行
// @Tags 定时任务管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "定时任务ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id}/run [post]
func RunCronJob(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的任务ID").ToJSON(c)
	}

	if err := cronJobService.RunNow(c.Context(), id); err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "定时任务不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "执行定时任务失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "cron", "执行", fmt.Sprintf("手动执行定时任务 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "定时任务已触发执行").ToJSON(c)
}

// GetCronJobLogs 获取定时任务日志列表
// @Summary 获取定时任务日志列表
// @Description 获取指定定时任务的执行日志文件列表
// @Tags 定时任务管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "定时任务ID"
// @Success 200 {object} utils.Response{data=[]cron.LogFileInfo}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id}/logs [get]
func GetCronJobLogs(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的任务ID").ToJSON(c)
	}

	logs, err := cronJobService.GetLogs(c.Context(), id)
	if err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "定时任务不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取日志列表失败").ToJSON(c)
	}

	return resp.Success(logs, global.ResponseSuccessCode, "获取日志列表成功").ToJSON(c)
}

// GetCronJobLogContent 获取指定日志文件内容
// @Summary 获取日志文件内容
// @Description 获取指定定时任务的指定日志文件内容
// @Tags 定时任务管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "定时任务ID"
// @Param filename path string true "日志文件名"
// @Success 200 {object} utils.Response{data=string}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id}/logs/{filename} [get]
func GetCronJobLogContent(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的任务ID").ToJSON(c)
	}

	filename := c.Params("filename")
	if filename == "" {
		return resp.Fail(global.ResponseErrorCode, "文件名不能为空").ToJSON(c)
	}

	content, err := cronJobService.GetLogContent(c.Context(), id, filename)
	if err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "定时任务不存在").ToJSON(c)
		}
		return resp.Fail(global.ErrCodeCronJobLogNotFound, "获取日志内容失败").ToJSON(c)
	}

	return resp.Success(content, global.ResponseSuccessCode, "获取日志内容成功").ToJSON(c)
}

// TailCronJobLog 读取最新日志尾部
// @Summary 读取最新日志尾部
// @Description 读取指定定时任务最新日志文件的最后N行
// @Tags 定时任务管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "定时任务ID"
// @Param lines query int false "读取行数，默认100"
// @Success 200 {object} utils.Response{data=string}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id}/logs/tail [get]
func TailCronJobLog(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的任务ID").ToJSON(c)
	}

	lines := c.QueryInt("lines", 100)

	content, err := cronJobService.TailLog(c.Context(), id, lines)
	if err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "定时任务不存在").ToJSON(c)
		}
		return resp.Fail(global.ErrCodeCronJobLogNotFound, "获取日志失败").ToJSON(c)
	}

	return resp.Success(content, global.ResponseSuccessCode, "获取日志成功").ToJSON(c)
}

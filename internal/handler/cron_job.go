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

// CreateCronJob 鍒涘缓瀹氭椂浠诲姟
// @Summary 鍒涘缓瀹氭椂浠诲姟
// @Description 鍒涘缓鏂扮殑瀹氭椂浠诲姟
// @Tags 瀹氭椂浠诲姟绠＄悊
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body service.CreateCronJobRequest true "瀹氭椂浠诲姟淇℃伅"
// @Success 200 {object} utils.Response{data=service.CronJobResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs [post]
func CreateCronJob(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	req, err := validate.ParseAndValidate[service.CreateCronJobRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "璇锋眰鍙傛暟鏃犳晥").ToJSON(c)
	}

	job, err := cronJobService.Create(c.Context(), req)
	if err != nil {
		switch err {
		case service.ErrCronJobNameExists:
			return resp.Fail(global.ErrCodeCronJobNameExists, "瀹氭椂浠诲姟鍚嶇О宸插瓨鍦?).ToJSON(c)
		case service.ErrCronJobInvalidExpr:
			return resp.Fail(global.ErrCodeCronJobInvalidExpr, "鏃犳晥鐨刢ron琛ㄨ揪寮?).ToJSON(c)
		case service.ErrCronJobInvalidType:
			return resp.Fail(global.ErrCodeCronJobInvalidType, "鏃犳晥鐨勪换鍔＄被鍨?).ToJSON(c)
		case service.ErrCronJobTaskNotFound:
			return resp.Fail(global.ErrCodeCronJobTaskNotFound, "Go task鏈敞鍐?).ToJSON(c)
		case service.ErrCronJobShellNotAllowed:
			return resp.Fail(global.ErrCodeCronJobTaskNotFound, "Shell鍛戒护鏈敞鍐岋紝闇€鍦ㄤ唬鐮佷腑閫氳繃 cron.Register() 娉ㄥ唽").ToJSON(c)
		default:
			return resp.Fail(global.ResponseInternalServerError, "鍒涘缓瀹氭椂浠诲姟澶辫触").ToJSON(c)
		}
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "cron", "鍒涘缓", "鍒涘缓瀹氭椂浠诲姟: "+job.Name)

	return resp.Success(job, global.ResponseSuccessCode, "瀹氭椂浠诲姟鍒涘缓鎴愬姛").ToJSON(c)
}

// ListCronJobs 鑾峰彇瀹氭椂浠诲姟鍒楄〃
// @Summary 鑾峰彇瀹氭椂浠诲姟鍒楄〃
// @Description 鑾峰彇瀹氭椂浠诲姟鍒楄〃锛屾敮鎸佸垎椤靛拰鍏抽敭瀛楃瓫閫?// @Tags 瀹氭椂浠诲姟绠＄悊
// @Produce json
// @Security BearerAuth
// @Param page query int false "椤电爜锛岄粯璁?"
// @Param page_size query int false "姣忛〉鏁伴噺锛岄粯璁?0"
// @Param keyword query string false "鍏抽敭瀛?
// @Param type query string false "浠诲姟绫诲瀷(shell/curl/task)"
// @Param enabled query bool false "鏄惁鍚敤"
// @Success 200 {object} utils.Response{data=service.ListCronJobsResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs [get]
func ListCronJobs(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	req, err := validate.ParseQueryAndValidate[service.ListCronJobsRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "璇锋眰鍙傛暟鏃犳晥").ToJSON(c)
	}

	if req.Page == 0 {
		req.Page = 1
	}
	if req.PageSize == 0 {
		req.PageSize = 10
	}

	result, err := cronJobService.List(c.Context(), req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "鑾峰彇瀹氭椂浠诲姟鍒楄〃澶辫触").ToJSON(c)
	}

	return resp.Success(result, global.ResponseSuccessCode, "鑾峰彇瀹氭椂浠诲姟鍒楄〃鎴愬姛").ToJSON(c)
}

// GetCronJob 鑾峰彇瀹氭椂浠诲姟璇︽儏
// @Summary 鑾峰彇瀹氭椂浠诲姟璇︽儏
// @Description 鏍规嵁ID鑾峰彇瀹氭椂浠诲姟璇︾粏淇℃伅
// @Tags 瀹氭椂浠诲姟绠＄悊
// @Produce json
// @Security BearerAuth
// @Param id path int true "瀹氭椂浠诲姟ID"
// @Success 200 {object} utils.Response{data=service.CronJobResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id} [get]
func GetCronJob(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "鏃犳晥鐨勪换鍔D").ToJSON(c)
	}

	job, err := cronJobService.Get(c.Context(), id)
	if err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "瀹氭椂浠诲姟涓嶅瓨鍦?).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "鑾峰彇瀹氭椂浠诲姟澶辫触").ToJSON(c)
	}

	return resp.Success(job, global.ResponseSuccessCode, "鑾峰彇瀹氭椂浠诲姟鎴愬姛").ToJSON(c)
}

// UpdateCronJob 鏇存柊瀹氭椂浠诲姟
// @Summary 鏇存柊瀹氭椂浠诲姟
// @Description 鏍规嵁ID鏇存柊瀹氭椂浠诲姟淇℃伅
// @Tags 瀹氭椂浠诲姟绠＄悊
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "瀹氭椂浠诲姟ID"
// @Param body body service.UpdateCronJobRequest true "鏇存柊淇℃伅"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id} [put]
func UpdateCronJob(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "鏃犳晥鐨勪换鍔D").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.UpdateCronJobRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "璇锋眰鍙傛暟鏃犳晥").ToJSON(c)
	}

	if err := cronJobService.Update(c.Context(), id, req); err != nil {
		switch err {
		case service.ErrCronJobNotFound:
			return resp.Fail(global.ErrCodeCronJobNotFound, "瀹氭椂浠诲姟涓嶅瓨鍦?).ToJSON(c)
		case service.ErrCronJobNameExists:
			return resp.Fail(global.ErrCodeCronJobNameExists, "瀹氭椂浠诲姟鍚嶇О宸插瓨鍦?).ToJSON(c)
		case service.ErrCronJobInvalidExpr:
			return resp.Fail(global.ErrCodeCronJobInvalidExpr, "鏃犳晥鐨刢ron琛ㄨ揪寮?).ToJSON(c)
		case service.ErrCronJobTaskNotFound:
			return resp.Fail(global.ErrCodeCronJobTaskNotFound, "Go task鏈敞鍐?).ToJSON(c)
		case service.ErrCronJobShellNotAllowed:
			return resp.Fail(global.ErrCodeCronJobTaskNotFound, "Shell鍛戒护鏈敞鍐岋紝闇€鍦ㄤ唬鐮佷腑閫氳繃 cron.Register() 娉ㄥ唽").ToJSON(c)
		default:
			return resp.Fail(global.ResponseInternalServerError, "鏇存柊瀹氭椂浠诲姟澶辫触").ToJSON(c)
		}
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "cron", "鏇存柊", fmt.Sprintf("鏇存柊瀹氭椂浠诲姟 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "瀹氭椂浠诲姟鏇存柊鎴愬姛").ToJSON(c)
}

// DeleteCronJob 鍒犻櫎瀹氭椂浠诲姟
// @Summary 鍒犻櫎瀹氭椂浠诲姟
// @Description 鏍规嵁ID鍒犻櫎瀹氭椂浠诲姟
// @Tags 瀹氭椂浠诲姟绠＄悊
// @Produce json
// @Security BearerAuth
// @Param id path int true "瀹氭椂浠诲姟ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id} [delete]
func DeleteCronJob(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "鏃犳晥鐨勪换鍔D").ToJSON(c)
	}

	if err := cronJobService.Delete(c.Context(), id); err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "瀹氭椂浠诲姟涓嶅瓨鍦?).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "鍒犻櫎瀹氭椂浠诲姟澶辫触").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "cron", "鍒犻櫎", fmt.Sprintf("鍒犻櫎瀹氭椂浠诲姟 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "瀹氭椂浠诲姟鍒犻櫎鎴愬姛").ToJSON(c)
}

// EnableCronJob 鍚敤瀹氭椂浠诲姟
// @Summary 鍚敤瀹氭椂浠诲姟
// @Description 鏍规嵁ID鍚敤瀹氭椂浠诲姟
// @Tags 瀹氭椂浠诲姟绠＄悊
// @Produce json
// @Security BearerAuth
// @Param id path int true "瀹氭椂浠诲姟ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id}/enable [post]
func EnableCronJob(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "鏃犳晥鐨勪换鍔D").ToJSON(c)
	}

	if err := cronJobService.Enable(c.Context(), id); err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "瀹氭椂浠诲姟涓嶅瓨鍦?).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "鍚敤瀹氭椂浠诲姟澶辫触").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "cron", "鍚敤", fmt.Sprintf("鍚敤瀹氭椂浠诲姟 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "瀹氭椂浠诲姟鍚敤鎴愬姛").ToJSON(c)
}

// DisableCronJob 绂佺敤瀹氭椂浠诲姟
// @Summary 绂佺敤瀹氭椂浠诲姟
// @Description 鏍规嵁ID绂佺敤瀹氭椂浠诲姟
// @Tags 瀹氭椂浠诲姟绠＄悊
// @Produce json
// @Security BearerAuth
// @Param id path int true "瀹氭椂浠诲姟ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id}/disable [post]
func DisableCronJob(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "鏃犳晥鐨勪换鍔D").ToJSON(c)
	}

	if err := cronJobService.Disable(c.Context(), id); err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "瀹氭椂浠诲姟涓嶅瓨鍦?).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "绂佺敤瀹氭椂浠诲姟澶辫触").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "cron", "绂佺敤", fmt.Sprintf("绂佺敤瀹氭椂浠诲姟 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "瀹氭椂浠诲姟绂佺敤鎴愬姛").ToJSON(c)
}

// RunCronJob 绔嬪嵆鎵ц瀹氭椂浠诲姟
// @Summary 绔嬪嵆鎵ц瀹氭椂浠诲姟
// @Description 鎵嬪姩瑙﹀彂涓€娆″畾鏃朵换鍔℃墽琛?// @Tags 瀹氭椂浠诲姟绠＄悊
// @Produce json
// @Security BearerAuth
// @Param id path int true "瀹氭椂浠诲姟ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id}/run [post]
func RunCronJob(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "鏃犳晥鐨勪换鍔D").ToJSON(c)
	}

	if err := cronJobService.RunNow(c.Context(), id); err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "瀹氭椂浠诲姟涓嶅瓨鍦?).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "鎵ц瀹氭椂浠诲姟澶辫触").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "cron", "鎵ц", fmt.Sprintf("鎵嬪姩鎵ц瀹氭椂浠诲姟 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "瀹氭椂浠诲姟宸茶Е鍙戞墽琛?).ToJSON(c)
}

// GetCronJobLogs 鑾峰彇瀹氭椂浠诲姟鏃ュ織鍒楄〃
// @Summary 鑾峰彇瀹氭椂浠诲姟鏃ュ織鍒楄〃
// @Description 鑾峰彇鎸囧畾瀹氭椂浠诲姟鐨勬墽琛屾棩蹇楁枃浠跺垪琛?// @Tags 瀹氭椂浠诲姟绠＄悊
// @Produce json
// @Security BearerAuth
// @Param id path int true "瀹氭椂浠诲姟ID"
// @Success 200 {object} utils.Response{data=[]object}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id}/logs [get]
func GetCronJobLogs(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "鏃犳晥鐨勪换鍔D").ToJSON(c)
	}

	logs, err := cronJobService.GetLogs(c.Context(), id)
	if err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "瀹氭椂浠诲姟涓嶅瓨鍦?).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "鑾峰彇鏃ュ織鍒楄〃澶辫触").ToJSON(c)
	}

	return resp.Success(logs, global.ResponseSuccessCode, "鑾峰彇鏃ュ織鍒楄〃鎴愬姛").ToJSON(c)
}

// GetCronJobLogContent 鑾峰彇鎸囧畾鏃ュ織鏂囦欢鍐呭
// @Summary 鑾峰彇鏃ュ織鏂囦欢鍐呭
// @Description 鑾峰彇鎸囧畾瀹氭椂浠诲姟鐨勬寚瀹氭棩蹇楁枃浠跺唴瀹?// @Tags 瀹氭椂浠诲姟绠＄悊
// @Produce json
// @Security BearerAuth
// @Param id path int true "瀹氭椂浠诲姟ID"
// @Param filename path string true "鏃ュ織鏂囦欢鍚?
// @Success 200 {object} utils.Response{data=string}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id}/logs/{filename} [get]
func GetCronJobLogContent(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "鏃犳晥鐨勪换鍔D").ToJSON(c)
	}

	filename := c.Params("filename")
	if filename == "" {
		return resp.Fail(global.ResponseErrorCode, "鏂囦欢鍚嶄笉鑳戒负绌?).ToJSON(c)
	}

	content, err := cronJobService.GetLogContent(c.Context(), id, filename)
	if err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "瀹氭椂浠诲姟涓嶅瓨鍦?).ToJSON(c)
		}
		return resp.Fail(global.ErrCodeCronJobLogNotFound, "鑾峰彇鏃ュ織鍐呭澶辫触").ToJSON(c)
	}

	return resp.Success(content, global.ResponseSuccessCode, "鑾峰彇鏃ュ織鍐呭鎴愬姛").ToJSON(c)
}

// TailCronJobLog 璇诲彇鏈€鏂版棩蹇楀熬閮?// @Summary 璇诲彇鏈€鏂版棩蹇楀熬閮?// @Description 璇诲彇鎸囧畾瀹氭椂浠诲姟鏈€鏂版棩蹇楁枃浠剁殑鏈€鍚嶯琛?// @Tags 瀹氭椂浠诲姟绠＄悊
// @Produce json
// @Security BearerAuth
// @Param id path int true "瀹氭椂浠诲姟ID"
// @Param lines query int false "璇诲彇琛屾暟锛岄粯璁?00"
// @Success 200 {object} utils.Response{data=string}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/cron-jobs/{id}/logs/tail [get]
func TailCronJobLog(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	cronJobService := c.Locals("service.CronJobService").(*service.CronJobService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "鏃犳晥鐨勪换鍔D").ToJSON(c)
	}

	linesStr := c.Query("lines", "100")
	lines, err := strconv.Atoi(linesStr)
	if err != nil {
		lines = 100
	}

	content, err := cronJobService.TailLog(c.Context(), id, lines)
	if err != nil {
		if err == service.ErrCronJobNotFound {
			return resp.Fail(global.ErrCodeCronJobNotFound, "瀹氭椂浠诲姟涓嶅瓨鍦?).ToJSON(c)
		}
		return resp.Fail(global.ErrCodeCronJobLogNotFound, "鑾峰彇鏃ュ織澶辫触").ToJSON(c)
	}

	return resp.Success(content, global.ResponseSuccessCode, "鑾峰彇鏃ュ織鎴愬姛").ToJSON(c)
}

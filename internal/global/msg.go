package global

// 响应常量
const (
	ResponseSuccessCode         = 1
	ResponseSuccessActionCode   = 2
	ResponseErrorCode           = 0
	ResponseUnauthorized        = 401
	ResponseForbidden           = 403
	ResponseNotFound            = 404
	ResponseMethodNotAllowed    = 405
	ResponseConflict            = 409
	ResponseInternalServerError = 500

	// 角色相关错误码 (2000-2099)
	ErrCodeRoleNotFound        = 2001
	ErrCodeRoleNameExists      = 2002
	ErrCodeRoleInUse           = 2003
	ErrCodeSystemRoleProtected = 2004
	ErrCodeRoleDisabled        = 2005
	ErrCodeInvalidRoleName     = 2006
	ErrCodeInvalidRoleStatus   = 2007

	// 定时任务错误码 (4000-4099)
	ErrCodeCronJobNotFound     = 4001
	ErrCodeCronJobNameExists   = 4002
	ErrCodeCronJobInvalidExpr  = 4003
	ErrCodeCronJobInvalidType  = 4004
	ErrCodeCronJobTaskNotFound = 4005 // Go task 未注册
	ErrCodeCronJobLogNotFound  = 4006

	// 附件管理错误码 (3000-3099)
	ErrCodeAttachmentNotFound      = 3001
	ErrCodeAttachmentGroupNotFound = 3002
	ErrCodeAttachmentGroupNotEmpty = 3003
	ErrCodeAttachmentGroupExists   = 3004
	ErrCodeAttachmentUploadFailed  = 3005
	ErrCodeAttachmentDeleteFailed  = 3006

	// 操作日志错误码 (5000-5099)
	ErrCodeOperationLogNotFound = 5001
)

// ResponseMsg 响应消息映射
var ResponseMsg = map[int]string{
	ResponseSuccessCode:         "成功",
	ResponseSuccessActionCode:   "操作成功",
	ResponseErrorCode:           "失败",
	ResponseUnauthorized:        "未授权",
	ResponseForbidden:           "拒绝访问",
	ResponseNotFound:            "资源不存在",
	ResponseMethodNotAllowed:    "方法不允许",
	ResponseConflict:            "冲突",
	ResponseInternalServerError: "内部服务器错误",

	// 角色相关错误消息
	ErrCodeRoleNotFound:        "角色不存在",
	ErrCodeRoleNameExists:      "角色名称已存在",
	ErrCodeRoleInUse:           "角色正在使用中，无法删除",
	ErrCodeSystemRoleProtected: "系统内置角色受保护",
	ErrCodeRoleDisabled:        "角色已禁用",
	ErrCodeInvalidRoleName:     "角色名称无效",
	ErrCodeInvalidRoleStatus:   "角色状态无效",

	// 定时任务错误消息
	ErrCodeCronJobNotFound:     "定时任务不存在",
	ErrCodeCronJobNameExists:   "定时任务名称已存在",
	ErrCodeCronJobInvalidExpr:  "无效的cron表达式",
	ErrCodeCronJobInvalidType:  "无效的任务类型",
	ErrCodeCronJobTaskNotFound: "Go task未注册",
	ErrCodeCronJobLogNotFound:  "日志不存在",

	// 附件管理错误消息
	ErrCodeAttachmentNotFound:      "附件不存在",
	ErrCodeAttachmentGroupNotFound: "附件分组不存在",
	ErrCodeAttachmentGroupNotEmpty: "附件分组下存在附件，无法删除",
	ErrCodeAttachmentGroupExists:   "附件分组名称已存在",
	ErrCodeAttachmentUploadFailed:  "附件上传失败",
	ErrCodeAttachmentDeleteFailed:  "附件删除失败",

	// 操作日志错误消息
	ErrCodeOperationLogNotFound: "操作日志不存在",
}

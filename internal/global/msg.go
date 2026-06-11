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
	ErrCodeChunkUploadNotFound     = 3007 // 分片上传会话不存在
	ErrCodeChunkUploadInvalidChunk = 3008 // 无效的分片索引
	ErrCodeChunkUploadMergeFailed  = 3009 // 分片合并失败
	ErrCodeFileSizeExceeded        = 3010 // 文件大小超过限制
	ErrCodeChunkSizeExceeded       = 3011 // 单分片大小超过限制
	ErrCodeMimeTypeNotAllowed      = 3012 // MIME类型不允许
	ErrCodeUploadConcurrencyLimit  = 3013 // 上传并发数超限
	ErrCodeChunkUploadExpired      = 3014 // 分片上传会话已过期

	// 操作日志错误码 (5000-5099)
	ErrCodeOperationLogNotFound = 5001

	// 用户管理错误码 (6000-6099)
	ResponseUserNotFound         = 6001
	ResponseUserAlreadyActive    = 6002
	ResponseUserAlreadyInactive  = 6003
	ResponseUserBlacklisted      = 6004
	ResponseUserNotBlacklisted   = 6005
	ResponseCannotBlacklistSelf  = 6006
	ResponseCannotDeactivateSelf = 6007
	ResponseInvalidRoleIDs       = 6008
	ResponsePrimaryRoleInvalid   = 6009
	ResponseThirdPartyBound      = 6010
	ResponseThirdPartyNotFound   = 6011

	// 站内消息错误码 (7000-7099)
	ResponseMessageNotFound          = 7001
	ResponseMessageForbidden         = 7002
	ResponseMessageInvalidTicket     = 7003
	ResponseMessageSSEFailure        = 7004
	ResponseMessageMarkReadFail      = 7005
	ResponseMessageDeleteFail        = 7006
	ResponseMessageCreateFail        = 7007
	ResponseMessageInvalidReq        = 7008
	ResponseMessageBatchTooLarge     = 7009 // 批量操作 ID 超过上限
	ResponseMessageClearNoCriteria   = 7010 // 清空操作未提供条件
	ResponseMessagePreferenceFail    = 7011 // 偏好操作失败
	ResponseMessageBroadcastFail     = 7012 // 广播失败
	ResponseMessageInvalidScope      = 7013 // 无效的目标范围
	ResponseMessageInvalidTimeFormat = 7014 // 免打扰时间格式错误

	// 插件管理错误码 (8000-8099)
	ErrCodePluginNotFound        = 8001
	ErrCodePluginAlreadyInstall  = 8002
	ErrCodePluginPackageInvalid  = 8003
	ErrCodePluginEnableFailed    = 8004
	ErrCodePluginDisableFailed   = 8005
	ErrCodePluginUninstallFail   = 8006
	ErrCodePluginManifestInvalid = 8007
	ErrCodePluginPathInvalid     = 8008

	// 部门管理错误码 (9000-9099)
	ErrCodeDepartmentNotFound      = 9001
	ErrCodeDepartmentHasChildren   = 9002
	ErrCodeDepartmentCycle         = 9003
	ErrCodeInvalidDepartmentParent = 9004

	// 数据权限与系统权限错误码 (9005-9099)
	ResponsePermissionParentCycle   = 9005
	ResponseSelfToggleForbidden     = 9007
	ResponseSelfRoleModifyForbidden = 9008
	// ResponseDepartmentOutOfScope 目标部门不在数据权限范围内
	ResponseDepartmentOutOfScope = 9009
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
	ErrCodeChunkUploadNotFound:     "分片上传会话不存在",
	ErrCodeChunkUploadInvalidChunk: "无效的分片索引",
	ErrCodeChunkUploadMergeFailed:  "分片合并失败",
	ErrCodeFileSizeExceeded:        "文件大小超过限制",
	ErrCodeChunkSizeExceeded:       "单分片大小超过限制",
	ErrCodeMimeTypeNotAllowed:      "不支持的文件类型",
	ErrCodeUploadConcurrencyLimit:  "上传并发数超限，请稍后重试",
	ErrCodeChunkUploadExpired:      "分片上传会话已过期",

	// 操作日志错误消息
	ErrCodeOperationLogNotFound: "操作日志不存在",

	// 用户管理错误消息
	ResponseUserNotFound:         "用户不存在",
	ResponseUserAlreadyActive:    "用户当前已处于激活状态",
	ResponseUserAlreadyInactive:  "用户当前已处于停用状态",
	ResponseUserBlacklisted:      "该用户已被加入黑名单，请先解除黑名单",
	ResponseUserNotBlacklisted:   "该用户不在黑名单中",
	ResponseCannotBlacklistSelf:  "不能将自己加入黑名单",
	ResponseCannotDeactivateSelf: "不能停用自己的账号",
	ResponseInvalidRoleIDs:       "包含无效的角色ID",
	ResponsePrimaryRoleInvalid:   "主角色必须在分配的角色列表内",
	ResponseThirdPartyBound:      "该第三方账号已被其他用户绑定",
	ResponseThirdPartyNotFound:   "第三方用户关联不存在",

	// 站内消息错误消息
	ResponseMessageNotFound:          "消息不存在",
	ResponseMessageForbidden:         "无权操作该消息",
	ResponseMessageInvalidTicket:     "SSE 凭证无效或已过期",
	ResponseMessageSSEFailure:        "SSE 连接失败",
	ResponseMessageMarkReadFail:      "标记已读失败",
	ResponseMessageDeleteFail:        "删除消息失败",
	ResponseMessageCreateFail:        "创建消息失败",
	ResponseMessageInvalidReq:        "非法的消息请求参数",
	ResponseMessageBatchTooLarge:     "批量操作 ID 数量超过上限",
	ResponseMessageClearNoCriteria:   "清空操作必须至少指定一个条件",
	ResponseMessagePreferenceFail:    "消息偏好操作失败",
	ResponseMessageBroadcastFail:     "消息广播失败",
	ResponseMessageInvalidScope:      "无效的广播目标范围",
	ResponseMessageInvalidTimeFormat: "免打扰时间格式应为 HH:mm",

	// 插件管理错误消息
	ErrCodePluginNotFound:        "插件不存在",
	ErrCodePluginAlreadyInstall:  "插件已安装",
	ErrCodePluginPackageInvalid:  "插件包无效",
	ErrCodePluginEnableFailed:    "插件启用失败",
	ErrCodePluginDisableFailed:   "插件停用失败",
	ErrCodePluginUninstallFail:   "插件卸载失败",
	ErrCodePluginManifestInvalid: "插件 Manifest 无效",
	ErrCodePluginPathInvalid:     "插件路径非法",

	// 部门管理错误消息
	ErrCodeDepartmentNotFound:      "部门不存在",
	ErrCodeDepartmentHasChildren:   "部门下存在子部门，无法删除",
	ErrCodeDepartmentCycle:         "父部门不能是自身或子部门",
	ErrCodeInvalidDepartmentParent: "无效的父部门ID",

	// 数据权限与系统权限错误消息
	ResponsePermissionParentCycle:   "不能将权限的父级设为自身或其后代",
	ResponseSelfToggleForbidden:     "不能对自己执行启用/禁用",
	ResponseSelfRoleModifyForbidden: "不能修改自己的角色",
	ResponseDepartmentOutOfScope:    "目标部门不在你的数据权限范围内",
}

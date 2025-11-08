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
}

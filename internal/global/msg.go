package global

// 响应常量
const (
	ResponseSuccessCode         = 200
	ResponseErrorCode           = 400
	ResponseUnauthorized        = 401
	ResponseForbidden           = 403
	ResponseNotFound            = 404
	ResponseMethodNotAllowed    = 405
	ResponseConflict            = 409
	ResponseInternalServerError = 500
)

// ResponseMsg 响应消息映射
var ResponseMsg = map[int]string{
	ResponseSuccessCode:         "操作成功",
	ResponseErrorCode:           "操作失败",
	ResponseUnauthorized:        "未授权",
	ResponseForbidden:           "拒绝访问",
	ResponseNotFound:            "资源不存在",
	ResponseMethodNotAllowed:    "方法不允许",
	ResponseConflict:            "冲突",
	ResponseInternalServerError: "内部服务器错误",
}

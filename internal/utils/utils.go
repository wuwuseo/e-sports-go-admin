package utils

import "go-admin/internal/global"

// GetResponseMsg 获取响应消息
func GetResponseMsg(code int) string {
	return global.ResponseMsg[code]
}

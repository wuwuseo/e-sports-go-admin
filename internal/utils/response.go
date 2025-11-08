package utils

import (
	"go-admin/internal/global"

	"github.com/gofiber/fiber/v2"
)

type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func NewResponse() *Response {
	return &Response{}
}

func (r *Response) Success(options ...any) *Response {
	r.Code = global.ResponseSuccessCode // 默认成功状态码
	r.Message = GetResponseMsg(r.Code)
	if len(options) > 0 {
		r.Data = options[0]

	}
	if len(options) > 1 {
		r.Code = options[1].(int)
	}
	if len(options) > 2 {
		r.Message = options[2].(string)
	}
	return r
}

func (r *Response) Fail(options ...any) *Response {
	r.Code = global.ResponseErrorCode // 默认失败状态码
	r.Message = GetResponseMsg(r.Code)
	if len(options) > 0 {
		r.Code = options[0].(int)
	}
	if len(options) > 1 {
		r.Message = options[1].(string)
	}
	return r
}

func (r *Response) ToJSON(c *fiber.Ctx) error {
	return c.JSON(r)
}

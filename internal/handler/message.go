package handler

import (
	"errors"
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/jwt"
	"github.com/wuwuseo/cmf/validate"
)

// uidFromCtx 从 JWT claims 中取出当前用户 uid
func uidFromCtx(c fiber.Ctx) string {
	claims := jwt.GetJWTUserData(c)
	uid, _ := claims["id"].(string)
	return uid
}

// ListMessages 当前用户消息列表
// @Summary 当前用户消息列表
// @Tags 站内消息
// @Produce json
// @Param page         query int    false "页码"
// @Param page_size    query int    false "每页数量"
// @Param type         query string false "消息类型 system/business/task"
// @Param read_status  query int    false "0=未读 1=已读"
// @Success 200 {object} utils.Response{data=service.ListResponse[service.MessageResponse]}
// @Router /api/v1/admin/auth/messages [get]
func ListMessages(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid := uidFromCtx(c)
	if uid == "" {
		return response.Fail(global.ResponseUnauthorized, "缺少用户身份").ToJSON(c)
	}
	req, err := validate.ParseQueryAndValidate[service.ListMessageRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "请求参数错误").ToJSON(c)
	}
	svc := c.Locals("service.MessageService").(service.MessageServiceInterface)
	result, err := svc.List(c.Context(), uid, req)
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}
	return response.Success(result).ToJSON(c)
}

// MarkRead 标记消息已读
// @Summary 标记消息已读
// @Tags 站内消息
// @Accept json
// @Produce json
// @Param request body service.MarkReadRequest true "ID 列表"
// @Success 200 {object} utils.Response
// @Router /api/v1/admin/auth/messages/read [post]
func MarkRead(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid := uidFromCtx(c)
	if uid == "" {
		return response.Fail(global.ResponseUnauthorized, "缺少用户身份").ToJSON(c)
	}
	req, err := validate.ParseAndValidate[service.MarkReadRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "请求参数错误").ToJSON(c)
	}
	svc := c.Locals("service.MessageService").(service.MessageServiceInterface)
	n, err := svc.MarkRead(c.Context(), uid, req.IDs)
	if err != nil {
		if errors.Is(err, service.ErrMessageForbidden) {
			return response.Fail(global.ResponseMessageForbidden).ToJSON(c)
		}
		return response.Fail(global.ResponseMessageMarkReadFail, err.Error()).ToJSON(c)
	}
	return response.Success(fiber.Map{"affected": n}).ToJSON(c)
}

// MarkAllRead 全部标记为已读
// @Summary 全部标记为已读
// @Tags 站内消息
// @Produce json
// @Success 200 {object} utils.Response
// @Router /api/v1/admin/auth/messages/read-all [post]
func MarkAllRead(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid := uidFromCtx(c)
	if uid == "" {
		return response.Fail(global.ResponseUnauthorized, "缺少用户身份").ToJSON(c)
	}
	svc := c.Locals("service.MessageService").(service.MessageServiceInterface)
	n, err := svc.MarkAllRead(c.Context(), uid)
	if err != nil {
		return response.Fail(global.ResponseMessageMarkReadFail, err.Error()).ToJSON(c)
	}
	return response.Success(fiber.Map{"affected": n}).ToJSON(c)
}

// UnreadCount 获取未读数
// @Summary 获取未读数
// @Tags 站内消息
// @Produce json
// @Success 200 {object} utils.Response{data=service.UnreadCountResponse}
// @Router /api/v1/admin/auth/messages/unread [get]
func UnreadCount(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid := uidFromCtx(c)
	if uid == "" {
		return response.Fail(global.ResponseUnauthorized, "缺少用户身份").ToJSON(c)
	}
	svc := c.Locals("service.MessageService").(service.MessageServiceInterface)
	data, err := svc.UnreadCount(c.Context(), uid)
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}
	return response.Success(data).ToJSON(c)
}

// DeleteMessage 删除单条消息
// @Summary 删除单条消息
// @Tags 站内消息
// @Produce json
// @Param id path int true "消息 ID"
// @Success 200 {object} utils.Response
// @Router /api/v1/admin/auth/messages/{id} [delete]
func DeleteMessage(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid := uidFromCtx(c)
	if uid == "" {
		return response.Fail(global.ResponseUnauthorized, "缺少用户身份").ToJSON(c)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id <= 0 {
		return response.Fail(global.ResponseErrorCode, "ID 参数无效").ToJSON(c)
	}
	svc := c.Locals("service.MessageService").(service.MessageServiceInterface)
	if err := svc.Delete(c.Context(), uid, id); err != nil {
		switch {
		case errors.Is(err, service.ErrMessageNotFound):
			return response.Fail(global.ResponseMessageNotFound).ToJSON(c)
		case errors.Is(err, service.ErrMessageForbidden):
			return response.Fail(global.ResponseMessageForbidden).ToJSON(c)
		default:
			return response.Fail(global.ResponseMessageDeleteFail, err.Error()).ToJSON(c)
		}
	}
	return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
}

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

// domainFromCtx 从 Locals 中取出当前域
func domainFromCtx(c fiber.Ctx) string {
	domain, _ := c.Locals("domain").(string)
	return domain
}

// isSuperAdminFromCtx 判断当前用户是否为超级管理员
func isSuperAdminFromCtx(c fiber.Ctx) bool {
	v, _ := c.Locals("isSuperAdmin").(bool)
	return v
}

// ListMessages 当前用户消息列表
// @Summary 当前用户消息列表
// @Tags 站内消息
// @Produce json
// @Param page         query int    false "页码"
// @Param page_size    query int    false "每页数量"
// @Param type         query string false "消息类型 system/business/task"
// @Param read_status  query int    false "0=未读 1=已读"
// @Param keyword      query string false "按标题/内容模糊搜索"
// @Param level        query string false "消息级别 info/warning/error"
// @Param start_time   query string false "起始时间 RFC3339"
// @Param end_time     query string false "结束时间 RFC3339"
// @Param sort         query string false "排序 created_at_desc/created_at_asc"
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

// GetMessage 获取消息详情
// @Summary 获取消息详情
// @Tags 站内消息
// @Produce json
// @Param id path int true "消息 ID"
// @Success 200 {object} utils.Response{data=service.MessageResponse}
// @Router /api/v1/admin/auth/messages/{id} [get]
func GetMessage(c fiber.Ctx) error {
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
	data, err := svc.Get(c.Context(), uid, id)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrMessageNotFound):
			return response.Fail(global.ResponseMessageNotFound).ToJSON(c)
		case errors.Is(err, service.ErrMessageForbidden):
			return response.Fail(global.ResponseMessageForbidden).ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
		}
	}
	return response.Success(data).ToJSON(c)
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
		if errors.Is(err, service.ErrMessageBatchTooLarge) {
			return response.Fail(global.ResponseMessageBatchTooLarge).ToJSON(c)
		}
		return response.Fail(global.ResponseMessageMarkReadFail, err.Error()).ToJSON(c)
	}
	return response.Success(fiber.Map{"affected": n}).ToJSON(c)
}

// MarkUnread 批量标记为未读（已读回退）
// @Summary 批量标记为未读
// @Tags 站内消息
// @Accept json
// @Produce json
// @Param request body service.MarkUnreadRequest true "ID 列表"
// @Success 200 {object} utils.Response
// @Router /api/v1/admin/auth/messages/unread [post]
func MarkUnread(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid := uidFromCtx(c)
	if uid == "" {
		return response.Fail(global.ResponseUnauthorized, "缺少用户身份").ToJSON(c)
	}
	req, err := validate.ParseAndValidate[service.MarkUnreadRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "请求参数错误").ToJSON(c)
	}
	svc := c.Locals("service.MessageService").(service.MessageServiceInterface)
	n, err := svc.MarkUnread(c.Context(), uid, req.IDs)
	if err != nil {
		if errors.Is(err, service.ErrMessageBatchTooLarge) {
			return response.Fail(global.ResponseMessageBatchTooLarge).ToJSON(c)
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
// @Router /api/v1/admin/auth/messages/unread-count [get]
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

// DeleteBatchMessages 批量删除消息
// @Summary 批量删除消息
// @Tags 站内消息
// @Accept json
// @Produce json
// @Param request body service.DeleteBatchRequest true "ID 列表"
// @Success 200 {object} utils.Response{data=service.DeleteBatchResponse}
// @Router /api/v1/admin/auth/messages/delete-batch [post]
func DeleteBatchMessages(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid := uidFromCtx(c)
	if uid == "" {
		return response.Fail(global.ResponseUnauthorized, "缺少用户身份").ToJSON(c)
	}
	req, err := validate.ParseAndValidate[service.DeleteBatchRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "请求参数错误").ToJSON(c)
	}
	svc := c.Locals("service.MessageService").(service.MessageServiceInterface)
	n, err := svc.DeleteBatch(c.Context(), uid, req.IDs)
	if err != nil {
		if errors.Is(err, service.ErrMessageBatchTooLarge) {
			return response.Fail(global.ResponseMessageBatchTooLarge).ToJSON(c)
		}
		return response.Fail(global.ResponseMessageDeleteFail, err.Error()).ToJSON(c)
	}
	return response.Success(&service.DeleteBatchResponse{Deleted: n}).ToJSON(c)
}

// ClearMessages 条件清空消息
// @Summary 条件清空消息
// @Tags 站内消息
// @Accept json
// @Produce json
// @Param request body service.ClearMessagesRequest true "清空条件（至少一个）"
// @Success 200 {object} utils.Response{data=service.DeleteBatchResponse}
// @Router /api/v1/admin/auth/messages/clear [post]
func ClearMessages(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid := uidFromCtx(c)
	if uid == "" {
		return response.Fail(global.ResponseUnauthorized, "缺少用户身份").ToJSON(c)
	}
	req, err := validate.ParseAndValidate[service.ClearMessagesRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "请求参数错误").ToJSON(c)
	}
	svc := c.Locals("service.MessageService").(service.MessageServiceInterface)
	n, err := svc.Clear(c.Context(), uid, req)
	if err != nil {
		if errors.Is(err, service.ErrMessageClearNoCriteria) {
			return response.Fail(global.ResponseMessageClearNoCriteria).ToJSON(c)
		}
		if errors.Is(err, service.ErrMessageInvalidReq) {
			return response.Fail(global.ResponseMessageInvalidReq, err.Error()).ToJSON(c)
		}
		return response.Fail(global.ResponseMessageDeleteFail, err.Error()).ToJSON(c)
	}
	return response.Success(&service.DeleteBatchResponse{Deleted: n}).ToJSON(c)
}

// GetMessagePreference 获取当前用户消息偏好
// @Summary 获取消息偏好
// @Tags 站内消息
// @Produce json
// @Success 200 {object} utils.Response{data=service.MessagePreferenceResponse}
// @Router /api/v1/admin/auth/messages/preference [get]
func GetMessagePreference(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid := uidFromCtx(c)
	if uid == "" {
		return response.Fail(global.ResponseUnauthorized, "缺少用户身份").ToJSON(c)
	}
	svc := c.Locals("service.MessagePreferenceService").(service.MessagePreferenceServiceInterface)
	data, err := svc.GetOrDefault(c.Context(), uid)
	if err != nil {
		return response.Fail(global.ResponseMessagePreferenceFail, err.Error()).ToJSON(c)
	}
	return response.Success(data).ToJSON(c)
}

// UpdateMessagePreference 更新消息偏好
// @Summary 更新消息偏好
// @Tags 站内消息
// @Accept json
// @Produce json
// @Param request body service.UpdateMessagePreferenceRequest true "偏好字段（全部可选）"
// @Success 200 {object} utils.Response{data=service.MessagePreferenceResponse}
// @Router /api/v1/admin/auth/messages/preference [put]
func UpdateMessagePreference(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid := uidFromCtx(c)
	if uid == "" {
		return response.Fail(global.ResponseUnauthorized, "缺少用户身份").ToJSON(c)
	}
	req, err := validate.ParseAndValidate[service.UpdateMessagePreferenceRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "请求参数错误").ToJSON(c)
	}
	svc := c.Locals("service.MessagePreferenceService").(service.MessagePreferenceServiceInterface)
	data, err := svc.Update(c.Context(), uid, req)
	if err != nil {
		if errors.Is(err, service.ErrMessagePreferenceInvalidTime) {
			return response.Fail(global.ResponseMessageInvalidTimeFormat).ToJSON(c)
		}
		return response.Fail(global.ResponseMessagePreferenceFail, err.Error()).ToJSON(c)
	}
	return response.Success(data).ToJSON(c)
}

// BroadcastMessage 管理员广播消息（需 admin 权限）
// @Summary 管理员广播消息
// @Tags 站内消息
// @Accept json
// @Produce json
// @Param request body service.BroadcastRequest true "广播请求"
// @Success 200 {object} utils.Response{data=service.BroadcastResponse}
// @Router /api/v1/admin/auth/messages/broadcast [post]
func BroadcastMessage(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid := uidFromCtx(c)
	if uid == "" {
		return response.Fail(global.ResponseUnauthorized, "缺少用户身份").ToJSON(c)
	}
	// admin 权限校验
	if !isSuperAdminFromCtx(c) {
		return response.Fail(global.ResponseForbidden, "仅管理员可执行广播操作").ToJSON(c)
	}
	req, err := validate.ParseAndValidate[service.BroadcastRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "请求参数错误").ToJSON(c)
	}
	domain := domainFromCtx(c)
	svc := c.Locals("service.MessageBroadcastService").(service.MessageBroadcastServiceInterface)
	data, err := svc.Broadcast(c.Context(), domain, req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrBroadcastInvalidScope):
			return response.Fail(global.ResponseMessageInvalidScope).ToJSON(c)
		case errors.Is(err, service.ErrBroadcastUsersRequired),
			errors.Is(err, service.ErrBroadcastRolesRequired),
			errors.Is(err, service.ErrBroadcastNoTargetResolved):
			return response.Fail(global.ResponseMessageInvalidReq, err.Error()).ToJSON(c)
		default:
			return response.Fail(global.ResponseMessageBroadcastFail, err.Error()).ToJSON(c)
		}
	}
	return response.Success(data).ToJSON(c)
}

// TestPushMessage 测试推送：仅给当前登录用户发一条 system 消息（调试用）
// @Summary 测试推送
// @Description 调试用：给当前用户发一条测试消息，验证 SSE 链路
// @Tags 站内消息
// @Produce json
// @Success 200 {object} utils.Response{data=service.MessageResponse}
// @Router /api/v1/admin/auth/messages/test-push [post]
func TestPushMessage(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid := uidFromCtx(c)
	if uid == "" {
		return response.Fail(global.ResponseUnauthorized, "缺少用户身份").ToJSON(c)
	}
	svc := c.Locals("service.MessageService").(service.MessageServiceInterface)
	data, err := svc.PublishToUser(c.Context(), uid, service.PublishMessage{
		Type:    service.MessageTypeSystem,
		Level:   service.MessageLevelInfo,
		Title:   "SSE 推送测试",
		Content: "如果你看到这条消息说明 SSE 链路正常工作",
	})
	if err != nil {
		return response.Fail(global.ResponseMessageCreateFail, err.Error()).ToJSON(c)
	}
	return response.Success(data).ToJSON(c)
}

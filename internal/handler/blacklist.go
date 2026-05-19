package handler

import (
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/validate"
)

// AddToBlacklistRequest 加入黑名单请求
type AddToBlacklistRequest struct {
	Reason string `json:"reason" validate:"required"`
}

// AddToBlacklist 将用户加入黑名单
// @Summary 将用户加入黑名单
// @Description 将指定用户加入黑名单，禁止其登录
// @Tags 黑名单
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "用户ID"
// @Param request body AddToBlacklistRequest true "加入黑名单请求"
// @Success 200 {object} utils.Response "加入成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足/不能将自己加入黑名单"
// @Failure 404 {object} utils.Response "用户不存在"
// @Router /api/v1/admin/auth/users/{id}/blacklist [post]
func AddToBlacklist(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	userID, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "无效的用户ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[AddToBlacklistRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	operatorUID := c.Locals("uid").(string)
	operatorName := getOperatorName(c)

	svc := c.Locals("service.BlacklistService").(service.BlacklistServiceInterface)
	err = svc.AddToBlacklist(c.Context(), userID, req.Reason, operatorUID, operatorName)
	if err != nil {
		switch err {
		case service.ErrUserNotFound:
			return response.Fail(global.ResponseUserNotFound, global.ResponseMsg[global.ResponseUserNotFound]).ToJSON(c)
		case service.ErrCannotBlacklistSelf:
			return response.Fail(global.ResponseCannotBlacklistSelf, global.ResponseMsg[global.ResponseCannotBlacklistSelf]).ToJSON(c)
		case service.ErrUserBlacklisted:
			return response.Fail(global.ResponseUserBlacklisted, global.ResponseMsg[global.ResponseUserBlacklisted]).ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "加入黑名单失败").ToJSON(c)
		}
	}

	return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
}

// RemoveFromBlacklist 将用户从黑名单移除
// @Summary 将用户从黑名单移除
// @Description 将指定用户从黑名单移除，恢复登录权限
// @Tags 黑名单
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "用户ID"
// @Success 200 {object} utils.Response "移除成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Failure 404 {object} utils.Response "用户不存在/用户不在黑名单中"
// @Router /api/v1/admin/auth/users/{id}/blacklist [delete]
func RemoveFromBlacklist(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	userID, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "无效的用户ID").ToJSON(c)
	}

	operatorUID := c.Locals("uid").(string)

	svc := c.Locals("service.BlacklistService").(service.BlacklistServiceInterface)
	err = svc.RemoveFromBlacklist(c.Context(), userID, operatorUID)
	if err != nil {
		switch err {
		case service.ErrUserNotFound:
			return response.Fail(global.ResponseUserNotFound, global.ResponseMsg[global.ResponseUserNotFound]).ToJSON(c)
		case service.ErrUserNotBlacklisted:
			return response.Fail(global.ResponseUserNotBlacklisted, global.ResponseMsg[global.ResponseUserNotBlacklisted]).ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "移除黑名单失败").ToJSON(c)
		}
	}

	return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
}

// ListBlacklist 获取黑名单列表
// @Summary 获取黑名单列表
// @Description 获取所有在黑名单中的用户列表
// @Tags 黑名单
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Success 200 {object} utils.Response{data=service.BlacklistResponse} "获取成功"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Router /api/v1/admin/auth/blacklist [get]
func ListBlacklist(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "10"))

	svc := c.Locals("service.BlacklistService").(service.BlacklistServiceInterface)
	result, err := svc.ListBlacklist(c.Context(), page, pageSize)
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, "查询黑名单失败").ToJSON(c)
	}

	return response.Success(result).ToJSON(c)
}

// getOperatorName 从 JWT Claims 获取操作人姓名
func getOperatorName(c fiber.Ctx) string {
	claims := c.Locals("claims")
	if claims == nil {
		return ""
	}
	if m, ok := claims.(map[string]interface{}); ok {
		if name, ok := m["subject"].(string); ok {
			return name
		}
	}
	return ""
}

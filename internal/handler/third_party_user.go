package handler

import (
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// ListThirdPartyUsers 获取第三方用户列表
// @Summary 获取第三方用户列表
// @Description 获取所有第三方绑定用户列表，支持分页和筛选
// @Tags 第三方用户
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Param platform query string false "平台筛选"
// @Param keyword query string false "搜索关键词"
// @Success 200 {object} utils.Response{data=service.ThirdPartyUserListResponse} "获取成功"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Router /api/v1/admin/auth/third-party-users [get]
func ListThirdPartyUsers(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "10"))
	platform := c.Query("platform")
	keyword := c.Query("keyword")

	svc := c.Locals("service.ThirdPartyUserService").(service.ThirdPartyUserServiceInterface)
	result, err := svc.ListThirdPartyUsers(c.Context(), page, pageSize, platform, keyword)
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, "查询第三方用户失败").ToJSON(c)
	}

	return response.Success(result).ToJSON(c)
}

// UnbindThirdPartyUser 解绑第三方账号
// @Summary 解绑第三方账号
// @Description 解绑用户的第三方登录账号
// @Tags 第三方用户
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "记录ID"
// @Success 200 {object} utils.Response "解绑成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Failure 404 {object} utils.Response "记录不存在"
// @Router /api/v1/admin/auth/third-party-users/{id} [delete]
func UnbindThirdPartyUser(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "无效的记录ID").ToJSON(c)
	}

	svc := c.Locals("service.ThirdPartyUserService").(service.ThirdPartyUserServiceInterface)
	err = svc.UnbindThirdPartyUser(c.Context(), id)
	if err != nil {
		switch err {
		case service.ErrThirdPartyNotFound:
			return response.Fail(global.ResponseThirdPartyNotFound, global.ResponseMsg[global.ResponseThirdPartyNotFound]).ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "解绑第三方账号失败").ToJSON(c)
		}
	}

	return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
}

package handler

import (
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// ListThirdPartyUsers 获取第三方用户列表
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

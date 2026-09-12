package middleware

import (
	"context"
	"errors"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/wuwuseo/cmf/jwt"
)

// BlacklistCheck 黑名单校验中间件
// 在JWT认证通过后、PermissionMiddleware之前执行
func BlacklistCheck(blacklistSvc service.BlacklistServiceInterface, accountTypes ...string) fiber.Handler {
	return func(c fiber.Ctx) error {
		claims := jwt.GetJWTUserData(c)
		if claims == nil {
			return c.Next()
		}
		uid, ok := claims["id"].(string)
		if !ok || uid == "" {
			return c.Next()
		}
		if len(accountTypes) > 0 {
			active, err := accountIsActive(c.Context(), uid, accountTypes[0], claims)
			if err != nil {
				response := utils.NewResponse()
				return response.Fail(global.ResponseInternalServerError, "账号状态校验异常").ToJSON(c)
			}
			if !active {
				response := utils.NewResponse()
				return response.Fail(global.ResponseUnauthorized, "账号已停用或不存在").ToJSON(c)
			}
		}

		if blacklistSvc != nil {
			blacklisted, err := blacklistSvc.IsBlacklisted(c.Context(), uid)
			if err != nil {
				response := utils.NewResponse()
				return response.Fail(global.ResponseInternalServerError, "黑名单校验异常").ToJSON(c)
			}
			if blacklisted {
				response := utils.NewResponse()
				return response.Fail(global.ResponseForbidden, "您的账号已被限制访问，请联系管理员").ToJSON(c)
			}

			loggedOut, err := blacklistSvc.IsLoggedOut(c.Context(), uid)
			if err != nil {
				response := utils.NewResponse()
				return response.Fail(global.ResponseInternalServerError, "会话校验异常").ToJSON(c)
			}
			if loggedOut {
				response := utils.NewResponse()
				return response.Fail(global.ResponseUnauthorized, "登录已失效，请重新登录").ToJSON(c)
			}
		}

		return c.Next()
	}
}

func accountIsActive(ctx context.Context, uid, accountType string, claims jwtlib.MapClaims) (bool, error) {
	if global.ORM == nil {
		return false, errors.New("account store unavailable")
	}
	switch accountType {
	case "admin":
		exists, err := global.ORM.Admin.Query().
			Where(admin.UIDEQ(uid), admin.StatusEQ(admin.StatusActive)).
			Exist(ctx)
		return exists, err
	case "user":
		u, err := global.ORM.User.Query().
			Where(user.UIDEQ(uid), user.StatusEQ(user.StatusActive)).
			Only(ctx)
		if ent.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return utils.TokenVersionMatches(claims["token_version"], u.TokenVersion), nil
	default:
		return false, errors.New("unsupported account type")
	}
}

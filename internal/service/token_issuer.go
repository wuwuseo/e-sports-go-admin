package service

import (
	"context"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	"github.com/golang-jwt/jwt/v5"
	cmfConfig "github.com/wuwuseo/cmf/config"
	cmfJwt "github.com/wuwuseo/cmf/jwt"
)

func issueUserToken(ctx context.Context, client *ent.Client, uid string, now time.Time) (*TokenResponse, error) {
	u, err := client.User.Query().Where(user.UIDEQ(uid)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	if u.Status != user.StatusActive {
		return nil, ErrInvalidUserStatus
	}
	configService, _ := global.Bootstrap.GetService("config")
	config := configService.(*cmfConfig.Config)
	accessExpiresAt := now.Add(time.Duration(config.App.LoginExpires) * time.Second)
	refreshExpiresAt := now.Add(time.Duration(config.App.RefreshExpires) * time.Second)
	accessToken, err := cmfJwt.CreateToken(jwt.MapClaims{
		"id": u.UID, "subject": u.Username, "issuer": config.App.Name, "domain": "default",
		"expires": jwt.NewNumericDate(accessExpiresAt), "issued_at": jwt.NewNumericDate(now), "token_type": "access",
	}, config.App.Secret)
	if err != nil {
		return nil, err
	}
	refreshToken, err := cmfJwt.CreateToken(jwt.MapClaims{
		"id": u.UID, "subject": u.Username, "issuer": config.App.Name, "domain": "default",
		"expires": jwt.NewNumericDate(refreshExpiresAt), "issued_at": jwt.NewNumericDate(now), "token_type": "refresh",
	}, config.App.Secret)
	if err != nil {
		return nil, err
	}
	if _, err := client.User.UpdateOne(u).SetLastLogin(now).Save(ctx); err != nil {
		return nil, err
	}
	return &TokenResponse{AccessToken: accessToken, TokenType: "Bearer", ExpiresIn: int64(config.App.LoginExpires), RefreshToken: refreshToken}, nil
}

func issueAdminToken(ctx context.Context, client *ent.Client, uid string, now time.Time) (*TokenResponse, error) {
	a, err := client.Admin.Query().Where(admin.UIDEQ(uid)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrAdminNotFound
		}
		return nil, err
	}
	if a.Status != admin.StatusActive {
		return nil, ErrInvalidAdminStatus
	}
	configService, _ := global.Bootstrap.GetService("config")
	config := configService.(*cmfConfig.Config)
	accessExpiresAt := now.Add(time.Duration(config.App.AdminLoginExpires) * time.Second)
	refreshExpiresAt := now.Add(time.Duration(config.App.AdminRefreshExpires) * time.Second)
	accessToken, err := cmfJwt.CreateToken(jwt.MapClaims{
		"id": a.UID, "subject": a.Username, "issuer": config.App.Name, "domain": "default",
		"expires": jwt.NewNumericDate(accessExpiresAt), "issued_at": jwt.NewNumericDate(now), "token_type": "access",
	}, config.App.AdminSecret)
	if err != nil {
		return nil, err
	}
	refreshToken, err := cmfJwt.CreateToken(jwt.MapClaims{
		"id": a.UID, "subject": a.Username, "issuer": config.App.Name, "domain": "default",
		"expires": jwt.NewNumericDate(refreshExpiresAt), "issued_at": jwt.NewNumericDate(now), "token_type": "refresh",
	}, config.App.AdminSecret)
	if err != nil {
		return nil, err
	}
	if _, err := client.Admin.UpdateOne(a).SetLastLogin(now).Save(ctx); err != nil {
		return nil, err
	}
	return &TokenResponse{AccessToken: accessToken, TokenType: "Bearer", ExpiresIn: int64(config.App.AdminLoginExpires), RefreshToken: refreshToken}, nil
}

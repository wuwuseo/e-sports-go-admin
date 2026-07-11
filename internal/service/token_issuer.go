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

func createTokenPair(id, subject, issuer, secret string, accessTTL, refreshTTL time.Duration, now func() time.Time) (*TokenResponse, error) {
	create := func(tokenType string, ttl time.Duration) (string, error) {
		expiresAt := now().Add(ttl)
		issuedAt := now()
		return cmfJwt.CreateToken(jwt.MapClaims{
			"id": id, "subject": subject, "issuer": issuer, "domain": "default",
			"expires": jwt.NewNumericDate(expiresAt), "issued_at": jwt.NewNumericDate(issuedAt), "token_type": tokenType,
		}, secret)
	}
	accessToken, err := create("access", accessTTL)
	if err != nil {
		return nil, err
	}
	refreshToken, err := create("refresh", refreshTTL)
	if err != nil {
		return nil, err
	}
	return &TokenResponse{
		AccessToken: accessToken, TokenType: "Bearer",
		ExpiresIn: int64(accessTTL / time.Second), RefreshToken: refreshToken,
	}, nil
}

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
	tokens, err := createTokenPair(
		u.UID, u.Username, config.App.Name, config.App.Secret,
		time.Duration(config.App.LoginExpires)*time.Second,
		time.Duration(config.App.RefreshExpires)*time.Second,
		func() time.Time { return now },
	)
	if err != nil {
		return nil, err
	}
	if _, err := client.User.UpdateOne(u).SetLastLogin(now).Save(ctx); err != nil {
		return nil, err
	}
	return tokens, nil
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
	tokens, err := createTokenPair(
		a.UID, a.Username, config.App.Name, config.App.AdminSecret,
		time.Duration(config.App.AdminLoginExpires)*time.Second,
		time.Duration(config.App.AdminRefreshExpires)*time.Second,
		func() time.Time { return now },
	)
	if err != nil {
		return nil, err
	}
	if _, err := client.Admin.UpdateOne(a).SetLastLogin(now).Save(ctx); err != nil {
		return nil, err
	}
	return tokens, nil
}

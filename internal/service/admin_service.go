package service

import (
	"context"
	"errors"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/global"

	"github.com/golang-jwt/jwt/v5"
	cmfConfig "github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/crypto"
	cmfJwt "github.com/wuwuseo/cmf/jwt"
)

var (
	ErrAdminNotFound      = errors.New("admin not found")
	ErrInvalidAdminPassword   = errors.New("invalid password")
	ErrInvalidAdminStatus = errors.New("admin is not active")
)

// AdminServiceInterface 管理员服务接口
type AdminServiceInterface interface {
	Login(ctx context.Context, req *AdminLoginRequest) (*TokenResponse, error)
	RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error)
	GetAdminByUID(ctx context.Context, uid string) (*AdminResponse, error)
	UpdateAdmin(ctx context.Context, uid string, updates map[string]interface{}) error
	ChangePassword(ctx context.Context, uid, oldPassword, newPassword string) error
	Logout(ctx context.Context, uid string) error
}

// AdminService 管理员服务
type AdminService struct {
	client *ent.Client
}

// NewAdminService 创建管理员服务实例
func NewAdminService() *AdminService {
	return &AdminService{
		client: global.ORM,
	}
}

// AdminLoginRequest 管理员登录请求
type AdminLoginRequest struct {
	Email             string `json:"email"`
	Password          string `json:"password"`
	EncryptedEmail    string `json:"encrypted_email"`
	EncryptedPassword string `json:"encrypted_password"`
	Timestamp         int64  `json:"timestamp"`
	Nonce             string `json:"nonce"`
	KeyID             string `json:"key_id"`
}

// Login 管理员登录
func (s *AdminService) Login(ctx context.Context, req *AdminLoginRequest) (*TokenResponse, error) {
	a, err := s.client.Admin.Query().
		Where(admin.EmailEQ(req.Email)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrAdminNotFound
		}
		return nil, err
	}

	if a.Status != admin.StatusActive {
		return nil, ErrInvalidAdminStatus
	}

	match, err := crypto.VerifyPassword(req.Password, a.Password, a.PasswordSalt)
	if err != nil {
		return nil, err
	}
	if !match {
		return nil, ErrInvalidAdminPassword
	}

	cfg, _ := global.Bootstrap.GetService("config")
	config := cfg.(*cmfConfig.Config)

	accessExpiresAt := time.Now().Add(time.Duration(config.App.AdminLoginExpires) * time.Second)
	domain := "default"

	accessClaims := jwt.MapClaims{
		"id":         a.UID,
		"subject":    a.Username,
		"issuer":     config.App.Name,
		"domain":     domain,
		"expires":    jwt.NewNumericDate(accessExpiresAt),
		"issued_at":  jwt.NewNumericDate(time.Now()),
		"token_type": "access",
	}

	accessToken, err := cmfJwt.CreateToken(accessClaims, config.App.AdminSecret)
	if err != nil {
		return nil, err
	}

	refreshExpiresAt := time.Now().Add(time.Duration(config.App.AdminRefreshExpires) * time.Second)
	refreshClaims := jwt.MapClaims{
		"id":         a.UID,
		"subject":    a.Username,
		"issuer":     config.App.Name,
		"domain":     domain,
		"expires":    jwt.NewNumericDate(refreshExpiresAt),
		"issued_at":  jwt.NewNumericDate(time.Now()),
		"token_type": "refresh",
	}

	refreshToken, err := cmfJwt.CreateToken(refreshClaims, config.App.AdminSecret)
	if err != nil {
		return nil, err
	}

	_, err = s.client.Admin.UpdateOne(a).
		SetLastLogin(time.Now()).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	return &TokenResponse{
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    int64(config.App.AdminLoginExpires),
		RefreshToken: refreshToken,
	}, nil
}

// RefreshToken 刷新管理员令牌
func (s *AdminService) RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	cfg, _ := global.Bootstrap.GetService("config")
	config := cfg.(*cmfConfig.Config)

	token, err := jwt.Parse(refreshToken, func(token *jwt.Token) (interface{}, error) {
		return []byte(config.App.AdminSecret), nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid refresh token")
	}

	if claims["token_type"] != "refresh" {
		return nil, errors.New("invalid token type")
	}

	uid, ok := claims["id"].(string)
	if !ok {
		return nil, errors.New("invalid token claims")
	}

	a, err := s.client.Admin.Query().
		Where(admin.UID(uid)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrAdminNotFound
		}
		return nil, err
	}

	if a.Status != admin.StatusActive {
		return nil, ErrInvalidAdminStatus
	}

	accessExpiresAt := time.Now().Add(time.Duration(config.App.AdminLoginExpires) * time.Second)
	domain := "default"

	accessClaims := jwt.MapClaims{
		"id":         a.UID,
		"subject":    a.Username,
		"issuer":     config.App.Name,
		"domain":     domain,
		"expires":    jwt.NewNumericDate(accessExpiresAt),
		"issued_at":  jwt.NewNumericDate(time.Now()),
		"token_type": "access",
	}

	accessToken, err := cmfJwt.CreateToken(accessClaims, config.App.AdminSecret)
	if err != nil {
		return nil, err
	}

	refreshExpiresAt := time.Now().Add(time.Duration(config.App.AdminRefreshExpires) * time.Second)
	refreshClaims := jwt.MapClaims{
		"id":         a.UID,
		"subject":    a.Username,
		"issuer":     config.App.Name,
		"domain":     domain,
		"expires":    jwt.NewNumericDate(refreshExpiresAt),
		"issued_at":  jwt.NewNumericDate(time.Now()),
		"token_type": "refresh",
	}

	newRefreshToken, err := cmfJwt.CreateToken(refreshClaims, config.App.AdminSecret)
	if err != nil {
		return nil, err
	}

	return &TokenResponse{
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    int64(config.App.AdminLoginExpires),
		RefreshToken: newRefreshToken,
	}, nil
}

// AdminResponse 管理员信息响应
type AdminResponse struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	Username   string `json:"username"`
	Nickname   string `json:"nickname"`
	Name       string `json:"name"`
	Phone      string `json:"phone"`
	Avatar     string `json:"avatar"`
	Department string `json:"department"`
	Position   string `json:"position"`
	Status     string `json:"status"`
}

// GetAdminByUID 根据UID获取管理员信息
func (s *AdminService) GetAdminByUID(ctx context.Context, uid string) (*AdminResponse, error) {
	a, err := s.client.Admin.Query().
		Where(admin.UIDEQ(uid)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrAdminNotFound
		}
		return nil, err
	}

	return &AdminResponse{
		ID:         a.UID,
		Email:      a.Email,
		Username:   a.Username,
		Nickname:   a.Nickname,
		Name:       a.Name,
		Phone:      a.Phone,
		Avatar:     a.Avatar,
		Department: a.Department,
		Position:   a.Position,
		Status:     string(a.Status),
	}, nil
}

// UpdateAdmin 更新管理员信息
func (s *AdminService) UpdateAdmin(ctx context.Context, uid string, updates map[string]interface{}) error {
	exists, err := s.client.Admin.Query().
		Where(admin.UIDEQ(uid)).
		Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return ErrAdminNotFound
	}

	update := s.client.Admin.Update().
		Where(admin.UIDEQ(uid))

	for key, value := range updates {
		switch key {
		case "nickname":
			if v, ok := value.(string); ok {
				update.SetNickname(v)
			}
		case "phone":
			if v, ok := value.(string); ok {
				update.SetPhone(v)
			}
		case "avatar":
			if v, ok := value.(string); ok {
				update.SetAvatar(v)
			}
		case "name":
			if v, ok := value.(string); ok {
				update.SetName(v)
			}
		case "department":
			if v, ok := value.(string); ok {
				update.SetDepartment(v)
			}
		case "position":
			if v, ok := value.(string); ok {
				update.SetPosition(v)
			}
		case "status":
			if v, ok := value.(string); ok {
				update.SetStatus(admin.Status(v))
			}
		}
	}

	_, err = update.Save(ctx)
	return err
}

// ChangePassword 修改管理员密码
func (s *AdminService) ChangePassword(ctx context.Context, uid, oldPassword, newPassword string) error {
	a, err := s.client.Admin.Query().
		Where(admin.UIDEQ(uid)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrAdminNotFound
		}
		return err
	}

	match, err := crypto.VerifyPassword(oldPassword, a.Password, a.PasswordSalt)
	if err != nil {
		return err
	}
	if !match {
		return ErrInvalidAdminPassword
	}

	hashedPassword, salt, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}

	_, err = s.client.Admin.UpdateOne(a).
		SetPassword(hashedPassword).
		SetPasswordSalt(salt).
		Save(ctx)
	return err
}

// Logout 管理员退出登录
func (s *AdminService) Logout(ctx context.Context, uid string) error {
	_, err := s.client.Admin.Query().
		Where(admin.UIDEQ(uid)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrAdminNotFound
		}
		return err
	}

	return nil
}

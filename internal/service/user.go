package service

import (
	"context"
	"errors"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	"github.com/golang-jwt/jwt/v5"
	cmfConfig "github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/crypto"
	cmfJwt "github.com/wuwuseo/cmf/jwt"
)

var (
	ErrUserNotFound      = errors.New("user not found")
	ErrInvalidPassword   = errors.New("invalid password")
	ErrEmailExists       = errors.New("email already exists")
	ErrUsernameExists    = errors.New("username already exists")
	ErrInvalidUserStatus = errors.New("user is not active")
)

// UserServiceInterface 定义用户服务接口
type UserServiceInterface interface {
	Login(ctx context.Context, req *LoginRequest) (*TokenResponse, error)
	GetUserByID(ctx context.Context, id string) (*UserResponse, error)
	UpdateUser(ctx context.Context, id string, updates map[string]interface{}) error
	ChangePassword(ctx context.Context, id string, oldPassword, newPassword string) error
}

type UserService struct {
	client *ent.Client
}

func NewUserService() *UserService {
	return &UserService{
		client: global.ORM,
	}
}

// RegisterRequest 用户注册请求
type RegisterRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=6"`
	Username string `json:"username" validate:"required,min=3"`
	Nickname string `json:"nickname"`
	Phone    string `json:"phone" validate:"omitempty,numeric"`
}

// LoginRequest 用户登录请求
type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

// TokenResponse 登录token响应
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// UserResponse 用户信息响应
type UserResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Username  string    `json:"username"`
	Nickname  string    `json:"nickname"`
	Phone     string    `json:"phone"`
	Avatar    string    `json:"avatar"`
	Status    string    `json:"status"`
	LastLogin time.Time `json:"last_login"`
}

// Register 用户注册
func (s *UserService) Register(ctx context.Context, req *RegisterRequest) error {
	// 检查邮箱是否已存在
	exists, err := s.client.User.Query().
		Where(user.EmailEQ(req.Email)).
		Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return ErrEmailExists
	}

	// 检查用户名是否已存在
	if req.Username != "" {
		exists, err = s.client.User.Query().
			Where(user.UsernameEQ(req.Username)).
			Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			return ErrUsernameExists
		}
	}

	// 加密密码
	hashedPassword, salt, err := crypto.HashPassword(req.Password)
	if err != nil {
		return err
	}

	// 创建用户
	_, err = s.client.User.Create().
		SetEmail(req.Email).
		SetPassword(hashedPassword).
		SetPasswordSalt(salt).
		SetUsername(req.Username).
		SetNickname(req.Nickname).
		SetPhone(req.Phone).
		SetStatus(user.StatusInactive). // 需要邮箱验证后才能激活
		Save(ctx)

	return err
}

// Login 用户登录
func (s *UserService) Login(ctx context.Context, req *LoginRequest) (*TokenResponse, error) {
	// 查找用户
	u, err := s.client.User.Query().
		Where(user.EmailEQ(req.Email)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	// 检查用户状态
	if u.Status != user.StatusActive {
		return nil, ErrInvalidUserStatus
	}

	// 验证密码
	match, err := crypto.VerifyPassword(req.Password, u.Password, u.PasswordSalt)
	if err != nil {
		return nil, err
	}
	if !match {
		return nil, ErrInvalidPassword
	}
	config, _ := global.Bootstrap.GetService("config")
	// 生成JWT token
	expiresAt := time.Now().Add(time.Duration(config.(*cmfConfig.Config).App.LoginExpires) * time.Second)
	claims := jwt.MapClaims{
		"id":        u.UID,
		"subject":   u.Username,
		"issuer":    "go-admin",
		"expires":   jwt.NewNumericDate(expiresAt),
		"issued_at": jwt.NewNumericDate(time.Now()),
	}

	token, err := cmfJwt.CreateToken(claims, config.(*cmfConfig.Config).App.Secret)
	if err != nil {
		return nil, err
	}
	// 更新最后登录时间和IP
	_, err = s.client.User.UpdateOne(u).
		SetLastLogin(time.Now()).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	return &TokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int64(config.(*cmfConfig.Config).App.LoginExpires), // 24小时
	}, nil
}

// GetUserByID 根据ID获取用户信息
func (s *UserService) GetUserByID(ctx context.Context, id string) (*UserResponse, error) {
	u, err := s.client.User.Query().
		Where(user.UID(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	return &UserResponse{
		ID:       u.UID,
		Email:    u.Email,
		Username: u.Username,
		Nickname: u.Nickname,
		Phone:    u.Phone,
		Avatar:   u.Avatar,
	}, nil
}

// UpdateUser 更新用户信息
func (s *UserService) UpdateUser(ctx context.Context, id string, updates map[string]interface{}) error {
	// 先检查用户是否存在
	exists, err := s.client.User.Query().
		Where(user.UID(id)).
		Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return ErrUserNotFound
	}

	update := s.client.User.Update().
		Where(user.UID(id))

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
		case "status":
			if v, ok := value.(string); ok {
				update.SetStatus(user.Status(v))
			}
		}
	}

	_, err = update.Save(ctx)
	if err != nil {
		return err
	}
	return nil
}

// ChangePassword 修改密码
func (s *UserService) ChangePassword(ctx context.Context, id string, oldPassword, newPassword string) error {
	// 获取用户信息
	u, err := s.client.User.Query().
		Where(user.UID(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrUserNotFound
		}
		return err
	}

	// 验证旧密码
	match, err := crypto.VerifyPassword(oldPassword, u.Password, u.PasswordSalt)
	if err != nil {
		return err
	}
	if !match {
		return ErrInvalidPassword
	}

	// 加密新密码
	hashedPassword, salt, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}

	// 更新密码
	_, err = s.client.User.UpdateOne(u).
		SetPassword(hashedPassword).
		SetPasswordSalt(salt).
		Save(ctx)

	return err
}

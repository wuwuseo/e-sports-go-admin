package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/eko/gocache/lib/v4/store"
	"github.com/golang-jwt/jwt/v5"
	cmfCache "github.com/wuwuseo/cmf/cache"
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
	RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error)
	GetUserByUID(ctx context.Context, uid string) (*UserResponse, error)
	UpdateUser(ctx context.Context, uid string, updates map[string]interface{}) error
	ChangePassword(ctx context.Context, uid string, oldPassword, newPassword string) error
	Logout(ctx context.Context, uid string) error
	CreateUser(ctx context.Context, req *CreateUserRequest) (*UserResponse, error)
}

type UserService struct {
	client *ent.Client
	cache  *cmfCache.Cache[[]byte]
}

func NewUserService() *UserService {
	return &UserService{
		client: global.ORM,
	}
}

func NewUserServiceWithCache(rawCache *cmfCache.Cache[[]byte]) *UserService {
	return &UserService{
		client: global.ORM,
		cache:  rawCache,
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

// CreateUserRequest 管理员创建用户请求
type CreateUserRequest struct {
	Email         string `json:"email" validate:"required,email"`
	Password      string `json:"password" validate:"required,min=6"`
	Username      string `json:"username" validate:"required,min=3"`
	Nickname      string `json:"nickname"`
	Phone         string `json:"phone"`
	RoleIDs       []int  `json:"role_ids"`
	PrimaryRoleID int    `json:"primary_role_id"`
}

// LoginRequest 用户登录请求
type LoginRequest struct {
	Email             string `json:"email"`
	Password          string `json:"password"`
	EncryptedEmail    string `json:"encrypted_email"`    // 加密的邮箱（用于等保要求）
	EncryptedPassword string `json:"encrypted_password"` // 加密的密码（用于等保要求）
	Timestamp         int64  `json:"timestamp"`          // 请求时间戳（防止重放攻击）
	Nonce             string `json:"nonce"`              // 随机数（防止重放攻击）
	KeyID             string `json:"key_id"`             // 加密密钥ID
}

// TokenResponse 登录token响应
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// RefreshTokenRequest 刷新令牌请求
type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
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
		SetUID(utils.GenerateUID()).
		SetStatus(user.StatusInactive). // 需要邮箱验证后才能激活
		Save(ctx)

	return err
}

// CreateUser 管理员创建用户
func (s *UserService) CreateUser(ctx context.Context, req *CreateUserRequest) (*UserResponse, error) {
	if err := rejectSuperAdminRoleIDs(ctx, s.client, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
		return nil, err
	}

	exists, err := s.client.User.Query().Where(user.EmailEQ(req.Email)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrEmailExists
	}

	if req.Username != "" {
		exists, err = s.client.User.Query().Where(user.UsernameEQ(req.Username)).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, ErrUsernameExists
		}
	}

	hashedPassword, salt, err := crypto.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	create := s.client.User.Create().
		SetEmail(req.Email).
		SetPassword(hashedPassword).
		SetPasswordSalt(salt).
		SetUsername(req.Username).
		SetUID(utils.GenerateUID()).
		SetStatus(user.StatusInactive)
	if req.Nickname != "" {
		create.SetNickname(req.Nickname)
	}
	if req.Phone != "" {
		create.SetPhone(req.Phone)
	}

	u, err := create.Save(ctx)
	if err != nil {
		return nil, err
	}

	return &UserResponse{
		ID:       u.UID,
		Email:    u.Email,
		Username: u.Username,
		Nickname: u.Nickname,
		Phone:    u.Phone,
		Avatar:   u.Avatar,
		Status:   string(u.Status),
	}, nil
}

// Logout 用户退出登录，记录退出时间戳到缓存使当前Token失效
func (s *UserService) Logout(ctx context.Context, uid string) error {
	if s.cache == nil {
		return nil
	}

	cfg, _ := global.Bootstrap.GetService("config")
	ttl := time.Duration(cfg.(*cmfConfig.Config).App.LoginExpires) * time.Second

	key := fmt.Sprintf("logout:uid:%s", uid)
	timestamp := []byte(fmt.Sprintf("%d", time.Now().Unix()))
	return s.cache.Set(ctx, key, timestamp, store.WithExpiration(ttl))
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
	// 默认域 公司代码或者部门代码
	domain := "default"

	// 生成访问令牌
	accessClaims := jwt.MapClaims{
		"id":         u.UID,
		"subject":    u.Username,
		"issuer":     config.(*cmfConfig.Config).App.Name,
		"domain":     domain,
		"expires":    jwt.NewNumericDate(expiresAt),
		"issued_at":  jwt.NewNumericDate(time.Now()),
		"token_type": "access",
	}

	accessToken, err := cmfJwt.CreateToken(accessClaims, config.(*cmfConfig.Config).App.Secret)
	if err != nil {
		return nil, err
	}

	// 生成刷新令牌 - 有效期更长
	refreshExpiresAt := time.Now().Add(time.Duration(config.(*cmfConfig.Config).App.RefreshExpires) * time.Second)
	refreshClaims := jwt.MapClaims{
		"id":         u.UID,
		"subject":    u.Username,
		"issuer":     config.(*cmfConfig.Config).App.Name,
		"domain":     domain,
		"expires":    jwt.NewNumericDate(refreshExpiresAt),
		"issued_at":  jwt.NewNumericDate(time.Now()),
		"token_type": "refresh",
	}

	refreshToken, err := cmfJwt.CreateToken(refreshClaims, config.(*cmfConfig.Config).App.Secret)
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
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    int64(config.(*cmfConfig.Config).App.LoginExpires), // 24小时
		RefreshToken: refreshToken,
	}, nil
}

// GetUserByID 根据ID获取用户信息
func (s *UserService) GetUserByID(ctx context.Context, id int) (*UserResponse, error) {
	u, err := s.client.User.Query().
		Where(user.IDEQ(id)).
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

func (s *UserService) GetUserByUID(ctx context.Context, uid string) (*UserResponse, error) {
	u, err := s.client.User.Query().
		Where(user.UIDEQ(uid)).
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
func (s *UserService) UpdateUser(ctx context.Context, uid string, updates map[string]interface{}) error {
	// 先检查用户是否存在
	exists, err := s.client.User.Query().
		Where(user.UIDEQ(uid)).
		Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return ErrUserNotFound
	}

	update := s.client.User.Update().
		Where(user.UIDEQ(uid))

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

// RefreshToken 刷新访问令牌
func (s *UserService) RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	config, _ := global.Bootstrap.GetService("config")
	secret := config.(*cmfConfig.Config).App.Secret

	// 解析刷新令牌
	token, err := jwt.Parse(refreshToken, func(token *jwt.Token) (interface{}, error) {
		// 验证签名方法
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})

	if err != nil {
		return nil, fmt.Errorf("invalid refresh token: %w", err)
	}

	// 验证令牌有效性
	if !token.Valid {
		return nil, errors.New("invalid refresh token")
	}

	// 获取令牌声明
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("invalid token claims")
	}

	// 检查令牌类型
	tokenType, ok := claims["token_type"].(string)
	if !ok || tokenType != "refresh" {
		return nil, errors.New("invalid token type")
	}

	// 获取用户ID
	userID, ok := claims["id"].(string)
	if !ok {
		return nil, errors.New("invalid user id in token")
	}

	// 检查用户是否存在
	u, err := s.client.User.Query().
		Where(user.UIDEQ(userID)).
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

	// 生成新的访问令牌
	expiresAt := time.Now().Add(time.Duration(config.(*cmfConfig.Config).App.LoginExpires) * time.Second)
	domain := claims["domain"].(string)

	accessClaims := jwt.MapClaims{
		"id":         u.UID,
		"subject":    u.Username,
		"issuer":     config.(*cmfConfig.Config).App.Name,
		"domain":     domain,
		"expires":    jwt.NewNumericDate(expiresAt),
		"issued_at":  jwt.NewNumericDate(time.Now()),
		"token_type": "access",
	}

	newAccessToken, err := cmfJwt.CreateToken(accessClaims, secret)
	if err != nil {
		return nil, err
	}

	return &TokenResponse{
		AccessToken:  newAccessToken,
		TokenType:    "Bearer",
		ExpiresIn:    int64(config.(*cmfConfig.Config).App.LoginExpires),
		RefreshToken: refreshToken, // 可以继续使用原刷新令牌，或生成新的
	}, nil
}

// ChangePassword 修改密码
func (s *UserService) ChangePassword(ctx context.Context, uid string, oldPassword, newPassword string) error {
	// 获取用户信息
	u, err := s.client.User.Query().
		Where(user.UIDEQ(uid)).
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

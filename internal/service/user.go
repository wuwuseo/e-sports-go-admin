package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/blacklist"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/message"
	"go-admin/internal/ent/messagepreference"
	"go-admin/internal/ent/oauthsession"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/thirdpartyuser"
	"go-admin/internal/ent/user"
	"go-admin/internal/ent/userrole"
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/eko/gocache/lib/v4/store"
	"github.com/golang-jwt/jwt/v5"
	cmfCache "github.com/wuwuseo/cmf/cache"
	cmfConfig "github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/crypto"
	cmfJwt "github.com/wuwuseo/cmf/jwt"
	"github.com/wuwuseo/cmf/log"
	"go.uber.org/zap"
)

var (
	ErrUserNotFound      = errors.New("user not found")
	ErrInvalidPassword   = errors.New("invalid password")
	ErrEmailExists       = errors.New("email already exists")
	ErrUsernameExists    = errors.New("username already exists")
	ErrInvalidUserStatus = errors.New("user is not active")
	ErrCannotDeleteSelf  = errors.New("不能删除自己的账号")
)

// UserServiceInterface 定义用户服务接口
type UserServiceInterface interface {
	Login(ctx context.Context, req *LoginRequest) (*TokenResponse, error)
	RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error)
	GetUserByUID(ctx context.Context, uid string) (*UserResponse, error)
	GetUserByID(ctx context.Context, id int) (*UserResponse, error)
	UpdateUser(ctx context.Context, uid string, updates map[string]interface{}) error
	ChangePassword(ctx context.Context, uid string, oldPassword, newPassword string) error
	Logout(ctx context.Context, uid string) error
	CreateUser(ctx context.Context, op *EffectiveDataScope, req *CreateUserRequest) (*UserResponse, error)
	ResetManagedUserPassword(ctx context.Context, id int) (string, error)
	UpdateManagedUser(ctx context.Context, id int, req *UpdateManagedUserRequest) error
	DeleteManagedUser(ctx context.Context, id int, operatorUID string) error
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
	Password string `json:"password" validate:"required,min=8"`
	Username string `json:"username" validate:"required,min=3"`
	Nickname string `json:"nickname"`
	Phone    string `json:"phone" validate:"omitempty,numeric"`
}

// CreateUserRequest 管理员创建用户请求
type CreateUserRequest struct {
	Email         string `json:"email" validate:"required,email"`
	Password      string `json:"password" validate:"required,min=8"`
	Username      string `json:"username" validate:"required,min=3"`
	Nickname      string `json:"nickname"`
	Phone         string `json:"phone"`
	RoleIDs       []int  `json:"role_ids"`
	PrimaryRoleID int    `json:"primary_role_id"`
}

// UpdateManagedUserRequest is the editable subset exposed by user management.
type UpdateManagedUserRequest struct {
	Email    *string `json:"email" validate:"omitempty,email"`
	Username *string `json:"username" validate:"omitempty,min=3"`
	Nickname *string `json:"nickname" validate:"omitempty,max=255"`
	Phone    *string `json:"phone" validate:"omitempty,numeric"`
	Avatar   *string `json:"avatar" validate:"omitempty,max=1024"`
	Password *string `json:"password" validate:"omitempty,min=8"`
}

// LoginRequest 用户登录请求
type LoginRequest struct {
	Email             string `json:"email"`
	Username          string `json:"username"`
	Phone             string `json:"phone"`
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
	// 校验密码强度（复杂度要求）
	if err := utils.ValidatePasswordStrength(req.Password); err != nil {
		return err
	}

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
func (s *UserService) CreateUser(ctx context.Context, op *EffectiveDataScope, req *CreateUserRequest) (*UserResponse, error) {
	// 校验密码强度（复杂度要求）
	if err := utils.ValidatePasswordStrength(req.Password); err != nil {
		return nil, err
	}

	if err := rejectSuperAdminRoleIDs(ctx, s.client, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
		return nil, err
	}
	domain := permissionDomainForScope(op)
	operatorUID := ""
	if op != nil {
		operatorUID = op.AdminUID
	}
	if err := ensureRolePermissionsNotEscalating(ctx, s.client, op, operatorUID, domain, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
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

	// 角色预校验：无效/停用角色直接拒绝，避免创建出"用户已存在但没有角色"的半成品
	roleIDs := dedupeRoleIDs(requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID))
	var roles []*ent.Role
	primaryRoleID := 0
	if len(roleIDs) > 0 {
		if req.PrimaryRoleID != 0 && !containsInt(req.RoleIDs, req.PrimaryRoleID) {
			return nil, ErrPrimaryRoleInvalid
		}
		roles, err = s.client.Role.Query().
			Where(role.IDIn(roleIDs...)).
			All(ctx)
		if err != nil {
			return nil, err
		}
		if len(roles) != len(roleIDs) {
			return nil, ErrInvalidRoleIDs
		}
		for _, r := range roles {
			if r.Status != role.StatusActive {
				return nil, ErrInvalidRoleIDs
			}
		}
		primaryRoleID = req.PrimaryRoleID
		if primaryRoleID == 0 {
			primaryRoleID = req.RoleIDs[0]
		}
	}

	var u *ent.User
	if len(roles) > 0 {
		// 用户创建与角色绑定同事务，保证管理端一次请求即可得到可用的账号
		tx, txErr := s.client.Tx(ctx)
		if txErr != nil {
			return nil, fmt.Errorf("开启事务失败: %w", txErr)
		}
		creator := tx.User.Create().
			SetEmail(req.Email).
			SetPassword(hashedPassword).
			SetPasswordSalt(salt).
			SetUsername(req.Username).
			SetUID(utils.GenerateUID()).
			SetStatus(user.StatusInactive)
		if req.Nickname != "" {
			creator = creator.SetNickname(req.Nickname)
		}
		if req.Phone != "" {
			creator = creator.SetPhone(req.Phone)
		}
		u, txErr = creator.Save(ctx)
		if txErr != nil {
			_ = tx.Rollback()
			return nil, txErr
		}
		for _, r := range roles {
			if _, txErr = tx.UserRole.Create().
				SetUserID(u.ID).
				SetRoleID(r.ID).
				SetIsPrimary(r.ID == primaryRoleID).
				Save(ctx); txErr != nil {
				_ = tx.Rollback()
				return nil, fmt.Errorf("分配角色失败: %w", txErr)
			}
		}
		if txErr = tx.Commit(); txErr != nil {
			return nil, fmt.Errorf("提交事务失败: %w", txErr)
		}
	} else {
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
		u, err = create.Save(ctx)
		if err != nil {
			return nil, err
		}
	}

	// Casbin g 同步：新账号无历史角色，追加即可；失败仅记录日志（与 CreateAdmin 行为一致）
	if len(roles) > 0 && global.EnforcerManager != nil {
		if enforcer, enfErr := global.EnforcerManager.GetEnforcer(domain); enfErr == nil {
			roleNames := make([]string, 0, len(roles))
			for _, r := range roles {
				roleNames = append(roleNames, r.Name)
			}
			if err := addCasbinUserRoles(enforcer, u.UID, domain, roleNames); err != nil {
				log.Error("CreateUser 同步 Casbin 角色失败", zap.String("uid", u.UID), zap.Error(err))
			}
		}
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
	account := strings.TrimSpace(req.Email)
	if account == "" {
		account = strings.TrimSpace(req.Username)
	}
	if account == "" {
		account = strings.TrimSpace(req.Phone)
	}
	if account == "" {
		return nil, ErrUserNotFound
	}
	// 查找用户：账号可填邮箱、用户名或手机号；按优先级逐个匹配，避免跨字段撞号。
	u, err := s.client.User.Query().
		Where(user.EmailEQ(account)).
		Only(ctx)
	if ent.IsNotFound(err) {
		u, err = s.client.User.Query().
			Where(user.UsernameEQ(account)).
			Only(ctx)
	}
	if ent.IsNotFound(err) {
		u, err = s.client.User.Query().
			Where(user.PhoneEQ(account)).
			Only(ctx)
	}
	if err != nil {
		if ent.IsNotFound(err) {
			s.recordLoginAttempt(ctx, account, 0, false, "账号不存在")
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	// 检查用户状态
	if u.Status != user.StatusActive {
		s.recordLoginAttempt(ctx, account, u.ID, false, "账号已禁用")
		return nil, ErrInvalidUserStatus
	}

	// 验证密码
	if u.Password == "" {
		s.recordLoginAttempt(ctx, account, u.ID, false, "密码错误")
		return nil, ErrInvalidPassword
	}

	match, err := crypto.VerifyPassword(req.Password, u.Password, u.PasswordSalt)
	if err != nil {
		return nil, err
	}
	if !match {
		s.recordLoginAttempt(ctx, req.Email, u.ID, false, "密码错误")
		return nil, ErrInvalidPassword
	}
	configService, _ := global.Bootstrap.GetService("config")
	config := configService.(*cmfConfig.Config)
	tokens, err := createTokenPair(
		u.UID, u.Username, config.App.Name, config.App.Secret,
		time.Duration(config.App.LoginExpires)*time.Second,
		time.Duration(config.App.RefreshExpires)*time.Second,
		time.Now,
		u.TokenVersion,
	)
	if err != nil {
		return nil, err
	}

	// 登录成功补写最近登录时间与来源 IP（来源 IP 从登录元数据上下文获取）
	update := s.client.User.UpdateOne(u).SetLastLogin(time.Now())
	if meta, ok := loginMetaFromContext(ctx); ok && meta.IP != "" {
		update = update.SetLastIP(meta.IP)
	}
	if _, err = update.Save(ctx); err != nil {
		return nil, err
	}
	s.recordLoginAttempt(ctx, u.Username, u.ID, true, "")
	return tokens, nil
}

// resetPasswordLength 管理员初始化密码的临时密码长度
const resetPasswordLength = 16

// ResetManagedUserPassword 管理员初始化用户密码：
// 生成随机强密码（一次性返回给操作者），旧密码即行失效并吊销该用户全部令牌。
func (s *UserService) ResetManagedUserPassword(ctx context.Context, id int) (string, error) {
	u, err := s.client.User.Query().
		Where(user.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", ErrUserNotFound
		}
		return "", err
	}

	newPwd, err := utils.GenerateStrongPassword(resetPasswordLength)
	if err != nil {
		return "", err
	}
	hashedPassword, salt, err := crypto.HashPassword(newPwd)
	if err != nil {
		return "", err
	}
	if _, err = s.client.User.UpdateOne(u).
		SetPassword(hashedPassword).
		SetPasswordSalt(salt).
		AddTokenVersion(1).
		Save(ctx); err != nil {
		return "", err
	}

	return newPwd, nil
}

// dedupeRoleIDs 去除重复的角色 ID，保持原有顺序
func dedupeRoleIDs(roleIDs []int) []int {
	seen := make(map[int]struct{}, len(roleIDs))
	unique := make([]int, 0, len(roleIDs))
	for _, id := range roleIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}

// recordLoginAttempt 记录前台用户登录成败日志。
// 仅当请求上下文携带登录元数据（HTTP 登录入口注入）时记录；
// 内部调用与单测上下文无元数据，跳过写入。
func (s *UserService) recordLoginAttempt(ctx context.Context, account string, userID int, success bool, message string) {
	meta, ok := loginMetaFromContext(ctx)
	if !ok || (meta.IP == "" && meta.UserAgent == "") {
		return
	}
	svc := defaultLoginLogService.Load()
	if svc == nil {
		return
	}
	entry := &LoginLogEntry{
		Username:  account,
		UserID:    userID,
		LoginType: "user",
		IP:        meta.IP,
		UserAgent: meta.UserAgent,
	}
	if success {
		entry.Status = "success"
	} else {
		entry.Status = "fail"
		entry.Message = message
	}
	_ = svc.Record(context.WithoutCancel(ctx), entry)
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
	if !utils.TokenVersionMatches(claims["token_version"], u.TokenVersion) {
		return nil, errors.New("refresh token has been revoked")
	}

	// 生成新的访问令牌
	expiresAt := time.Now().Add(time.Duration(config.(*cmfConfig.Config).App.LoginExpires) * time.Second)
	domain, err := permissionDomainFromClaims(claims)
	if err != nil {
		return nil, err
	}

	accessClaims := jwt.MapClaims{
		"id":            u.UID,
		"subject":       u.Username,
		"issuer":        config.(*cmfConfig.Config).App.Name,
		"domain":        domain,
		"expires":       jwt.NewNumericDate(expiresAt),
		"issued_at":     jwt.NewNumericDate(time.Now()),
		"token_type":    "access",
		"token_version": u.TokenVersion,
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
	// 校验新密码强度（复杂度要求）
	if err := utils.ValidatePasswordStrength(newPassword); err != nil {
		return err
	}

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
		AddTokenVersion(1).
		Save(ctx)

	return err
}

// UpdateManagedUser updates a user selected by its numeric management ID.
func (s *UserService) UpdateManagedUser(ctx context.Context, id int, req *UpdateManagedUserRequest) error {
	u, err := s.client.User.Query().Where(user.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrUserNotFound
		}
		return err
	}

	if req.Email != nil && *req.Email != u.Email {
		exists, err := s.client.User.Query().
			Where(user.EmailEQ(*req.Email), user.IDNEQ(id)).
			Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			return ErrEmailExists
		}
	}
	if req.Username != nil && *req.Username != u.Username {
		exists, err := s.client.User.Query().
			Where(user.UsernameEQ(*req.Username), user.IDNEQ(id)).
			Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			return ErrUsernameExists
		}
	}

	update := s.client.User.UpdateOne(u)
	if req.Email != nil {
		update.SetEmail(*req.Email)
	}
	if req.Username != nil {
		update.SetUsername(*req.Username)
	}
	if req.Nickname != nil {
		update.SetNickname(*req.Nickname)
	}
	if req.Phone != nil {
		update.SetPhone(*req.Phone)
	}
	if req.Avatar != nil {
		update.SetAvatar(*req.Avatar)
	}
	if req.Password != nil && *req.Password != "" {
		if err := utils.ValidatePasswordStrength(*req.Password); err != nil {
			return err
		}
		hash, salt, err := crypto.HashPassword(*req.Password)
		if err != nil {
			return err
		}
		update.SetPassword(hash).SetPasswordSalt(salt).AddTokenVersion(1)
	}

	_, err = update.Save(ctx)
	return err
}

// DeleteManagedUser removes a user and data owned only by that account.
func (s *UserService) DeleteManagedUser(ctx context.Context, id int, operatorUID string) error {
	u, err := s.client.User.Query().Where(user.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrUserNotFound
		}
		return err
	}

	if u.UID == operatorUID {
		return ErrCannotDeleteSelf
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	mtx := NewManagedTx(tx)
	defer mtx.RollbackIfNotCommitted()

	deletes := []func() error{
		func() error { _, err := tx.UserRole.Delete().Where(userrole.UserIDEQ(id)).Exec(ctx); return err },
		func() error { _, err := tx.Blacklist.Delete().Where(blacklist.UserIDEQ(id)).Exec(ctx); return err },
		func() error {
			_, err := tx.ThirdPartyUser.Delete().Where(thirdpartyuser.UserIDEQ(id)).Exec(ctx)
			return err
		},
		func() error { _, err := tx.Message.Delete().Where(message.UserUIDEQ(u.UID)).Exec(ctx); return err },
		func() error {
			_, err := tx.MessagePreference.Delete().Where(messagepreference.UserUIDEQ(u.UID)).Exec(ctx)
			return err
		},
		func() error {
			_, err := tx.OAuthSession.Delete().Where(
				oauthsession.Or(oauthsession.SubjectUIDEQ(u.UID), oauthsession.LoginSubjectUIDEQ(u.UID)),
			).Exec(ctx)
			return err
		},
		func() error {
			_, err := tx.CasbinRule.Delete().Where(casbinrule.PtypeEQ("g"), casbinrule.V0EQ(u.UID)).Exec(ctx)
			return err
		},
		func() error { return tx.User.DeleteOneID(id).Exec(ctx) },
	}
	for _, remove := range deletes {
		if err := remove(); err != nil {
			return err
		}
	}
	if err := mtx.Commit(); err != nil {
		return err
	}

	if global.EnforcerManager != nil {
		domain := configuredPermissionDomain()
		if enforcer, err := global.EnforcerManager.GetEnforcer(domain); err == nil {
			_ = removeCasbinUserRoles(enforcer, u.UID, domain)
		}
	}
	if s.cache != nil {
		domain := configuredPermissionDomain()
		for _, key := range []string{
			fmt.Sprintf("user_roles:%d", id),
			fmt.Sprintf("user:menu:%s:%s", u.UID, domain),
			fmt.Sprintf("user:button:%s:%s", u.UID, domain),
			fmt.Sprintf("user:api:%s:%s", u.UID, domain),
		} {
			_ = s.cache.Delete(ctx, key)
		}
		_ = s.Logout(ctx, u.UID)
	}

	return nil
}

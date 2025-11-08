package service

import (
	"context"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/user"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
)

func TestUserService_Register(t *testing.T) {
	// 创建测试数据库客户端
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	// 初始化UserService
	s := NewUserService(client)

	ctx := context.Background()

	tests := []struct {
		name    string
		req     *RegisterRequest
		wantErr error
	}{
		{
			name: "正常注册",
			req: &RegisterRequest{
				Email:    "test@example.com",
				Password: "password123",
				Username: "testuser",
				Nickname: "Test User",
				Phone:    "1234567890",
			},
			wantErr: nil,
		},
		{
			name: "邮箱已存在",
			req: &RegisterRequest{
				Email:    "test@example.com",
				Password: "password123",
				Username: "testuser2",
				Nickname: "Test User 2",
			},
			wantErr: ErrEmailExists,
		},
		{
			name: "用户名已存在",
			req: &RegisterRequest{
				Email:    "test2@example.com",
				Password: "password123",
				Username: "testuser",
				Nickname: "Test User 2",
			},
			wantErr: ErrUsernameExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.Register(ctx, tt.req)
			if tt.wantErr != nil {
				assert.Equal(t, tt.wantErr, err)
				return
			}
			assert.NoError(t, err)

			// 验证用户是否创建成功
			u, err := client.User.Query().Where(user.EmailEQ(tt.req.Email)).Only(ctx)
			assert.NoError(t, err)
			assert.Equal(t, tt.req.Email, u.Email)
			assert.Equal(t, tt.req.Username, u.Username)
			assert.Equal(t, tt.req.Nickname, u.Nickname)
			assert.Equal(t, tt.req.Phone, u.Phone)
			assert.Equal(t, user.StatusInactive, u.Status)
			assert.NotEmpty(t, u.PasswordSalt)
		})
	}
}

func TestUserService_Login(t *testing.T) {
	// 创建测试数据库客户端
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	// 初始化UserService
	s := NewUserService(client)

	ctx := context.Background()

	// 创建测试用户
	testUser := &RegisterRequest{
		Email:    "test@example.com",
		Password: "password123",
		Username: "testuser",
		Nickname: "Test User",
	}
	err := s.Register(ctx, testUser)
	assert.NoError(t, err)

	// 激活用户
	u, err := client.User.Query().Where(user.EmailEQ(testUser.Email)).Only(ctx)
	assert.NoError(t, err)
	_, err = client.User.UpdateOne(u).SetStatus(user.StatusActive).Save(ctx)
	assert.NoError(t, err)

	tests := []struct {
		name    string
		req     *LoginRequest
		wantErr error
	}{
		{
			name: "正确的登录信息",
			req: &LoginRequest{
				Email:    "test@example.com",
				Password: "password123",
			},
			wantErr: nil,
		},
		{
			name: "错误的密码",
			req: &LoginRequest{
				Email:    "test@example.com",
				Password: "wrongpassword",
			},
			wantErr: ErrInvalidPassword,
		},
		{
			name: "不存在的用户",
			req: &LoginRequest{
				Email:    "nonexistent@example.com",
				Password: "password123",
			},
			wantErr: ErrUserNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := s.Login(ctx, tt.req)
			if tt.wantErr != nil {
				assert.Equal(t, tt.wantErr, err)
				return
			}
			assert.NoError(t, err)
			assert.NotNil(t, token)
			assert.NotEmpty(t, token.AccessToken)
			assert.Equal(t, "Bearer", token.TokenType)
			assert.Equal(t, int64(24*60*60), token.ExpiresIn)
		})
	}
}

func TestUserService_GetUserByID(t *testing.T) {
	// 创建测试数据库客户端
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	// 初始化UserService
	s := NewUserService(client)

	ctx := context.Background()

	// 创建测试用户
	testUser := &RegisterRequest{
		Email:    "test@example.com",
		Password: "password123",
		Username: "testuser",
		Nickname: "Test User",
		Phone:    "1234567890",
	}
	err := s.Register(ctx, testUser)
	assert.NoError(t, err)

	u, err := client.User.Query().Where(user.EmailEQ(testUser.Email)).Only(ctx)
	assert.NoError(t, err)

	tests := []struct {
		name    string
		id      int
		wantErr error
	}{
		{
			name:    "存在的用户ID",
			id:      u.ID,
			wantErr: nil,
		},
		{
			name:    "不存在的用户ID",
			id:      999,
			wantErr: ErrUserNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, err := s.GetUserByID(ctx, tt.id)
			if tt.wantErr != nil {
				assert.Equal(t, tt.wantErr, err)
				return
			}
			assert.NoError(t, err)
			assert.NotNil(t, user)
			assert.Equal(t, tt.id, user.ID)
		})
	}
}

func TestUserService_UpdateUser(t *testing.T) {
	// 创建测试数据库客户端
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	// 初始化UserService
	s := NewUserService(client)

	ctx := context.Background()

	// 创建测试用户
	testUser := &RegisterRequest{
		Email:    "test@example.com",
		Password: "password123",
		Username: "testuser",
		Nickname: "Test User",
	}
	err := s.Register(ctx, testUser)
	assert.NoError(t, err)

	u, err := client.User.Query().Where(user.EmailEQ(testUser.Email)).Only(ctx)
	assert.NoError(t, err)

	tests := []struct {
		name    string
		id      int
		updates map[string]interface{}
		wantErr error
	}{
		{
			name: "正常更新",
			id:   u.ID,
			updates: map[string]interface{}{
				"nickname": "Updated Name",
				"phone":    "9876543210",
				"avatar":   "avatar.jpg",
				"status":   string(user.StatusActive),
			},
			wantErr: nil,
		},
		{
			name: "不存在的用户",
			id:   999,
			updates: map[string]interface{}{
				"nickname": "Updated Name",
			},
			wantErr: ErrUserNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.UpdateUser(ctx, tt.id, tt.updates)
			if tt.wantErr != nil {
				assert.Equal(t, tt.wantErr, err)
				return
			}
			assert.NoError(t, err)

			// 验证更新是否成功
			updated, err := s.GetUserByID(ctx, tt.id)
			assert.NoError(t, err)
			if nickname, ok := tt.updates["nickname"].(string); ok {
				assert.Equal(t, nickname, updated.Nickname)
			}
			if phone, ok := tt.updates["phone"].(string); ok {
				assert.Equal(t, phone, updated.Phone)
			}
			if avatar, ok := tt.updates["avatar"].(string); ok {
				assert.Equal(t, avatar, updated.Avatar)
			}
			if status, ok := tt.updates["status"].(string); ok {
				assert.Equal(t, user.Status(status), updated.Status)
			}
		})
	}
}

func TestUserService_ChangePassword(t *testing.T) {
	// 创建测试数据库客户端
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	// 初始化UserService
	s := NewUserService(client)

	ctx := context.Background()

	// 创建测试用户
	testUser := &RegisterRequest{
		Email:    "test@example.com",
		Password: "password123",
		Username: "testuser",
		Nickname: "Test User",
	}
	err := s.Register(ctx, testUser)
	assert.NoError(t, err)

	u, err := client.User.Query().Where(user.EmailEQ(testUser.Email)).Only(ctx)
	assert.NoError(t, err)

	tests := []struct {
		name        string
		id          int
		oldPassword string
		newPassword string
		wantErr     error
	}{
		{
			name:        "正确的旧密码",
			id:          u.ID,
			oldPassword: "password123",
			newPassword: "newpassword123",
			wantErr:     nil,
		},
		{
			name:        "错误的旧密码",
			id:          u.ID,
			oldPassword: "wrongpassword",
			newPassword: "newpassword123",
			wantErr:     ErrInvalidPassword,
		},
		{
			name:        "不存在的用户",
			id:          999,
			oldPassword: "password123",
			newPassword: "newpassword123",
			wantErr:     ErrUserNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.ChangePassword(ctx, tt.id, tt.oldPassword, tt.newPassword)
			if tt.wantErr != nil {
				assert.Equal(t, tt.wantErr, err)
				return
			}
			assert.NoError(t, err)

			// 验证密码是否已更改
			if tt.wantErr == nil {
				loginReq := &LoginRequest{
					Email:    testUser.Email,
					Password: tt.newPassword,
				}
				token, err := s.Login(ctx, loginReq)
				assert.NoError(t, err)
				assert.NotEmpty(t, token)
			}
		})
	}
}
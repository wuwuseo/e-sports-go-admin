package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/wuwuseo/cmf/jwt"
)

// MockUserService 模拟 UserService
type MockUserService struct {
	mock.Mock
}

func (m *MockUserService) Login(ctx context.Context, req *service.LoginRequest) (*service.TokenResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*service.TokenResponse), args.Error(1)
}

func (m *MockUserService) GetUserByID(ctx context.Context, id string) (*service.UserResponse, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*service.UserResponse), args.Error(1)
}

func (m *MockUserService) UpdateUser(ctx context.Context, id string, updates map[string]interface{}) error {
	args := m.Called(ctx, id, updates)
	return args.Error(0)
}

func (m *MockUserService) ChangePassword(ctx context.Context, id string, oldPassword, newPassword string) error {
	args := m.Called(ctx, id, oldPassword, newPassword)
	return args.Error(0)
}

func setupTestApp() *fiber.App {
	app := fiber.New()
	return app
}

func TestLogin(t *testing.T) {
	tests := []struct {
		name           string
		requestBody    interface{}
		mockSetup      func(*MockUserService)
		expectedStatus int
		expectedBody   map[string]interface{}
	}{
		{
			name: "成功登录",
			requestBody: service.LoginRequest{
				Email:    "test@example.com",
				Password: "password123",
			},
			mockSetup: func(m *MockUserService) {
				m.On("Login", mock.Anything, mock.Anything).Return(&service.TokenResponse{
					AccessToken: "test-token",
					ExpiresIn:   86400,
					TokenType:   "Bearer",
				}, nil)
			},
			expectedStatus: 200,
			expectedBody: map[string]interface{}{
				"code": float64(global.ResponseSuccessCode),
				"data": map[string]interface{}{
					"access_token": "test-token",
					"expires_in":   float64(86400),
					"token_type":   "Bearer",
				},
				"message": "success",
			},
		},
		{
			name: "用户不存在",
			requestBody: service.LoginRequest{
				Email:    "nonexistent@example.com",
				Password: "password123",
			},
			mockSetup: func(m *MockUserService) {
				m.On("Login", mock.Anything, mock.Anything).Return(nil, service.ErrUserNotFound)
			},
			expectedStatus: 200,
			expectedBody: map[string]interface{}{
				"code":    float64(global.ResponseNotFound),
				"message": "User not found",
			},
		},
		{
			name: "密码错误",
			requestBody: service.LoginRequest{
				Email:    "test@example.com",
				Password: "wrongpassword",
			},
			mockSetup: func(m *MockUserService) {
				m.On("Login", mock.Anything, mock.Anything).Return(nil, service.ErrInvalidPassword)
			},
			expectedStatus: 200,
			expectedBody: map[string]interface{}{
				"code":    float64(global.ResponseUnauthorized),
				"message": "Invalid password",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := setupTestApp()
			mockUserService := new(MockUserService)
			tt.mockSetup(mockUserService)

			app.Post("/login", func(c *fiber.Ctx) error {
				c.Locals("utils.Response", utils.NewResponse())
				c.Locals("service.UserService", mockUserService)
				return Login(c)
			})

			body, _ := json.Marshal(tt.requestBody)
			req := httptest.NewRequest("POST", "/login", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			resp, err := app.Test(req)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, resp.StatusCode)

			var result map[string]interface{}
			json.NewDecoder(resp.Body).Decode(&result)
			assert.Equal(t, tt.expectedBody, result)
		})
	}
}

func TestGetUser(t *testing.T) {
	tests := []struct {
		name           string
		userID         string
		mockSetup      func(*MockUserService)
		expectedStatus int
		expectedBody   map[string]interface{}
	}{
		{
			name:   "成功获取用户信息",
			userID: "user123",
			mockSetup: func(m *MockUserService) {
				m.On("GetUserByID", mock.Anything, "user123").Return(&service.UserResponse{
					ID:        "user123",
					Email:     "test@example.com",
					Username:  "testuser",
					Nickname:  "Test User",
					Phone:     "1234567890",
					Avatar:    "avatar.jpg",
					Status:    "active",
					LastLogin: time.Now(),
				}, nil)
			},
			expectedStatus: 200,
			expectedBody: map[string]interface{}{
				"code": float64(global.ResponseSuccessCode),
				"data": map[string]interface{}{
					"id":       "user123",
					"email":    "test@example.com",
					"username": "testuser",
					"nickname": "Test User",
					"phone":    "1234567890",
					"avatar":   "avatar.jpg",
					"status":   "active",
				},
				"message": "success",
			},
		},
		{
			name:   "用户不存在",
			userID: "nonexistent",
			mockSetup: func(m *MockUserService) {
				m.On("GetUserByID", mock.Anything, "nonexistent").Return(nil, service.ErrUserNotFound)
			},
			expectedStatus: 200,
			expectedBody: map[string]interface{}{
				"code":    float64(404),
				"message": "User not found",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := setupTestApp()
			mockUserService := new(MockUserService)
			tt.mockSetup(mockUserService)

			app.Get("/user", func(c *fiber.Ctx) error {
				c.Locals("utils.Response", utils.NewResponse())
				c.Locals("service.UserService", mockUserService)
				// 设置 JWT token 数据
				token := &jwt.Token{
					Claims: map[string]interface{}{
						"id": tt.userID,
					},
				}
				c.Locals("jwt", token)
				return GetUser(c)
			})

			req := httptest.NewRequest("GET", "/user", nil)
			resp, err := app.Test(req)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, resp.StatusCode)

			var result map[string]interface{}
			json.NewDecoder(resp.Body).Decode(&result)
			assert.Equal(t, tt.expectedBody, result)
		})
	}
}

func TestUpdateUser(t *testing.T) {
	tests := []struct {
		name           string
		userID         string
		requestBody    map[string]interface{}
		mockSetup      func(*MockUserService)
		expectedStatus int
		expectedBody   map[string]interface{}
	}{
		{
			name:   "成功更新用户信息",
			userID: "user123",
			requestBody: map[string]interface{}{
				"nickname": "New Name",
				"phone":    "9876543210",
			},
			mockSetup: func(m *MockUserService) {
				m.On("UpdateUser", mock.Anything, "user123", mock.Anything).Return(nil)
			},
			expectedStatus: 200,
			expectedBody: map[string]interface{}{
				"code":    float64(global.ResponseSuccessCode),
				"message": "success",
			},
		},
		{
			name:   "更新失败",
			userID: "user123",
			requestBody: map[string]interface{}{
				"nickname": "New Name",
			},
			mockSetup: func(m *MockUserService) {
				m.On("UpdateUser", mock.Anything, "user123", mock.Anything).Return(service.ErrUserNotFound)
			},
			expectedStatus: 200,
			expectedBody: map[string]interface{}{
				"code":    float64(global.ResponseInternalServerError),
				"message": "Internal server error",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := setupTestApp()
			mockUserService := new(MockUserService)
			tt.mockSetup(mockUserService)

			app.Put("/user", func(c *fiber.Ctx) error {
				c.Locals("utils.Response", utils.NewResponse())
				c.Locals("service.UserService", mockUserService)
				// 设置 JWT token 数据
				token := &jwt.Token{
					Claims: map[string]interface{}{
						"id": tt.userID,
					},
				}
				c.Locals("jwt", token)
				return UpdateUser(c)
			})

			body, _ := json.Marshal(tt.requestBody)
			req := httptest.NewRequest("PUT", "/user", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			resp, err := app.Test(req)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, resp.StatusCode)

			var result map[string]interface{}
			json.NewDecoder(resp.Body).Decode(&result)
			assert.Equal(t, tt.expectedBody, result)
		})
	}
}

func TestChangePassword(t *testing.T) {
	tests := []struct {
		name           string
		userID         string
		requestBody    map[string]string
		mockSetup      func(*MockUserService)
		expectedStatus int
		expectedBody   map[string]interface{}
	}{
		{
			name:   "成功修改密码",
			userID: "user123",
			requestBody: map[string]string{
				"old_password": "oldpass123",
				"new_password": "newpass123",
			},
			mockSetup: func(m *MockUserService) {
				m.On("ChangePassword", mock.Anything, "user123", "oldpass123", "newpass123").Return(nil)
			},
			expectedStatus: 200,
			expectedBody: map[string]interface{}{
				"code":    float64(global.ResponseSuccessCode),
				"message": "success",
			},
		},
		{
			name:   "旧密码错误",
			userID: "user123",
			requestBody: map[string]string{
				"old_password": "wrongpass",
				"new_password": "newpass123",
			},
			mockSetup: func(m *MockUserService) {
				m.On("ChangePassword", mock.Anything, "user123", "wrongpass", "newpass123").Return(service.ErrInvalidPassword)
			},
			expectedStatus: 200,
			expectedBody: map[string]interface{}{
				"code":    float64(global.ResponseUnauthorized),
				"message": "Invalid old password",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := setupTestApp()
			mockUserService := new(MockUserService)
			tt.mockSetup(mockUserService)

			app.Put("/password", func(c *fiber.Ctx) error {
				c.Locals("utils.Response", utils.NewResponse())
				c.Locals("service.UserService", mockUserService)
				// 设置 JWT token 数据
				token := &jwt.Token{
					Claims: map[string]interface{}{
						"id": tt.userID,
					},
				}
				c.Locals("jwt", token)
				return ChangePassword(c)
			})

			body, _ := json.Marshal(tt.requestBody)
			req := httptest.NewRequest("PUT", "/password", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			resp, err := app.Test(req)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, resp.StatusCode)

			var result map[string]interface{}
			json.NewDecoder(resp.Body).Decode(&result)
			assert.Equal(t, tt.expectedBody, result)
		})
	}
}
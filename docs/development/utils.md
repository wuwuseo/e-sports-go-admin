# 工具类使用指南

## 概述

项目提供了一系列工具类来简化开发工作，包括统一的响应处理、错误码管理等功能。

## Response 响应工具

### 基本用法

Response工具类提供了统一的API响应格式处理。

```go
package handler

import (
    "go-admin/internal/utils"
    "github.com/gofiber/fiber/v2"
)

func SomeHandler(c *fiber.Ctx) error {
    response := c.Locals("utils.Response").(*utils.Response)
    
    // 成功响应
    return response.Success(data).ToJSON(c)
    
    // 失败响应
    return response.Fail(400, "错误信息").ToJSON(c)
}
```

### 响应格式

所有API响应都遵循统一的格式：

```json
{
  "code": 200,
  "message": "success",
  "data": {
    // 响应数据
  }
}
```

### 方法说明

#### Success 方法

```go
func (r *Response) Success(options ...any) *Response
```

**参数说明**:
- `options[0]`: 响应数据 (可选)
- `options[1]`: 状态码 (可选，默认200)
- `options[2]`: 消息 (可选，默认"success")

**使用示例**:
```go
// 只返回成功状态
response.Success()

// 返回数据
response.Success(userData)

// 自定义状态码和消息
response.Success(userData, 201, "创建成功")
```

#### Fail 方法

```go
func (r *Response) Fail(options ...any) *Response
```

**参数说明**:
- `options[0]`: 状态码 (可选，默认400)
- `options[1]`: 错误消息 (可选，默认"fail")

**使用示例**:
```go
// 默认错误响应
response.Fail()

// 自定义状态码
response.Fail(404)

// 自定义状态码和消息
response.Fail(400, "参数错误")
```

## 全局常量

### 响应状态码

项目定义了标准的响应状态码常量：

```go
const (
    ResponseSuccessCode         = 200  // 操作成功
    ResponseErrorCode           = 400  // 操作失败
    ResponseUnauthorized        = 401  // 未授权
    ResponseForbidden           = 403  // 拒绝访问
    ResponseNotFound            = 404  // 资源不存在
    ResponseMethodNotAllowed    = 405  // 方法不允许
    ResponseConflict            = 409  // 冲突
    ResponseInternalServerError = 500  // 内部服务器错误
)
```

### 响应消息映射

```go
var ResponseMsg = map[int]string{
    ResponseSuccessCode:         "操作成功",
    ResponseErrorCode:           "操作失败",
    ResponseUnauthorized:        "未授权",
    ResponseForbidden:           "拒绝访问",
    ResponseNotFound:            "资源不存在",
    ResponseMethodNotAllowed:    "方法不允许",
    ResponseConflict:            "冲突",
    ResponseInternalServerError: "内部服务器错误",
}
```

### 使用示例

```go
import "go-admin/internal/global"

func SomeHandler(c *fiber.Ctx) error {
    response := c.Locals("utils.Response").(*utils.Response)
    
    // 使用全局常量
    return response.Fail(global.ResponseNotFound, "用户不存在").ToJSON(c)
}
```

## 错误处理最佳实践

### 1. 统一错误响应

```go
func HandleError(c *fiber.Ctx, err error) error {
    response := c.Locals("utils.Response").(*utils.Response)
    
    switch err {
    case service.ErrUserNotFound:
        return response.Fail(global.ResponseNotFound, "用户不存在").ToJSON(c)
    case service.ErrInvalidPassword:
        return response.Fail(global.ResponseUnauthorized, "密码错误").ToJSON(c)
    default:
        return response.Fail(global.ResponseInternalServerError, "内部服务器错误").ToJSON(c)
    }
}
```

### 2. 参数验证错误

```go
func ValidateRequest(c *fiber.Ctx, req interface{}) error {
    response := c.Locals("utils.Response").(*utils.Response)
    
    if err := c.BodyParser(req); err != nil {
        return response.Fail(global.ResponseErrorCode, "请求参数格式错误").ToJSON(c)
    }
    
    // 进行其他验证...
    
    return nil
}
```

### 3. 业务逻辑错误

```go
func BusinessLogic(c *fiber.Ctx) error {
    response := c.Locals("utils.Response").(*utils.Response)
    
    // 执行业务逻辑
    result, err := someService.DoSomething()
    if err != nil {
        // 根据错误类型返回不同的响应
        if errors.Is(err, service.ErrValidationFailed) {
            return response.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
        }
        return response.Fail(global.ResponseInternalServerError, "操作失败").ToJSON(c)
    }
    
    return response.Success(result).ToJSON(c)
}
```

## 中间件集成

### Response中间件

在路由中间件中注入Response实例：

```go
func RegisterRoute(app *fiber.App, config *config.Config) {
    v1 := api.Group("/v1", func(c *fiber.Ctx) error {
        c.Set("Version", "1")
        c.Locals("utils.Response", utils.NewResponse())
        return c.Next()
    })
}
```

### 服务注入中间件

```go
adminPublic := v1.Group("/public", func(c *fiber.Ctx) error {
    c.Locals("service.UserService", service.NewUserService())
    return c.Next()
})
```

## 扩展工具类

### 添加新的工具函数

在 `internal/utils/utils.go` 中添加通用工具函数：

```go
package utils

import (
    "go-admin/internal/global"
    "regexp"
)

// ValidateEmail 验证邮箱格式
func ValidateEmail(email string) bool {
    emailRegex := regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
    return emailRegex.MatchString(email)
}

// ValidatePhone 验证手机号格式
func ValidatePhone(phone string) bool {
    phoneRegex := regexp.MustCompile(`^[0-9]{11}$`)
    return phoneRegex.MatchString(phone)
}

// GetResponseMsg 获取响应消息
func GetResponseMsg(code int) string {
    if msg, exists := global.ResponseMsg[code]; exists {
        return msg
    }
    return "未知错误"
}
```

### 使用自定义工具函数

```go
import "go-admin/internal/utils"

func ValidateUserInput(email, phone string) error {
    if !utils.ValidateEmail(email) {
        return errors.New("邮箱格式不正确")
    }
    
    if phone != "" && !utils.ValidatePhone(phone) {
        return errors.New("手机号格式不正确")
    }
    
    return nil
}
```

## 注意事项

1. **响应格式一致性**: 所有API接口都应该使用Response工具类来确保响应格式的一致性

2. **错误码标准化**: 使用全局定义的错误码常量，避免硬编码状态码

3. **错误消息国际化**: 考虑后续的国际化需求，错误消息应该支持多语言

4. **日志记录**: 在返回错误响应时，应该记录详细的错误日志用于调试

5. **性能考虑**: Response对象通过中间件注入，避免在每个处理器中重复创建

通过合理使用这些工具类，可以大大提高开发效率，保证代码的一致性和可维护性。
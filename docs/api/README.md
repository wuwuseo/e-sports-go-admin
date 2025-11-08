# API 接口文档

## 概述

Go-Admin 提供RESTful API接口，支持用户认证、权限管理等功能。所有API接口都遵循统一的响应格式和错误处理机制。

## 基础信息

- **Base URL**: `http://localhost:9501/api/v1`
- **Content-Type**: `application/json`
- **字符编码**: UTF-8

## 认证机制

### JWT Token认证

受保护的API端点需要在请求头中包含JWT Token：

```http
Authorization: Bearer <jwt_token>
```

### Token获取

通过登录接口获取JWT Token，Token有效期为24小时。

## API端点

### 公开接口

#### 用户登录

**接口地址**: `POST /public/login`

**请求参数**:
```json
{
  "email": "user@example.com",
  "password": "password123"
}
```

**参数说明**:
- `email`: 用户邮箱，必填，需要是有效的邮箱格式
- `password`: 用户密码，必填

**响应示例**:
```json
{
  "code": 200,
  "message": "success",
  "data": {
    "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
    "token_type": "Bearer",
    "expires_in": 86400
  }
}
```

**错误响应**:
```json
{
  "code": 404,
  "message": "User not found"
}
```

### 受保护接口

以下接口需要JWT Token认证。

#### 获取用户信息

**接口地址**: `GET /admin/auth/user`

**请求头**:
```http
Authorization: Bearer <jwt_token>
```

**响应示例**:
```json
{
  "code": 200,
  "message": "success",
  "data": {
    "id": "user_unique_id",
    "email": "user@example.com",
    "username": "username",
    "nickname": "User Nickname",
    "phone": "13800138000",
    "avatar": "https://example.com/avatar.jpg",
    "status": "active"
  }
}
```

#### 更新用户信息

**接口地址**: `PUT /admin/auth/user`

**请求头**:
```http
Authorization: Bearer <jwt_token>
Content-Type: application/json
```

**请求参数**:
```json
{
  "nickname": "新昵称",
  "phone": "13800138000",
  "avatar": "https://example.com/avatar.jpg"
}
```

**参数说明**:
- `nickname`: 用户昵称，可选
- `phone`: 手机号码，可选，需要是有效的数字格式
- `avatar`: 头像URL，可选

**响应示例**:
```json
{
  "code": 200,
  "message": "success"
}
```

#### 修改密码

**接口地址**: `PUT /admin/auth/password`

**请求头**:
```http
Authorization: Bearer <jwt_token>
Content-Type: application/json
```

**请求参数**:
```json
{
  "old_password": "old_password123",
  "new_password": "new_password123"
}
```

**参数说明**:
- `old_password`: 原密码，必填
- `new_password`: 新密码，必填，最少6位

**响应示例**:
```json
{
  "code": 200,
  "message": "success"
}
```

## 响应格式

### 成功响应

```json
{
  "code": 200,
  "message": "操作成功",
  "data": {
    // 响应数据
  }
}
```

### 错误响应

```json
{
  "code": 400,
  "message": "错误描述",
  "error": "详细错误信息"
}
```

## 状态码说明

| 状态码 | 说明 |
|--------|------|
| 200 | 请求成功 |
| 201 | 创建成功 |
| 400 | 请求参数错误 |
| 401 | 未授权，需要登录 |
| 403 | 禁止访问，权限不足 |
| 404 | 资源不存在 |
| 422 | 数据验证失败 |
| 500 | 服务器内部错误 |

## 错误码说明

| 错误码 | 说明 |
|--------|------|
| 10001 | 参数验证失败 |
| 10002 | 用户不存在 |
| 10003 | 密码错误 |
| 10004 | 邮箱已存在 |
| 10005 | Token无效或过期 |
| 10006 | 权限不足 |
| 20001 | 数据库操作失败 |
| 20002 | 缓存操作失败 |
| 30001 | 第三方服务调用失败 |

## 数据验证规则

### 用户相关

- **email**: 必填，有效的邮箱格式，最大长度255字符
- **password**: 必填，最小长度8字符，最大长度255字符
- **name**: 可选，最大长度255字符
- **username**: 可选，最大长度255字符，只能包含字母、数字、下划线
- **nickname**: 可选，最大长度255字符

### 通用规则

- 所有字符串字段都会自动去除首尾空格
- 日期时间格式统一使用ISO 8601标准
- 分页参数：page (页码，默认1)，limit (每页数量，默认20，最大100)

## 请求示例

### cURL示例

```bash
# 用户登录
curl -X POST http://localhost:9501/api/v1/public/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "test@example.com",
    "password": "password123"
  }'

# 获取用户信息 (需要先登录获取token)
curl -X GET http://localhost:9501/api/v1/admin/auth/user \
  -H "Authorization: Bearer YOUR_JWT_TOKEN"

# 更新用户信息
curl -X PUT http://localhost:9501/api/v1/admin/auth/user \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "nickname": "新昵称",
    "phone": "13800138000"
  }'
```

### JavaScript示例

```javascript
// 用户注册
const registerUser = async (userData) => {
  const response = await fetch('/api/v1/admin/register', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(userData)
  });
  return response.json();
};

// 用户登录
const loginUser = async (credentials) => {
  const response = await fetch('/api/v1/admin/login', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(credentials)
  });
  return response.json();
};

// 获取用户信息
const getUserInfo = async (token) => {
  const response = await fetch('/api/v1/admin/auth/user', {
    method: 'GET',
    headers: {
      'Authorization': `Bearer ${token}`,
    }
  });
  return response.json();
};
```

## 限流和安全

### 请求限流

- **登录接口**: 每分钟最多5次请求
- **注册接口**: 每分钟最多3次请求
- **其他接口**: 每分钟最多100次请求

### 安全措施

- 所有密码都经过bcrypt加密存储
- JWT Token包含用户ID和过期时间
- 敏感接口需要HTTPS传输 (生产环境)
- 实施CORS策略防止跨域攻击
- 输入参数严格验证防止注入攻击

## 开发调试

### 启用调试模式

在配置文件中设置 `debug: true` 可以获得更详细的错误信息：

```yaml
app:
  debug: true
```

### API测试工具

推荐使用以下工具测试API：

- **Postman**: 图形化API测试工具
- **Insomnia**: 轻量级API测试工具
- **curl**: 命令行工具
- **HTTPie**: 用户友好的命令行工具

### 示例Postman集合

```json
{
  "info": {
    "name": "Go-Admin API",
    "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"
  },
  "item": [
    {
      "name": "用户注册",
      "request": {
        "method": "POST",
        "header": [
          {
            "key": "Content-Type",
            "value": "application/json"
          }
        ],
        "body": {
          "mode": "raw",
          "raw": "{\n  \"email\": \"test@example.com\",\n  \"password\": \"password123\",\n  \"name\": \"Test User\"\n}"
        },
        "url": {
          "raw": "{{baseUrl}}/admin/register",
          "host": ["{{baseUrl}}"],
          "path": ["admin", "register"]
        }
      }
    }
  ],
  "variable": [
    {
      "key": "baseUrl",
      "value": "http://localhost:9501/api/v1"
    }
  ]
}
```

## 版本更新

### API版本控制

当前API版本为 v1，所有接口都在 `/api/v1` 路径下。

### 向后兼容性

- 新增字段不会影响现有客户端
- 废弃的字段会在下个主版本中移除
- 重大变更会发布新的API版本

### 更新日志

- **v1.0.0** (2024-01-01): 初始版本，包含基础用户认证功能

---

如需更多API接口信息，请参考项目源代码或联系开发团队。
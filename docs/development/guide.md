# 开发者指南

## 概述

本文档为go-admin项目的开发者提供完整的开发指南，包括环境搭建、代码规范、开发流程、测试策略和最佳实践。

## 开发环境搭建

### 系统要求

- **操作系统**: Linux / macOS / Windows
- **Go版本**: 1.25.1 或更高版本
- **Git**: 用于版本控制
- **IDE**: 推荐 VS Code、GoLand 或 Vim

### 环境安装

#### 1. 安装Go语言

```bash
# Linux/macOS (使用官方安装包)
wget https://golang.org/dl/go1.25.1.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.25.1.linux-amd64.tar.gz

# 配置环境变量
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
echo 'export GOPATH=$HOME/go' >> ~/.bashrc
echo 'export GO111MODULE=on' >> ~/.bashrc
source ~/.bashrc

# 验证安装
go version
```

#### 2. 安装开发工具

```bash
# 安装常用Go工具
go install golang.org/x/tools/cmd/goimports@latest
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
go install github.com/swaggo/swag/cmd/swag@latest

# 安装Ent代码生成工具
go install entgo.io/ent/cmd/ent@latest
```

#### 3. 克隆项目

```bash
# 克隆主项目
git clone <repository-url> go-admin
cd go-admin

# 克隆CMF框架 (本地依赖)
cd ..
git clone <cmf-repository-url> cmf
cd go-admin
```

#### 4. 安装依赖

```bash
# 下载依赖
go mod download

# 验证依赖
go mod verify

# 整理依赖
go mod tidy
```

### 数据库设置

#### 开发数据库

```bash
# 启动MySQL (Docker方式)
docker run --name mysql-dev \
  -e MYSQL_ROOT_PASSWORD=root \
  -e MYSQL_DATABASE=cmf \
  -p 3306:3306 \
  -d mysql:8.0

# 或使用本地MySQL
mysql -u root -p
CREATE DATABASE cmf CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

#### Redis设置 (可选)

```bash
# 启动Redis (Docker方式)
docker run --name redis-dev -p 6379:6379 -d redis:7-alpine

# 或使用本地Redis
redis-server
```

### IDE配置

#### VS Code配置

安装推荐插件：

```json
{
  "recommendations": [
    "golang.go",
    "ms-vscode.vscode-json",
    "redhat.vscode-yaml",
    "ms-vscode.vscode-typescript-next"
  ]
}
```

配置settings.json：

```json
{
  "go.useLanguageServer": true,
  "go.formatTool": "goimports",
  "go.lintTool": "golangci-lint",
  "go.testFlags": ["-v"],
  "editor.formatOnSave": true
}
```

## 项目结构

### 目录结构

```
go-admin/
├── main.go                 # 应用入口
├── go.mod                  # Go模块定义
├── go.sum                  # 依赖校验和
├── .golangci.yml           # 代码质量检查配置
├── config/
│   └── config.yml          # 配置文件
├── internal/               # 内部包
│   ├── init.go            # 初始化逻辑
│   ├── router.go          # 路由定义
│   ├── cleanup.go         # 清理逻辑
│   ├── ent/               # Ent ORM生成代码
│   │   ├── schema/        # 数据模型定义
│   │   │   ├── user.go
│   │   │   └── casbinrule.go
│   │   └── ...            # 生成的ORM代码
│   ├── global/            # 全局变量和常量
│   │   ├── global.go      # 全局变量
│   │   └── msg.go         # 响应消息常量
│   ├── handler/           # HTTP处理器
│   │   └── public.go      # 用户相关处理器
│   ├── service/           # 业务逻辑层
│   │   └── user.go        # 用户服务
│   └── utils/             # 工具函数
│       ├── response.go    # 响应工具
│       └── utils.go       # 通用工具
├── web/                   # 前端资源
│   └── dist/              # 构建后的前端文件
├── data/                  # 数据目录
│   ├── logs/              # 日志文件
│   └── storage/           # 文件存储
└── docs/                  # 项目文档
    ├── database/
    ├── architecture/
    ├── deployment/
    └── development/
```

### 包组织原则

1. **internal包**: 项目内部代码，不对外暴露
2. **分层架构**: handler -> service -> ent (数据访问)
3. **单一职责**: 每个包有明确的职责边界
4. **依赖方向**: 上层依赖下层，避免循环依赖

## 代码规范

### Go代码规范

#### 1. 命名规范

```go
// 包名：小写，简洁，有意义
package handler

// 常量：大写，使用下划线分隔
const (
    MAX_RETRY_COUNT = 3
    DEFAULT_TIMEOUT = 30
)

// 变量：驼峰命名
var userService *UserService

// 函数：驼峰命名，公开函数首字母大写
func GetUser(c *fiber.Ctx) error { }
func validateEmail(email string) bool { }

// 结构体：驼峰命名，公开结构体首字母大写
type UserService struct {
    db *ent.Client
}

// 接口：以er结尾或描述性名称
type UserRepository interface {
    Create(user *User) error
    GetByID(id int) (*User, error)
}
```

#### 2. 注释规范

```go
// Package handler provides HTTP request handlers for the admin API.
package handler

// UserService handles user-related business logic.
type UserService struct {
    db *ent.Client
}

// NewUserService creates a new UserService instance.
// It requires a valid database client.
func NewUserService(db *ent.Client) *UserService {
    return &UserService{db: db}
}

// Login authenticates a user with email and password.
// Returns JWT token on success, error on failure.
func (s *UserService) Login(email, password string) (string, error) {
    // Implementation details...
}
```

#### 3. 错误处理

```go
// 使用标准错误处理模式
func GetUser(c *fiber.Ctx) error {
    id, err := strconv.Atoi(c.Params("id"))
    if err != nil {
        return c.Status(400).JSON(fiber.Map{
            "error": "Invalid user ID",
        })
    }

    user, err := userService.GetByID(id)
    if err != nil {
        if ent.IsNotFound(err) {
            return c.Status(404).JSON(fiber.Map{
                "error": "User not found",
            })
        }
        return c.Status(500).JSON(fiber.Map{
            "error": "Internal server error",
        })
    }

    return c.JSON(user)
}

// 自定义错误类型
type ValidationError struct {
    Field   string
    Message string
}

func (e ValidationError) Error() string {
    return fmt.Sprintf("validation failed for %s: %s", e.Field, e.Message)
}
```

#### 4. 结构体标签

```go
type User struct {
    ID       int    `json:"id" db:"id"`
    Email    string `json:"email" db:"email" validate:"required,email"`
    Name     string `json:"name" db:"name" validate:"required,min=2,max=50"`
    Status   string `json:"status" db:"status" validate:"oneof=active inactive suspended"`
}
```

### 数据库规范

#### 1. Ent Schema定义

```go
// Fields定义规范
func (User) Fields() []ent.Field {
    return []ent.Field{
        // 主键字段
        field.Int("id").
            Immutable().
            Unique().
            StructTag(`json:"id,omitempty"`),
        
        // 字符串字段
        field.String("email").
            Unique().
            MaxLen(255).
            Comment("用户邮箱地址"),
        
        // 可选字段
        field.String("name").
            Optional().
            MaxLen(255).
            Comment("用户真实姓名"),
        
        // 枚举字段
        field.Enum("status").
            Values("active", "inactive", "suspended").
            Default("active").
            Comment("用户状态"),
    }
}

// 索引定义
func (User) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("email").Unique(),
        index.Fields("status"),
    }
}
```

#### 2. 数据库操作

```go
// 查询操作
func (s *UserService) GetByEmail(email string) (*ent.User, error) {
    return s.db.User.
        Query().
        Where(user.EmailEQ(email)).
        Only(context.Background())
}

// 创建操作
func (s *UserService) Create(req CreateUserRequest) (*ent.User, error) {
    return s.db.User.
        Create().
        SetEmail(req.Email).
        SetName(req.Name).
        SetPassword(hashPassword(req.Password)).
        Save(context.Background())
}

// 更新操作
func (s *UserService) Update(id int, req UpdateUserRequest) (*ent.User, error) {
    return s.db.User.
        UpdateOneID(id).
        SetName(req.Name).
        SetStatus(req.Status).
        Save(context.Background())
}

// 事务操作
func (s *UserService) CreateWithProfile(userReq CreateUserRequest, profileReq CreateProfileRequest) error {
    tx, err := s.db.Tx(context.Background())
    if err != nil {
        return err
    }
    defer tx.Rollback()

    user, err := tx.User.Create().
        SetEmail(userReq.Email).
        SetName(userReq.Name).
        Save(context.Background())
    if err != nil {
        return err
    }

    _, err = tx.Profile.Create().
        SetUserID(user.ID).
        SetBio(profileReq.Bio).
        Save(context.Background())
    if err != nil {
        return err
    }

    return tx.Commit()
}
```

## 开发流程

### 1. 功能开发流程

#### 创建新功能

```bash
# 1. 创建功能分支
git checkout -b feature/user-management

# 2. 如果需要新的数据模型
cd internal/ent/schema
# 创建或修改schema文件

# 3. 生成Ent代码
go generate ./internal/ent

# 4. 实现业务逻辑
# 创建service层代码
# 创建handler层代码
# 更新路由配置

# 5. 测试功能
go test ./...

# 6. 提交代码
git add .
git commit -m "feat: add user management functionality"
git push origin feature/user-management
```

#### 添加新的API端点

1. **定义数据模型** (如需要)
```go
// internal/ent/schema/article.go
func (Article) Fields() []ent.Field {
    return []ent.Field{
        field.Int("id").Immutable().Unique(),
        field.String("title").MaxLen(255),
        field.Text("content"),
        field.Int("author_id"),
    }
}
```

2. **实现Service层**
```go
// internal/service/article.go
type ArticleService struct {
    db *ent.Client
}

func (s *ArticleService) Create(req CreateArticleRequest) (*ent.Article, error) {
    // 实现创建逻辑
}
```

3. **实现Handler层**
```go
// internal/handler/article.go
func CreateArticle(c *fiber.Ctx) error {
    var req CreateArticleRequest
    if err := c.BodyParser(&req); err != nil {
        return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
    }
    
    // 调用service层
    article, err := articleService.Create(req)
    if err != nil {
        return c.Status(500).JSON(fiber.Map{"error": err.Error()})
    }
    
    return c.Status(201).JSON(article)
}
```

4. **注册路由**
```go
// internal/router.go
func RegisterRoute(app *fiber.App, config *config.Config) {
    // 现有路由...
    
    // 新增文章路由
    articles := adminAuth.Group("/articles")
    articles.Post("/", handler.CreateArticle)
    articles.Get("/", handler.ListArticles)
    articles.Get("/:id", handler.GetArticle)
    articles.Put("/:id", handler.UpdateArticle)
    articles.Delete("/:id", handler.DeleteArticle)
}
```

### 2. 数据库迁移

#### 修改Schema

```bash
# 1. 修改schema文件
vim internal/ent/schema/user.go

# 2. 重新生成代码
go generate ./internal/ent

# 3. 应用会在启动时自动迁移
# 或手动运行迁移
go run main.go
```

#### Schema版本管理

```go
// 使用Ent的版本化迁移 (高级用法)
func migrate(ctx context.Context, client *ent.Client) error {
    return client.Schema.Create(
        ctx,
        migrate.WithDropIndex(true),
        migrate.WithDropColumn(true),
    )
}
```

### 3. 配置管理

#### 添加新配置项

```yaml
# config/config.yml
app:
  name: admin-api
  # 新增配置项
  max_upload_size: 10485760  # 10MB
  allowed_origins:
    - "http://localhost:3000"
    - "https://admin.example.com"

# 新增配置段
email:
  smtp_host: smtp.gmail.com
  smtp_port: 587
  username: ""
  password: ""
```

#### 在代码中使用配置

```go
// 通过CMF框架访问配置
func SendEmail(config *config.Config, to, subject, body string) error {
    emailConfig := config.Email
    // 使用配置发送邮件
}
```

## 测试策略

### 1. 单元测试

#### 测试文件组织

```
internal/
├── service/
│   ├── user.go
│   └── user_test.go
├── handler/
│   ├── public.go
│   └── public_test.go
```

#### Service层测试

```go
// internal/service/user_test.go
package service

import (
    "testing"
    "context"
    
    "go-admin/internal/ent/enttest"
    _ "github.com/mattn/go-sqlite3"
)

func TestUserService_Create(t *testing.T) {
    // 创建测试数据库
    client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
    defer client.Close()
    
    service := NewUserService(client)
    
    tests := []struct {
        name    string
        req     CreateUserRequest
        wantErr bool
    }{
        {
            name: "valid user",
            req: CreateUserRequest{
                Email:    "test@example.com",
                Name:     "Test User",
                Password: "password123",
            },
            wantErr: false,
        },
        {
            name: "invalid email",
            req: CreateUserRequest{
                Email:    "invalid-email",
                Name:     "Test User",
                Password: "password123",
            },
            wantErr: true,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            user, err := service.Create(tt.req)
            if (err != nil) != tt.wantErr {
                t.Errorf("Create() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if !tt.wantErr && user.Email != tt.req.Email {
                t.Errorf("Create() user.Email = %v, want %v", user.Email, tt.req.Email)
            }
        })
    }
}
```

#### Handler层测试

```go
// internal/handler/public_test.go
package handler

import (
    "bytes"
    "encoding/json"
    "net/http/httptest"
    "testing"
    
    "github.com/gofiber/fiber/v2"
    "github.com/stretchr/testify/assert"
)

func TestLogin(t *testing.T) {
    app := fiber.New()
    app.Post("/login", Login)
    
    tests := []struct {
        name           string
        body           map[string]string
        expectedStatus int
    }{
        {
            name: "valid login",
            body: map[string]string{
                "email":    "test@example.com",
                "password": "password123",
            },
            expectedStatus: 200,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            bodyBytes, _ := json.Marshal(tt.body)
            req := httptest.NewRequest("POST", "/login", bytes.NewReader(bodyBytes))
            req.Header.Set("Content-Type", "application/json")
            
            resp, err := app.Test(req)
            assert.NoError(t, err)
            assert.Equal(t, tt.expectedStatus, resp.StatusCode)
        })
    }
}
```

### 2. 集成测试

```go
// tests/integration/api_test.go
package integration

import (
    "testing"
    "net/http/httptest"
    
    "go-admin/internal"
    "github.com/gofiber/fiber/v2"
)

func TestAPIIntegration(t *testing.T) {
    // 设置测试环境
    app := fiber.New()
    config := loadTestConfig()
    
    // 初始化应用
    internal.Init(config)
    internal.RegisterRoute(app, config)
    
    // 测试API端点
    t.Run("POST /api/v1/admin/register", func(t *testing.T) {
        // 测试用户注册
    })
    
    t.Run("POST /api/v1/admin/login", func(t *testing.T) {
        // 测试用户登录
    })
}
```

### 3. 运行测试

```bash
# 运行所有测试
go test ./...

# 运行特定包的测试
go test ./internal/service

# 运行测试并显示覆盖率
go test -cover ./...

# 生成覆盖率报告
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html

# 运行基准测试
go test -bench=. ./...
```

### 4. 代码质量检查

项目使用golangci-lint进行代码质量检查：

```bash
# 运行代码质量检查
golangci-lint run

# 运行检查并自动修复可修复的问题
golangci-lint run --fix

# 检查特定目录
golangci-lint run ./internal/...

# 查看配置信息
golangci-lint config path
```

#### 配置说明

项目的golangci-lint配置文件 `.golangci.yml` 包含以下设置：

- **跳过目录**: `tmp`, `web` - 跳过临时文件和前端资源
- **跳过文件**: 测试文件 (`*_test.go`) - 测试文件使用相对宽松的规则
- **启用的检查器**: 
  - `gofmt` - 代码格式检查
  - `golint` - 代码风格检查
  - `govet` - 静态分析检查
- **排除规则**: Ent生成的代码跳过golint检查

#### 集成到开发流程

```bash
# 提交前检查脚本
#!/bin/bash
echo "Running code quality checks..."
golangci-lint run
if [ $? -ne 0 ]; then
    echo "Code quality check failed!"
    exit 1
fi

echo "Running tests..."
go test ./...
if [ $? -ne 0 ]; then
    echo "Tests failed!"
    exit 1
fi

echo "All checks passed!"
```

## 调试和性能优化

### 1. 调试技巧

#### 使用调试器

```bash
# 安装delve调试器
go install github.com/go-delve/delve/cmd/dlv@latest

# 启动调试
dlv debug main.go

# 在代码中设置断点
(dlv) break main.main
(dlv) break internal/handler.Login
(dlv) continue
```

#### 日志调试

```go
import (
    fiberlog "github.com/gofiber/fiber/v2/log"
)

func Login(c *fiber.Ctx) error {
    fiberlog.Infof("Login attempt from IP: %s", c.IP())
    
    var req LoginRequest
    if err := c.BodyParser(&req); err != nil {
        fiberlog.Errorf("Failed to parse login request: %v", err)
        return c.Status(400).JSON(fiber.Map{"error": "Invalid request"})
    }
    
    fiberlog.Debugf("Login request for email: %s", req.Email)
    // 处理登录逻辑...
}
```

### 2. 性能监控

#### 添加性能指标

```go
import (
    "time"
    "github.com/gofiber/fiber/v2/middleware/monitor"
)

// 添加监控中间件
app.Get("/metrics", monitor.New())

// 自定义性能监控
func performanceMiddleware() fiber.Handler {
    return func(c *fiber.Ctx) error {
        start := time.Now()
        
        err := c.Next()
        
        duration := time.Since(start)
        fiberlog.Infof("Request %s %s took %v", c.Method(), c.Path(), duration)
        
        return err
    }
}
```

#### 数据库性能优化

```go
// 使用数据库连接池
func initDatabase(config *config.Config) error {
    db, err := sql.Open("mysql", dsn)
    if err != nil {
        return err
    }
    
    // 设置连接池参数
    db.SetMaxOpenConns(25)
    db.SetMaxIdleConns(5)
    db.SetConnMaxLifetime(5 * time.Minute)
    
    // 创建Ent客户端
    drv := entsql.OpenDB("mysql", db)
    client := ent.NewClient(ent.Driver(drv))
    
    return nil
}

// 优化查询
func (s *UserService) GetActiveUsers() ([]*ent.User, error) {
    return s.db.User.
        Query().
        Where(user.StatusEQ("active")).
        Select(user.FieldID, user.FieldEmail, user.FieldName). // 只选择需要的字段
        Limit(100). // 限制结果数量
        All(context.Background())
}
```

## 最佳实践

### 1. 代码组织

- **单一职责原则**: 每个函数和结构体只负责一个功能
- **依赖注入**: 通过参数传递依赖，便于测试
- **接口设计**: 定义清晰的接口，降低耦合度
- **错误处理**: 统一的错误处理策略

### 2. 安全实践

```go
// 输入验证
func validateCreateUserRequest(req CreateUserRequest) error {
    if len(req.Email) == 0 {
        return errors.New("email is required")
    }
    if !isValidEmail(req.Email) {
        return errors.New("invalid email format")
    }
    if len(req.Password) < 8 {
        return errors.New("password must be at least 8 characters")
    }
    return nil
}

// 密码加密
func hashPassword(password string) string {
    hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
    return string(hash)
}

// SQL注入防护 (Ent自动处理)
func (s *UserService) GetByEmail(email string) (*ent.User, error) {
    // Ent会自动处理SQL注入防护
    return s.db.User.Query().Where(user.EmailEQ(email)).Only(context.Background())
}
```

### 3. 性能优化

- **数据库索引**: 为常用查询字段添加索引
- **缓存策略**: 使用Redis缓存热点数据
- **连接池**: 合理配置数据库连接池
- **异步处理**: 对于耗时操作使用异步处理

### 4. 监控和日志

```go
// 结构化日志
fiberlog.Infof("User login successful", 
    "user_id", user.ID,
    "email", user.Email,
    "ip", c.IP(),
    "user_agent", c.Get("User-Agent"),
)

// 错误追踪
func handleError(c *fiber.Ctx, err error, message string) error {
    fiberlog.Errorf("Error: %s, Details: %v", message, err)
    
    // 在生产环境中，可以发送到错误追踪服务
    // sentry.CaptureException(err)
    
    return c.Status(500).JSON(fiber.Map{
        "error": message,
    })
}
```

## 常见问题

### 1. 依赖管理

**问题**: go.mod中的replace指令导致部署问题

**解决方案**:
```bash
# 开发环境使用本地依赖
replace github.com/wuwuseo/cmf => ../cmf

# 生产环境使用远程依赖
# replace github.com/wuwuseo/cmf => ../cmf
```

### 2. 数据库迁移

**问题**: Schema变更导致数据丢失

**解决方案**:
- 在生产环境中禁用自动迁移
- 使用版本化的迁移脚本
- 在迁移前备份数据

### 3. 性能问题

**问题**: API响应时间过长

**排查步骤**:
1. 检查数据库查询性能
2. 分析慢查询日志
3. 优化数据库索引
4. 添加缓存层
5. 使用性能分析工具

```bash
# 使用pprof进行性能分析
go tool pprof http://localhost:9501/debug/pprof/profile
```

## 贡献指南

### 1. 代码提交规范

```bash
# 提交信息格式
<type>(<scope>): <subject>

# 类型说明
feat: 新功能
fix: 修复bug
docs: 文档更新
style: 代码格式调整
refactor: 代码重构
test: 测试相关
chore: 构建过程或辅助工具的变动

# 示例
feat(user): add user registration functionality
fix(auth): resolve JWT token validation issue
docs(api): update API documentation
```

### 2. Pull Request流程

1. Fork项目到个人仓库
2. 创建功能分支
3. 编写代码和测试
4. 提交Pull Request
5. 代码审查
6. 合并到主分支

### 3. 代码审查清单

- [ ] 代码符合项目规范
- [ ] 包含必要的测试
- [ ] 文档已更新
- [ ] 没有引入安全漏洞
- [ ] 性能影响可接受
- [ ] 向后兼容性

通过遵循本开发指南，您可以高效地参与go-admin项目的开发，并确保代码质量和项目的可维护性。
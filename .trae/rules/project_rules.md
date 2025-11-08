# 产品概述

Go-Admin 是一个基于Go语言构建的现代化管理后台API系统，专为高性能Web应用程序设计，提供全面的用户管理和认证功能。

## 核心特性

- **高性能Web API**: 基于Fiber框架构建，提供卓越的性能表现
- **类型安全的数据库操作**: 使用Ent ORM确保数据库交互的类型安全
- **JWT认证**: 安全的基于令牌的认证系统
- **RBAC授权**: 基于Casbin的角色访问控制
- **缓存支持**: 内存缓存和Redis缓存能力
- **文件存储**: 本地存储和S3兼容的云存储支持
- **SPA集成**: 内嵌前端单页面应用
- **配置管理**: 灵活的基于YAML的配置管理

## 目标应用场景

- 管理仪表板和管理系统
- 用户认证和授权服务
- 内容管理系统
- Web应用程序的API后端

## 核心业务逻辑

- 用户注册、登录和个人资料管理
- 密码加密和安全
- JWT令牌生成和验证
- 基于角色的权限管理
- 文件上传和存储管理

# 项目结构与组织

## 目录结构

```
go-admin/
├── main.go                 # 应用程序入口
├── go.mod                  # Go模块定义
├── go.sum                  # Go模块依赖校验
├── .gitignore             # Git忽略文件配置
├── .golangci.yml          # 代码质量配置
├── .trae/                 # Trae IDE配置
│   └── rules/             # 项目规则配置
├── .kiro/                 # Kiro配置
├── config/
│   └── config.yml         # 应用程序配置
├── internal/              # 内部包 (不可导入)
│   ├── init.go           # 初始化逻辑
│   ├── router.go         # 路由定义
│   ├── cleanup.go        # 清理逻辑
│   ├── ent/              # Ent ORM生成代码
│   │   ├── schema/       # 数据模型定义
│   │   ├── migrate/      # 数据库迁移代码
│   │   ├── hook/         # 数据库钩子
│   │   └── runtime/      # 运行时代码
│   ├── global/           # 全局变量和常量
│   │   ├── global.go     # 全局变量
│   │   └── msg.go        # 响应消息常量
│   ├── handler/          # HTTP请求处理器
│   ├── service/          # 业务逻辑层
│   ├── casbin/           # 授权规则和策略
│   │   └── adapter/      # Casbin适配器
│   └── utils/            # 工具函数
│       ├── response.go   # 响应工具
│       └── utils.go      # 通用工具
├── web/                  # 前端资源
│   └── dist/             # 构建后的前端文件 (内嵌)
├── data/                 # 运行时数据
│   ├── logs/             # 应用程序日志
│   └── storage/          # 文件存储
├── tmp/                  # 临时文件目录
└── docs/                 # 项目文档
    ├── docs.go          # Swagger文档生成代码
    ├── swagger.json     # Swagger JSON文档
    └── swagger.yaml     # Swagger YAML文档
```

## 架构模式

### 分层架构
- **处理器层**: HTTP请求/响应处理
- **服务层**: 业务逻辑实现
- **数据层**: 通过Ent ORM进行数据库操作

### 依赖注入
- 使用CMF框架的依赖注入来管理服务
- 通过中间件locals传递依赖
- 尽可能避免全局状态

### 路由组织
```go
// 路由结构模式
api := app.Group("/api")
v1 := api.Group("/v1")
admin := v1.Group("/admin")
adminPublic := v1.Group("/public")  // 无需认证
adminAuth := admin.Group("/auth")   // 需要JWT认证
```

## 文件命名约定

### Go文件
- 使用snake_case命名文件: `user_service.go`
- 测试文件: `user_service_test.go`
- 接口文件: `user_repository.go`

### 包结构
- 每个目录一个包
- 包名应该是小写的单个单词
- 避免使用通用名称如 `util`, `common`, `base`

## 代码组织规则

### 处理器函数
- 保持处理器简洁 - 委托给服务层
- 通过 `utils.Response` 使用一致的响应格式
- 使用适当的HTTP状态码优雅地处理错误

### 服务层
- 在此实现业务逻辑
- 返回领域错误，而不是HTTP错误
- 对仓储/外部服务使用依赖注入

### 数据库模型
- 在 `internal/ent/schema/` 中定义Ent模式
- 使用适当的字段验证和约束
- 遵循带有 `cmf_` 前缀的数据库命名约定

### 配置
- 所有配置都在 `config/config.yml` 中
- 敏感数据使用环境变量
- 启动时验证配置

### 错误处理
- 使用结构化错误响应
- 使用适当级别记录错误
- 返回用户友好的错误消息

### 测试
- 服务层的单元测试
- 处理器的集成测试
- 使用testify进行断言
- 模拟外部依赖

## 导入组织
```go
// 标准库导入
import (
    "context"
    "fmt"
)

// 第三方导入
import (
    "github.com/gofiber/fiber/v2"
    "entgo.io/ent"
)

// 本地导入
import (
    "go-admin/internal/service"
    "go-admin/internal/global"
)
```

# 技术栈

## 核心框架
- **Go**: 1.25.1 (主要编程语言)
- **Fiber**: v2.52.9 (高性能HTTP Web框架)
- **CMF**: v0.0.0 (自定义基础框架)

## 数据库与ORM
- **Ent**: v0.14.5 (类型安全的ORM框架)
- **MySQL**: 5.7+ / 8.0+ (主数据库)
- **MySQL驱动**: github.com/go-sql-driver/mysql v1.9.3

## 认证与安全
- **JWT**: github.com/golang-jwt/jwt/v5 v5.3.0 (JSON Web Token认证)
- **Casbin**: github.com/casbin/casbin/v2 v2.132.0 (基于RBAC的权限控制)
- **Zap**: go.uber.org/zap v1.27.0 (日志记录)

## 缓存与存储
- **Redis**: github.com/redis/go-redis/v9 v9.16.0 (缓存和会话存储)
- **BigCache**: github.com/allegro/bigcache/v3 v3.1.0 (内存缓存)
- **S3**: AWS S3兼容的存储支持 (github.com/aws/aws-sdk-go-v2 v1.38.2)
- **GoCache**: github.com/eko/gocache v4.2.2 (缓存管理)

## 配置管理
- **Viper**: github.com/spf13/viper v1.21.0 (配置管理)
- **YAML**: go.yaml.in/yaml/v3 v3.0.4 (YAML解析)

## 开发工具
- **golangci-lint**: 代码质量检查和代码规范
- **Swagger**: github.com/swaggo/swag v1.16.4 (API文档生成)
- **Fiber Swagger**: github.com/gofiber/swagger v1.1.1 (Fiber Swagger中间件)
- **Air**: 开发热重载工具 (推荐)
- **Mock**: github.com/golang/mock v1.6.0 (模拟测试)

## 构建系统与命令

### 开发环境
```bash
# 开发模式运行
go run main.go

# 热重载 (需要先安装air)
go install github.com/cosmtrek/air@latest
air
```

### 构建
```bash
# 开发构建
go build -o go-admin main.go

# 生产构建 (优化版本)
CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-w -s' -o go-admin main.go
```

### 测试
```bash
# 运行所有测试
go test ./...

# 运行测试并显示覆盖率
go test -cover ./...

# 生成覆盖率报告
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html
```

### 代码质量
```bash
# 运行代码检查
golangci-lint run

# 自动修复问题
golangci-lint run --fix

# 格式化代码
gofmt -w .
go mod tidy
```

### 数据库操作
```bash
# 生成Ent模式
go run entgo.io/ent/cmd/ent new ModelName

# 生成Ent代码
go generate ./internal/ent
```

### API文档生成
```bash
# 安装Swag工具
go install github.com/swaggo/swag/cmd/swag@latest

# 生成Swagger文档
swag init
```

## 配置管理
- **Viper**: 配置管理
- **YAML**: 主要配置格式 (config/config.yml)
- 支持基于环境变量的配置
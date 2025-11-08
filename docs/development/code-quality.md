# 代码质量保证

## 概述

go-admin项目使用golangci-lint作为代码质量检查工具，确保代码符合Go语言最佳实践和项目规范。

## golangci-lint配置

### 配置文件

项目根目录的 `.golangci.yml` 文件包含了完整的代码质量检查配置：

```yaml
run:
  # 要跳过的目录列表
  skip-dirs:
    - tmp
    - web
  
  # 要跳过的文件列表（支持正则表达式）
  skip-files:
    - ".*_test\\.go$"
  
  # 默认超时时间
  timeout: 5m

linters:
  # 禁用所有默认启用的linters
  disable-all: false
  # 启用额外的linters
  enable:
    - gofmt
    - golint
    - govet
  
  # 禁用特定的linters
  disable:
    - maligned
    - prealloc

issues:
  # 排除特定的issue
  exclude-rules:
    # 排除生成代码的检查
    - path: internal/ent
      linters:
        - golint
  
  # 最大问题数，0表示无限制
  max-issues-per-linter: 0
  max-same-issues: 0
```

### 配置说明

#### 运行配置 (run)

- **skip-dirs**: 跳过不需要检查的目录
  - `tmp`: 临时文件目录
  - `web`: 前端资源目录
- **skip-files**: 跳过测试文件的检查，因为测试文件可以使用相对宽松的规则
- **timeout**: 设置检查超时时间为5分钟

#### 检查器配置 (linters)

**启用的检查器**:
- `gofmt`: 检查代码格式是否符合Go标准
- `golint`: 检查代码风格和命名规范
- `govet`: 进行静态分析，发现潜在的bug

**禁用的检查器**:
- `maligned`: 结构体字段对齐检查（在某些情况下过于严格）
- `prealloc`: 预分配slice检查（可能影响代码可读性）

#### 问题配置 (issues)

- **exclude-rules**: 排除Ent生成代码的golint检查，因为生成的代码不需要遵循所有风格规范
- **max-issues-per-linter**: 设置为0表示不限制每个检查器的问题数量
- **max-same-issues**: 设置为0表示不限制相同问题的数量

## 使用方法

### 基本命令

```bash
# 运行所有检查
golangci-lint run

# 运行检查并自动修复可修复的问题
golangci-lint run --fix

# 检查特定目录
golangci-lint run ./internal/...

# 检查特定文件
golangci-lint run internal/handler/public.go

# 只运行特定的检查器
golangci-lint run --enable-only=gofmt,govet

# 查看配置信息
golangci-lint config path
```

### 高级用法

```bash
# 输出详细信息
golangci-lint run --verbose

# 输出JSON格式结果
golangci-lint run --out-format=json

# 设置自定义配置文件
golangci-lint run --config=.golangci-custom.yml

# 显示所有可用的检查器
golangci-lint linters

# 缓存清理
golangci-lint cache clean
```

## 集成到开发流程

### Git Hooks

创建 `.git/hooks/pre-commit` 文件：

```bash
#!/bin/bash
echo "Running golangci-lint..."
golangci-lint run
if [ $? -ne 0 ]; then
    echo "❌ Code quality check failed!"
    echo "Please fix the issues above before committing."
    exit 1
fi
echo "✅ Code quality check passed!"
```

使脚本可执行：
```bash
chmod +x .git/hooks/pre-commit
```

### CI/CD集成

#### GitHub Actions

```yaml
name: Code Quality
on: [push, pull_request]
jobs:
  golangci:
    name: lint
    runs-on: ubuntu-latest
    steps:
    - uses: actions/checkout@v3
    - uses: actions/setup-go@v3
      with:
        go-version: 1.25.1
    - name: golangci-lint
      uses: golangci/golangci-lint-action@v3
      with:
        version: latest
```

#### GitLab CI

```yaml
lint:
  stage: test
  image: golangci/golangci-lint:latest
  script:
    - golangci-lint run
```

### IDE集成

#### VS Code

安装Go插件后，在 `settings.json` 中添加：

```json
{
  "go.lintTool": "golangci-lint",
  "go.lintFlags": ["--fast"]
}
```

#### GoLand

1. 打开 Settings → Tools → File Watchers
2. 添加新的File Watcher
3. 设置Program为golangci-lint路径
4. 设置Arguments为 `run $FileDir$`

## 常见问题和解决方案

### 1. 检查器冲突

**问题**: 不同检查器给出相互冲突的建议

**解决方案**: 在配置文件中禁用冲突的检查器，或使用exclude-rules排除特定情况

### 2. 生成代码问题

**问题**: Ent生成的代码触发大量lint警告

**解决方案**: 已在配置中排除，如需要更多排除规则：

```yaml
issues:
  exclude-rules:
    - path: internal/ent
      linters:
        - golint
        - gofmt
```

### 3. 性能问题

**问题**: golangci-lint运行缓慢

**解决方案**:
- 使用 `--fast` 参数
- 增加timeout时间
- 排除不必要的目录

```bash
golangci-lint run --fast --timeout=10m
```

### 4. 误报问题

**问题**: 检查器报告了不应该修复的问题

**解决方案**: 使用注释禁用特定行的检查

```go
//nolint:golint
func someFunction() {
    // 这行不会被golint检查
}

//nolint:govet
var globalVar = "some value"
```

## 自定义规则

### 添加新的检查器

在 `.golangci.yml` 中启用更多检查器：

```yaml
linters:
  enable:
    - gofmt
    - golint
    - govet
    - errcheck      # 检查错误处理
    - gosimple      # 简化代码建议
    - ineffassign   # 检查无效赋值
    - staticcheck   # 静态分析
    - unused        # 检查未使用的代码
```

### 自定义排除规则

```yaml
issues:
  exclude-rules:
    # 排除测试文件的某些检查
    - path: _test\.go
      linters:
        - errcheck
        - gosec
    
    # 排除特定函数的检查
    - text: "should have comment"
      linters:
        - golint
```

## 最佳实践

### 1. 渐进式采用

- 从基本的检查器开始（gofmt, govet）
- 逐步添加更严格的检查器
- 为现有代码设置合理的排除规则

### 2. 团队协作

- 确保所有团队成员使用相同的配置
- 在CI/CD中强制执行代码质量检查
- 定期审查和更新配置

### 3. 性能优化

- 使用缓存加速重复检查
- 合理设置超时时间
- 排除不必要的文件和目录

### 4. 持续改进

- 定期更新golangci-lint版本
- 关注新的检查器和功能
- 根据项目需求调整配置

通过合理配置和使用golangci-lint，可以显著提高代码质量，减少潜在的bug，并保持代码风格的一致性。
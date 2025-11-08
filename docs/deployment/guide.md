# 部署指南

## 概述

本文档提供go-admin项目的完整部署指南，包括环境要求、安装步骤、配置说明和运维指导。

## 环境要求

### 系统要求

- **操作系统**: Linux (推荐 Ubuntu 20.04+, CentOS 8+) / macOS / Windows
- **架构**: x86_64 / ARM64
- **内存**: 最小 512MB，推荐 2GB+
- **存储**: 最小 1GB 可用空间

### 软件依赖

#### 必需组件

1. **Go语言环境**
   - 版本: Go 1.25.1 或更高版本
   - 下载地址: https://golang.org/dl/
   - 验证安装: `go version`

2. **MySQL数据库**
   - 版本: MySQL 5.7+ 或 MySQL 8.0+
   - 字符集: UTF-8 (utf8mb4)
   - 存储引擎: InnoDB

#### 可选组件

1. **Redis缓存**
   - 版本: Redis 5.0+
   - 用途: 缓存和会话存储
   - 如不使用Redis，系统将使用内存缓存

2. **反向代理**
   - Nginx (推荐)
   - Apache HTTP Server
   - 用于生产环境的负载均衡和SSL终止

## 安装步骤

### 1. 获取源代码

```bash
# 克隆项目
git clone <repository-url> go-admin
cd go-admin

# 或者下载发布版本
wget <release-url>
tar -xzf go-admin-v1.0.0.tar.gz
cd go-admin
```

### 2. 安装依赖

```bash
# 下载Go模块依赖
go mod download

# 验证依赖
go mod verify
```

**注意**: 项目使用了本地CMF框架依赖，确保 `../cmf` 目录存在并包含CMF框架代码。

### 3. 数据库准备

#### 创建数据库

```sql
-- 连接到MySQL
mysql -u root -p

-- 创建数据库
CREATE DATABASE cmf CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 创建用户 (可选，推荐用于生产环境)
CREATE USER 'go_admin'@'localhost' IDENTIFIED BY 'your_password';
GRANT ALL PRIVILEGES ON cmf.* TO 'go_admin'@'localhost';
FLUSH PRIVILEGES;
```

#### 数据库配置

数据库表结构将在应用首次启动时自动创建 (Ent ORM自动迁移)。

### 4. 配置文件设置

复制并修改配置文件：

```bash
# 复制配置模板 (如果存在)
cp config/config.yml.example config/config.yml

# 或直接编辑配置文件
vim config/config.yml
```

### 5. 代码质量检查 (推荐)

```bash
# 运行代码质量检查
golangci-lint run

# 如果有问题，修复后再构建
golangci-lint run --fix
```

### 6. 构建应用

```bash
# 开发环境构建
go build -o go-admin main.go

# 生产环境构建 (优化版本)
CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-w -s' -o go-admin main.go
```

### 7. 前端资源

确保前端资源已构建并放置在 `web/dist` 目录：

```bash
# 检查前端资源
ls -la web/dist/

# 应该包含 index.html 和其他静态资源
```

## 配置说明

### 应用配置 (app)

```yaml
app:
  name: admin-api          # 应用名称
  port: 9501              # 监听端口
  debug: false            # 调试模式 (生产环境设为false)
  idle_timeout: 60        # 空闲超时 (秒)
  prefork: false          # 预分叉模式 (Linux生产环境可启用)
  swagger: true           # Swagger文档 (生产环境可关闭)
  secret: your_jwt_secret # JWT密钥 (必须修改)
```

**重要**: 生产环境必须修改 `secret` 为强密码！

### 数据库配置 (database)

```yaml
database:
  default: default
  connections:
    default:
      driver: mysql
      host: localhost       # 数据库主机
      port: 3306           # 数据库端口
      user: go_admin       # 数据库用户
      password: password   # 数据库密码
      name: cmf           # 数据库名
      ssl_mode: false     # SSL连接
      table_prefix: cmf_  # 表前缀
```

### 缓存配置 (cache)

```yaml
cache:
  default: memory         # 默认缓存驱动 (memory/redis)
  stores:
    memory:
      driver: memory
      default_ttl: 3600   # 默认过期时间 (秒)
    redis:
      driver: redis
      default_ttl: 3600
```

### Redis配置 (redis)

```yaml
redis:
  default: redis
  connections:
    redis:
      addr: localhost:6379    # Redis地址
      username: ""           # Redis用户名 (Redis 6.0+)
      password: ""           # Redis密码
      db: 0                  # 数据库编号
      dial_timeout: 5        # 连接超时
      read_timeout: 3        # 读取超时
      write_timeout: 3       # 写入超时
      pool_size: 10          # 连接池大小
      min_idle_conns: 5      # 最小空闲连接
      max_idle_conns: 10     # 最大空闲连接
      conn_max_idle_time: 30 # 连接最大空闲时间
      conn_max_lifetime: 24  # 连接最大生存时间
      use_tls: false         # 启用TLS
```

### 日志配置 (log)

```yaml
log:
  file_path: ./data/logs/admin-api.log  # 日志文件路径
  console_output: true                  # 控制台输出
  file_output: true                     # 文件输出
  max_size: 10                         # 单文件最大大小 (MB)
  max_backups: 10                      # 备份文件数量
  max_age: 180                         # 保留天数
```

### 文件存储配置 (filesystem)

```yaml
filesystem:
  default: local          # 默认存储驱动
  is_and_local: false
  disks:
    local:
      driver: local
      options:
        root: ./data/storage  # 本地存储根目录
    s3:
      driver: s3
      options:
        access_key: ""        # AWS Access Key
        secret_key: ""        # AWS Secret Key
        region: ""           # AWS区域
        bucket: ""           # S3存储桶
        endpoint: ""         # 自定义端点 (兼容S3的服务)
```

## 启动应用

### 开发环境

```bash
# 直接运行
go run main.go

# 或使用构建的二进制文件
./go-admin
```

### 生产环境

#### 使用systemd (推荐)

创建systemd服务文件：

```bash
sudo vim /etc/systemd/system/go-admin.service
```

```ini
[Unit]
Description=Go Admin API Service
After=network.target mysql.service

[Service]
Type=simple
User=go-admin
Group=go-admin
WorkingDirectory=/opt/go-admin
ExecStart=/opt/go-admin/go-admin
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=go-admin
KillMode=mixed
KillSignal=SIGINT

[Install]
WantedBy=multi-user.target
```

启动服务：

```bash
# 重新加载systemd配置
sudo systemctl daemon-reload

# 启用服务
sudo systemctl enable go-admin

# 启动服务
sudo systemctl start go-admin

# 查看状态
sudo systemctl status go-admin

# 查看日志
sudo journalctl -u go-admin -f
```

#### 使用Docker (可选)

创建Dockerfile：

```dockerfile
FROM golang:1.25.1-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-w -s' -o go-admin main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /root/

COPY --from=builder /app/go-admin .
COPY --from=builder /app/config ./config
COPY --from=builder /app/web ./web

EXPOSE 9501
CMD ["./go-admin"]
```

构建和运行：

```bash
# 构建镜像
docker build -t go-admin:latest .

# 运行容器
docker run -d \
  --name go-admin \
  -p 9501:9501 \
  -v /path/to/config:/root/config \
  -v /path/to/data:/root/data \
  go-admin:latest
```

## 反向代理配置

### Nginx配置示例

```nginx
server {
    listen 80;
    server_name your-domain.com;

    # 重定向到HTTPS (生产环境推荐)
    return 301 https://$server_name$request_uri;
}

server {
    listen 443 ssl http2;
    server_name your-domain.com;

    # SSL证书配置
    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;

    # 静态资源缓存
    location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg)$ {
        expires 1y;
        add_header Cache-Control "public, immutable";
    }

    # API代理
    location /api/ {
        proxy_pass http://127.0.0.1:9501;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # 前端SPA
    location / {
        proxy_pass http://127.0.0.1:9501;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

## 监控和维护

### 健康检查

应用提供以下端点用于健康检查：

- **应用状态**: `GET /` (返回前端页面)
- **API状态**: `GET /api/v1/admin/login` (应返回登录页面或错误)

### 日志监控

```bash
# 查看应用日志
tail -f ./data/logs/admin-api.log

# 使用systemd查看日志
sudo journalctl -u go-admin -f

# 日志轮转检查
ls -la ./data/logs/
```

### 性能监控

监控以下指标：

- **CPU使用率**: 正常情况下应低于50%
- **内存使用**: 根据负载，通常在100MB-1GB之间
- **数据库连接**: 监控连接池使用情况
- **响应时间**: API响应时间应在100ms以内

### 备份策略

#### 数据库备份

```bash
# 每日备份脚本
#!/bin/bash
DATE=$(date +%Y%m%d_%H%M%S)
mysqldump -u go_admin -p cmf > /backup/cmf_$DATE.sql
find /backup -name "cmf_*.sql" -mtime +7 -delete
```

#### 配置文件备份

```bash
# 备份配置和数据
tar -czf /backup/go-admin-config_$DATE.tar.gz config/ data/
```

## 故障排除

### 常见问题

#### 1. 应用无法启动

**症状**: 应用启动失败或立即退出

**排查步骤**:
```bash
# 检查配置文件
cat config/config.yml

# 检查数据库连接
mysql -h localhost -u go_admin -p cmf

# 检查端口占用
netstat -tlnp | grep 9501

# 查看详细错误
./go-admin 2>&1 | tee error.log
```

#### 2. 数据库连接失败

**症状**: "failed to connect to database" 错误

**解决方案**:
- 检查MySQL服务状态: `sudo systemctl status mysql`
- 验证数据库配置: 用户名、密码、数据库名
- 检查网络连接: `telnet localhost 3306`
- 查看MySQL错误日志: `/var/log/mysql/error.log`

#### 3. 前端资源404错误

**症状**: 访问网站显示404或资源加载失败

**解决方案**:
- 检查 `web/dist` 目录是否存在
- 确认 `index.html` 文件存在
- 检查文件权限: `ls -la web/dist/`

#### 4. JWT认证失败

**症状**: 认证接口返回401错误

**解决方案**:
- 检查JWT密钥配置: `app.secret`
- 确认密钥长度足够 (建议32字符以上)
- 检查系统时间同步

### 日志分析

#### 错误日志关键字

- `failed to connect`: 数据库连接问题
- `bind: address already in use`: 端口被占用
- `permission denied`: 权限问题
- `no such file or directory`: 文件路径问题

#### 性能日志

监控以下日志信息：
- 请求响应时间
- 数据库查询时间
- 内存使用情况
- 并发连接数

## 安全建议

### 生产环境安全

1. **修改默认配置**
   - 更改JWT密钥
   - 使用强数据库密码
   - 关闭调试模式

2. **网络安全**
   - 使用防火墙限制端口访问
   - 启用HTTPS
   - 配置反向代理

3. **文件权限**
   ```bash
   # 设置适当的文件权限
   chmod 755 go-admin
   chmod 644 config/config.yml
   chmod 700 data/
   ```

4. **定期更新**
   - 更新Go版本
   - 更新依赖包
   - 应用安全补丁

### 数据保护

- 定期备份数据库
- 加密敏感配置信息
- 监控异常访问
- 实施访问控制策略

## 升级指南

### 版本升级步骤

1. **备份当前版本**
   ```bash
   # 停止服务
   sudo systemctl stop go-admin
   
   # 备份
   cp go-admin go-admin.backup
   cp -r config config.backup
   ```

2. **部署新版本**
   ```bash
   # 替换二进制文件
   cp go-admin.new go-admin
   
   # 更新配置 (如需要)
   # 比较配置文件差异
   diff config/config.yml config.yml.new
   ```

3. **启动服务**
   ```bash
   # 启动服务
   sudo systemctl start go-admin
   
   # 检查状态
   sudo systemctl status go-admin
   ```

4. **验证升级**
   - 检查API功能
   - 验证前端页面
   - 查看错误日志

### 回滚步骤

如果升级失败，可以快速回滚：

```bash
# 停止服务
sudo systemctl stop go-admin

# 恢复备份
cp go-admin.backup go-admin
cp -r config.backup/* config/

# 启动服务
sudo systemctl start go-admin
```
# 数据库模型文档

## 概述

本文档描述了go-admin项目中使用的数据库模型。项目使用Ent ORM框架来定义和管理数据模型，支持MySQL数据库。

## 数据库配置

- **数据库类型**: MySQL
- **表前缀**: `cmf_`
- **字符集**: UTF-8
- **连接配置**: 参见 `config/config.yml` 中的database配置

## 数据模型

### 1. User (用户表)

用户表存储系统用户的基本信息和认证数据。

#### 表结构

| 字段名 | 类型 | 长度 | 约束 | 默认值 | 描述 |
|--------|------|------|------|--------|------|
| id | INT | - | PRIMARY KEY, AUTO_INCREMENT, UNIQUE | - | 用户唯一标识符 |
| email | VARCHAR | 255 | UNIQUE, NOT NULL | - | 用户邮箱地址 |
| password | VARCHAR | 255 | NOT NULL, SENSITIVE | - | 用户密码(加密存储) |
| name | VARCHAR | 255 | OPTIONAL | NULL | 用户真实姓名 |
| username | VARCHAR | 255 | OPTIONAL | NULL | 用户名 |
| nickname | VARCHAR | 255 | OPTIONAL | NULL | 用户昵称 |
| uid | VARCHAR | 255 | OPTIONAL | NULL | 用户唯一标识 |
| password_salt | VARCHAR | 255 | OPTIONAL, SENSITIVE | NULL | 密码盐值 |
| status | ENUM | - | NOT NULL | 'active' | 用户状态 |
| create_time | TIMESTAMP | - | NOT NULL | CURRENT_TIMESTAMP | 创建时间 |
| update_time | TIMESTAMP | - | NOT NULL | CURRENT_TIMESTAMP ON UPDATE | 更新时间 |

#### 字段说明

- **id**: 自增主键，用于唯一标识用户
- **email**: 用户邮箱，必须唯一，用于登录和通信，支持邮箱格式验证
- **password**: 用户密码，经过bcrypt加密处理，标记为敏感字段
- **name**: 用户真实姓名，可选字段
- **username**: 用户名，可选字段，可用于登录
- **nickname**: 用户昵称，用于显示
- **uid**: 用户唯一标识符，用于JWT token中的用户标识
- **password_salt**: 密码加密盐值，敏感字段
- **avatar**: 用户头像URL，最大长度1024字符
- **phone**: 手机号码，支持数字格式验证，最大长度20字符
- **status**: 用户状态，枚举值包括：
  - `active`: 活跃用户，可正常登录
  - `inactive`: 非活跃用户，默认状态，需要激活后才能登录
  - `suspended`: 已暂停用户，禁止登录
- **last_login**: 最后登录时间，记录用户最近一次登录的时间
- **last_ip**: 最后登录IP地址，记录用户最近一次登录的IP
- **create_time**: 记录创建时间，由mixin.Time自动管理
- **update_time**: 记录更新时间，由mixin.Time自动管理

#### 索引

- **PRIMARY**: id (主键索引)
- **UNIQUE**: email (唯一索引)
- **INDEX**: phone (普通索引，提高手机号查询性能)
- **INDEX**: username (普通索引，提高用户名查询性能)

### 2. CasbinRule (权限规则表)

权限规则表用于存储Casbin权限控制框架的规则数据。

#### 表结构

| 字段名 | 类型 | 长度 | 约束 | 默认值 | 描述 |
|--------|------|------|------|--------|------|
| id | INT | - | PRIMARY KEY, AUTO_INCREMENT | - | 规则唯一标识符 |
| Ptype | VARCHAR | 255 | NOT NULL | "" | 策略类型 |
| V0 | VARCHAR | 255 | NOT NULL | "" | 规则参数0 |
| V1 | VARCHAR | 255 | NOT NULL | "" | 规则参数1 |
| V2 | VARCHAR | 255 | NOT NULL | "" | 规则参数2 |
| V3 | VARCHAR | 255 | NOT NULL | "" | 规则参数3 |
| V4 | VARCHAR | 255 | NOT NULL | "" | 规则参数4 |
| V5 | VARCHAR | 255 | NOT NULL | "" | 规则参数5 |

#### 字段说明

- **id**: 自增主键，用于唯一标识权限规则
- **Ptype**: 策略类型，标识规则的类型(如p、g等)
- **V0-V5**: 规则参数，用于存储具体的权限规则内容
  - 对于策略规则(p)：通常V0为主体，V1为对象，V2为动作
  - 对于角色规则(g)：通常V0为用户，V1为角色

#### 索引

- **PRIMARY**: id (主键索引)
- **UNIQUE**: (Ptype, V0, V1, V2, V3, V4, V5) (复合唯一索引)

#### 使用示例

权限规则示例：
```
Ptype: "p", V0: "admin", V1: "/api/users", V2: "GET"
Ptype: "g", V0: "user1", V1: "admin"
```

## 数据关系

目前两个表之间没有直接的外键关系，但在业务逻辑中：
- User表的用户可以通过CasbinRule表分配角色和权限
- CasbinRule表中的V0字段可能引用User表中的username或email

## 数据库操作

### 连接管理

项目使用Ent ORM进行数据库操作：
- 连接配置在 `config/config.yml` 中定义
- 数据库客户端在 `internal/global/global.go` 中全局管理
- 连接初始化在 `internal/init.go` 中完成

### 迁移管理

- 使用Ent的自动迁移功能
- 在应用启动时自动创建表结构
- Schema变更通过重新生成Ent代码实现

### 性能优化

- 为常用查询字段添加索引
- 使用连接池管理数据库连接
- 敏感字段标记确保安全处理

## 注意事项

1. **敏感数据**: password和password_salt字段标记为敏感，在日志和调试输出中会被隐藏
2. **唯一约束**: email字段必须唯一，注册时需要验证
3. **权限规则**: CasbinRule表的复合唯一索引确保权限规则不重复
4. **时间戳**: User表使用mixin.Time自动管理创建和更新时间
5. **表前缀**: 所有表名会自动添加 `cmf_` 前缀
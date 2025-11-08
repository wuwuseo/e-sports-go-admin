# Go-Admin 项目文档

欢迎来到 Go-Admin 项目文档中心！这里包含了项目的完整技术文档，帮助您快速了解和使用本项目。

## 📚 文档导航

### 🏠 [项目概览](../README.md)
项目的基本介绍、特性、快速开始指南和基础使用说明。

### 🔌 [API接口文档](api/README.md)
- 完整的API接口说明
- 请求和响应格式
- 认证和权限机制
- 错误码和状态码
- 请求示例和调试指南

### 🏗️ [架构设计](architecture/overview.md)
- 系统整体架构设计
- 核心组件说明
- 技术栈介绍
- 设计模式和最佳实践
- 性能优化策略

### 🗄️ [数据库设计](database/models.md)
- 数据库模型定义
- 表结构说明
- 字段详细描述
- 索引和约束
- 数据关系图

### 🚀 [部署指南](deployment/guide.md)
- 环境要求和依赖
- 安装和配置步骤
- 生产环境部署
- Docker容器化
- 监控和运维
- 故障排除

### 👨‍💻 [开发者指南](development/guide.md)
- 开发环境搭建
- 代码规范和最佳实践
- 开发流程和工作流
- 测试策略和方法
- 调试和性能优化
- 贡献指南

### 🔍 [代码质量保证](development/code-quality.md)
- golangci-lint配置说明
- 代码质量检查工具使用
- CI/CD集成和自动化
- 常见问题和解决方案
- 自定义规则和最佳实践

### 🛠️ [工具类使用指南](development/utils.md)
- Response响应工具使用
- 全局常量和错误码
- 错误处理最佳实践
- 中间件集成方法
- 自定义工具函数扩展

## 🎯 快速导航

### 新手入门
1. 📖 [阅读项目概览](../README.md) - 了解项目基本信息
2. 🚀 [快速开始](../README.md#-快速开始) - 5分钟运行项目
3. 🔌 [API接口文档](api/README.md) - 了解可用接口

### 开发者
1. 👨‍💻 [开发环境搭建](development/guide.md#开发环境搭建) - 配置开发环境
2. 📝 [代码规范](development/guide.md#代码规范) - 遵循项目规范
3. 🧪 [测试指南](development/guide.md#测试策略) - 编写和运行测试
4. 🔍 [代码质量检查](development/code-quality.md) - 使用质量检查工具

### 运维人员
1. 🚀 [部署指南](deployment/guide.md#安装步骤) - 部署到生产环境
2. ⚙️ [配置说明](deployment/guide.md#配置说明) - 配置各项参数
3. 📊 [监控运维](deployment/guide.md#监控和维护) - 监控和维护系统

### 架构师
1. 🏗️ [系统架构](architecture/overview.md#系统架构) - 了解整体设计
2. 🗄️ [数据库设计](database/models.md#数据模型) - 查看数据模型
3. 🔒 [安全设计](architecture/overview.md#安全特性) - 了解安全机制

## 📖 文档说明

### 文档结构
```
docs/
├── README.md              # 文档导航 (本文件)
├── api/                   # API接口文档
│   └── README.md          # API接口说明
├── architecture/          # 架构设计文档
│   └── overview.md        # 系统架构概览
├── database/              # 数据库设计文档
│   └── models.md          # 数据模型说明
├── deployment/            # 部署相关文档
│   └── guide.md           # 部署指南
└── development/           # 开发相关文档
    ├── guide.md           # 开发者指南
    ├── code-quality.md    # 代码质量保证
    └── utils.md           # 工具类使用指南
```

### 文档维护
- 📅 **更新频率**: 随项目版本同步更新
- 👥 **维护团队**: 项目核心开发团队
- 🔄 **版本控制**: 与代码仓库同步管理
- 📝 **贡献方式**: 通过Pull Request提交文档改进

### 文档约定
- 使用Markdown格式编写
- 包含详细的代码示例
- 提供清晰的步骤说明
- 包含必要的图表和流程图
- 保持内容的准确性和时效性

## 🔗 相关链接

### 项目资源
- 🏠 [项目主页](../README.md)
- 📦 [源代码仓库](https://github.com/your-org/go-admin)
- 🐛 [问题反馈](https://github.com/your-org/go-admin/issues)
- 💬 [讨论区](https://github.com/your-org/go-admin/discussions)

### 技术文档
- 🌐 [Fiber框架文档](https://docs.gofiber.io/)
- 🗄️ [Ent ORM文档](https://entgo.io/docs/getting-started)
- 🔐 [Casbin权限文档](https://casbin.org/docs/overview)
- 🔑 [JWT规范文档](https://jwt.io/introduction)

### 开发工具
- 🛠️ [Go语言官网](https://golang.org/)
- 📝 [VS Code Go插件](https://marketplace.visualstudio.com/items?itemName=golang.Go)
- 🐳 [Docker官方文档](https://docs.docker.com/)
- 🔧 [MySQL官方文档](https://dev.mysql.com/doc/)
- 🔍 [golangci-lint文档](https://golangci-lint.run/)

## 📞 获取帮助

### 常见问题
在遇到问题时，建议按以下顺序查找解决方案：

1. **查看文档** - 首先查阅相关文档章节
2. **搜索问题** - 在GitHub Issues中搜索类似问题
3. **查看日志** - 检查应用和系统日志
4. **社区求助** - 在讨论区发起讨论
5. **提交Issue** - 如果是bug或新需求，提交Issue

### 联系方式
- 📧 **邮件支持**: admin@example.com
- 💬 **即时讨论**: [GitHub Discussions](https://github.com/your-org/go-admin/discussions)
- 🐛 **Bug报告**: [GitHub Issues](https://github.com/your-org/go-admin/issues)

### 响应时间
- 🚨 **紧急问题**: 24小时内响应
- 📋 **一般问题**: 3个工作日内响应
- 💡 **功能建议**: 1周内响应

## 🎉 贡献文档

我们欢迎您为项目文档做出贡献！

### 贡献方式
1. **发现错误** - 报告文档中的错误或过时信息
2. **改进内容** - 提供更清晰的说明或示例
3. **添加内容** - 补充缺失的文档章节
4. **翻译文档** - 提供多语言版本

### 提交流程
1. Fork项目仓库
2. 创建文档分支
3. 修改或添加文档
4. 提交Pull Request
5. 等待审核和合并

### 文档规范
- 使用清晰简洁的语言
- 提供实际可用的代码示例
- 包含必要的截图或图表
- 遵循现有的文档格式
- 确保内容的准确性

---

感谢您使用 Go-Admin 项目！如果您有任何问题或建议，请随时联系我们。 🚀
package middleware

import (
	"context"
	"errors"
	"strings"
	"time"

	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/jwt"
)

// AuditRule 审计白名单规则：命中后按 module/action 记录操作日志
type AuditRule struct {
	Module string
	Action string
}

// adminAuditRules 后台关键操作的审计白名单。
// key 为 "HTTP方法 完整路由模板"（与 c.Route().Path 一致，参数段为 :id 形式）。
// 只收录当前无手动 logOperation 埋点的关键变更路由，避免与既有埋点重复记录：
//   - 管理员增删改、启停、角色分配
//   - 用户创建、启停、黑名单、角色分配
//   - 权限定义 CRUD
//   - 管理员登出
//
// 登录成功/失败由登录日志（login_logs）单独记录，不在此列。
func AdminAuditRules() map[string]AuditRule {
	return map[string]AuditRule{
		// 管理员管理
		"POST /api/v1/admin/auth/admins":                   {Module: "管理员管理", Action: "创建"},
		"PUT /api/v1/admin/auth/admins/:id":                {Module: "管理员管理", Action: "更新"},
		"PUT /api/v1/admin/auth/admins/:id/activate":       {Module: "管理员管理", Action: "启用"},
		"PUT /api/v1/admin/auth/admins/:id/deactivate":     {Module: "管理员管理", Action: "禁用"},
		"PUT /api/v1/admin/auth/admins/:id/roles":          {Module: "管理员管理", Action: "分配角色"},
		"DELETE /api/v1/admin/auth/third-party-admins/:id": {Module: "管理员管理", Action: "解绑第三方身份"},
		// 用户管理
		"POST /api/v1/admin/auth/users":                   {Module: "用户管理", Action: "创建"},
		"PUT /api/v1/admin/auth/users/:id/activate":       {Module: "用户管理", Action: "启用"},
		"PUT /api/v1/admin/auth/users/:id/deactivate":     {Module: "用户管理", Action: "禁用"},
		"PUT /api/v1/admin/auth/users/:id/roles":          {Module: "用户管理", Action: "分配角色"},
		"POST /api/v1/admin/auth/users/:id/blacklist":     {Module: "用户管理", Action: "加入黑名单"},
		"DELETE /api/v1/admin/auth/users/:id/blacklist":   {Module: "用户管理", Action: "移出黑名单"},
		"DELETE /api/v1/admin/auth/third-party-users/:id": {Module: "用户管理", Action: "解绑第三方身份"},
		// 权限定义
		"POST /api/v1/admin/auth/system-permissions":       {Module: "权限定义", Action: "创建"},
		"PUT /api/v1/admin/auth/system-permissions/:id":    {Module: "权限定义", Action: "更新"},
		"DELETE /api/v1/admin/auth/system-permissions/:id": {Module: "权限定义", Action: "删除"},
		// 认证
		"POST /api/v1/admin/auth/logout": {Module: "认证", Action: "登出"},
	}
}

// Audit 返回操作审计中间件：仅记录白名单内的请求，
// 使用真实响应状态码与耗时，且请求被权限/黑名单拒绝时同样留痕。
func Audit(rules map[string]AuditRule, logSvc service.OperationLogServiceInterface) fiber.Handler {
	return func(c fiber.Ctx) error {
		start := time.Now()
		err := c.Next()

		rule, ok := auditRuleForRequest(c, rules)
		if ok && logSvc != nil {
			statusCode := c.Response().StatusCode()
			if err != nil {
				statusCode = fiber.StatusInternalServerError
				var fiberErr *fiber.Error
				if errors.As(err, &fiberErr) {
					statusCode = fiberErr.Code
				}
			}
			userUID, username := auditOperatorFromJWT(c)
			_ = logSvc.Log(context.Background(), &service.LogEntry{
				UserUID:     userUID,
				Username:    username,
				Module:      rule.Module,
				Action:      rule.Action,
				Description: rule.Action,
				RequestBody: utils.SafeGetBody(c),
				Method:      c.Method(),
				Path:        c.Path(),
				IP:          c.IP(),
				UserAgent:   c.Get("User-Agent"),
				StatusCode:  statusCode,
				DurationMs:  time.Since(start).Milliseconds(),
			})
		}
		return err
	}
}

func auditRuleForRequest(c fiber.Ctx, rules map[string]AuditRule) (AuditRule, bool) {
	if rule, ok := rules[auditRuleKey(c.Method(), c.Route().Path)]; ok {
		return rule, true
	}
	// 中间件提前拒绝时 Route() 仍指向路由组，按注册顺序找到目标业务路由。
	cfg := c.App().Config()
	path := c.Path()
	// RoutePatternMatch 仅清理模板的尾斜杠，请求路径需按相同配置规范化。
	if !cfg.StrictRouting && len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	for _, route := range c.App().GetRoutes(true) {
		if route.Method == c.Method() && fiber.RoutePatternMatch(path, route.Path, cfg) {
			rule, ok := rules[auditRuleKey(route.Method, route.Path)]
			return rule, ok
		}
	}
	return AuditRule{}, false
}

func auditRuleKey(method, routePath string) string {
	return method + " " + routePath
}

func auditOperatorFromJWT(c fiber.Ctx) (userUID, username string) {
	claims := jwt.GetJWTUserData(c)
	if claims == nil {
		return "", ""
	}
	if id, ok := claims["id"].(string); ok {
		userUID = id
	}
	if name, ok := claims["subject"].(string); ok {
		username = name
	}
	return userUID, username
}

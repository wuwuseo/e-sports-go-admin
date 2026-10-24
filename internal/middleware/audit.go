package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/jwt"
)

// 审计快照实体种类：命中后由中间件自动抓取该实体的「改前 / 改后」关键字段值，
// 使价格 / 规格 / 结单要求等变更可查「操作人 + 时间 + 改前改后值」。
const (
	AuditSnapshotEsportsSku       = "esports_sku"
	AuditSnapshotEsportsCloseRule = "esports_close_rule"
)

// AuditRule 审计白名单规则：命中后按 module/action 记录操作日志
type AuditRule struct {
	Module string
	Action string
	// Snapshot 指定需要记录改前改后值的实体种类（AuditSnapshot* 常量之一）。
	// 为空表示只记录操作人、时间与请求体。
	Snapshot string
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

		// —— 陪玩报单（② / ）：资金 / 价格 / 规格 / 结单要求 / 内容发布变更 ——
		// 价格与规格：Snapshot 让操作日志带上改前改后值（实体的价格/规格/结单要求字段）。
		"POST /api/v1/esports/admin/products":                         {Module: "陪玩报单·商品", Action: "创建商品"},
		"PUT /api/v1/esports/admin/products/:id":                      {Module: "陪玩报单·商品", Action: "编辑商品（价格/结单要求）", Snapshot: AuditSnapshotEsportsSku},
		"PUT /api/v1/esports/admin/close-rules/:id":                   {Module: "陪玩报单·结单要求", Action: "编辑结单要求", Snapshot: AuditSnapshotEsportsCloseRule},
		"POST /api/v1/esports/admin/close-rules/:id/activate":         {Module: "陪玩报单·结单要求", Action: "生效结单要求"},
		"POST /api/v1/esports/admin/close-rules/:id/rollback":         {Module: "陪玩报单·结单要求", Action: "回滚结单要求"},
		"POST /api/v1/esports/admin/products/:id/suspensions":         {Module: "陪玩报单·商品", Action: "发起停服申请"},
		"POST /api/v1/esports/admin/product-suspensions/:id/decision": {Module: "陪玩报单·商品", Action: "审批停服申请"},
		// 订单资金
		"PUT /api/v1/esports/admin/orders/:id/quote":                {Module: "陪玩报单·订单资金", Action: "订单改价"},
		"POST /api/v1/esports/admin/price-adjustments/:id/decision": {Module: "陪玩报单·订单资金", Action: "复核超限改价"},
		"POST /api/v1/esports/admin/orders/:id/pay-by-balance":      {Module: "陪玩报单·订单资金", Action: "余额核销订单"},
		"POST /api/v1/esports/admin/wallets/:id/recharge":           {Module: "陪玩报单·资金", Action: "钱包充值"},
		"POST /api/v1/esports/admin/wallets/:id/adjust":             {Module: "陪玩报单·资金", Action: "钱包调整"},
		"POST /api/v1/esports/admin/withdrawals/:id/review":         {Module: "陪玩报单·资金", Action: "审核提现"},
		"POST /api/v1/esports/admin/withdrawals/:id/pay":            {Module: "陪玩报单·资金", Action: "登记提现打款"},
		"POST /api/v1/esports/admin/withdrawals/:id/payout-request": {Module: "陪玩报单·资金", Action: "提交提现打款"},
		"POST /api/v1/esports/admin/withdrawal-payouts/:id/paid":    {Module: "陪玩报单·资金", Action: "标记打款成功"},
		"POST /api/v1/esports/admin/withdrawal-payouts/:id/failed":  {Module: "陪玩报单·资金", Action: "标记打款失败"},
		"POST /api/v1/esports/admin/settlements/:id/pay":            {Module: "陪玩报单·资金", Action: "登记结算发放"},
		// 收款账户与到账核销
		"POST /api/v1/esports/admin/payment-accounts":                  {Module: "陪玩报单·收款账户", Action: "提交收款账户"},
		"POST /api/v1/esports/admin/payment-accounts/:id/review":       {Module: "陪玩报单·收款账户", Action: "审核收款账户"},
		"POST /api/v1/esports/admin/payment-accounts/:id/disable":      {Module: "陪玩报单·收款账户", Action: "停用收款账户"},
		"POST /api/v1/esports/admin/payment-channels/:channel/status":  {Module: "陪玩报单·收款账户", Action: "启停收款方式"},
		"POST /api/v1/esports/admin/offline-payment-proofs/:id/verify": {Module: "陪玩报单·资金", Action: "核销外部到账"},
		"POST /api/v1/esports/admin/offline-payment-proofs/:id/reject": {Module: "陪玩报单·资金", Action: "驳回到账凭证"},
		// 售后赔付
		"POST /api/v1/esports/admin/after-sales/:id/approve":        {Module: "陪玩报单·售后赔付", Action: "审批售后"},
		"POST /api/v1/esports/admin/after-sales/:id/execute":        {Module: "陪玩报单·售后赔付", Action: "执行售后"},
		"POST /api/v1/esports/admin/after-sales/:id/refund-execute": {Module: "陪玩报单·售后赔付", Action: "批次退款执行"},
		"POST /api/v1/esports/admin/external-refunds":               {Module: "陪玩报单·售后赔付", Action: "登记外部退款"},
		"POST /api/v1/esports/admin/external-refunds/:id/execute":   {Module: "陪玩报单·售后赔付", Action: "执行外部退款"},
		// 内容发布（装修）
		"PUT /api/v1/esports/admin/pages/:channel/draft":                              {Module: "陪玩报单·页面装修", Action: "保存装修草稿"},
		"POST /api/v1/esports/admin/pages/:channel/publish":                           {Module: "陪玩报单·页面装修", Action: "提交发布申请"},
		"POST /api/v1/esports/admin/pages/:channel/publish-review/:versionId/approve": {Module: "陪玩报单·页面装修", Action: "通过发布审核"},
		"POST /api/v1/esports/admin/pages/:channel/publish-review/:versionId/reject":  {Module: "陪玩报单·页面装修", Action: "驳回发布审核"},
	}
}

// Audit 返回操作审计中间件：仅记录白名单内的请求，
// 使用真实响应状态码与耗时，且请求被权限/黑名单拒绝时同样留痕。
// 命中带 Snapshot 的规则时，额外记录该实体的改前改后值。
func Audit(rules map[string]AuditRule, logSvc service.OperationLogServiceInterface) fiber.Handler {
	return func(c fiber.Ctx) error {
		start := time.Now()
		// 请求前解析规则并抓取改前快照：改写前值只能在处理器执行之前读取。
		match := auditRuleForRequest(c, rules)
		var before map[string]string
		if match.Matched && match.Rule.Snapshot != "" {
			before = auditEntitySnapshot(c, match.Rule.Snapshot, match.RoutePath)
		}

		err := c.Next()

		if match.Matched && logSvc != nil {
			rule := match.Rule
			statusCode := c.Response().StatusCode()
			if err != nil {
				statusCode = fiber.StatusInternalServerError
				var fiberErr *fiber.Error
				if errors.As(err, &fiberErr) {
					statusCode = fiberErr.Code
				}
			}
			userUID, username := auditOperatorFromJWT(c)
			description := rule.Action
			if rule.Snapshot != "" {
				if diff := auditSnapshotDiff(before, auditEntitySnapshot(c, rule.Snapshot, match.RoutePath)); diff != "" {
					description = rule.Action + "：" + diff
				}
			}
			_ = logSvc.Log(context.Background(), &service.LogEntry{
				UserUID:     userUID,
				Username:    username,
				Module:      rule.Module,
				Action:      rule.Action,
				Description: description,
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

// auditSnapshotMaxDiff 限制单条变更摘要的长度，避免超长字段撑爆描述列。
const auditSnapshotMaxDiff = 480

// auditEntitySnapshot 抓取实体在请求当刻的关键字段快照；实体不存在或不在白名单时返回 nil。
//
// 只读取与「价格 / 规格 / 结单要求」相关的字段，绝不把整行实体写进日志。
func auditEntitySnapshot(c fiber.Ctx, kind, routePath string) map[string]string {
	db := global.ORM
	if db == nil {
		return nil
	}
	id, err := strconv.Atoi(auditPathParam(c.Path(), routePath, "id"))
	if err != nil || id < 1 {
		return nil
	}
	ctx := c.Context()

	switch kind {
	case AuditSnapshotEsportsSku:
		row, err := db.EsportsSku.Get(ctx, id)
		if err != nil {
			return nil
		}
		return map[string]string{
			"title":                  row.Title,
			"unit_price_cents":       strconv.FormatInt(row.UnitPriceCents, 10),
			"min_unit_price_cents":   strconv.FormatInt(row.MinUnitPriceCents, 10),
			"adjustment_approval_bp": strconv.Itoa(row.AdjustmentApprovalBp),
			"service_units":          strconv.FormatInt(row.ServiceUnits, 10),
			"service_unit_name":      row.ServiceUnitName,
			"per_seat_service_units": strconv.FormatInt(row.PerSeatServiceUnits, 10),
			"required_seats":         strconv.Itoa(row.RequiredSeats),
			"close_rule_code":        row.CloseRuleCode,
			"quota_total":            strconv.FormatInt(row.QuotaTotal, 10),
			"status":                 string(row.Status),
			"version":                strconv.Itoa(row.Version),
		}
	case AuditSnapshotEsportsCloseRule:
		row, err := db.EsportsCloseRule.Get(ctx, id)
		if err != nil {
			return nil
		}
		return map[string]string{
			"code":                      row.Code,
			"name":                      row.Name,
			"scope":                     string(row.Scope),
			"scope_key":                 row.ScopeKey,
			"min_evidence":              strconv.Itoa(row.MinEvidence),
			"require_duration":          strconv.FormatBool(row.RequireDuration),
			"require_no_pending_addon":  strconv.FormatBool(row.RequireNoPendingAddon),
			"require_no_open_aftersale": strconv.FormatBool(row.RequireNoOpenAftersale),
			"review_threshold_cents":    strconv.FormatInt(row.ReviewThresholdCents, 10),
			"status":                    string(row.Status),
			"version":                   strconv.Itoa(row.Version),
		}
	}
	return nil
}

// auditSnapshotValueMaxLen 限制单个快照值的长度（逐席座位等 JSON 字段可能较长）。
const auditSnapshotValueMaxLen = 120

// auditSnapshotJSON 把结构性字段（逐席角色等）编码为可比对的短字符串。
func auditSnapshotJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	text := string(raw)
	if len(text) > auditSnapshotValueMaxLen {
		text = text[:auditSnapshotValueMaxLen] + "…"
	}
	return text
}

// auditSnapshotDiff 输出「字段 改前→改后」摘要；任一侧缺失或无变化时返回空串。
func auditSnapshotDiff(before, after map[string]string) string {
	if len(before) == 0 || len(after) == 0 {
		return ""
	}
	keys := make([]string, 0, len(after))
	for key := range after {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		oldValue, hadOld := before[key]
		if !hadOld || oldValue == after[key] {
			continue
		}
		parts = append(parts, key+" "+oldValue+"→"+after[key])
	}
	if len(parts) == 0 {
		return ""
	}
	diff := strings.Join(parts, "；")
	if len(diff) > auditSnapshotMaxDiff {
		diff = diff[:auditSnapshotMaxDiff] + "…"
	}
	return diff
}

// auditMatch 是一次审计规则解析结果：Matched 表示命中白名单规则，
// RoutePath 是命中的路由模板（供路径参数取值的统一口径使用）。
type auditMatch struct {
	Rule      AuditRule
	RoutePath string
	Matched   bool
}

// auditRuleForRequest 解析请求命中的审计规则。
//
// 解析只依赖「请求方法 + 请求路径」与白名单规则表，不依赖 Fiber 当时匹配到哪一条路由：
// 中间件挂在前缀上时，路由参数与最终路由在 c.Next() 之前尚未确定，
// 因此这里直接用 fiber.RoutePatternMatch 把请求路径与规则路径（:id 形式）比对，
// 既覆盖「中间件提前拒绝（Route() 仍是路由组）」的场景，也让未命中白名单的
// 高频路由只付出 O(白名单条数) 的比较成本（不扫描整张路由表、不查库）。
func auditRuleForRequest(c fiber.Ctx, rules map[string]AuditRule) auditMatch {
	method := c.Method()
	if routePath := c.Route().Path; routePath != "" {
		if rule, ok := rules[auditRuleKey(method, routePath)]; ok {
			return auditMatch{Rule: rule, RoutePath: routePath, Matched: true}
		}
	}

	cfg := c.App().Config()
	path := c.Path()
	// RoutePatternMatch 仅清理模板的尾斜杠，请求路径需按相同配置规范化。
	if !cfg.StrictRouting && len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	for key, rule := range rules {
		ruleMethod, rulePath, found := strings.Cut(key, " ")
		if !found || ruleMethod != method {
			continue
		}
		if fiber.RoutePatternMatch(path, rulePath, cfg) {
			return auditMatch{Rule: rule, RoutePath: rulePath, Matched: true}
		}
	}
	return auditMatch{}
}

// auditPathParam 从请求路径里取出路由模板中 :name 段的实际值。
// 中间件在 c.Next() 之前拿不到 Fiber 解析后的路由参数，这里按路径段自行对齐，
// 使请求前后的两次快照使用完全同一套取值口径。
func auditPathParam(requestPath, routePath, name string) string {
	routeSegments := strings.Split(strings.TrimRight(routePath, "/"), "/")
	requestSegments := strings.Split(strings.TrimRight(requestPath, "/"), "/")
	want := ":" + name
	for i, segment := range routeSegments {
		if segment == want && i < len(requestSegments) {
			return requestSegments[i]
		}
	}
	return ""
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

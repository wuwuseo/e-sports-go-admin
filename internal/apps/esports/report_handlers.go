package esports

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// adminReports 是 §17 经营统计报表入口：单一路由按 :type 分派五类只读聚合报表。
// 数据边界：非超级管理员（客服）只能看本人负责 / 协作授权 / 本人报单的订单与本人处理的售后；
// 财务资金报表仅超级管理员或命中 app.esports.finance_report_role_names 的财务角色可见。
func adminReports(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	reportType := strings.TrimSpace(c.Params("type"))
	filter, err := parseReportFilter(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	// 财务资金报表：超级管理员，或命中 app.esports.finance_report_role_names 配置角色的管理员可看。
	if reportType == "finance" && !canAccessFinanceReport(c) {
		return businessError(c, errForbidden)
	}
	// 财务统计不具备逐订单归属：渠道 / 入口 / SKU / 游戏 / 负责客服 / 预约 / 席位等筛选
	// 已在 parseReportFilter 中按 :type 统一拒绝（查看与导出共用），不会静默忽略成全量。
	filter = scopeReportFilter(filter, operator.ID, isSuper)
	var result map[string]any
	switch reportType {
	case "owner_overview":
		result, err = ownerOverviewReport(c.Context(), global.ORM, filter)
	case "cs_performance":
		result, err = csPerformanceReport(c.Context(), global.ORM, filter)
	case "dispatch_efficiency":
		result, err = dispatchEfficiencyReport(c.Context(), global.ORM, filter)
	case "aftersale_quality":
		handler := 0
		if !isSuper {
			handler = operator.ID
		}
		result, err = aftersaleQualityReport(c.Context(), global.ORM, filter, handler)
	case "finance":
		result, err = financeReport(c.Context(), global.ORM, filter)
	default:
		return businessError(c, errInvalidInput)
	}
	if err != nil {
		return businessError(c, err)
	}
	result["range"] = map[string]any{
		"start": filter.Start, "end": filter.End, "period": filter.Period,
	}
	// §17 / P1-07：响应显式带出本次请求的数据域，便于事后审计「这次是全量还是局部」。
	// 财务资金报表不存在「订单局部」口径（需求明确不接受订单归属维度），授权者恒走显式全量分支。
	label, _, _ := reportDataScope(reportType, filter)
	result["data_scope"] = label
	// §17-07：按报表类型给出可用筛选项清单，前端据此裁剪筛选控件。
	result["filter_capabilities"] = map[string]any{
		"supported":   supportedReportDimensions(reportType),
		"unsupported": unsupportedReportDimensions(reportType),
	}
	return reply(c, result)
}

// scopeReportFilter 收敛非超管的数据可见范围（§17-09 / §3.5-06）。
// ResponsibleAdmin 保留为「被强制收敛的筛选值」（历史行为：客服不能借筛选参数读他人数据），
// 真正的权限边界交给 VisibleAdmin＝本人负责 OR 存在协作授权行，与订单列表 / 详情同口径。
func scopeReportFilter(filter reportFilter, adminID int, isSuper bool) reportFilter {
	if !isSuper {
		filter.ResponsibleAdmin = adminID
		filter.VisibleAdmin = adminID
	}
	return filter
}

// parseReportFilter 解析报表筛选参数；时间区间按周期推导或自定义。
// 财务总表（:type=finance）在解析阶段就拒绝订单归属维度，查看与导出两条链路共用同一校验。
func parseReportFilter(c fiber.Ctx) (reportFilter, error) {
	period := strings.TrimSpace(c.Query("period"))
	start, end, err := reportRange(period, c.Query("start"), c.Query("end"), time.Now())
	if err != nil {
		return reportFilter{}, err
	}
	if period == "" {
		period = "daily"
	}
	channelSource := strings.TrimSpace(c.Query("channel_source"))
	switch channelSource {
	case "", "manual", "wecom", "self", "import":
	default:
		return reportFilter{}, errInvalidInput
	}
	entryPoint := strings.TrimSpace(c.Query("entry_point"))
	switch entryPoint {
	case "", "cs_pc", "cs_mobile", "wecom_sidebar", "customer_self", "admin_backfill":
	default:
		return reportFilter{}, errInvalidInput
	}
	filter := reportFilter{
		Start:            start,
		End:              end,
		Period:           period,
		ResponsibleAdmin: queryInt(c, "responsible_admin_id"),
		CreatedByAdmin:   queryInt(c, "created_by_admin_id"),
		SkuID:            queryInt(c, "sku_id"),
		Game:             strings.TrimSpace(c.Query("game")),
		ChannelSource:    channelSource,
		EntryPoint:       entryPoint,
		AppointmentType:  strings.TrimSpace(c.Query("appointment_type")),
		SeatType:         strings.TrimSpace(c.Query("seat_type")),
		PaymentChannel:   strings.TrimSpace(c.Query("payment_channel")),
		AftersaleKind:    strings.TrimSpace(c.Query("kind")),
		FaultParty:       strings.TrimSpace(c.Query("fault_party")),
		Limit:            queryInt(c, "limit"),
		// 资金维度（§17 / P1-08）：账户类型 / 流水类型 / 结算状态只由财务资金报表接受，
		// 且必须真正下推到查询；其余报表类型由 validateReportFilter 显式 400。
		WalletAccountType: strings.TrimSpace(c.Query("account_type")),
		WalletEntryType:   strings.TrimSpace(c.Query("entry_type")),
		SettlementStatus:  strings.TrimSpace(c.Query("settlement_status")),
	}
	if err := validateReportFilter(strings.TrimSpace(c.Params("type")), filter); err != nil {
		return reportFilter{}, err
	}
	// 维度取值校验放在维度支持性校验之后：先报「该报表不支持此维度」，再报「取值非法」。
	if err := validateReportDimensionValues(filter); err != nil {
		return reportFilter{}, err
	}
	return filter, nil
}

// validateReportDimensionValues 校验筛选维度的取值是否合法（P1-08 / P1-09）：
// 取值非法时显式返回中文参数错误，避免拼错枚举被静默当成「命中 0 行」。
func validateReportDimensionValues(f reportFilter) error {
	switch f.WalletAccountType {
	case "", "customer_balance", "player_catfood", "stored_value_card":
	default:
		return fmt.Errorf("%w：账户类型仅支持 customer_balance / player_catfood / stored_value_card", errInvalidInput)
	}
	switch f.WalletEntryType {
	case "", "recharge", "consume", "refund_return", "adjust", "expire", "withdraw":
	default:
		return fmt.Errorf("%w：流水类型仅支持 recharge / consume / refund_return / adjust / expire / withdraw", errInvalidInput)
	}
	switch f.SettlementStatus {
	case "", "draft", "locked", "paid", "void":
	default:
		return fmt.Errorf("%w：结算状态仅支持 draft / locked / paid / void", errInvalidInput)
	}
	return nil
}

// reportFilterDimensionOrder 是报表筛选维度的稳定顺序（错误提示与能力清单共用）。
var reportFilterDimensionOrder = []string{
	"channel_source", "entry_point", "responsible_admin_id", "created_by_admin_id",
	"sku_id", "game", "appointment_type", "seat_type", "payment_channel", "kind", "fault_party",
	"account_type", "entry_type", "settlement_status",
}

// reportDimensionLabels 是各筛选维度的中文说明（拒绝提示用）。
var reportDimensionLabels = map[string]string{
	"channel_source":       "按客户渠道筛选",
	"entry_point":          "按报单入口筛选",
	"responsible_admin_id": "按负责客服筛选",
	"created_by_admin_id":  "按业绩归属客服（报单客服）筛选",
	"sku_id":               "按 SKU 筛选",
	"game":                 "按游戏筛选",
	"appointment_type":     "按即时预约筛选",
	"seat_type":            "按单人多人筛选",
	"payment_channel":      "按支付方式筛选",
	"kind":                 "按售后类型筛选",
	"fault_party":          "按责任方筛选",
	"account_type":         "按账户类型筛选",
	"entry_type":           "按流水类型筛选",
	"settlement_status":    "按结算状态筛选",
}

// reportTypeLabel 返回报表中文名（用于筛选拒绝提示）。
func reportTypeLabel(reportType string) string {
	switch reportType {
	case "owner_overview":
		return "经营总览报表"
	case "cs_performance":
		return "客服业绩报表"
	case "dispatch_efficiency":
		return "派单效率报表"
	case "aftersale_quality":
		return "售后质量报表"
	case "finance":
		return "财务资金报表"
	}
	return "报表"
}

// unsupportedReportDimensions 返回该报表类型不收下（也不静默忽略）的维度键（§17-07）：
//   - 售后质量只按订单渠道 / 入口圈定订单，另支持售后类型、责任方与处理客服；
//   - 财务资金不具备逐订单归属（含业绩归属客服 / 负责客服 / 创建人），只接受资金维度；
//   - 经营总览 / 客服业绩不使用售后类型、责任方与资金维度；
//   - 派单效率的需求筛选项里没有「业绩归属客服」（§17 只列负责客服），该维度必须显式 400。
func unsupportedReportDimensions(reportType string) []string {
	unsupported := map[string]bool{}
	switch reportType {
	case "aftersale_quality":
		for _, key := range []string{"sku_id", "game", "payment_channel", "appointment_type", "seat_type",
			"created_by_admin_id", "account_type", "entry_type", "settlement_status"} {
			unsupported[key] = true
		}
	case "finance":
		for _, key := range []string{"channel_source", "entry_point", "responsible_admin_id",
			"created_by_admin_id", "sku_id", "game", "appointment_type", "seat_type",
			"kind", "fault_party"} {
			unsupported[key] = true
		}
	case "dispatch_efficiency":
		for _, key := range []string{"created_by_admin_id", "kind", "fault_party",
			"account_type", "entry_type", "settlement_status"} {
			unsupported[key] = true
		}
	case "owner_overview", "cs_performance":
		for _, key := range []string{"kind", "fault_party", "account_type", "entry_type", "settlement_status"} {
			unsupported[key] = true
		}
	}
	out := make([]string, 0, len(unsupported))
	for _, key := range reportFilterDimensionOrder {
		if unsupported[key] {
			out = append(out, key)
		}
	}
	return out
}

// supportedReportDimensions 返回该报表类型可用的维度键，与不支持清单互补。
func supportedReportDimensions(reportType string) []string {
	unsupported := map[string]bool{}
	for _, key := range unsupportedReportDimensions(reportType) {
		unsupported[key] = true
	}
	out := make([]string, 0, len(reportFilterDimensionOrder))
	for _, key := range reportFilterDimensionOrder {
		if !unsupported[key] {
			out = append(out, key)
		}
	}
	return out
}

// reportFilterDimensionsInUse 返回本次请求实际带上的筛选维度键。
func reportFilterDimensionsInUse(f reportFilter) map[string]bool {
	inUse := map[string]bool{}
	if f.ChannelSource != "" {
		inUse["channel_source"] = true
	}
	if f.EntryPoint != "" {
		inUse["entry_point"] = true
	}
	if f.ResponsibleAdmin > 0 {
		inUse["responsible_admin_id"] = true
	}
	if f.CreatedByAdmin > 0 {
		inUse["created_by_admin_id"] = true
	}
	if f.SkuID > 0 {
		inUse["sku_id"] = true
	}
	if f.Game != "" {
		inUse["game"] = true
	}
	if f.AppointmentType != "" {
		inUse["appointment_type"] = true
	}
	if f.SeatType != "" {
		inUse["seat_type"] = true
	}
	if f.PaymentChannel != "" {
		inUse["payment_channel"] = true
	}
	if f.AftersaleKind != "" {
		inUse["kind"] = true
	}
	if f.FaultParty != "" {
		inUse["fault_party"] = true
	}
	if f.WalletAccountType != "" {
		inUse["account_type"] = true
	}
	if f.WalletEntryType != "" {
		inUse["entry_type"] = true
	}
	if f.SettlementStatus != "" {
		inUse["settlement_status"] = true
	}
	return inUse
}

// validateReportFilter 按报表类型校验筛选维度（§17-07）：不支持的维度显式返回中文参数错误，
// 避免「收下后不使用」把筛选静默变成全量统计。支持维度之间可叠加；同一请求命中多个不支持维度时
// 按固定顺序报告第一个，保证提示稳定且可断言。
func validateReportFilter(reportType string, f reportFilter) error {
	unsupported := map[string]bool{}
	for _, key := range unsupportedReportDimensions(reportType) {
		unsupported[key] = true
	}
	inUse := reportFilterDimensionsInUse(f)
	for _, key := range reportFilterDimensionOrder {
		if inUse[key] && unsupported[key] {
			return fmt.Errorf("%w：%s不支持%s", errInvalidInput, reportTypeLabel(reportType), reportDimensionLabels[key])
		}
	}
	return nil
}

// reportRange 推导统计区间：给定 start（可带 end）走自定义区间，否则按 period 取当前自然周期。
// 自然日 / 周 / 月边界统一用工作室业务时区（cstZone，UTC+8）计算，返回的绝对时刻以 UTC 存储。
func reportRange(period, startRaw, endRaw string, now time.Time) (time.Time, time.Time, error) {
	if start := strings.TrimSpace(startRaw); start != "" {
		begin, err := parseReportTime(start)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		end := now.UTC()
		if raw := strings.TrimSpace(endRaw); raw != "" {
			if end, err = parseReportEndTime(raw); err != nil {
				return time.Time{}, time.Time{}, err
			}
		}
		if !end.After(begin) {
			return time.Time{}, time.Time{}, errInvalidInput
		}
		return begin, end, nil
	}
	if strings.TrimSpace(endRaw) != "" {
		return time.Time{}, time.Time{}, errInvalidInput
	}
	local := now.In(cstZone)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, cstZone)
	switch period {
	case "", "daily":
		return day.UTC(), day.AddDate(0, 0, 1).UTC(), nil
	case "weekly":
		offset := (int(day.Weekday()) + 6) % 7
		begin := day.AddDate(0, 0, -offset)
		return begin.UTC(), begin.AddDate(0, 0, 7).UTC(), nil
	case "monthly":
		begin := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, cstZone)
		return begin.UTC(), begin.AddDate(0, 1, 0).UTC(), nil
	default:
		return time.Time{}, time.Time{}, errInvalidInput
	}
}

// parseReportTime 解析 RFC3339 或 YYYY-MM-DD 时间；空串返回零值。
// RFC3339 是绝对时刻，直接转 UTC；YYYY-MM-DD 按业务时区（cstZone）当日零点解析，再转 UTC 存储，
// 与 period 自然周期口径一致，避免把业务日期误读为 UTC 零点造成跨日错位。
func parseReportTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC(), nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02", raw, cstZone); err == nil {
		return parsed.UTC(), nil
	}
	return time.Time{}, errInvalidInput
}

// parseReportEndTime 解析自定义区间的结束时刻。日期（YYYY-MM-DD）按「业务时区次日零点」返回，
// 即结束日整天包含在区间内；若只按当日零点解析会排他漏掉当天（单日区间尤其明显）。
// 带时间的 RFC3339 仍按精确时刻处理。
func parseReportEndTime(raw string) (time.Time, error) {
	if parsed, err := time.ParseInLocation("2006-01-02", raw, cstZone); err == nil {
		return parsed.AddDate(0, 0, 1).UTC(), nil
	}
	return parseReportTime(raw)
}

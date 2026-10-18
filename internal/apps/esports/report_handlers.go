package esports

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// adminReports 是 §17 经营统计报表入口：单一路由按 :type 分派五类只读聚合报表。
// 数据边界：非超级管理员（客服）只能看本人负责的订单与本人处理的售后；财务资金报表仅超级管理员可见。
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
	return reply(c, result)
}

func scopeReportFilter(filter reportFilter, adminID int, isSuper bool) reportFilter {
	if !isSuper {
		filter.ResponsibleAdmin = adminID
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
	}
	if err := validateReportFilter(strings.TrimSpace(c.Params("type")), filter); err != nil {
		return reportFilter{}, err
	}
	return filter, nil
}

// validateReportFilter 按报表类型校验筛选维度。财务资金来自独立钱包、结算与支付回执，
// 不具备逐订单归属，订单归属筛选若被接受只会静默变成全量数据；这里显式返回中文参数错误。
// 时间区间与支付方式等资金维度不受影响；账户类型 / 流水类型 / 结算状态仍是报表输出维度。
func validateReportFilter(reportType string, f reportFilter) error {
	if reportType != "finance" {
		return nil
	}
	switch {
	case f.ChannelSource != "" || f.EntryPoint != "":
		return fmt.Errorf("%w：财务资金报表不支持按客户渠道 / 报单入口筛选", errInvalidInput)
	case f.SkuID > 0 || f.Game != "":
		return fmt.Errorf("%w：财务资金报表不支持按 SKU / 游戏筛选", errInvalidInput)
	case f.ResponsibleAdmin > 0:
		return fmt.Errorf("%w：财务资金报表不支持按负责客服筛选", errInvalidInput)
	case f.AppointmentType != "" || f.SeatType != "":
		return fmt.Errorf("%w：财务资金报表不支持按即时预约 / 单人多人筛选", errInvalidInput)
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

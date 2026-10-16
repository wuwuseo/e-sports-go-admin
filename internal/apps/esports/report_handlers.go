package esports

import (
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
	if reportType == "finance" && !isSuper {
		return businessError(c, errForbidden)
	}
	// 资金总表同时包含独立钱包充值、提现与结算，不具备逐订单渠道归属。
	// 拒绝该筛选，避免把全局资金误标为某一订单来源。
	if reportType == "finance" && (filter.ChannelSource != "" || filter.EntryPoint != "") {
		return businessError(c, errInvalidInput)
	}
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
	return reportFilter{
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
	}, nil
}

// reportRange 推导统计区间：给定 start（可带 end）走自定义区间，否则按 period 取当前自然周期。
func reportRange(period, startRaw, endRaw string, now time.Time) (time.Time, time.Time, error) {
	if start := strings.TrimSpace(startRaw); start != "" {
		begin, err := parseReportTime(start)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		end := now.UTC()
		if raw := strings.TrimSpace(endRaw); raw != "" {
			if end, err = parseReportTime(raw); err != nil {
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
	utc := now.UTC()
	day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case "", "daily":
		return day, day.AddDate(0, 0, 1), nil
	case "weekly":
		offset := (int(day.Weekday()) + 6) % 7
		begin := day.AddDate(0, 0, -offset)
		return begin, begin.AddDate(0, 0, 7), nil
	case "monthly":
		begin := time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
		return begin, begin.AddDate(0, 1, 0), nil
	default:
		return time.Time{}, time.Time{}, errInvalidInput
	}
}

// parseReportTime 解析 RFC3339 或 YYYY-MM-DD 时间；空串返回零值。
func parseReportTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC(), nil
	}
	if parsed, err := time.Parse("2006-01-02", raw); err == nil {
		return parsed.UTC(), nil
	}
	return time.Time{}, errInvalidInput
}

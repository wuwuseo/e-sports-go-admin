package esports

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentreceipt"
	"go-admin/internal/ent/esportsproductcategory"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/predicate"
)

// §17 报表的取数与聚合口径：
//   - 总量与分组统计一律走数据库端聚合（Ent 的 Aggregate / GroupBy），大样本下与订单行数无关，
//     不再因为超过某个扫描上限而整体报错；
//   - 只有下钻明细列表保留上限（reportDetailLimit），超限时在响应里显式给出 detail_truncated，
//     避免「静默漏数」；
//   - 少数 Ent 表达不了的维度（订单分段的时间轴归集、按订单集合筛选的席位/售后）用有界分页或有界分片
//     在 Go 里做轻量后处理，任何一处都不会把全量单据逐行读进内存。
const (
	// reportDetailLimit 是下钻明细列表的默认上限，可由入参 limit 覆盖（1 ~ reportDetailLimit）。
	reportDetailLimit = 5000
	// reportBucketLimit 是时间轴分段的查询次数上限：数据跨度过大时逐级放粗粒度（日 → 周 → 月 → 年）。
	reportBucketLimit = 366
	// reportOrderIDPage 是依赖订单集合的报表（派单效率、售后关联、提成归属）分片归集的片大小。
	reportOrderIDPage = 2000
)

// reportBucketPeriods 是时间轴可用的分段粒度，由细到粗。
var reportBucketPeriods = []string{"daily", "weekly", "monthly", "yearly"}

// paidPaymentStatuses 是「订单已实收」的支付状态：部分到账也计入营收，异常到账不计。
var paidPaymentStatuses = []esportsorder.PaymentStatus{
	esportsorder.PaymentStatusPaid,
	esportsorder.PaymentStatusPartial,
}

// reportFilter 是报表筛选条件。零值表示该维度不过滤。
type reportFilter struct {
	Start time.Time
	End   time.Time
	// Period 决定时间轴分段粒度：daily / weekly / monthly。
	Period           string
	ResponsibleAdmin int
	CreatedByAdmin   int
	SkuID            int
	Game             string
	ChannelSource    string
	EntryPoint       string
	AppointmentType  string
	SeatType         string
	PaymentChannel   string
	AftersaleKind    string
	FaultParty       string
	Limit            int
}

// reportBucket 是经营总览时间轴的一段。
type reportBucket struct {
	Bucket       string `json:"bucket"`
	OrderCount   int    `json:"order_count"`
	RevenueCents int64  `json:"revenue_cents"`
	RefundCents  int64  `json:"refund_cents"`
}

// reportBucketRange 是时间轴分段的一段（绝对时刻半开区间 [Start, End)，均以 UTC 存储）。
type reportBucketRange struct {
	Start time.Time
	End   time.Time
}

// csPerformanceRow 是客服个人业绩报表的一行（按负责客服 / 业绩归属聚合）。
type csPerformanceRow struct {
	AdminID      int    `json:"admin_id"`
	AdminName    string `json:"admin_name"`
	OrderCount   int    `json:"order_count"`
	RevenueCents int64  `json:"revenue_cents"`
	RefundCents  int64  `json:"refund_cents"`
	NetCents     int64  `json:"net_cents"`
	// 提成（§12.2）：区间内该客服的客服提成明细净额（含负数冲回）。
	CommissionCents int64 `json:"commission_cents"`
}

// dispatchEfficiencyRow 是派单效率报表的一行（按邀请管理员聚合）。
type dispatchEfficiencyRow struct {
	AdminID       int    `json:"admin_id"`
	AdminName     string `json:"admin_name"`
	InviteCount   int    `json:"invite_count"`
	AcceptedCount int    `json:"accepted_count"`
	RejectedCount int    `json:"rejected_count"`
	ExpiredCount  int    `json:"expired_count"`
}

// financeChannelRow 是财务资金报表按支付方式聚合的一行。
type financeChannelRow struct {
	Channel     string `json:"channel"`
	AmountCents int64  `json:"amount_cents"`
	Count       int    `json:"count"`
}

// orderOverviewTotals 是经营总览一次 SQL 聚合得到的总量口径。
type orderOverviewTotals struct {
	OrderCount     int   `json:"order_count"`
	PaidCount      int   `json:"paid_count"`
	RevenueCents   int64 `json:"revenue_cents"`
	RefundCents    int64 `json:"refund_cents"`
	CompletedCount int   `json:"completed_count"`
	CancelledCount int   `json:"cancelled_count"`
}

// orderTimelineAggregate 是订单时间线埋点（§5.9）四类等待时长的聚合结果。
type orderTimelineAggregate struct {
	PoolToInviteSeconds   int64 `json:"pool_to_invite_seconds"`
	PoolToInviteCount     int   `json:"pool_to_invite_count"`
	InviteToAcceptSeconds int64 `json:"invite_to_accept_seconds"`
	InviteToAcceptCount   int   `json:"invite_to_accept_count"`
	AcceptToStartSeconds  int64 `json:"accept_to_start_seconds"`
	AcceptToStartCount    int   `json:"accept_to_start_count"`
	PoolToStartSeconds    int64 `json:"pool_to_start_seconds"`
	PoolToStartCount      int   `json:"pool_to_start_count"`
}

// reportOrderPredicates 把报表筛选条件翻译成订单查询谓词。
func reportOrderPredicates(ctx context.Context, db *ent.Client, f reportFilter) ([]predicate.EsportsOrder, error) {
	ps := []predicate.EsportsOrder{
		esportsorder.CreatedAtGTE(f.Start),
		esportsorder.CreatedAtLT(f.End),
	}
	if f.ResponsibleAdmin > 0 {
		ps = append(ps, esportsorder.ResponsibleAdminIDEQ(f.ResponsibleAdmin))
	}
	if f.CreatedByAdmin > 0 {
		ps = append(ps, esportsorder.CreatedByAdminIDEQ(f.CreatedByAdmin))
	}
	if f.ChannelSource != "" {
		ps = append(ps, esportsorder.ChannelSourceEQ(esportsorder.ChannelSource(f.ChannelSource)))
	}
	if f.EntryPoint != "" {
		ps = append(ps, esportsorder.EntryPointEQ(esportsorder.EntryPoint(f.EntryPoint)))
	}
	if f.PaymentChannel != "" {
		ps = append(ps, esportsorder.PaymentChannelEQ(esportsorder.PaymentChannel(f.PaymentChannel)))
	}
	if f.SkuID > 0 {
		ps = append(ps, esportsorder.SkuIDIn(f.SkuID))
	}
	if f.Game != "" {
		ids, err := skuIDsForGame(ctx, db, f.Game)
		if err != nil {
			return nil, err
		}
		ps = append(ps, skuFilter(ids))
	}
	switch f.AppointmentType {
	case "instant":
		ps = append(ps, esportsorder.AppointmentStatusEQ(esportsorder.AppointmentStatusInstant))
	case "request":
		ps = append(ps, esportsorder.AppointmentStatusNEQ(esportsorder.AppointmentStatusInstant))
	}
	switch f.SeatType {
	case "single":
		ps = append(ps, esportsorder.RequiredSeatsEQ(1))
	case "multi":
		ps = append(ps, esportsorder.RequiredSeatsGT(1))
	}
	return ps, nil
}

// skuIDsForGame 取某游戏下的全部 SKU ID（经产品分类归属）。
func skuIDsForGame(ctx context.Context, db *ent.Client, game string) ([]int, error) {
	categoryIDs, err := db.EsportsProductCategory.Query().Where(esportsproductcategory.GameEQ(game)).IDs(ctx)
	if err != nil || len(categoryIDs) == 0 {
		return nil, err
	}
	return db.EsportsSku.Query().Where(esportssku.CategoryIDIn(categoryIDs...)).IDs(ctx)
}

// skuFilter 构造 SKU 归属过滤；无匹配 SKU 时返回恒假谓词（id <= 0）。
func skuFilter(ids []int) predicate.EsportsOrder {
	if len(ids) == 0 {
		return esportsorder.IDLTE(0)
	}
	return esportsorder.SkuIDIn(ids...)
}

// withOrderRange 在既有订单谓词上追加一个时间子区间，用于时间轴分段聚合。
// 注意复制切片，避免 append 复用调用方底层数组。
func withOrderRange(predicates []predicate.EsportsOrder, start, end time.Time) []predicate.EsportsOrder {
	scoped := make([]predicate.EsportsOrder, 0, len(predicates)+2)
	scoped = append(scoped, predicates...)
	return append(scoped, esportsorder.CreatedAtGTE(start), esportsorder.CreatedAtLT(end))
}

// forEachOrderIDPage 按主键分页（keyset）遍历符合订单筛选的订单 ID，逐片交给 fn。
// 报表里依赖订单集合的维度（席位、关联售后、提成归属）无法用 Ent 表达为子查询，
// 这里以有界分片替代一次性物化全部订单。
func forEachOrderIDPage(ctx context.Context, db *ent.Client, predicates []predicate.EsportsOrder, fn func(ids []int) error) error {
	var cursor int
	for {
		page := make([]predicate.EsportsOrder, 0, len(predicates)+1)
		page = append(page, predicates...)
		page = append(page, esportsorder.IDGT(cursor))
		ids, err := db.EsportsOrder.Query().Where(page...).
			Order(ent.Asc(esportsorder.FieldID)).Limit(reportOrderIDPage).IDs(ctx)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := fn(ids); err != nil {
			return err
		}
		cursor = ids[len(ids)-1]
		if len(ids) < reportOrderIDPage {
			return nil
		}
	}
}

// reportDetailCap 归一化下钻明细上限：入参缺省取默认值，上限受 reportDetailLimit 约束。
func reportDetailCap(limit int) int {
	if limit < 1 {
		return reportDetailLimit
	}
	if limit > reportDetailLimit {
		return reportDetailLimit
	}
	return limit
}

// enumIn 返回「列取值属于给定枚举集合」的条件构造器。
// values 只来自本包引用的枚举常量，不含用户输入，因此可以安全地内联为 SQL 字面量。
func enumIn[T ~string](column string, values ...T) func(*sql.Selector) string {
	return func(s *sql.Selector) string {
		quoted := make([]string, 0, len(values))
		for _, value := range values {
			quoted = append(quoted, "'"+string(value)+"'")
		}
		return s.C(column) + " IN (" + strings.Join(quoted, ", ") + ")"
	}
}

// notNull 返回「给定列全部非空」的条件构造器。
func notNull(columns ...string) func(*sql.Selector) string {
	return func(s *sql.Selector) string {
		parts := make([]string, 0, len(columns))
		for _, column := range columns {
			parts = append(parts, s.C(column)+" IS NOT NULL")
		}
		return strings.Join(parts, " AND ")
	}
}

// columnExpr 返回列引用表达式构造器。
func columnExpr(column string) func(*sql.Selector) string {
	return func(s *sql.Selector) string { return s.C(column) }
}

// secondsBetween 返回两个时间点列之间的整数秒差表达式，按数据库方言选择实现。
// 结果就地取整（四舍五入消除浮点误差），与既有「按行求秒差」的口径一致。
func secondsBetween(fromColumn, toColumn string) func(*sql.Selector) string {
	return func(s *sql.Selector) string {
		from, to := s.C(fromColumn), s.C(toColumn)
		switch s.Dialect() {
		case dialect.SQLite:
			return "CAST(ROUND((julianday(" + to + ") - julianday(" + from + ")) * 86400) AS INTEGER)"
		case dialect.Postgres:
			return "CAST(ROUND(EXTRACT(EPOCH FROM (" + to + " - " + from + "))) AS BIGINT)"
		default: // MySQL / MariaDB
			return "TIMESTAMPDIFF(SECOND, " + from + ", " + to + ")"
		}
	}
}

// reportCount 返回「满足条件的行数」聚合函数。
func reportCount(condition func(*sql.Selector) string) ent.AggregateFunc {
	return func(s *sql.Selector) string {
		return "SUM(CASE WHEN " + condition(s) + " THEN 1 ELSE 0 END)"
	}
}

// reportSum 返回「满足条件的表达式求和」聚合函数。
func reportSum(condition func(*sql.Selector) string, expr func(*sql.Selector) string) ent.AggregateFunc {
	return func(s *sql.Selector) string {
		return "SUM(CASE WHEN " + condition(s) + " THEN " + expr(s) + " ELSE 0 END)"
	}
}

// reportBucketStart 把时间按周期粒度归到分段起点，分段边界统一用工作室业务时区（cstZone，UTC+8），
// 避免服务器 UTC 或本地时区导致跨日错位（与生日礼遇、提现每日次数等自然日口径一致）。
// 返回值保留业务时区定位，供后续 AddDate 做日历运算（周 / 月 / 年加减必须在业务时区日历下进行）。
func reportBucketStart(t time.Time, period string) time.Time {
	local := t.In(cstZone)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, cstZone)
	switch period {
	case "weekly":
		offset := (int(day.Weekday()) + 6) % 7 // 距本周一的天数
		return day.AddDate(0, 0, -offset)
	case "monthly":
		return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, cstZone)
	case "yearly":
		return time.Date(local.Year(), 1, 1, 0, 0, 0, 0, cstZone)
	default:
		return day
	}
}

// reportBucketKey 按周期粒度把时间归到分段的起始日（业务时区日期）。
func reportBucketKey(t time.Time, period string) string {
	return reportBucketStart(t, period).Format("2006-01-02")
}

// nextReportBucket 返回给定分段起点之后的一段起点。
func nextReportBucket(start time.Time, period string) time.Time {
	switch period {
	case "weekly":
		return start.AddDate(0, 0, 7)
	case "monthly":
		return start.AddDate(0, 1, 0)
	case "yearly":
		return start.AddDate(1, 0, 0)
	default:
		return start.AddDate(0, 0, 1)
	}
}

// reportBucketStarts 把 [first, last] 按粒度切成升序的分段起点。
func reportBucketStarts(first, last time.Time, period string) []time.Time {
	if first.After(last) {
		return nil
	}
	starts := make([]time.Time, 0, 8)
	for cursor := reportBucketStart(first, period); !cursor.After(last); cursor = nextReportBucket(cursor, period) {
		starts = append(starts, cursor)
		if len(starts) > reportBucketLimit { // 安全阀：粒度放粗失效时不再继续累积
			break
		}
	}
	return starts
}

// reportBucketPlan 返回时间轴分段方案：实际采用的粒度与各段区间。
// 数据跨度过大时逐级放粗粒度（日 → 周 → 月 → 年），保证分段查询次数有界；
// 极端跨度下退化为整段一段，不丢任何数据。
func reportBucketPlan(first, last time.Time, period string) (string, []reportBucketRange) {
	begin := 0
	for i, candidate := range reportBucketPeriods {
		if candidate == period {
			begin = i
			break
		}
	}
	for _, candidate := range reportBucketPeriods[begin:] {
		starts := reportBucketStarts(first, last, candidate)
		if len(starts) > reportBucketLimit {
			continue
		}
		ranges := make([]reportBucketRange, 0, len(starts))
		for _, start := range starts {
			// 分段区间转为 UTC 绝对时刻，供 SQL 范围过滤；业务时区日历运算已在 reportBucketStart/nextReportBucket 完成。
			ranges = append(ranges, reportBucketRange{Start: start.UTC(), End: nextReportBucket(start, candidate).UTC()})
		}
		return candidate, ranges
	}
	return "range", []reportBucketRange{{Start: first, End: last.Add(time.Nanosecond)}}
}

// reportOrderCreatedSpan 取符合筛选的订单最早 / 最晚下单时间（各取一行，仅用于收敛时间轴分段范围）。
func reportOrderCreatedSpan(ctx context.Context, db *ent.Client, predicates []predicate.EsportsOrder) (time.Time, time.Time, bool, error) {
	first, err := db.EsportsOrder.Query().Where(predicates...).
		Order(ent.Asc(esportsorder.FieldCreatedAt), ent.Asc(esportsorder.FieldID)).Limit(1).
		Select(esportsorder.FieldCreatedAt).All(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	if len(first) == 0 {
		return time.Time{}, time.Time{}, false, nil
	}
	last, err := db.EsportsOrder.Query().Where(predicates...).
		Order(ent.Desc(esportsorder.FieldCreatedAt), ent.Desc(esportsorder.FieldID)).Limit(1).
		Select(esportsorder.FieldCreatedAt).All(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	return first[0].CreatedAt, last[0].CreatedAt, true, nil
}

// adminNames 批量取管理员展示名（昵称优先，回落用户名）。
func adminNames(ctx context.Context, db *ent.Client, ids []int) map[int]string {
	names := map[int]string{}
	if len(ids) == 0 {
		return names
	}
	items, err := db.Admin.Query().Where(admin.IDIn(ids...)).All(ctx)
	if err != nil {
		return names
	}
	for _, item := range items {
		if item.Nickname != "" {
			names[item.ID] = item.Nickname
		} else {
			names[item.ID] = item.Username
		}
	}
	return names
}

// ownerOverviewReport 老板经营总览：区间内订单量、营收、退款与净额，并按周期分段。
// 汇总与分段均为数据库端聚合；下钻明细单独限流并标记是否被截断。
func ownerOverviewReport(ctx context.Context, db *ent.Client, f reportFilter) (map[string]any, error) {
	predicates, err := reportOrderPredicates(ctx, db, f)
	if err != nil {
		return nil, err
	}
	// 汇总：一次 SQL 聚合拿到全部总量口径。
	var totals []orderOverviewTotals
	if err := db.EsportsOrder.Query().Where(predicates...).Aggregate(
		ent.As(ent.Count(), "order_count"),
		ent.As(reportCount(enumIn(esportsorder.FieldPaymentStatus, paidPaymentStatuses...)), "paid_count"),
		ent.As(reportSum(enumIn(esportsorder.FieldPaymentStatus, paidPaymentStatuses...),
			columnExpr(esportsorder.FieldPayableCents)), "revenue_cents"),
		ent.As(ent.Sum(esportsorder.FieldRefundedCents), "refund_cents"),
		ent.As(reportCount(enumIn(esportsorder.FieldFulfillmentStatus, esportsorder.FulfillmentStatusCompleted)), "completed_count"),
		ent.As(reportCount(enumIn(esportsorder.FieldFulfillmentStatus, esportsorder.FulfillmentStatusCancelled)), "cancelled_count"),
	).Scan(ctx, &totals); err != nil {
		return nil, err
	}
	var aggregate orderOverviewTotals
	if len(totals) > 0 {
		aggregate = totals[0]
	}

	// 时间轴分段：先取数据实际跨度收敛分段范围，再逐段做 SQL 聚合（只保留有数据的分段，与既有口径一致）。
	buckets := make([]reportBucket, 0)
	bucketPeriod := f.Period
	if aggregate.OrderCount > 0 {
		first, last, ok, err := reportOrderCreatedSpan(ctx, db, predicates)
		if err != nil {
			return nil, err
		}
		if ok {
			period, ranges := reportBucketPlan(first, last, f.Period)
			bucketPeriod = period
			for _, item := range ranges {
				var rows []struct {
					OrderCount   int   `json:"order_count"`
					RevenueCents int64 `json:"revenue_cents"`
					RefundCents  int64 `json:"refund_cents"`
				}
				if err := db.EsportsOrder.Query().Where(withOrderRange(predicates, item.Start, item.End)...).Aggregate(
					ent.As(ent.Count(), "order_count"),
					ent.As(reportSum(enumIn(esportsorder.FieldPaymentStatus, paidPaymentStatuses...),
						columnExpr(esportsorder.FieldPayableCents)), "revenue_cents"),
					ent.As(ent.Sum(esportsorder.FieldRefundedCents), "refund_cents"),
				).Scan(ctx, &rows); err != nil {
					return nil, err
				}
				if len(rows) == 0 || rows[0].OrderCount == 0 {
					continue
				}
				buckets = append(buckets, reportBucket{
					Bucket:       reportBucketKey(item.Start, period),
					OrderCount:   rows[0].OrderCount,
					RevenueCents: rows[0].RevenueCents,
					RefundCents:  rows[0].RefundCents,
				})
			}
		}
	}

	// 下钻明细：单独限流，最多多取一条用于判断是否被截断。
	detailCap := reportDetailCap(f.Limit)
	orders, err := db.EsportsOrder.Query().Where(predicates...).
		Order(ent.Asc(esportsorder.FieldCreatedAt), ent.Asc(esportsorder.FieldID)).
		Limit(detailCap+1).
		Select(esportsorder.FieldID, esportsorder.FieldOrderNo, esportsorder.FieldCustomerID,
			esportsorder.FieldPayableCents, esportsorder.FieldRefundedCents,
			esportsorder.FieldPaymentStatus, esportsorder.FieldFulfillmentStatus,
			esportsorder.FieldCreatedAt).All(ctx)
	if err != nil {
		return nil, err
	}
	detailTruncated := len(orders) > detailCap
	if detailTruncated {
		orders = orders[:detailCap]
	}
	details := make([]map[string]any, 0, len(orders))
	for _, order := range orders {
		details = append(details, map[string]any{
			"id": order.ID, "order_no": order.OrderNo, "customer_id": order.CustomerID,
			"payable_cents": order.PayableCents, "refunded_cents": order.RefundedCents,
			"payment_status": order.PaymentStatus, "fulfillment_status": order.FulfillmentStatus,
			"created_at": order.CreatedAt,
		})
	}
	return map[string]any{
		"type": "owner_overview",
		"summary": map[string]any{
			"order_count": aggregate.OrderCount, "paid_count": aggregate.PaidCount,
			"revenue_cents": aggregate.RevenueCents, "refund_cents": aggregate.RefundCents,
			"net_cents":       aggregate.RevenueCents - aggregate.RefundCents,
			"completed_count": aggregate.CompletedCount, "cancelled_count": aggregate.CancelledCount,
		},
		"buckets":          buckets,
		"bucket_period":    bucketPeriod,
		"details":          details,
		"detail_truncated": detailTruncated,
	}, nil
}

// csPerformanceReport 客服个人业绩：按负责客服（业绩归属）分组统计订单量与营收。
// 分组与提成归集均为数据库端聚合，不再受订单 / 流水行数限制。
func csPerformanceReport(ctx context.Context, db *ent.Client, f reportFilter) (map[string]any, error) {
	predicates, err := reportOrderPredicates(ctx, db, f)
	if err != nil {
		return nil, err
	}
	// 订单维度：按负责客服分组一次聚合（业绩归属为空的行不计入任何客服）。
	var orderRows []struct {
		AdminID      int   `json:"responsible_admin_id"`
		OrderCount   int   `json:"order_count"`
		RevenueCents int64 `json:"revenue_cents"`
		RefundCents  int64 `json:"refund_cents"`
	}
	if err := db.EsportsOrder.Query().Where(predicates...).
		GroupBy(esportsorder.FieldResponsibleAdminID).
		Aggregate(
			ent.As(ent.Count(), "order_count"),
			ent.As(ent.Sum(esportsorder.FieldRefundedCents), "refund_cents"),
			ent.As(reportSum(enumIn(esportsorder.FieldPaymentStatus, paidPaymentStatuses...),
				columnExpr(esportsorder.FieldPayableCents)), "revenue_cents"),
		).Scan(ctx, &orderRows); err != nil {
		return nil, err
	}
	// 提成（§12.2）：按业绩归属客服聚合区间内提成明细净额，含负数冲回；只有提成没有订单的客服也要成行。
	// 订单维度筛选生效时，提成只计入「关联订单命中筛选」的明细；该限制按订单主键分片归集（Ent 无子查询表达）。
	orderDimensions := f.ChannelSource != "" || f.EntryPoint != "" || f.PaymentChannel != "" ||
		f.CreatedByAdmin > 0 || f.SkuID > 0 || f.Game != "" || f.AppointmentType != "" || f.SeatType != ""
	commissions := map[int]int64{}
	collectCommission := func(orderIDs []int) error {
		query := db.EsportsCsCommissionEntry.Query().Where(
			esportscscommissionentry.CreatedAtGTE(f.Start),
			esportscscommissionentry.CreatedAtLT(f.End),
		)
		if f.ResponsibleAdmin > 0 {
			query.Where(esportscscommissionentry.AdminIDEQ(f.ResponsibleAdmin))
		}
		if len(orderIDs) > 0 {
			query.Where(esportscscommissionentry.OrderIDIn(orderIDs...))
		}
		var rows []struct {
			AdminID int   `json:"admin_id"`
			Amount  int64 `json:"amount_cents"`
		}
		if err := query.GroupBy(esportscscommissionentry.FieldAdminID).
			Aggregate(ent.As(ent.Sum(esportscscommissionentry.FieldAmountCents), "amount_cents")).
			Scan(ctx, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			commissions[row.AdminID] += row.Amount
		}
		return nil
	}
	if orderDimensions {
		if err := forEachOrderIDPage(ctx, db, predicates, collectCommission); err != nil {
			return nil, err
		}
	} else if err := collectCommission(nil); err != nil {
		return nil, err
	}

	// 合并订单与提成结果。
	index := map[int]int{}
	rows := make([]csPerformanceRow, 0, len(orderRows))
	upsert := func(adminID int) int {
		at, ok := index[adminID]
		if !ok {
			at = len(rows)
			index[adminID] = at
			rows = append(rows, csPerformanceRow{AdminID: adminID})
		}
		return at
	}
	for _, row := range orderRows {
		if row.AdminID <= 0 { // 无业绩归属客服的订单不计入个人业绩
			continue
		}
		at := upsert(row.AdminID)
		rows[at].OrderCount += row.OrderCount
		rows[at].RevenueCents += row.RevenueCents
		rows[at].RefundCents += row.RefundCents
	}
	for adminID, amount := range commissions {
		if adminID <= 0 {
			continue
		}
		at := upsert(adminID)
		rows[at].CommissionCents += amount
	}
	ids := make([]int, 0, len(rows))
	for i := range rows {
		rows[i].NetCents = rows[i].RevenueCents - rows[i].RefundCents
		ids = append(ids, rows[i].AdminID)
	}
	names := adminNames(ctx, db, ids)
	for i := range rows {
		rows[i].AdminName = names[rows[i].AdminID]
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].NetCents != rows[j].NetCents {
			return rows[i].NetCents > rows[j].NetCents
		}
		return rows[i].OrderCount > rows[j].OrderCount
	})
	return map[string]any{"type": "cs_performance", "rows": rows}, nil
}

// dispatchEfficiencyReport 派单效率：区间内席位的邀请、接单、拒单、过期与平均接单时长。
// 席位按（邀请管理员 + 状态）分片聚合，订单时间线等待时长按方言在 SQL 内求差。
func dispatchEfficiencyReport(ctx context.Context, db *ent.Client, f reportFilter) (map[string]any, error) {
	predicates, err := reportOrderPredicates(ctx, db, f)
	if err != nil {
		return nil, err
	}
	type seatGroup struct {
		AdminID          int
		Status           string
		SeatCount        int
		RespondedCount   int
		RespondedSeconds int64
	}
	groups := make([]seatGroup, 0)
	// 席位聚合：Ent 无订单边可比对，按订单主键分片归集后再合并（各分组计数与时长求和均可加）。
	err = forEachOrderIDPage(ctx, db, predicates, func(orderIDs []int) error {
		var rows []struct {
			AdminID          int    `json:"invited_by_admin_id"`
			Status           string `json:"status"`
			SeatCount        int    `json:"seat_count"`
			RespondedCount   int    `json:"responded_count"`
			RespondedSeconds int64  `json:"responded_seconds"`
		}
		if err := db.EsportsDispatchSeat.Query().Where(esportsdispatchseat.OrderIDIn(orderIDs...)).
			GroupBy(esportsdispatchseat.FieldInvitedByAdminID, esportsdispatchseat.FieldStatus).
			Aggregate(
				ent.As(ent.Count(), "seat_count"),
				ent.As(reportCount(notNull(esportsdispatchseat.FieldRespondedAt)), "responded_count"),
				ent.As(reportSum(notNull(esportsdispatchseat.FieldRespondedAt),
					secondsBetween(esportsdispatchseat.FieldCreatedAt, esportsdispatchseat.FieldRespondedAt)), "responded_seconds"),
			).Scan(ctx, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			groups = append(groups, seatGroup{
				AdminID: row.AdminID, Status: row.Status, SeatCount: row.SeatCount,
				RespondedCount: row.RespondedCount, RespondedSeconds: row.RespondedSeconds,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	var invite, accepted, rejected, expired, open int
	var respondedSeconds int64
	var respondedCount int
	index := map[int]int{}
	rows := make([]dispatchEfficiencyRow, 0)
	for _, group := range groups {
		// 全局状态计数覆盖全部席位（含没有邀请人的席位）。
		switch group.Status {
		case string(esportsdispatchseat.StatusAccepted):
			accepted += group.SeatCount
		case string(esportsdispatchseat.StatusRejected):
			rejected += group.SeatCount
		case string(esportsdispatchseat.StatusExpired):
			expired += group.SeatCount
		case string(esportsdispatchseat.StatusOpen):
			open += group.SeatCount
		}
		respondedSeconds += group.RespondedSeconds
		respondedCount += group.RespondedCount
		if group.AdminID <= 0 {
			continue
		}
		at, ok := index[group.AdminID]
		if !ok {
			index[group.AdminID] = len(rows)
			rows = append(rows, dispatchEfficiencyRow{AdminID: group.AdminID})
			at = index[group.AdminID]
		}
		invite += group.SeatCount
		rows[at].InviteCount += group.SeatCount
		switch group.Status {
		case string(esportsdispatchseat.StatusAccepted):
			rows[at].AcceptedCount += group.SeatCount
		case string(esportsdispatchseat.StatusRejected):
			rows[at].RejectedCount += group.SeatCount
		case string(esportsdispatchseat.StatusExpired):
			rows[at].ExpiredCount += group.SeatCount
		}
	}
	var average int64
	if respondedCount > 0 {
		average = respondedSeconds / int64(respondedCount)
	}

	// §5.9 等待时长：按订单时间线埋点派生四类平均等待时长（只有时间点齐全的订单才计入样本）。
	// 入池 → 派出（客服/系统派单快慢）、派出 → 接单（陪玩响应）、接单 → 开工（开工延迟）、
	// 入池 → 开工（端到端等待）。缺埋点的历史订单自动排除，不影响计数。
	var timeline []orderTimelineAggregate
	poolToInvite := notNull(esportsorder.FieldFirstPooledAt, esportsorder.FieldFirstInvitedAt)
	inviteToAccept := notNull(esportsorder.FieldFirstInvitedAt, esportsorder.FieldFirstAcceptedAt)
	acceptToStart := notNull(esportsorder.FieldFirstAcceptedAt, esportsorder.FieldServiceStartedAt)
	poolToStart := notNull(esportsorder.FieldFirstPooledAt, esportsorder.FieldServiceStartedAt)
	if err := db.EsportsOrder.Query().Where(predicates...).Aggregate(
		ent.As(reportSum(poolToInvite,
			secondsBetween(esportsorder.FieldFirstPooledAt, esportsorder.FieldFirstInvitedAt)), "pool_to_invite_seconds"),
		ent.As(reportCount(poolToInvite), "pool_to_invite_count"),
		ent.As(reportSum(inviteToAccept,
			secondsBetween(esportsorder.FieldFirstInvitedAt, esportsorder.FieldFirstAcceptedAt)), "invite_to_accept_seconds"),
		ent.As(reportCount(inviteToAccept), "invite_to_accept_count"),
		ent.As(reportSum(acceptToStart,
			secondsBetween(esportsorder.FieldFirstAcceptedAt, esportsorder.FieldServiceStartedAt)), "accept_to_start_seconds"),
		ent.As(reportCount(acceptToStart), "accept_to_start_count"),
		ent.As(reportSum(poolToStart,
			secondsBetween(esportsorder.FieldFirstPooledAt, esportsorder.FieldServiceStartedAt)), "pool_to_start_seconds"),
		ent.As(reportCount(poolToStart), "pool_to_start_count"),
	).Scan(ctx, &timeline); err != nil {
		return nil, err
	}
	avgSeconds := func(total int64, count int) int64 {
		if count == 0 {
			return 0
		}
		return total / int64(count)
	}
	var timelineRow orderTimelineAggregate
	if len(timeline) > 0 {
		timelineRow = timeline[0]
	}

	ids := make([]int, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].AdminID)
	}
	names := adminNames(ctx, db, ids)
	for i := range rows {
		rows[i].AdminName = names[rows[i].AdminID]
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].InviteCount > rows[j].InviteCount })
	return map[string]any{
		"type": "dispatch_efficiency",
		"summary": map[string]any{
			"invite_count": invite, "accepted_count": accepted, "rejected_count": rejected,
			"expired_count": expired, "open_count": open, "avg_accept_seconds": average,
			"avg_pool_to_invite_seconds":   avgSeconds(timelineRow.PoolToInviteSeconds, timelineRow.PoolToInviteCount),
			"avg_invite_to_accept_seconds": avgSeconds(timelineRow.InviteToAcceptSeconds, timelineRow.InviteToAcceptCount),
			"avg_accept_to_start_seconds":  avgSeconds(timelineRow.AcceptToStartSeconds, timelineRow.AcceptToStartCount),
			"avg_pool_to_start_seconds":    avgSeconds(timelineRow.PoolToStartSeconds, timelineRow.PoolToStartCount),
		},
		"rows": rows,
	}, nil
}

// errReportDetailFilled 用于提前结束作用域遍历（下钻明细已取满）。
var errReportDetailFilled = errors.New("esports: report detail filled")

// aftersaleQualityReport 售后质量：区间内售后单状态分布、赔付金额与按类型 / 责任方拆分。
// handlerAdmin > 0 时只统计该客服处理的售后（非超级管理员的数据边界）。
func aftersaleQualityReport(ctx context.Context, db *ent.Client, f reportFilter, handlerAdmin int) (map[string]any, error) {
	base := []predicate.EsportsAfterSale{
		esportsaftersale.CreatedAtGTE(f.Start),
		esportsaftersale.CreatedAtLT(f.End),
	}
	if handlerAdmin > 0 {
		base = append(base, esportsaftersale.HandlerAdminIDEQ(handlerAdmin))
	}
	if f.AftersaleKind != "" {
		base = append(base, esportsaftersale.KindEQ(esportsaftersale.Kind(f.AftersaleKind)))
	}
	if f.FaultParty != "" {
		base = append(base, esportsaftersale.FaultPartyEQ(esportsaftersale.FaultParty(f.FaultParty)))
	}
	// 按订单渠道 / 入口筛选售后时，Ent 没有「售后 → 订单」可比对的边，
	// 只能把命中的订单按主键分片后逐片作为作用域（分片而非一次性物化全部订单 ID）。
	sourcePredicates := make([]predicate.EsportsOrder, 0, 2)
	if f.ChannelSource != "" {
		sourcePredicates = append(sourcePredicates, esportsorder.ChannelSourceEQ(esportsorder.ChannelSource(f.ChannelSource)))
	}
	if f.EntryPoint != "" {
		sourcePredicates = append(sourcePredicates, esportsorder.EntryPointEQ(esportsorder.EntryPoint(f.EntryPoint)))
	}
	// eachScope 顺序遍历所有作用域；没有订单维度筛选时只有一个空作用域。
	eachScope := func(fn func(scope func(*ent.EsportsAfterSaleQuery)) error) error {
		if len(sourcePredicates) == 0 {
			return fn(nil)
		}
		return forEachOrderIDPage(ctx, db, sourcePredicates, func(ids []int) error {
			return fn(func(query *ent.EsportsAfterSaleQuery) {
				query.Where(esportsaftersale.SourceIDIn(ids...))
			})
		})
	}

	// 汇总与拆分：按（类型 + 状态 + 责任方）分组一次聚合，再在 Go 里做轻量归并。
	var total, refundCents int64
	statusCounts := map[string]int{}
	byKind := map[string]int{}
	byFaultParty := map[string]int{}
	if err := eachScope(func(scope func(*ent.EsportsAfterSaleQuery)) error {
		query := db.EsportsAfterSale.Query().Where(base...)
		if scope != nil {
			scope(query)
		}
		var rows []struct {
			Kind        string `json:"kind"`
			Status      string `json:"status"`
			FaultParty  string `json:"fault_party"`
			Count       int    `json:"count"`
			RefundCents int64  `json:"refund_cents"`
		}
		if err := query.
			GroupBy(esportsaftersale.FieldKind, esportsaftersale.FieldStatus, esportsaftersale.FieldFaultParty).
			Aggregate(ent.As(ent.Count(), "count"),
				ent.As(ent.Sum(esportsaftersale.FieldRefundCents), "refund_cents")).
			Scan(ctx, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			total += int64(row.Count)
			refundCents += row.RefundCents
			statusCounts[row.Status] += row.Count
			byKind[row.Kind] += row.Count
			byFaultParty[row.FaultParty] += row.Count
		}
		return nil
	}); err != nil {
		return nil, err
	}
	completed := statusCounts[string(esportsaftersale.StatusCompleted)]
	rejected := statusCounts[string(esportsaftersale.StatusRejected)]
	withdrawn := statusCounts[string(esportsaftersale.StatusWithdrawn)]
	inProgress := int(total) - completed - rejected - withdrawn

	// 下钻明细：单独限流，逐作用域取满即止，最多多取一条用于判断截断。
	detailCap := reportDetailCap(f.Limit)
	details := make([]map[string]any, 0, detailCap)
	err := eachScope(func(scope func(*ent.EsportsAfterSaleQuery)) error {
		if len(details) > detailCap {
			return errReportDetailFilled
		}
		query := db.EsportsAfterSale.Query().Where(base...)
		if scope != nil {
			scope(query)
		}
		rows, err := query.
			Order(ent.Asc(esportsaftersale.FieldID)).
			Limit(detailCap+1-len(details)).
			Select(esportsaftersale.FieldID, esportsaftersale.FieldKind,
				esportsaftersale.FieldStatus, esportsaftersale.FieldFaultParty,
				esportsaftersale.FieldRefundCents, esportsaftersale.FieldCreatedAt).All(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			details = append(details, map[string]any{
				"id": row.ID, "kind": row.Kind, "status": row.Status,
				"fault_party": row.FaultParty, "refund_cents": row.RefundCents,
				"created_at": row.CreatedAt,
			})
		}
		return nil
	})
	if err != nil && !errors.Is(err, errReportDetailFilled) {
		return nil, err
	}
	detailTruncated := len(details) > detailCap
	if detailTruncated {
		details = details[:detailCap]
	}
	return map[string]any{
		"type": "aftersale_quality",
		"summary": map[string]any{
			"total": int(total), "completed": completed, "rejected": rejected,
			"withdrawn": withdrawn, "in_progress": inProgress, "refund_cents": refundCents,
		},
		"by_kind":          byKind,
		"by_fault_party":   byFaultParty,
		"details":          details,
		"detail_truncated": detailTruncated,
	}, nil
}

// financeReport 财务资金与结算：区间内钱包流水、结算单与按支付方式聚合的支付回执。
func financeReport(ctx context.Context, db *ent.Client, f reportFilter) (map[string]any, error) {
	// 钱包流水：按（账户类型 + 流水类型）分组求和；储值卡是独立预付账户，
	// 单独分桶，不与客户余额混算（§11.3）。
	var walletRows []struct {
		AccountType string `json:"account_type"`
		EntryType   string `json:"entry_type"`
		Principal   int64  `json:"principal_delta_cents"`
	}
	if err := db.EsportsWalletTx.Query().Where(
		esportswallettx.CreatedAtGTE(f.Start),
		esportswallettx.CreatedAtLT(f.End),
	).GroupBy(esportswallettx.FieldAccountType, esportswallettx.FieldEntryType).
		Aggregate(ent.As(ent.Sum(esportswallettx.FieldPrincipalDeltaCents), "principal_delta_cents")).
		Scan(ctx, &walletRows); err != nil {
		return nil, err
	}
	customerPrincipal := map[string]int64{}
	playerPrincipal := map[string]int64{}
	storedValuePrincipal := map[string]int64{}
	for _, row := range walletRows {
		switch row.AccountType {
		case string(esportswallettx.AccountTypePlayerCatfood):
			playerPrincipal[row.EntryType] += row.Principal
		case string(esportswallettx.AccountTypeStoredValueCard):
			storedValuePrincipal[row.EntryType] += row.Principal
		default:
			customerPrincipal[row.EntryType] += row.Principal
		}
	}

	// 结算单：按状态分组一次聚合，再合并总量与状态分布。
	var settlementRows []struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
		Gross  int64  `json:"gross_cents"`
		Net    int64  `json:"net_cents"`
	}
	if err := db.EsportsSettlement.Query().Where(
		esportssettlement.CreatedAtGTE(f.Start),
		esportssettlement.CreatedAtLT(f.End),
	).GroupBy(esportssettlement.FieldStatus).
		Aggregate(ent.As(ent.Count(), "count"),
			ent.As(ent.Sum(esportssettlement.FieldGrossCents), "gross_cents"),
			ent.As(ent.Sum(esportssettlement.FieldNetCents), "net_cents")).
		Scan(ctx, &settlementRows); err != nil {
		return nil, err
	}
	var settlementCount int
	var settlementGross, settlementNet int64
	settlementStatus := map[string]int{}
	for _, row := range settlementRows {
		settlementCount += row.Count
		settlementGross += row.Gross
		settlementNet += row.Net
		settlementStatus[row.Status] = row.Count
	}

	// 支付回执：按支付方式分组聚合。
	receiptQuery := db.EsportsPaymentReceipt.Query().Where(
		esportspaymentreceipt.CreatedAtGTE(f.Start),
		esportspaymentreceipt.CreatedAtLT(f.End),
	)
	if f.PaymentChannel != "" {
		receiptQuery.Where(esportspaymentreceipt.ChannelEQ(f.PaymentChannel))
	}
	var receiptRows []struct {
		Channel string `json:"channel"`
		Count   int    `json:"count"`
		Amount  int64  `json:"amount_cents"`
	}
	if err := receiptQuery.GroupBy(esportspaymentreceipt.FieldChannel).
		Aggregate(ent.As(ent.Count(), "count"), ent.As(ent.Sum(esportspaymentreceipt.FieldActualCents), "amount_cents")).
		Scan(ctx, &receiptRows); err != nil {
		return nil, err
	}
	channels := make([]financeChannelRow, 0, len(receiptRows))
	for _, row := range receiptRows {
		channels = append(channels, financeChannelRow{Channel: row.Channel, AmountCents: row.Amount, Count: row.Count})
	}
	sort.Slice(channels, func(i, j int) bool {
		if channels[i].AmountCents != channels[j].AmountCents {
			return channels[i].AmountCents > channels[j].AmountCents
		}
		return channels[i].Channel < channels[j].Channel
	})
	return map[string]any{
		"type": "finance",
		"wallet": map[string]any{
			"customer_balance":  customerPrincipal,
			"player_catfood":    playerPrincipal,
			"stored_value_card": storedValuePrincipal,
		},
		"settlement": map[string]any{
			"count": settlementCount, "gross_cents": settlementGross,
			"net_cents": settlementNet, "by_status": settlementStatus,
		},
		"payment_channels": channels,
	}, nil
}

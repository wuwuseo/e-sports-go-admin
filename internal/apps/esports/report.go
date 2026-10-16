package esports

import (
	"context"
	"fmt"
	"sort"
	"time"

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

// 报表一次聚合的明细上限。§17 是只读聚合入口，数据量级以大促单日订单为界；
// ponytail: 应用层内存聚合，订单量级到十万级时改为 SQL 分组聚合（GROUP BY + SUM）。
const reportScanLimit = 5000

func reportLimitError(count int) error {
	if count > reportScanLimit {
		return fmt.Errorf("%w：报表明细超过 %d 条，请缩小时间范围或增加筛选条件", errInvalidInput, reportScanLimit)
	}
	return nil
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

// reportBucketKey 按周期粒度把时间归到分段的起始日（UTC）。
func reportBucketKey(t time.Time, period string) string {
	utc := t.UTC()
	day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case "weekly":
		offset := (int(day.Weekday()) + 6) % 7 // 距本周一的天数
		return day.AddDate(0, 0, -offset).Format("2006-01-02")
	case "monthly":
		return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	default:
		return day.Format("2006-01-02")
	}
}

// sortedBuckets 把分段 map 按时间升序拍平成切片。
func sortedBuckets(buckets map[string]*reportBucket) []reportBucket {
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]reportBucket, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, *buckets[key])
	}
	return rows
}

// trimDetails 裁剪下钻明细条数（默认 100，最多 500）。
func trimDetails(rows []map[string]any, limit int) []map[string]any {
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if len(rows) > limit {
		return rows[:limit]
	}
	return rows
}

// isPaidOrder 判断订单是否已实收（部分到账也计入营收，异常到账不计）。
func isPaidOrder(status esportsorder.PaymentStatus) bool {
	return status == esportsorder.PaymentStatusPaid || status == esportsorder.PaymentStatusPartial
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
func ownerOverviewReport(ctx context.Context, db *ent.Client, f reportFilter) (map[string]any, error) {
	predicates, err := reportOrderPredicates(ctx, db, f)
	if err != nil {
		return nil, err
	}
	orders, err := db.EsportsOrder.Query().Where(predicates...).Limit(reportScanLimit+1).
		Select(esportsorder.FieldID, esportsorder.FieldOrderNo, esportsorder.FieldCustomerID,
			esportsorder.FieldPayableCents, esportsorder.FieldRefundedCents,
			esportsorder.FieldPaymentStatus, esportsorder.FieldFulfillmentStatus,
			esportsorder.FieldCreatedAt).
		Order(ent.Asc(esportsorder.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	if err = reportLimitError(len(orders)); err != nil {
		return nil, err
	}
	var orderCount, paidCount, completedCount, cancelledCount int
	var revenue, refund int64
	buckets := map[string]*reportBucket{}
	details := make([]map[string]any, 0, len(orders))
	for _, order := range orders {
		orderCount++
		paid := isPaidOrder(order.PaymentStatus)
		if paid {
			paidCount++
			revenue += order.PayableCents
		}
		refund += order.RefundedCents
		switch order.FulfillmentStatus {
		case esportsorder.FulfillmentStatusCompleted:
			completedCount++
		case esportsorder.FulfillmentStatusCancelled:
			cancelledCount++
		}
		key := reportBucketKey(order.CreatedAt, f.Period)
		bucket := buckets[key]
		if bucket == nil {
			bucket = &reportBucket{Bucket: key}
			buckets[key] = bucket
		}
		bucket.OrderCount++
		bucket.RefundCents += order.RefundedCents
		if paid {
			bucket.RevenueCents += order.PayableCents
		}
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
			"order_count": orderCount, "paid_count": paidCount,
			"revenue_cents": revenue, "refund_cents": refund, "net_cents": revenue - refund,
			"completed_count": completedCount, "cancelled_count": cancelledCount,
		},
		"buckets": sortedBuckets(buckets),
		"details": trimDetails(details, f.Limit),
	}, nil
}

// csPerformanceReport 客服个人业绩：按负责客服（业绩归属）分组统计订单量与营收。
func csPerformanceReport(ctx context.Context, db *ent.Client, f reportFilter) (map[string]any, error) {
	predicates, err := reportOrderPredicates(ctx, db, f)
	if err != nil {
		return nil, err
	}
	orders, err := db.EsportsOrder.Query().Where(predicates...).Limit(reportScanLimit+1).
		Select(esportsorder.FieldID, esportsorder.FieldResponsibleAdminID, esportsorder.FieldPayableCents,
			esportsorder.FieldRefundedCents, esportsorder.FieldPaymentStatus).All(ctx)
	if err != nil {
		return nil, err
	}
	if err = reportLimitError(len(orders)); err != nil {
		return nil, err
	}
	index := map[int]int{}
	matchedOrderIDs := make(map[int]bool, len(orders))
	rows := make([]csPerformanceRow, 0)
	for _, order := range orders {
		matchedOrderIDs[order.ID] = true
		if order.ResponsibleAdminID == nil {
			continue
		}
		owner := *order.ResponsibleAdminID
		at, ok := index[owner]
		if !ok {
			index[owner] = len(rows)
			rows = append(rows, csPerformanceRow{AdminID: owner})
			at = index[owner]
		}
		rows[at].OrderCount++
		rows[at].RefundCents += order.RefundedCents
		if isPaidOrder(order.PaymentStatus) {
			rows[at].RevenueCents += order.PayableCents
		}
	}
	// 提成（§12.2）：按业绩归属客服聚合区间内提成明细净额，含负数冲回；只有提成没有订单的客服也要成行。
	commissionQuery := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.CreatedAtGTE(f.Start),
		esportscscommissionentry.CreatedAtLT(f.End),
	)
	if f.ResponsibleAdmin > 0 {
		commissionQuery.Where(esportscscommissionentry.AdminIDEQ(f.ResponsibleAdmin))
	}
	commissions, err := commissionQuery.Limit(reportScanLimit+1).
		Select(esportscscommissionentry.FieldAdminID, esportscscommissionentry.FieldOrderID,
			esportscscommissionentry.FieldAmountCents).All(ctx)
	if err != nil {
		return nil, err
	}
	if err = reportLimitError(len(commissions)); err != nil {
		return nil, err
	}
	orderDimensions := f.ChannelSource != "" || f.EntryPoint != "" || f.PaymentChannel != "" ||
		f.CreatedByAdmin > 0 || f.SkuID > 0 || f.Game != "" || f.AppointmentType != "" || f.SeatType != ""
	for _, entry := range commissions {
		if orderDimensions && (entry.OrderID == nil || !matchedOrderIDs[*entry.OrderID]) {
			continue
		}
		at, ok := index[entry.AdminID]
		if !ok {
			index[entry.AdminID] = len(rows)
			rows = append(rows, csPerformanceRow{AdminID: entry.AdminID})
			at = index[entry.AdminID]
		}
		rows[at].CommissionCents += entry.AmountCents
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
func dispatchEfficiencyReport(ctx context.Context, db *ent.Client, f reportFilter) (map[string]any, error) {
	predicates, err := reportOrderPredicates(ctx, db, f)
	if err != nil {
		return nil, err
	}
	orderIDs, err := db.EsportsOrder.Query().Where(predicates...).Limit(reportScanLimit + 1).IDs(ctx)
	if err != nil {
		return nil, err
	}
	if err = reportLimitError(len(orderIDs)); err != nil {
		return nil, err
	}
	result := map[string]any{
		"type": "dispatch_efficiency",
		"summary": map[string]any{
			"invite_count": 0, "accepted_count": 0, "rejected_count": 0,
			"expired_count": 0, "open_count": 0, "avg_accept_seconds": 0,
			"avg_pool_to_invite_seconds": 0, "avg_invite_to_accept_seconds": 0,
			"avg_accept_to_start_seconds": 0, "avg_pool_to_start_seconds": 0,
		},
		"rows": []any{},
	}
	if len(orderIDs) == 0 {
		return result, nil
	}
	seats, err := db.EsportsDispatchSeat.Query().Where(esportsdispatchseat.OrderIDIn(orderIDs...)).
		Select(esportsdispatchseat.FieldID, esportsdispatchseat.FieldStatus,
			esportsdispatchseat.FieldInvitedByAdminID,
			esportsdispatchseat.FieldCreatedAt, esportsdispatchseat.FieldRespondedAt).All(ctx)
	if err != nil {
		return nil, err
	}
	index := map[int]int{}
	rows := make([]dispatchEfficiencyRow, 0)
	var invite, accepted, rejected, expired, open int
	var respondedSeconds int64
	var respondedCount int
	for _, seat := range seats {
		switch seat.Status {
		case esportsdispatchseat.StatusAccepted:
			accepted++
		case esportsdispatchseat.StatusRejected:
			rejected++
		case esportsdispatchseat.StatusExpired:
			expired++
		case esportsdispatchseat.StatusOpen:
			open++
		}
		if seat.RespondedAt != nil {
			respondedSeconds += int64(seat.RespondedAt.Sub(seat.CreatedAt).Seconds())
			respondedCount++
		}
		if seat.InvitedByAdminID == nil {
			continue
		}
		invite++
		owner := *seat.InvitedByAdminID
		at, ok := index[owner]
		if !ok {
			index[owner] = len(rows)
			rows = append(rows, dispatchEfficiencyRow{AdminID: owner})
			at = index[owner]
		}
		rows[at].InviteCount++
		switch seat.Status {
		case esportsdispatchseat.StatusAccepted:
			rows[at].AcceptedCount++
		case esportsdispatchseat.StatusRejected:
			rows[at].RejectedCount++
		case esportsdispatchseat.StatusExpired:
			rows[at].ExpiredCount++
		}
	}
	var average int64
	if respondedCount > 0 {
		average = respondedSeconds / int64(respondedCount)
	}
	// §5.9 等待时长：按订单时间线埋点派生四类平均等待时长（只有时间点齐全的订单才计入样本）。
	// 入池 → 派出（客服/系统派单快慢）、派出 → 接单（陪玩响应）、接单 → 开工（开工延迟）、
	// 入池 → 开工（端到端等待）。缺埋点的历史订单自动排除，不影响计数。
	timelineOrders, err := db.EsportsOrder.Query().Where(esportsorder.IDIn(orderIDs...)).
		Select(esportsorder.FieldFirstPooledAt, esportsorder.FieldFirstInvitedAt,
			esportsorder.FieldFirstAcceptedAt, esportsorder.FieldServiceStartedAt).All(ctx)
	if err != nil {
		return nil, err
	}
	var poolToInvite, inviteToAccept, acceptToStart, poolToStart int64
	var poolToInviteN, inviteToAcceptN, acceptToStartN, poolToStartN int
	for _, timelineOrder := range timelineOrders {
		if timelineOrder.FirstPooledAt != nil && timelineOrder.FirstInvitedAt != nil {
			poolToInvite += int64(timelineOrder.FirstInvitedAt.Sub(*timelineOrder.FirstPooledAt).Seconds())
			poolToInviteN++
		}
		if timelineOrder.FirstInvitedAt != nil && timelineOrder.FirstAcceptedAt != nil {
			inviteToAccept += int64(timelineOrder.FirstAcceptedAt.Sub(*timelineOrder.FirstInvitedAt).Seconds())
			inviteToAcceptN++
		}
		if timelineOrder.FirstAcceptedAt != nil && timelineOrder.ServiceStartedAt != nil {
			acceptToStart += int64(timelineOrder.ServiceStartedAt.Sub(*timelineOrder.FirstAcceptedAt).Seconds())
			acceptToStartN++
		}
		if timelineOrder.FirstPooledAt != nil && timelineOrder.ServiceStartedAt != nil {
			poolToStart += int64(timelineOrder.ServiceStartedAt.Sub(*timelineOrder.FirstPooledAt).Seconds())
			poolToStartN++
		}
	}
	avgSeconds := func(total int64, count int) int64 {
		if count == 0 {
			return 0
		}
		return total / int64(count)
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
	result["summary"] = map[string]any{
		"invite_count": invite, "accepted_count": accepted, "rejected_count": rejected,
		"expired_count": expired, "open_count": open, "avg_accept_seconds": average,
		"avg_pool_to_invite_seconds":   avgSeconds(poolToInvite, poolToInviteN),
		"avg_invite_to_accept_seconds": avgSeconds(inviteToAccept, inviteToAcceptN),
		"avg_accept_to_start_seconds":  avgSeconds(acceptToStart, acceptToStartN),
		"avg_pool_to_start_seconds":    avgSeconds(poolToStart, poolToStartN),
	}
	result["rows"] = rows
	return result, nil
}

// aftersaleQualityReport 售后质量：区间内售后单状态分布、赔付金额与按类型 / 责任方拆分。
// handlerAdmin > 0 时只统计该客服处理的售后（非超级管理员的数据边界）。
func aftersaleQualityReport(ctx context.Context, db *ent.Client, f reportFilter, handlerAdmin int) (map[string]any, error) {
	query := db.EsportsAfterSale.Query().Where(
		esportsaftersale.CreatedAtGTE(f.Start),
		esportsaftersale.CreatedAtLT(f.End),
	)
	if handlerAdmin > 0 {
		query.Where(esportsaftersale.HandlerAdminIDEQ(handlerAdmin))
	}
	if f.ChannelSource != "" || f.EntryPoint != "" {
		orders := db.EsportsOrder.Query()
		if f.ChannelSource != "" {
			orders.Where(esportsorder.ChannelSourceEQ(esportsorder.ChannelSource(f.ChannelSource)))
		}
		if f.EntryPoint != "" {
			orders.Where(esportsorder.EntryPointEQ(esportsorder.EntryPoint(f.EntryPoint)))
		}
		ids, err := orders.IDs(ctx)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			query.Where(esportsaftersale.IDLTE(0))
		} else {
			query.Where(esportsaftersale.SourceIDIn(ids...))
		}
	}
	if f.AftersaleKind != "" {
		query.Where(esportsaftersale.KindEQ(esportsaftersale.Kind(f.AftersaleKind)))
	}
	if f.FaultParty != "" {
		query.Where(esportsaftersale.FaultPartyEQ(esportsaftersale.FaultParty(f.FaultParty)))
	}
	rows, err := query.Limit(reportScanLimit+1).
		Select(esportsaftersale.FieldID, esportsaftersale.FieldKind,
			esportsaftersale.FieldStatus, esportsaftersale.FieldFaultParty,
			esportsaftersale.FieldRefundCents, esportsaftersale.FieldCreatedAt).All(ctx)
	if err != nil {
		return nil, err
	}
	if err = reportLimitError(len(rows)); err != nil {
		return nil, err
	}
	var total, completed, rejected, withdrawn, inProgress int
	var refundCents int64
	byKind := map[string]int{}
	byFaultParty := map[string]int{}
	details := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		total++
		refundCents += row.RefundCents
		switch row.Status {
		case esportsaftersale.StatusCompleted:
			completed++
		case esportsaftersale.StatusRejected:
			rejected++
		case esportsaftersale.StatusWithdrawn:
			withdrawn++
		default:
			inProgress++
		}
		byKind[string(row.Kind)]++
		byFaultParty[string(row.FaultParty)]++
		details = append(details, map[string]any{
			"id": row.ID, "kind": row.Kind, "status": row.Status,
			"fault_party": row.FaultParty, "refund_cents": row.RefundCents,
			"created_at": row.CreatedAt,
		})
	}
	return map[string]any{
		"type": "aftersale_quality",
		"summary": map[string]any{
			"total": total, "completed": completed, "rejected": rejected,
			"withdrawn": withdrawn, "in_progress": inProgress, "refund_cents": refundCents,
		},
		"by_kind":        byKind,
		"by_fault_party": byFaultParty,
		"details":        trimDetails(details, f.Limit),
	}, nil
}

// financeReport 财务资金与结算：区间内钱包流水、结算单与按支付方式聚合的支付回执。
func financeReport(ctx context.Context, db *ent.Client, f reportFilter) (map[string]any, error) {
	transactions, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.CreatedAtGTE(f.Start),
		esportswallettx.CreatedAtLT(f.End),
	).Limit(reportScanLimit+1).
		Select(esportswallettx.FieldAccountType, esportswallettx.FieldEntryType,
			esportswallettx.FieldPrincipalDeltaCents).All(ctx)
	if err != nil {
		return nil, err
	}
	if err = reportLimitError(len(transactions)); err != nil {
		return nil, err
	}
	customerPrincipal := map[string]int64{}
	playerPrincipal := map[string]int64{}
	storedValuePrincipal := map[string]int64{}
	for _, tx := range transactions {
		switch tx.AccountType {
		case esportswallettx.AccountTypePlayerCatfood:
			playerPrincipal[string(tx.EntryType)] += tx.PrincipalDeltaCents
		case esportswallettx.AccountTypeStoredValueCard:
			// 储值卡是独立预付账户，单独分桶，不与客户余额混算（§11.3）。
			storedValuePrincipal[string(tx.EntryType)] += tx.PrincipalDeltaCents
		default:
			customerPrincipal[string(tx.EntryType)] += tx.PrincipalDeltaCents
		}
	}
	settlements, err := db.EsportsSettlement.Query().Where(
		esportssettlement.CreatedAtGTE(f.Start),
		esportssettlement.CreatedAtLT(f.End),
	).Limit(reportScanLimit+1).
		Select(esportssettlement.FieldGrossCents, esportssettlement.FieldNetCents,
			esportssettlement.FieldStatus).All(ctx)
	if err != nil {
		return nil, err
	}
	if err = reportLimitError(len(settlements)); err != nil {
		return nil, err
	}
	var settlementCount int
	var settlementGross, settlementNet int64
	settlementStatus := map[string]int{}
	for _, item := range settlements {
		settlementCount++
		settlementGross += item.GrossCents
		settlementNet += item.NetCents
		settlementStatus[string(item.Status)]++
	}
	receiptQuery := db.EsportsPaymentReceipt.Query().Where(
		esportspaymentreceipt.CreatedAtGTE(f.Start),
		esportspaymentreceipt.CreatedAtLT(f.End),
	)
	if f.PaymentChannel != "" {
		receiptQuery.Where(esportspaymentreceipt.ChannelEQ(f.PaymentChannel))
	}
	receipts, err := receiptQuery.Limit(reportScanLimit+1).
		Select(esportspaymentreceipt.FieldChannel, esportspaymentreceipt.FieldActualCents).All(ctx)
	if err != nil {
		return nil, err
	}
	if err = reportLimitError(len(receipts)); err != nil {
		return nil, err
	}
	channelIndex := map[string]int{}
	channels := make([]financeChannelRow, 0)
	for _, receipt := range receipts {
		at, ok := channelIndex[receipt.Channel]
		if !ok {
			channelIndex[receipt.Channel] = len(channels)
			channels = append(channels, financeChannelRow{Channel: receipt.Channel})
			at = channelIndex[receipt.Channel]
		}
		channels[at].AmountCents += receipt.ActualCents
		channels[at].Count++
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].AmountCents > channels[j].AmountCents })
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

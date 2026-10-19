package esports

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/predicate"
)

// §17 报表导出：复用 report.go 的筛选与聚合口径，把「聚合汇总 + 下钻明细」序列化为 CSV。
// 汇总与分段走数据库端聚合（不随订单行数线性占用内存）；下钻明细按键集分页流式写出，
// 大样本下 CSV 分块落盘，不一次性物化全量单据。
const reportExportPageSize = 500

// exportReportCSV 把 reportType 报表导出为 CSV 写入 w，返回导出的数据行数与错误。
// handlerAdmin > 0 时仅统计该客服处理的售后（与报表接口同一数据边界）。
func exportReportCSV(ctx context.Context, db *ent.Client, reportType string, f reportFilter, handlerAdmin int, w io.Writer) (int, error) {
	csvw := csv.NewWriter(w)
	rowCount, err := writeReportCSV(ctx, db, reportType, f, handlerAdmin, csvw)
	if err != nil {
		return 0, err
	}
	csvw.Flush()
	if err := csvw.Error(); err != nil {
		return 0, err
	}
	return rowCount, nil
}

func writeReportCSV(ctx context.Context, db *ent.Client, reportType string, f reportFilter, handlerAdmin int, w *csv.Writer) (int, error) {
	switch reportType {
	case "owner_overview":
		return writeOwnerOverviewCSV(ctx, db, f, w)
	case "cs_performance":
		return writeCsPerformanceCSV(ctx, db, f, w)
	case "dispatch_efficiency":
		return writeDispatchEfficiencyCSV(ctx, db, f, w)
	case "aftersale_quality":
		return writeAftersaleQualityCSV(ctx, db, f, handlerAdmin, w)
	case "finance":
		return writeFinanceCSV(ctx, db, f, w)
	default:
		return 0, errInvalidInput
	}
}

// writeOwnerOverviewCSV 导出经营总览：汇总 + 时间轴分段 + 订单下钻明细。
func writeOwnerOverviewCSV(ctx context.Context, db *ent.Client, f reportFilter, w *csv.Writer) (int, error) {
	// 汇总与分段只取聚合口径，明细走流式导出，避免重复物化上限内的明细。
	agg := f
	agg.Limit = 1
	result, err := ownerOverviewReport(ctx, db, agg)
	if err != nil {
		return 0, err
	}
	summary := result["summary"].(map[string]any)
	for _, key := range []string{"order_count", "paid_count", "revenue_cents", "refund_cents", "net_cents", "completed_count", "cancelled_count"} {
		if err := writeCSV(w, "summary", key, summary[key]); err != nil {
			return 0, err
		}
	}
	if err := writeCSV(w, "bucket", "bucket", "order_count", "revenue_cents", "refund_cents"); err != nil {
		return 0, err
	}
	for _, bucket := range result["buckets"].([]reportBucket) {
		if err := writeCSV(w, "bucket", bucket.Bucket, bucket.OrderCount, bucket.RevenueCents, bucket.RefundCents); err != nil {
			return 0, err
		}
	}
	predicates, err := reportOrderPredicates(ctx, db, f)
	if err != nil {
		return 0, err
	}
	if err := writeCSV(w, "order", "id", "order_no", "customer_id", "payable_cents", "refunded_cents", "payment_status", "fulfillment_status", "created_at"); err != nil {
		return 0, err
	}
	return streamOrdersCSV(ctx, db, predicates, w)
}

// streamOrdersCSV 按键集分页流式导出订单明细。
func streamOrdersCSV(ctx context.Context, db *ent.Client, predicates []predicate.EsportsOrder, w *csv.Writer) (int, error) {
	count := 0
	cursor := 0
	for {
		ps := make([]predicate.EsportsOrder, 0, len(predicates)+1)
		ps = append(ps, predicates...)
		ps = append(ps, esportsorder.IDGT(cursor))
		orders, err := db.EsportsOrder.Query().Where(ps...).
			Order(ent.Asc(esportsorder.FieldID)).Limit(reportExportPageSize).
			Select(esportsorder.FieldID, esportsorder.FieldOrderNo, esportsorder.FieldCustomerID,
				esportsorder.FieldPayableCents, esportsorder.FieldRefundedCents,
				esportsorder.FieldPaymentStatus, esportsorder.FieldFulfillmentStatus,
				esportsorder.FieldCreatedAt).All(ctx)
		if err != nil {
			return count, err
		}
		if len(orders) == 0 {
			return count, nil
		}
		for _, order := range orders {
			if err := writeCSV(w, "order", order.ID, order.OrderNo, order.CustomerID,
				order.PayableCents, order.RefundedCents, string(order.PaymentStatus),
				string(order.FulfillmentStatus), order.CreatedAt); err != nil {
				return count, err
			}
			count++
		}
		cursor = orders[len(orders)-1].ID
		if len(orders) < reportExportPageSize {
			return count, nil
		}
	}
}

// writeCsPerformanceCSV 导出客服个人业绩（按负责客服聚合）+ 订单下钻明细（§17-06）。
func writeCsPerformanceCSV(ctx context.Context, db *ent.Client, f reportFilter, w *csv.Writer) (int, error) {
	result, err := csPerformanceReport(ctx, db, f)
	if err != nil {
		return 0, err
	}
	if err := writeCSV(w, "cs", "admin_id", "admin_name", "order_count", "revenue_cents", "refund_cents", "net_cents", "commission_cents"); err != nil {
		return 0, err
	}
	rows := result["rows"].([]csPerformanceRow)
	for _, row := range rows {
		if err := writeCSV(w, "cs", row.AdminID, row.AdminName, row.OrderCount, row.RevenueCents, row.RefundCents, row.NetCents, row.CommissionCents); err != nil {
			return 0, err
		}
	}
	details, _ := result["details"].([]map[string]any)
	if err := writeCSV(w, "cs_order", "id", "order_no", "responsible_admin_id",
		"payable_cents", "refunded_cents", "created_at"); err != nil {
		return 0, err
	}
	for _, detail := range details {
		if err := writeCSV(w, "cs_order", detail["id"], detail["order_no"],
			detail["responsible_admin_id"], detail["payable_cents"],
			detail["refunded_cents"], detail["created_at"]); err != nil {
			return 0, err
		}
	}
	return len(rows) + len(details), nil
}

// writeDispatchEfficiencyCSV 导出派单效率：汇总 + 按邀请管理员聚合。
func writeDispatchEfficiencyCSV(ctx context.Context, db *ent.Client, f reportFilter, w *csv.Writer) (int, error) {
	result, err := dispatchEfficiencyReport(ctx, db, f)
	if err != nil {
		return 0, err
	}
	summary := result["summary"].(map[string]any)
	for _, key := range []string{"invite_count", "accepted_count", "rejected_count", "expired_count", "open_count",
		"avg_accept_seconds", "avg_pool_to_invite_seconds", "avg_invite_to_accept_seconds", "avg_accept_to_start_seconds", "avg_pool_to_start_seconds"} {
		if err := writeCSV(w, "summary", key, summary[key]); err != nil {
			return 0, err
		}
	}
	if err := writeCSV(w, "dispatch", "admin_id", "admin_name", "invite_count", "accepted_count", "rejected_count", "expired_count"); err != nil {
		return 0, err
	}
	rows := result["rows"].([]dispatchEfficiencyRow)
	for _, row := range rows {
		if err := writeCSV(w, "dispatch", row.AdminID, row.AdminName, row.InviteCount, row.AcceptedCount, row.RejectedCount, row.ExpiredCount); err != nil {
			return 0, err
		}
	}
	// §17-06：派单效率的订单下钻明细，可定位原单据。
	details, _ := result["details"].([]map[string]any)
	if err := writeCSV(w, "dispatch_order", "id", "order_no", "responsible_admin_id", "created_at"); err != nil {
		return 0, err
	}
	for _, detail := range details {
		if err := writeCSV(w, "dispatch_order", detail["id"], detail["order_no"],
			detail["responsible_admin_id"], detail["created_at"]); err != nil {
			return 0, err
		}
	}
	return len(rows) + len(details), nil
}

// writeAftersaleQualityCSV 导出售后质量：汇总 + 类型/责任方拆分 + 售后下钻明细。
func writeAftersaleQualityCSV(ctx context.Context, db *ent.Client, f reportFilter, handlerAdmin int, w *csv.Writer) (int, error) {
	agg := f
	agg.Limit = 1
	result, err := aftersaleQualityReport(ctx, db, agg, handlerAdmin)
	if err != nil {
		return 0, err
	}
	summary := result["summary"].(map[string]any)
	for _, key := range []string{"total", "completed", "rejected", "withdrawn", "in_progress", "refund_cents"} {
		if err := writeCSV(w, "summary", key, summary[key]); err != nil {
			return 0, err
		}
	}
	for _, key := range sortedIntKeys(result["by_kind"].(map[string]int)) {
		if err := writeCSV(w, "summary", "by_kind."+key, result["by_kind"].(map[string]int)[key]); err != nil {
			return 0, err
		}
	}
	for _, key := range sortedIntKeys(result["by_fault_party"].(map[string]int)) {
		if err := writeCSV(w, "summary", "by_fault_party."+key, result["by_fault_party"].(map[string]int)[key]); err != nil {
			return 0, err
		}
	}
	if err := writeCSV(w, "aftersale", "id", "kind", "status", "fault_party", "refund_cents", "source_type", "source_id", "order_no", "created_at"); err != nil {
		return 0, err
	}
	return streamAftersaleCSV(ctx, db, f, handlerAdmin, w)
}

// streamAftersaleCSV 流式导出售后明细：无订单维度筛选时单作用域按键集分页；
// 有渠道/入口筛选时按命中的订单主键分片归集，每个分片内售后按主键分页。
func streamAftersaleCSV(ctx context.Context, db *ent.Client, f reportFilter, handlerAdmin int, w *csv.Writer) (int, error) {
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
	sourcePredicates := make([]predicate.EsportsOrder, 0, 2)
	if f.ChannelSource != "" {
		sourcePredicates = append(sourcePredicates, esportsorder.ChannelSourceEQ(esportsorder.ChannelSource(f.ChannelSource)))
	}
	if f.EntryPoint != "" {
		sourcePredicates = append(sourcePredicates, esportsorder.EntryPointEQ(esportsorder.EntryPoint(f.EntryPoint)))
	}
	count := 0
	stream := func(scope func(*ent.EsportsAfterSaleQuery)) error {
		cursor := 0
		for {
			query := db.EsportsAfterSale.Query().Where(base...)
			if scope != nil {
				scope(query)
			}
			query.Where(esportsaftersale.IDGT(cursor))
			rows, err := query.Order(ent.Asc(esportsaftersale.FieldID)).Limit(reportExportPageSize).
				Select(esportsaftersale.FieldID, esportsaftersale.FieldKind, esportsaftersale.FieldStatus,
					esportsaftersale.FieldFaultParty, esportsaftersale.FieldRefundCents,
					esportsaftersale.FieldSourceType, esportsaftersale.FieldSourceID,
					esportsaftersale.FieldCreatedAt).All(ctx)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			// §17-06：来源为订单的售后补出来源订单号，便于跳回原单据核对。
			sourceOrderIDs := make([]int, 0, len(rows))
			for _, row := range rows {
				if row.SourceType == esportsaftersale.SourceTypeOrder {
					sourceOrderIDs = append(sourceOrderIDs, row.SourceID)
				}
			}
			orderNos, err := orderNoByID(ctx, db, sourceOrderIDs)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if err := writeCSV(w, "aftersale", row.ID, string(row.Kind), string(row.Status),
					string(row.FaultParty), row.RefundCents, string(row.SourceType), row.SourceID,
					orderNos[row.SourceID], row.CreatedAt); err != nil {
					return err
				}
				count++
			}
			cursor = rows[len(rows)-1].ID
			if len(rows) < reportExportPageSize {
				return nil
			}
		}
	}
	if len(sourcePredicates) == 0 {
		return count, stream(nil)
	}
	err := forEachOrderIDPage(ctx, db, sourcePredicates, func(ids []int) error {
		return stream(func(query *ent.EsportsAfterSaleQuery) {
			// 与售后质量报表口径一致：按订单渠道 / 入口圈定时必须限定 source_type=order，
			// 避免礼物 / 储值卡 / 充值售后的 ID 与订单 ID 撞号。
			query.Where(
				esportsaftersale.SourceTypeEQ(esportsaftersale.SourceTypeOrder),
				esportsaftersale.SourceIDIn(ids...),
			)
		})
	})
	return count, err
}

// writeFinanceCSV 导出财务资金：钱包资金、结算单与支付方式回执的聚合汇总。
func writeFinanceCSV(ctx context.Context, db *ent.Client, f reportFilter, w *csv.Writer) (int, error) {
	result, err := financeReport(ctx, db, f)
	if err != nil {
		return 0, err
	}
	wallet := result["wallet"].(map[string]any)
	for _, account := range []string{"customer_balance", "player_catfood", "stored_value_card"} {
		entries := wallet[account].(map[string]int64)
		for _, key := range sortedInt64Keys(entries) {
			if err := writeCSV(w, "summary", "wallet."+account+"."+key, entries[key]); err != nil {
				return 0, err
			}
		}
	}
	settlement := result["settlement"].(map[string]any)
	for _, key := range []string{"count", "gross_cents", "net_cents"} {
		if err := writeCSV(w, "summary", "settlement."+key, settlement[key]); err != nil {
			return 0, err
		}
	}
	byStatus := settlement["by_status"].(map[string]int)
	for _, key := range sortedIntKeys(byStatus) {
		if err := writeCSV(w, "summary", "settlement.by_status."+key, byStatus[key]); err != nil {
			return 0, err
		}
	}
	if err := writeCSV(w, "channel", "channel", "amount_cents", "count"); err != nil {
		return 0, err
	}
	channels := result["payment_channels"].([]financeChannelRow)
	for _, channel := range channels {
		if err := writeCSV(w, "channel", channel.Channel, channel.AmountCents, channel.Count); err != nil {
			return 0, err
		}
	}
	// §12.7-03：分成明细级对账逐笔明细（含规则版本），财务可与结算单逐笔核对。
	reconciliation, _ := result["commission_reconciliation"].(map[string]any)
	details, _ := reconciliation["details"].([]map[string]any)
	if err := writeCSV(w, "commission_reconciliation", "id", "order_no", "player_id", "kind",
		"rule_no", "rule_version", "base_cents", "amount_cents"); err != nil {
		return 0, err
	}
	for _, detail := range details {
		if err := writeCSV(w, "commission_reconciliation", detail["id"], detail["order_no"],
			detail["player_id"], detail["kind"], detail["rule_no"], detail["rule_version"],
			detail["base_cents"], detail["amount_cents"]); err != nil {
			return 0, err
		}
	}
	// §12.7-04：工作室抽成汇总（按周期聚合工作室剩余）随财务导出。
	studio, _ := result["studio"].(map[string]any)
	studioSummary, _ := studio["summary"].(map[string]any)
	for _, key := range []string{"studio_remainder_cents", "cs_commission_cents", "entry_count"} {
		if err := writeCSV(w, "summary", "studio."+key, studioSummary[key]); err != nil {
			return 0, err
		}
	}
	if err := writeCSV(w, "studio_bucket", "bucket", "studio_remainder_cents", "cs_commission_cents", "entry_count"); err != nil {
		return 0, err
	}
	for _, bucket := range studio["buckets"].([]studioRemainderBucket) {
		if err := writeCSV(w, "studio_bucket", bucket.Bucket, bucket.StudioRemainderCents,
			bucket.CsCommissionCents, bucket.EntryCount); err != nil {
			return 0, err
		}
	}
	// §17-06：财务下钻单据明细（结算单 / 支付回执）。
	docs, _ := result["details"].([]map[string]any)
	if err := writeCSV(w, "finance_doc", "doc_type", "id", "doc_no", "amount_cents", "status", "created_at"); err != nil {
		return 0, err
	}
	for _, doc := range docs {
		docNo := doc["settlement_no"]
		if docNo == nil || docNo == "" {
			docNo = doc["tx_no"]
		}
		amount := doc["net_cents"]
		if amount == nil {
			amount = doc["amount_cents"]
		}
		if err := writeCSV(w, "finance_doc", doc["doc_type"], doc["id"], docNo, amount,
			doc["status"], doc["created_at"]); err != nil {
			return 0, err
		}
	}
	return len(channels) + len(details) + len(docs), nil
}

// writeCSV 把任意类型字段序列化为一行 CSV 记录。
func writeCSV(w *csv.Writer, fields ...any) error {
	record := make([]string, len(fields))
	for i, field := range fields {
		record[i] = csvField(field)
	}
	return w.Write(record)
}

func csvField(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	case time.Time:
		return v.UTC().Format(time.RFC3339)
	case *int:
		// 可空外键（结算单 / 归属客服 / 结算单号）导出为整数或空，避免打印指针地址。
		if v == nil {
			return ""
		}
		return strconv.Itoa(*v)
	default:
		return fmt.Sprint(v)
	}
}

func sortedIntKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedInt64Keys(values map[string]int64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// reportExportFileName 生成确定性导出文件名（带报表类型与业务时区时间范围）。
func reportExportFileName(reportType string, start, end time.Time) string {
	return "esports_" + reportType + "_" + start.In(cstZone).Format("20060102") + "_" + end.In(cstZone).Format("20060102") + ".csv"
}

// reportExportSnapshot 构造审计用的筛选参数快照。
func reportExportSnapshot(f reportFilter, handlerAdmin int) map[string]any {
	return map[string]any{
		"start":                f.Start,
		"end":                  f.End,
		"period":               f.Period,
		"responsible_admin_id": f.ResponsibleAdmin,
		"created_by_admin_id":  f.CreatedByAdmin,
		"sku_id":               f.SkuID,
		"game":                 f.Game,
		"channel_source":       f.ChannelSource,
		"entry_point":          f.EntryPoint,
		"appointment_type":     f.AppointmentType,
		"seat_type":            f.SeatType,
		"payment_channel":      f.PaymentChannel,
		"kind":                 f.AftersaleKind,
		"fault_party":          f.FaultParty,
		"limit":                f.Limit,
		"handler_admin_id":     handlerAdmin,
	}
}

// recordReportExport 落一条导出审计记录（只增）。
func recordReportExport(ctx context.Context, db *ent.Client, adminID int, adminName, reportType string,
	filter reportFilter, handlerAdmin, rowCount int, fileName, checksum string,
) (*ent.EsportsReportExport, error) {
	return db.EsportsReportExport.Create().
		SetReportType(reportType).
		SetFormat("csv").
		SetAdminID(adminID).
		SetAdminName(adminName).
		SetFilterSnapshot(reportExportSnapshot(filter, handlerAdmin)).
		SetRowCount(rowCount).
		SetFileName(fileName).
		SetChecksum(checksum).
		Save(ctx)
}

// fileSHA256 返回文件内容的 SHA-256 十六进制摘要。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// adminDisplayName 返回管理员展示名（昵称优先，回落用户名）。
func adminDisplayName(admin *ent.Admin) string {
	if admin == nil {
		return ""
	}
	if admin.Nickname != "" {
		return admin.Nickname
	}
	return admin.Username
}

// validReportType 校验报表类型是否可导出。
func validReportType(reportType string) bool {
	switch strings.TrimSpace(reportType) {
	case "owner_overview", "cs_performance", "dispatch_efficiency", "aftersale_quality", "finance":
		return true
	default:
		return false
	}
}

package esports

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsreportexport"
	"go-admin/internal/ent/migrate"
)

func parseExportCSV(t *testing.T, raw string) [][]string {
	t.Helper()
	reader := csv.NewReader(strings.NewReader(raw))
	reader.FieldsPerRecord = -1 // 汇总与明细列宽不同
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("导出 CSV 无法解析：%v", err)
	}
	return records
}

// 导出 CSV 必须同时包含聚合汇总与下钻明细，且受渠道筛选约束。
func TestReportExportCSVContent(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_export_content?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	seedReportOrder(t, db, "source-manual", day.Add(time.Hour), 11, 1000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	wecom := seedReportOrder(t, db, "source-wecom", day.Add(2*time.Hour), 11, 2000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	db.EsportsOrder.UpdateOneID(wecom.ID).SetChannelSource(esportsorder.ChannelSourceWecom).
		SetEntryPoint(esportsorder.EntryPointWecomSidebar).SaveX(ctx)

	f := reportFilter{Start: day, End: day.AddDate(0, 0, 1), ChannelSource: "wecom", EntryPoint: "wecom_sidebar"}
	var buf bytes.Buffer
	rowCount, err := exportReportCSV(ctx, db, "owner_overview", f, 0, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("导出行数不正确：%d", rowCount)
	}
	records := parseExportCSV(t, buf.String())
	var orderCount, revenue int64
	var orderRows int
	for _, record := range records {
		switch {
		case len(record) >= 3 && record[0] == "summary" && record[1] == "order_count":
			orderCount, _ = strconv.ParseInt(record[2], 10, 64)
		case len(record) >= 3 && record[0] == "summary" && record[1] == "revenue_cents":
			revenue, _ = strconv.ParseInt(record[2], 10, 64)
		case len(record) >= 3 && record[0] == "order" && record[1] != "id":
			orderRows++
			if record[2] != "source-wecom" {
				t.Fatalf("渠道筛选未作用到导出明细：%+v", record)
			}
		}
	}
	if orderCount != 1 || revenue != 2000 || orderRows != 1 {
		t.Fatalf("导出 CSV 汇总或明细不正确：order_count=%d revenue=%d orderRows=%d", orderCount, revenue, orderRows)
	}
}

// 非超级管理员导出时同样收敛本人，不得通过筛选参数读取他人订单。
func TestReportExportCSVScopesToAdmin(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_export_scope?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	seedReportOrder(t, db, "scope-11", day.Add(time.Hour), 11, 1000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	seedReportOrder(t, db, "scope-12", day.Add(2*time.Hour), 12, 2000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)

	scoped := scopeReportFilter(reportFilter{Start: day, End: day.AddDate(0, 0, 1)}, 11, false)
	var buf bytes.Buffer
	rowCount, err := exportReportCSV(ctx, db, "owner_overview", scoped, 0, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("非超级管理员导出不得包含他人订单：rowCount=%d", rowCount)
	}
	for _, record := range parseExportCSV(t, buf.String()) {
		if len(record) >= 3 && record[0] == "order" && record[1] != "id" && record[2] != "scope-11" {
			t.Fatalf("导出明细泄露他人订单：%+v", record)
		}
	}
}

// 导出落一条只增审计记录，且可按报表类型 / 导出人查询导出历史。
func TestReportExportAuditRecord(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_export_audit?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	rec, err := recordReportExport(ctx, db, 11, "客服甲", "owner_overview",
		reportFilter{Start: day, End: day.AddDate(0, 0, 1), ChannelSource: "wecom"}, 0, 7,
		"esports_owner_overview_20261011_20261012.csv", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID == 0 {
		t.Fatal("审计记录未生成")
	}

	// 历史可查：按报表类型 + 导出人过滤。
	history, err := db.EsportsReportExport.Query().
		Where(esportsreportexport.ReportTypeEQ("owner_overview"), esportsreportexport.AdminIDEQ(11)).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].RowCount != 7 || history[0].Checksum != "abc123" ||
		history[0].FileName != "esports_owner_overview_20261011_20261012.csv" {
		t.Fatalf("导出历史不正确：%+v", history)
	}
	if len(history[0].FilterSnapshot) == 0 {
		t.Fatal("筛选参数快照未落库")
	}

	// 非超管边界：他人不可见。
	if others, err := db.EsportsReportExport.Query().Where(esportsreportexport.AdminIDEQ(99)).All(ctx); err != nil || len(others) != 0 {
		t.Fatalf("导出历史越权：%+v err=%v", others, err)
	}
}

// finance 报表导出仅超级管理员可用，且仍拒绝渠道 / 入口筛选。
func TestReportExportFinanceForbidden(t *testing.T) {
	if err := validateReportExportAccess("finance", reportFilter{}, false); !errors.Is(err, errForbidden) {
		t.Fatalf("finance 非超管导出应 403：%v", err)
	}
	if err := validateReportExportAccess("finance", reportFilter{ChannelSource: "wecom"}, true); !errors.Is(err, errInvalidInput) {
		t.Fatalf("finance 渠道筛选应被拒绝：%v", err)
	}
	if err := validateReportExportAccess("finance", reportFilter{}, true); err != nil {
		t.Fatalf("超管 finance 导出应放行：%v", err)
	}
	if err := validateReportExportAccess("owner_overview", reportFilter{}, false); err != nil {
		t.Fatalf("非超管普通报表导出应放行：%v", err)
	}
}

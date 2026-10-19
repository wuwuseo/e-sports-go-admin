package esports

import (
	"bytes"
	"fmt"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/migrate"
)

// 第二轮补充：售后质量导出的「处理客服」过滤必须与页面一致
// （此前 streamAftersaleCSV 自己拼谓词，超管按 responsible_admin_id 导出时汇总筛了、明细没筛）。
func TestReportAftersaleExportMatchesPageFilter(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_export_aftersale?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	// 客服 11 处理 2 条、客服 12 处理 1 条。
	for i, handler := range []int{11, 11, 12} {
		db.EsportsAfterSale.Create().SetAfterSaleNo("export-as-" + strconv.Itoa(i)).
			SetSourceID(i + 1).SetKind(esportsaftersale.KindRefundRequest).
			SetChannel(esportsaftersale.ChannelCustomer).SetDemand("测试").
			SetHandlerAdminID(handler).
			SetCreatedAt(day.Add(time.Duration(i+1) * time.Hour)).SaveX(ctx)
	}
	filter := reportFilter{Start: day, End: day.AddDate(0, 0, 1), ResponsibleAdmin: 11}

	page, err := aftersaleQualityReport(ctx, db, filter, 0)
	if err != nil {
		t.Fatal(err)
	}
	pageTotal := page["summary"].(map[string]any)["total"].(int)
	if pageTotal != 2 {
		t.Fatalf("页面按处理客服筛选结果不正确：total=%d", pageTotal)
	}

	var buf bytes.Buffer
	rowCount, err := exportReportCSV(ctx, db, "aftersale_quality", filter, 0, &buf)
	if err != nil {
		t.Fatal(err)
	}
	detailRows := 0
	for _, record := range parseExportCSV(t, buf.String()) {
		if len(record) >= 2 && record[0] == "aftersale" && record[1] != "id" {
			detailRows++
		}
	}
	if detailRows != pageTotal {
		t.Fatalf("导出明细与页面结果不一致：导出 %d 行、页面 %d 条（rowCount=%d）",
			detailRows, pageTotal, rowCount)
	}

	// 非超管：页面（handlerAdmin 收敛）与导出必须一致，且都只含本人的售后。
	scoped := scopeReportFilter(reportFilter{Start: day, End: day.AddDate(0, 0, 1)}, 12, false)
	page, err = aftersaleQualityReport(ctx, db, scoped, 12)
	if err != nil {
		t.Fatal(err)
	}
	if got := page["summary"].(map[string]any)["total"].(int); got != 1 {
		t.Fatalf("非超管页面结果不正确：total=%d", got)
	}
	buf.Reset()
	if _, err := exportReportCSV(ctx, db, "aftersale_quality", scoped, 12, &buf); err != nil {
		t.Fatal(err)
	}
	detailRows = 0
	for _, record := range parseExportCSV(t, buf.String()) {
		if len(record) >= 2 && record[0] == "aftersale" && record[1] != "id" {
			detailRows++
		}
	}
	if detailRows != 1 {
		t.Fatalf("非超管导出未收敛到本人处理：明细 %d 行", detailRows)
	}
}

// 第二轮补充：导出审计快照必须记录全部生效维度与数据域，事后可从审计记录还原口径。
func TestReportExportSnapshotRecordsDimensionsAndScope(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_export_snapshot?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	rec, err := recordReportExport(ctx, db, 11, "财务甲", "finance",
		reportFilter{
			Start: day, End: day.AddDate(0, 0, 1),
			WalletAccountType: "player_catfood",
			WalletEntryType:   "recharge",
			SettlementStatus:  "paid",
		}, 0, 3, "esports_finance_20261011_20261012.csv", "sum")
	if err != nil {
		t.Fatal(err)
	}
	// 从库里读回（JSON 字段经序列化往返，数字会变成 float64，统一用 fmt.Sprint 比较）。
	stored, err := db.EsportsReportExport.Get(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := stored.FilterSnapshot
	if snapshot["account_type"] != "player_catfood" || snapshot["entry_type"] != "recharge" ||
		snapshot["settlement_status"] != "paid" {
		t.Fatalf("导出快照缺少资金维度：%+v", snapshot)
	}
	scope, ok := snapshot["data_scope"].(map[string]any)
	if !ok {
		t.Fatalf("导出快照缺少 data_scope：%+v", snapshot)
	}
	if fmt.Sprint(scope["scope"]) != financeReportDataScope || fmt.Sprint(scope["full"]) != "true" {
		t.Fatalf("财务导出数据域标记不正确：%+v", scope)
	}

	// 非超管普通报表：数据域为 owner，admin_id 记录被收敛的管理员。
	scoped := scopeReportFilter(reportFilter{
		Start: day, End: day.AddDate(0, 0, 1), ChannelSource: "wecom",
	}, 22, false)
	rec, err = recordReportExport(ctx, db, 22, "客服乙", "owner_overview", scoped, 0, 1, "x.csv", "sum2")
	if err != nil {
		t.Fatal(err)
	}
	stored, err = db.EsportsReportExport.Get(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope, ok = stored.FilterSnapshot["data_scope"].(map[string]any)
	if !ok {
		t.Fatalf("导出快照缺少 data_scope：%+v", stored.FilterSnapshot)
	}
	if fmt.Sprint(scope["scope"]) != "owner" || fmt.Sprint(scope["full"]) != "false" ||
		fmt.Sprint(scope["admin_id"]) != "22" {
		t.Fatalf("非超管导出数据域标记不正确：%+v", scope)
	}
}

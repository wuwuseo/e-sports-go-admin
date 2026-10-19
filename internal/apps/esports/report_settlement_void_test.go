package esports

import (
	"fmt"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/ent/migrate"
)

// 第二轮补充：结算聚合必须排除作废单（void）——作废是「明细回退、不可再发放」的终态，
// 金额不得计入 gross / net，只能单列供对账；下钻明细同样不得出现作废单。
func TestReportFinanceExcludesVoidSettlements(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_settlement_void?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	for i, spec := range []struct {
		no     string
		status esportssettlement.Status
		amount int64
	}{
		{"void-st-locked", esportssettlement.StatusLocked, 3000},
		{"void-st-paid", esportssettlement.StatusPaid, 2000},
		{"void-st-void", esportssettlement.StatusVoid, 9000},
	} {
		db.EsportsSettlement.Create().SetSettlementNo(spec.no).SetOwnerID(2).
			SetPeriodStart(day).SetPeriodEnd(day.AddDate(0, 0, 1)).
			SetGrossCents(spec.amount).SetNetCents(spec.amount).SetEntryCount(1).
			SetStatus(spec.status).
			SetCreatedAt(day.Add(time.Duration(i+1) * time.Hour)).SaveX(ctx)
	}

	result, err := financeReport(ctx, db, reportFilter{Start: day, End: day.AddDate(0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	settlement := result["settlement"].(map[string]any)
	if settlement["count"].(int) != 2 {
		t.Fatalf("作废结算单不应计入结算单数：%+v", settlement)
	}
	if settlement["gross_cents"].(int64) != 5000 || settlement["net_cents"].(int64) != 5000 {
		t.Fatalf("作废结算单金额混入汇总：%+v", settlement)
	}
	if status := settlement["by_status"].(map[string]int); status[string(esportssettlement.StatusVoid)] != 0 {
		t.Fatalf("状态分布不应含作废单：%+v", status)
	}
	void := settlement["void"].(map[string]any)
	if void["count"].(int) != 1 || void["gross_cents"].(int64) != 9000 || void["net_cents"].(int64) != 9000 {
		t.Fatalf("作废结算单未单列：%+v", void)
	}

	// 下钻明细与汇总同口径：不出现作废单。
	for _, doc := range result["details"].([]map[string]any) {
		if doc["doc_type"] == "settlement" && fmt.Sprint(doc["status"]) == string(esportssettlement.StatusVoid) {
			t.Fatalf("下钻明细不得含作废结算单：%+v", doc)
		}
	}

	// 显式按结算状态筛选时仍排除作废：选 void 时主口径为空，作废数据只从 void 单列取。
	byVoid, err := financeReport(ctx, db, reportFilter{
		Start: day, End: day.AddDate(0, 0, 1), SettlementStatus: string(esportssettlement.StatusVoid),
	})
	if err != nil {
		t.Fatal(err)
	}
	settlement = byVoid["settlement"].(map[string]any)
	if settlement["count"].(int) != 0 || settlement["gross_cents"].(int64) != 0 {
		t.Fatalf("按作废筛选时金额口径仍应为空（作废不计入 gross/net）：%+v", settlement)
	}
	if settlement["void"].(map[string]any)["count"].(int) != 1 {
		t.Fatalf("按作废筛选时 void 单列应可查：%+v", settlement["void"])
	}
}

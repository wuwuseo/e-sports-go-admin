package esports

import (
	"net/http"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"
)

// P1-09 售后质量的「负责 / 处理客服」参数必须真正生效（此前接受后完全忽略）。
// 非超管仍由 handlerAdmin 强制收敛，超管按 responsible_admin_id 下推。
func TestReportAftersaleQualityFiltersHandlerAdmin(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_aftersale_handler?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	for i, handler := range []int{11, 12} {
		db.EsportsAfterSale.Create().SetAfterSaleNo("handler-as-" + string(rune('a'+i))).
			SetSourceID(i + 1).SetKind(esportsaftersale.KindRefundRequest).
			SetChannel(esportsaftersale.ChannelCustomer).SetDemand("测试").
			SetHandlerAdminID(handler).
			SetCreatedAt(day.Add(time.Duration(i+1) * time.Hour)).SaveX(ctx)
	}

	// 未筛选：两条都在。
	all, err := aftersaleQualityReport(ctx, db, reportFilter{Start: day, End: day.AddDate(0, 0, 1)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := all["summary"].(map[string]any)["total"].(int); got != 2 {
		t.Fatalf("未筛选时应统计全部售后：total=%d", got)
	}

	// 超管按 responsible_admin_id=11 筛选：只剩 11 处理的那条。
	scoped, err := aftersaleQualityReport(ctx, db, reportFilter{
		Start: day, End: day.AddDate(0, 0, 1), ResponsibleAdmin: 11,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := scoped["summary"].(map[string]any)["total"].(int); got != 1 {
		t.Fatalf("处理客服筛选未下推：total=%d", got)
	}

	// 非超管：handlerAdmin 是权限边界，与 scopeReportFilter 写入的值一致。
	nonSuper, err := aftersaleQualityReport(ctx, db, reportFilter{
		Start: day, End: day.AddDate(0, 0, 1), ResponsibleAdmin: 12, VisibleAdmin: 12,
	}, 12)
	if err != nil {
		t.Fatal(err)
	}
	if got := nonSuper["summary"].(map[string]any)["total"].(int); got != 1 {
		t.Fatalf("非超管售后必须收敛到本人处理：total=%d", got)
	}
}

// P1-09 任何未实现的筛选参数不得静默接受：
// 派单效率的需求筛选项里没有「业绩归属客服」（只列负责客服），必须显式 400；
// 同时按需求的「负责客服」维度仍须可用。
func TestReportDispatchEfficiencyRejectsPerformanceAdminDimension(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_dispatch_dims?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	admin := seedAdminAccount(t, db, "report-dispatch-dims")
	app := reportTestApp(admin, true)

	status, _, payload := getJSON(t, app, "/admin/reports/dispatch_efficiency?created_by_admin_id=11")
	if status != http.StatusBadRequest {
		t.Fatalf("派单效率带业绩归属客服维度应返回 400，实际 %d：%+v", status, payload)
	}
	message, _ := payload["message"].(string)
	if !strings.Contains(message, "派单效率报表") || !strings.Contains(message, "业绩归属") {
		t.Fatalf("派单效率拒绝提示不明确：%q", message)
	}

	// 需求列明的「负责客服」维度保持可用。
	status, _, payload = getJSON(t, app, "/admin/reports/dispatch_efficiency?responsible_admin_id=11")
	if status != http.StatusOK {
		t.Fatalf("派单效率按负责客服筛选应成功，实际 %d：%+v", status, payload)
	}

	// 售后质量不提供游戏 / 商品维度：同样显式 400。
	status, _, payload = getJSON(t, app, "/admin/reports/aftersale_quality?game=王者荣耀")
	if status != http.StatusBadRequest {
		t.Fatalf("售后质量带游戏维度应返回 400，实际 %d：%+v", status, payload)
	}
	// 售后质量按处理客服筛选：真正生效（返回 200 且不报不支持）。
	status, _, payload = getJSON(t, app, "/admin/reports/aftersale_quality?responsible_admin_id=11")
	if status != http.StatusOK {
		t.Fatalf("售后质量按处理客服筛选应成功，实际 %d：%+v", status, payload)
	}
}

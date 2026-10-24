package esports

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersaleaction"
	"go-admin/internal/ent/esportsexternalrefund"
	"go-admin/internal/global"
)

// 售后列表 / 详情的外部执行信号（审核报告 ）回归：
//  1. awaiting_execution 动作项要在列表 / 详情视图里按售后单计数，且多单混合时分组正确；
//  2. 关联的外部退款「registered 且超过督办阈值」要把该售后单标成已超期，未超期 / 已执行 / 无关联一律 false；
//  3. 无相关数据的售后单为 0 / false；
//  4. 批量信号聚合的 SQL 次数不随售后单数量增长（不是 N+1）。

// seedAfterSaleSignalRow 直接落一条售后单（复用 SLA 用例的行构造，只关心状态与时间戳）。
func seedAfterSaleSignalRow(t *testing.T, db *ent.Client, no string, status esportsaftersale.Status,
	createdAt, approvedAt time.Time) *ent.EsportsAfterSale {
	t.Helper()
	return seedAfterSaleSLARow(t, db, no, status, createdAt, createdAt.Add(time.Minute),
		createdAt.Add(2*time.Minute), approvedAt)
}

// seedAfterSaleSignalAction 直接写入一条指定状态的动作项（不走方案提交，便于构造待执行项）。
func seedAfterSaleSignalAction(t *testing.T, db *ent.Client, afterSaleID, seq int,
	status esportsaftersaleaction.Status) *ent.EsportsAfterSaleAction {
	t.Helper()
	return db.EsportsAfterSaleAction.Create().
		SetAfterSaleID(afterSaleID).SetSeq(seq).
		SetActionType(esportsaftersaleaction.ActionTypeRefund).
		SetAmountCents(500).SetStatus(status).
		SetIdemKey("aftersale:" + strconv.Itoa(afterSaleID) + ":action:" + strconv.Itoa(seq) + ":signal-test").
		SaveX(t.Context())
}

// seedAfterSaleSignalRefund 直接写入一条关联售后单的外部退款登记（登记时间由调用方指定，
// 便于构造「登记很久仍未执行」的挂单与「刚登记」的正常单）。
func seedAfterSaleSignalRefund(t *testing.T, db *ent.Client, txNo string, afterSaleID int,
	status esportsexternalrefund.Status, createdAt time.Time) *ent.EsportsExternalRefund {
	t.Helper()
	return db.EsportsExternalRefund.Create().
		SetRefundNo(externalRefundNo()).SetChannel("wechat_work").
		SetTransactionNo(txNo).SetAmountCents(500).SetOrderID(1).
		SetAftersaleID(afterSaleID).SetStatus(status).
		SetReason("售后外部执行信号回归").
		SetRegisteredByAdminID(11).SetCreatedAt(createdAt).SaveX(t.Context())
}

// TestAfterSaleAdminViewsCarryExternalSignals 列表与详情都要带出两个新字段，
// 且混合多单时按售后单分组计数、未超期与已执行单不得误标超期。
func TestAfterSaleAdminViewsCarryExternalSignals(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_external_signal")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

	// 1) 等待财务执行 + 外部退款已超期（登记 48h 前，阈值 24h）。
	overdueItem := seedAfterSaleSignalRow(t, db, "AS-SIG-OVERDUE", esportsaftersale.StatusPendingExecution,
		now.Add(-48*time.Hour), now.Add(-40*time.Hour))
	seedAfterSaleSignalAction(t, db, overdueItem.ID, 1, esportsaftersaleaction.StatusAwaitingExecution)
	seedAfterSaleSignalRefund(t, db, "SIG-TX-OVERDUE", overdueItem.ID,
		esportsexternalrefund.StatusRegistered, now.Add(-48*time.Hour))

	// 2) 等待财务执行，但外部退款刚登记（1h 前，未超期）。
	freshItem := seedAfterSaleSignalRow(t, db, "AS-SIG-FRESH", esportsaftersale.StatusPendingExecution,
		now.Add(-2*time.Hour), now.Add(-time.Hour))
	seedAfterSaleSignalAction(t, db, freshItem.ID, 1, esportsaftersaleaction.StatusAwaitingExecution)
	seedAfterSaleSignalRefund(t, db, "SIG-TX-FRESH", freshItem.ID,
		esportsexternalrefund.StatusRegistered, now.Add(-time.Hour))

	// 3) 等待财务执行、无外部退款；另有一条已成功的动作项不得计入待执行数量。
	plainItem := seedAfterSaleSignalRow(t, db, "AS-SIG-PLAIN", esportsaftersale.StatusPendingExecution,
		now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	seedAfterSaleSignalAction(t, db, plainItem.ID, 1, esportsaftersaleaction.StatusAwaitingExecution)
	seedAfterSaleSignalAction(t, db, plainItem.ID, 2, esportsaftersaleaction.StatusSucceeded)

	// 4) 完全无相关数据：没有任何动作项、没有外部退款。
	emptyItem := seedAfterSaleSignalRow(t, db, "AS-SIG-EMPTY", esportsaftersale.StatusProcessing,
		now.Add(-time.Hour), time.Time{})

	// 5) 关联的外部退款早已执行（72h 前）：已执行不等于超期未执行。
	executedItem := seedAfterSaleSignalRow(t, db, "AS-SIG-EXECUTED", esportsaftersale.StatusCompleted,
		now.Add(-80*time.Hour), now.Add(-70*time.Hour))
	seedAfterSaleSignalRefund(t, db, "SIG-TX-EXECUTED", executedItem.ID,
		esportsexternalrefund.StatusExecuted, now.Add(-72*time.Hour))

	items := []*ent.EsportsAfterSale{overdueItem, freshItem, plainItem, emptyItem, executedItem}
	views, err := afterSaleAdminViewsOf(ctx, db, items, now)
	if err != nil {
		t.Fatalf("售后列表视图计算失败：%v", err)
	}
	if len(views) != len(items) {
		t.Fatalf("视图条数 = %d，期望 %d", len(views), len(items))
	}
	want := []struct {
		name      string
		awaiting  int
		overdue   bool
		slaActive bool
	}{
		{"AS-SIG-OVERDUE", 1, true, true},
		{"AS-SIG-FRESH", 1, false, true},
		{"AS-SIG-PLAIN", 1, false, true},
		// 处理中（方案阶段）仍在限时区间内，只是没有外部信号。
		{"AS-SIG-EMPTY", 0, false, true},
		// 已完成是终态，SLA 不再计时，外部退款也已执行，两个信号同样为空。
		{"AS-SIG-EXECUTED", 0, false, false},
	}
	for i, c := range want {
		view := views[i]
		if view.AfterSaleNo != c.name {
			t.Fatalf("第 %d 条售后单 = %q，期望 %q", i, view.AfterSaleNo, c.name)
		}
		if view.AwaitingExternalExecutionCount != c.awaiting {
			t.Fatalf("%s 待执行计数 = %d，期望 %d",
				c.name, view.AwaitingExternalExecutionCount, c.awaiting)
		}
		if view.ExternalRefundOverdue != c.overdue {
			t.Fatalf("%s 外部退款超期标记 = %v，期望 %v", c.name, view.ExternalRefundOverdue, c.overdue)
		}
		// 既有 SLA 字段结构不变：有阶段时限的单仍带截止时间，终态单不计算。
		if c.slaActive != (view.SLADeadline != nil) {
			t.Fatalf("%s 既有 sla_deadline 口径被改动：%+v", c.name, view.afterSaleView)
		}
	}

	// 详情复用同一聚合函数：同一张单在详情与列表上口径一致。
	for _, item := range []*ent.EsportsAfterSale{overdueItem, emptyItem} {
		detail, err := afterSaleAdminViewOf(ctx, db, item, now)
		if err != nil {
			t.Fatalf("售后详情视图计算失败：%v", err)
		}
		var list afterSaleAdminView
		for _, view := range views {
			if view.ID == item.ID {
				list = view
			}
		}
		if detail.AwaitingExternalExecutionCount != list.AwaitingExternalExecutionCount ||
			detail.ExternalRefundOverdue != list.ExternalRefundOverdue {
			t.Fatalf("%s 详情与列表口径不一致：详情 %d/%v，列表 %d/%v", item.AfterSaleNo,
				detail.AwaitingExternalExecutionCount, detail.ExternalRefundOverdue,
				list.AwaitingExternalExecutionCount, list.ExternalRefundOverdue)
		}
	}
	if detail, err := afterSaleAdminViewOf(ctx, db, emptyItem, now); err != nil ||
		detail.AwaitingExternalExecutionCount != 0 || detail.ExternalRefundOverdue {
		t.Fatalf("无相关数据的售后单应为 0/false：%+v %v", detail, err)
	}
}

// TestAfterSaleAdminViewsBatchAggregateQueries 锁定「不逐单查库」：
// 1 单与 3 单的批量视图发往数据库的语句数相同，且 3 单各自的待执行计数都是 1
// （证明是按 after_sale_id IN (...) 分组聚合，而不是只算第一单或逐单查）。
func TestAfterSaleAdminViewsBatchAggregateQueries(t *testing.T) {
	const dsn = "file:esports_aftersale_external_signal_batch?mode=memory&cache=shared&_fk=1"
	db := openAfterSaleDB(t, "esports_aftersale_external_signal_batch")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

	items := make([]*ent.EsportsAfterSale, 0, 3)
	for i, no := range []string{"AS-BATCH-1", "AS-BATCH-2", "AS-BATCH-3"} {
		item := seedAfterSaleSignalRow(t, db, no, esportsaftersale.StatusPendingExecution,
			now.Add(-time.Duration(i+2)*time.Hour), now.Add(-time.Hour))
		seedAfterSaleSignalAction(t, db, item.ID, 1, esportsaftersaleaction.StatusAwaitingExecution)
		items = append(items, item)
	}

	oneQueries := countAfterSaleViewQueries(t, dsn, items[:1], now)
	threeQueries := countAfterSaleViewQueries(t, dsn, items, now)
	if oneQueries == 0 {
		t.Fatal("SQL 计数钩子未生效，无法断言聚合查询次数")
	}
	if threeQueries != oneQueries {
		t.Fatalf("批量视图的 SQL 次数随单数增长（疑似 N+1）：1 单 %d 次，3 单 %d 次",
			oneQueries, threeQueries)
	}
	t.Logf("批量视图 SQL 次数：1 单 %d 次 / 3 单 %d 次（等值即无 N+1）", oneQueries, threeQueries)

	views, err := afterSaleAdminViewsOf(ctx, db, items, now)
	if err != nil {
		t.Fatalf("售后列表视图计算失败：%v", err)
	}
	for _, view := range views {
		if view.AwaitingExternalExecutionCount != 1 || view.ExternalRefundOverdue {
			t.Fatalf("%s 期望 1/false，实际 %d/%v", view.AfterSaleNo,
				view.AwaitingExternalExecutionCount, view.ExternalRefundOverdue)
		}
	}
}

// countAfterSaleViewQueries 统计一次批量视图计算实际发往数据库的语句数：
// 用 ent 的 debug 驱动在同一内存库上新建一个客户端，只计数不改数据。
func countAfterSaleViewQueries(t *testing.T, dsn string, items []*ent.EsportsAfterSale,
	now time.Time) int {
	t.Helper()
	count := 0
	counted, err := ent.Open("sqlite3", dsn, ent.Debug(), ent.Log(func(...any) { count++ }))
	if err != nil {
		t.Fatalf("打开计数客户端失败：%v", err)
	}
	defer counted.Close()
	if _, err := afterSaleAdminViewsOf(t.Context(), counted, items, now); err != nil {
		t.Fatalf("批量视图计算失败：%v", err)
	}
	return count
}

// TestAfterSaleAdminViewJSONCarriesExternalSignals 前端契约：两个字段是**新增**的顶层字段，
// 既有字段（售后单字段 / sla_*）保持不变；空数据时为 0 / false 而不是缺字段。
func TestAfterSaleAdminViewJSONCarriesExternalSignals(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_external_signal_json")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

	overdueItem := seedAfterSaleSignalRow(t, db, "AS-JSON-OVERDUE", esportsaftersale.StatusPendingExecution,
		now.Add(-48*time.Hour), now.Add(-40*time.Hour))
	seedAfterSaleSignalAction(t, db, overdueItem.ID, 1, esportsaftersaleaction.StatusAwaitingExecution)
	seedAfterSaleSignalRefund(t, db, "SIG-TX-JSON", overdueItem.ID,
		esportsexternalrefund.StatusRegistered, now.Add(-30*time.Hour))
	emptyItem := seedAfterSaleSignalRow(t, db, "AS-JSON-EMPTY", esportsaftersale.StatusProcessing,
		now.Add(-time.Hour), time.Time{})

	overdueView, err := afterSaleAdminViewOf(ctx, db, overdueItem, now)
	if err != nil {
		t.Fatalf("售后详情视图计算失败：%v", err)
	}
	emptyView, err := afterSaleAdminViewOf(ctx, db, emptyItem, now)
	if err != nil {
		t.Fatalf("售后详情视图计算失败：%v", err)
	}

	payload := decodeAfterSaleViewJSON(t, overdueView)
	if got := payload["awaiting_external_execution_count"]; got != float64(1) {
		t.Fatalf("awaiting_external_execution_count = %v，期望 1", got)
	}
	if got, ok := payload["external_refund_overdue"].(bool); !ok || !got {
		t.Fatalf("external_refund_overdue = %v，期望 true", payload["external_refund_overdue"])
	}
	// 既有字段仍在顶层且未被改名：售后单字段 + SLA 字段。
	for _, key := range []string{"id", "after_sale_no", "status", "sla_limit_hours", "sla_deadline", "sla_overdue"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("既有字段 %q 从售后视图 JSON 中消失（前端契约被破坏）", key)
		}
	}

	empty := decodeAfterSaleViewJSON(t, emptyView)
	if got, ok := empty["awaiting_external_execution_count"]; !ok || got != float64(0) {
		t.Fatalf("无相关数据时 awaiting_external_execution_count 应为 0 而不是缺字段：%v", empty)
	}
	if got, ok := empty["external_refund_overdue"]; !ok || got != false {
		t.Fatalf("无相关数据时 external_refund_overdue 应为 false 而不是缺字段：%v", empty)
	}
}

// decodeAfterSaleViewJSON 把视图编码成 JSON 对象，按前端实际消费的顶层字段名断言。
func decodeAfterSaleViewJSON(t *testing.T, view afterSaleAdminView) map[string]any {
	t.Helper()
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("售后视图 JSON 编码失败：%v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("售后视图 JSON 解码失败：%v", err)
	}
	return payload
}

// TestAdminAfterSaleEndpointsCarryExternalSignals 接口层验证：GET /admin/after-sales 与
// GET /admin/after-sales/{id} 的响应体里都能直接看到两个新字段（客服不必再点进动作项明细）。
func TestAdminAfterSaleEndpointsCarryExternalSignals(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_external_signal_http")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	now := time.Now().UTC()
	admin := seedAdminAccount(t, db, "cs-aftersale-signal")
	signalItem := seedAfterSaleSignalRow(t, db, "AS-HTTP-OVERDUE", esportsaftersale.StatusPendingExecution,
		now.Add(-48*time.Hour), now.Add(-40*time.Hour))
	seedAfterSaleSignalAction(t, db, signalItem.ID, 1, esportsaftersaleaction.StatusAwaitingExecution)
	seedAfterSaleSignalRefund(t, db, "SIG-TX-HTTP", signalItem.ID,
		esportsexternalrefund.StatusRegistered, now.Add(-48*time.Hour))
	seedAfterSaleSignalRow(t, db, "AS-HTTP-PLAIN", esportsaftersale.StatusProcessing,
		now.Add(-time.Hour), time.Time{})

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", true)
		return c.Next()
	})
	app.Get("/admin/after-sales", adminAfterSales)
	app.Get("/admin/after-sales/:id", adminAfterSaleDetail)

	status, _, payload := getJSON(t, app, "/admin/after-sales")
	if status != http.StatusOK {
		t.Fatalf("售后列表接口状态码 = %d，响应 %+v", status, payload)
	}
	rows, ok := payload["data"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("售后列表应返回 2 条：%+v", payload["data"])
	}
	got := map[string]map[string]any{}
	for _, row := range rows {
		item, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("售后列表条目不是对象：%+v", row)
		}
		no, _ := item["after_sale_no"].(string)
		got[no] = item
	}
	if item := got["AS-HTTP-OVERDUE"]; item == nil ||
		item["awaiting_external_execution_count"] != float64(1) || item["external_refund_overdue"] != true {
		t.Fatalf("列表未带出「待财务执行 + 外部退款已超期」：%+v", got["AS-HTTP-OVERDUE"])
	}
	if item := got["AS-HTTP-PLAIN"]; item == nil ||
		item["awaiting_external_execution_count"] != float64(0) || item["external_refund_overdue"] != false {
		t.Fatalf("列表对无信号售后单应返回 0/false：%+v", got["AS-HTTP-PLAIN"])
	}

	status, _, detailPayload := getJSON(t, app, "/admin/after-sales/"+strconv.Itoa(signalItem.ID))
	if status != http.StatusOK {
		t.Fatalf("售后详情接口状态码 = %d，响应 %+v", status, detailPayload)
	}
	detail, ok := detailPayload["data"].(map[string]any)
	if !ok {
		t.Fatalf("售后详情 data 不是对象：%+v", detailPayload["data"])
	}
	if detail["after_sale_no"] != "AS-HTTP-OVERDUE" ||
		detail["awaiting_external_execution_count"] != float64(1) || detail["external_refund_overdue"] != true {
		t.Fatalf("详情未带出两个外部执行信号：%+v", detail)
	}
	if _, ok := detail["sla_deadline"]; !ok {
		t.Fatalf("详情既有 sla_deadline 字段消失（前端契约被破坏）：%+v", detail)
	}
}

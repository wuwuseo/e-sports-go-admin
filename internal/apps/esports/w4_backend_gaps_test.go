package esports

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"
	cmfapp "github.com/wuwuseo/cmf/app"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsrechargerefundexecution"
	"go-admin/internal/global"
)

// 本文件是「审核报告收尾批次：4 处后端缺接口」的验收回归：
//  1. 售后代客确认 POST /admin/after-sales/:id/confirm
//  2. 档期超时处置可用动作 GET /admin/orders/:id/appointment/disposal-options
//  3. 服务段计时读取 GET /admin/orders/:id/players/:playerId/service/timing
//  4. 充值退款执行批次列表 GET /admin/recharge-refund-executions

// w4GetJSONToHandler 构造带登录态的最小路由并调用一次 GET handler，返回状态码与解析后的响应体。
func w4GetJSONToHandler(t *testing.T, admin *ent.Admin, isSuper bool, handler fiber.Handler,
	route, path string) (int, map[string]any) {
	t.Helper()
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Get(route, handler)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("请求 %s 失败：%v", path, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	return resp.StatusCode, decoded
}

// w4FindAdminRoute 在声明里查找指定方法 + 路径（Fiber 写法）的路由。
func w4FindAdminRoute(app cmfapp.App, method, routePath string) *cmfapp.Route {
	for i := range app.Routes {
		if app.Routes[i].Method == method && app.Routes[i].Path == routePath {
			return &app.Routes[i]
		}
	}
	return nil
}

// w4FindAPIPermission 在声明里查找指定方法 + 路径（权限写法）的 PermAPI。
func w4FindAPIPermission(app cmfapp.App, method, permPath string) *cmfapp.Permission {
	for i := range app.Permissions {
		item := app.Permissions[i]
		if item.Type == cmfapp.PermAPI && item.Method == method && item.Path == permPath {
			return &app.Permissions[i]
		}
	}
	return nil
}

// 路由契约（AGENTS.md §5-3）：4 条新路由都必须存在、为 AuthAdmin，且每条都有方法 + 完整路径
// 完全一致的 PermAPI 声明（路径参数写 {id}）。整体声明交给 cmfapp.ValidateApps 复核：
// 它同时校验「管理端路由必须有同名 PermAPI」「PermAPI 必须有对应路由」「权限码唯一」「Parent 存在」。
func TestBackendGapRoutesDeclaredWithPermissions(t *testing.T) {
	app := esportsAppDeclaration(t)
	app.Enabled = true
	if err := cmfapp.ValidateApps([]cmfapp.App{app}, cmfapp.Options{BasePath: "/api/v1"}); err != nil {
		t.Fatalf("esports 功能集声明非法：%v", err)
	}

	cases := []struct {
		name     string
		method   string
		route    string
		permPath string
		code     string
		parent   string
	}{
		{
			name: "售后代客确认", method: http.MethodPost,
			route:    "/admin/after-sales/:id/confirm",
			permPath: "/api/v1/esports/admin/after-sales/{id}/confirm",
			code:     "esports:aftersales:confirm", parent: "esports:aftersale:menu",
		},
		{
			name: "档期处置可用动作", method: http.MethodGet,
			route:    "/admin/orders/:id/appointment/disposal-options",
			permPath: "/api/v1/esports/admin/orders/{id}/appointment/disposal-options",
			code:     "esports:orders:appointment_disposal_options", parent: "esports:order:menu",
		},
		{
			name: "服务段计时读取", method: http.MethodGet,
			route:    "/admin/orders/:id/players/:playerId/service/timing",
			permPath: "/api/v1/esports/admin/orders/{id}/players/{playerId}/service/timing",
			code:     "esports:orders:service_timing", parent: "esports:order:menu",
		},
		{
			name: "充值退款执行批次列表", method: http.MethodGet,
			route:    "/admin/recharge-refund-executions",
			permPath: "/api/v1/esports/admin/recharge-refund-executions",
			code:     "esports:settlements:recharge_refund_executions", parent: "esports:settlement:menu",
		},
	}
	for _, want := range cases {
		route := w4FindAdminRoute(app, want.method, want.route)
		if route == nil {
			t.Fatalf("缺少路由 %s %s（%s）", want.method, want.route, want.name)
		}
		if route.Auth != cmfapp.AuthAdmin {
			t.Fatalf("%s 应为管理端鉴权（AuthAdmin），got=%v", want.name, route.Auth)
		}
		if route.Handler == nil {
			t.Fatalf("%s 的处理函数为空", want.name)
		}
		perm := w4FindAPIPermission(app, want.method, want.permPath)
		if perm == nil {
			t.Fatalf("%s 缺少 PermAPI 声明 %s %s", want.name, want.method, want.permPath)
		}
		if perm.Code != want.code || perm.Parent != want.parent {
			t.Fatalf("%s 的 PermAPI 声明不正确：%+v", want.name, perm)
		}
	}

	// 未加包装的列表路由必须直接指向本次实现的 handler。
	listRoute := w4FindAdminRoute(app, http.MethodGet, "/admin/recharge-refund-executions")
	if reflect.ValueOf(listRoute.Handler).Pointer() != reflect.ValueOf(listRechargeRefundExecutionsHandler).Pointer() {
		t.Fatal("批次列表路由未指向 listRechargeRefundExecutionsHandler")
	}
	// 代客确认不得复用审批路由：两者必须是不同路径 + 不同权限码。
	if approve := w4FindAdminRoute(app, http.MethodPost, "/admin/after-sales/:id/approve"); approve == nil {
		t.Fatal("审批路由缺失，无法核对职责分离")
	}
	if w4FindAPIPermission(app, http.MethodPost, "/api/v1/esports/admin/after-sales/{id}/confirm").Code ==
		w4FindAPIPermission(app, http.MethodPost, "/api/v1/esports/admin/after-sales/{id}/approve").Code {
		t.Fatal("代客确认与审批不得共用同一权限码")
	}
}

// 代客确认（§13.3）：把「待客户确认」推进到「待审批」，只写代确认人，不写审批人、不预占退款额度；
// 重复调用 409（不能当作审批的替代），且既有职责分离不变（处理人本人仍不能审批）。
func TestAfterSaleProxyConfirmAdvancesToPendingApproval(t *testing.T) {
	db := openAfterSaleDB(t, "w4_aftersale_proxy_confirm")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	order := seedBalancePaidOrder(t, db, "w4proxy")
	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "refund_request", Channel: "customer",
		Demand: "客户反馈服务时长不足，申请退款", ExpectedPlan: "按差额退款",
	}, 11, now)
	if err != nil {
		t.Fatalf("创建售后单失败：%v", err)
	}
	if _, err = acceptAfterSale(ctx, db, created.ID, 11, now); err != nil {
		t.Fatalf("受理售后失败：%v", err)
	}
	planned, err := submitAfterSalePlan(ctx, db, created.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: 1500, Resolution: "陪玩迟到，按差额退款",
	}, now)
	if err != nil || planned.Status != esportsaftersale.StatusPendingCustomerConfirm {
		t.Fatalf("提交方案应进入待客户确认：%v %+v", err, planned)
	}

	operator := seedAdminAccount(t, db, "w4-proxy-confirm")
	const route = "/admin/after-sales/:id/confirm"
	path := "/admin/after-sales/" + strconv.Itoa(created.ID) + "/confirm"
	status, body := postJSONToHandler(t, operator, true,
		ownedAfterSaleHandler(adminConfirmAfterSaleHandler), route, path, "")
	if status != http.StatusOK {
		t.Fatalf("代客确认应返回 200，got=%d body=%+v", status, body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil || data["status"] != "pending_approval" ||
		data["customer_confirm_channel"] != "admin_proxy" {
		t.Fatalf("代客确认应推进到待审批并标记 admin_proxy：%+v", body)
	}
	if got, _ := data["customer_confirmed_by_admin_id"].(float64); int(got) != operator.ID {
		t.Fatalf("代客确认人未落库：%+v", data["customer_confirmed_by_admin_id"])
	}

	// 不是审批：不写审批人、不预占退款额度（这两项只能由 approve 产生）。
	confirmed := db.EsportsAfterSale.GetX(ctx, created.ID)
	if confirmed.ApproverAdminID != nil || confirmed.ApprovedAt != nil {
		t.Fatalf("代客确认不得写成审批：%+v", confirmed)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 0 {
		t.Fatalf("代客确认不得预占退款额度：%d", got.RefundReservedCents)
	}

	// 重复代客确认 → 409：不能借这个入口把状态一路推到待执行 / 完成。
	if again, _ := postJSONToHandler(t, operator, true,
		ownedAfterSaleHandler(adminConfirmAfterSaleHandler), route, path, ""); again != http.StatusConflict {
		t.Fatalf("非待客户确认状态下代确认应冲突，got=%d", again)
	}
	if got := db.EsportsAfterSale.GetX(ctx, created.ID); got.Status != esportsaftersale.StatusPendingApproval {
		t.Fatalf("重复代确认不得改写状态：%s", got.Status)
	}

	// 职责分离不变：处理人（登记人）本人审批仍被拒绝。
	if _, err := approveAfterSale(ctx, db, created.ID, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("处理人自审未被拦截：%v", err)
	}
	// 换人审批才真正进入待执行。
	approved, err := approveAfterSale(ctx, db, created.ID, 12, now)
	if err != nil || approved.Status != esportsaftersale.StatusPendingExecution {
		t.Fatalf("换人审批失败：%v %+v", err, approved)
	}
}

// 档期处置可用动作（§5.5-06 只读）：随 app.esports.appointment_expire_action 变化而变化，
// 且不产生副作用；HTTP 接线返回同一口径。
func TestAppointmentDisposalOptionsFollowConfig(t *testing.T) {
	db := openProductDB(t, "w4_appointment_disposal_options")
	defer db.Close()
	poSeedBaseConfigs(t, db)
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	deadline := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	order := poSeedPaidOrderWithSoldQuota(t, db, "W4-APPT-OPT")
	order = db.EsportsOrder.UpdateOneID(order.ID).
		SetAppointmentStatus(esportsorder.AppointmentStatusRequest).
		SetAppointmentConfirmationDeadline(deadline).SaveX(ctx)

	both := []string{appointmentExpireActionRefund, appointmentExpireActionReschedule}
	cases := []struct {
		config  string
		actions []string
		current string
	}{
		{"", both, appointmentExpireActionBoth}, // 未配置 → both（与处置入口的回落一致）
		{appointmentExpireActionRefund, []string{appointmentExpireActionRefund}, appointmentExpireActionRefund},
		{appointmentExpireActionReschedule, []string{appointmentExpireActionReschedule}, appointmentExpireActionReschedule},
		{appointmentExpireActionBoth, both, appointmentExpireActionBoth},
		{"not-an-enum", both, appointmentExpireActionBoth}, // 非法值同样回落 both
	}
	for _, tc := range cases {
		if tc.config != "" {
			poSeedConfig(t, db, appointmentExpireActionKey, tc.config)
		}
		view := appointmentDisposalOptions(ctx, db, order)
		if view.CurrentAction != tc.current {
			t.Fatalf("current_action 应为 %q，got=%q（配置 %q）", tc.current, view.CurrentAction, tc.config)
		}
		if len(view.AllowedActions) != len(tc.actions) {
			t.Fatalf("allowed_actions 应为 %v，got=%v（配置 %q）", tc.actions, view.AllowedActions, tc.config)
		}
		for i := range tc.actions {
			if view.AllowedActions[i] != tc.actions[i] {
				t.Fatalf("allowed_actions 应为 %v，got=%v（配置 %q）", tc.actions, view.AllowedActions, tc.config)
			}
		}
		if want := deadline.UTC().Format(time.RFC3339); view.ExpireAt != want {
			t.Fatalf("expire_at 应为档期确认截止时间 %q，got=%q", want, view.ExpireAt)
		}
	}

	// 只读：不改变订单状态与版本。
	before := db.EsportsOrder.GetX(ctx, order.ID)
	_ = appointmentDisposalOptions(ctx, db, before)
	after := db.EsportsOrder.GetX(ctx, order.ID)
	if before.Version != after.Version || before.AppointmentStatus != after.AppointmentStatus ||
		before.AppointmentSuspendedAt != nil || after.AppointmentSuspendedAt != nil {
		t.Fatalf("读取处置选项不应有副作用：before=%+v after=%+v", before, after)
	}

	// HTTP 接线：配置为 refund 时只回 refund。
	poSeedConfig(t, db, appointmentExpireActionKey, appointmentExpireActionRefund)
	reader := seedAdminAccount(t, db, "w4-appt-options")
	status, body := w4GetJSONToHandler(t, reader, true,
		readableOrderHandler(appointmentDisposalOptionsHandler),
		"/admin/orders/:id/appointment/disposal-options",
		"/admin/orders/"+strconv.Itoa(order.ID)+"/appointment/disposal-options")
	if status != http.StatusOK {
		t.Fatalf("读取处置选项应返回 200，got=%d body=%+v", status, body)
	}
	payload, _ := body["data"].(map[string]any)
	if payload == nil || payload["current_action"] != appointmentExpireActionRefund ||
		payload["expire_at"] != deadline.UTC().Format(time.RFC3339) {
		t.Fatalf("处置选项响应不正确：%+v", body)
	}
	actions, _ := payload["allowed_actions"].([]any)
	if len(actions) != 1 || actions[0] != appointmentExpireActionRefund {
		t.Fatalf("allowed_actions 应为 [refund]，got=%+v", payload["allowed_actions"])
	}
}

// 服务段计时读取（§3.3 只读）：有效 / 墙钟 / 暂停秒数与上界口径正确，且上界与更正校验完全一致
// （上界值可更正，上界 +1 必被拒）；非本单成员 403；HTTP 接线字段齐全。
func TestServiceTimingReadoutMatchesCorrectionBound(t *testing.T) {
	db := openPlayerTestDB(t, "w4_service_timing_readout")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	const adminID = 91
	player := seedPlayer(t, db, 9101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	other := seedPlayer(t, db, 9102, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "W4-TIMING-READ")
	// 订单标准时长 1 分钟：上界 = min(墙钟 100 秒, 60×1.2 = 72 秒) = 72 秒，用于验证 clamp。
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(adminID).
		SetServiceDurationMinutes(1).ExecX(ctx)
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := setPlayerServicePause(ctx, db, player.ID, order.ID, true, "中场暂停", now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := setPlayerServicePause(ctx, db, player.ID, order.ID, false, "", now.Add(40*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := finishPlayerService(ctx, db, player.ID, order.ID, 1, 70, "proof", now.Add(100*time.Second)); err != nil {
		t.Fatal(err)
	}

	readout, err := serviceTimingForAdmin(ctx, db, order.ID, player.ID, now.Add(120*time.Second))
	if err != nil {
		t.Fatalf("读取服务段计时失败：%v", err)
	}
	if readout.WallClockSeconds != 100 || readout.UpperBoundSeconds != 72 ||
		readout.EffectiveSeconds != 70 || readout.PausedSeconds != 30 ||
		readout.Status != "finished" || readout.SeatIndex != 1 {
		t.Fatalf("计时口径不正确：%+v", readout)
	}

	// 上界与更正校验一致：上界 +1 必被拒，等于上界可通过（同一 serviceTimingCorrectionBound）。
	if _, err := correctServiceTiming(ctx, db, adminID, order.ID, player.ID, false,
		readout.UpperBoundSeconds+1, readout.PausedSeconds, "player", "超过上界", now.Add(130*time.Second)); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超过上界应被拒绝：%v", err)
	}
	if _, err := correctServiceTiming(ctx, db, adminID, order.ID, player.ID, false,
		readout.UpperBoundSeconds, readout.PausedSeconds, "player", "按上界更正", now.Add(130*time.Second)); err != nil {
		t.Fatalf("等于上界应被接受：%v", err)
	}
	updated, err := serviceTimingForAdmin(ctx, db, order.ID, player.ID, now.Add(140*time.Second))
	if err != nil || updated.EffectiveSeconds != readout.UpperBoundSeconds {
		t.Fatalf("更正后读回的生效秒数应与上界一致：%+v %v", updated, err)
	}

	// 非本单成员：403（与代暂停 / 更正同一口径）。
	if _, err := serviceTimingForAdmin(ctx, db, order.ID, other.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非本单成员应返回 403：%v", err)
	}

	// HTTP 接线：字段齐全，且上界仍等于更正边界。
	reader := seedAdminAccount(t, db, "w4-service-timing")
	status, body := w4GetJSONToHandler(t, reader, true, readableOrderHandler(adminServiceTimingHandler),
		"/admin/orders/:id/players/:playerId/service/timing",
		"/admin/orders/"+strconv.Itoa(order.ID)+"/players/"+strconv.Itoa(player.ID)+"/service/timing")
	if status != http.StatusOK {
		t.Fatalf("读取服务段计时应返回 200，got=%d body=%+v", status, body)
	}
	payload, _ := body["data"].(map[string]any)
	if payload == nil || payload["status"] != "finished" {
		t.Fatalf("计时响应不正确：%+v", body)
	}
	for _, key := range []string{"effective_seconds", "wall_clock_seconds", "upper_bound_seconds", "paused_seconds"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("计时响应缺少字段 %s：%+v", key, payload)
		}
	}
	if got, _ := payload["upper_bound_seconds"].(float64); int64(got) != readout.UpperBoundSeconds {
		t.Fatalf("HTTP 上界与更正边界不一致：%+v", payload["upper_bound_seconds"])
	}
}

// 充值退款执行批次列表（§4.5 只读）：可按 status 过滤、默认按 ID 倒序、带上关联售后单号与执行人；
// 非法 status 中文报错；HTTP 接线支持 ?status=&limit=。
func TestRechargeRefundExecutionListQuery(t *testing.T) {
	db := openAfterSaleDB(t, "w4_refund_execution_list")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	afterSale := db.EsportsAfterSale.Create().
		SetAfterSaleNo("AS-W4-RRE-1").SetSourceType(esportsaftersale.SourceTypeRecharge).
		SetSourceID(7).SetKind(esportsaftersale.KindRefundRequest).
		SetChannel(esportsaftersale.ChannelAdmin).SetDemand("充值按批次退款").SaveX(ctx)
	pending := db.EsportsRechargeRefundExecution.Create().
		SetExecutionNo("RRE-W4-1").SetIdemKey("w4-key-1").SetAfterSaleID(afterSale.ID).
		SetAccountType("customer_balance").SetAccountID("5").
		SetBatchSourceType("wallet_tx").SetBatchSourceID("11").
		SetAmountCents(1200).SetStatus(esportsrechargerefundexecution.StatusPending).SaveX(ctx)
	executed := db.EsportsRechargeRefundExecution.Create().
		SetExecutionNo("RRE-W4-2").SetIdemKey("w4-key-2").SetAfterSaleID(afterSale.ID).
		SetAccountType("customer_balance").SetAccountID("5").
		SetBatchSourceType("wallet_tx").SetBatchSourceID("12").
		SetAmountCents(800).SetStatus(esportsrechargerefundexecution.StatusExecuted).
		SetExecutedByAdminID(33).SetExecutedAt(now).SetFinanceRef("BANK-W4-1").SaveX(ctx)

	all, err := listRechargeRefundExecutions(ctx, db, "", 0)
	if err != nil {
		t.Fatalf("列出批次失败：%v", err)
	}
	if len(all) != 2 || all[0].ID != executed.ID || all[1].ID != pending.ID {
		t.Fatalf("批次列表应按 ID 倒序返回两条：%+v", all)
	}
	if all[0].ExecutionNo != "RRE-W4-2" || all[0].AmountCents != 800 ||
		all[0].Status != "executed" || all[0].AfterSaleNo != "AS-W4-RRE-1" ||
		all[0].ExecutedByAdminID == nil || *all[0].ExecutedByAdminID != 33 ||
		all[0].FinanceRef != "BANK-W4-1" || all[0].ExecutedAt == nil {
		t.Fatalf("已打款批次字段不完整：%+v", all[0])
	}
	if all[1].Status != "pending" || all[1].ExecutedByAdminID != nil || all[1].FinanceRef != "" {
		t.Fatalf("待打款批次字段不正确：%+v", all[1])
	}

	pendingOnly, err := listRechargeRefundExecutions(ctx, db, "pending", 10)
	if err != nil || len(pendingOnly) != 1 || pendingOnly[0].ID != pending.ID {
		t.Fatalf("status=pending 过滤不正确：%+v %v", pendingOnly, err)
	}
	executedOnly, err := listRechargeRefundExecutions(ctx, db, " executed ", 10)
	if err != nil || len(executedOnly) != 1 || executedOnly[0].ID != executed.ID {
		t.Fatalf("status=executed 过滤不正确：%+v %v", executedOnly, err)
	}
	if _, err := listRechargeRefundExecutions(ctx, db, "void", 10); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法 status 应返回参数错误：%v", err)
	}
	if limited, err := listRechargeRefundExecutions(ctx, db, "", 1); err != nil || len(limited) != 1 || limited[0].ID != executed.ID {
		t.Fatalf("limit 未生效：%+v %v", limited, err)
	}

	// HTTP 接线：?status=pending&limit=5。
	reader := seedAdminAccount(t, db, "w4-refund-execution-list")
	status, body := w4GetJSONToHandler(t, reader, false, listRechargeRefundExecutionsHandler,
		"/admin/recharge-refund-executions", "/admin/recharge-refund-executions?status=pending&limit=5")
	if status != http.StatusOK {
		t.Fatalf("批次列表应返回 200，got=%d body=%+v", status, body)
	}
	items, _ := body["data"].([]any)
	if len(items) != 1 {
		t.Fatalf("批次列表应返回 1 条，got=%+v", body["data"])
	}
	first, _ := items[0].(map[string]any)
	if first == nil || first["execution_no"] != "RRE-W4-1" || first["after_sale_no"] != "AS-W4-RRE-1" {
		t.Fatalf("批次列表字段不正确：%+v", items[0])
	}
}

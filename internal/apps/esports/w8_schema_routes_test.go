package esports

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"
	cmfapp "github.com/wuwuseo/cmf/app"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersaleaction"
	"go-admin/internal/ent/esportsmemberlevelrule"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/global"
	"go-admin/internal/tasks"
)

// 本文件是「清零残留」批次（审核报告 ）的回归：
//  1. 外部渠道退款动作项新增 awaiting_execution（已登记待执行），售后单不落 execution_failed，
//     财务执行外部退款后动作项转 succeeded、售后单结案；
//  2. 代客确认 / 客户确认共用入口补存单冻结守卫；
//  3. 客服提成明细冻结 / 解冻 HTTP 入口；
//  4. 会员等级删除路由（仅停用且未被引用）；
//  5. 外部退款超时督办任务的注册与 cron 种子成对。

// w8DeleteJSONToHandler 构造带登录态的最小路由并调用一次 DELETE handler，返回状态码与解析后的响应体。
func w8DeleteJSONToHandler(t *testing.T, admin *ent.Admin, isSuper bool, handler fiber.Handler,
	route, path string) (int, map[string]any) {
	t.Helper()
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Delete(route, handler)
	req := httptest.NewRequest(http.MethodDelete, path, nil)
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

// w8ResponseMessage 取统一响应体里的中文提示（fail() 用 message 字段）。
func w8ResponseMessage(body map[string]any) string {
	message, _ := body["message"].(string)
	return message
}

// ：外部渠道退款登记后动作项落 awaiting_execution（不是假 succeeded），
// 售后单停在 pending_execution（不被当作 unresolved 落 execution_failed）；
// 财务执行外部退款成功后动作项转 succeeded、售后单结案。
func TestAfterSaleActionAwaitingExecutionOnExternalRefund(t *testing.T) {
	db := openAfterSaleDB(t, "w8_aftersale_awaiting_execution")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)

	// 外部渠道已核销订单：没有余额 / 储值卡核销流水，退原核销款只能登记外部退款单。
	order := seedPaidOrder(t, db, "w8-awaiting", 1)
	db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentChannel(esportsorder.PaymentChannelMerchantQr).ExecX(ctx)
	planned := newMultiActionAfterSale(t, db, order.ID, "refund_request", 11, []afterSaleActionInput{
		{ActionType: "refund", AmountCents: 800, SourceRef: "order:" + order.OrderNo},
	}, now)
	if _, err := approveAfterSale(ctx, db, planned.ID, 12, now); err != nil {
		t.Fatalf("审批失败：%v", err)
	}

	done, err := executeAfterSale(ctx, db, planned.ID, 12, now)
	if err != nil {
		t.Fatalf("执行售后失败：%v", err)
	}
	if done.Status == esportsaftersale.StatusExecutionFailed {
		t.Fatalf("外部渠道退款已登记，售后单不应被判执行失败：%+v", done)
	}
	if done.Status != esportsaftersale.StatusPendingExecution {
		t.Fatalf("外部渠道退款待财务执行期间售后单应停在待执行：%+v", done)
	}
	if !strings.Contains(done.Resolution, "待财务核验执行") {
		t.Fatalf("应写明外部退款待财务执行：%q", done.Resolution)
	}
	action := db.EsportsAfterSaleAction.Query().Where(
		esportsaftersaleaction.AfterSaleIDEQ(planned.ID),
		esportsaftersaleaction.SeqEQ(1),
	).OnlyX(ctx)
	if action.Status != esportsaftersaleaction.StatusAwaitingExecution {
		t.Fatalf("外部渠道退款动作项应落 awaiting_execution，got=%s", action.Status)
	}
	// 钱未出账：预占额度保留、不累加已退额。
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 0 || got.RefundReservedCents != 800 {
		t.Fatalf("登记阶段不得累加已退额 / 释放预占：%+v", got)
	}

	// 再次执行：待执行项不再重复登记，也不被当作失败。
	again, err := executeAfterSale(ctx, db, planned.ID, 13, now)
	if err != nil || again.Status != esportsaftersale.StatusPendingExecution {
		t.Fatalf("重复执行应保持待执行：%v %+v", err, again)
	}
	refunds := db.EsportsExternalRefund.Query().AllX(ctx)
	if len(refunds) != 1 {
		t.Fatalf("应只登记一张外部退款单，got=%d", len(refunds))
	}

	// 财务执行外部退款 → 动作项转 succeeded、售后单结案。
	if _, err = executeExternalRefund(ctx, db, 14, refunds[0].ID, now); err != nil {
		t.Fatalf("财务执行外部退款失败：%v", err)
	}
	settled := db.EsportsAfterSaleAction.GetX(ctx, action.ID)
	if settled.Status != esportsaftersaleaction.StatusSucceeded ||
		!strings.Contains(settled.ResultSummary, "外部渠道退款已由财务执行") {
		t.Fatalf("财务执行后动作项应转已执行：%+v", settled)
	}
	if got := db.EsportsAfterSale.GetX(ctx, planned.ID); got.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("财务执行后售后单应结案：%+v", got)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 800 || got.RefundReservedCents != 0 {
		t.Fatalf("财务执行后应核销预占并累加已退额：%+v", got)
	}
}

// 客户确认与代客确认共用 confirmAfterSalePlanIn，冻结订单不得被推进到待审批；
// 两个入口都返回 errOrderFrozen，HTTP 层映射 409 + 中文提示，状态与额度均不变。
func TestAfterSaleConfirmRejectedWhenOrderFrozen(t *testing.T) {
	db := openAfterSaleDB(t, "w8_aftersale_confirm_frozen")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	order := seedBalancePaidOrder(t, db, "w8confirm")
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

	db.EsportsOrder.UpdateOneID(order.ID).
		SetDepositStatus(esportsorder.DepositStatusFrozen).ExecX(ctx)

	// 代客确认：冻结守卫拦截。
	if _, err = confirmAfterSaleByAdmin(ctx, db, created.ID, 12, now); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结订单的代客确认应被拒（errOrderFrozen），got=%v", err)
	}
	// 客户自助确认：同一守卫同样拦截。
	if planned.CustomerID == nil {
		t.Fatal("售后单应绑定客户")
	}
	if _, err = confirmAfterSaleByCustomer(ctx, db, created.ID, *planned.CustomerID, now); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结订单的客户自助确认应被拒（errOrderFrozen），got=%v", err)
	}
	if got := db.EsportsAfterSale.GetX(ctx, created.ID); got.Status != esportsaftersale.StatusPendingCustomerConfirm ||
		got.CustomerConfirmChannel != esportsaftersale.CustomerConfirmChannelNone {
		t.Fatalf("被拒的确认不得改写售后状态：%+v", got)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 0 {
		t.Fatalf("被拒的确认不得预占退款额度：%d", got.RefundReservedCents)
	}

	// HTTP 层：409 + 中文提示（复用既有路由的 handler）。
	operator := seedAdminAccount(t, db, "w8-confirm-frozen")
	status, body := postJSONToHandler(t, operator, true,
		ownedAfterSaleHandler(adminConfirmAfterSaleHandler), "/admin/after-sales/:id/confirm",
		"/admin/after-sales/"+strconv.Itoa(created.ID)+"/confirm", "")
	if status != http.StatusConflict {
		t.Fatalf("冻结订单代客确认应返回 409，got=%d body=%+v", status, body)
	}
	if message := w8ResponseMessage(body); !strings.Contains(message, "存单冻结") {
		t.Fatalf("应给出「订单已存单冻结」的中文提示，got=%q", message)
	}

	// 恢复存单后可正常代客确认。
	db.EsportsOrder.UpdateOneID(order.ID).
		SetDepositStatus(esportsorder.DepositStatusNone).ExecX(ctx)
	confirmed, err := confirmAfterSaleByAdmin(ctx, db, created.ID, 12, now)
	if err != nil || confirmed.Status != esportsaftersale.StatusPendingApproval {
		t.Fatalf("恢复存单后代客确认应成功：%v %+v", err, confirmed)
	}
}

// ：客服提成明细冻结 / 解冻必须有管理端路由 + 方法路径完全一致的 PermAPI，
// 冻结需填原因（中文提示），解冻记录操作人（外盒留痕）。
func TestCsCommissionEntryFreezeRoutesAndHandlers(t *testing.T) {
	app := esportsAppDeclaration(t)
	app.Enabled = true
	if err := cmfapp.ValidateApps([]cmfapp.App{app}, cmfapp.Options{BasePath: "/api/v1"}); err != nil {
		t.Fatalf("esports 功能集声明非法：%v", err)
	}
	cases := []struct {
		name   string
		route  string
		perm   string
		code   string
		parent string
	}{
		{
			name:  "冻结客服提成明细",
			route: "/admin/cs-commission-entries/:id/freeze",
			perm:  "/api/v1/esports/admin/cs-commission-entries/{id}/freeze",
			code:  "esports:settlements:cs_entry_freeze", parent: "esports:settlement:menu",
		},
		{
			name:  "解冻客服提成明细",
			route: "/admin/cs-commission-entries/:id/unfreeze",
			perm:  "/api/v1/esports/admin/cs-commission-entries/{id}/unfreeze",
			code:  "esports:settlements:cs_entry_unfreeze", parent: "esports:settlement:menu",
		},
	}
	for _, want := range cases {
		route := w4FindAdminRoute(app, http.MethodPost, want.route)
		if route == nil {
			t.Fatalf("缺少路由 POST %s（%s）", want.route, want.name)
		}
		if route.Auth != cmfapp.AuthAdmin {
			t.Fatalf("%s 应为 AuthAdmin，got=%v", want.name, route.Auth)
		}
		perm := w4FindAPIPermission(app, http.MethodPost, want.perm)
		if perm == nil {
			t.Fatalf("%s 缺少 PermAPI %s", want.name, want.perm)
		}
		if perm.Code != want.code || perm.Parent != want.parent {
			t.Fatalf("%s 的 PermAPI 声明不正确：%+v", want.name, perm)
		}
	}
	if route := w4FindAdminRoute(app, http.MethodPost, "/admin/cs-commission-entries/:id/freeze"); route.Handler == nil {
		t.Fatal("冻结路由处理函数为空")
	}

	db := openCsCommissionDB(t, "w8_cs_entry_freeze_routes")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	admin, _, _, entry, _ := seedCsCommissionReady(t, db, "w8freeze")
	const route = "/admin/cs-commission-entries/:id/freeze"
	path := "/admin/cs-commission-entries/" + strconv.Itoa(entry.ID) + "/freeze"

	// 缺原因 → 400 中文提示。
	status, body := postJSONToHandler(t, admin, true, freezeCsCommissionEntryHandler, route, path, `{}`)
	if status != http.StatusBadRequest || !strings.Contains(w8ResponseMessage(body), "须填写原因") {
		t.Fatalf("冻结缺原因应 400 并给中文提示，got=%d body=%+v", status, body)
	}
	// 正常冻结：记录操作人与原因。
	status, body = postJSONToHandler(t, admin, true, freezeCsCommissionEntryHandler, route, path,
		`{"reason":"客户争议待核"}`)
	if status != http.StatusOK {
		t.Fatalf("冻结应返回 200，got=%d body=%+v", status, body)
	}
	payload, _ := body["data"].(map[string]any)
	if payload == nil || payload["frozen_reason"] != "客户争议待核" {
		t.Fatalf("冻结响应应回传原因：%+v", body)
	}
	if got, _ := payload["frozen_by_admin_id"].(float64); int(got) != admin.ID {
		t.Fatalf("冻结人未记录：%+v", payload["frozen_by_admin_id"])
	}
	// 重复冻结 → 409。
	if status, _ = postJSONToHandler(t, admin, true, freezeCsCommissionEntryHandler, route, path,
		`{"reason":"再次冻结"}`); status != http.StatusConflict {
		t.Fatalf("重复冻结应 409，got=%d", status)
	}

	// 解冻（原因可选：空正文也可）：操作人与冻结快照写外盒留痕。
	unfreezeRoute := "/admin/cs-commission-entries/:id/unfreeze"
	unfreezePath := "/admin/cs-commission-entries/" + strconv.Itoa(entry.ID) + "/unfreeze"
	if status, body = postJSONToHandler(t, admin, true, unfreezeCsCommissionEntryHandler,
		unfreezeRoute, unfreezePath, ""); status != http.StatusOK {
		t.Fatalf("解冻应返回 200，got=%d body=%+v", status, body)
	}
	event := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("settlement.cs_entry_unfrozen"),
	).OnlyX(ctx)
	if event.AggregateRef != strconv.Itoa(entry.ID) {
		t.Fatalf("解冻留痕的聚合引用不正确：%+v", event)
	}
	if got, _ := event.Payload["operator_admin_id"].(float64); int(got) != admin.ID {
		t.Fatalf("解冻操作人未留痕：%+v", event.Payload)
	}
	if event.Payload["frozen_reason"] != "客户争议待核" {
		t.Fatalf("解冻留痕应带上冻结原因快照：%+v", event.Payload)
	}
	if live := db.EsportsCsCommissionEntry.GetX(ctx, entry.ID); live.FrozenAt != nil || live.FrozenByAdminID != nil {
		t.Fatalf("解冻后冻结痕迹应清空：%+v", live)
	}
	// 未冻结再解冻 → 409。
	if status, _ = postJSONToHandler(t, admin, true, unfreezeCsCommissionEntryHandler,
		unfreezeRoute, unfreezePath, ""); status != http.StatusConflict {
		t.Fatalf("未冻结明细解冻应 409，got=%d", status)
	}
}

// ：会员等级删除路由 + 语义（启用中拒绝、被客户引用拒绝、停用且无引用才可删）。
func TestMemberLevelDeleteRouteSemantics(t *testing.T) {
	app := esportsAppDeclaration(t)
	app.Enabled = true
	if err := cmfapp.ValidateApps([]cmfapp.App{app}, cmfapp.Options{BasePath: "/api/v1"}); err != nil {
		t.Fatalf("esports 功能集声明非法：%v", err)
	}
	route := w4FindAdminRoute(app, http.MethodDelete, "/admin/member-levels/:id")
	if route == nil || route.Auth != cmfapp.AuthAdmin || route.Handler == nil {
		t.Fatalf("缺少 DELETE /admin/member-levels/:id 管理端路由：%+v", route)
	}
	perm := w4FindAPIPermission(app, http.MethodDelete, "/api/v1/esports/admin/member-levels/{id}")
	if perm == nil || perm.Code != "esports:members:level_delete" || perm.Parent != "esports:member:menu" {
		t.Fatalf("删除会员等级的 PermAPI 声明不正确：%+v", perm)
	}

	db := openMemberDB(t, "w8_member_level_delete")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	level := seedMemberLevel(t, db, memberLevelInput{Code: "w8-del", Name: "待删等级"})
	// 启用中 → 中文冲突提示，引导先停用。
	err := deleteMemberLevel(ctx, db, level.ID)
	if !errors.Is(err, errConflict) || !strings.Contains(err.Error(), "请先停用") {
		t.Fatalf("启用中的等级删除应被拒：%v", err)
	}
	disabled, err := setMemberLevelStatus(ctx, db, level.ID, level.Version, "disabled")
	if err != nil {
		t.Fatalf("停用等级失败：%v", err)
	}
	// 停用但被客户作为当前等级引用 → 拒绝。
	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("w8-del").ExecX(ctx)
	err = deleteMemberLevel(ctx, db, disabled.ID)
	if !errors.Is(err, errConflict) || !strings.Contains(err.Error(), "仍被客户引用") {
		t.Fatalf("被客户引用的等级删除应被拒：%v", err)
	}
	if !db.EsportsMemberLevelRule.Query().Where(esportsmemberlevelrule.IDEQ(disabled.ID)).ExistX(ctx) {
		t.Fatal("被拒的删除不得删掉等级")
	}

	// 解绑后删除成功（服务层 + HTTP 入口）。
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("").ExecX(ctx)
	admin := seedAdminAccount(t, db, "w8-level-delete")
	status, body := w8DeleteJSONToHandler(t, admin, true, deleteMemberLevelHandler,
		"/admin/member-levels/:id", "/admin/member-levels/"+strconv.Itoa(disabled.ID))
	if status != http.StatusOK {
		t.Fatalf("停用且无引用的等级删除应返回 200，got=%d body=%+v", status, body)
	}
	if db.EsportsMemberLevelRule.Query().Where(esportsmemberlevelrule.IDEQ(disabled.ID)).ExistX(ctx) {
		t.Fatal("删除后等级应不存在")
	}
	// 已删除再删 → 404（记录不存在）。
	if status, _ = w8DeleteJSONToHandler(t, admin, true, deleteMemberLevelHandler,
		"/admin/member-levels/:id", "/admin/member-levels/"+strconv.Itoa(disabled.ID)); status != http.StatusNotFound {
		t.Fatalf("重复删除应返回 404，got=%d", status)
	}
}

// ：外部退款超时督办任务必须同时有 tasks.Register 注册与 cron 种子（成对），
// 种子表达式为每小时第 15 分（6 段含秒）。
func TestExternalRefundOverdueTaskRegisteredWithCronSeed(t *testing.T) {
	const name = "esports_expire_pending_external_refunds"
	var seed CronSeed
	found := false
	for _, item := range CronSeeds() {
		if item.Name == name {
			seed, found = item, true
		}
	}
	if !found {
		t.Fatalf("缺少 cron 种子 %s：任务注册了却永远不会被调度", name)
	}
	if seed.Expression != "0 15 * * * *" {
		t.Fatalf("cron 表达式应为每小时 15 分，got=%q", seed.Expression)
	}
	if _, err := cronSeedsParser().Parse(seed.Expression); err != nil {
		t.Fatalf("cron 表达式非法：%v", err)
	}
	if strings.TrimSpace(seed.Description) == "" {
		t.Fatal("cron 种子缺少说明文案")
	}

	// 注册表在功能集开启（ESPORTS_EXPERIMENTAL=1）时才注册 esports_ 任务；未开启时跳过交叉校验
	// （与 TestCronSeedsAreValidAndComplete 同口径：内置 health_check 恒存在，不能拿它当判断依据）。
	anyEsports, registered := false, false
	for _, item := range tasks.List() {
		if !strings.HasPrefix(item, "esports_") {
			continue
		}
		anyEsports = true
		if item == name {
			registered = true
		}
	}
	if !anyEsports {
		t.Log("功能集未开启，跳过「注册表 ↔ 种子」交叉校验")
		return
	}
	if !registered {
		t.Fatalf("任务 %s 未注册到任务注册表", name)
	}
	if fn, ok := tasks.Get(name); !ok || fn == nil {
		t.Fatalf("任务 %s 的注册函数为空", name)
	}
}

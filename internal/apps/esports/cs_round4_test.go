package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomerfollowup"
	"go-admin/internal/ent/esportsorder"
)

// 第 4 轮新增配置键字面量（RED 阶段生产常量尚未存在）。
const repurchaseFollowUpDaysKeyLiteral = "app.esports.repurchase_follow_up_days"

// ── ：客户归属转移 ──

// 归属转移：改 owner_admin_id、留操作人与时间、历史跟进不变；越权与非法目标被拒。
func TestTransferCustomerOwner(t *testing.T) {
	db := openCSDB(t, "esports_r4_customer_transfer")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	owner := seedAdminAccount(t, db, "r4-cs-owner")
	target := seedAdminAccount(t, db, "r4-cs-target")
	other := seedAdminAccount(t, db, "r4-cs-other")
	customer := db.EsportsCustomer.Create().SetDisplayName("待转交客户").SetOwnerAdminID(owner.ID).SaveX(ctx)
	history := db.EsportsCustomerFollowUp.Create().SetCustomerID(customer.ID).
		SetAdminID(owner.ID).SetSummary("历史跟进：客户偏好晚上开黑").SaveX(ctx)

	// 非归属客服（且非超管）不能转交他人客户。
	if _, err := transferCustomerOwner(ctx, db, other.ID, false, customer.ID, target.ID, "越权转交", now); !errors.Is(err, errForbidden) {
		t.Fatalf("非归属客服转交应被拒，实际 %v", err)
	}
	// 目标客服必须存在。
	if _, err := transferCustomerOwner(ctx, db, owner.ID, false, customer.ID, 999999, "不存在的人", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("目标客服不存在应返回参数错误，实际 %v", err)
	}
	// 转交给当前归属人（自己）无意义。
	if _, err := transferCustomerOwner(ctx, db, owner.ID, false, customer.ID, owner.ID, "转给自己", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("转交给当前归属人应返回参数错误，实际 %v", err)
	}

	updated, err := transferCustomerOwner(ctx, db, owner.ID, false, customer.ID, target.ID, "客户交接给同事", now)
	if err != nil {
		t.Fatalf("归属转移失败：%v", err)
	}
	if updated.OwnerAdminID == nil || *updated.OwnerAdminID != target.ID {
		t.Fatalf("归属未更新为 %d：%+v", target.ID, updated.OwnerAdminID)
	}
	// 新归属客服可见，旧客服立即失去访问权。
	if _, err := loadOwnedCustomer(ctx, db, target.ID, false, customer.ID); err != nil {
		t.Fatalf("新归属客服应可见该客户：%v", err)
	}
	if _, err := loadOwnedCustomer(ctx, db, owner.ID, false, customer.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("原客服应失去访问权，实际 %v", err)
	}
	// 历史跟进不变（只增不改）。
	stored := db.EsportsCustomerFollowUp.GetX(ctx, history.ID)
	if stored.AdminID != owner.ID || stored.Summary != "历史跟进：客户偏好晚上开黑" {
		t.Fatalf("历史跟进记录被改写：%+v", stored)
	}
	// 转移动作留痕：追加一条含操作人与时间的交接记录。
	latest := db.EsportsCustomerFollowUp.Query().Where(
		esportscustomerfollowup.CustomerIDEQ(customer.ID),
	).Order(ent.Desc(esportscustomerfollowup.FieldID)).FirstX(ctx)
	if latest.ID == history.ID {
		t.Fatal("归属转移未留痕（无追加的交接记录）")
	}
	if latest.AdminID != owner.ID {
		t.Fatalf("交接记录应记操作人 %d，实际 %d", owner.ID, latest.AdminID)
	}
	if !strings.Contains(latest.Summary, "转交") {
		t.Fatalf("交接记录摘要应说明归属转移：%q", latest.Summary)
	}
	if latest.CreatedAt.Before(now.Add(-time.Minute)) {
		t.Fatalf("交接记录时间异常：%s", latest.CreatedAt)
	}
}

// 超管可转交任意客户（既有超管越权口径）。
func TestTransferCustomerOwnerBySuperAdmin(t *testing.T) {
	db := openCSDB(t, "esports_r4_customer_transfer_super")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	owner := seedAdminAccount(t, db, "r4-cs-owner2")
	target := seedAdminAccount(t, db, "r4-cs-target2")
	super := seedAdminAccount(t, db, "r4-cs-super")
	customer := db.EsportsCustomer.Create().SetDisplayName("超管转交客户").SetOwnerAdminID(owner.ID).SaveX(ctx)

	updated, err := transferCustomerOwner(ctx, db, super.ID, true, customer.ID, target.ID, "管理员统一分配", now)
	if err != nil {
		t.Fatalf("超管转交失败：%v", err)
	}
	if updated.OwnerAdminID == nil || *updated.OwnerAdminID != target.ID {
		t.Fatalf("超管转交后归属应为 %d：%+v", target.ID, updated.OwnerAdminID)
	}
}

// ── ：档案改动乐观锁 ──

// 带 expected_version 的改档为乐观锁：版本正确自增，过期版本返回冲突。
func TestUpdateCustomerProfileOptimisticLock(t *testing.T) {
	db := openCSDB(t, "esports_r4_customer_profile_lock")
	defer db.Close()
	ctx := t.Context()

	customer := db.EsportsCustomer.Create().SetDisplayName("档案客户").SetOwnerAdminID(1).SaveX(ctx)
	first, err := updateCustomerProfile(ctx, db, 1, false, customer.ID, customerProfileInput{
		ConsumptionPreference: "王者荣耀", ExpectedVersion: customer.Version,
	})
	if err != nil {
		t.Fatalf("带正确版本的改档应成功：%v", err)
	}
	if first.Version != customer.Version+1 {
		t.Fatalf("改档后版本应自增：%d → %d", customer.Version, first.Version)
	}
	if first.ConsumptionPreference != "王者荣耀" {
		t.Fatalf("改档未生效：%+v", first)
	}
	// 并发改档：其中一次拿着过期版本必然冲突（同一旧版本第二次提交）。
	if _, err := updateCustomerProfile(ctx, db, 1, false, customer.ID, customerProfileInput{
		ConsumptionPreference: "英雄联盟", ExpectedVersion: customer.Version,
	}); !errors.Is(err, errConflict) {
		t.Fatalf("过期版本改档应返回冲突，实际 %v", err)
	}
	// 版本冲突后档案不应被覆盖。
	reloaded := db.EsportsCustomer.GetX(ctx, customer.ID)
	if reloaded.ConsumptionPreference != "王者荣耀" {
		t.Fatalf("冲突请求不应写入：%+v", reloaded)
	}
	// 用最新版本重试成功。
	if _, err := updateCustomerProfile(ctx, db, 1, false, customer.ID, customerProfileInput{
		ConsumptionPreference: "英雄联盟", ExpectedVersion: first.Version,
	}); err != nil {
		t.Fatalf("用最新版本重试应成功：%v", err)
	}
}

// ── （剩余）：客服看板 / 待办计数纳入「被授权协作」订单 ──

// 协作客服的待办计数必须与订单列表口径一致（负责 OR 协作）。
func TestCsWorkbenchTodosIncludeCollaboratorOrders(t *testing.T) {
	db := openCSDB(t, "esports_r4_cs_collab_todos")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	owner := seedAdminAccount(t, db, "r4-collab-owner")
	helper := seedAdminAccount(t, db, "r4-collab-helper")
	stranger := seedAdminAccount(t, db, "r4-collab-stranger")

	shared := db.EsportsOrder.Create().SetOrderNo("r4-collab-close").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(100).SetPayableCents(100).SetPromisedServiceUnits(1).
		SetRequiredSeats(1).SetResponsibleAdminID(owner.ID).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	overdue := db.EsportsOrder.Create().SetOrderNo("r4-collab-overdue").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(100).SetPayableCents(100).SetPromisedServiceUnits(1).
		SetRequiredSeats(1).SetResponsibleAdminID(owner.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingPayment).
		SetPaymentStatus(esportsorder.PaymentStatusUnpaid).
		SetPaymentDeadline(now.Add(-time.Hour)).SaveX(ctx)
	for _, orderID := range []int{shared.ID, overdue.ID} {
		db.EsportsOrderCollaborator.Create().SetOrderID(orderID).SetAdminID(helper.ID).
			SetGrantedByAdminID(owner.ID).SaveX(ctx)
	}

	// 协作客服：待结单 / 待付款超时两项都计入。
	if count, err := countOrders(ctx, db, helper.ID, false, esportsorder.FulfillmentStatusPendingClose); err != nil || count != 1 {
		t.Fatalf("协作客服待结单计数应为 1，实际 %d err=%v", count, err)
	}
	todos, err := csWorkbenchTodos(ctx, db, helper.ID, false, now)
	if err != nil {
		t.Fatalf("客服待办计算失败：%v", err)
	}
	counts := map[string]int{}
	for _, item := range todos {
		counts[item.Key] = item.Count
	}
	if counts["pending_close"] != 1 || counts["overdue_payment"] != 1 {
		t.Fatalf("协作订单未纳入待办计数：%+v", counts)
	}
	// 无关客服仍然看不到。
	if count, err := countOrders(ctx, db, stranger.ID, false, esportsorder.FulfillmentStatusPendingClose); err != nil || count != 0 {
		t.Fatalf("无关客服待结单计数应为 0，实际 %d err=%v", count, err)
	}
	if todos, err = csWorkbenchTodos(ctx, db, stranger.ID, false, now); err != nil {
		t.Fatal(err)
	} else {
		for _, item := range todos {
			if item.Count != 0 {
				t.Fatalf("无关客服待办应全为零：%+v", item)
			}
		}
	}
}

// ── ：结单后复购跟进待办 ──

// 结单 N 天后自动生成客户维度复购跟进待办，可被客服工作台看到并可标记完成。
func TestScheduleRepurchaseFollowUps(t *testing.T) {
	db := openCSDB(t, "esports_r4_repurchase")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	owner := seedAdminAccount(t, db, "r4-repurchase-owner")
	customer := db.EsportsCustomer.Create().SetDisplayName("复购客户").SetOwnerAdminID(owner.ID).SaveX(ctx)

	due := db.EsportsOrder.Create().SetOrderNo("r4-repurchase-due").SetCustomerID(customer.ID).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).SetPromisedServiceUnits(1).
		SetRequiredSeats(1).SetResponsibleAdminID(owner.ID).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).
		SetClosedAt(now.AddDate(0, 0, -10)).SaveX(ctx)
	fresh := db.EsportsOrder.Create().SetOrderNo("r4-repurchase-fresh").SetCustomerID(customer.ID).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).SetPromisedServiceUnits(1).
		SetRequiredSeats(1).SetResponsibleAdminID(owner.ID).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).
		SetClosedAt(now.AddDate(0, 0, -1)).SaveX(ctx)
	seedUpgradeConfig(t, db, repurchaseFollowUpDaysKeyLiteral, "7")

	count, err := scheduleRepurchaseFollowUps(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("复购跟进生成任务失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("应只为结单满 7 天的订单生成 1 条复购待办，实际 %d", count)
	}
	markers := db.EsportsCustomerFollowUp.Query().Where(
		esportscustomerfollowup.CustomerIDEQ(customer.ID),
		esportscustomerfollowup.SummaryHasPrefix(repurchaseFollowUpPrefix),
	).AllX(ctx)
	if len(markers) != 1 {
		t.Fatalf("应落 1 条复购跟进记录，实际 %d", len(markers))
	}
	marker := markers[0]
	if marker.AdminID != owner.ID {
		t.Fatalf("复购待办应落给负责客服 %d，实际 %d", owner.ID, marker.AdminID)
	}
	if marker.NextFollowAt == nil || marker.NextFollowAt.After(now) {
		t.Fatalf("复购待办应立即到期（next_follow_at <= now）：%+v", marker.NextFollowAt)
	}
	if !strings.Contains(marker.Summary, due.OrderNo) {
		t.Fatalf("复购待办应可溯源到订单 %s：%q", due.OrderNo, marker.Summary)
	}
	// 未满周期的订单不生成（只生成了 1 条即可证明），且幂等。
	again, err := scheduleRepurchaseFollowUps(ctx, db, now, 100)
	if err != nil || again != 0 {
		t.Fatalf("重复运行不应重复生成，实际 %d err=%v", again, err)
	}
	if n := db.EsportsCustomerFollowUp.Query().Where(
		esportscustomerfollowup.SummaryHasPrefix(repurchaseFollowUpPrefix),
		esportscustomerfollowup.SummaryContains("["+repurchaseOrderToken(fresh.ID)+"]"),
	).CountX(ctx); n != 0 {
		t.Fatalf("结单未满周期不应生成复购待办，实际 %d", n)
	}

	// 工作台待办：复购项 1，且不与「跟进已到期」重复计数。
	todos, err := csWorkbenchTodos(ctx, db, owner.ID, false, now)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, item := range todos {
		counts[item.Key] = item.Count
	}
	if counts["repurchase_due"] != 1 {
		t.Fatalf("复购待办应为 1，实际 %+v", counts)
	}
	if counts["due_follow_up"] != 0 {
		t.Fatalf("复购待办不应重复计入「跟进已到期」，实际 %+v", counts)
	}

	// 标记完成：客服补一条普通跟进（最新记录不再是复购标记）后待办归零。
	if _, err := addCustomerFollowUp(ctx, db, owner.ID, false, customer.ID, followUpInput{
		Summary: "已回访，客户计划下周复购",
	}, now); err != nil {
		t.Fatalf("写跟进记录失败：%v", err)
	}
	todos, err = csWorkbenchTodos(ctx, db, owner.ID, false, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range todos {
		if item.Key == "repurchase_due" && item.Count != 0 {
			t.Fatalf("复购待办应可标记完成（补一条跟进后归零），实际 %d", item.Count)
		}
	}
}

// 关闭复购跟进（配 0）时不生成任何待办。
func TestScheduleRepurchaseFollowUpsDisabled(t *testing.T) {
	db := openCSDB(t, "esports_r4_repurchase_off")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := db.EsportsCustomer.Create().SetDisplayName("关闭复购客户").SetOwnerAdminID(1).SaveX(ctx)
	db.EsportsOrder.Create().SetOrderNo("r4-repurchase-off").SetCustomerID(customer.ID).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).SetPromisedServiceUnits(1).
		SetRequiredSeats(1).SetResponsibleAdminID(1).SetPaymentDeadline(now.Add(time.Hour)).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).
		SetClosedAt(now.AddDate(0, 0, -30)).SaveX(ctx)
	seedUpgradeConfig(t, db, repurchaseFollowUpDaysKeyLiteral, "0")

	count, err := scheduleRepurchaseFollowUps(ctx, db, now, 100)
	if err != nil || count != 0 {
		t.Fatalf("配置为 0 时不应生成复购待办，实际 %d err=%v", count, err)
	}
	if n := db.EsportsCustomerFollowUp.Query().CountX(ctx); n != 0 {
		t.Fatalf("配置为 0 时不应落记录，实际 %d", n)
	}
}

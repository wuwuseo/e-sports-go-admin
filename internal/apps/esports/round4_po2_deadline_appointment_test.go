package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsoutbox"
)

// ── 第 2 波：结单时限配置化与超时待办（2.1-09） ──

// po2SeedPendingCloseOrder 造一张待结单订单，并把 updated_at 拨回指定时长之前。
func po2SeedPendingCloseOrder(t *testing.T, db *ent.Client, orderNo string, adminID int, age time.Duration) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	order := db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(2000).SetPayableCents(2000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).SetResponsibleAdminID(adminID).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	return db.EsportsOrder.UpdateOneID(order.ID).SetUpdatedAt(now.Add(-age)).SaveX(ctx)
}

// §2.1-09：结单时限可配；超时未结单的订单进入待办并可查询，未配置（0）时待办关闭。
func TestRound4W2CloseDeadlineOverdueTodo(t *testing.T) {
	db := openCloseFormDB(t, "esports_po2_close_deadline")
	defer db.Close()
	ctx := t.Context()

	stale := po2SeedPendingCloseOrder(t, db, "po2-close-deadline-1", 11, 3*time.Hour)
	fresh := po2SeedPendingCloseOrder(t, db, "po2-close-deadline-2", 11, 5*time.Minute)

	// 未配置结单时限：待办整体关闭（不伪造超时）。
	items, err := listCloseDeadlineOverdue(ctx, db, time.Now().UTC(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("未配置结单时限时不应有待办：%+v", items)
	}

	poSeedConfig(t, db, closeDeadlineHoursKey, "2")
	items, err = listCloseDeadlineOverdue(ctx, db, time.Now().UTC(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].OrderID != stale.ID {
		t.Fatalf("超时未结单订单未进入待办：%+v", items)
	}
	if items[0].HoursOverdue < 2 {
		t.Fatalf("超时时长计算不正确：%+v", items[0])
	}
	if items[0].ResponsibleAdminID == nil || *items[0].ResponsibleAdminID != 11 {
		t.Fatalf("待办未带负责客服：%+v", items[0])
	}
	if fresh.ID == stale.ID {
		t.Fatal("测试数据错误")
	}

	created, err := scanCloseDeadlineOverdue(ctx, db, time.Now().UTC(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("扫描应新增 1 条待办，实际 %d", created)
	}
	if n := db.EsportsOutbox.Query().
		Where(esportsoutbox.EventTypeEQ("order.close_deadline_overdue")).CountX(ctx); n != 1 {
		t.Fatalf("待办外盒未落库：%d", n)
	}
	// 重复扫描幂等：同业务日不重复建待办。
	again, err := scanCloseDeadlineOverdue(ctx, db, time.Now().UTC(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("重复扫描应幂等，实际新增 %d", again)
	}

	// 非正 / 非法配置回落关闭。
	poSeedConfig(t, db, closeDeadlineHoursKey, "abc")
	if items, err = listCloseDeadlineOverdue(ctx, db, time.Now().UTC(), 100); err != nil || len(items) != 0 {
		t.Fatalf("非法结单时限配置应回落关闭：%+v %v", items, err)
	}
}

// ── 第 2 波：档期确认到期扫描与全额退还 / 改期入口（5.5-06） ──

// po2SeedExpiredAppointmentOrder 造一张已付款、档期确认已超时的预约单。
func po2SeedExpiredAppointmentOrder(t *testing.T, db *ent.Client, code string) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	order := poSeedPaidOrderWithSoldQuota(t, db, code)
	startAt := now.Add(48 * time.Hour)
	deadline := now.Add(-2 * time.Hour)
	return db.EsportsOrder.UpdateOneID(order.ID).
		SetRequestedStartAt(startAt).
		SetAppointmentStatus(esportsorder.AppointmentStatusRequest).
		SetAppointmentConfirmationDeadline(deadline).
		SaveX(ctx)
}

// §5.5-06：到期扫描把已付款预约单置为「待确认档期超时」并进待办；显式提供改期与全额退还两个入口。
func TestRound4W2AppointmentExpiryScanAndDisposal(t *testing.T) {
	db := openProductDB(t, "esports_po2_appointment_expiry")
	defer db.Close()
	poSeedBaseConfigs(t, db)
	ctx := t.Context()
	now := time.Now().UTC()

	order := po2SeedExpiredAppointmentOrder(t, db, "PO2-APPT-1")
	count, err := expireAppointmentConfirmations(ctx, db, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("到期扫描应处置 1 单，实际 %d", count)
	}
	suspended := db.EsportsOrder.GetX(ctx, order.ID)
	if suspended.AppointmentStatus != esportsorder.AppointmentStatusPendingConfirm ||
		suspended.AppointmentSuspendedAt == nil {
		t.Fatalf("订单未置为待确认档期超时：%+v", suspended)
	}
	if n := db.EsportsOrderEvent.Query().
		Where(esportsorderevent.EventTypeEQ("order.appointment_confirmation_expired")).CountX(ctx); n != 1 {
		t.Fatalf("到期事件未落库：%d", n)
	}
	if n := db.EsportsOutbox.Query().
		Where(esportsoutbox.EventTypeEQ("order.appointment_overdue")).CountX(ctx); n != 1 {
		t.Fatalf("客服待办外盒未落库：%d", n)
	}
	if count, err = expireAppointmentConfirmations(ctx, db, now, 100); err != nil || count != 0 {
		t.Fatalf("重复扫描应幂等：%d %v", count, err)
	}
	pending, err := appointmentPendingDisposals(ctx, db, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != order.ID {
		t.Fatalf("待处置列表不正确：%+v", pending)
	}
	if n, err := appointmentPendingDisposalCount(ctx, db); err != nil || n != 1 {
		t.Fatalf("待处置计数不正确：%d %v", n, err)
	}

	// 改期入口：给出新的预约开始时间后回到 request 并恢复可派单。
	newStart := now.Add(72 * time.Hour)
	rescheduled, err := disposeExpiredAppointment(ctx, db, order.ID, 11, appointmentDisposalInput{
		Action: appointmentExpireActionReschedule, Reason: "客户改期", StartAt: &newStart,
		ExpectedVersion: suspended.Version,
	}, now)
	if err != nil {
		t.Fatalf("改期处置失败：%v", err)
	}
	if rescheduled.AppointmentStatus != esportsorder.AppointmentStatusRequest ||
		rescheduled.AppointmentSuspendedAt != nil {
		t.Fatalf("改期后档期状态不正确：%+v", rescheduled)
	}
	if rescheduled.RequestedStartAt == nil || !rescheduled.RequestedStartAt.Equal(newStart) {
		t.Fatalf("改期后预约开始时间不正确：%+v", rescheduled.RequestedStartAt)
	}

	// 全额退还入口：再次到期后按全额退款处置（平台责任、不收手续费）。
	db.EsportsOrder.UpdateOneID(order.ID).
		SetAppointmentStatus(esportsorder.AppointmentStatusRequest).
		SetAppointmentConfirmationDeadline(now.Add(-time.Hour)).
		ClearAppointmentSuspendedAt().SaveX(ctx)
	if count, err = expireAppointmentConfirmations(ctx, db, now, 100); err != nil || count != 1 {
		t.Fatalf("二次到期扫描失败：%d %v", count, err)
	}
	suspended = db.EsportsOrder.GetX(ctx, order.ID)
	refunded, err := disposeExpiredAppointment(ctx, db, order.ID, 11, appointmentDisposalInput{
		Action: appointmentExpireActionRefund, Reason: "档期无法确认",
		ExpectedVersion: suspended.Version,
	}, now)
	if err != nil {
		t.Fatalf("全额退还处置失败：%v", err)
	}
	if refunded.FulfillmentStatus != esportsorder.FulfillmentStatusCancelled ||
		refunded.RefundStatus != esportsorder.RefundStatusPendingApproval {
		t.Fatalf("全额退还后订单状态不正确：%+v", refunded)
	}
	event := db.EsportsOrderEvent.Query().
		Where(esportsorderevent.EventTypeEQ("order.cancelled")).OnlyX(ctx)
	if event.Details["full_refund"] != true {
		t.Fatalf("取消事件未标记全额退还：%+v", event.Details)
	}
	if got, _ := event.Details["refund_cents"].(float64); int64(got) != refunded.PayableCents {
		t.Fatalf("退款金额不是全额：%+v", event.Details)
	}
}

package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
)

// seedUnpaidOrder 建一张待付款订单，用于走改期 → 档期提案 → 确认的完整链路。
func seedUnpaidOrder(t *testing.T, db *ent.Client, orderNo string) *ent.EsportsOrder {
	t.Helper()
	now := time.Now().UTC()
	return db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).SaveX(t.Context())
}

// §3.2 / §5.5 预约档期确认：改期生成带时限的提案，确认后才允许派单。
func TestOrderAppointmentConfirmationGatesDispatch(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_order_appointment")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedPlayer(t, db, 9701, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	order := seedUnpaidOrder(t, db, "appt-1")
	start := now.Add(48 * time.Hour).Truncate(time.Hour)
	proposal, err := rescheduleOrder(ctx, db, order.ID, 11, rescheduleInput{
		ExpectedVersion: order.Version, StartAt: start, Reason: "客户要求改到后天",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.AppointmentStatus != esportsorder.AppointmentStatusRequest ||
		proposal.AppointmentConfirmationDeadline == nil {
		t.Fatalf("改期应生成带确认时限的档期提案：%+v", proposal)
	}

	paid := db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).SaveX(ctx)

	if _, err := assignDispatchSeat(ctx, db, paid.ID, 1, player.ID, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("档期未确认的预约单不应允许派单：%v", err)
	}

	confirmed, err := confirmOrderAppointment(ctx, db, paid.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.AppointmentStatus != esportsorder.AppointmentStatusConfirmed {
		t.Fatalf("档期未转为 confirmed：%+v", confirmed.AppointmentStatus)
	}
	if _, err := confirmOrderAppointment(ctx, db, paid.ID, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复确认应被拦截：%v", err)
	}
	if _, err := assignDispatchSeat(ctx, db, paid.ID, 1, player.ID, 11, now); err != nil {
		t.Fatalf("确认档期后应允许派单：%v", err)
	}
}

// 即时单、超时提案与已过开始时间的提案都不允许确认。
func TestOrderAppointmentConfirmationGuards(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_order_appointment_guard")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	instant := seedPaidOrder(t, db, "appt-instant", 1)
	if _, err := confirmOrderAppointment(ctx, db, instant.ID, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("即时单不应可确认档期：%v", err)
	}

	expired := seedUnpaidOrder(t, db, "appt-expired")
	if _, err := rescheduleOrder(ctx, db, expired.ID, 11, rescheduleInput{
		ExpectedVersion: expired.Version, StartAt: now.Add(48 * time.Hour), Reason: "档期提案",
	}, now); err != nil {
		t.Fatal(err)
	}
	db.EsportsOrder.UpdateOneID(expired.ID).
		SetAppointmentConfirmationDeadline(now.Add(-time.Minute)).SaveX(ctx)
	if _, err := confirmOrderAppointment(ctx, db, expired.ID, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("超时提案不应可确认：%v", err)
	}

	past := seedUnpaidOrder(t, db, "appt-past")
	db.EsportsOrder.UpdateOneID(past.ID).
		SetRequestedStartAt(now.Add(-time.Hour)).
		SetAppointmentStatus(esportsorder.AppointmentStatusRequest).SaveX(ctx)
	if _, err := confirmOrderAppointment(ctx, db, past.ID, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("开始时间已过的提案不应可确认：%v", err)
	}
}

package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsplayer"
)

// mustConfirmGrab 供既有服务链路测试沿用 accepted 语义：抢单成功后立即按订单客户确认。
// ponytail: 只在测试里用，业务代码不得走这条捷径绕过客户确认。
func mustConfirmGrab(t *testing.T, db *ent.Client, orderID int, now time.Time) {
	t.Helper()
	order := db.EsportsOrder.GetX(t.Context(), orderID)
	if _, err := confirmGrabbedPlayerByCustomer(t.Context(), db, order.CustomerID, orderID, now); err != nil {
		t.Fatalf("确认抢单陪玩失败：%v", err)
	}
}

// 抢单成功后进入「待客户确认」：写入确认截止时间，尚未标记可以开工。
func TestGrabEntersPendingCustomerConfirm(t *testing.T) {
	db := openPlayerTestDB(t, "esports_customer_confirm_grab")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	player := seedPlayer(t, db, 1301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "confirm-grab-1")

	grabbed, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if grabbed.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCustomerConfirm {
		t.Fatalf("抢单后应进入待客户确认：%+v", grabbed.FulfillmentStatus)
	}
	if grabbed.CustomerConfirmDeadline == nil {
		t.Fatal("抢单后应写入客户确认截止时间")
	}
	if grabbed.ReadyToStartAt != nil {
		t.Fatal("客户确认前不应标记可以开工")
	}
	if countEventsByType(t, db)["order.grabbed"] != 1 {
		t.Fatal("缺少 order.grabbed 事件")
	}
	rows := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.customer_confirm_required")).AllX(ctx)
	if len(rows) != 1 {
		t.Fatalf("抢单成功应写一条待客户确认通知外盒，got=%d", len(rows))
	}
}

// 客户确认：待客户确认 → 已接单，写入可以开工时间点并留事件。
func TestCustomerConfirmGrabbedPlayer(t *testing.T) {
	db := openPlayerTestDB(t, "esports_customer_confirm_accept")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	player := seedPlayer(t, db, 1302, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "confirm-accept-1")
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}

	confirmed, err := confirmGrabbedPlayerByCustomer(ctx, db, order.CustomerID, order.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.FulfillmentStatus != esportsorder.FulfillmentStatusAccepted {
		t.Fatalf("客户确认后应转已接单：%+v", confirmed.FulfillmentStatus)
	}
	if confirmed.ReadyToStartAt == nil {
		t.Fatal("客户确认后应写入可以开工时间点")
	}
	if countEventsByType(t, db)["order.customer_confirmed_player"] != 1 {
		t.Fatal("缺少 order.customer_confirmed_player 事件")
	}
	if countEventsByType(t, db)["order.ready_to_start"] != 1 {
		t.Fatal("缺少 order.ready_to_start 事件")
	}
	// 重复确认：已完成流转，返回冲突。
	if _, err := confirmGrabbedPlayerByCustomer(ctx, db, order.CustomerID, order.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复确认应返回冲突：%v", err)
	}
}

// 客户更换：原席位回收并重置为 open，订单回抢单大厅后可被他人再次抢下。
func TestCustomerDeclineGrabbedPlayerReleasesToHall(t *testing.T) {
	db := openPlayerTestDB(t, "esports_customer_confirm_decline")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	first := seedPlayer(t, db, 1303, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	second := seedPlayer(t, db, 1304, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "confirm-decline-1")
	if _, err := grabPlayerOrder(ctx, db, first.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}

	declined, err := declineGrabbedPlayerByCustomer(ctx, db, order.CustomerID, order.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if declined.FulfillmentStatus != esportsorder.FulfillmentStatusGrabbing {
		t.Fatalf("客户更换后应回到抢单大厅：%+v", declined.FulfillmentStatus)
	}
	seat := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(order.ID)).OnlyX(ctx)
	if seat.Status != esportsdispatchseat.StatusOpen || seat.PlayerID != nil {
		t.Fatalf("被更换的席位应重置为可抢：%+v", seat)
	}
	if countEventsByType(t, db)["order.customer_declined_player"] != 1 {
		t.Fatal("缺少 order.customer_declined_player 事件")
	}
	// 释放后其他陪玩可以再次抢下。
	if _, err := grabPlayerOrder(ctx, db, second.ID, order.ID, now); err != nil {
		t.Fatalf("释放后应可再次抢单：%v", err)
	}
}

// 非本人订单不可确认 / 更换。
func TestCustomerConfirmChecksOwnership(t *testing.T) {
	db := openPlayerTestDB(t, "esports_customer_confirm_owner")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	player := seedPlayer(t, db, 1305, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "confirm-owner-1")
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := confirmGrabbedPlayerByCustomer(ctx, db, order.CustomerID+1, order.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非本人确认应返回禁止：%v", err)
	}
	if _, err := declineGrabbedPlayerByCustomer(ctx, db, order.CustomerID+1, order.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非本人更换应返回禁止：%v", err)
	}
}

// 超时未确认：定时扫描自动确认并转已接单。
func TestAutoConfirmOverdueCustomerGrabs(t *testing.T) {
	db := openPlayerTestDB(t, "esports_customer_confirm_overdue")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	player := seedPlayer(t, db, 1306, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "confirm-overdue-1")
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	// 把截止时间改到过去，模拟客户超时未响应。
	db.EsportsOrder.UpdateOneID(order.ID).
		SetCustomerConfirmDeadline(now.Add(-time.Minute)).SaveX(ctx)

	count, err := autoConfirmCustomerGrabs(ctx, db, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("应自动确认 1 单，got=%d", count)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusAccepted {
		t.Fatalf("超时后应自动转已接单：%+v", got.FulfillmentStatus)
	}
}

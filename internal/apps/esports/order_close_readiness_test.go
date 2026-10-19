package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsplayer"
)

// P1-02：「待结单」判定改为「全部在册席位都了结」，而不是 finishedCount >= seatCount。
// 3 席中 2 席结束服务、1 席从未开工 → 订单保持服务中；把该席位移除后 → 转待结单。
func TestPendingCloseRequiresAllSeatsSettled(t *testing.T) {
	db := openPlayerTestDB(t, "esports_close_readiness")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	p1 := seedPlayer(t, db, 7301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	p2 := seedPlayer(t, db, 7302, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	p3 := seedPlayer(t, db, 7303, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedPaidOrder(t, db, "close-readiness-1", 3)
	acceptSeat(t, db, order.ID, 1, p1.ID, 11, now)
	acceptSeat(t, db, order.ID, 2, p2.ID, 11, now)
	acceptSeat(t, db, order.ID, 3, p3.ID, 11, now)
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusAccepted {
		t.Fatalf("三席位接满后订单应转已接单：%+v", got)
	}

	// 只有前两席开工，第三席从未开工。
	if _, err := startPlayerService(ctx, db, p1.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, p2.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := finishPlayerService(ctx, db, p1.ID, order.ID, 1, 60, "ev-1.png", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		t.Fatalf("仍有成员在场时不得转待结单：%+v", got)
	}
	if _, err := finishPlayerService(ctx, db, p2.ID, order.ID, 1, 60, "ev-2.png", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// 关键回归：2/3 席位结束但第三席从未开工 → 仍停在服务中，并把未了结席位写进事件。
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		t.Fatalf("有席位从未开工时不得转待结单（P1-02）：%+v", got)
	}
	lastEvent := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.service_finished"),
	).Order(ent.Desc(esportsorderevent.FieldID)).FirstX(ctx)
	unsettled, _ := lastEvent.Details["unsettled_seat_indexes"].([]interface{})
	if len(unsettled) != 1 {
		t.Fatalf("服务结束事件应给出未了结席位，got=%+v", lastEvent.Details)
	}

	// 处置从未开工的席位（客服移除未开工的已接单席位）→ 整单收敛到待结单。
	if _, err := revokeDispatchSeat(ctx, db, order.ID, 3, 11, "该席位从未开工，客服移除"); err != nil {
		t.Fatalf("移除未开工席位应成功，err=%v", err)
	}
	final := db.EsportsOrder.GetX(ctx, order.ID)
	if final.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		t.Fatalf("全部席位了结后应转待结单：%+v", final)
	}
	if n := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ(orderEnteredPendingCloseEvent),
	).CountX(ctx); n != 1 {
		t.Fatalf("应写一条 %s 事件，got=%d", orderEnteredPendingCloseEvent, n)
	}
}

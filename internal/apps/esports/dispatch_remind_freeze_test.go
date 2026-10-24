package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsorderevent"
)

// 存单冻结期间催单必须被拒：催单会改订单字段并抬升版本，属订单写操作。
func TestRemindOrderRejectedWhenDepositFrozen(t *testing.T) {
	db := openDepositDB(t, "esports_remind_frozen_guard")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order, _ := seedPaidOrderWithQuota(t, db, "REMIND-FROZEN-GUARD")
	// 冻结前先成功催单一次，作为「字段与事件都不应再变化」的基线。
	if err := remindOrder(ctx, db, order.ID, 99, 1, "冻结前催单"); err != nil {
		t.Fatalf("冻结前催单应成功：%v", err)
	}
	before := db.EsportsOrder.GetX(ctx, order.ID)
	eventsBefore := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.reminded"),
	).CountX(ctx)

	if _, err := depositOrder(ctx, db, order.ID, 99, now); err != nil {
		t.Fatalf("已付款单应可存单：%v", err)
	}
	frozen := db.EsportsOrder.GetX(ctx, order.ID)

	if err := remindOrder(ctx, db, order.ID, 99, 3, "冻结后催单"); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("存单冻结期间催单应被拒（errOrderFrozen），got=%v", err)
	}
	after := db.EsportsOrder.GetX(ctx, order.ID)
	if after.Version != frozen.Version ||
		after.RemindPriorityLevel != frozen.RemindPriorityLevel ||
		after.RemindNote != frozen.RemindNote {
		t.Fatalf("冻结期间催单不应改订单字段或版本：frozen=%+v after=%+v", frozen, after)
	}
	if after.RemindedAt == nil || before.RemindedAt == nil || !after.RemindedAt.Equal(*before.RemindedAt) {
		t.Fatalf("冻结期间催单不应改写催单时间：before=%v after=%v", before.RemindedAt, after.RemindedAt)
	}
	if eventsAfter := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.reminded"),
	).CountX(ctx); eventsAfter != eventsBefore {
		t.Fatalf("冻结期间催单不应写 order.reminded 事件：before=%d after=%d", eventsBefore, eventsAfter)
	}

	// 恢复存单后催单恢复可用（回归保护）。
	if _, err := restoreOrder(ctx, db, order.ID, 99, now); err != nil {
		t.Fatalf("恢复存单应成功：%v", err)
	}
	if err := remindOrder(ctx, db, order.ID, 99, 2, "恢复后催单"); err != nil {
		t.Fatalf("恢复后催单应成功：%v", err)
	}
	restored := db.EsportsOrder.GetX(ctx, order.ID)
	if restored.RemindPriorityLevel != 2 || restored.RemindNote != "恢复后催单" {
		t.Fatalf("恢复后催单未落到订单字段：%+v", restored)
	}
}

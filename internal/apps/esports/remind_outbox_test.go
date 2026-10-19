package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsplayer"
)

// P1-04：手动催单必须在同一事务内写事务外盒，使被催陪玩与责任客服真的收到站内信；
// 事件键按「订单 + 本次催单版本」幂等，重复催单不会产生重复键。
func TestRemindOrderWritesOutboxForPlayerAndAdmin(t *testing.T) {
	db := openPlayerTestDB(t, "esports_remind_outbox")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	player := seedPlayer(t, db, 7501, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedPaidOrder(t, db, "remind-outbox-1", 1)
	seat := acceptSeat(t, db, order.ID, 1, player.ID, 11, now)
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(11).SaveX(ctx)

	if err := remindOrder(ctx, db, order.ID, 11, 2, "请尽快接单"); err != nil {
		t.Fatalf("催单应成功，err=%v", err)
	}
	rows := db.EsportsOutbox.Query().Where(
		esportsoutbox.AggregateRefEQ(order.OrderNo),
		esportsoutbox.EventTypeIn("dispatch.remind_player", "dispatch.remind_admin"),
	).AllX(ctx)
	if len(rows) != 2 {
		t.Fatalf("催单应在事务外盒写「陪玩 + 客服」两条事件，got=%d", len(rows))
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.EventKey] {
			t.Fatalf("事件键必须唯一，重复键=%s", row.EventKey)
		}
		seen[row.EventKey] = true
		// 收件人必须可解析：陪玩事件定向到本人，客服事件定向到责任客服。
		recipients, err := resolveNotificationRecipients(ctx, db, row.EventType, row.AggregateRef, row.Payload)
		if err != nil {
			t.Fatalf("%s 必须能解析出收件人，err=%v", row.EventType, err)
		}
		switch row.EventType {
		case "dispatch.remind_player":
			if len(recipients) != 1 || recipients[0].PlayerID != player.ID {
				t.Fatalf("催单事件应定向通知被催陪玩：%+v", recipients)
			}
			if payloadInt(row.Payload, "seat_index") != seat.SeatIndex {
				t.Fatalf("催单事件应带席位序号：%+v", row.Payload)
			}
		case "dispatch.remind_admin":
			if len(recipients) != 1 || recipients[0].AdminID != 11 {
				t.Fatalf("催单升级事件应定向通知责任客服：%+v", recipients)
			}
		}
	}

	// 二次催单：版本变化 → 新增两条，且全表事件键仍然唯一。
	if err := remindOrder(ctx, db, order.ID, 11, 3, "客户已等待 30 分钟"); err != nil {
		t.Fatalf("再次催单应成功，err=%v", err)
	}
	all := db.EsportsOutbox.Query().Where(
		esportsoutbox.AggregateRefEQ(order.OrderNo),
		esportsoutbox.EventTypeIn("dispatch.remind_player", "dispatch.remind_admin"),
	).AllX(ctx)
	if len(all) != 4 {
		t.Fatalf("两次催单应有 4 条外盒事件，got=%d", len(all))
	}
	keys := map[string]bool{}
	for _, row := range all {
		if keys[row.EventKey] {
			t.Fatalf("两次催单的事件键不得重复：%s", row.EventKey)
		}
		keys[row.EventKey] = true
	}
	if _, err := db.EsportsOutbox.Create().
		SetEventKey(all[0].EventKey).SetEventType("dispatch.remind_player").
		SetAggregateRef(order.OrderNo).Save(ctx); err == nil {
		t.Fatal("重复事件键必须被唯一索引拦截（幂等）")
	}
}

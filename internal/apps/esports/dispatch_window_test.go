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

// seedScheduledPaidOrder 建一张已确认档期的已付款订单（预约开始时间 + 服务时长快照）。
func seedScheduledPaidOrder(t *testing.T, db *ent.Client, orderNo string, start time.Time, durationMinutes int) *ent.EsportsOrder {
	t.Helper()
	order := seedPaidOrder(t, db, orderNo, 1)
	return db.EsportsOrder.UpdateOneID(order.ID).
		SetRequestedStartAt(start.UTC()).
		SetAppointmentStatus(esportsorder.AppointmentStatusConfirmed).
		SetServiceDurationMinutes(durationMinutes).
		SaveX(t.Context())
}

// §5.5 并发上限按服务时段重叠判定：重叠占容量，不重叠不占（旧口径「未结束即占位」会误挡）。
func TestDispatchConcurrencyUsesServiceWindowOverlap(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_window_overlap")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedPlayer(t, db, 9501, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetConcurrentLimit(1).SaveX(ctx)

	base := now.Add(24 * time.Hour).Truncate(time.Hour)
	first := seedScheduledPaidOrder(t, db, "win-1", base, 60)
	overlapping := seedScheduledPaidOrder(t, db, "win-2", base.Add(30*time.Minute), 60)
	apart := seedScheduledPaidOrder(t, db, "win-3", base.Add(3*time.Hour), 60)

	seat, err := assignDispatchSeat(ctx, db, first.ID, 1, player.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, player.ID, seat.ID, true, now); err != nil {
		t.Fatal(err)
	}

	if _, err := assignDispatchSeat(ctx, db, overlapping.ID, 1, player.ID, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("时段重叠应占用并发容量：%v", err)
	}
	if _, err := assignDispatchSeat(ctx, db, apart.ID, 1, player.ID, 11, now); err != nil {
		t.Fatalf("时段不重叠不应占用并发容量：%v", err)
	}
}

// §5.5 每日上限按「服务开始自然日」归集：不同日期的预约不再互相误挡，同日才累计。
func TestDispatchDailyLimitUsesServiceStartBusinessDay(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_window_daily")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedPlayer(t, db, 9502, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetDailyLimit(1).SaveX(ctx)

	base := now.Add(24 * time.Hour).Truncate(time.Hour)
	tomorrow := seedScheduledPaidOrder(t, db, "day-1", base, 60)
	dayAfter := seedScheduledPaidOrder(t, db, "day-2", base.Add(24*time.Hour), 60)
	sameDayApart := seedScheduledPaidOrder(t, db, "day-3", base.Add(5*time.Hour), 60)

	seat, err := assignDispatchSeat(ctx, db, tomorrow.ID, 1, player.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, player.ID, seat.ID, true, now); err != nil {
		t.Fatal(err)
	}

	// 后天的预约：归集日不同，且时段不重叠 → 旧口径（按接单 UTC 日）会误挡，这里应放行。
	if _, err := assignDispatchSeat(ctx, db, dayAfter.ID, 1, player.ID, 11, now); err != nil {
		t.Fatalf("不同服务开始自然日不应占用每日上限：%v", err)
	}
	// 同一天的另一段档期：归集日相同 → 占用每日上限被拦。
	if _, err := assignDispatchSeat(ctx, db, sameDayApart.ID, 1, player.ID, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("同一服务开始自然日应占用每日上限：%v", err)
	}
}

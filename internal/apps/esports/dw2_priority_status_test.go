package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsdispatchprioritygrant"
	"go-admin/internal/ent/esportsdispatchprioritynotice"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
)

// ：陪玩端必须能查到本人「是否持有优先派单权 / 来源 / 有效期 / 剩余名额」。
// 断言：生效中的名单条目 active=true 且带 quota/used/remaining；未生效（下一期）的条目 active=false；
// 被移出/已过期的条目不再返回；无持权者返回 holding=false 的空列表。
func TestDw2PlayerPriorityStatusQuery(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dw2_priority_status")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"

	holder := seedPlayer(t, db, 8601, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	other := seedPlayer(t, db, 8602, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	bystander := seedPlayer(t, db, 8603, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// 本期生效名单：holder(rank1) + other(rank2)，名额默认 3。
	// 期起点必须用同一份周期窗口（与 grantDispatchPriority 同口径），否则不属于「同期」。
	start, end := dispatchPriorityPeriodWindow("weekly", now)
	nextStart, nextEnd := dispatchPriorityPeriodWindow("weekly", end)
	db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(holder.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(start).SetPeriodEnd(end).
		SetRank(1).SetSource(esportsdispatchprioritygrant.SourceManual).
		SetStatus(esportsdispatchprioritygrant.StatusActive).SetNote("人工指定").SaveX(ctx)
	db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(other.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(start).SetPeriodEnd(end).
		SetRank(2).SetSource(esportsdispatchprioritygrant.SourcePoints).
		SetStatus(esportsdispatchprioritygrant.StatusActive).SaveX(ctx)
	// 下一期预生成名单（pending）：应返回但 active=false。
	db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(holder.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(nextStart).SetPeriodEnd(nextEnd).
		SetRank(1).SetSource(esportsdispatchprioritygrant.SourceRanking).
		SetStatus(esportsdispatchprioritygrant.StatusPending).SaveX(ctx)
	// 已移出 / 已过期：不得出现在状态查询里（(game, period_start, player_id) 唯一，故用不同期）。
	revokedStart, revokedEnd := dispatchPriorityPeriodWindow("daily", now.AddDate(-1, 0, 0))
	db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(holder.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodDaily).
		SetPeriodStart(revokedStart).SetPeriodEnd(revokedEnd).
		SetStatus(esportsdispatchprioritygrant.StatusRevoked).SaveX(ctx)
	expiredStart, expiredEnd := dispatchPriorityPeriodWindow("daily", now.AddDate(-1, 0, -1))
	db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(holder.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodDaily).
		SetPeriodStart(expiredStart).SetPeriodEnd(expiredEnd).
		SetStatus(esportsdispatchprioritygrant.StatusExpired).SaveX(ctx)

	view, err := playerPriorityStatus(ctx, db, holder.ID, now)
	if err != nil {
		t.Fatalf("优先派单权状态查询应成功：%v", err)
	}
	if !view.Holding || len(view.Holdings) != 2 {
		t.Fatalf("应返回本期生效 + 下一期两条持权记录，got=%+v", view)
	}
	active := view.Holdings[0]
	if !active.Active || active.Rank != 1 || active.Source != "manual" || active.Game != game {
		t.Fatalf("生效中条目字段不正确：%+v", active)
	}
	if active.Quota != dispatchPriorityDefaultQuota || active.Used != 2 ||
		active.Remaining != dispatchPriorityDefaultQuota-2 {
		t.Fatalf("剩余名额口径不正确：%+v", active)
	}
	if !active.PeriodStart.Before(active.PeriodEnd) || active.PeriodEnd.IsZero() {
		t.Fatalf("有效期必须随响应下发：%+v", active)
	}
	if view.Holdings[1].Active {
		t.Fatalf("下一期预生成名单不应算生效：%+v", view.Holdings[1])
	}
	for _, item := range view.Holdings {
		if item.Status != "active" && item.Status != "pending" {
			t.Fatalf("移出/过期条目不得返回：%+v", item)
		}
	}
	if len(view.Games) != 1 || view.Games[0].Remaining != dispatchPriorityDefaultQuota-2 {
		t.Fatalf("应按游戏汇总名额：%+v", view.Games)
	}

	// 无持权者的空态：holding=false，不是报错。
	empty, err := playerPriorityStatus(ctx, db, bystander.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Holding || len(empty.Holdings) != 0 || len(empty.Games) != 0 {
		t.Fatalf("无持权者应返回空态：%+v", empty)
	}
	if _, err := playerPriorityStatus(ctx, db, 0, now); err == nil {
		t.Fatal("非法陪玩 ID 应报参数错误")
	}
}

// ：优先通知列表必须下发窗口截止时间（notice_until / priority_notice_until 同值）。
func TestDw2PlayerDispatchNoticeCarriesDeadline(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dw2_notice_deadline")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	player := seedPlayer(t, db, 8701, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	deadline := now.Add(3 * time.Minute).UTC()
	order := db.EsportsOrder.Create().SetOrderNo("DW2-ND-1").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SetPriorityNoticeStatus(esportsorder.PriorityNoticeStatusPending).
		SetPriorityNoticeUntil(deadline).SetPriorityGame("王者荣耀").
		SaveX(ctx)
	db.EsportsDispatchPriorityNotice.Create().SetOrderID(order.ID).SetPlayerID(player.ID).
		SetGame("王者荣耀").SetStatus(esportsdispatchprioritynotice.StatusNotified).
		SetNotifiedAt(now.UTC()).SaveX(ctx)

	items, err := listPlayerDispatchNotices(ctx, db, player.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("应返回 1 条待回应通知，got=%d", len(items))
	}
	if items[0].NoticeUntil == nil || !items[0].NoticeUntil.Equal(deadline) {
		t.Fatalf("通知必须下发窗口截止时间：%+v", items[0])
	}
	if items[0].PriorityNoticeUntil == nil || !items[0].PriorityNoticeUntil.Equal(deadline) {
		t.Fatalf("通知必须下发 priority_notice_until（与 notice_until 同值）：%+v", items[0])
	}
}

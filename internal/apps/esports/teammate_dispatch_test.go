package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
)

// 固排成队（§5.3）：主陪玩 + 固排一次成队，候选只含合格固排，席位来源记 fixed_team。
func TestTeamDispatch(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_team_dispatch")
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"

	sku := seedGameSku(t, db, "TD", game)
	order := db.EsportsOrder.Create().SetOrderNo("TD-1").SetCustomerID(1).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(3).
		SetPaymentDeadline(now.UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(ctx)

	main := seedPlayer(t, db, 8001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	t1 := seedPlayer(t, db, 8002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	t2 := seedPlayer(t, db, 8003, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	outsider := seedPlayer(t, db, 8004, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// main 与 t1、t2 建立固排；outsider 不是固排。
	mustConfirmTeammate := func(initiator, recipient *ent.EsportsPlayer, key string) {
		t.Helper()
		rel, err := requestTeammate(ctx, db, initiator.ID, recipient.ID, game, key, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := acceptTeammateRequest(ctx, db, recipient.ID, rel.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	mustConfirmTeammate(main, t1, "td-rel-0001")
	mustConfirmTeammate(main, t2, "td-rel-0002")

	// 候选：只含 t1、t2，不含 outsider。
	cands, err := listTeammateCandidatesForOrder(ctx, db, order.ID, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int]bool{}
	for _, c := range cands {
		ids[c.PlayerID] = true
	}
	if len(cands) != 2 || !ids[t1.ID] || !ids[t2.ID] || ids[outsider.ID] {
		t.Fatalf("固排候选不正确：%+v", cands)
	}

	// 成队：main + t1 + t2 占满 3 席。
	seats, err := teamDispatch(ctx, db, order.ID, main.ID, []int{t1.ID, t2.ID}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(seats) != 3 {
		t.Fatalf("应有 3 席，got=%d", len(seats))
	}
	for _, s := range seats {
		if s.Status != esportsdispatchseat.StatusInvited || s.Source != esportsdispatchseat.SourceFixedTeam {
			t.Fatalf("席位状态/来源不正确：%+v", s)
		}
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance {
		t.Fatalf("成队后订单应转待接单：%v", got.FulfillmentStatus)
	}
}

// 成队必须校验固排关系与硬性条件：非固排、席位不足、主陪玩不合格都被拒。
func TestTeamDispatchGuards(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_team_dispatch_guards")
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"

	sku := seedGameSku(t, db, "TDG", game)
	mkOrder := func(orderNo string, seats int) *ent.EsportsOrder {
		return db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(sku.ID).
			SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
			SetPromisedServiceUnits(1).SetRequiredSeats(seats).
			SetPaymentDeadline(now.UTC().Add(time.Hour)).
			SetPaymentStatus(esportsorder.PaymentStatusPaid).
			SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
			SaveX(ctx)
	}

	main := seedPlayer(t, db, 8101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	teammate := seedPlayer(t, db, 8102, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	notTeammate := seedPlayer(t, db, 8103, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	offline := seedPlayer(t, db, 8104, esportsplayer.QualificationQualified, esportsplayer.PresenceOffline)

	// main 与 teammate 建立固排。
	rel, err := requestTeammate(ctx, db, main.ID, teammate.ID, game, "tdg-rel-0001", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptTeammateRequest(ctx, db, teammate.ID, rel.ID, now); err != nil {
		t.Fatal(err)
	}

	// 非固排：拒绝。
	order := mkOrder("TDG-1", 2)
	if _, err := teamDispatch(ctx, db, order.ID, main.ID, []int{notTeammate.ID}, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非固排成队未拦截：%v", err)
	}
	// 离线固排：拒绝。
	if _, err := teamDispatch(ctx, db, order.ID, main.ID, []int{offline.ID}, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非固排（离线）成队未拦截：%v", err)
	}
	// 席位不足（2 人但只有 1 席）：拒绝。
	order1 := mkOrder("TDG-2", 1)
	if _, err := teamDispatch(ctx, db, order1.ID, main.ID, []int{teammate.ID}, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("席位不足成队未拦截：%v", err)
	}
	// 正常：1 主 + 1 固排占满 2 席。
	order2 := mkOrder("TDG-3", 2)
	if _, err := teamDispatch(ctx, db, order2.ID, main.ID, []int{teammate.ID}, 11, now); err != nil {
		t.Fatalf("正常成队失败：%v", err)
	}
}

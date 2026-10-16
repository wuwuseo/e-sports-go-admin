package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
)

// 陪玩端主动邀请固排（§5.3）：主陪玩就本人已接单的订单邀请固排占剩余席位，非成员 / 非固排被拒。
func TestPlayerInviteTeammatesOnOwnOrder(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_player_team_invite")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	game := "王者荣耀"

	sku := seedGameSku(t, db, "PTI", game)
	order := db.EsportsOrder.Create().SetOrderNo("PTI-1").SetCustomerID(1).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(3).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(ctx)

	main := seedPlayer(t, db, 4701, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	mate := seedPlayer(t, db, 4702, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	offlineMate := seedPlayer(t, db, 4703, esportsplayer.QualificationQualified, esportsplayer.PresenceOffline)
	outsider := seedPlayer(t, db, 4704, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	rel, err := requestTeammate(ctx, db, main.ID, mate.ID, game, "pti-rel-0001", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptTeammateRequest(ctx, db, mate.ID, rel.ID, now); err != nil {
		t.Fatal(err)
	}
	offlineRel, err := requestTeammate(ctx, db, offlineMate.ID, main.ID, game, "pti-rel-0002", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptTeammateRequest(ctx, db, main.ID, offlineRel.ID, now); err != nil {
		t.Fatal(err)
	}

	// 主陪玩尚未接单时不能以「主陪玩」身份邀请。
	if _, err := playerInviteTeammates(ctx, db, main.ID, order.ID, []int{mate.ID}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("未接单陪玩发起邀请未被拦截：%v", err)
	}
	firstSeat, err := assignDispatchSeat(ctx, db, order.ID, 1, main.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, main.ID, firstSeat.ID, true, now); err != nil {
		t.Fatal(err)
	}

	// 候选只含合格且在线的固排。
	candidates, err := playerTeammateCandidates(ctx, db, main.ID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].PlayerID != mate.ID {
		t.Fatalf("固排候选不正确：%+v", candidates)
	}
	if _, err := playerTeammateCandidates(ctx, db, outsider.ID, order.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("非本单成员查看候选未被拦截：%v", err)
	}

	// 非固排、重复 ID、离线固排都被拒。
	for _, ids := range [][]int{{outsider.ID}, {mate.ID, mate.ID}, {offlineMate.ID}} {
		if _, err := playerInviteTeammates(ctx, db, main.ID, order.ID, ids, now); !errors.Is(err, errInvalidInput) && !errors.Is(err, errForbidden) {
			t.Fatalf("非法固排邀请未被拦截：%v", err)
		}
	}
	if _, err := playerInviteTeammates(ctx, db, main.ID, order.ID, []int{}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("空固排列表未被拦截：%v", err)
	}

	seats, err := playerInviteTeammates(ctx, db, main.ID, order.ID, []int{mate.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(seats) != 3 {
		t.Fatalf("席位数量不正确：%d", len(seats))
	}
	invited := seats[1]
	if invited.Status != esportsdispatchseat.StatusInvited ||
		invited.PlayerID == nil || *invited.PlayerID != mate.ID ||
		invited.Source != esportsdispatchseat.SourceFixedTeam ||
		invited.InvitedByAdminID != nil || invited.ExpiresAt == nil {
		t.Fatalf("陪玩发起的席位记录不正确：%+v", invited)
	}
	if seats[0].Status != esportsdispatchseat.StatusAccepted || seats[2].Status != esportsdispatchseat.StatusOpen {
		t.Fatalf("既有席位被误改：%+v", seats)
	}
	if countEventsByType(t, db)["order.team_invited_by_player"] != 1 {
		t.Fatal("缺少 order.team_invited_by_player 事件")
	}
	// 剩余席位不足时再次邀请被拒（mate 已占用，仅剩 1 席）。
	outsiderRel, err := requestTeammate(ctx, db, main.ID, outsider.ID, game, "pti-rel-0003", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptTeammateRequest(ctx, db, outsider.ID, outsiderRel.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := playerInviteTeammates(ctx, db, main.ID, order.ID,
		[]int{mate.ID, outsider.ID}, now); !errors.Is(err, errConflict) {
		t.Fatalf("已占位固排被重复邀请未被拦截：%v", err)
	}
}

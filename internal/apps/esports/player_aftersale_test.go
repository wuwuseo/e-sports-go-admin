package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsplayer"
)

// 陪玩服务售后申诉：仅本单服务成员可发起，落 channel=player 与来源陪玩，同问题查重。
func TestPlayerOrderAfterSaleRequiresServiceMembership(t *testing.T) {
	db := openAfterSaleDB(t, "esports_player_after_sale")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	order := seedBalancePaidOrder(t, db, "player-as")
	player := seedPlayer(t, db, 901, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	stranger := seedPlayer(t, db, 902, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	in := playerAfterSaleInput{OrderID: order.ID, Kind: "service_mismatch", Demand: "客户临时加需求导致超时"}
	if _, err := createPlayerOrderAfterSale(ctx, db, stranger.ID, in, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非本单成员发起售后未拦截：%v", err)
	}
	// 成为本单服务成员（席位记录）后允许发起。
	db.EsportsDispatchSeat.Create().SetOrderID(order.ID).SetSeatIndex(1).SetPlayerID(player.ID).
		SetStatus(esportsdispatchseat.StatusAccepted).SaveX(ctx)
	item, err := createPlayerOrderAfterSale(ctx, db, player.ID, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if item.Channel != esportsaftersale.ChannelPlayer || item.SourceType != esportsaftersale.SourceTypeOrder ||
		item.PlayerID == nil || *item.PlayerID != player.ID ||
		item.CustomerID == nil || *item.CustomerID != order.CustomerID {
		t.Fatalf("陪玩申诉售后单来源记录不完整：%+v", item)
	}
	if item.Status != esportsaftersale.StatusPendingAccept {
		t.Fatalf("陪玩申诉售后单初始状态不正确：%+v", item)
	}
	if _, err := createPlayerOrderAfterSale(ctx, db, player.ID, in, now); !errors.Is(err, errConflict) {
		t.Fatalf("同问题重复申诉未拦截：%v", err)
	}
	// 退款类口径不向陪玩开放。
	if _, err := createPlayerOrderAfterSale(ctx, db, player.ID,
		playerAfterSaleInput{OrderID: order.ID, Kind: "refund_request", Demand: "给我退钱"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("陪玩越权发起退款类售后未拦截：%v", err)
	}
	rows, err := listPlayerOrderAfterSales(ctx, db, player.ID, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != item.ID {
		t.Fatalf("陪玩端售后列表不正确：%+v %v", rows, err)
	}
}

// 严重服务时长差异在服务结束时自动建立售后单，来源陪玩为该成员，客服可在统一状态机内处置。
func TestSevereServiceDurationMismatchAutoCreatesAfterSale(t *testing.T) {
	db := openAfterSaleDB(t, "esports_duration_auto_ticket")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	player := seedPlayer(t, db, 911, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedPaidOrder(t, db, "duration-auto", 1)
	seat, err := assignDispatchSeat(ctx, db, order.ID, 1, player.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, player.ID, seat.ID, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	// 系统计时 60 秒、陪玩上报 3600 秒：差异 3540 秒远超默认严重阈值 30 分钟。
	if _, err := finishPlayerService(ctx, db, player.ID, order.ID, 1, 3600, "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	tickets := db.EsportsAfterSale.Query().AllX(ctx)
	if len(tickets) != 1 || tickets[0].Kind != esportsaftersale.KindDurationShort ||
		tickets[0].Channel != esportsaftersale.ChannelSystem ||
		tickets[0].PlayerID == nil || *tickets[0].PlayerID != player.ID {
		t.Fatalf("严重时长差异未自动建立售后单：%+v", tickets)
	}
	if tickets[0].Status != esportsaftersale.StatusPendingAccept {
		t.Fatalf("自动售后单初始状态不正确：%+v", tickets[0])
	}
	// 差异在普通阈值内时不自动建单。
	other := seedPlayer(t, db, 912, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	quietOrder := seedPaidOrder(t, db, "duration-quiet", 1)
	quietSeat, err := assignDispatchSeat(ctx, db, quietOrder.ID, 1, other.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, other.ID, quietSeat.ID, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, other.ID, quietOrder.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := finishPlayerService(ctx, db, other.ID, quietOrder.ID, 1, 70, "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if count := db.EsportsAfterSale.Query().Where(
		esportsaftersale.SourceIDEQ(quietOrder.ID),
	).CountX(ctx); count != 0 {
		t.Fatalf("轻微差异不应自动建售后单：%d", count)
	}
}

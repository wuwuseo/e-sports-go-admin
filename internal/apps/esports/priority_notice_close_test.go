package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsdispatchprioritynotice"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsplayer"
)

// P1-05-01：人工指派确定陪玩后，优先通知窗口必须立即闭合（状态置结束、不再计入看板「通知中」）。
func TestAssignClosesPriorityNoticeWindow(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_priority_close_assign")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "P1闭合分区"

	listed := seedPlayer(t, db, 7601, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	other := seedPlayer(t, db, 7602, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "P1-CLOSE-SKU", game)
	if _, err := grantDispatchPriority(ctx, db, 7, basePriorityInput(game, listed.ID), now); err != nil {
		t.Fatal(err)
	}
	order := seedPendingOrderWithSku(t, db, "P1-CLOSE-1", sku.ID)
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = onOrderEnteredPendingDispatch(ctx, tx, order.ID, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); !dispatchPriorityWindowOpen(got, now) {
		t.Fatalf("进入待派单应先开窗：%+v", got)
	}

	// 人工指派（名单外的陪玩也可以，窗口只影响可见性）。
	if _, err = assignDispatchSeat(ctx, db, order.ID, 1, other.ID, 11, now); err != nil {
		t.Fatalf("人工指派应成功，err=%v", err)
	}
	closed := db.EsportsOrder.GetX(ctx, order.ID)
	if closed.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusReleased {
		t.Fatalf("指派后优先通知窗口必须闭合（released），got=%s", closed.PriorityNoticeStatus)
	}
	if closed.PriorityReleaseReason != dispatchReleaseReasonAssigned || closed.PriorityReleasedAt == nil {
		t.Fatalf("闭合必须写原因与时间：%+v", closed)
	}
	if dispatchPriorityWindowOpen(closed, now) {
		t.Fatal("闭合后不得再处于「优先通知中」")
	}
	if n := db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID),
		esportsdispatchprioritynotice.StatusEQ(esportsdispatchprioritynotice.StatusNotified),
	).CountX(ctx); n != 0 {
		t.Fatalf("闭合后不得再有待回应通知，got=%d", n)
	}
	// 看板「通知中」判定依据窗口：闭合后该单不在通知中。
	board, err := loadDispatchBoard(ctx, db, 11, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range board.PendingDispatch {
		if item.ID == order.ID && dispatchPriorityWindowOpen(item, now) {
			t.Fatalf("看板不得把已指派的订单显示为「通知中」：%+v", item)
		}
	}
	// 留痕：窗口闭合事件。
	if n := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.priority_notice_closed"),
	).CountX(ctx); n != 1 {
		t.Fatalf("应写一条 order.priority_notice_closed 事件，got=%d", n)
	}
}

// P1-05-02：名单 game 找不到对应游戏分区时必须显式报错（不得静默落库后静默不开窗）。
func TestPriorityGrantRejectsUnknownGame(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_priority_close_unknown_game")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	player := seedPlayer(t, db, 7611, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	_, err := grantDispatchPriority(ctx, db, 7, basePriorityInput("填错的分区", player.ID), now)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("game 填错应返回参数错误，err=%v", err)
	}
	if !strings.Contains(err.Error(), "填错的分区") {
		t.Fatalf("错误提示必须指出名单项 game，err=%v", err)
	}

	// 建好对应的启用游戏分区后即可录入。
	if _, err = saveCategory(ctx, db, categoryInput{
		Code: "CAT-P1-GAME", Name: "填错的分区分类", Game: "填错的分区", Status: "enabled",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = grantDispatchPriority(ctx, db, 7, basePriorityInput("填错的分区", player.ID), now); err != nil {
		t.Fatalf("分区存在时应可录入名单，err=%v", err)
	}
}

// P1-05-03：多席订单不开优先通知窗口（抢单只接受单席，开窗必失败）。
func TestMultiSeatOrderOpensNoPriorityWindow(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_priority_close_multiseat")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "P1多席分区"

	listed := seedPlayer(t, db, 7621, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "P1-MULTI-SKU", game)
	if _, err := grantDispatchPriority(ctx, db, 7, basePriorityInput(game, listed.ID), now); err != nil {
		t.Fatal(err)
	}
	order := seedPendingOrderWithSku(t, db, "P1-MULTI-1", sku.ID)
	db.EsportsOrder.UpdateOneID(order.ID).SetRequiredSeats(2).SaveX(ctx)

	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = onOrderEnteredPendingDispatch(ctx, tx, order.ID, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusNone ||
		got.PriorityNoticeUntil != nil {
		t.Fatalf("多席订单不得开优先通知窗口：%+v", got)
	}
	if n := db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID)).CountX(ctx); n != 0 {
		t.Fatalf("多席订单不得产生优先通知，got=%d", n)
	}
}

// P1-05-01（固排成队路径）：历史上已开窗的订单一旦固排成队确定陪玩，窗口必须立即闭合。
func TestTeamDispatchClosesLegacyPriorityWindow(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_priority_close_team")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "P1固排分区"

	sku := seedGameSku(t, db, "P1-TEAM-SKU", game)
	main := seedPlayer(t, db, 7631, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	teammate := seedPlayer(t, db, 7632, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	rel, err := requestTeammate(ctx, db, main.ID, teammate.ID, game, "p1-team-rel-0001", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptTeammateRequest(ctx, db, teammate.ID, rel.ID, now); err != nil {
		t.Fatal(err)
	}
	order := db.EsportsOrder.Create().SetOrderNo("P1-TEAM-1").SetCustomerID(1).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(2000).
		SetPromisedServiceUnits(2).SetRequiredSeats(2).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).SaveX(ctx)
	// 模拟历史遗留：多席订单此前已开窗（新单不再开窗，见 TestMultiSeatOrderOpensNoPriorityWindow）。
	openPriorityWindow(t, db, order, game, []int{main.ID}, now.Add(3*time.Minute))

	if _, err = teamDispatch(ctx, db, order.ID, main.ID, []int{teammate.ID}, 11, now); err != nil {
		t.Fatalf("固排成队应成功，err=%v", err)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusReleased ||
		got.PriorityReleaseReason != dispatchReleaseReasonAssigned {
		t.Fatalf("固排成队后必须闭合优先通知窗口：%+v", got)
	}
	if dispatchPriorityWindowOpen(got, now) {
		t.Fatal("成队后不得再处于「优先通知中」")
	}
}

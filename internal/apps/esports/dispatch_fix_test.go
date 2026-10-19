package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsdispatchprioritynotice"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsseattransferrequest"
	"go-admin/internal/ent/migrate"
)

// 任务 D 回归测试：席位死锁、优先通知释放、转单权限与开窗过滤。

func openDispatchFixDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// 单席订单被拒后席位回到 open，可再次指派；再次拒绝后也能发布到大厅并被抢单。
func TestDispatchRejectedSeatReopensAndCanBeReassigned(t *testing.T) {
	db := openDispatchFixDB(t, "esports_dispatch_fix_reject_reopen")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := seedPaidOrder(t, db, "D-FIX-REJECT-1", 1)
	first := seedPlayer(t, db, 8101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	second := seedPlayer(t, db, 8102, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	third := seedPlayer(t, db, 8103, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	seat, err := assignDispatchSeat(ctx, db, order.ID, 1, first.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = respondPlayerInvite(ctx, db, first.ID, seat.ID, false, now); err != nil {
		t.Fatal(err)
	}
	reopened := db.EsportsDispatchSeat.GetX(ctx, seat.ID)
	if reopened.Status != esportsdispatchseat.StatusOpen || reopened.PlayerID != nil ||
		reopened.ExpiresAt != nil || reopened.RespondedAt != nil {
		t.Fatalf("单席被拒后应重置为可派单席位：%+v", reopened)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("单席被拒后订单应回退待派单，got=%s", got.FulfillmentStatus)
	}

	// 同一席位可再次指派。
	if _, err = assignDispatchSeat(ctx, db, order.ID, 1, second.ID, 11, now); err != nil {
		t.Fatalf("被拒席位应可再次指派，err=%v", err)
	}
	if err = respondPlayerInvite(ctx, db, second.ID, seat.ID, false, now); err != nil {
		t.Fatal(err)
	}

	// 再次被拒后发布到大厅，仍应存在可抢席位。
	if _, err = publishOrderToGrabHall(ctx, db, order.ID, 11, "客服发布", now); err != nil {
		t.Fatalf("被拒订单应可发布到大厅，err=%v", err)
	}
	if _, err = grabPlayerOrder(ctx, db, third.ID, order.ID, now); err != nil {
		t.Fatalf("发布到大厅后应可抢单，err=%v", err)
	}
	grabbed := db.EsportsDispatchSeat.GetX(ctx, seat.ID)
	if grabbed.Status != esportsdispatchseat.StatusAccepted || grabbed.PlayerID == nil ||
		*grabbed.PlayerID != third.ID {
		t.Fatalf("抢单后席位应归属抢单陪玩：%+v", grabbed)
	}
	if countEventsByType(t, db)["order.seat_rejected"] != 2 {
		t.Fatal("应保留两次拒单事件作为历史")
	}
}

// 多席订单一席被拒后，其余席位接满应能把订单推进 accepted；被拒席位不再阻塞。
func TestDispatchMultiSeatIgnoresRejectedSeatForAcceptance(t *testing.T) {
	db := openDispatchFixDB(t, "esports_dispatch_fix_multi_reject")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := seedPaidOrder(t, db, "D-FIX-MULTI-1", 2)
	first := seedPlayer(t, db, 8201, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	second := seedPlayer(t, db, 8202, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	firstSeat, err := assignDispatchSeat(ctx, db, order.ID, 1, first.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	secondSeat, err := assignDispatchSeat(ctx, db, order.ID, 2, second.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = respondPlayerInvite(ctx, db, first.ID, firstSeat.ID, false, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsDispatchSeat.GetX(ctx, firstSeat.ID); got.Status != esportsdispatchseat.StatusRejected {
		t.Fatalf("多席部分拒绝时应保留 rejected 状态作为历史：%+v", got)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance {
		t.Fatalf("还有队友占位时订单应留在待接单，got=%s", got.FulfillmentStatus)
	}

	if err = respondPlayerInvite(ctx, db, second.ID, secondSeat.ID, true, now); err != nil {
		t.Fatal(err)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.FulfillmentStatus != esportsorder.FulfillmentStatusAccepted {
		t.Fatalf("其余席位接满后订单应转已接单，got=%s", got.FulfillmentStatus)
	}
	if got := db.EsportsDispatchSeat.GetX(ctx, secondSeat.ID); got.Status != esportsdispatchseat.StatusAccepted {
		t.Fatalf("第二个席位应为 accepted，got=%s", got.Status)
	}
	if countEventsByType(t, db)["order.seat_rejected"] != 1 {
		t.Fatal("应保留拒单事件作为历史")
	}
}

// 优先通知名单内接单（窗口 accepted）后，陪玩转单并经负责客服确认应成功，
// 订单释放回大厅且窗口从 accepted 正常转 released，而不是冲突失败。
func TestSeatTransferSucceedsAfterPriorityNoticeAccepted(t *testing.T) {
	db := openDispatchFixDB(t, "esports_dispatch_fix_transfer_priority")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	game := "王者荣耀"

	listed := seedPlayer(t, db, 8301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	// P1-05-02：名单 game 必须能对上启用中的商品分类游戏分区，先建分类再录名单。
	sku := seedGameSku(t, db, "D-FIX-TR-PRIO", game)
	if _, err := grantDispatchPriority(ctx, db, 7, basePriorityInput(game, listed.ID), now); err != nil {
		t.Fatal(err)
	}
	order := seedPendingOrderWithSku(t, db, "D-FIX-TR-PRIO-1", sku.ID)
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(11).SaveX(ctx)

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
	if _, err = grabPlayerOrder(ctx, db, listed.ID, order.ID, now); err != nil {
		t.Fatalf("名单内陪玩应可接单，err=%v", err)
	}
	accepted := db.EsportsOrder.GetX(ctx, order.ID)
	if accepted.FulfillmentStatus != esportsorder.FulfillmentStatusAccepted ||
		accepted.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusAccepted {
		t.Fatalf("接单后订单状态不正确：%+v", accepted)
	}

	item, err := requestSeatTransfer(ctx, db, listed.ID, order.ID, seatTransferInput{
		Reason: "临时有事", IdempotencyKey: "d-fix-transfer-priority-key",
	}, now)
	if err != nil {
		t.Fatalf("发起转单应成功，err=%v", err)
	}
	decided, err := decideSeatTransfer(ctx, db, 11, item.ID, true, "", now)
	if err != nil {
		t.Fatalf("优先通知接单后转单应成功，err=%v", err)
	}
	if decided.Status != esportsseattransferrequest.StatusApproved {
		t.Fatalf("转单应已通过：%+v", decided)
	}
	released := db.EsportsOrder.GetX(ctx, order.ID)
	if released.FulfillmentStatus != esportsorder.FulfillmentStatusGrabbing ||
		released.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusReleased {
		t.Fatalf("转单释放后订单应回到大厅且窗口 released：%+v", released)
	}
	if got := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(order.ID)).OnlyX(ctx).Status; got != esportsdispatchseat.StatusOpen {
		t.Fatalf("转单通过后席位应回到 open，got=%s", got)
	}
}

// 非订单负责人且非超管不能确认他人订单的转单；超级管理员可以。
func TestSeatTransferOtherAdminForbidden(t *testing.T) {
	db := openDispatchFixDB(t, "esports_dispatch_fix_transfer_owner")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	order := seedPaidOrder(t, db, "D-FIX-TR-OWNER-1", 1)
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(11).SaveX(ctx)
	player := seedPlayer(t, db, 8401, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seat := acceptSeat(t, db, order.ID, 1, player.ID, 11, now)

	item, err := requestSeatTransfer(ctx, db, player.ID, order.ID, seatTransferInput{
		Reason: "有事", IdempotencyKey: "d-fix-transfer-owner-key",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decideSeatTransfer(ctx, db, 22, item.ID, true, "", now); !errors.Is(err, errForbidden) {
		t.Fatalf("非负责人确认他人订单转单应 403，err=%v", err)
	}
	if got := db.EsportsSeatTransferRequest.GetX(ctx, item.ID).Status; got != esportsseattransferrequest.StatusPending {
		t.Fatalf("被拒后申请应保持待确认，got=%s", got)
	}

	super, err := db.Admin.Create().
		SetEmail("d-fix-super@example.com").SetPassword("test").
		SetUID("d-fix-super").SetUsername("d-fix-super").SetStatus("active").
		SetIsSuperAdmin(true).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	decided, err := decideSeatTransfer(ctx, db, super.ID, item.ID, true, "", now)
	if err != nil {
		t.Fatalf("超级管理员应可确认他人订单转单，err=%v", err)
	}
	if decided.DecidedByAdminID == nil || *decided.DecidedByAdminID != super.ID {
		t.Fatalf("确认人留痕不正确：%+v", decided)
	}
	if got := db.EsportsDispatchSeat.GetX(ctx, seat.ID).Status; got != esportsdispatchseat.StatusOpen {
		t.Fatalf("通过后席位应回到 open，got=%s", got)
	}
}

// 优先通知开窗前过滤不可接单者：离线、接单上限已满的名单成员不产生通知；
// 全部不可接时不开窗，订单留在常规待派单流程。
func TestPriorityNoticeFiltersUnavailablePlayers(t *testing.T) {
	db := openDispatchFixDB(t, "esports_dispatch_fix_notice_filter")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	// 场景一：名单内只有离线陪玩 → 不开窗、不发通知。
	gameOnlyOffline := "离线分区"
	offline := seedPlayer(t, db, 8501, esportsplayer.QualificationQualified, esportsplayer.PresenceOffline)
	// P1-05-02：名单 game 必须能对上启用中的游戏分区（商品分类），先建分类再录名单。
	offlineSku := seedGameSku(t, db, "D-FIX-NOTICE-OFF", gameOnlyOffline)
	if _, err := grantDispatchPriority(ctx, db, 7, basePriorityInput(gameOnlyOffline, offline.ID), now); err != nil {
		t.Fatal(err)
	}
	offlineOrder := seedPendingOrderWithSku(t, db, "D-FIX-NOTICE-OFF-1", offlineSku.ID)
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = onOrderEnteredPendingDispatch(ctx, tx, offlineOrder.ID, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, offlineOrder.ID); got.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusNone {
		t.Fatalf("全部不可接时不应开窗：%+v", got)
	}
	if n := db.EsportsDispatchPriorityNotice.Query().
		Where(esportsdispatchprioritynotice.OrderIDEQ(offlineOrder.ID)).CountX(ctx); n != 0 {
		t.Fatalf("全部不可接时不应产生通知，got=%d", n)
	}

	// 场景二：名单内离线 + 在线 + 容量已满 → 只通知在线且有容量的人。
	game := "混合分区"
	online := seedPlayer(t, db, 8502, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	capped := seedPlayer(t, db, 8503, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(capped.ID).SetConcurrentLimit(1).SaveX(ctx)
	// 让 capped 在另一张订单上已占一个 accepted 席位，容量用满。
	otherOrder := seedPaidOrder(t, db, "D-FIX-NOTICE-CAP-1", 1)
	acceptSeat(t, db, otherOrder.ID, 1, capped.ID, 11, now)
	// P1-05-02：先建该分区的商品分类（seedGameSku 会建分类），再录名单。
	sku := seedGameSku(t, db, "D-FIX-NOTICE-MIX", game)
	if _, err = grantDispatchPriority(ctx, db, 7, basePriorityInput(game, offline.ID), now); err != nil {
		t.Fatal(err)
	}
	if _, err = grantDispatchPriority(ctx, db, 7, basePriorityInput(game, online.ID), now); err != nil {
		t.Fatal(err)
	}
	if _, err = grantDispatchPriority(ctx, db, 7, basePriorityInput(game, capped.ID), now); err != nil {
		t.Fatal(err)
	}
	order := seedPendingOrderWithSku(t, db, "D-FIX-NOTICE-MIX-1", sku.ID)
	tx, err = db.Tx(ctx)
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
	opened := db.EsportsOrder.GetX(ctx, order.ID)
	if opened.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusPending {
		t.Fatalf("存在可接单名单成员时应开窗：%+v", opened)
	}
	notices := db.EsportsDispatchPriorityNotice.Query().
		Where(esportsdispatchprioritynotice.OrderIDEQ(order.ID)).AllX(ctx)
	if len(notices) != 1 || notices[0].PlayerID != online.ID {
		t.Fatalf("只应通知在线且有容量的名单成员：%+v", notices)
	}
	if db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID),
		esportsdispatchprioritynotice.PlayerIDEQ(offline.ID)).ExistX(ctx) {
		t.Fatal("离线名单成员不应收到通知")
	}
	if db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID),
		esportsdispatchprioritynotice.PlayerIDEQ(capped.ID)).ExistX(ctx) {
		t.Fatal("接单上限已满的名单成员不应收到通知")
	}
}

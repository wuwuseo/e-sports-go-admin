package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchprioritynotice"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsservicesegment"
)

// 第 4 轮排单 / 派单 / 看板 / 消息桶回归测试（T3.x / T4.x）。
//
// 本文件只使用**既有符号**，RED 阶段应当是断言失败而不是编译失败；
// 需要新边界（看板客服结构体、任务函数、作用域列表、通知视图）的用例放在
// dispatch_round4_boundary_test.go。

// seedAdminAccount 复用 order_paging_test.go 的既有定义（active 管理端账号）。

// 5.6-04：抢单成功必须写通知外盒（定向责任客服），落败抢单者必须留痕并可被定向通知。
func TestGrabWritesOutboxAndRecordsLostAttempts(t *testing.T) {
	db := openPlayerTestDB(t, "esports_r4_grab_outbox")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	admin := seedAdminAccount(t, db, "r4cs11")
	order := seedGrabbingOrder(t, db, "r4-grab-1")
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)
	winner := seedPlayer(t, db, 1201, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	loser := seedPlayer(t, db, 1202, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	if _, err := grabPlayerOrder(ctx, db, winner.ID, order.ID, now); err != nil {
		t.Fatalf("抢单应成功，err=%v", err)
	}
	rows := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.seat_accepted")).AllX(ctx)
	if len(rows) != 1 {
		t.Fatalf("抢单成功应写一条 order.seat_accepted 外盒，got=%d", len(rows))
	}
	if rows[0].AggregateRef != order.OrderNo {
		t.Fatalf("外盒聚合键应为订单号，got=%q", rows[0].AggregateRef)
	}
	recipients, err := resolveNotificationRecipients(ctx, db, rows[0].EventType,
		rows[0].AggregateRef, rows[0].Payload)
	if err != nil {
		t.Fatalf("抢单外盒应能解析收件人，err=%v", err)
	}
	hasAdmin := false
	for _, item := range recipients {
		if item.AdminID == admin.ID {
			hasAdmin = true
		}
	}
	if !hasAdmin {
		t.Fatalf("抢单外盒收件人应包含责任客服：%+v", recipients)
	}

	// 落败抢单者：同一单已被抢下，应留痕为「抢单未成功」并定向通知本人。
	if _, err := grabPlayerOrder(ctx, db, loser.ID, order.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("已被抢下的订单应返回冲突，err=%v", err)
	}
	lost := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.grab_attempt_lost")).AllX(ctx)
	if len(lost) != 1 {
		t.Fatalf("落败抢单应留外盒痕迹，got=%d", len(lost))
	}
	lostRecipients, err := resolveNotificationRecipients(ctx, db, lost[0].EventType,
		lost[0].AggregateRef, lost[0].Payload)
	if err != nil {
		t.Fatalf("落败抢单留痕应能解析收件人，err=%v", err)
	}
	if len(lostRecipients) != 1 || lostRecipients[0].PlayerID != loser.ID {
		t.Fatalf("落败抢单只应定向通知本人：%+v", lostRecipients)
	}
}

// 5.3-06：多席订单有人占位时被拒席位应自动重开补位，且订单不被永久卡在待接单。
func TestMultiSeatRejectionReopensSeatForRefill(t *testing.T) {
	db := openTransferDB(t, "esports_r4_multiseat_refill")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	order := seedPaidOrder(t, db, "r4-refill-1", 2)
	keeper := seedPlayer(t, db, 1301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	quitter := seedPlayer(t, db, 1302, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	acceptSeat(t, db, order.ID, 1, keeper.ID, 11, now)
	invited, err := assignDispatchSeat(ctx, db, order.ID, 2, quitter.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = respondPlayerInvite(ctx, db, quitter.ID, invited.ID, false, now); err != nil {
		t.Fatalf("拒单应成功，err=%v", err)
	}
	// 未超时：保留 rejected 原状态作为历史（与 dispatch_fix_test 的多席口径一致）。
	seat := db.EsportsDispatchSeat.GetX(ctx, invited.ID)
	if seat.Status != esportsdispatchseat.StatusRejected {
		t.Fatalf("未超时前应保留 rejected 作为历史：%+v", seat)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID).FulfillmentStatus; got != esportsorder.FulfillmentStatusPendingAcceptance {
		t.Fatalf("仍有人占位时订单应保持待接单，got=%s", got)
	}
	// 超过 app.esports.dispatch_refill_minutes（默认 10）后由任务自动补位：席位重开为待补位并留事件。
	refilled, err := refillVacantMultiSeatSeats(ctx, db, now.Add(15*time.Minute), 100)
	if err != nil {
		t.Fatalf("缺员自动补位应成功，err=%v", err)
	}
	if refilled != 1 {
		t.Fatalf("应补位 1 个席位，got=%d", refilled)
	}
	seat = db.EsportsDispatchSeat.GetX(ctx, invited.ID)
	if seat.Status != esportsdispatchseat.StatusOpen || seat.PlayerID != nil {
		t.Fatalf("多席订单缺员席位应自动重开为待补位：%+v", seat)
	}
	if !db.EsportsOrderEvent.Query().Where(
		esportsorderevent.EventTypeEQ("order.seat_refill_required")).ExistX(ctx) {
		t.Fatal("缺少 order.seat_refill_required 事件留痕")
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID).FulfillmentStatus; got != esportsorder.FulfillmentStatusPendingAcceptance {
		t.Fatalf("补位后订单应保持待接单等待补齐，got=%s", got)
	}
	// 补位：重开的席位可再次指派。
	refill := seedPlayer(t, db, 1303, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	if _, err = assignDispatchSeat(ctx, db, order.ID, 2, refill.ID, 11, now.Add(15*time.Minute)); err != nil {
		t.Fatalf("重开的席位应可再次指派，err=%v", err)
	}
}

// 5.3-06 / 5.9-06：看板异常桶必须纳入非支付类异常（临近开始未满员 / 无人接单超时）。
func TestDispatchBoardAbnormalIncludesUnfilledAndNoTaker(t *testing.T) {
	db := openTransferDB(t, "esports_r4_board_abnormal")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	nearStart := seedPaidOrder(t, db, "r4-abn-near", 1)
	db.EsportsOrder.UpdateOneID(nearStart.ID).
		SetRequestedStartAt(now.Add(30 * time.Minute)).SaveX(ctx)

	noTaker := seedPaidOrder(t, db, "r4-abn-stale", 1)
	db.EsportsOrder.UpdateOneID(noTaker.ID).
		SetFirstPooledAt(now.Add(-3 * time.Hour)).SaveX(ctx)

	healthy := seedPaidOrder(t, db, "r4-abn-fine", 1)
	db.EsportsOrder.UpdateOneID(healthy.ID).
		SetRequestedStartAt(now.Add(72 * time.Hour)).SaveX(ctx)

	board, err := loadDispatchBoard(ctx, db, 11, true)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for _, item := range board.Abnormal {
		seen[item.ID] = true
	}
	if !seen[nearStart.ID] {
		t.Fatalf("临近开始仍未满员的订单应进异常桶：%+v", board.Abnormal)
	}
	if !seen[noTaker.ID] {
		t.Fatalf("长时间无人接单的订单应进异常桶：%+v", board.Abnormal)
	}
	if seen[healthy.ID] {
		t.Fatalf("正常在途订单不应进异常桶：%+v", board.Abnormal)
	}
}

// 5.3-06 关联：终态订单不得进异常桶。
func TestDispatchBoardAbnormalSkipsClosedOrders(t *testing.T) {
	db := openTransferDB(t, "esports_r4_board_abnormal_closed")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	closed := seedPaidOrder(t, db, "r4-abn-closed", 1)
	db.EsportsOrder.UpdateOneID(closed.ID).
		SetRequestedStartAt(now.Add(10 * time.Minute)).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).SaveX(ctx)
	cancelled := seedPaidOrder(t, db, "r4-abn-cancelled", 1)
	db.EsportsOrder.UpdateOneID(cancelled.ID).
		SetFirstPooledAt(now.Add(-6 * time.Hour)).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCancelled).SaveX(ctx)

	board, err := loadDispatchBoard(ctx, db, 11, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range board.Abnormal {
		if item.ID == closed.ID || item.ID == cancelled.ID {
			t.Fatalf("终态订单不应进异常桶：%+v", item)
		}
	}
}

// 5.7-02：服务中可发起转单（终止并结算原服务段），并写 outbox 定向通知责任客服；新陪玩可接手。
func TestInServiceSeatTransferSettlesSegmentAndNotifiesAdmin(t *testing.T) {
	db := openTransferDB(t, "esports_r4_inservice_transfer")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	admin := seedAdminAccount(t, db, "r4cs12")
	order := seedPaidOrder(t, db, "r4-insvc-1", 1)
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)
	player := seedPlayer(t, db, 1401, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seat := acceptSeat(t, db, order.ID, 1, player.ID, 11, now)
	if _, err := startPlayerService(ctx, db, player.ID, order.ID, now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID).FulfillmentStatus; got != esportsorder.FulfillmentStatusInService {
		t.Fatalf("开段后订单应服务中，got=%s", got)
	}

	item, err := requestSeatTransfer(ctx, db, player.ID, order.ID, seatTransferInput{
		Reason: "临时有事", IdempotencyKey: "r4-insvc-key-1",
	}, now)
	if err != nil {
		t.Fatalf("服务中应允许发起转单，err=%v", err)
	}
	rows := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.seat_transfer_requested")).AllX(ctx)
	if len(rows) != 1 {
		t.Fatalf("转单申请应写一条 outbox 通知，got=%d", len(rows))
	}
	recipients, err := resolveNotificationRecipients(ctx, db, rows[0].EventType,
		rows[0].AggregateRef, rows[0].Payload)
	if err != nil {
		t.Fatalf("转单申请应能解析收件人，err=%v", err)
	}
	hasAdmin := false
	for _, one := range recipients {
		if one.AdminID == admin.ID {
			hasAdmin = true
		}
	}
	if !hasAdmin {
		t.Fatalf("转单申请应收件到责任客服：%+v", recipients)
	}

	if _, err = decideSeatTransfer(ctx, db, admin.ID, item.ID, true, "已安排替补", now); err != nil {
		t.Fatalf("确认转单应成功，err=%v", err)
	}
	segment := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(order.ID),
		esportsservicesegment.PlayerIDEQ(player.ID),
	).OnlyX(ctx)
	if segment.Status != esportsservicesegment.StatusFinished || segment.EndedAt == nil ||
		segment.SystemSeconds <= 0 {
		t.Fatalf("服务中转单应结算并保留原服务段：%+v", segment)
	}
	if got := db.EsportsDispatchSeat.GetX(ctx, seat.ID); got.Status != esportsdispatchseat.StatusOpen || got.PlayerID != nil {
		t.Fatalf("转单通过后席位应回到待补位：%+v", got)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID).FulfillmentStatus; got != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("无剩余席位时订单应退回待派单，got=%s", got)
	}
	next := seedPlayer(t, db, 1402, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	if _, err = assignDispatchSeat(ctx, db, order.ID, 1, next.ID, admin.ID, now); err != nil {
		t.Fatalf("服务中转单后新陪玩应能接手，err=%v", err)
	}
}

// 5.4-09：order.priority_notice 必须有专用收件人分支（只投被通知的陪玩），取不到收件人即失败。
func TestPriorityNoticeNotifiesListedPlayersOnly(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_r4_priority_recipients")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	listed := seedPlayer(t, db, 1501, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "R4-PN", game)
	if _, err := grantDispatchPriority(ctx, db, 7, basePriorityInput(game, listed.ID), now); err != nil {
		t.Fatal(err)
	}
	order := seedPendingOrderWithSku(t, db, "R4-PN-1", sku.ID)

	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = onOrderEnteredPendingDispatch(ctx, tx, order.ID, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("开窗应成功，err=%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	opened := db.EsportsOrder.GetX(ctx, order.ID)
	if opened.PriorityNoticeUntil == nil {
		t.Fatal("开窗后应有截止时间")
	}

	notices := db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID)).AllX(ctx)
	if len(notices) != 1 {
		t.Fatalf("应恰好通知名单内 1 人，got=%d", len(notices))
	}
	payload := map[string]interface{}{
		"order_no": order.OrderNo, "player_id": notices[0].PlayerID,
		"game": game, "notice_until": opened.PriorityNoticeUntil,
	}
	recipients, err := resolveNotificationRecipients(ctx, db, "order.priority_notice", order.OrderNo, payload)
	if err != nil {
		t.Fatalf("优先通知应能解析收件人，err=%v", err)
	}
	if len(recipients) != 1 || recipients[0].PlayerID != listed.ID ||
		recipients[0].Address != playerNotificationAddress(listed.ID) {
		t.Fatalf("优先通知只应投给被通知的陪玩：%+v", recipients)
	}
	// 取不到收件人时必须明确失败，绝不降级广播。
	if _, err = resolveNotificationRecipients(ctx, db, "order.priority_notice", order.OrderNo,
		map[string]interface{}{"order_no": order.OrderNo}); !errors.Is(err, errNotificationRecipientMissing) {
		t.Fatalf("缺少陪玩 ID 时应返回收件人缺失，err=%v", err)
	}
	if title := notificationEventTitle("order.priority_notice"); title == "订单通知" {
		t.Fatalf("优先通知应有专用文案，got=%q", title)
	}
	// 陪玩端通知列表需能取到该通知（订单号 / 截止时间在边界用例中断言）。
	views, err := listPlayerDispatchNotices(ctx, db, listed.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].OrderID != order.ID {
		t.Fatalf("陪玩端应看到 1 条优先通知：%+v", views)
	}
}

// 5.4-13 / 5.5-01：开窗必须按订单所需等级过滤，等级不足的名单成员不产生通知。
func TestPriorityNoticeSkipsPlayersBelowRequiredLevel(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_r4_priority_level")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "英雄联盟"
	junior := seedPlayer(t, db, 1601, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	senior := seedPlayer(t, db, 1602, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(junior.ID).SetLevel(esportsplayer.LevelNovice).SaveX(ctx)
	db.EsportsPlayer.UpdateOneID(senior.ID).SetLevel(esportsplayer.LevelSenior).SaveX(ctx)
	sku := seedGameSku(t, db, "R4-PL", game)
	for _, player := range []*ent.EsportsPlayer{junior, senior} {
		if _, err := grantDispatchPriority(ctx, db, 7, basePriorityInput(game, player.ID), now); err != nil {
			t.Fatal(err)
		}
	}
	order := seedPendingOrderWithSku(t, db, "R4-PL-1", sku.ID)
	db.EsportsOrder.UpdateOneID(order.ID).SetRequiredLevel(esportsorder.RequiredLevelSenior).SaveX(ctx)

	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = onOrderEnteredPendingDispatch(ctx, tx, order.ID, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("开窗应成功，err=%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows := db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID)).AllX(ctx)
	if len(rows) != 1 || rows[0].PlayerID != senior.ID {
		t.Fatalf("开窗应只通知等级达标的名单成员：%+v", rows)
	}
	if db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.PlayerIDEQ(junior.ID)).ExistX(ctx) {
		t.Fatal("等级不足的名单成员不应产生通知")
	}
}

// 5.5-01：候选池按心跳新鲜度过滤「连接正常」，接受邀请必须复核资质与在线状态。
func TestDispatchEligibilityUsesConnectionHealth(t *testing.T) {
	db := openTransferDB(t, "esports_r4_conn_health")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	order := seedPaidOrder(t, db, "r4-conn-1", 1)
	stale := seedPlayer(t, db, 1701, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(stale.ID).SetLastHeartbeatAt(now.Add(-2 * time.Hour).UTC()).SaveX(ctx)
	fresh := seedPlayer(t, db, 1702, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(fresh.ID).SetLastHeartbeatAt(now.Add(-time.Minute).UTC()).SaveX(ctx)

	candidates, err := dispatchCandidates(ctx, db, order, 0, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	inPool := map[int]bool{}
	for _, item := range candidates {
		inPool[item.PlayerID] = true
	}
	if inPool[stale.ID] {
		t.Fatalf("心跳超时的在线陪玩不应进入派单候选：%+v", candidates)
	}
	if !inPool[fresh.ID] {
		t.Fatalf("心跳新鲜的陪玩应进入派单候选：%+v", candidates)
	}

	// 接受邀请时必须复核在线状态。
	seat, err := assignDispatchSeat(ctx, db, order.ID, 1, fresh.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsPlayer.UpdateOneID(fresh.ID).
		SetPresence(esportsplayer.PresenceProtectedAbnormal).SaveX(ctx)
	if err = respondPlayerInvite(ctx, db, fresh.ID, seat.ID, true, now); !errors.Is(err, errForbidden) {
		t.Fatalf("连接异常的陪玩接受邀请应被拒，err=%v", err)
	}
	db.EsportsPlayer.UpdateOneID(fresh.ID).
		SetPresence(esportsplayer.PresenceOnline).SaveX(ctx)
	if err = respondPlayerInvite(ctx, db, fresh.ID, seat.ID, true, now); err != nil {
		t.Fatalf("恢复在线后接受邀请应成功，err=%v", err)
	}

	// 未合格者（资格被撤销）接受邀请同样被拒。
	other := seedPaidOrder(t, db, "r4-conn-2", 1)
	suspended := seedPlayer(t, db, 1703, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	manual, err := assignDispatchSeat(ctx, db, other.ID, 1, suspended.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsPlayer.UpdateOneID(suspended.ID).
		SetQualification(esportsplayer.QualificationSuspended).SaveX(ctx)
	if err = respondPlayerInvite(ctx, db, suspended.ID, manual.ID, true, now); !errors.Is(err, errForbidden) {
		t.Fatalf("资格被撤销的陪玩接受邀请应被拒，err=%v", err)
	}
}

// 5.9-02：派单事务必须校验 SKU 快照有效（存在且未下架 / 未删除）。
func TestAssignRejectsUnavailableSkuSnapshot(t *testing.T) {
	db := openTransferDB(t, "esports_r4_sku_snapshot")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	player := seedPlayer(t, db, 1801, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	offSale := seedGameSku(t, db, "R4-SKU-OFF", "王者荣耀")
	db.EsportsSku.UpdateOneID(offSale.ID).SetStatus("off_sale").SaveX(ctx)
	offOrder := seedPaidOrder(t, db, "r4-sku-off", 1)
	db.EsportsOrder.UpdateOneID(offOrder.ID).SetSkuID(offSale.ID).SetSkuCode("R4-SKU-OFF").SaveX(ctx)
	if _, err := assignDispatchSeat(ctx, db, offOrder.ID, 1, player.ID, 11, now); err == nil {
		t.Fatal("SKU 已下架时不应放行派单")
	}

	goneOrder := seedPaidOrder(t, db, "r4-sku-gone", 1)
	db.EsportsOrder.UpdateOneID(goneOrder.ID).SetSkuID(987654).SetSkuCode("R4-SKU-GONE").SaveX(ctx)
	if _, err := assignDispatchSeat(ctx, db, goneOrder.ID, 1, player.ID, 11, now); err == nil {
		t.Fatal("SKU 已被删除时不应放行派单")
	}

	// 管理员补录的无 SKU 历史单（product_id=0）不因此被拦。
	legacy := seedPaidOrder(t, db, "r4-sku-legacy", 1)
	db.EsportsOrder.UpdateOneID(legacy.ID).SetSkuID(0).SaveX(ctx)
	if _, err := assignDispatchSeat(ctx, db, legacy.ID, 1, player.ID, 11, now); err != nil {
		t.Fatalf("无 SKU 的历史补录单不应被 SKU 校验拦截，err=%v", err)
	}
}

// 5.9-07：改期提案期间同单派单 / 改派被拒。
func TestRescheduleProposalBlocksDispatch(t *testing.T) {
	db := openTransferDB(t, "esports_r4_reschedule_lock")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)
	order := seedPaidOrder(t, db, "r4-resched-lock", 2)
	db.EsportsOrder.UpdateOneID(order.ID).SetCustomerID(customer.ID).
		SetRequestedStartAt(now.Add(24 * time.Hour)).SetServiceDurationMinutes(60).
		SetAppointmentStatus(esportsorder.AppointmentStatusConfirmed).SaveX(ctx)
	member := seedPlayer(t, db, 1901, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	acceptSeat(t, db, order.ID, 1, member.ID, 11, now)

	current := db.EsportsOrder.GetX(ctx, order.ID)
	item, err := proposeReschedule(ctx, db, order.ID, 11, rescheduleProposalInput{
		ExpectedVersion: current.Version, StartAt: now.Add(48 * time.Hour),
		ServiceDurationMinutes: 60, Reason: "客户要求改到次日",
	}, now)
	if err != nil {
		t.Fatalf("发起改期提案应成功，err=%v", err)
	}

	// 空席位（seat 2 仍是 open）：提案期间不得再派单。
	free := seedPlayer(t, db, 1902, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	if _, err = assignDispatchSeat(ctx, db, order.ID, 2, free.ID, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("改期提案期间指派应冲突，err=%v", err)
	}
	if _, err = transferOrder(ctx, db, order.ID, 22, 11, "客服交接"); !errors.Is(err, errConflict) {
		t.Fatalf("改期提案期间转单应冲突，err=%v", err)
	}
	// 撤回后可继续正常派单。
	if _, err = withdrawRescheduleProposal(ctx, db, item.ID, 11, now); err != nil {
		t.Fatalf("撤回提案应成功，err=%v", err)
	}
	if _, err = assignDispatchSeat(ctx, db, order.ID, 2, free.ID, 11, now); err != nil {
		t.Fatalf("撤回提案后应恢复派单，err=%v", err)
	}
}

// 5.9-07：改期提案的确认集合必须含「待接单（invited）成员」。
func TestRescheduleProposalWaitsInvitedMembers(t *testing.T) {
	db := openTransferDB(t, "esports_r4_reschedule_invited")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)
	order := seedPaidOrder(t, db, "r4-resched-invited", 2)
	db.EsportsOrder.UpdateOneID(order.ID).SetCustomerID(customer.ID).
		SetRequestedStartAt(now.Add(24 * time.Hour)).SetServiceDurationMinutes(60).
		SetAppointmentStatus(esportsorder.AppointmentStatusConfirmed).SaveX(ctx)
	member := seedPlayer(t, db, 1911, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	invited := seedPlayer(t, db, 1912, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	acceptSeat(t, db, order.ID, 1, member.ID, 11, now)
	if _, err := assignDispatchSeat(ctx, db, order.ID, 2, invited.ID, 11, now); err != nil {
		t.Fatal(err)
	}

	current := db.EsportsOrder.GetX(ctx, order.ID)
	item, err := proposeReschedule(ctx, db, order.ID, 11, rescheduleProposalInput{
		ExpectedVersion: current.Version, StartAt: now.Add(48 * time.Hour),
		ServiceDurationMinutes: 60, Reason: "客户要求改到次日",
	}, now)
	if err != nil {
		t.Fatalf("发起改期提案应成功，err=%v", err)
	}
	if _, err = confirmRescheduleProposal(ctx, db, item.ID,
		rescheduleCustomerConfirmer(customer.ID), true, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err = confirmRescheduleProposal(ctx, db, item.ID,
		reschedulePlayerConfirmer(member.ID), true, "", now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsRescheduleProposal.GetX(ctx, item.ID).Status; string(got) != "pending" {
		t.Fatalf("待接单成员未确认时提案不应生效，got=%s", got)
	}
	if _, err = confirmRescheduleProposal(ctx, db, item.ID,
		reschedulePlayerConfirmer(invited.ID), true, "", now); err != nil {
		t.Fatalf("待接单成员应可确认改期提案，err=%v", err)
	}
	if got := db.EsportsRescheduleProposal.GetX(ctx, item.ID).Status; string(got) != "confirmed" {
		t.Fatalf("全员确认后提案应生效，got=%s", got)
	}
	final := db.EsportsOrder.GetX(ctx, order.ID)
	if final.RequestedStartAt == nil || !final.RequestedStartAt.Equal(now.Add(48*time.Hour).UTC()) {
		t.Fatalf("订单档期未切到目标档期：%+v", final.RequestedStartAt)
	}
}

// 3.3-03 / 3.3-07：服务中换人应替换原席位、保留并结算原陪玩服务段。
func TestAssignDuringServiceReplacesPlayer(t *testing.T) {
	db := openTransferDB(t, "esports_r4_inservice_replace")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	order := seedPaidOrder(t, db, "r4-replace-1", 1)
	old := seedPlayer(t, db, 2001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	fresh := seedPlayer(t, db, 2002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seat := acceptSeat(t, db, order.ID, 1, old.ID, 11, now)
	if _, err := startPlayerService(ctx, db, old.ID, order.ID, now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}

	replaced, err := assignDispatchSeat(ctx, db, order.ID, 1, fresh.ID, 11, now)
	if err != nil {
		t.Fatalf("服务中换人应被允许，err=%v", err)
	}
	if replaced.PlayerID == nil || *replaced.PlayerID != fresh.ID ||
		replaced.Status != esportsdispatchseat.StatusInvited {
		t.Fatalf("服务中换人应替换原席位为新陪玩邀请：%+v", replaced)
	}
	segment := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(order.ID),
		esportsservicesegment.PlayerIDEQ(old.ID),
	).OnlyX(ctx)
	if segment.Status != esportsservicesegment.StatusFinished || segment.EndedAt == nil {
		t.Fatalf("原陪玩服务段应保留并结算：%+v", segment)
	}
	if countEventsByType(t, db)["order.service_player_replaced"] != 1 {
		t.Fatalf("缺少服务中换人事件留痕：%+v", countEventsByType(t, db))
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID).FulfillmentStatus; got != esportsorder.FulfillmentStatusPendingAcceptance {
		t.Fatalf("换人后订单应等待新陪玩接单，got=%s", got)
	}
	if got := db.EsportsDispatchSeat.GetX(ctx, seat.ID); got.PlayerID == nil || *got.PlayerID != fresh.ID {
		t.Fatalf("席位未换到新陪玩：%+v", got)
	}
}

// 5.7-06：语音渠道必须有事件映射；未配置网关时明确降级并可见，不得伪成功。
func TestVoiceChannelMappedAndDegradesExplicitly(t *testing.T) {
	spy := &recordingNotificationSender{}
	if err := sendNotificationEvent(t.Context(), spy, map[string]bool{notifyChannelVoice: true},
		"r4-voice", "dispatch.escalation_no_taker", "R4-VOICE-1",
		map[string]interface{}{"order_no": "R4-VOICE-1"}); err != nil {
		t.Fatalf("语音通道投递不应报错：%v", err)
	}
	if !containsString(spy.sends, notifyChannelVoice+":dispatch.escalation_no_taker") {
		t.Fatalf("至少一类派单事件应映射到 voice 通道，got=%v", spy.sends)
	}

	db := openTransferDB(t, "esports_r4_voice_degrade")
	defer db.Close()
	ctx := t.Context()
	admin := seedAdminAccount(t, db, "r4voice")
	order := seedPaidOrder(t, db, "r4-voice-1", 1)
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)

	sender := newCompositeNotificationSender(db)
	enabled := map[string]bool{notifyChannelWecomApp: true, notifyChannelVoice: true}
	if err := sendNotificationEvent(ctx, sender, enabled, "r4-voice-degrade",
		"dispatch.escalation_no_taker", order.OrderNo,
		map[string]interface{}{"order_no": order.OrderNo, "admin_id": admin.ID}); err != nil {
		t.Fatalf("网关未配置属于可降级情形，不应报错：%v", err)
	}
	messages := db.Message.Query().AllX(ctx)
	if len(messages) == 0 {
		t.Fatal("站内信应当仍投递")
	}
	if !notificationExtraHasChannel(messages[0].Extra, "degraded_channels", notifyChannelVoice) {
		t.Fatalf("未配置的 voice 通道必须显式降级并可见：%+v", messages[0].Extra)
	}
}

// notificationExtraHasChannel 判断站内信 extra 的某个通道清单字段是否包含指定通道
// （extra 经 JSON 往返后是 []interface{}，这里两种形态都接受）。
func notificationExtraHasChannel(extra map[string]interface{}, field, channel string) bool {
	if extra == nil {
		return false
	}
	switch value := extra[field].(type) {
	case []string:
		return containsString(value, channel)
	case []interface{}:
		for _, one := range value {
			if name, ok := one.(string); ok && name == channel {
				return true
			}
		}
	}
	return false
}

// recordingNotificationSender 复用 notification_outbox_test.go 的既有定义
// （记录 "channel:eventType"），本文件不再重复声明同名类型。

// 6.1-15：待回应席位陪玩必须收到派单通知；settlement.* 必须有生产事件、通道映射、收件人与文案闭环。
func TestSeatInviteAndSettlementNotificationRecipients(t *testing.T) {
	db := openTransferDB(t, "esports_r4_notify_branches")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	admin := seedAdminAccount(t, db, "r4notify")
	order := seedPaidOrder(t, db, "r4-notify-1", 1)
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)
	player := seedPlayer(t, db, 2101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seat, err := assignDispatchSeat(ctx, db, order.ID, 1, player.ID, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	recipients, err := resolveNotificationRecipients(ctx, db, "order.seat_invited", order.OrderNo,
		map[string]interface{}{"order_no": order.OrderNo, "seat_index": 1, "player_id": player.ID})
	if err != nil {
		t.Fatalf("接单邀请应能解析收件人，err=%v", err)
	}
	found := false
	for _, one := range recipients {
		if one.PlayerID == player.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("待回应席位陪玩应收到派单通知：%+v", recipients)
	}

	if title := notificationEventTitle("settlement.segment_finished"); title == "业务通知" {
		t.Fatalf("settlement.* 应有专用文案，got=%q", title)
	}
	if got := eventNotifyChannels("settlement.segment_finished"); len(got) == 0 {
		t.Fatalf("settlement.* 必须有通道映射，got=%v", got)
	}

	// 服务段结束产生 settlement 生产事件并定向通知陪玩。
	if err = respondPlayerInvite(ctx, db, player.ID, seat.ID, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err = startPlayerService(ctx, db, player.ID, order.ID, now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = finishPlayerService(ctx, db, player.ID, order.ID, 1, 1800, "", now); err != nil {
		t.Fatal(err)
	}
	rows := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("settlement.segment_finished")).AllX(ctx)
	if len(rows) != 1 {
		t.Fatalf("服务段结束应写 settlement 外盒事件，got=%d", len(rows))
	}
	settled, err := resolveNotificationRecipients(ctx, db, rows[0].EventType, rows[0].AggregateRef, rows[0].Payload)
	if err != nil {
		t.Fatalf("结算事件应能解析收件人，err=%v", err)
	}
	if len(settled) != 1 || settled[0].PlayerID != player.ID {
		t.Fatalf("结算事件应定向通知该陪玩：%+v", settled)
	}
}

// P1-01-03：在途保护异常（protected_abnormal）的陪玩**不得**再被指派新单，
// 也仍然禁止抢新单——「连接异常暂缓新派单」不因在途订单而放宽。
func TestInflightProtectedAbnormalPlayerStaysDispatchable(t *testing.T) {
	db := openPlayerTestDB(t, "esports_r4_inflight_dispatchable")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	busy := seedPlayer(t, db, 2201, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	inflight := seedPaidOrder(t, db, "r4-inflight-1", 1)
	seat, err := assignDispatchSeat(ctx, db, inflight.ID, 1, busy.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = respondPlayerInvite(ctx, db, busy.ID, seat.ID, true, now); err != nil {
		t.Fatal(err)
	}
	// 接单后再让心跳超时：在途订单使其转保护异常（§6.4）。
	db.EsportsPlayer.UpdateOneID(busy.ID).SetLastHeartbeatAt(now.Add(-30 * time.Minute).UTC()).SaveX(ctx)
	if _, err = autoOfflineStalePlayers(ctx, db, now, 100); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsPlayer.GetX(ctx, busy.ID); got.Presence != esportsplayer.PresenceProtectedAbnormal {
		t.Fatalf("有在途订单的失联陪玩应转保护异常：%+v", got)
	}

	other := seedPaidOrder(t, db, "r4-inflight-2", 1)
	if _, err = assignDispatchSeat(ctx, db, other.ID, 1, busy.ID, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("连接异常的陪玩不得被指派新单，err=%v", err)
	}
	// 抢新单同样被禁止。
	if _, err = grabPlayerOrder(ctx, db, busy.ID, seedGrabbingOrder(t, db, "r4-inflight-grab").ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("在途保护异常陪玩应禁止抢新单，err=%v", err)
	}
	// 在途履约不受影响：既有席位仍是他本人。
	if got := db.EsportsDispatchSeat.GetX(ctx, seat.ID); got.Status != esportsdispatchseat.StatusAccepted ||
		got.PlayerID == nil || *got.PlayerID != busy.ID {
		t.Fatalf("在途席位应保留原陪玩：%+v", got)
	}
}

// 通知事件契约闭环（D3 根因）：通道表里的每个事件都必须有文案；命名空间前缀要有专属回落标题。
func TestNotificationEventContractClosedLoop(t *testing.T) {
	namespaces := map[string]bool{
		"order.": true, "aftersale.": true, "payment.": true, "gift.": true,
		"member.": true, "customer.": true, "player.": true, "settlement.": true,
	}
	for _, target := range notifyChannelTargets {
		if len(target.channels) == 0 {
			t.Fatalf("事件 %q 没有映射任何通道", target.prefix)
		}
		if _, ok := notificationEventText[target.prefix]; ok {
			continue
		}
		if !namespaces[target.prefix] {
			t.Fatalf("事件 %q 未命中专用文案且不是命名空间前缀", target.prefix)
		}
		if title := notificationEventTitle(target.prefix + "probe"); title == "业务通知" {
			t.Fatalf("命名空间 %q 缺少专属回落标题", target.prefix)
		}
	}
}

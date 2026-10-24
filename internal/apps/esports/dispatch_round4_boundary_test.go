package esports

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscspresence"
	"go-admin/internal/ent/esportsdispatchprioritynotice"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsplayer"
)

// 第 4 轮需要**新边界**（看板客服结构体 / 定时任务函数 / 通知视图）的回归用例。
// RED 阶段这里是编译失败（符号不存在），GREEN 后逐条断言通过。

// 5.8-04：看板返回客服清单及在线状态（按最近操作时间推导）+ 最近活跃时间。
func TestDispatchBoardAdminPresenceIsDerivedFromRecentActivity(t *testing.T) {
	db := openTransferDB(t, "esports_r4_board_admins")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	recent := seedAdminAccount(t, db, "r4presence-a")
	stale := seedAdminAccount(t, db, "r4presence-b")
	fromEvents := seedAdminAccount(t, db, "r4presence-c")
	db.Admin.UpdateOneID(recent.ID).SetLastLogin(now.Add(-time.Minute)).SaveX(ctx)
	db.Admin.UpdateOneID(stale.ID).SetLastLogin(now.Add(-4 * time.Hour)).SaveX(ctx)

	// 仅凭最近订单操作（无 last_login）也应判为活跃。
	order := seedPaidOrder(t, db, "r4-presence-1", 1)
	db.EsportsOrderEvent.Create().SetOrderID(order.ID).SetOrderVersion(1).
		SetEventType("order.claimed").SetActorType("admin").
		SetActorID(strconv.Itoa(fromEvents.ID)).SetCreatedAt(now.Add(-2 * time.Minute)).SaveX(ctx)

	board, err := loadDispatchBoard(ctx, db, recent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int]dispatchBoardAdmin{}
	for _, item := range board.Admins {
		byID[item.AdminID] = item
	}
	if len(byID) < 3 {
		t.Fatalf("看板应返回全部客服清单：%+v", board.Admins)
	}
	if item, ok := byID[recent.ID]; !ok || !item.Online || item.LastActiveAt == nil {
		t.Fatalf("最近登录的客服应判为在线：%+v", board.Admins)
	}
	if item, ok := byID[stale.ID]; !ok || item.Online {
		t.Fatalf("长时间无操作的客服应判为离线：%+v", board.Admins)
	}
	if item := byID[stale.ID]; item.LastActiveAt == nil ||
		item.LastActiveAt.After(now.Add(-time.Hour)) {
		t.Fatalf("离线客服应带最近活跃时间：%+v", item)
	}
	if item, ok := byID[fromEvents.ID]; !ok || !item.Online {
		t.Fatalf("有近期操作的客服应判为在线：%+v", board.Admins)
	}
	// 阈值可配：把在线阈值放宽到 8 小时后，4 小时无操作的客服转为在线。
	setUpConfig(t, db, csOfflineTimeoutMinutesKey, "480")
	relaxed, err := loadDispatchBoard(ctx, db, recent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range relaxed.Admins {
		if item.AdminID == stale.ID && !item.Online {
			t.Fatalf("放宽在线阈值后 4 小时无操作应判在线：%+v", item)
		}
	}
}

// 5.8-04：presence 表（EsportsCsPresence）是权威数据源 —— offline 覆盖近期登录，
// online/busy 仍需在 cs_offline_timeout_minutes 内活跃才算在线。
func TestDispatchBoardAdminPresenceUsesPresenceLedger(t *testing.T) {
	db := openTransferDB(t, "esports_r4_board_presence_ledger")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	setUpConfig(t, db, csOfflineTimeoutMinutesKey, "15")
	recent := now.Add(-time.Minute)
	staleAt := now.Add(-2 * time.Hour)

	// ① presence=offline 但 last_login 很近 → 仍判离线（权威）。
	manualOffline := seedAdminAccount(t, db, "r4ledger-a")
	db.Admin.UpdateOneID(manualOffline.ID).SetLastLogin(recent).SaveX(ctx)
	db.EsportsCsPresence.Create().SetAdminID(manualOffline.ID).
		SetPresence(esportscspresence.PresenceOffline).SetNillableLastActiveAt(&recent).SaveX(ctx)
	// ② presence=busy 且活跃 → 在线。
	busy := seedAdminAccount(t, db, "r4ledger-b")
	busyActive := now.Add(-2 * time.Minute)
	db.EsportsCsPresence.Create().SetAdminID(busy.ID).
		SetPresence(esportscspresence.PresenceBusy).SetNillableLastActiveAt(&busyActive).SaveX(ctx)
	// ③ presence=online 但活跃时间已超阈值 → 离线（掉线未上报）。
	staleOnline := seedAdminAccount(t, db, "r4ledger-c")
	db.EsportsCsPresence.Create().SetAdminID(staleOnline.ID).
		SetPresence(esportscspresence.PresenceOnline).SetNillableLastActiveAt(&staleAt).SaveX(ctx)

	board, err := loadDispatchBoard(ctx, db, manualOffline.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int]dispatchBoardAdmin{}
	for _, item := range board.Admins {
		byID[item.AdminID] = item
	}
	if item := byID[manualOffline.ID]; item.Online || item.Presence != "offline" {
		t.Fatalf("presence=offline 必须判离线（权威）：%+v", item)
	}
	if item := byID[busy.ID]; !item.Online || item.Presence != "busy" {
		t.Fatalf("presence=busy 且活跃应判在线：%+v", item)
	}
	if item := byID[staleOnline.ID]; item.Online {
		t.Fatalf("presence=online 但活跃超阈值应判离线：%+v", item)
	}
	if item := byID[staleOnline.ID]; item.LastActiveAt == nil ||
		item.LastActiveAt.After(now.Add(-time.Hour)) {
		t.Fatalf("应回填 presence 表的最近活跃时间：%+v", item)
	}
}

// 5.9-07 关联：任何入口都不得下调已售必需席位（显式守护）。
func TestEnsureRequiredSeatsIntact(t *testing.T) {
	db := openTransferDB(t, "esports_r4_required_seats_guard")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	order := seedPaidOrder(t, db, "r4-seats-guard", 2)
	player := seedPlayer(t, db, 2301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	if _, err := assignDispatchSeat(ctx, db, order.ID, 1, player.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if err := ensureRequiredSeatsIntact(ctx, db, order.ID); err != nil {
		t.Fatalf("刚生成席位的订单不应触发守护，err=%v", err)
	}
	if _, err := db.EsportsDispatchSeat.Delete().
		Where(esportsdispatchseat.OrderIDEQ(order.ID), esportsdispatchseat.SeatIndexEQ(2)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ensureRequiredSeatsIntact(ctx, db, order.ID); err == nil {
		t.Fatal("席位少于 required_seats 时守护应报错")
	}
	// 守护触发时同一入口的派单也必须被拒。
	if _, err := assignDispatchSeat(ctx, db, order.ID, 2, player.ID, 11, now); err == nil {
		t.Fatal("席位缺失的订单不应放行派单")
	}
}

// 5.4-09：陪玩端优先通知列表须返回订单号 / 游戏分区 / 截止时间。
func TestPlayerDispatchNoticeViewCarriesOrderContext(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_r4_notice_view")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	listed := seedPlayer(t, db, 2401, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "R4-NV", game)
	if _, err := grantDispatchPriority(ctx, db, 7, basePriorityInput(game, listed.ID), now); err != nil {
		t.Fatal(err)
	}
	order := seedPendingOrderWithSku(t, db, "R4-NV-1", sku.ID)
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
	until := db.EsportsOrder.GetX(ctx, order.ID).PriorityNoticeUntil
	views, err := listPlayerDispatchNotices(ctx, db, listed.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("应返回 1 条优先通知，got=%d", len(views))
	}
	if views[0].OrderNo != order.OrderNo || views[0].Game != game ||
		views[0].NoticeUntil == nil || until == nil || !views[0].NoticeUntil.Equal(*until) {
		t.Fatalf("陪玩端通知应含订单号 / 分区 / 截止时间：%+v", views[0])
	}
}

// 5.1-05：负责客服离线超阈值（或待派单积压超阈值）时订单自动转公共池并通知该客服。
func TestAutoReleaseStaleAdminOrdersToPublicPool(t *testing.T) {
	db := openTransferDB(t, "esports_r4_auto_release")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	setUpConfig(t, db, csOfflineTimeoutMinutesKey, "30")
	setUpConfig(t, db, csBacklogAutoTransferMinutesKey, "60")
	staleAdmin := seedAdminAccount(t, db, "r4offline")
	db.Admin.UpdateOneID(staleAdmin.ID).SetLastLogin(now.Add(-3 * time.Hour)).SaveX(ctx)
	busyAdmin := seedAdminAccount(t, db, "r4busy")
	db.Admin.UpdateOneID(busyAdmin.ID).SetLastLogin(now.Add(-time.Minute)).SaveX(ctx)

	offlineOrder := seedPaidOrder(t, db, "r4-auto-offline", 1)
	db.EsportsOrder.UpdateOneID(offlineOrder.ID).SetResponsibleAdminID(staleAdmin.ID).SaveX(ctx)
	healthyOrder := seedPaidOrder(t, db, "r4-auto-healthy", 1)
	db.EsportsOrder.UpdateOneID(healthyOrder.ID).SetResponsibleAdminID(busyAdmin.ID).SaveX(ctx)

	count, err := autoTransferBacklogOrders(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("自动转池应成功，err=%v", err)
	}
	if count != 1 {
		t.Fatalf("应只释放离线客服名下的 1 单，got=%d", count)
	}
	if got := db.EsportsOrder.GetX(ctx, offlineOrder.ID); got.ResponsibleAdminID != nil {
		t.Fatalf("离线客服名下的待派单应转公共池：%+v", got.ResponsibleAdminID)
	}
	if got := db.EsportsOrder.GetX(ctx, healthyOrder.ID); got.ResponsibleAdminID == nil {
		t.Fatal("在线客服名下的订单不应被释放")
	}
	if !db.EsportsOrderEvent.Query().Where(
		esportsorderevent.EventTypeEQ("order.released_to_public_pool")).ExistX(ctx) {
		t.Fatal("自动转池应写释放事件留痕")
	}
	rows := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("dispatch.auto_pool_release")).AllX(ctx)
	if len(rows) != 1 {
		t.Fatalf("自动转池应写一条定向通知外盒，got=%d", len(rows))
	}
	recipients, err := resolveNotificationRecipients(ctx, db, rows[0].EventType,
		rows[0].AggregateRef, rows[0].Payload)
	if err != nil {
		t.Fatalf("自动转池通知应能解析收件人，err=%v", err)
	}
	if len(recipients) != 1 || recipients[0].AdminID != staleAdmin.ID {
		t.Fatalf("自动转池只应通知原负责客服：%+v", recipients)
	}
	// 幂等：重复运行不再处理。
	if again, err := autoTransferBacklogOrders(ctx, db, now, 100); err != nil || again != 0 {
		t.Fatalf("重复运行应幂等，got=%d err=%v", again, err)
	}

	// 积压口径：待派单积压超过阈值时同样转池（阈值 2，在线客服名下灌到 3 单）。
	setUpConfig(t, db, csBacklogOrderThresholdKey, "2")
	for _, no := range []string{"r4-backlog-1", "r4-backlog-2"} {
		extra := seedPaidOrder(t, db, no, 1)
		db.EsportsOrder.UpdateOneID(extra.ID).SetResponsibleAdminID(busyAdmin.ID).SaveX(ctx)
	}
	// busyAdmin 现有 3 单（含 healthyOrder）→ 超过阈值 2，全部释放（积压条件不要求离线）。
	if released, err := autoTransferBacklogOrders(ctx, db, now, 100); err != nil {
		t.Fatal(err)
	} else if released != 3 {
		t.Fatalf("积压超阈值应释放该客服名下 3 单待派单，got=%d", released)
	}
}

// 5.9-06：三类逐级升级（无人接单 / 多人缺席 / 临近开始未满员）必须产生升级通知与异常待办。
func TestDispatchEscalationsCoverThreeTriggers(t *testing.T) {
	db := openTransferDB(t, "esports_r4_escalations")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	admin := seedAdminAccount(t, db, "r4escalate")

	// ① 无人接单：入池已久且无任何有效席位。
	noTaker := seedPaidOrder(t, db, "r4-esc-notaker", 1)
	db.EsportsOrder.UpdateOneID(noTaker.ID).SetResponsibleAdminID(admin.ID).
		SetFirstPooledAt(now.Add(-3 * time.Hour)).SaveX(ctx)

	// ② 多人缺席：多席订单出现空缺席位（自动补位后的待补位状态）。
	absent := seedPaidOrder(t, db, "r4-esc-absent", 2)
	db.EsportsOrder.UpdateOneID(absent.ID).SetResponsibleAdminID(admin.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingAcceptance).SaveX(ctx)
	keeper := seedPlayer(t, db, 2501, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsDispatchSeat.Create().SetOrderID(absent.ID).SetSeatIndex(1).
		SetPlayerID(keeper.ID).SetStatus("accepted").SaveX(ctx)
	db.EsportsDispatchSeat.Create().SetOrderID(absent.ID).SetSeatIndex(2).
		SetStatus("open").SaveX(ctx)

	// ③ 临近开始未满员：单席位、预约时间在即。
	starting := seedPaidOrder(t, db, "r4-esc-starting", 1)
	db.EsportsOrder.UpdateOneID(starting.ID).SetResponsibleAdminID(admin.ID).
		SetRequestedStartAt(now.Add(30 * time.Minute)).SaveX(ctx)

	count, err := runDispatchEscalations(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("升级扫描应成功，err=%v", err)
	}
	if count == 0 {
		t.Fatal("三类触发条件都应产生升级")
	}
	for _, eventType := range []string{
		"dispatch.escalation_no_taker",
		"dispatch.escalation_member_absent",
		"dispatch.escalation_starting_unfilled",
	} {
		rows := db.EsportsOutbox.Query().Where(esportsoutbox.EventTypeEQ(eventType)).AllX(ctx)
		if len(rows) == 0 {
			t.Fatalf("缺少升级事件 %s", eventType)
		}
		recipients, err := resolveNotificationRecipients(ctx, db, eventType,
			rows[0].AggregateRef, rows[0].Payload)
		if err != nil {
			t.Fatalf("%s 应能解析收件人，err=%v", eventType, err)
		}
		if len(recipients) != 1 || recipients[0].AdminID != admin.ID {
			t.Fatalf("%s 应定向通知责任客服：%+v", eventType, recipients)
		}
		if title := notificationEventTitle(eventType); title == "派单提醒" {
			t.Fatalf("%s 缺少专用文案，got=%q", eventType, title)
		}
	}
	// 升级后的订单必须出现在看板异常桶（非支付类异常待办）。
	board, err := loadDispatchBoard(ctx, db, admin.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for _, item := range board.Abnormal {
		seen[item.ID] = true
	}
	for _, order := range []*ent.EsportsOrder{noTaker, starting} {
		if !seen[order.ID] {
			t.Fatalf("升级订单应进异常桶：%d %+v", order.ID, board.Abnormal)
		}
	}
	// 幂等：重复运行不重复升级。
	if again, err := runDispatchEscalations(ctx, db, now, 100); err != nil || again != 0 {
		t.Fatalf("重复运行应幂等，got=%d err=%v", again, err)
	}
}

// 5.9-02：SKU 下架应给出明确业务错误而不是静默跳过分区校验。
func TestAssignUnavailableSkuReturnsExplicitError(t *testing.T) {
	db := openTransferDB(t, "esports_r4_sku_error_text")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	player := seedPlayer(t, db, 2601, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "R4-SKU-TXT", "王者荣耀")
	db.EsportsSku.UpdateOneID(sku.ID).SetStatus("off_sale").SaveX(ctx)
	order := seedPaidOrder(t, db, "r4-sku-txt", 1)
	db.EsportsOrder.UpdateOneID(order.ID).SetSkuID(sku.ID).SetSkuCode("R4-SKU-TXT").SaveX(ctx)
	_, err := assignDispatchSeat(ctx, db, order.ID, 1, player.ID, 11, now)
	if err == nil {
		t.Fatal("下架 SKU 不应放行派单")
	}
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("应为参数类业务错误，got=%v", err)
	}
	if !strings.Contains(err.Error(), "SKU") {
		t.Fatalf("错误文案应点明 SKU：%v", err)
	}
}

// 5.5-01 关联：名单开窗同样按连接健康度过滤（心跳超时的在线成员不产生通知）。
func TestPriorityWindowSkipsStaleHeartbeatMembers(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_r4_window_heartbeat")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	stale := seedPlayer(t, db, 2701, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(stale.ID).SetLastHeartbeatAt(now.Add(-2 * time.Hour).UTC()).SaveX(ctx)
	fresh := seedPlayer(t, db, 2702, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(fresh.ID).SetLastHeartbeatAt(now.Add(-time.Minute).UTC()).SaveX(ctx)
	sku := seedGameSku(t, db, "R4-WH", game)
	for _, player := range []*ent.EsportsPlayer{stale, fresh} {
		if _, err := grantDispatchPriority(ctx, db, 7, basePriorityInput(game, player.ID), now); err != nil {
			t.Fatal(err)
		}
	}
	order := seedPendingOrderWithSku(t, db, "R4-WH-1", sku.ID)
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
	rows := db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID)).AllX(ctx)
	if len(rows) != 1 || rows[0].PlayerID != fresh.ID {
		t.Fatalf("开窗应只通知连接正常的名单成员：%+v", rows)
	}
}

// P1-01-03：连接异常（protected_abnormal）的陪玩**不得**进入派单候选池——
// 「连接异常暂缓新派单」优先于「有在途订单保留上线状态」：在途订单只保留履约与结算。
func TestInflightProtectedAbnormalInCandidates(t *testing.T) {
	db := openPlayerTestDB(t, "esports_r4_inflight_candidates")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	busy := seedPlayer(t, db, 2801, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	inflight := seedPaidOrder(t, db, "r4-cand-inflight", 1)
	seat, err := assignDispatchSeat(ctx, db, inflight.ID, 1, busy.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = respondPlayerInvite(ctx, db, busy.ID, seat.ID, true, now); err != nil {
		t.Fatal(err)
	}
	// 接单后再让心跳超时：在途订单使其转保护异常。
	db.EsportsPlayer.UpdateOneID(busy.ID).SetLastHeartbeatAt(now.Add(-30 * time.Minute).UTC()).SaveX(ctx)
	if _, err = autoOfflineStalePlayers(ctx, db, now, 100); err != nil {
		t.Fatal(err)
	}
	target := seedPaidOrder(t, db, "r4-cand-target", 1)
	candidates, err := dispatchCandidates(ctx, db, target, 0, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range candidates {
		if item.PlayerID == busy.ID {
			t.Fatalf("连接异常（受保护）的陪玩不得进入派单候选：%+v", candidates)
		}
	}
	// 人工指派同样被拒（暂缓一切新派单）。
	if _, err = assignDispatchSeat(ctx, db, target.ID, 1, busy.ID, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("连接异常的陪玩不得被人工指派，err=%v", err)
	}
}

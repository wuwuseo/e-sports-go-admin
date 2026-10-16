package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsdispatchprioritygrant"
	"go-admin/internal/ent/esportsdispatchprioritynotice"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/migrate"
)

func openDispatchPriorityDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

func basePriorityInput(game string, playerID int) dispatchPriorityInput {
	return dispatchPriorityInput{
		Game: game, PlayerID: playerID, Period: "weekly",
		Source: "manual", Rank: 0,
	}
}

// 名额上限：每游戏每期有上限，重复录入同一陪玩冲突，参数非法被拒。
func TestDispatchPriorityGrantQuota(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_priority_grant")
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	first := seedPlayer(t, db, 1001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	second := seedPlayer(t, db, 1002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	third := seedPlayer(t, db, 1003, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	setWithdrawalConfig(t, db, dispatchPriorityQuotaKey, "2")

	grant, err := grantDispatchPriority(ctx, db, 7, basePriorityInput(game, first.ID), now)
	if err != nil {
		t.Fatalf("授予优先派单权应成功，err=%v", err)
	}
	// 当期已开始 → 直接生效。
	if grant.Status != esportsdispatchprioritygrant.StatusActive {
		t.Fatalf("当期名单应为 active，got=%s", grant.Status)
	}
	if _, err = grantDispatchPriority(ctx, db, 7, basePriorityInput(game, second.ID), now); err != nil {
		t.Fatalf("第二名应可录入，err=%v", err)
	}
	// 名额已满。
	if _, err = grantDispatchPriority(ctx, db, 7, basePriorityInput(game, third.ID), now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超出名额应被拒，err=%v", err)
	}
	// 同一游戏同一期同一陪玩重复录入冲突。
	if _, err = grantDispatchPriority(ctx, db, 7, basePriorityInput(game, first.ID), now); !errors.Is(err, errConflict) {
		t.Fatalf("重复录入应冲突，err=%v", err)
	}
	// 其他游戏不受该游戏名额影响。
	other := basePriorityInput("英雄联盟", third.ID)
	if _, err = grantDispatchPriority(ctx, db, 7, other, now); err != nil {
		t.Fatalf("其他游戏应可录入，err=%v", err)
	}
	// 参数非法。
	for i, in := range []dispatchPriorityInput{
		{Game: "", PlayerID: first.ID, Period: "weekly", Source: "manual"},
		{Game: game, PlayerID: 0, Period: "weekly", Source: "manual"},
		{Game: game, PlayerID: first.ID, Period: "yearly", Source: "manual"},
		{Game: game, PlayerID: first.ID, Period: "weekly", Source: "lottery"},
		{Game: game, PlayerID: first.ID, Period: "weekly", Source: "manual", Rank: -1},
	} {
		if _, err = grantDispatchPriority(ctx, db, 7, in, now); !errors.Is(err, errInvalidInput) {
			t.Fatalf("第 %d 组非法入参应被拒，err=%v", i+1, err)
		}
	}
	// 预生成下一期 → pending。
	next := basePriorityInput(game, third.ID)
	next.NextPeriod = true
	pending, err := grantDispatchPriority(ctx, db, 7, next, now)
	if err != nil {
		t.Fatalf("预生成下一期名单应成功，err=%v", err)
	}
	if pending.Status != esportsdispatchprioritygrant.StatusPending || !pending.PeriodStart.After(now.UTC()) {
		t.Fatalf("下一期名单应为 pending 且起始在未来：%+v", pending)
	}
}

// 中途人工增减：移出名单须填原因，移出后释放名额。
func TestDispatchPriorityRevoke(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_priority_revoke")
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	player := seedPlayer(t, db, 2001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	other := seedPlayer(t, db, 2002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	setWithdrawalConfig(t, db, dispatchPriorityQuotaKey, "1")

	grant, err := grantDispatchPriority(ctx, db, 7, basePriorityInput(game, player.ID), now)
	if err != nil {
		t.Fatal(err)
	}
	// 名额为 1 时第二名被拒。
	if _, err = grantDispatchPriority(ctx, db, 7, basePriorityInput(game, other.ID), now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("名额已满应被拒，err=%v", err)
	}
	if _, err = revokeDispatchPriority(ctx, db, 7, grant.ID, "  ", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未填原因应被拒，err=%v", err)
	}
	revoked, err := revokeDispatchPriority(ctx, db, 7, grant.ID, "本期表现不达标", now)
	if err != nil {
		t.Fatalf("移出名单应成功，err=%v", err)
	}
	if revoked.Status != esportsdispatchprioritygrant.StatusRevoked || revoked.RevokeReason == "" {
		t.Fatalf("移出留痕不正确：%+v", revoked)
	}
	// 重复移出冲突。
	if _, err = revokeDispatchPriority(ctx, db, 7, grant.ID, "再移出一次", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复移出应冲突，err=%v", err)
	}
	// 名额释放后可录入他人。
	if _, err = grantDispatchPriority(ctx, db, 7, basePriorityInput(game, other.ID), now); err != nil {
		t.Fatalf("移出后应释放名额，err=%v", err)
	}
}

// 周期重算：过期转 expired、到点转 active，并在切换点前预生成下一期名单。
func TestRebuildDispatchPriority(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_priority_rebuild")
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	otherGame := "英雄联盟"
	current := seedPlayer(t, db, 3001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	due := seedPlayer(t, db, 3002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	start, end := dispatchPriorityPeriodWindow("weekly", now)
	// game：上一期（已过期）的生效名单。
	db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(current.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(start.AddDate(0, 0, -7)).SetPeriodEnd(start).
		SetStatus(esportsdispatchprioritygrant.StatusActive).SaveX(ctx)
	// game：本期生效名单（rank 1），用于预生成下一期。
	db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(current.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(start).SetPeriodEnd(end).SetRank(1).
		SetStatus(esportsdispatchprioritygrant.StatusActive).SaveX(ctx)
	// otherGame：下一期已有人工名单，重算不应覆盖或重复生成。
	db.EsportsDispatchPriorityGrant.Create().SetGame(otherGame).SetPlayerID(due.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(start).SetPeriodEnd(end).SetRank(1).
		SetStatus(esportsdispatchprioritygrant.StatusActive).SaveX(ctx)
	db.EsportsDispatchPriorityGrant.Create().SetGame(otherGame).SetPlayerID(due.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(end).SetPeriodEnd(end.AddDate(0, 0, 7)).SetRank(1).
		SetStatus(esportsdispatchprioritygrant.StatusPending).SaveX(ctx)

	count, err := rebuildDispatchPriority(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("周期重算应成功，err=%v", err)
	}
	if count < 1 {
		t.Fatalf("应至少处理 1 条，got=%d", count)
	}
	// 上一期转 expired。
	expired := db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.StatusEQ(esportsdispatchprioritygrant.StatusExpired),
		esportsdispatchprioritygrant.PeriodEndEQ(start)).CountX(ctx)
	if expired != 1 {
		t.Fatalf("上一期应转 expired，got=%d", expired)
	}
	// 本期保持 active（两个游戏各 1 条）。
	active := db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.StatusEQ(esportsdispatchprioritygrant.StatusActive),
		esportsdispatchprioritygrant.PeriodStartEQ(start)).CountX(ctx)
	if active != 2 {
		t.Fatalf("本期应保持 2 条 active，got=%d", active)
	}
	// game 预生成下一期：延续本期生效名单，source=rotation 且带排名。
	generated, err := db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.GameEQ(game),
		esportsdispatchprioritygrant.SourceEQ(esportsdispatchprioritygrant.SourceRotation),
		esportsdispatchprioritygrant.PeriodStartEQ(end),
	).Only(ctx)
	if err != nil {
		t.Fatalf("应存在预生成的下一期名单，err=%v", err)
	}
	if generated.PlayerID != current.ID || generated.Rank < 1 ||
		generated.Status != esportsdispatchprioritygrant.StatusPending {
		t.Fatalf("预生成名单不正确：%+v", generated)
	}
	// otherGame 已有人工下一期名单 → 不重复生成。
	otherNext := db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.GameEQ(otherGame),
		esportsdispatchprioritygrant.PeriodStartEQ(end)).CountX(ctx)
	if otherNext != 1 {
		t.Fatalf("已有人工下一期名单时不应重复生成，got=%d", otherNext)
	}
	// 重算幂等：再次运行不新增下一期名单。
	if _, err = rebuildDispatchPriority(ctx, db, now.Add(time.Minute), 100); err != nil {
		t.Fatal(err)
	}
	again := db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.PeriodStartEQ(end)).CountX(ctx)
	if again != 2 {
		t.Fatalf("下一期名单不应重复生成，got=%d", again)
	}
}

// 到点的待生效名单在重算时转为生效。
func TestRebuildDispatchPriorityActivatesDueGrant(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_priority_due")
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	player := seedPlayer(t, db, 3101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	start, end := dispatchPriorityPeriodWindow("weekly", now)

	pending := db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(player.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(start).SetPeriodEnd(end).
		SetStatus(esportsdispatchprioritygrant.StatusPending).SaveX(ctx)

	if _, err := rebuildDispatchPriority(ctx, db, now, 100); err != nil {
		t.Fatalf("周期重算应成功，err=%v", err)
	}
	if got := db.EsportsDispatchPriorityGrant.GetX(ctx, pending.ID).Status; got != esportsdispatchprioritygrant.StatusActive {
		t.Fatalf("到点名单应转 active，got=%s", got)
	}
}

// seedGameSku 建一个归属某游戏分区的上架商品。
func seedGameSku(t *testing.T, db *ent.Client, code, game string) *ent.EsportsSku {
	t.Helper()
	ctx := t.Context()
	category, err := saveCategory(ctx, db, categoryInput{
		Code: "CAT-" + code, Name: "分类", Game: game, Status: "enabled",
	})
	if err != nil {
		t.Fatal(err)
	}
	sku, err := saveSku(ctx, db, skuInput{
		Code: code, Title: "商品", ServiceType: "play", CategoryID: &category.ID,
		UnitPriceCents: 1000, ServiceUnits: 1, ServiceUnitName: "局",
		RequiredSeats: 1, QuotaTotal: 10, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	return sku
}

// seedPendingOrderWithSku 建一张待派单单席位订单。
func seedPendingOrderWithSku(t *testing.T, db *ent.Client, orderNo string, skuID int) *ent.EsportsOrder {
	t.Helper()
	return db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(skuID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(t.Context())
}

// openPriorityWindow 直接给订单开优先通知窗口并逐人建通知，用于构造「窗口内」状态。
func openPriorityWindow(t *testing.T, db *ent.Client, order *ent.EsportsOrder,
	game string, playerIDs []int, until time.Time) {
	t.Helper()
	ctx := t.Context()
	db.EsportsOrder.UpdateOneID(order.ID).
		SetPriorityNoticeStatus(esportsorder.PriorityNoticeStatusPending).
		SetPriorityNoticeUntil(until.UTC()).SetPriorityGame(game).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).SaveX(ctx)
	for _, playerID := range playerIDs {
		db.EsportsDispatchPriorityNotice.Create().SetOrderID(order.ID).SetPlayerID(playerID).
			SetGame(game).SetNotifiedAt(time.Now().UTC()).SaveX(ctx)
	}
}

// 订单进入待派单时按名单开窗并逐人通知；再次回退待派单时直接释放到大厅，不二次开窗。
func TestOnOrderEnteredPendingDispatchOpensWindow(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_priority_hook")
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	listed := seedPlayer(t, db, 5001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	other := seedPlayer(t, db, 5002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "DP-HOOK", game)
	if _, err := grantDispatchPriority(ctx, db, 7, basePriorityInput(game, listed.ID), now); err != nil {
		t.Fatal(err)
	}
	order := db.EsportsOrder.Create().SetOrderNo("DP-HOOK-1").SetCustomerID(1).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.UTC().Add(time.Hour)).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingPayment).SaveX(ctx)

	// 模拟核销路径：先置待派单，再走统一入口。
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
	).SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).AddVersion(1).SaveX(ctx)
	if err = onOrderEnteredPendingDispatch(ctx, tx, order.ID, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("开窗应成功，err=%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	opened := db.EsportsOrder.GetX(ctx, order.ID)
	if opened.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusPending ||
		opened.PriorityGame != game || opened.PriorityNoticeUntil == nil ||
		!opened.PriorityNoticeUntil.After(now.UTC()) {
		t.Fatalf("应开优先通知窗口：%+v", opened)
	}
	// 只通知名单内的人。
	notices := db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID)).AllX(ctx)
	if len(notices) != 1 || notices[0].PlayerID != listed.ID ||
		notices[0].Status != esportsdispatchprioritynotice.StatusNotified {
		t.Fatalf("应只通知名单内陪玩：%+v", notices)
	}
	if db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.PlayerIDEQ(other.ID)).ExistX(ctx) {
		t.Fatal("名单外的陪玩不应收到优先通知")
	}
	// 通知通道：写事务外盒事件。
	if !db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.priority_notice")).ExistX(ctx) {
		t.Fatal("应写优先通知的事务外盒事件")
	}

	// 名单内拒绝导致回退待派单：不再二次开窗，直接释放到大厅。
	notice := notices[0]
	if _, err = declineDispatchPriorityNotice(ctx, db, listed.ID, notice.ID, "没空", now); err != nil {
		t.Fatalf("拒绝应成功，err=%v", err)
	}
	released := db.EsportsOrder.GetX(ctx, order.ID)
	if released.FulfillmentStatus != esportsorder.FulfillmentStatusGrabbing ||
		released.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusReleased {
		t.Fatalf("名单内全部拒绝后应释放到大厅：%+v", released)
	}
	// 所有人可见。
	hall, err := listGrabHall(ctx, db, other.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(hall) != 1 || hall[0].ID != order.ID {
		t.Fatalf("释放后应对所有人可见：%+v", hall)
	}
}

// 优先通知窗口内：只有被通知的陪玩在大厅能看到该单，其他人看不到。
func TestGrabHallScopedByPriorityNotice(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_priority_hall")
	ctx := t.Context()
	now := time.Now()
	listed := seedPlayer(t, db, 6001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	outsider := seedPlayer(t, db, 6002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "DP-HALL", "王者荣耀")
	order := seedPendingOrderWithSku(t, db, "DP-HALL-1", sku.ID)
	openPriorityWindow(t, db, order, "王者荣耀", []int{listed.ID}, now.Add(10*time.Minute))

	hallListed, err := listGrabHall(ctx, db, listed.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(hallListed) != 1 || hallListed[0].ID != order.ID {
		t.Fatalf("名单内陪玩应在大厅看到优先通知单：%+v", hallListed)
	}
	hallOutsider, err := listGrabHall(ctx, db, outsider.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(hallOutsider) != 0 {
		t.Fatalf("窗口内名单外陪玩不应看到该单：%+v", hallOutsider)
	}
	// 名单外陪玩直接抢单被拒。
	if _, err = grabPlayerOrder(ctx, db, outsider.ID, order.ID, now); !errors.Is(err, errDispatchPriorityHold) {
		t.Fatalf("窗口内名单外陪玩抢单应被拒，err=%v", err)
	}
	// 名单内抢单成功，窗口闭合、其余通知作废。
	if _, err = grabPlayerOrder(ctx, db, listed.ID, order.ID, now); err != nil {
		t.Fatalf("名单内抢单应成功，err=%v", err)
	}
	accepted := db.EsportsOrder.GetX(ctx, order.ID)
	if accepted.FulfillmentStatus != esportsorder.FulfillmentStatusAccepted ||
		accepted.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusAccepted {
		t.Fatalf("接单后应转已接单并闭合窗口：%+v", accepted)
	}
	notice := db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID)).OnlyX(ctx)
	if notice.Status != esportsdispatchprioritynotice.StatusAccepted {
		t.Fatalf("本人通知应转已接受：%+v", notice)
	}
}

// 客户指定陪玩的订单不进优先通知窗口，直接留在待派单池由客服指派，并在派单候选中置顶（§5.4）。
func TestPreferredPlayerSkipsPriorityWindow(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_preferred_skip")
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	listed := seedPlayer(t, db, 7001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	preferred := seedPlayer(t, db, 7002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "DP-PREF", game)
	if _, err := grantDispatchPriority(ctx, db, 7, basePriorityInput(game, listed.ID), now); err != nil {
		t.Fatal(err)
	}

	order := db.EsportsOrder.Create().SetOrderNo("DP-PREF-1").SetCustomerID(1).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).SetPreferredPlayerID(preferred.ID).
		SetPaymentDeadline(now.UTC().Add(time.Hour)).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingPayment).SaveX(ctx)

	// 模拟核销路径：先置待派单，再走统一入口。
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
	).SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).AddVersion(1).SaveX(ctx)
	if err = onOrderEnteredPendingDispatch(ctx, tx, order.ID, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("指定陪玩单进入待派单不应报错，err=%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch ||
		got.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusNone ||
		got.PriorityNoticeUntil != nil {
		t.Fatalf("指定陪玩单不应开优先通知窗口：%+v", got)
	}
	if db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID)).ExistX(ctx) {
		t.Fatal("指定陪玩单不应产生优先通知")
	}
	// 派单候选：订单上的指定陪玩应置顶。
	candidates, err := dispatchCandidates(ctx, db, got, preferred.ID, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) == 0 || !candidates[0].Preferred || candidates[0].PlayerID != preferred.ID {
		t.Fatalf("指定陪玩应在候选首位：%+v", candidates)
	}
}

// 名单内全部拒绝 → 释放到大厅；名单外陪玩不能拒绝别人的通知。
func TestDeclinePriorityNoticeReleasesToHall(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_priority_decline")
	ctx := t.Context()
	now := time.Now()
	first := seedPlayer(t, db, 7001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	second := seedPlayer(t, db, 7002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	outsider := seedPlayer(t, db, 7003, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "DP-DECLINE", "王者荣耀")
	order := seedPendingOrderWithSku(t, db, "DP-DECLINE-1", sku.ID)
	openPriorityWindow(t, db, order, "王者荣耀", []int{first.ID, second.ID}, now.Add(10*time.Minute))

	notices := db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID)).
		Order(ent.Asc(esportsdispatchprioritynotice.FieldID)).AllX(ctx)
	// 名单外陪玩不能拒绝他人的通知。
	if _, err := declineDispatchPriorityNotice(ctx, db, outsider.ID, notices[0].ID, "", now); !errors.Is(err, errForbidden) {
		t.Fatalf("名单外陪玩拒绝应被拒，err=%v", err)
	}
	// 有人拒绝但名单内仍有人待回应 → 订单留在待派单。
	if _, err := declineDispatchPriorityNotice(ctx, db, first.ID, notices[0].ID, "在忙", now); err != nil {
		t.Fatalf("拒绝应成功，err=%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID).FulfillmentStatus; got != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("名单内仍有人待回应时订单应留在待派单，got=%s", got)
	}
	// 重复拒绝冲突。
	if _, err := declineDispatchPriorityNotice(ctx, db, first.ID, notices[0].ID, "再拒一次", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复拒绝应冲突，err=%v", err)
	}
	// 最后一人拒绝 → 释放到大厅。
	if _, err := declineDispatchPriorityNotice(ctx, db, second.ID, notices[1].ID, "没空", now); err != nil {
		t.Fatalf("最后一人拒绝应成功，err=%v", err)
	}
	released := db.EsportsOrder.GetX(ctx, order.ID)
	if released.FulfillmentStatus != esportsorder.FulfillmentStatusGrabbing ||
		released.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusReleased ||
		released.PriorityReleaseReason != dispatchReleaseReasonDeclined {
		t.Fatalf("全部拒绝后应释放到大厅并留原因：%+v", released)
	}
	if db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID),
		esportsdispatchprioritynotice.StatusEQ(esportsdispatchprioritynotice.StatusNotified),
	).ExistX(ctx) {
		t.Fatal("释放后不应仍有待回应通知")
	}
}

// 超过配置的通知时长 → 任务把订单释放到大厅。
func TestExpirePriorityNoticeReleasesToHall(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_priority_expire")
	ctx := t.Context()
	now := time.Now()
	listed := seedPlayer(t, db, 8001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	outsider := seedPlayer(t, db, 8002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "DP-EXPIRE", "王者荣耀")
	order := seedPendingOrderWithSku(t, db, "DP-EXPIRE-1", sku.ID)
	// 窗口已过截止时间。
	openPriorityWindow(t, db, order, "王者荣耀", []int{listed.ID}, now.Add(-time.Minute))

	if hall, err := listGrabHall(ctx, db, outsider.ID, now); err != nil {
		t.Fatal(err)
	} else if len(hall) != 0 {
		t.Fatalf("窗口超时前名单外不应看到：%+v", hall)
	}
	count, err := expireDispatchPriorityNotices(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("超时释放应成功，err=%v", err)
	}
	if count != 1 {
		t.Fatalf("应释放 1 单，got=%d", count)
	}
	released := db.EsportsOrder.GetX(ctx, order.ID)
	if released.FulfillmentStatus != esportsorder.FulfillmentStatusGrabbing ||
		released.PriorityReleaseReason != dispatchReleaseReasonExpired {
		t.Fatalf("超时后应释放到大厅并留原因：%+v", released)
	}
	if db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID),
		esportsdispatchprioritynotice.StatusEQ(esportsdispatchprioritynotice.StatusNotified),
	).ExistX(ctx) {
		t.Fatal("超时后待回应通知应转作废")
	}
	// 释放后名单外陪玩可正常抢。
	if hall, err := listGrabHall(ctx, db, outsider.ID, now); err != nil {
		t.Fatal(err)
	} else if len(hall) != 1 {
		t.Fatalf("释放后应对所有人可见：%+v", hall)
	}
	if _, err = grabPlayerOrder(ctx, db, outsider.ID, order.ID, now); err != nil {
		t.Fatalf("释放后名单外陪玩应可抢单，err=%v", err)
	}
	// 幂等：再次运行不应重复处理。
	if again, err := expireDispatchPriorityNotices(ctx, db, now, 100); err != nil {
		t.Fatal(err)
	} else if again != 0 {
		t.Fatalf("超时释放应幂等，got=%d", again)
	}
}

// 派单候选的可见性由窗口决定：窗口内只列被通知者，窗口外列全部合格在线陪玩。
func TestDispatchCandidatesWindowScoped(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_priority_candidates")
	ctx := t.Context()
	now := time.Now()
	listed := seedPlayer(t, db, 9001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	outsider := seedPlayer(t, db, 9002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	offline := seedPlayer(t, db, 9003, esportsplayer.QualificationQualified, esportsplayer.PresenceOffline)
	pendingPlayer := seedPlayer(t, db, 9004, esportsplayer.QualificationPending, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "DP-CAND", "王者荣耀")
	order := seedPendingOrderWithSku(t, db, "DP-CAND-1", sku.ID)
	openPriorityWindow(t, db, order, "王者荣耀", []int{listed.ID}, now.Add(10*time.Minute))

	inside, err := dispatchCandidates(ctx, db, db.EsportsOrder.GetX(ctx, order.ID), 0, 20, now)
	if err != nil {
		t.Fatalf("窗口内候选应成功，err=%v", err)
	}
	if len(inside) != 1 || inside[0].PlayerID != listed.ID || !inside[0].FromPriorityNotice {
		t.Fatalf("窗口内只应列出被通知的名单内陪玩：%+v", inside)
	}

	// 释放到大厅后：全部合格在线陪玩（离线与未合格仍排除）。
	if _, err = releaseDispatchPriorityNotice(ctx, db, order.ID, "", now); err != nil {
		t.Fatal(err)
	}
	opened := db.EsportsOrder.GetX(ctx, order.ID)
	after, err := dispatchCandidates(ctx, db, opened, 0, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatalf("窗口结束后应列出 2 名合格在线陪玩，got=%d", len(after))
	}
	for _, candidate := range after {
		if candidate.FromPriorityNotice {
			t.Fatalf("窗口结束后不应标记来自优先通知：%+v", candidate)
		}
		if candidate.PlayerID == offline.ID || candidate.PlayerID == pendingPlayer.ID {
			t.Fatalf("不应包含离线或未合格的陪玩：%+v", candidate)
		}
	}
	// 客服指定陪玩置顶（仅提示）。
	preferred, err := dispatchCandidates(ctx, db, opened, outsider.ID, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	if preferred[0].PlayerID != outsider.ID || !preferred[0].Preferred {
		t.Fatalf("指定陪玩应排第一：%+v", preferred[0])
	}
	// 已占席位的陪玩不再列出。
	db.EsportsDispatchSeat.Create().SetOrderID(order.ID).SetSeatIndex(1).
		SetPlayerID(outsider.ID).SetStatus(esportsdispatchseat.StatusInvited).
		SetSource(esportsdispatchseat.SourceAssign).SaveX(ctx)
	afterSeat, err := dispatchCandidates(ctx, db, opened, 0, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range afterSeat {
		if candidate.PlayerID == outsider.ID {
			t.Fatal("已占席位的陪玩不应再出现在候选中")
		}
	}
}

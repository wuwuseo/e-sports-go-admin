package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/migrate"
)

// seedPaidOrder 直接构造一张已付款、待派单的订单，供排单用例使用。
func seedPaidOrder(t *testing.T, db *ent.Client, orderNo string, requiredSeats int) *ent.EsportsOrder {
	t.Helper()
	return db.EsportsOrder.Create().
		SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(requiredSeats).
		SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(t.Context())
}

func seedPlayer(t *testing.T, db *ent.Client, userID int, qualification esportsplayer.Qualification, presence esportsplayer.Presence) *ent.EsportsPlayer {
	t.Helper()
	return db.EsportsPlayer.Create().SetUserID(userID).SetDisplayName("陪玩").
		SetQualification(qualification).SetPresence(presence).SaveX(t.Context())
}

func countEventsByType(t *testing.T, db *ent.Client) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, event := range db.EsportsOrderEvent.Query().AllX(t.Context()) {
		counts[event.EventType]++
	}
	return counts
}

// 指派、撤回必须原子更新席位与订单状态，并写事件留痕；未合格陪玩与重复指派被拦截。
func TestDispatchAssignRevokeAndGuards(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_dispatch_assign?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedPaidOrder(t, db, "dispatch-assign-1", 1)
	online := seedPlayer(t, db, 101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	pending := seedPlayer(t, db, 102, esportsplayer.QualificationPending, esportsplayer.PresenceOnline)

	seat, err := assignDispatchSeat(ctx, db, order.ID, 1, online.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if seat.Status != esportsdispatchseat.StatusInvited || seat.PlayerID == nil || *seat.PlayerID != online.ID ||
		seat.Source != esportsdispatchseat.SourceAssign {
		t.Fatalf("指派结果不正确：%+v", seat)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance {
		t.Fatalf("订单未转待接单：%+v", got)
	}
	if countEventsByType(t, db)["order.seat_invited"] != 1 {
		t.Fatal("缺少 order.seat_invited 事件")
	}

	if _, err := assignDispatchSeat(ctx, db, order.ID, 1, pending.ID, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("不合格陪玩被放行：%v", err)
	}
	if _, err := assignDispatchSeat(ctx, db, order.ID, 1, online.ID, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复指派已邀请席位未拦截：%v", err)
	}

	revoked, err := revokeDispatchSeat(ctx, db, order.ID, 1, 11, "客户临时取消")
	if err != nil {
		t.Fatal(err)
	}
	// 撤回后订单退回待派单，席位同步重置为可重新指派；历史由 order.seat_revoked 事件保留。
	if revoked.Status != esportsdispatchseat.StatusOpen || revoked.PlayerID != nil ||
		revoked.RespondedAt != nil {
		t.Fatalf("撤回未把席位释放为可派单：%+v", revoked)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("订单未回退待派单：%+v", got)
	}
	if countEventsByType(t, db)["order.seat_revoked"] != 1 {
		t.Fatal("缺少 order.seat_revoked 事件")
	}
	if _, err := revokeDispatchSeat(ctx, db, order.ID, 1, 11, "重复撤回"); !errors.Is(err, errConflict) {
		t.Fatalf("重复撤回未拦截：%v", err)
	}
	if _, err := revokeDispatchSeat(ctx, db, order.ID, 1, 11, "   "); !errors.Is(err, errInvalidInput) {
		t.Fatalf("空白原因未拦截：%v", err)
	}
}

// 公共池认领只能成功一次；转单与催单写事件且不改错状态。
func TestDispatchClaimTransferAndRemind(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_dispatch_claim?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	order := seedPaidOrder(t, db, "dispatch-claim-1", 1)
	claimed, err := claimOrder(ctx, db, order.ID, 11)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ResponsibleAdminID == nil || *claimed.ResponsibleAdminID != 11 {
		t.Fatalf("认领未写入负责客服：%+v", claimed)
	}
	if _, err := claimOrder(ctx, db, order.ID, 12); !errors.Is(err, errConflict) {
		t.Fatalf("重复认领未拦截：%v", err)
	}

	transferred, err := transferOrder(ctx, db, order.ID, 22, 11, "客服交接")
	if err != nil {
		t.Fatal(err)
	}
	if transferred.ResponsibleAdminID == nil || *transferred.ResponsibleAdminID != 22 {
		t.Fatalf("转单未改负责客服：%+v", transferred)
	}

	if err := remindOrder(ctx, db, order.ID, 11, 9, "越界级别"); !errors.Is(err, errInvalidInput) {
		t.Fatalf("催单级别越界未拦截：%v", err)
	}
	if err := remindOrder(ctx, db, order.ID, 11, 2, "请尽快接单"); err != nil {
		t.Fatal(err)
	}
	counts := countEventsByType(t, db)
	if counts["order.claimed"] != 1 || counts["order.transferred"] != 1 || counts["order.reminded"] != 1 {
		t.Fatalf("事件留痕不完整：%+v", counts)
	}
}

// 邀请到期由系统任务转为 expired 并把订单退回待派单，重复调用不重复处理。
func TestDispatchInviteExpiresOnce(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_dispatch_expire?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedPaidOrder(t, db, "dispatch-expire-1", 1)
	online := seedPlayer(t, db, 201, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	if _, err := assignDispatchSeat(ctx, db, order.ID, 1, online.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if n, err := expireDispatchInvites(ctx, db, now.Add(5*time.Minute), 100); err != nil || n != 0 {
		t.Fatalf("未到期邀请被处理：%d %v", n, err)
	}
	if n, err := expireDispatchInvites(ctx, db, now.Add(11*time.Minute), 100); err != nil || n != 1 {
		t.Fatalf("过期邀请未被处理：%d %v", n, err)
	}
	seat := db.EsportsDispatchSeat.Query().OnlyX(ctx)
	// 过期后订单退回待派单，席位同步重置为可重新指派；历史由 order.seat_expired 事件保留。
	if seat.Status != esportsdispatchseat.StatusOpen || seat.PlayerID != nil || seat.RespondedAt != nil {
		t.Fatalf("过期席位未重置为可派单：%+v", seat)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("订单未回退待派单：%+v", got)
	}
	if n, err := expireDispatchInvites(ctx, db, now.Add(20*time.Minute), 100); err != nil || n != 0 {
		t.Fatalf("重复过期未被忽略：%d %v", n, err)
	}
	if countEventsByType(t, db)["order.seat_expired"] != 1 {
		t.Fatal("缺少 order.seat_expired 事件")
	}
}

// 普通待派单订单可由客服发布到抢单大厅：多人单、未付款单与重复发布都被拦截。
func TestPublishOrderToGrabHallGuards(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_publish_grab?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedPaidOrder(t, db, "publish-grab-1", 1)
	published, err := publishOrderToGrabHall(ctx, db, order.ID, 11, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if published.FulfillmentStatus != esportsorder.FulfillmentStatusGrabbing ||
		published.PriorityReleaseReason != dispatchReleaseReasonPublish {
		t.Fatalf("发布结果不正确：%+v", published)
	}
	if countEventsByType(t, db)["order.published_to_grab_hall"] != 1 {
		t.Fatal("缺少 order.published_to_grab_hall 事件")
	}
	if _, err := publishOrderToGrabHall(ctx, db, order.ID, 11, "", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复发布未拦截：%v", err)
	}

	multi := seedPaidOrder(t, db, "publish-grab-multi", 2)
	if _, err := publishOrderToGrabHall(ctx, db, multi.ID, 11, "", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("多人订单被放行：%v", err)
	}

	unpaid := db.EsportsOrder.Create().
		SetOrderNo("publish-grab-unpaid").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).SaveX(ctx)
	if _, err := publishOrderToGrabHall(ctx, db, unpaid.ID, 11, "", now); !errors.Is(err, errConflict) {
		t.Fatalf("未付款订单被放行：%v", err)
	}
}

// 席位被撤回后订单回退待派单，发布到大厅应把席位重置为 open 并对所有陪玩可见。
func TestPublishOrderToGrabHallReopensSeats(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_publish_grab_seat?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedPaidOrder(t, db, "publish-grab-seat", 1)
	online := seedPlayer(t, db, 301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	if _, err := assignDispatchSeat(ctx, db, order.ID, 1, online.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err := revokeDispatchSeat(ctx, db, order.ID, 1, 11, "客户临时取消"); err != nil {
		t.Fatal(err)
	}

	if _, err := publishOrderToGrabHall(ctx, db, order.ID, 11, "客服发布到大厅", now); err != nil {
		t.Fatal(err)
	}
	seat := db.EsportsDispatchSeat.Query().OnlyX(ctx)
	if seat.Status != esportsdispatchseat.StatusOpen || seat.PlayerID != nil || seat.ExpiresAt != nil {
		t.Fatalf("席位未重置为可抢：%+v", seat)
	}
	hall, err := listGrabHall(ctx, db, online.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(hall) != 1 || hall[0].ID != order.ID {
		t.Fatalf("抢单大厅未见到该订单：%+v", hall)
	}
}

// 会员优先等级客户的待派单订单在看板置顶并被标记，非会员订单保持原顺序。
func TestDispatchBoardPrioritizesMemberOrders(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_member_priority_board?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	seedMemberLevel(t, db, memberLevelInput{
		Code: "gold", Name: "金卡", GrowthThreshold: 0, DiscountBps: 9500, PriorityDispatch: true,
	})
	normal := saveCustomerOrFail(t, db, 11)
	member := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(member.ID).SetMemberLevelCode("gold").SaveX(ctx)

	seed := func(orderNo string, customerID int) *ent.EsportsOrder {
		return db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(customerID).SetSkuID(1).
			SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
			SetPromisedServiceUnits(1).SetRequiredSeats(1).
			SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
			SetPaymentStatus(esportsorder.PaymentStatusPaid).
			SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).SaveX(ctx)
	}
	// 会员单先建（ID 更小），按 ID 倒序本应排在后；会员优先后应置顶。
	memberOrder := seed("MB-PRIO-1", member.ID)
	normalOrder := seed("MB-PRIO-2", normal.ID)

	board, err := loadDispatchBoard(ctx, db, 11, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.PendingDispatch) != 2 || board.PendingDispatch[0].ID != memberOrder.ID ||
		board.PendingDispatch[1].ID != normalOrder.ID {
		t.Fatalf("会员订单未在看板置顶：%+v", board.PendingDispatch)
	}
	if len(board.MemberPriorityOrderIDs) != 1 || board.MemberPriorityOrderIDs[0] != memberOrder.ID {
		t.Fatalf("会员订单标记不正确：%+v", board.MemberPriorityOrderIDs)
	}

	// 等级未开启派单优先时不再置顶，也不产生标记。
	db.EsportsMemberLevelRule.Update().SetPriorityDispatch(false).SaveX(ctx)
	plain, err := loadDispatchBoard(ctx, db, 11, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.MemberPriorityOrderIDs) != 0 ||
		plain.PendingDispatch[0].ID != normalOrder.ID {
		t.Fatalf("未开启派单优先的等级不应置顶：%+v", plain.PendingDispatch)
	}
}

// 公共池脱敏：非超管看公共池只得到客户/金额掩码的摘要，超管看完整数据。
func TestDispatchBoardPublicPoolMasking(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_dispatch_pool_mask?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	order := seedPaidOrder(t, db, "dispatch-pool-mask-1", 1)

	// 非超管：公共池订单被掩码，并列入 masked_public_pool_ids。
	board, err := loadDispatchBoard(ctx, db, 11, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.PublicPool) != 1 {
		t.Fatalf("公共池应有 1 条，got=%d", len(board.PublicPool))
	}
	masked := board.PublicPool[0]
	if masked.CustomerID != 0 || masked.PayableCents != 0 || masked.UnitPriceCents != 0 {
		t.Fatalf("非超管公共池未掩码：%+v", masked)
	}
	found := false
	for _, id := range board.MaskedPublicPoolIDs {
		if id == order.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("缺少掩码标记：%v", board.MaskedPublicPoolIDs)
	}

	// 超管：看完整数据，无掩码标记。
	super, err := loadDispatchBoard(ctx, db, 11, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(super.PublicPool) != 1 {
		t.Fatalf("超管公共池应有 1 条，got=%d", len(super.PublicPool))
	}
	full := super.PublicPool[0]
	if full.CustomerID != 1 || full.PayableCents != 1000 || full.UnitPriceCents != 1000 {
		t.Fatalf("超管应看完整数据：%+v", full)
	}
	if len(super.MaskedPublicPoolIDs) != 0 {
		t.Fatalf("超管不应有掩码标记：%v", super.MaskedPublicPoolIDs)
	}
}

package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsservicesegment"
)

// seedMemberCommissionOrder 构造一张余额核销的待结单订单、两名成员的服务段，
// 并直接写入各自 service 分成明细，聚焦退款冲回口径。
func seedMemberCommissionOrder(t *testing.T, db *ent.Client, suffix string, p1, p2 int64, now time.Time) int {
	t.Helper()
	ctx := t.Context()
	order := seedBalancePaidOrder(t, db, suffix)
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	for i, player := range []int{1, 2} {
		db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(player).
			SetSeatIndex(i + 1).SetStatus(esportsservicesegment.StatusFinished).
			SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).
			SetSystemSeconds(600).SetEvidenceRef("ev.png").SaveX(ctx)
	}
	db.EsportsCommissionEntry.Create().
		SetIdemKey("commission:service:" + suffix + ":1").
		SetOrderID(order.ID).SetPlayerID(1).
		SetKind(esportscommissionentry.KindService).SetSeatIndex(1).
		SetBaseCents(1000).SetAmountCents(p1).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)
	db.EsportsCommissionEntry.Create().
		SetIdemKey("commission:service:" + suffix + ":2").
		SetOrderID(order.ID).SetPlayerID(2).
		SetKind(esportscommissionentry.KindService).SetSeatIndex(2).
		SetBaseCents(1000).SetAmountCents(p2).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)
	return order.ID
}

// 指定争议陪玩的部分退款（成员明细冲回）：先冲满该成员分成，剩余目标再分摊其他成员；
// 再次退款时累计冲回不超过该成员原分成；争议陪玩须为实际服务成员。
func TestPartialRefundPrefersDisputedMemberReversal(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_member_reversal")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	orderID := seedMemberCommissionOrder(t, db, "member-rev", 200, 300, now)

	// 非服务成员不能被指定为争议陪玩。
	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: orderID, Kind: "refund_request", Channel: "customer",
		Demand: "客户反馈服务与约定不符，申请退款", ExpectedPlan: "按差额退款",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptAfterSale(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err := submitAfterSalePlan(ctx, db, created.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: 1000,
		DisputePlayerID: 999, Resolution: "指定非服务成员",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("指定非服务成员为争议陪玩未拦截：%v", err)
	}

	// 第一次退款 1000/2000：冲回目标 250 全部由争议成员（陪玩 2）承担。
	first, err := submitAfterSalePlan(ctx, db, created.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: 1000,
		DisputePlayerID: 2, Resolution: "陪玩 2 服务问题，优先冲回其分成",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlayerID == nil || *first.PlayerID != 2 {
		t.Fatalf("售后单未记录争议陪玩：%+v", first)
	}
	if _, err := confirmAfterSaleByAdmin(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err := approveAfterSale(ctx, db, created.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAfterSale(ctx, db, created.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	reversalByPlayer := func() map[int]int64 {
		rows := db.EsportsCommissionEntry.Query().Where(
			esportscommissionentry.KindEQ(esportscommissionentry.KindReversal),
		).AllX(ctx)
		sums := map[int]int64{}
		for _, row := range rows {
			sums[row.PlayerID] += row.AmountCents
		}
		return sums
	}
	if sums := reversalByPlayer(); sums[2] != -250 || sums[1] != 0 {
		t.Fatalf("首次冲回应全部由争议成员承担（期望 2→-250、1→0）：%v", sums)
	}

	// 第二次退款 900/2000：目标 225，陪玩 2 仅剩 50 可冲，剩余 175 分摊给陪玩 1。
	second, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: orderID, Kind: "service_mismatch", Channel: "admin",
		Demand: "客户再次反馈服务与约定不符", ExpectedPlan: "按差额退款",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptAfterSale(ctx, db, second.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err := submitAfterSalePlan(ctx, db, second.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: 900,
		DisputePlayerID: 2, Resolution: "继续由陪玩 2 承担",
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := confirmAfterSaleByAdmin(ctx, db, second.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err := approveAfterSale(ctx, db, second.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAfterSale(ctx, db, second.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	sums := reversalByPlayer()
	if sums[2] != -300 || sums[1] != -175 {
		t.Fatalf("二次冲回应封顶争议成员原分成并把余额分摊他人（期望 2→-300、1→-175）：%v", sums)
	}
	if total := sums[1] + sums[2]; total != -475 {
		t.Fatalf("两次退款合计冲回应为 500*1900/2000=475，got=%d", total)
	}
}

// 未指定争议陪玩时维持整体比例冲回，且退款冲回的幂等与明细归属不变。
func TestRefundWithoutDisputePlayerStaysProportional(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_member_reversal_prop")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	orderID := seedMemberCommissionOrder(t, db, "member-rev-prop", 200, 300, now)
	item := newRefundAfterSale(t, db, orderID, "refund_request", 1000, 11, 12, now)
	if item.PlayerID != nil {
		t.Fatalf("未指定争议陪玩不应写入 player_id：%+v", item)
	}
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	rows := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal),
	).AllX(ctx)
	sums := map[int]int64{}
	for _, row := range rows {
		sums[row.PlayerID] += row.AmountCents
	}
	// 目标 250 按各自分成比例：陪玩 1 → 100，陪玩 2 → 150。
	if sums[1] != -100 || sums[2] != -150 {
		t.Fatalf("未指定争议陪玩应按比例冲回（期望 1→-100、2→-150）：%v", sums)
	}
}

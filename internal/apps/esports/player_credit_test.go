package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayercreditevent"
)

// playerFaultAfterSale 构造一张责任方为陪玩、含追偿金额并执行完成的售后单（+ ）。
func playerFaultAfterSale(t *testing.T, db *ent.Client, order *ent.EsportsOrder, kind string,
	playerID int, refundCents, recoveryCents int64, now time.Time) *ent.EsportsAfterSale {
	t.Helper()
	ctx := t.Context()
	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: kind, Channel: "customer",
		Demand: "客户投诉陪玩未按约到场", ExpectedPlan: "退款并向陪玩追偿",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptAfterSale(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err = submitAfterSalePlan(ctx, db, created.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: refundCents,
		RecoveryCents: recoveryCents, RecoveryPlayerID: playerID,
		Resolution: "陪玩未到场，退款并向责任陪玩追偿",
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = confirmAfterSaleByAdmin(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err = approveAfterSale(ctx, db, created.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	done, err := executeAfterSale(ctx, db, created.ID, 12, now)
	if err != nil || done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("执行含追偿的售后失败：%v %+v", err, done)
	}
	return done
}

// 陪玩责任信用联动：责任方为陪玩的售后执行后自动只增记载，复核决定是否暂停接单。
func TestAfterSalePlayerFaultRecordsPlayerCredit(t *testing.T) {
	db := openAfterSaleDB(t, "esports_player_credit")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	player := seedPlayer(t, db, 4501, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedBalancePaidOrder(t, db, "credit")
	item := playerFaultAfterSale(t, db, order, "no_show", player.ID, 500, 300, now)

	events := db.EsportsPlayerCreditEvent.Query().AllX(ctx)
	if len(events) != 1 {
		t.Fatalf("责任成立未自动记载陪玩信用：%+v", events)
	}
	event := events[0]
	if event.PlayerID != player.ID || event.OrderID != order.ID || event.AfterSaleID != item.ID ||
		event.Kind != esportsplayercreditevent.KindFaultAdmitted ||
		event.AmountCents != 300 || event.Status != esportsplayercreditevent.StatusRecorded {
		t.Fatalf("信用记载内容不正确：%+v", event)
	}
	// 自动记载不处罚：资格保持不变。
	if got := db.EsportsPlayer.GetX(ctx, player.ID); got.Qualification != esportsplayer.QualificationQualified {
		t.Fatalf("自动记载不应改变接单资格：%+v", got)
	}

	// 复核参数校验：penalty 必须带说明，动作枚举受控。
	if _, err := reviewPlayerCreditEvent(ctx, db, event.ID, 12,
		playerCreditReviewInput{Action: "penalty"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("处罚复核缺少说明未拦截：%v", err)
	}
	if _, err := reviewPlayerCreditEvent(ctx, db, event.ID, 12,
		playerCreditReviewInput{Action: "unknown", Note: "乱填"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法复核动作未拦截：%v", err)
	}
	penalized, err := reviewPlayerCreditEvent(ctx, db, event.ID, 12,
		playerCreditReviewInput{Action: "penalty", Note: "确认陪玩责任，暂停接单"}, now)
	if err != nil || penalized.Status != esportsplayercreditevent.StatusPenaltyApplied ||
		penalized.ReviewedByAdminID == nil || *penalized.ReviewedByAdminID != 12 || penalized.ReviewedAt == nil {
		t.Fatalf("处罚复核失败：%v %+v", err, penalized)
	}
	if got := db.EsportsPlayer.GetX(ctx, player.ID); got.Qualification != esportsplayer.QualificationSuspended {
		t.Fatalf("处罚复核未暂停接单资格：%+v", got)
	}
	// 同一记载不得重复复核。
	if _, err := reviewPlayerCreditEvent(ctx, db, event.ID, 13,
		playerCreditReviewInput{Action: "clear", Note: "反悔"}, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复复核未拦截：%v", err)
	}

	// 第二条记载走 clear：判定不构成责任，不改动资格。
	db.EsportsPlayer.UpdateOneID(player.ID).SetQualification(esportsplayer.QualificationQualified).ExecX(ctx)
	second := playerFaultAfterSale(t, db, order, "service_mismatch", player.ID, 400, 200, now)
	records := db.EsportsPlayerCreditEvent.Query().AllX(ctx)
	if len(records) != 2 {
		t.Fatalf("第二条责任未记载：%+v", records)
	}
	if records[1].AmountCents != 200 || records[1].AfterSaleID != second.ID {
		t.Fatalf("第二条信用记载内容不正确：%+v", records[1])
	}
	cleared, err := reviewPlayerCreditEvent(ctx, db, records[1].ID, 13,
		playerCreditReviewInput{Action: "clear", Note: "核实为客户端网络问题，不构成责任"}, now)
	if err != nil || cleared.Status != esportsplayercreditevent.StatusCleared {
		t.Fatalf("消除复核失败：%v %+v", err, cleared)
	}
	if got := db.EsportsPlayer.GetX(ctx, player.ID); got.Qualification != esportsplayer.QualificationQualified {
		t.Fatalf("消除复核不应改动接单资格：%+v", got)
	}
	if second.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("第二张售后未完成：%+v", second)
	}

	// 列表与过滤：可按陪玩与状态查询，非法状态被拒。
	if rows, err := listPlayerCreditEvents(ctx, db, player.ID, "", 10); err != nil || len(rows) != 2 {
		t.Fatalf("按陪玩查询信用记载不正确：%+v %v", rows, err)
	}
	if rows, err := listPlayerCreditEvents(ctx, db, player.ID, "cleared", 10); err != nil || len(rows) != 1 {
		t.Fatalf("按状态查询信用记载不正确：%+v %v", rows, err)
	}
	if rows, err := listPlayerCreditEvents(ctx, db, 0, "", 10); err != nil || len(rows) != 2 {
		t.Fatalf("不带陪玩过滤的查询不正确：%+v %v", rows, err)
	}
	if _, err := listPlayerCreditEvents(ctx, db, 0, "unknown", 10); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法状态过滤未拦截：%v", err)
	}
}

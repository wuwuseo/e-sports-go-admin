package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayerrecovery"
)

// 陪玩追偿：售后执行按方案生成待审批追偿单 → 非申请人审批 → 从猫粮扣回；不得自审、重复执行冲突。
func TestPlayerRecoveryLifecycle(t *testing.T) {
	db := openAfterSaleDB(t, "esports_player_recovery")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedBalancePaidOrder(t, db, "recovery")
	player := seedPlayer(t, db, 4401, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	if _, err := walletRecharge(ctx, db, 11, walletPlayerCatfood, player.ID,
		walletInput{PrincipalCents: 5000, Note: "结算入账"}, "recovery-seed"); err != nil {
		t.Fatal(err)
	}
	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "duration_short", Channel: "customer",
		Demand: "时长不足，要求退款并追偿", ExpectedPlan: "按差额退款",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptAfterSale(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}

	// 追偿校验：责任方非陪玩 / 未指定陪玩 / 追偿超赔付额 均被拒。
	invalid := []afterSalePlanInput{
		{FaultParty: "platform", PlanAction: "refund_original", RefundCents: 1000,
			RecoveryPlayerID: player.ID, RecoveryCents: 500, Resolution: "责任方非陪玩"},
		{FaultParty: "player", PlanAction: "refund_original", RefundCents: 1000,
			RecoveryCents: 500, Resolution: "未指定陪玩"},
		{FaultParty: "player", PlanAction: "refund_original", RefundCents: 1000,
			RecoveryPlayerID: player.ID, RecoveryCents: 2000, Resolution: "追偿超赔付额"},
	}
	for i, in := range invalid {
		if _, err = submitAfterSalePlan(ctx, db, created.ID, 11, in, now); !errors.Is(err, errInvalidInput) {
			t.Fatalf("第 %d 组非法追偿方案未拦截：%v", i+1, err)
		}
	}

	planned, err := submitAfterSalePlan(ctx, db, created.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: 1000,
		RecoveryPlayerID: player.ID, RecoveryCents: 600, Resolution: "陪玩责任，退款并追偿",
	}, now)
	if err != nil || planned.RecoveryCents != 600 || planned.RecoveryPlayerID == nil ||
		*planned.RecoveryPlayerID != player.ID {
		t.Fatalf("追偿方案提交失败：%v %+v", err, planned)
	}
	if _, err = confirmAfterSaleByAdmin(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err = approveAfterSale(ctx, db, created.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if _, err = executeAfterSale(ctx, db, created.ID, 12, now); err != nil {
		t.Fatal(err)
	}

	recoveries := db.EsportsPlayerRecovery.Query().AllX(ctx)
	if len(recoveries) != 1 {
		t.Fatalf("售后执行应生成一条追偿明细：%d", len(recoveries))
	}
	recovery := recoveries[0]
	if recovery.PlayerID != player.ID || recovery.AmountCents != 600 ||
		recovery.Status != esportsplayerrecovery.StatusPending || recovery.AfterSaleID != created.ID {
		t.Fatalf("追偿明细不正确：%+v", recovery)
	}
	// 申请人（执行人 12）不得自审。
	if _, err = approvePlayerRecovery(ctx, db, recovery.ID, 12, now); !errors.Is(err, errForbidden) {
		t.Fatalf("追偿自审未拦截：%v", err)
	}
	approved, err := approvePlayerRecovery(ctx, db, recovery.ID, 13, now)
	if err != nil || approved.Status != esportsplayerrecovery.StatusApproved {
		t.Fatalf("追偿审批失败：%v %+v", err, approved)
	}
	recovered, err := executePlayerRecovery(ctx, db, recovery.ID, 13, now)
	if err != nil || recovered.Status != esportsplayerrecovery.StatusRecovered || recovered.WalletTxID == nil {
		t.Fatalf("追偿执行失败：%v %+v", err, recovered)
	}
	balance, err := walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID)
	if err != nil || balance.total() != 4400 {
		t.Fatalf("猫粮应扣到 4400：%v %+v", err, balance)
	}
	if _, err = executePlayerRecovery(ctx, db, recovery.ID, 13, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复执行应冲突：%v", err)
	}
}

// 追偿执行：猫粮余额不足时拒绝（可待结算后再执行）；驳回进入终态。
func TestPlayerRecoveryBalanceAndReject(t *testing.T) {
	db := openAfterSaleDB(t, "esports_player_recovery_guard")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	player := seedPlayer(t, db, 4402, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	item, err := createPlayerRecoveryIn(ctx, tx, player.ID, 0, 0, 800, "测试追偿", 11)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = approvePlayerRecovery(ctx, db, item.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if _, err = executePlayerRecovery(ctx, db, item.ID, 12, now); !errors.Is(err, errInsufficientBalance) {
		t.Fatalf("猫粮不足应被拒：%v", err)
	}
	if _, err = walletRecharge(ctx, db, 11, walletPlayerCatfood, player.ID,
		walletInput{PrincipalCents: 800, Note: "结算入账"}, "recovery-guard-seed"); err != nil {
		t.Fatal(err)
	}
	if _, err = executePlayerRecovery(ctx, db, item.ID, 12, now); err != nil {
		t.Fatalf("补足余额后应可执行：%v", err)
	}

	// 驳回：申请人不得驳回本人的追偿单，驳回须填原因，通过后进入终态。
	tx, err = db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rejectedItem, err := createPlayerRecoveryIn(ctx, tx, player.ID, 0, 0, 100, "测试驳回", 11)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = rejectPlayerRecovery(ctx, db, rejectedItem.ID, 11, "理由", now); !errors.Is(err, errForbidden) {
		t.Fatalf("申请人驳回未拦截：%v", err)
	}
	if _, err = rejectPlayerRecovery(ctx, db, rejectedItem.ID, 12, "  ", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未填驳回原因未拦截：%v", err)
	}
	done, err := rejectPlayerRecovery(ctx, db, rejectedItem.ID, 12, "责任判定有误", now)
	if err != nil || done.Status != esportsplayerrecovery.StatusRejected ||
		done.RejectReason != "责任判定有误" {
		t.Fatalf("驳回失败：%v %+v", err, done)
	}
}

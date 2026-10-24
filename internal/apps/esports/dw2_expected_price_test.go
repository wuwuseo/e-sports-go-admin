package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsplayer"
)

// ：陪玩可自助提交「期望报价」，状态先落 pending（待运营审核）；
// 运营审核通过 / 驳回（驳回须填写原因）；待审核期间不得重复提交；资料视图回显全部字段。
func TestDw2PlayerExpectedPriceSubmitAndReview(t *testing.T) {
	db := openTransferDB(t, "esports_dw2_expected_price")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	admin := seedAdminAccount(t, db, "dw2price")
	player := seedPlayer(t, db, 9301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// ① 提交期望报价 → pending，并落提交时间。
	updated, err := submitPlayerExpectedPrice(ctx, db, player.ID, 5000, now)
	if err != nil {
		t.Fatalf("提交期望报价应成功：%v", err)
	}
	if updated.ExpectedPriceStatus != esportsplayer.ExpectedPriceStatusPending ||
		updated.ExpectedPriceCents != 5000 || updated.ExpectedPriceSubmittedAt == nil {
		t.Fatalf("期望报价应落待审核态：%+v", updated)
	}
	// ② 待审核期间不得重复提交。
	if _, err = submitPlayerExpectedPrice(ctx, db, player.ID, 6000, now); !errors.Is(err, errConflict) {
		t.Fatalf("待审核期间重复提交应报冲突：%v", err)
	}
	// ③ 运营驳回（必须填原因）→ rejected，落审核人与时间与原因。
	if _, err = reviewPlayerExpectedPrice(ctx, db, admin.ID, player.ID, false, "", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("驳回未填原因应报参数错误：%v", err)
	}
	rejected, err := reviewPlayerExpectedPrice(ctx, db, admin.ID, player.ID, false, "高于同档市场价", now)
	if err != nil {
		t.Fatalf("驳回应成功：%v", err)
	}
	if rejected.ExpectedPriceStatus != esportsplayer.ExpectedPriceStatusRejected ||
		rejected.ExpectedPriceReviewedByAdminID == nil || *rejected.ExpectedPriceReviewedByAdminID != admin.ID ||
		rejected.ExpectedPriceReviewedAt == nil || rejected.ExpectedPriceReviewNote != "高于同档市场价" {
		t.Fatalf("驳回留痕不完整：%+v", rejected)
	}
	// ④ 驳回后可再次提交 → 回到 pending，并清空上一轮审核留痕。
	if _, err = submitPlayerExpectedPrice(ctx, db, player.ID, 4000, now); err != nil {
		t.Fatalf("驳回后应可再次提交：%v", err)
	}
	if got := db.EsportsPlayer.GetX(ctx, player.ID); got.ExpectedPriceReviewNote != "" ||
		got.ExpectedPriceReviewedAt != nil || got.ExpectedPriceReviewedByAdminID != nil {
		t.Fatalf("重新提交应清空上一轮审核留痕：%+v", got)
	}
	// ⑤ 通过。
	approved, err := reviewPlayerExpectedPrice(ctx, db, admin.ID, player.ID, true, "符合档位", now)
	if err != nil {
		t.Fatalf("通过应成功：%v", err)
	}
	if approved.ExpectedPriceStatus != esportsplayer.ExpectedPriceStatusApproved ||
		approved.ExpectedPriceCents != 4000 {
		t.Fatalf("审核通过后状态不正确：%+v", approved)
	}
	// ⑥ 非 pending 状态不得审核。
	if _, err = reviewPlayerExpectedPrice(ctx, db, admin.ID, player.ID, true, "", now); !errors.Is(err, errConflict) {
		t.Fatalf("非待审核状态不得复核：%v", err)
	}
	// ⑦ 非法输入。
	if _, err = submitPlayerExpectedPrice(ctx, db, player.ID, 0, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("期望报价必须为正整数分：%v", err)
	}
	if _, err = submitPlayerExpectedPrice(ctx, db, 0, 4000, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法陪玩 ID 应报参数错误：%v", err)
	}

	// ⑧ 资料视图必须回显期望报价四项。
	view, err := playerSelfProfile(ctx, db, db.EsportsPlayer.GetX(ctx, player.ID))
	if err != nil {
		t.Fatal(err)
	}
	if view.ExpectedPriceCents != 4000 || view.ExpectedPriceStatus != "approved" ||
		view.ExpectedPriceReviewNote != "符合档位" || view.ExpectedPriceSubmittedAt == nil {
		t.Fatalf("资料视图未回显期望报价：%+v", view)
	}
}

// ：期望报价允许区间由配置约束（min,max，0=该侧不限），越界必须拒绝。
func TestDw2PlayerExpectedPriceLimits(t *testing.T) {
	db := openTransferDB(t, "esports_dw2_expected_price_limits")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	playerA := seedPlayer(t, db, 9311, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	playerB := seedPlayer(t, db, 9312, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	playerC := seedPlayer(t, db, 9313, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	setUpConfig(t, db, playerExpectedPriceLimitsKey, "1000,3000")
	if _, err := submitPlayerExpectedPrice(ctx, db, playerA.ID, 500, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("低于下限应被拒：%v", err)
	}
	if _, err := submitPlayerExpectedPrice(ctx, db, playerB.ID, 4000, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("高于上限应被拒：%v", err)
	}
	if _, err := submitPlayerExpectedPrice(ctx, db, playerC.ID, 2000, now); err != nil {
		t.Fatalf("区间内应通过：%v", err)
	}
	// 配置非法 → 明确回落「两侧不限」，而不是把所有人拒掉。
	setUpConfig(t, db, playerExpectedPriceLimitsKey, "abc")
	if _, err := submitPlayerExpectedPrice(ctx, db, playerA.ID, 999999, now); err != nil {
		t.Fatalf("配置非法时应回落不限：%v", err)
	}
}

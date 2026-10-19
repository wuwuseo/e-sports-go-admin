package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsassessment"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayerreviewrevision"
)

// P0-15 回归：申诉把「通过」改判为「不通过」时必须撤销该次考核带来的定级与合格标记，
// 并把接单权限置为不可用（qualification=suspended）；旧实现直接 return nil，等级与 qualified 全部保留。
func TestAppealPassToFailedRevokesQualification(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_rollback_entry")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	player := seedPlayer(t, db, 1301, esportsplayer.QualificationPending, esportsplayer.PresenceOffline)
	seedVerifiedIdentity(t, db, player.ID)
	assessment := runScoredAssessmentForTest(t, db, "entry", "", player.ID, allDimensionScores(100), now)
	if assessment.Status != esportsassessment.StatusPassed ||
		db.EsportsPlayer.GetX(ctx, player.ID).Qualification != esportsplayer.QualificationQualified {
		t.Fatalf("用例前提不成立：入职考核应已通过并定级合格：%+v", assessment)
	}

	if _, err := createAppeal(ctx, db, player.ID, assessment.ID, "当天考官未按标准打分", now); err != nil {
		t.Fatalf("发起申诉失败：%v", err)
	}
	reviewed, err := reviewAppeal(ctx, db, assessment.ID, 11, true, "核实后改判不通过", now)
	if err != nil || reviewed.Status != esportsassessment.StatusFailed {
		t.Fatalf("申诉改判失败：%+v, %v", reviewed, err)
	}
	updated := db.EsportsPlayer.GetX(ctx, player.ID)
	if updated.Qualification != esportsplayer.QualificationSuspended {
		t.Fatalf("改判不通过后接单权限未撤销（仍为 %s）", updated.Qualification)
	}
	// 定级为「不通过」后不得再被派单：派单侧只认 qualified。
	if updated.Qualification == esportsplayer.QualificationQualified {
		t.Fatal("出现「考核不通过但仍可接单」的越权状态")
	}
}

// P0-15 回归：升级考核改判不通过要回退该次考核抬上去的等级（mentor → senior），并暂停接单。
func TestAppealPassToFailedRevertsUpgradeLevel(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_rollback_upgrade")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	player := seedPlayer(t, db, 1302, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seedVerifiedIdentity(t, db, player.ID)
	db.EsportsPlayer.UpdateOneID(player.ID).SetLevel(esportsplayer.LevelSenior).SaveX(ctx)

	assessment := runScoredAssessmentForTest(t, db, "upgrade", "mentor", player.ID, allDimensionScores(100), now)
	if got := db.EsportsPlayer.GetX(ctx, player.ID); got.Level != esportsplayer.LevelMentor ||
		got.Qualification != esportsplayer.QualificationQualified {
		t.Fatalf("用例前提不成立：升级考核应已把等级抬到 mentor：%+v", got)
	}

	if _, err := createAppeal(ctx, db, player.ID, assessment.ID, "评分表填报有误", now); err != nil {
		t.Fatalf("发起申诉失败：%v", err)
	}
	if _, err := reviewAppeal(ctx, db, assessment.ID, 11, true, "改判不通过", now); err != nil {
		t.Fatalf("申诉改判失败：%v", err)
	}
	updated := db.EsportsPlayer.GetX(ctx, player.ID)
	if updated.Level != esportsplayer.LevelSenior {
		t.Fatalf("改判不通过后等级未回退一档，实际 %s", updated.Level)
	}
	if updated.Qualification != esportsplayer.QualificationSuspended {
		t.Fatalf("改判不通过后接单权限未暂停，实际 %s", updated.Qualification)
	}
}

// P0-15 回归：抽查复核不通过后申诉成立（failed → passed）要补记合格并解除暂停。
func TestAppealFailedToPassedRestoresQualification(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_restore_spot")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	player := seedPlayer(t, db, 1303, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seedVerifiedIdentity(t, db, player.ID)
	db.EsportsPlayer.UpdateOneID(player.ID).SetLevel(esportsplayer.LevelMentor).SaveX(ctx)

	assessment := runScoredAssessmentForTest(t, db, "spot_check", "mentor", player.ID, allDimensionScores(0), now)
	if got := db.EsportsPlayer.GetX(ctx, player.ID); got.Qualification != esportsplayer.QualificationSuspended {
		t.Fatalf("用例前提不成立：抽查不通过应已暂停接单：%+v", got)
	}

	if _, err := createAppeal(ctx, db, player.ID, assessment.ID, "抽查记录张冠李戴", now); err != nil {
		t.Fatalf("发起申诉失败：%v", err)
	}
	reviewed, err := reviewAppeal(ctx, db, assessment.ID, 11, true, "核实后改判通过", now)
	if err != nil || reviewed.Status != esportsassessment.StatusPassed {
		t.Fatalf("申诉改判失败：%+v, %v", reviewed, err)
	}
	updated := db.EsportsPlayer.GetX(ctx, player.ID)
	if updated.Qualification != esportsplayer.QualificationQualified {
		t.Fatalf("改判通过后未恢复接单权限，实际 %s", updated.Qualification)
	}
	if updated.Level != esportsplayer.LevelSenior || updated.LevelAppraisedAt == nil {
		t.Fatalf("改判通过后考核时间未刷新或等级被擅自上抬：%+v", updated)
	}
}

// P0-16 回归：客服代录不得覆盖已核验的客户评价——必须返回冲突 + 中文提示，且原记录（评分 / 评语 /
// verified / 版本 / 修订历史）一字不改。
func TestAdminProxyReviewCannotOverwriteVerifiedReview(t *testing.T) {
	db := openAssessmentDB(t, "esports_review_proxy_guard")
	defer db.Close()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	player := seedPlayer(t, db, 1401, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedReviewOrderForTest(t, db, "review-guard-1", customer.ID, player.ID)

	verified, err := submitCustomerReview(ctx, db, customer.ID, order.ID, player.ID, 5, "服务很好")
	if err != nil || !verified.Verified {
		t.Fatalf("用例前提不成立：客户评价应已核验通过：%+v, %v", verified, err)
	}

	_, err = recordReviewByAdmin(ctx, db, reviewInput{
		OrderID: order.ID, CustomerID: customer.ID, PlayerID: player.ID, Rating: 1, Comment: "客服代录覆盖",
	})
	if !errors.Is(err, errConflict) {
		t.Fatalf("代录覆盖已核验评价未被拦截：%v", err)
	}
	if !strings.Contains(err.Error(), "核验") || !strings.Contains(err.Error(), "申诉") {
		t.Fatalf("拦截提示未说明需走申诉 / 撤回流程：%v", err)
	}

	kept := db.EsportsPlayerReview.GetX(ctx, verified.ID)
	if !kept.Verified || kept.Rating != 5 || kept.Comment != "服务很好" || kept.Version != 1 {
		t.Fatalf("已核验评价被改写：%+v", kept)
	}
	if got := db.EsportsPlayerReviewRevision.Query().Where(
		esportsplayerreviewrevision.ReviewIDEQ(verified.ID),
	).CountX(ctx); got != 1 {
		t.Fatalf("拦截后不应追加修订历史，实际 %d 条", got)
	}
	// 综合评分仍按客户真实评价统计。
	summary, err := playerRatingSummary(ctx, db, player.ID)
	if err != nil || summary.SampleCount != 1 || summary.Average != 5 {
		t.Fatalf("已核验评价被剔除出综合评分：%+v, %v", summary, err)
	}
}

// P0-16 回归：未核验的代录记录仍保留原有 upsert 能力（可被再次代录更新）。
func TestAdminProxyReviewStillUpsertsUnverifiedReview(t *testing.T) {
	db := openAssessmentDB(t, "esports_review_proxy_upsert")
	defer db.Close()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	player := seedPlayer(t, db, 1402, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedReviewOrderForTest(t, db, "review-upsert-1", customer.ID, player.ID)

	first, err := recordReviewByAdmin(ctx, db, reviewInput{
		OrderID: order.ID, CustomerID: customer.ID, PlayerID: player.ID, Rating: 3, Comment: "电话回访",
	})
	if err != nil || first.Verified {
		t.Fatalf("首次代录失败：%+v, %v", first, err)
	}
	second, err := recordReviewByAdmin(ctx, db, reviewInput{
		OrderID: order.ID, CustomerID: customer.ID, PlayerID: player.ID, Rating: 4, Comment: "回访更正",
	})
	if err != nil {
		t.Fatalf("未核验评价应允许代录更新：%v", err)
	}
	if second.ID != first.ID || second.Rating != 4 || second.Comment != "回访更正" || second.Version != 2 {
		t.Fatalf("未核验评价代录更新结果不符：%+v", second)
	}
}

// seedReviewOrderForTest 构造一张已完成订单并落一条服务段，使该陪玩成为「实际服务成员」。
func seedReviewOrderForTest(t *testing.T, db *ent.Client, orderNo string, customerID, playerID int) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	order := db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(customerID).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).SetPromisedServiceUnits(1).
		SetRequiredSeats(1).SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(playerID).
		SetSeatIndex(1).SetStartedAt(time.Now().UTC()).SaveX(ctx)
	return order
}

package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsassessment"
	"go-admin/internal/ent/esportsassessmentappeal"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayerreview"
	"go-admin/internal/ent/esportsplayerreviewrevision"
	"go-admin/internal/ent/migrate"
)

func openAssessmentDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// testEntryWeights 返回一组覆盖全部六维度、合计恰为 10000 基点的权重。
func testEntryWeights() []assessmentWeightInput {
	return []assessmentWeightInput{
		{Dimension: "game_skill", WeightBP: 2000},
		{Dimension: "teaching", WeightBP: 2000},
		{Dimension: "attitude", WeightBP: 2000},
		{Dimension: "communication", WeightBP: 2000},
		{Dimension: "compliance", WeightBP: 1000},
		{Dimension: "stability", WeightBP: 1000},
	}
}

// allDimensionScores 生成六维度同分打分，评语必填。
func allDimensionScores(score int) []assessmentScoreInput {
	scores := make([]assessmentScoreInput, 0, len(assessmentDimensions))
	for _, dim := range assessmentDimensions {
		scores = append(scores, assessmentScoreInput{Dimension: dim, Score: score, Comment: "测试评语"})
	}
	return scores
}

// 权重配置：未配置时回落等权（合计仍 10000），非法合计/缺维度被拒，合法配置可回读。
func TestAssessmentWeights(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_weights")
	defer db.Close()
	ctx := t.Context()

	fallback, err := resolveAssessmentWeights(ctx, db, "entry")
	if err != nil || len(fallback) != 6 {
		t.Fatalf("等权回落失败：%+v, %v", fallback, err)
	}
	sum := 0
	for _, w := range fallback {
		sum += w
	}
	if sum != 10000 {
		t.Fatalf("等权合计应为 10000，实际 %d", sum)
	}

	if _, err := setAssessmentRules(ctx, db, "entry", testEntryWeights()[:5]); !errors.Is(err, errInvalidInput) {
		t.Fatalf("缺维度未被拦截：%v", err)
	}
	bad := testEntryWeights()
	bad[0].WeightBP = 1999 // 合计 9999
	if _, err := setAssessmentRules(ctx, db, "entry", bad); !errors.Is(err, errInvalidInput) {
		t.Fatalf("合计非 10000 未被拦截：%v", err)
	}
	if _, err := setAssessmentRules(ctx, db, "bogus", testEntryWeights()); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法考核类型未被拦截：%v", err)
	}
	rows, err := setAssessmentRules(ctx, db, "entry", testEntryWeights())
	if err != nil || len(rows) != 6 {
		t.Fatalf("配置权重失败：%+v, %v", rows, err)
	}
	got, err := resolveAssessmentWeights(ctx, db, "entry")
	if err != nil || got["game_skill"] != 2000 || got["compliance"] != 1000 {
		t.Fatalf("回读权重不符：%+v, %v", got, err)
	}
}

// 入职考核：分配考官 → 打分加权总分达标则定级并开通接单权限；缺维度、越权打分被拦截。
func TestScoreAssessmentPassAndQualify(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_pass")
	defer db.Close()
	ctx := t.Context()
	if _, err := setAssessmentRules(ctx, db, "entry", testEntryWeights()); err != nil {
		t.Fatal(err)
	}
	player := seedPlayer(t, db, 101, esportsplayer.QualificationPending, esportsplayer.PresenceOffline)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	assessment, err := createAssessment(ctx, db, 11, assessmentInput{
		PlayerID: player.ID, AssessmentType: "entry",
	}, now)
	if err != nil || assessment.Status != esportsassessment.StatusPending || assessment.TargetLevel != "novice" {
		t.Fatalf("创建入职考核失败：%+v, %v", assessment, err)
	}
	// 未分配考官时不可评分（即使创建人本人也不行）。
	if _, err := scoreAssessment(ctx, db, assessment.ID, 11, allDimensionScores(100), "未分配考官", now); !errors.Is(err, errForbidden) {
		t.Fatalf("未分配考官打分未被拦截：%v", err)
	}
	// 分配考官。
	assigned, err := assignAssessment(ctx, db, assessment.ID, 11, nil, now)
	if err != nil || assigned.Status != esportsassessment.StatusScheduled || assigned.ExaminerAdminID == nil || *assigned.ExaminerAdminID != 11 {
		t.Fatalf("分配考官失败：%+v, %v", assigned, err)
	}
	// 缺维度打分被拒。
	if _, err := scoreAssessment(ctx, db, assessment.ID, 11, allDimensionScores(100)[:5], "缺维度", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("缺维度打分未被拦截：%v", err)
	}
	// 非分配考官打分被拒。
	if _, err := scoreAssessment(ctx, db, assessment.ID, 22, allDimensionScores(100), "越权", now); !errors.Is(err, errForbidden) {
		t.Fatalf("越权打分未被拦截：%v", err)
	}
	// 定级为合格前须实名认证通过（§6.1）。
	seedVerifiedIdentity(t, db, player.ID)
	// 全 100 分，加权总分 100 ≥ 60 通过。
	passed, err := scoreAssessment(ctx, db, assessment.ID, 11, allDimensionScores(100), "表现优秀", now)
	if err != nil || passed.Status != esportsassessment.StatusPassed || passed.TotalScore != 100 {
		t.Fatalf("打分通过失败：%+v, %v", passed, err)
	}
	updated := db.EsportsPlayer.GetX(ctx, player.ID)
	if updated.Qualification != esportsplayer.QualificationQualified || updated.Level != esportsplayer.LevelNovice || updated.LevelAppraisedAt == nil {
		t.Fatalf("通过后未定级开通接单权限：%+v", updated)
	}
	if db.EsportsAssessmentScore.Query().CountX(ctx) != 6 {
		t.Fatal("未写入逐维度打分")
	}
	if _, err := scoreAssessment(ctx, db, assessment.ID, 11, allDimensionScores(100), "重复", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复打分未被拦截：%v", err)
	}
}

// 抽查复核不达标：降一档并暂停接单权限。
func TestScoreAssessmentSpotCheckDemote(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_spot")
	defer db.Close()
	ctx := t.Context()
	if _, err := setAssessmentRules(ctx, db, "spot_check", testEntryWeights()); err != nil {
		t.Fatal(err)
	}
	player := seedPlayer(t, db, 101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetLevel(esportsplayer.LevelMentor).SaveX(ctx)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	assessment, err := createAssessment(ctx, db, 11, assessmentInput{
		PlayerID: player.ID, AssessmentType: "spot_check", TargetLevel: "mentor",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assignAssessment(ctx, db, assessment.ID, 11, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := scoreAssessment(ctx, db, assessment.ID, 11, allDimensionScores(0), "质量下降", now); err != nil {
		t.Fatal(err)
	}
	updated := db.EsportsPlayer.GetX(ctx, player.ID)
	if updated.Level != esportsplayer.LevelSenior || updated.Qualification != esportsplayer.QualificationSuspended {
		t.Fatalf("抽查不达标未降级并暂停接单：%+v", updated)
	}
	// 非 entry 类型缺目标等级被拒。
	if _, err := createAssessment(ctx, db, 11, assessmentInput{PlayerID: player.ID, AssessmentType: "upgrade"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("升级考核缺目标等级未被拦截：%v", err)
	}
}

// 申诉：每个任务仅一次，复核通过翻转结论并重放定级动作。
func TestAssessmentAppeal(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_appeal")
	defer db.Close()
	ctx := t.Context()
	if _, err := setAssessmentRules(ctx, db, "entry", testEntryWeights()); err != nil {
		t.Fatal(err)
	}
	player := seedPlayer(t, db, 101, esportsplayer.QualificationPending, esportsplayer.PresenceOffline)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	assessment, err := createAssessment(ctx, db, 11, assessmentInput{PlayerID: player.ID, AssessmentType: "entry"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assignAssessment(ctx, db, assessment.ID, 11, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := scoreAssessment(ctx, db, assessment.ID, 11, allDimensionScores(0), "未达标", now); err != nil {
		t.Fatal(err)
	}
	if db.EsportsPlayer.GetX(ctx, player.ID).Qualification != esportsplayer.QualificationPending {
		t.Fatal("不通过却改动了接单权限")
	}
	// 非本人申诉被拒。
	if _, err := createAppeal(ctx, db, 999, assessment.ID, "不服", now); !errors.Is(err, errForbidden) {
		t.Fatalf("非本人申诉未被拦截：%v", err)
	}
	appeal, err := createAppeal(ctx, db, player.ID, assessment.ID, "成绩有误", now)
	if err != nil || appeal.Status != esportsassessmentappeal.StatusPending {
		t.Fatalf("提交申诉失败：%+v, %v", appeal, err)
	}
	if _, err := createAppeal(ctx, db, player.ID, assessment.ID, "再来一次", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复申诉未被拦截：%v", err)
	}
	// 复核通过：failed → passed，重放定级；改判同样受实名认证门槛约束（§6.1）。
	seedVerifiedIdentity(t, db, player.ID)
	reviewed, err := reviewAppeal(ctx, db, assessment.ID, 11, true, "成绩录入有误", now)
	if err != nil || reviewed.Status != esportsassessment.StatusPassed || reviewed.AppealStatus != esportsassessment.AppealStatusApproved {
		t.Fatalf("申诉复核失败：%+v, %v", reviewed, err)
	}
	updated := db.EsportsPlayer.GetX(ctx, player.ID)
	if updated.Level != esportsplayer.LevelNovice || updated.Qualification != esportsplayer.QualificationQualified {
		t.Fatalf("改判后未重放定级：%+v", updated)
	}
	if _, err := reviewAppeal(ctx, db, assessment.ID, 11, true, "再来", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复复核未被拦截：%v", err)
	}
}

// 客户评价：同客户同订单同陪玩仅一份有效评价、修改留修订历史；未验证代录不计入评分；
// 异常刷评状态复核后剔除评分；样本不足标记。
func TestPlayerReviewUpsertRatingAndModeration(t *testing.T) {
	db := openAssessmentDB(t, "esports_player_review")
	defer db.Close()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	player := seedPlayer(t, db, 202, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := db.EsportsOrder.Create().SetOrderNo("review-order-1").SetCustomerID(customer.ID).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).SetPromisedServiceUnits(1).
		SetRequiredSeats(1).SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(player.ID).
		SetSeatIndex(1).SetStartedAt(time.Now().UTC()).SaveX(ctx)

	// 服务成员之外的人不可被评价。
	if _, err := submitCustomerReview(ctx, db, customer.ID, order.ID, 999, 5, "越权"); !errors.Is(err, errForbidden) {
		t.Fatalf("评价非服务成员未被拦截：%v", err)
	}
	review, err := submitCustomerReview(ctx, db, customer.ID, order.ID, player.ID, 5, "很棒")
	if err != nil || !review.Verified || review.Version != 1 {
		t.Fatalf("客户评价失败：%+v, %v", review, err)
	}
	// 修改评价保留修订历史。
	updated, err := submitCustomerReview(ctx, db, customer.ID, order.ID, player.ID, 4, "还行")
	if err != nil || updated.ID != review.ID || updated.Version != 2 {
		t.Fatalf("修改评价失败：%+v, %v", updated, err)
	}
	if db.EsportsPlayerReviewRevision.Query().Where(
		esportsplayerreviewrevision.ReviewIDEQ(review.ID),
	).CountX(ctx) != 2 {
		t.Fatal("修订历史未按版本追加")
	}
	// 代录评价标记未验证，不计入综合评分。
	proxy, err := recordReviewByAdmin(ctx, db, reviewInput{
		OrderID: order.ID + 1, CustomerID: customer.ID, PlayerID: player.ID, Rating: 1, Comment: "客服代录",
	})
	if err != nil || proxy.Verified {
		t.Fatalf("代录评价失败：%+v, %v", proxy, err)
	}
	summary, err := playerRatingSummary(ctx, db, player.ID)
	if err != nil || summary.SampleCount != 1 || summary.Average != 4 {
		t.Fatalf("综合评分统计错误：%+v, %v", summary, err)
	}
	if !summary.Insufficient || summary.RuleVersion != "v1" {
		t.Fatalf("样本不足或规则版本不符：%+v", summary)
	}
	// 异常刷评复核为 invalid 后剔除评分。
	if _, err := setReviewStatus(ctx, db, review.ID, "invalid"); err != nil {
		t.Fatal(err)
	}
	summary, err = playerRatingSummary(ctx, db, player.ID)
	if err != nil || summary.SampleCount != 0 || summary.Average != 0 {
		t.Fatalf("剔除异常评价后评分统计错误：%+v, %v", summary, err)
	}
	if _, err := setReviewStatus(ctx, db, review.ID, "bogus"); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法评价状态未被拦截：%v", err)
	}
	if db.EsportsPlayerReview.Query().Where(
		esportsplayerreview.StatusEQ(esportsplayerreview.StatusInvalid),
	).CountX(ctx) != 1 {
		t.Fatal("评价状态未更新")
	}
}

package esports

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsassessment"
	"go-admin/internal/ent/esportsassessmentscore"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayerreview"
	"go-admin/internal/ent/esportsplayerreviewappeal"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

// 第 4 轮新增配置键的字面量：RED 阶段生产常量尚未存在，测试用字面量保证可编译。
const (
	spotCheckRatioBPKeyLiteral       = "app.esports.spot_check_ratio_bp"
	spotCheckCycleDaysKeyLiteral     = "app.esports.spot_check_cycle_days"
	levelReviewCycleDaysKeyLiteral   = "app.esports.level_review_cycle_days"
	levelReviewFailPeriodsKeyLiteral = "app.esports.level_review_fail_periods"
	reviewBrushRuleKeyLiteral        = "app.esports.review_brush_rule"
	reviewAppealWindowDaysKeyLiteral = "app.esports.review_appeal_window_days"
)

// ── ：陪玩端考核明细必须含逐维度得分与权重（评级依据） ──

// scoreEntryAssessment 走既有「建单 → 分配考官 → 打分」流程，落一条有逐维度明细的考核单。
func scoreEntryAssessment(t *testing.T, db *ent.Client, playerID, examinerID, score int, now time.Time) *ent.EsportsAssessment {
	t.Helper()
	ctx := t.Context()
	item, err := createAssessment(ctx, db, 9, assessmentInput{
		PlayerID: playerID, AssessmentType: "entry", TargetLevel: string("novice"),
	}, now)
	if err != nil {
		t.Fatalf("建考核单失败：%v", err)
	}
	if _, err = assignAssessment(ctx, db, item.ID, examinerID, nil, now); err != nil {
		t.Fatalf("分配考官失败：%v", err)
	}
	scored, err := scoreAssessment(ctx, db, item.ID, examinerID, allDimensionScores(score), "考核评语", now)
	if err != nil {
		t.Fatalf("打分失败：%v", err)
	}
	return scored
}

// 陪玩端「我的考核」必须能拿到逐维度得分与权重，否则评级依据对陪玩不可见。
func TestListPlayerAssessmentDetailsExposeDimensionScores(t *testing.T) {
	db := openAssessmentDB(t, "esports_r4_p13_details")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedUpgradePlayer(t, db, 501, "novice", now.AddDate(0, 0, -30))
	scored := scoreEntryAssessment(t, db, player.ID, 77, 90, now)

	views, err := listPlayerAssessmentDetails(ctx, db, player.ID, 50)
	if err != nil {
		t.Fatalf("读取陪玩考核明细失败：%v", err)
	}
	if len(views) != 1 {
		t.Fatalf("应有 1 条考核明细，实际 %d", len(views))
	}
	view := views[0]
	if view.Assessment == nil || view.Assessment.ID != scored.ID {
		t.Fatalf("考核主表不匹配：%+v", view.Assessment)
	}
	if len(view.Scores) != len(assessmentDimensions) {
		t.Fatalf("逐维度得分缺失：应为 %d 项，实际 %d 项", len(assessmentDimensions), len(view.Scores))
	}
	totalWeight := 0
	for _, row := range view.Scores {
		if row.WeightBp <= 0 {
			t.Fatalf("维度 %s 的权重（评级依据）缺失：%+v", row.Dimension, row)
		}
		totalWeight += row.WeightBp
	}
	if totalWeight != 10000 {
		t.Fatalf("逐维度权重合计应为 10000 基点，实际 %d", totalWeight)
	}
	if view.WeightTotalBP != 10000 {
		t.Fatalf("权重合计字段应为 10000，实际 %d", view.WeightTotalBP)
	}
}

// 陪玩端考核接口（handler 层）下发的条目必须带 scores，不能只回主表。
func TestPlayerAssessmentsHandlerReturnsRatingBasis(t *testing.T) {
	db := openAssessmentDB(t, "esports_r4_p13_handler")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	now := time.Now().UTC()
	_, player := seedSelfServicePlayer(t, db, "r4-assess-player", "13900000001")
	// 考核通过会回写「合格」资格，前置是实名认证通过。
	seedVerifiedIdentity(t, db, player.ID)
	scoreEntryAssessment(t, db, player.ID, 77, 88, now)

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", "r4-assess-player")
		return c.Next()
	})
	app.Get("/user/player/assessments", playerAssessmentsHandler)

	status, payload := sendJSON(t, app, "GET", "/user/player/assessments", "")
	if status != 200 {
		t.Fatalf("陪玩端考核列表应成功，实际 %d %+v", status, payload)
	}
	rows, _ := payload["data"].([]any)
	if len(rows) != 1 {
		t.Fatalf("应回 1 条考核记录，实际 %+v", payload)
	}
	first, _ := rows[0].(map[string]any)
	if first == nil {
		t.Fatalf("考核记录结构异常：%+v", rows[0])
	}
	scores, _ := first["scores"].([]any)
	if len(scores) != len(assessmentDimensions) {
		t.Fatalf("陪玩端条目未返回逐维度得分：%+v", first)
	}
	dim, _ := scores[0].(map[string]any)
	if dim == nil || dim["weight_bp"] == nil || dim["dimension"] == nil {
		t.Fatalf("逐维度明细缺权重或维度名：%+v", scores[0])
	}
}

// ── ：按周期 + 比例随机抽取在岗合格陪玩生成抽查单 ──

// 抽样必须真的随机：同一批候选连续两次抽取的集合不能完全相同。
// 用固定种子的 rng 保证可复现（不抖动），但两轮抽取共享同一随机流，集合应不同。
func TestSpotCheckSamplingIsRandomAcrossRuns(t *testing.T) {
	candidates := make([]*ent.EsportsPlayer, 0, 20)
	for i := 0; i < 20; i++ {
		candidates = append(candidates, &ent.EsportsPlayer{ID: 100 + i})
	}
	rng := rand.New(rand.NewSource(20261018))
	first := sampleSpotCheckCandidates(candidates, 3000, rng)
	second := sampleSpotCheckCandidates(candidates, 3000, rng)

	if len(first) == 0 || len(first) == len(candidates) {
		t.Fatalf("30%% 抽中 %d/%d，抽样退化：%v", len(first), len(candidates), first)
	}
	if spotCheckSameSet(first, second) {
		t.Fatalf("同一批陪玩两次抽取集合完全相同，随机性不可证：%v", first)
	}
	// 比例边界：10000 基点 = 全抽，0 = 不抽。
	if got := sampleSpotCheckCandidates(candidates, 10000, rng); len(got) != len(candidates) {
		t.Fatalf("100%% 应全抽，实际 %d", len(got))
	}
	if got := sampleSpotCheckCandidates(candidates, 0, rng); len(got) != 0 {
		t.Fatalf("0%% 应不抽，实际 %d", len(got))
	}
}

func spotCheckSameSet(a, b []*ent.EsportsPlayer) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[int]int, len(a))
	for _, item := range a {
		seen[item.ID]++
	}
	for _, item := range b {
		if seen[item.ID] == 0 {
			return false
		}
		seen[item.ID]--
	}
	return true
}

// 调度器必须自动为在岗合格陪玩建 spot_check 单，不依赖考官手工指定。
func TestGenerateSpotCheckAssessmentsCreatesTasks(t *testing.T) {
	db := openAssessmentDB(t, "esports_r4_spot_check_task")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	eligible := []*ent.EsportsPlayer{
		seedUpgradePlayer(t, db, 601, "senior", now.AddDate(0, 0, -60)),
		seedUpgradePlayer(t, db, 602, "skilled", now.AddDate(0, 0, -60)),
		seedUpgradePlayer(t, db, 603, "novice", now.AddDate(0, 0, -60)),
	}
	// 未合格 / 离线：不属于「在岗合格」。
	seedPlayer(t, db, 604, esportsplayer.QualificationPending, esportsplayer.PresenceOnline)
	seedPlayer(t, db, 605, esportsplayer.QualificationQualified, esportsplayer.PresenceOffline)
	seedUpgradeConfig(t, db, spotCheckRatioBPKeyLiteral, "10000")
	seedUpgradeConfig(t, db, spotCheckCycleDaysKeyLiteral, "7")

	count, err := generateSpotCheckAssessments(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("抽查生成任务失败：%v", err)
	}
	if count != len(eligible) {
		t.Fatalf("应为 %d 名在岗合格陪玩建抽查单，实际 %d", len(eligible), count)
	}
	rows := db.EsportsAssessment.Query().Where(
		esportsassessment.AssessmentTypeEQ(esportsassessment.AssessmentTypeSpotCheck),
	).AllX(ctx)
	if len(rows) != len(eligible) {
		t.Fatalf("抽查单数量应为 %d，实际 %d", len(eligible), len(rows))
	}
	byPlayer := map[int]*ent.EsportsAssessment{}
	for _, row := range rows {
		byPlayer[row.PlayerID] = row
		if row.Status != esportsassessment.StatusPending {
			t.Fatalf("抽查单应为待处理，实际 %s", row.Status)
		}
		if row.Source != esportsassessment.SourceSystem {
			t.Fatalf("抽查单来源应为 system，实际 %s", row.Source)
		}
		if row.ExaminerAdminID != nil {
			t.Fatalf("抽查单不应替考官指定人选，实际 %d", *row.ExaminerAdminID)
		}
	}
	for _, player := range eligible {
		row := byPlayer[player.ID]
		if row == nil {
			t.Fatalf("在岗合格陪玩 %d 未生成抽查单", player.ID)
		}
		if row.TargetLevel != player.Level {
			t.Fatalf("抽查目标档位应为其当前等级 %s，实际 %s", player.Level, row.TargetLevel)
		}
	}
	// 同一周期内重复运行不重复建单（幂等）。
	again, err := generateSpotCheckAssessments(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("重复运行任务失败：%v", err)
	}
	if again != 0 {
		t.Fatalf("周期内重复运行不应再建单，实际新建 %d", again)
	}
}

// 抽查周期按「同一陪玩最近一次抽查创建时间」收敛：周期外可再抽，周期内跳过。
func TestGenerateSpotCheckRespectsCycleWindow(t *testing.T) {
	db := openAssessmentDB(t, "esports_r4_spot_check_cycle")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	due := seedUpgradePlayer(t, db, 611, "senior", now.AddDate(0, 0, -60))
	recent := seedUpgradePlayer(t, db, 612, "senior", now.AddDate(0, 0, -60))
	if _, err := db.EsportsAssessment.Create().SetTaskNo("AS-due").
		SetPlayerID(due.ID).SetAssessmentType(esportsassessment.AssessmentTypeSpotCheck).
		SetTargetLevel("senior").SetStatus(esportsassessment.StatusPassed).
		SetSource(esportsassessment.SourceSystem).SetCreatedAt(now.AddDate(0, 0, -10)).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EsportsAssessment.Create().SetTaskNo("AS-recent").
		SetPlayerID(recent.ID).SetAssessmentType(esportsassessment.AssessmentTypeSpotCheck).
		SetTargetLevel("senior").SetStatus(esportsassessment.StatusPassed).
		SetSource(esportsassessment.SourceSystem).SetCreatedAt(now.AddDate(0, 0, -1)).Save(ctx); err != nil {
		t.Fatal(err)
	}
	seedUpgradeConfig(t, db, spotCheckRatioBPKeyLiteral, "10000")
	seedUpgradeConfig(t, db, spotCheckCycleDaysKeyLiteral, "7")

	count, err := generateSpotCheckAssessments(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("抽查生成任务失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("周期外应再抽 1 人，实际 %d", count)
	}
	if n := db.EsportsAssessment.Query().Where(
		esportsassessment.PlayerIDEQ(due.ID),
		esportsassessment.AssessmentTypeEQ(esportsassessment.AssessmentTypeSpotCheck),
	).CountX(ctx); n != 2 {
		t.Fatalf("周期外陪玩应新增 1 条抽查单（共 2），实际 %d", n)
	}
	if n := db.EsportsAssessment.Query().Where(
		esportsassessment.PlayerIDEQ(recent.ID),
		esportsassessment.AssessmentTypeEQ(esportsassessment.AssessmentTypeSpotCheck),
	).CountX(ctx); n != 1 {
		t.Fatalf("周期内陪玩不应新增抽查单，实际 %d", n)
	}
}

// ── ：等级定期复核 + 「连续 N 期不达标」阈值 ──

// seedScoredSpotCheck 落一条「已由考官打分且已出结论」的抽查单，用于构造连续不达标期。
func seedScoredSpotCheck(t *testing.T, db *ent.Client, playerID int, level string, failed bool, createdAt time.Time) *ent.EsportsAssessment {
	t.Helper()
	ctx := t.Context()
	status := esportsassessment.StatusPassed
	if failed {
		status = esportsassessment.StatusFailed
	}
	item := db.EsportsAssessment.Create().
		SetTaskNo(fmt.Sprintf("AS-sc-%d-%s", playerID, createdAt.Format("20060102"))).
		SetPlayerID(playerID).SetAssessmentType(esportsassessment.AssessmentTypeSpotCheck).
		SetTargetLevel(level).SetStatus(status).SetSource(esportsassessment.SourceSystem).
		SetTotalScore(40).SetScoredAt(createdAt.UTC()).SetCreatedAt(createdAt.UTC()).SaveX(ctx)
	// 真有打分明细 = 该期由考官真实评过（区别于定期复核自己生成的降级记录）。
	db.EsportsAssessmentScore.Create().SetAssessmentID(item.ID).
		SetDimension(esportsassessmentscore.DimensionGameSkill).
		SetWeightBp(2000).SetScore(40).SetComment("抽查评语").SaveX(ctx)
	return item
}

// 连续 N 期抽查不达标的在岗陪玩应自动降一级并留下时间与依据。
func TestPlayerLevelReviewDemotesLongTermUnderperformers(t *testing.T) {
	db := openAssessmentDB(t, "esports_r4_level_review")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedUpgradePlayer(t, db, 701, "senior", now.AddDate(0, 0, -180))
	for i := 0; i < 3; i++ {
		// 每期 30 天，倒序落在第 i+1 期窗口内。
		created := now.AddDate(0, 0, -30*(i+1)).Add(time.Hour)
		seedScoredSpotCheck(t, db, player.ID, "senior", true, created)
	}
	seedUpgradeConfig(t, db, levelReviewCycleDaysKeyLiteral, "30")
	seedUpgradeConfig(t, db, levelReviewFailPeriodsKeyLiteral, "3")

	demoted, err := runPlayerLevelReview(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("等级定期复核任务失败：%v", err)
	}
	if demoted != 1 {
		t.Fatalf("应降级 1 人，实际 %d", demoted)
	}
	reloaded, err := db.EsportsPlayer.Get(ctx, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Level != "skilled" {
		t.Fatalf("等级应降一档为 skilled，实际 %s", reloaded.Level)
	}
	if reloaded.LevelAppraisedAt == nil {
		t.Fatal("降级未落时间（level_appraised_at）")
	}
	// 降级记录：一条无打分明细的 system 抽查复核单，note 留依据。
	records := db.EsportsAssessment.Query().Where(
		esportsassessment.PlayerIDEQ(player.ID),
		esportsassessment.AssessmentTypeEQ(esportsassessment.AssessmentTypeSpotCheck),
		esportsassessment.StatusEQ(esportsassessment.StatusFailed),
		esportsassessment.SourceEQ(esportsassessment.SourceSystem),
	).AllX(ctx)
	var record *ent.EsportsAssessment
	for _, row := range records {
		if !db.EsportsAssessmentScore.Query().Where(
			esportsassessmentscore.AssessmentIDEQ(row.ID),
		).ExistX(ctx) {
			record = row
		}
	}
	if record == nil {
		t.Fatal("未落降级记录（无打分明细的 system 抽查复核单）")
	}
	if record.Note == "" || record.ScoredAt == nil {
		t.Fatalf("降级记录应留时间与依据：%+v", record)
	}

	// 同一复核周期内幂等：不重复降级。
	again, err := runPlayerLevelReview(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("重复运行复核任务失败：%v", err)
	}
	if again != 0 {
		t.Fatalf("同一周期内不应重复降级，实际 %d", again)
	}
	if level := db.EsportsPlayer.GetX(ctx, player.ID).Level; level != "skilled" {
		t.Fatalf("重复运行后等级应保持 skilled，实际 %s", level)
	}
}

// 期数不足（未达「连续 N 期」阈值）不得降级，且上期通过会打断连续计数。
func TestPlayerLevelReviewNeedsConsecutiveFailures(t *testing.T) {
	db := openAssessmentDB(t, "esports_r4_level_review_guard")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	short := seedUpgradePlayer(t, db, 702, "senior", now.AddDate(0, 0, -180))
	seedScoredSpotCheck(t, db, short.ID, "senior", true, now.AddDate(0, 0, -30).Add(time.Hour))
	seedScoredSpotCheck(t, db, short.ID, "senior", true, now.AddDate(0, 0, -60).Add(time.Hour))

	broken := seedUpgradePlayer(t, db, 703, "senior", now.AddDate(0, 0, -180))
	seedScoredSpotCheck(t, db, broken.ID, "senior", true, now.AddDate(0, 0, -30).Add(time.Hour))
	seedScoredSpotCheck(t, db, broken.ID, "senior", false, now.AddDate(0, 0, -60).Add(time.Hour))
	seedScoredSpotCheck(t, db, broken.ID, "senior", true, now.AddDate(0, 0, -90).Add(time.Hour))

	seedUpgradeConfig(t, db, levelReviewCycleDaysKeyLiteral, "30")
	seedUpgradeConfig(t, db, levelReviewFailPeriodsKeyLiteral, "3")

	demoted, err := runPlayerLevelReview(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("等级定期复核任务失败：%v", err)
	}
	if demoted != 0 {
		t.Fatalf("未达连续 N 期阈值不应降级，实际降级 %d 人", demoted)
	}
	if level := db.EsportsPlayer.GetX(ctx, short.ID).Level; level != "senior" {
		t.Fatalf("期数不足不应降级，实际 %s", level)
	}
	if level := db.EsportsPlayer.GetX(ctx, broken.ID).Level; level != "senior" {
		t.Fatalf("连续计数被打断不应降级，实际 %s", level)
	}
}

// ── ：评分响应补计算周期（周期口径 + 起止时间） ──

func TestPlayerRatingSummaryReturnsPeriod(t *testing.T) {
	db := openAssessmentDB(t, "esports_r4_rating_period")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedPlayer(t, db, 801, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	older := now.Add(-48 * time.Hour)
	newer := now.Add(-1 * time.Hour)
	for i, at := range []time.Time{older, newer} {
		db.EsportsPlayerReview.Create().SetOrderID(8100 + i).SetCustomerID(11).
			SetPlayerID(player.ID).SetRating(5).SetVerified(true).
			SetStatus(esportsplayerreview.StatusNormal).SetCreatedAt(at).SaveX(ctx)
	}
	view, err := playerRatingSummary(ctx, db, player.ID)
	if err != nil {
		t.Fatalf("综合评分失败：%v", err)
	}
	if view.PeriodStart == "" || view.PeriodEnd == "" || view.PeriodScope == "" || view.ComputedAt == "" {
		t.Fatalf("评分响应应带非空计算周期字段：%+v", view)
	}
	start, err := time.Parse(time.RFC3339, view.PeriodStart)
	if err != nil {
		t.Fatalf("周期起点不是 RFC3339：%q", view.PeriodStart)
	}
	end, err := time.Parse(time.RFC3339, view.PeriodEnd)
	if err != nil {
		t.Fatalf("周期终点不是 RFC3339：%q", view.PeriodEnd)
	}
	if !start.Before(end) {
		t.Fatalf("周期起点应早于终点：%s ~ %s", view.PeriodStart, view.PeriodEnd)
	}
	if view.PeriodDays < 2 || view.PeriodDays > 4 {
		t.Fatalf("周期天数异常：%d（应覆盖 47 小时跨度）", view.PeriodDays)
	}

	// 无样本时周期字段同样非空（口径：以计算时刻为起止）。
	empty := seedPlayer(t, db, 802, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	blank, err := playerRatingSummary(ctx, db, empty.ID)
	if err != nil {
		t.Fatalf("无样本综合评分失败：%v", err)
	}
	if blank.PeriodStart == "" || blank.PeriodEnd == "" || blank.PeriodScope != "empty" {
		t.Fatalf("无样本时周期字段应非空且口径为 empty：%+v", blank)
	}
}

// ── ：异常刷评自动识别 + 评价申诉流水 ──

// 命中刷评规则的评价应自动置 disputed 并留痕（规则码 + 明细 + 时间）。
func TestDetectReviewBrushingMarksDisputedWithTrace(t *testing.T) {
	db := openAssessmentDB(t, "esports_r4_review_brush")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedUpgradeConfig(t, db, reviewBrushRuleKeyLiteral,
		`{"window_hours":24,"max_reviews_per_customer":3,"identical_comment":true}`)
	player := seedPlayer(t, db, 901, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	for i := 0; i < 3; i++ {
		db.EsportsPlayerReview.Create().SetOrderID(9100 + i).SetCustomerID(21).
			SetPlayerID(player.ID).SetRating(5).SetComment("好评").
			SetVerified(true).SetStatus(esportsplayerreview.StatusNormal).
			SetCreatedAt(now.Add(-time.Hour)).SaveX(ctx)
	}

	count, err := detectReviewBrushing(ctx, db, now, 200)
	if err != nil {
		t.Fatalf("刷评识别失败：%v", err)
	}
	if count != 3 {
		t.Fatalf("应识别 3 条异常评价，实际 %d", count)
	}
	rows := db.EsportsPlayerReview.Query().Where(esportsplayerreview.PlayerIDEQ(player.ID)).AllX(ctx)
	for _, row := range rows {
		if row.Status != esportsplayerreview.StatusDisputed {
			t.Fatalf("命中刷评规则的评价应置 disputed，实际 %s", row.Status)
		}
		if !row.AutoDisputed || row.DisputeRuleCode == "" || row.DisputedAt == nil {
			t.Fatalf("自动刷评识别未留痕：%+v", row)
		}
		if len(row.DisputeDetail) == 0 {
			t.Fatalf("刷评识别缺少命中明细：%+v", row)
		}
	}
	// 重复运行不重复留痕（已 disputed 的不再改写时间）。
	if again, err := detectReviewBrushing(ctx, db, now, 200); err != nil || again != 0 {
		t.Fatalf("重复识别应为 0，实际 %d err=%v", again, err)
	}
	// 被刷评标记后不计入综合评分。
	summary, err := playerRatingSummary(ctx, db, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.SampleCount != 0 {
		t.Fatalf("被判定刷评的评价不应计入综合评分，实际样本 %d", summary.SampleCount)
	}
}

// 未配置刷评规则时明确不识别（不伪成功），配置非法同样按关闭处理。
func TestDetectReviewBrushingDisabledWithoutRule(t *testing.T) {
	db := openAssessmentDB(t, "esports_r4_review_brush_off")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedPlayer(t, db, 902, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	for i := 0; i < 4; i++ {
		db.EsportsPlayerReview.Create().SetOrderID(9200 + i).SetCustomerID(22).
			SetPlayerID(player.ID).SetRating(5).SetComment("好评").
			SetVerified(true).SetStatus(esportsplayerreview.StatusNormal).
			SetCreatedAt(now.Add(-time.Hour)).SaveX(ctx)
	}
	count, err := detectReviewBrushing(ctx, db, now, 200)
	if err != nil {
		t.Fatalf("未配置规则时不应报错：%v", err)
	}
	if count != 0 {
		t.Fatalf("未配置规则时应明确不识别，实际标记 %d 条", count)
	}
	if n := db.EsportsPlayerReview.Query().Where(
		esportsplayerreview.StatusEQ(esportsplayerreview.StatusDisputed),
	).CountX(ctx); n != 0 {
		t.Fatalf("未配置规则不应改写评价状态，实际 %d 条", n)
	}
}

// 评价申诉：发起 → 评价转 appeal_pending → 管理员复核成立后复原 normal。
func TestReviewAppealFlow(t *testing.T) {
	db := openAssessmentDB(t, "esports_r4_review_appeal")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedPlayer(t, db, 903, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	review := db.EsportsPlayerReview.Create().SetOrderID(9300).SetCustomerID(23).
		SetPlayerID(player.ID).SetRating(2).SetComment("差评").
		SetVerified(true).SetStatus(esportsplayerreview.StatusNormal).
		SetCreatedAt(now.Add(-time.Hour)).SaveX(ctx)

	// 无关第三方不能替他人申诉。
	if _, err := createReviewAppeal(ctx, db, review.ID, "customer", 999, "与我无关", "", now); !errors.Is(err, errForbidden) {
		t.Fatalf("非评价当事人申诉应被拒，实际 %v", err)
	}
	appeal, err := createReviewAppeal(ctx, db, review.ID, "customer", 23, "评语被误判", "", now)
	if err != nil {
		t.Fatalf("客户申诉失败：%v", err)
	}
	if appeal.Status != esportsplayerreviewappeal.StatusPending {
		t.Fatalf("申诉应落 pending 复核队列，实际 %s", appeal.Status)
	}
	reloaded := db.EsportsPlayerReview.GetX(ctx, review.ID)
	if reloaded.Status != esportsplayerreview.StatusAppealPending {
		t.Fatalf("申诉后评价应转 appeal_pending，实际 %s", reloaded.Status)
	}
	// 每条评价仅一条申诉流水。
	if _, err := createReviewAppeal(ctx, db, review.ID, "customer", 23, "重复申诉", "", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复申诉应冲突，实际 %v", err)
	}

	appeals, err := listReviewAppeals(ctx, db, "pending", 50)
	if err != nil || len(appeals) != 1 {
		t.Fatalf("待复核申诉列表异常：%v, %v", appeals, err)
	}

	// 复核成立 → 复原 normal 且清自动判定标记。
	restored, err := reviewReviewAppeal(ctx, db, appeal.ID, 99, true, "误判，恢复", now)
	if err != nil {
		t.Fatalf("申诉复核失败：%v", err)
	}
	if restored.Status != esportsplayerreview.StatusNormal || restored.AutoDisputed {
		t.Fatalf("申诉成立应复原 normal 且清自动判定标记：%+v", restored)
	}
	stored := db.EsportsPlayerReviewAppeal.GetX(ctx, appeal.ID)
	if stored.Status != esportsplayerreviewappeal.StatusApproved || stored.ReviewedAt == nil {
		t.Fatalf("申诉流水未落复核结论：%+v", stored)
	}
	// 已复核的申诉不能再次复核。
	if _, err := reviewReviewAppeal(ctx, db, appeal.ID, 99, true, "再复核", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复复核应冲突，实际 %v", err)
	}
}

// 被自动判定的刷评评价，陪玩本人也可申诉，驳回后维持 disputed。
func TestReviewAppealByPlayerRejectedKeepsDisputed(t *testing.T) {
	db := openAssessmentDB(t, "esports_r4_review_appeal_player")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedPlayer(t, db, 904, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	review := db.EsportsPlayerReview.Create().SetOrderID(9400).SetCustomerID(24).
		SetPlayerID(player.ID).SetRating(5).SetComment("好评").
		SetVerified(true).SetStatus(esportsplayerreview.StatusDisputed).
		SetAutoDisputed(true).SetDisputeRuleCode("review_burst_customer").
		SetDisputedAt(now.Add(-time.Hour)).SetCreatedAt(now.Add(-time.Hour)).SaveX(ctx)

	appeal, err := createReviewAppeal(ctx, db, review.ID, "player", player.ID, "并非刷评", "", now)
	if err != nil {
		t.Fatalf("陪玩申诉失败：%v", err)
	}
	if _, err := reviewReviewAppeal(ctx, db, appeal.ID, 99, false, "维持原判", now); err != nil {
		t.Fatalf("申诉驳回失败：%v", err)
	}
	after := db.EsportsPlayerReview.GetX(ctx, review.ID)
	if after.Status != esportsplayerreview.StatusDisputed {
		t.Fatalf("申诉被驳回应维持 disputed，实际 %s", after.Status)
	}
}

// 超过申诉时限的评价不可申诉。
func TestReviewAppealWindowEnforced(t *testing.T) {
	db := openAssessmentDB(t, "esports_r4_review_appeal_window")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedUpgradeConfig(t, db, reviewAppealWindowDaysKeyLiteral, "7")
	player := seedPlayer(t, db, 905, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	old := db.EsportsPlayerReview.Create().SetOrderID(9500).SetCustomerID(25).
		SetPlayerID(player.ID).SetRating(2).SetComment("过期评价").
		SetVerified(true).SetStatus(esportsplayerreview.StatusNormal).
		SetCreatedAt(now.AddDate(0, 0, -30)).SaveX(ctx)

	if _, err := createReviewAppeal(ctx, db, old.ID, "customer", 25, "迟到的申诉", "", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超时限申诉应被拒，实际 %v", err)
	}
	// 0 = 不限时：同一评价在关闭时限后可以申诉。
	upsertAssessmentConfig(t, db, reviewAppealWindowDaysKeyLiteral, "0")
	if _, err := createReviewAppeal(ctx, db, old.ID, "customer", 25, "不限时申诉", "", now); err != nil {
		t.Fatalf("时限为 0 时应可申诉，实际 %v", err)
	}
}

// upsertAssessmentConfig 覆盖写入一条配置（seedUpgradeConfig 是纯新增，改值需先更新既有行）。
func upsertAssessmentConfig(t *testing.T, db *ent.Client, key, value string) {
	t.Helper()
	ctx := t.Context()
	changed, err := db.SystemConfig.Update().Where(systemconfig.KeyEQ(key)).SetValue(value).Save(ctx)
	if err != nil {
		t.Fatalf("更新配置 %s 失败：%v", key, err)
	}
	if changed == 0 {
		seedUpgradeConfig(t, db, key, value)
	}
}

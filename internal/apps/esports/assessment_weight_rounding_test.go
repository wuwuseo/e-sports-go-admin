package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsassessment"
	"go-admin/internal/ent/esportsplayer"
)

// P0-14 回归：加权总分必须先累加「分数×权重」分子、最后统一四舍五入一次。
// 旧实现 `total += score*weight/10000` 逐维整除再累加，每个维度丢一次余数：
// 等权回落（1667/1667/1667/1667/1666/1666）下六维满分只得 96。
func TestAssessmentEqualWeightFullScoreIsExactly100(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_rounding_full")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	// 用例前提：entry 未配置权重时回落等权，且权重合计恰为 10000 但不能被六维整除。
	weights, err := resolveAssessmentWeights(ctx, db, "entry")
	if err != nil {
		t.Fatalf("读取等权回落失败：%v", err)
	}
	if weights["game_skill"] != 1667 || weights["stability"] != 1666 {
		t.Fatalf("等权回落权重不符（应为 1667×4 + 1666×2）：%+v", weights)
	}

	player := seedPlayer(t, db, 1201, esportsplayer.QualificationPending, esportsplayer.PresenceOffline)
	seedVerifiedIdentity(t, db, player.ID)
	assessment := runScoredAssessmentForTest(t, db, "entry", "", player.ID, allDimensionScores(100), now)

	if assessment.TotalScore != 100 {
		t.Fatalf("六维满分加权总分应为 100（旧实现整数截断只得 96），实际 %d", assessment.TotalScore)
	}
	if assessment.Status != esportsassessment.StatusPassed {
		t.Fatalf("六维满分应判定通过，实际 %s", assessment.Status)
	}
	if got := db.EsportsPlayer.GetX(ctx, player.ID); got.Qualification != esportsplayer.QualificationQualified {
		t.Fatalf("满分通过后未开通接单权限：%+v", got)
	}
}

// P0-14 回归：等权下六维均 60 分必须恰好 60 分并通过（旧实现只得 58，60 分及格线被系统性误判）。
func TestAssessmentEqualWeightSixtyEachPasses(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_rounding_sixty")
	defer db.Close()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	player := seedPlayer(t, db, 1202, esportsplayer.QualificationPending, esportsplayer.PresenceOffline)
	seedVerifiedIdentity(t, db, player.ID)
	assessment := runScoredAssessmentForTest(t, db, "entry", "", player.ID, allDimensionScores(60), now)

	if assessment.TotalScore != 60 {
		t.Fatalf("六维均 60 的加权总分应为 60（旧实现整数截断只得 58），实际 %d", assessment.TotalScore)
	}
	if assessment.Status != esportsassessment.StatusPassed {
		t.Fatalf("恰好 60 分应判定通过（及格线 60），实际 %s", assessment.Status)
	}
}

// P0-14 回归：尾差必须一次性四舍五入（半值进位），不能逐维截断。
// 权重 5000/1000×5、仅游戏技能 3 分：加权分子 15000 / 10000 = 1.5 → 应进位为 2；
// 旧实现逐维截断为 3*5000/10000=1，其余维度 0，合计只有 1。
func TestAssessmentWeightedTotalRoundsHalfUp(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_rounding_half_up")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	if _, err := setAssessmentRules(ctx, db, "spot_check", []assessmentWeightInput{
		{Dimension: "game_skill", WeightBP: 5000},
		{Dimension: "teaching", WeightBP: 1000},
		{Dimension: "attitude", WeightBP: 1000},
		{Dimension: "communication", WeightBP: 1000},
		{Dimension: "compliance", WeightBP: 1000},
		{Dimension: "stability", WeightBP: 1000},
	}); err != nil {
		t.Fatalf("配置权重失败：%v", err)
	}
	player := seedPlayer(t, db, 1203, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seedVerifiedIdentity(t, db, player.ID)

	scores := make([]assessmentScoreInput, 0, len(assessmentDimensions))
	for _, dim := range assessmentDimensions {
		score := 0
		if dim == "game_skill" {
			score = 3
		}
		scores = append(scores, assessmentScoreInput{Dimension: dim, Score: score, Comment: "测试评语"})
	}
	assessment := runScoredAssessmentForTest(t, db, "spot_check", "novice", player.ID, scores, now)

	if assessment.TotalScore != 2 {
		t.Fatalf("加权尾差应按四舍五入进位为 2（旧实现截断得 1），实际 %d", assessment.TotalScore)
	}
}

// runScoredAssessmentForTest 建单 → 分配考官 → 打分，返回落库后的考核任务（含总分与结论）。
// P0-14 用例共用同一调用链，避免重复样板；考核类型为 entry 时 targetLevel 可留空。
func runScoredAssessmentForTest(t *testing.T, db *ent.Client, assessmentType, targetLevel string, playerID int,
	scores []assessmentScoreInput, now time.Time) *ent.EsportsAssessment {
	t.Helper()
	ctx := t.Context()
	assessment, err := createAssessment(ctx, db, 11, assessmentInput{
		PlayerID: playerID, AssessmentType: assessmentType, TargetLevel: targetLevel, Game: "王者荣耀",
	}, now)
	if err != nil {
		t.Fatalf("创建考核任务失败：%v", err)
	}
	if _, err = assignAssessment(ctx, db, assessment.ID, 11, nil, now); err != nil {
		t.Fatalf("分配考官失败：%v", err)
	}
	scored, err := scoreAssessment(ctx, db, assessment.ID, 11, scores, "测试结论", now)
	if err != nil {
		t.Fatalf("打分失败：%v", err)
	}
	return scored
}

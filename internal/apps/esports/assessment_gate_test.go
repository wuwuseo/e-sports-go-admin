package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsassessment"
	"go-admin/internal/ent/esportsassessmentappeal"
	"go-admin/internal/ent/esportsassessmentscore"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayeridentity"
)

// seedVerifiedIdentity 直接落一条已通过的实名认证记录，供考核定级用例使用。
func seedVerifiedIdentity(t *testing.T, db *ent.Client, playerID int) {
	t.Helper()
	db.EsportsPlayerIdentity.Create().SetPlayerID(playerID).SetRealName("张三").
		SetIDNumberMasked("110101********1234").
		SetStatus(esportsplayeridentity.StatusVerified).SaveX(t.Context())
}

// 未分配考官的 pending 考核不可评分：创建人本人也不行，避免绕过分配环节直接推动定级。
func TestScoreAssessmentRejectsUnassignedExaminer(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_gate_unassigned")
	defer db.Close()
	ctx := t.Context()
	player := seedPlayer(t, db, 101, esportsplayer.QualificationPending, esportsplayer.PresenceOffline)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	assessment, err := createAssessment(ctx, db, 11, assessmentInput{PlayerID: player.ID, AssessmentType: "entry"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.ExaminerAdminID != nil {
		t.Fatalf("用例前提不成立，考官应未分配：%+v", assessment)
	}
	if _, err := scoreAssessment(ctx, db, assessment.ID, 11, allDimensionScores(100), "未分配考官", now); !errors.Is(err, errForbidden) {
		t.Fatalf("未分配考官打分未被拦截：%v", err)
	}
	// 拦截发生在写库之前：考核仍为待处理，评分明细为空，陪玩资格未变。
	got := db.EsportsAssessment.GetX(ctx, assessment.ID)
	if got.Status != esportsassessment.StatusPending {
		t.Fatalf("考核状态被意外改动：%s", got.Status)
	}
	if db.EsportsAssessmentScore.Query().Where(esportsassessmentscore.AssessmentIDEQ(assessment.ID)).CountX(ctx) != 0 {
		t.Fatal("未分配考官的打分不应落库")
	}
	if db.EsportsPlayer.GetX(ctx, player.ID).Qualification != esportsplayer.QualificationPending {
		t.Fatal("未分配考官的打分不应改动陪玩资格")
	}
}

// 已分配考官后，同一管理员可正常打分（实名已通过则按目标等级定级）。
func TestScoreAssessmentAllowsAssignedExaminer(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_gate_assigned")
	defer db.Close()
	ctx := t.Context()
	player := seedPlayer(t, db, 102, esportsplayer.QualificationPending, esportsplayer.PresenceOffline)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	seedVerifiedIdentity(t, db, player.ID)

	assessment, err := createAssessment(ctx, db, 11, assessmentInput{
		PlayerID: player.ID, AssessmentType: "entry",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assignAssessment(ctx, db, assessment.ID, 11, nil, now); err != nil {
		t.Fatal(err)
	}
	scored, err := scoreAssessment(ctx, db, assessment.ID, 11, allDimensionScores(100), "表现优秀", now)
	if err != nil || scored.Status != esportsassessment.StatusPassed {
		t.Fatalf("已分配考官打分失败：%+v, %v", scored, err)
	}
	if db.EsportsAssessmentScore.Query().Where(esportsassessmentscore.AssessmentIDEQ(assessment.ID)).CountX(ctx) != 6 {
		t.Fatal("已分配考官打分未写入逐维度明细")
	}
}

// 实名未通过但考核通过：返回业务可读错误，且评分明细与考核状态整体回滚。
func TestScoreAssessmentBlocksUnverifiedQualification(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_gate_unverified")
	defer db.Close()
	ctx := t.Context()
	player := seedPlayer(t, db, 103, esportsplayer.QualificationPending, esportsplayer.PresenceOffline)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	assessment, err := createAssessment(ctx, db, 11, assessmentInput{PlayerID: player.ID, AssessmentType: "entry"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assignAssessment(ctx, db, assessment.ID, 11, nil, now); err != nil {
		t.Fatal(err)
	}
	_, err = scoreAssessment(ctx, db, assessment.ID, 11, allDimensionScores(100), "表现优秀", now)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("实名未通过未被拦截：%v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "实名认证未通过") {
		t.Fatalf("错误信息不可读：%v", err)
	}
	// 事务回滚验证：考核停在 scheduled，总分与逐维度评分均未落地。
	got := db.EsportsAssessment.GetX(ctx, assessment.ID)
	if got.Status != esportsassessment.StatusScheduled || got.TotalScore != 0 || got.ScoredAt != nil {
		t.Fatalf("考核状态未回滚：%+v", got)
	}
	if db.EsportsAssessmentScore.Query().Where(esportsassessmentscore.AssessmentIDEQ(assessment.ID)).CountX(ctx) != 0 {
		t.Fatal("评分明细未回滚")
	}
	if db.EsportsPlayer.GetX(ctx, player.ID).Qualification != esportsplayer.QualificationPending {
		t.Fatal("实名未通过却改动了陪玩资格")
	}
}

// 实名已通过：按 target_level 写入等级并置为合格。
func TestScoreAssessmentQualifiesVerifiedPlayerWithTargetLevel(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_gate_verified")
	defer db.Close()
	ctx := t.Context()
	player := seedPlayer(t, db, 104, esportsplayer.QualificationPending, esportsplayer.PresenceOffline)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	seedVerifiedIdentity(t, db, player.ID)

	assessment, err := createAssessment(ctx, db, 11, assessmentInput{
		PlayerID: player.ID, AssessmentType: "upgrade", TargetLevel: "senior",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assignAssessment(ctx, db, assessment.ID, 11, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := scoreAssessment(ctx, db, assessment.ID, 11, allDimensionScores(100), "晋级通过", now); err != nil {
		t.Fatal(err)
	}
	updated := db.EsportsPlayer.GetX(ctx, player.ID)
	if updated.Qualification != esportsplayer.QualificationQualified || updated.Level != esportsplayer.LevelSenior || updated.LevelAppraisedAt == nil {
		t.Fatalf("未按 target_level 定级：%+v", updated)
	}
}

// 申诉改判复用同一定级路径：实名未通过时同样拒绝，且申诉与考核状态回滚。
func TestReviewAppealBlocksUnverifiedQualification(t *testing.T) {
	db := openAssessmentDB(t, "esports_assessment_gate_appeal")
	defer db.Close()
	ctx := t.Context()
	player := seedPlayer(t, db, 105, esportsplayer.QualificationPending, esportsplayer.PresenceOffline)
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
	if _, err := createAppeal(ctx, db, player.ID, assessment.ID, "成绩有误", now); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewAppeal(ctx, db, assessment.ID, 11, true, "改判通过", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("申诉改判未受实名门槛约束：%v", err)
	}
	got := db.EsportsAssessment.GetX(ctx, assessment.ID)
	if got.Status != esportsassessment.StatusFailed || got.AppealStatus != esportsassessment.AppealStatusPending {
		t.Fatalf("申诉改判未回滚：%+v", got)
	}
	if db.EsportsAssessmentAppeal.Query().Where(
		esportsassessmentappeal.AssessmentIDEQ(assessment.ID),
	).OnlyX(ctx).Status != esportsassessmentappeal.StatusPending {
		t.Fatal("申诉记录状态未回滚")
	}
	if db.EsportsPlayer.GetX(ctx, player.ID).Qualification != esportsplayer.QualificationPending {
		t.Fatal("实名未通过却因申诉改判改了资格")
	}
}

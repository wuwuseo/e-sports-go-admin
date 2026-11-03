package esports

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsassessment"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"
)

func TestGameLevelSpotCheckAppealDemotesPartition(t *testing.T) {
	db := openAssessmentDB(t, "review_spot_game_demotion")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedPlayer(t, db, 99001, esportsplayer.QualificationPending, esportsplayer.PresenceOnline)
	seedVerifiedIdentity(t, db, player.ID)
	runScoredAssessmentForTest(t, db, "entry", "senior", player.ID, allDimensionScores(100), now)
	spot := runScoredAssessmentForTest(t, db, "spot_check", "senior", player.ID, allDimensionScores(100), now)
	if _, err := createAppeal(ctx, db, player.ID, spot.ID, "抽查结论有误", now); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewAppeal(ctx, db, spot.ID, 22, true, "改判不通过", now); err != nil {
		t.Fatal(err)
	}
	got := db.EsportsPlayer.GetX(ctx, player.ID)
	if got.GameLevels["王者荣耀"] != "skilled" {
		t.Fatalf("抽查改判不通过应从 senior 降为 skilled，实际分区等级 %q", got.GameLevels["王者荣耀"])
	}
	if got.GameLevels["英雄联盟"] != "novice" || got.Qualification != esportsplayer.QualificationSuspended {
		t.Fatalf("抽查改判影响了其它分区或未暂停资格：%+v", got)
	}
}

func TestGameLevelUpgradeSelectsPartition(t *testing.T) {
	db := openAssessmentDB(t, "review_upgrade_other_game")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedUpgradeEligiblePlayer(t, db, 99002, "novice", now.AddDate(0, 0, -60))
	db.EsportsPlayer.UpdateOneID(player.ID).
		SetGames([]string{"王者荣耀", "英雄联盟"}).
		SetGameLevels(map[string]string{"王者荣耀": "mentor", "英雄联盟": "novice"}).
		SetLevel("mentor").ExecX(ctx)
	assessment, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, " 英雄联盟 ", now)
	if err != nil {
		t.Fatalf("英雄联盟仍可升级，却被首个分区拦截：%v", err)
	}
	if assessment.Game != "英雄联盟" || assessment.TargetLevel != "skilled" {
		t.Fatalf("升级未按指定分区推导目标：%+v", assessment)
	}
	if _, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, "", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("升级申请未强制选择分区：%v", err)
	}
	if _, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, "未开通分区", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未开通分区可以申请升级：%v", err)
	}
	repeat, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, "英雄联盟", now)
	if err != nil || repeat.ID != assessment.ID {
		t.Fatalf("指定分区重复申请不幂等：%+v, %v", repeat, err)
	}
	if _, err := assignAssessment(ctx, db, assessment.ID, 11, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := scoreAssessment(ctx, db, assessment.ID, 11, allDimensionScores(100), "升级通过", now); err != nil {
		t.Fatal(err)
	}
	got := db.EsportsPlayer.GetX(ctx, player.ID)
	if got.GameLevels["英雄联盟"] != "skilled" || got.GameLevels["王者荣耀"] != "mentor" || got.Level != "mentor" {
		t.Fatalf("升级影响了其它分区或最高档展示值：%+v", got)
	}
}

func TestGameLevelUpgradeRejectsDifferentPartitionWhilePending(t *testing.T) {
	db := openAssessmentDB(t, "esports_game_level_upgrade_partition_conflict")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedUpgradeEligiblePlayer(t, db, 99105, "novice", now.AddDate(0, 0, -60))
	db.EsportsPlayer.UpdateOneID(player.ID).SetGames([]string{"王者荣耀", "英雄联盟"}).
		SetGameLevels(map[string]string{"王者荣耀": "novice", "英雄联盟": "novice"}).ExecX(ctx)
	if _, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, "王者荣耀", now); err != nil {
		t.Fatal(err)
	}
	if _, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, "英雄联盟", now); !errors.Is(err, errConflict) ||
		!strings.Contains(err.Error(), "王者荣耀") {
		t.Fatalf("不同分区申请被当成既有单返回：%v", err)
	}
	if n := db.EsportsAssessment.Query().CountX(ctx); n != 1 {
		t.Fatalf("不同分区申请绕过单人进行中约束：%d", n)
	}
}

func TestGameLevelAutoUpgradeConsidersAllPartitions(t *testing.T) {
	db := openAssessmentDB(t, "review_auto_upgrade_other_game")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedUpgradeEligiblePlayer(t, db, 99003, "novice", now.AddDate(0, 0, -60))
	db.EsportsPlayer.UpdateOneID(player.ID).
		SetGames([]string{"王者荣耀", "英雄联盟"}).
		SetGameLevels(map[string]string{"王者荣耀": "novice", "英雄联盟": "mentor"}).
		SetLevel("mentor").ExecX(ctx)
	count, err := autoTriggerUpgradeAssessments(ctx, db, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("王者荣耀仍可升级，自动任务应建单 1 个，实际 %d", count)
	}
	assessment := db.EsportsAssessment.Query().OnlyX(ctx)
	if assessment.Game != "王者荣耀" || assessment.TargetLevel != "skilled" {
		t.Fatalf("自动升级未选择尚可升级的分区：%+v", assessment)
	}
}

func TestGameLevelAddonCommissionUsesPartition(t *testing.T) {
	db := openAssessmentDB(t, "review_addon_game_level")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedPlayer(t, db, 99004, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).
		SetGameLevels(map[string]string{"王者荣耀": "mentor", "英雄联盟": "novice"}).
		SetLevel("mentor").ExecX(ctx)
	sku := seedGameSku(t, db, "REVIEW-LOL", "英雄联盟")
	order := seedInServiceOrder(t, db, "review-addon", player.ID)
	db.EsportsOrder.UpdateOneID(order.ID).SetSkuID(sku.ID).ExecX(ctx)
	novice := seedActiveRule(t, db, ruleInput{RuleNo: "REVIEW-N", Scope: "level", ScopeKey: "novice", CalcMode: "percent", ValueBp: 2000, UnitBasis: "sku_quantity"})
	seedActiveRule(t, db, ruleInput{RuleNo: "REVIEW-M", Scope: "level", ScopeKey: "mentor", CalcMode: "percent", ValueBp: 8000, UnitBasis: "sku_quantity"})
	addon, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{AddedCents: 1000, AddedUnits: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := confirmOrderAddon(ctx, db, player.ID, addon.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.CommissionCents != 200 || confirmed.RuleID == nil || *confirmed.RuleID != novice.ID {
		t.Fatalf("英雄联盟 novice 加钟应分成 200 分，实际 %d 分，命中规则 %s", confirmed.CommissionCents, confirmed.RuleNo)
	}
	// 无接单快照的分成解析也必须取同一分区；已锁定规则仍保持快照语义。
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	order = tx.EsportsOrder.GetX(ctx, order.ID)
	rule, err := commissionRuleForMember(ctx, tx, order, commissionMember{PlayerID: player.ID}, now)
	if err != nil || rule == nil || rule.ID != novice.ID {
		t.Fatalf("无快照分成解析仍使用全局等级：%+v, %v", rule, err)
	}
}

func TestGameLevelAssessmentsRequirePartition(t *testing.T) {
	db := openAssessmentDB(t, "esports_game_level_required_partition")
	defer db.Close()
	player := seedPlayer(t, db, 99101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	for _, kind := range []string{"entry", "upgrade", "retry", "spot_check"} {
		for _, game := range []string{"", "  "} {
			_, err := createAssessment(t.Context(), db, 11, assessmentInput{
				PlayerID: player.ID, AssessmentType: kind, TargetLevel: "senior", Game: game,
			}, time.Now())
			if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "分区") {
				t.Fatalf("%s 考核缺分区未被拦截：%v", kind, err)
			}
		}
	}
	if n := db.EsportsAssessment.Query().CountX(t.Context()); n != 0 {
		t.Fatalf("无分区的考核不应落库：%d", n)
	}
}

func TestGameLevelAutoUpgradeSkipsMaxedFirstPartition(t *testing.T) {
	db := openAssessmentDB(t, "esports_game_level_auto_maxed_first")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedUpgradeEligiblePlayer(t, db, 99102, "mentor", now.AddDate(0, 0, -60))
	db.EsportsPlayer.UpdateOneID(player.ID).
		SetGames([]string{"王者荣耀", "英雄联盟"}).
		SetGameLevels(map[string]string{"王者荣耀": "mentor", "英雄联盟": "novice"}).ExecX(ctx)
	count, err := autoTriggerUpgradeAssessments(ctx, db, now, 100)
	if err != nil || count != 1 {
		t.Fatalf("自动升级被首个满级分区拦截：%d, %v", count, err)
	}
	if got := db.EsportsAssessment.Query().OnlyX(ctx); got.Game != "英雄联盟" || got.TargetLevel != "skilled" {
		t.Fatalf("自动升级分区或目标错误：%+v", got)
	}
}

func TestGameLevelUpgradeHandlerAcceptsSelectedPartition(t *testing.T) {
	db := openAssessmentDB(t, "esports_game_level_upgrade_handler")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })
	ctx := t.Context()
	account := db.User.Create().SetUID("game-level-upgrade").SetEmail("game-level-upgrade@example.com").
		SetStatus(user.StatusActive).SaveX(ctx)
	player := seedUpgradeEligiblePlayer(t, db, account.ID, "mentor", time.Now().AddDate(0, 0, -60))
	db.EsportsPlayer.UpdateOneID(player.ID).
		SetGames([]string{"王者荣耀", "英雄联盟"}).
		SetGameLevels(map[string]string{"王者荣耀": "mentor", "英雄联盟": "novice"}).ExecX(ctx)
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", account.UID)
		return c.Next()
	})
	app.Post("/upgrade-apply", playerApplyUpgradeAssessmentHandler)
	for _, body := range []string{`{}`, `{"game":"未开通分区"}`} {
		status, payload := sendJSON(t, app, "POST", "/upgrade-apply", body)
		if status != 400 {
			t.Fatalf("无效分区未被接口拦截：%d, %+v", status, payload)
		}
	}
	status, payload := sendJSON(t, app, "POST", "/upgrade-apply", `{"game":"英雄联盟"}`)
	if status != 200 {
		t.Fatalf("指定分区申请失败：%d, %+v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	if data["game"] != "英雄联盟" || data["target_level"] != "skilled" {
		t.Fatalf("接口未传递指定分区：%+v", payload)
	}
}

// 在档案读取后、写入前插入一次版本变化，确定性覆盖并发写入的冲突窗口。
// 冲突必须回滚考核状态、打分明细和申诉结论，再次提交可使用最新版本成功处理。
func TestGameLevelConcurrentChangeRollsBackAssessment(t *testing.T) {
	for _, action := range []string{"score", "revoke", "restore"} {
		t.Run(action, func(t *testing.T) {
			db := openAssessmentDB(t, "esports_game_level_conflict_"+action)
			defer db.Close()
			ctx := t.Context()
			now := time.Now().UTC()
			player := seedUpgradePlayer(t, db, 99103, "senior", now.AddDate(0, 0, -60))
			var assessment *ent.EsportsAssessment
			var operate func() error
			if action == "score" {
				var err error
				assessment, err = createAssessment(ctx, db, 11, assessmentInput{
					PlayerID: player.ID, AssessmentType: "entry", TargetLevel: "skilled", Game: "英雄联盟",
				}, now)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = assignAssessment(ctx, db, assessment.ID, 11, nil, now); err != nil {
					t.Fatal(err)
				}
				operate = func() error {
					_, err := scoreAssessment(ctx, db, assessment.ID, 11, allDimensionScores(100), "定级通过", now)
					return err
				}
			} else {
				kind, score := "entry", 100
				if action == "restore" {
					kind, score = "spot_check", 0
				}
				assessment = runScoredAssessmentForTest(t, db, kind, "senior", player.ID, allDimensionScores(score), now)
				if _, err := createAppeal(ctx, db, player.ID, assessment.ID, "请求复核", now); err != nil {
					t.Fatal(err)
				}
				operate = func() error {
					_, err := reviewAppeal(ctx, db, assessment.ID, 22, true, "结论改判", now)
					return err
				}
			}
			beforePlayer := db.EsportsPlayer.GetX(ctx, player.ID)
			beforeAssessment := db.EsportsAssessment.GetX(ctx, assessment.ID)
			beforeScores := db.EsportsAssessmentScore.Query().CountX(ctx)
			injected := false
			db.EsportsPlayer.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					if !injected && mutation.Op().Is(ent.OpUpdate|ent.OpUpdateOne) {
						injected = true
						m := mutation.(*ent.EsportsPlayerMutation)
						if err := m.Client().EsportsPlayer.UpdateOneID(player.ID).AddVersion(1).Exec(ctx); err != nil {
							return nil, err
						}
					}
					return next.Mutate(ctx, mutation)
				})
			})
			if err := operate(); !errors.Is(err, errConflict) || !strings.Contains(err.Error(), "档案已变化") {
				t.Fatalf("档案版本变化未返回可重试冲突：%v", err)
			}
			if !injected {
				t.Fatal("未覆盖档案并发更新窗口")
			}
			afterPlayer := db.EsportsPlayer.GetX(ctx, player.ID)
			if !reflect.DeepEqual(beforePlayer.GameLevels, afterPlayer.GameLevels) ||
				!reflect.DeepEqual(beforePlayer.Games, afterPlayer.Games) ||
				beforePlayer.Level != afterPlayer.Level || beforePlayer.Qualification != afterPlayer.Qualification ||
				beforePlayer.Version != afterPlayer.Version {
				t.Fatalf("冲突后档案未整体回滚：%+v", afterPlayer)
			}
			afterAssessment := db.EsportsAssessment.GetX(ctx, assessment.ID)
			if beforeAssessment.Status != afterAssessment.Status ||
				beforeAssessment.AppealStatus != afterAssessment.AppealStatus ||
				beforeAssessment.Version != afterAssessment.Version ||
				beforeScores != db.EsportsAssessmentScore.Query().CountX(ctx) {
				t.Fatalf("冲突后考核状态或评分未回滚：%+v", afterAssessment)
			}
			if err := operate(); err != nil {
				t.Fatalf("冲突后重新提交失败：%v", err)
			}
			if action == "score" {
				got := db.EsportsPlayer.GetX(ctx, player.ID)
				if got.GameLevels["王者荣耀"] != "senior" || got.GameLevels["英雄联盟"] != "skilled" {
					t.Fatalf("重试覆盖了已有分区等级：%+v", got.GameLevels)
				}
			}
		})
	}
}

func TestGameLevelPeriodicReviewIsScopedToPartition(t *testing.T) {
	db := openAssessmentDB(t, "esports_game_level_periodic_partition")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedUpgradePlayer(t, db, 99104, "senior", now.AddDate(0, 0, -180))
	db.EsportsPlayer.UpdateOneID(player.ID).SetGames([]string{"王者荣耀", "英雄联盟"}).
		SetGameLevels(map[string]string{"王者荣耀": "senior", "英雄联盟": "mentor"}).SetLevel("mentor").ExecX(ctx)
	for i := 0; i < 3; i++ {
		created := now.AddDate(0, 0, -30*(i+1)).Add(time.Hour)
		seedScoredSpotCheck(t, db, player.ID, "senior", true, created)
		// 其它分区的通过不能打断王者荣耀的失败期数。
		passed := seedScoredSpotCheck(t, db, player.ID, "mentor", false, created.AddDate(0, 0, 1))
		db.EsportsAssessment.UpdateOneID(passed.ID).SetGame("英雄联盟").ExecX(ctx)
	}
	count, err := runPlayerLevelReview(ctx, db, now, 100)
	if err != nil || count != 1 {
		t.Fatalf("分区复核未执行降级：%d, %v", count, err)
	}
	got := db.EsportsPlayer.GetX(ctx, player.ID)
	if got.GameLevels["王者荣耀"] != "skilled" || got.GameLevels["英雄联盟"] != "mentor" || got.Level != "mentor" {
		t.Fatalf("定期复核降级影响分区错误：%+v", got)
	}
	if count, err := runPlayerLevelReview(ctx, db, now, 100); err != nil || count != 0 {
		t.Fatalf("同周期内重复降级：%d, %v", count, err)
	}
	record := db.EsportsAssessment.Query().Where(esportsassessment.NoteHasPrefix(levelReviewDemotionNotePrefix)).OnlyX(ctx)
	if record.Game != "王者荣耀" || record.TargetLevel != "skilled" {
		t.Fatalf("定期复核记录未绑定分区：%+v", record)
	}
}

func TestGameLevelPeriodicReviewDemotesMultiplePartitions(t *testing.T) {
	db := openAssessmentDB(t, "esports_game_level_periodic_multiple")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedUpgradePlayer(t, db, 99106, "senior", now.AddDate(0, 0, -180))
	db.EsportsPlayer.UpdateOneID(player.ID).SetGames([]string{"王者荣耀", "英雄联盟"}).
		SetGameLevels(map[string]string{"王者荣耀": "senior", "英雄联盟": "mentor"}).SetLevel("mentor").ExecX(ctx)
	for i := 0; i < 3; i++ {
		created := now.AddDate(0, 0, -30*(i+1)).Add(time.Hour)
		seedScoredSpotCheck(t, db, player.ID, "senior", true, created)
		other := seedScoredSpotCheck(t, db, player.ID, "mentor", true, created.AddDate(0, 0, 1))
		db.EsportsAssessment.UpdateOneID(other.ID).SetGame("英雄联盟").ExecX(ctx)
	}
	count, err := runPlayerLevelReview(ctx, db, now, 100)
	if err != nil || count != 1 {
		t.Fatalf("多分区复核应计 1 人且成功处理所有分区：%d, %v", count, err)
	}
	got := db.EsportsPlayer.GetX(ctx, player.ID)
	if got.GameLevels["王者荣耀"] != "skilled" || got.GameLevels["英雄联盟"] != "senior" || got.Level != "senior" {
		t.Fatalf("多个分区的降级互相覆盖：%+v", got)
	}
	if n := db.EsportsAssessment.Query().Where(esportsassessment.NoteHasPrefix(levelReviewDemotionNotePrefix)).CountX(ctx); n != 2 {
		t.Fatalf("应为两个分区分别留痕，实际 %d", n)
	}
	if count, err := runPlayerLevelReview(ctx, db, now, 100); err != nil || count != 0 {
		t.Fatalf("多分区复核重复执行不幂等：%d, %v", count, err)
	}
}

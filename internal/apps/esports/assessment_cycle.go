package esports

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsassessment"
	"go-admin/internal/ent/esportsassessmentscore"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/global"
)

// 考官端周期化考核（随机抽查 / 等级定期复核）。
//
// 两个任务共用一套「周期 + 配置」读取写法，配置键均由主控在 app.go 声明：
//   - 抽查：按比例随机抽取「在岗合格」陪玩建 spot_check 单（不去替考官指定人选，分配仍走既有 assign 接口）；
//   - 复核：连续 N 期抽查不达标者降一档，并落一条可追溯的降级记录（时间 + 依据）。

const (
	// spotCheckRatioBPKey 是单轮抽查的抽取比例（万分比，10000 = 全抽）。
	spotCheckRatioBPKey = "app.esports.spot_check_ratio_bp"
	// spotCheckCycleDaysKey 是同一陪玩两次抽查之间的最小间隔天数。
	spotCheckCycleDaysKey = "app.esports.spot_check_cycle_days"
	// levelReviewCycleDaysKey 是等级复核的「期」长度（天）。
	levelReviewCycleDaysKey = "app.esports.level_review_cycle_days"
	// levelReviewFailPeriodsKey 是触发降级所需的「连续不达标期数」阈值。
	levelReviewFailPeriodsKey = "app.esports.level_review_fail_periods"

	// 以下为代码内真实回落默认值（≤0 / 非法一律回落，无「0 关闭」语义）。
	defaultSpotCheckRatioBP     = 1000 // 10%
	defaultSpotCheckCycleDays   = 7
	defaultLevelReviewCycleDays = 30
	defaultLevelReviewFailTimes = 3

	// spotCheckBatchCap 是单轮扫描的候选陪玩上限（限流，避免一次扫全表）。
	spotCheckBatchCap = 200
	// levelReviewBatchCap 是单轮复核扫描的候选陪玩上限。
	levelReviewBatchCap = 200
	// levelReviewDemotionNotePrefix 是降级记录 note 的前缀，便于事后检索与人工区分。
	levelReviewDemotionNotePrefix = "等级定期复核降级："
)

// ── 随机抽查 ──

// sampleSpotCheckCandidates 按比例从候选陪玩中随机抽样：逐人独立判定，
// ratioBP 为万分比（>=10000 全抽、<=0 不抽）。rng 由调用方注入，任务每次运行新建随机流，
// 测试可注入固定种子以获得可复现结果。
func sampleSpotCheckCandidates(candidates []*ent.EsportsPlayer, ratioBP int, rng *rand.Rand) []*ent.EsportsPlayer {
	if len(candidates) == 0 {
		return nil
	}
	if ratioBP <= 0 {
		return nil
	}
	if ratioBP >= 10000 {
		out := make([]*ent.EsportsPlayer, len(candidates))
		copy(out, candidates)
		return out
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	out := make([]*ent.EsportsPlayer, 0, len(candidates))
	for _, player := range candidates {
		if rng.Intn(10000) < ratioBP {
			out = append(out, player)
		}
	}
	return out
}

// spotCheckBlockedPlayerIDs 返回本周期内不可再抽的陪玩 ID：已有进行中抽查单，
// 或最近一次抽查单创建于 now-cycleDays 之后（周期语义，避免同一人每轮都被抽中）。
func spotCheckBlockedPlayerIDs(ctx context.Context, db *ent.Client, now time.Time, cycleDays int) ([]int, error) {
	rows, err := db.EsportsAssessment.Query().Where(
		esportsassessment.AssessmentTypeEQ(esportsassessment.AssessmentTypeSpotCheck),
		esportsassessment.Or(
			esportsassessment.StatusIn(esportsassessment.StatusPending, esportsassessment.StatusScheduled),
			esportsassessment.CreatedAtGT(now.UTC().AddDate(0, 0, -cycleDays)),
		),
	).Select(esportsassessment.FieldPlayerID).All(ctx)
	if err != nil {
		return nil, err
	}
	blocked := make([]int, 0, len(rows))
	seen := make(map[int]struct{}, len(rows))
	for _, row := range rows {
		if _, ok := seen[row.PlayerID]; ok {
			continue
		}
		seen[row.PlayerID] = struct{}{}
		blocked = append(blocked, row.PlayerID)
	}
	return blocked, nil
}

// generateSpotCheckAssessments 单轮随机抽查生成：
// 候选 = 在岗（qualification=qualified）且在线、且不在本周期拦截名单内的陪玩；
// 按 spot_check_ratio_bp 随机抽样后逐人建 pending 抽查单（目标档位取当前等级，来源 system）。
//
// 幂等依据：候选集在 SQL 层排除「进行中 / 周期内已抽」的陪玩，重复运行不会重复建单。
func generateSpotCheckAssessments(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 500 {
		return 0, errInvalidInput
	}
	ratioBP := assessmentConfigInt(ctx, db, spotCheckRatioBPKey, defaultSpotCheckRatioBP)
	if ratioBP > 10000 {
		ratioBP = defaultSpotCheckRatioBP
	}
	cycleDays := assessmentConfigInt(ctx, db, spotCheckCycleDaysKey, defaultSpotCheckCycleDays)
	blocked, err := spotCheckBlockedPlayerIDs(ctx, db, now, cycleDays)
	if err != nil {
		return 0, err
	}
	q := db.EsportsPlayer.Query().Where(
		esportsplayer.QualificationEQ(esportsplayer.QualificationQualified),
		esportsplayer.PresenceEQ(esportsplayer.PresenceOnline),
	)
	if len(blocked) > 0 {
		q.Where(esportsplayer.IDNotIn(blocked...))
	}
	candidates, err := q.Order(ent.Asc(esportsplayer.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	// 每次运行新建随机流（全局 math/rand 已自动随机播种），保证两轮抽取集合不同。
	picked := sampleSpotCheckCandidates(candidates, ratioBP, rand.New(rand.NewSource(time.Now().UnixNano())))
	created := 0
	for _, player := range picked {
		// 等级按分区独立：对每个已开通且有等级的分区各建一单抽查。
		if len(player.Games) == 0 {
			continue
		}
		for _, game := range player.Games {
			level := playerGameLevel(player, game)
			if level == "" {
				continue
			}
			if _, err = createAssessment(ctx, db, 0, assessmentInput{
				PlayerID:       player.ID,
				AssessmentType: "spot_check",
				TargetLevel:    level,
				Game:           game,
				Source:         "system",
				Note:           fmt.Sprintf("系统按万分比 %d 比例随机抽查（抽查周期 %d 天）", ratioBP, cycleDays),
			}, now); err != nil {
				return created, err
			}
			created++
		}
	}
	return created, nil
}

// generateSpotCheckAssessmentsTask 定时任务入口（签名同既有任务，供 tasks.Register 注册）：
// 需由主控注册为 esports_generate_spot_checks（建议 CronJob `0 0 * * * *`，按小时抽样）。
func generateSpotCheckAssessmentsTask(ctx context.Context, logger io.Writer) error {
	count, err := generateSpotCheckAssessments(ctx, global.ORM, time.Now(), spotCheckBatchCap)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "生成随机抽查单：%d\n", count)
	}
	return err
}

// ── 等级定期复核 ──

// consecutiveSpotCheckFailurePeriods 统计陪玩「从最近一期往前连续不达标」的期数。
//
// 口径：把 now 往前按 cycleDays 天切期，第 i 期窗口为 [now-(i+1)*cycle, now-i*cycle)；
//   - 该期存在「由考官真实打分」的不通过抽查单（status=failed 且有打分明细）→ 不达标期，计数 +1；
//   - 该期存在通过的抽查单 → 打断连续计数；
//   - 该期没有任何抽查结论（未知期）→ 同样打断计数（无证据不判定「长期不达标」）。
//
// 定期复核自身生成的降级记录没有打分明细，因此不会被再次计入，避免自我强化。
func consecutiveSpotCheckFailurePeriods(ctx context.Context, db *ent.Client, playerID int, now time.Time, cycleDays int) (int, error) {
	if playerID < 1 || cycleDays < 1 {
		return 0, errInvalidInput
	}
	from := now.UTC().AddDate(0, 0, -cycleDays*20)
	rows, err := db.EsportsAssessment.Query().Where(
		esportsassessment.PlayerIDEQ(playerID),
		esportsassessment.AssessmentTypeEQ(esportsassessment.AssessmentTypeSpotCheck),
		esportsassessment.StatusIn(esportsassessment.StatusPassed, esportsassessment.StatusFailed),
		esportsassessment.CreatedAtGTE(from),
	).Select(
		esportsassessment.FieldID, esportsassessment.FieldStatus, esportsassessment.FieldCreatedAt,
	).All(ctx)
	if err != nil {
		return 0, err
	}
	// 只有带打分明细的抽查单才作数（区分定期复核自动生成的降级记录）。
	scored := make(map[int]struct{})
	if len(rows) > 0 {
		ids := make([]int, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		scoreRows, err := db.EsportsAssessmentScore.Query().
			Where(esportsassessmentscore.AssessmentIDIn(ids...)).
			Select(esportsassessmentscore.FieldAssessmentID).All(ctx)
		if err != nil {
			return 0, err
		}
		for _, row := range scoreRows {
			scored[row.AssessmentID] = struct{}{}
		}
	}
	window := time.Duration(cycleDays) * 24 * time.Hour
	streak := 0
	for i := 0; ; i++ {
		end := now.UTC().Add(-time.Duration(i) * window)
		start := end.Add(-window)
		failed, passed := false, false
		for _, row := range rows {
			if _, ok := scored[row.ID]; !ok {
				continue
			}
			at := row.CreatedAt.UTC()
			if at.Before(start) || !at.Before(end) {
				continue
			}
			if row.Status == esportsassessment.StatusFailed {
				failed = true
			}
			if row.Status == esportsassessment.StatusPassed {
				passed = true
			}
		}
		if !failed || passed {
			break
		}
		streak++
	}
	return streak, nil
}

// levelReviewDemotionRecorded 判断该陪玩在本复核周期内是否已落过降级记录（幂等闸门）：
// 降级记录 = 无打分明细、note 带前缀的 system 抽查复核单。
func levelReviewDemotionRecorded(ctx context.Context, db *ent.Client, playerID int, now time.Time, cycleDays int) (bool, error) {
	rows, err := db.EsportsAssessment.Query().Where(
		esportsassessment.PlayerIDEQ(playerID),
		esportsassessment.AssessmentTypeEQ(esportsassessment.AssessmentTypeSpotCheck),
		esportsassessment.SourceEQ(esportsassessment.SourceSystem),
		esportsassessment.CreatedAtGT(now.UTC().AddDate(0, 0, -cycleDays)),
	).Select(esportsassessment.FieldID, esportsassessment.FieldNote).All(ctx)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if !strings.HasPrefix(row.Note, levelReviewDemotionNotePrefix) {
			continue
		}
		exists, err := db.EsportsAssessmentScore.Query().
			Where(esportsassessmentscore.AssessmentIDEQ(row.ID)).Exist(ctx)
		if err != nil {
			return false, err
		}
		if !exists {
			return true, nil
		}
	}
	return false, nil
}

// runPlayerLevelReview 单轮等级定期复核：对「连续 N 期抽查不达标」的在岗合格陪玩降一档，
// 并落一条降级记录（时间 + 依据）。返回本轮实际降级人数。
//
// 不降到 novice 以下：已是 novice 的陪玩跳过（保留最低档）。
func runPlayerLevelReview(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 500 {
		return 0, errInvalidInput
	}
	cycleDays := assessmentConfigInt(ctx, db, levelReviewCycleDaysKey, defaultLevelReviewCycleDays)
	failPeriods := assessmentConfigInt(ctx, db, levelReviewFailPeriodsKey, defaultLevelReviewFailTimes)
	players, err := db.EsportsPlayer.Query().Where(
		esportsplayer.QualificationEQ(esportsplayer.QualificationQualified),
		esportsplayer.LevelNEQ(skillLevelLowest(skillLevels(ctx, db))),
	).Order(ent.Asc(esportsplayer.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	demoted := 0
	for _, player := range players {
		streak, err := consecutiveSpotCheckFailurePeriods(ctx, db, player.ID, now, cycleDays)
		if err != nil {
			return demoted, err
		}
		if streak < failPeriods {
			continue
		}
		recorded, err := levelReviewDemotionRecorded(ctx, db, player.ID, now, cycleDays)
		if err != nil {
			return demoted, err
		}
		if recorded {
			continue
		}
		target := demoteSkillLevel(skillLevels(ctx, db), player.Level)
		note := fmt.Sprintf("%s连续 %d 期抽查不达标（复核周期 %d 天，判定时刻 %s），依据：抽查结论不通过",
			levelReviewDemotionNotePrefix, streak, cycleDays, now.In(cstZone).Format("2006-01-02 15:04"))
		if err = applyLevelReviewDemotion(ctx, db, player, target, note, now); err != nil {
			return demoted, err
		}
		demoted++
	}
	return demoted, nil
}

// applyLevelReviewDemotion 在同一事务内降级并留下降级记录（时间 + 依据）。
func applyLevelReviewDemotion(ctx context.Context, db *ent.Client, player *ent.EsportsPlayer, target string, note string, now time.Time) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 降级记录：一条 note 可检索、无打分明细的 system 抽查复核单（时间 + 依据）。
	if _, err = tx.EsportsAssessment.Create().SetTaskNo("LR" + fmt.Sprintf("%d-%d", player.ID, now.UnixNano())).
		SetPlayerID(player.ID).SetAssessmentType(esportsassessment.AssessmentTypeSpotCheck).
		SetTargetLevel(target).SetStatus(esportsassessment.StatusFailed).
		SetSource(esportsassessment.SourceSystem).SetNote(note).
		SetScoredAt(now.UTC()).SetTotalScore(0).Save(ctx); err != nil {
		return err
	}
	if _, err = tx.EsportsPlayer.Update().Where(esportsplayer.IDEQ(player.ID)).
		SetLevel(target).SetLevelAppraisedAt(now.UTC()).AddVersion(1).Save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

// runPlayerLevelReviewTask 定时任务入口：需由主控注册为 esports_run_player_level_review
// （建议 CronJob `0 30 3 * * *`，每日凌晨复核一次）。
func runPlayerLevelReviewTask(ctx context.Context, logger io.Writer) error {
	count, err := runPlayerLevelReview(ctx, global.ORM, time.Now(), levelReviewBatchCap)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "陪玩等级定期复核降级：%d 人\n", count)
	}
	return err
}

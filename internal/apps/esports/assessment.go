package esports

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsassessment"
	"go-admin/internal/ent/esportsassessmentappeal"
	"go-admin/internal/ent/esportsassessmentrule"
	"go-admin/internal/ent/esportsassessmentscore"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayerreview"
	"go-admin/internal/ent/esportsplayerreviewappeal"
	"go-admin/internal/ent/systemconfig"
)

// 考核与评价的系统配置键（缺失时回落默认值）。
const (
	assessmentPassScoreKey     = "app.esports.assessment_pass_score"
	defaultAssessmentPassScore = 60
	reviewMinSamplesKey        = "app.esports.review_min_samples"
	defaultReviewMinSamples    = 5
	reviewRuleVersionKey       = "app.esports.review_rule_version"
	defaultReviewRuleVersion   = "v1"
)

// 考核维度固定六项，与 schema 枚举保持一致。
var assessmentDimensions = []string{
	"game_skill", "teaching", "attitude", "communication", "compliance", "stability",
}

func validAssessmentType(t string) bool {
	switch t {
	case "entry", "upgrade", "spot_check", "retry":
		return true
	}
	return false
}

func validDimension(d string) bool {
	for _, item := range assessmentDimensions {
		if item == d {
			return true
		}
	}
	return false
}

// validPlayerLevel 判断编码是否为当前等级配置中的合法档位。
func validPlayerLevel(levels []SkillLevel, code string) bool {
	_, ok := skillLevelRank(levels, code)
	return ok
}

// assessmentConfigInt 读取整数型系统配置，缺失/非法/非正数时回落默认值。
func assessmentConfigInt(ctx context.Context, db *ent.Client, key string, fallback int) int {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if err != nil {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func assessmentConfigString(ctx context.Context, db *ent.Client, key, fallback string) string {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if err != nil {
		return fallback
	}
	value := strings.TrimSpace(item.Value)
	if value == "" {
		return fallback
	}
	return value
}

// ── 权重配置 ──

type assessmentWeightInput struct {
	Dimension string `json:"dimension"`
	WeightBP  int    `json:"weight_bp"`
}

// listAssessmentRules 列出某考核类型的维度权重配置，按维度固定顺序返回。
func listAssessmentRules(ctx context.Context, db *ent.Client, assessmentType string) ([]*ent.EsportsAssessmentRule, error) {
	assessmentType = strings.TrimSpace(assessmentType)
	if !validAssessmentType(assessmentType) {
		return nil, errInvalidInput
	}
	return db.EsportsAssessmentRule.Query().Where(
		esportsassessmentrule.AssessmentTypeEQ(esportsassessmentrule.AssessmentType(assessmentType)),
	).All(ctx)
}

// setAssessmentRules 覆盖式写入某考核类型的维度权重，须覆盖全部六个维度且合计恰为 10000 基点。
func setAssessmentRules(ctx context.Context, db *ent.Client, assessmentType string, weights []assessmentWeightInput) ([]*ent.EsportsAssessmentRule, error) {
	assessmentType = strings.TrimSpace(assessmentType)
	if !validAssessmentType(assessmentType) || len(weights) != len(assessmentDimensions) {
		return nil, errInvalidInput
	}
	total := 0
	seen := map[string]bool{}
	for _, item := range weights {
		if !validDimension(item.Dimension) || seen[item.Dimension] || item.WeightBP <= 0 || item.WeightBP > 10000 {
			return nil, errInvalidInput
		}
		seen[item.Dimension] = true
		total += item.WeightBP
	}
	if total != 10000 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	typed := esportsassessmentrule.AssessmentType(assessmentType)
	if _, err = tx.EsportsAssessmentRule.Delete().Where(
		esportsassessmentrule.AssessmentTypeEQ(typed),
	).Exec(ctx); err != nil {
		return nil, err
	}
	for _, item := range weights {
		if _, err = tx.EsportsAssessmentRule.Create().SetAssessmentType(typed).
			SetDimension(esportsassessmentrule.Dimension(item.Dimension)).
			SetWeightBp(item.WeightBP).SetVersion(1).Save(ctx); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return listAssessmentRules(ctx, db, assessmentType)
}

// resolveAssessmentWeights 读取某类型的生效权重；未配置时回落等权（余数分给靠前维度，合计仍为 10000）。
func resolveAssessmentWeights(ctx context.Context, db *ent.Client, assessmentType string) (map[string]int, error) {
	rows, err := db.EsportsAssessmentRule.Query().Where(
		esportsassessmentrule.AssessmentTypeEQ(esportsassessmentrule.AssessmentType(assessmentType)),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	weights := map[string]int{}
	if len(rows) == 0 {
		base := 10000 / len(assessmentDimensions)
		remainder := 10000 - base*len(assessmentDimensions)
		for i, dim := range assessmentDimensions {
			w := base
			if i < remainder {
				w++
			}
			weights[dim] = w
		}
		return weights, nil
	}
	for _, row := range rows {
		weights[row.Dimension.String()] = row.WeightBp
	}
	return weights, nil
}

// ── 考核任务 ──

type assessmentInput struct {
	PlayerID        int        `json:"player_id"`
	AssessmentType  string     `json:"assessment_type"`
	TargetLevel     string     `json:"target_level"`
	Game            string     `json:"game"`
	Note            string     `json:"note"`
	Source          string     `json:"source"`
	ExaminerAdminID *int       `json:"examiner_admin_id"`
	ScheduledAt     *time.Time `json:"scheduled_at"`
}

// createAssessment 新建考核任务：入职默认目标 novice，状态待分配考官。
func createAssessment(ctx context.Context, db *ent.Client, adminID int, in assessmentInput, now time.Time) (*ent.EsportsAssessment, error) {
	in.AssessmentType = strings.TrimSpace(in.AssessmentType)
	in.TargetLevel = strings.TrimSpace(in.TargetLevel)
	in.Game = strings.TrimSpace(in.Game)
	in.Note = strings.TrimSpace(in.Note)
	in.Source = strings.TrimSpace(in.Source)
	if in.Source == "" {
		in.Source = "apply"
	}
	if !validAssessmentType(in.AssessmentType) || (in.Source != "apply" && in.Source != "system") ||
		len([]rune(in.Note)) > 500 || len([]rune(in.Game)) > 64 {
		return nil, errInvalidInput
	}
	// 分区必填：入职 / 升级 / 复考都要指明考哪个分区；抽查只复核等级，不绑分区。
	if in.AssessmentType != "spot_check" && in.Game == "" {
		return nil, fmt.Errorf("%w：请指定考核的游戏分区", errInvalidInput)
	}
	// 入职默认定级最低档；其余类型目标等级必填且合法。
	if in.AssessmentType == "entry" && in.TargetLevel == "" {
		in.TargetLevel = skillLevelLowest(skillLevels(ctx, db))
	}
	if in.TargetLevel != "" && !validPlayerLevel(skillLevels(ctx, db), in.TargetLevel) {
		return nil, errInvalidInput
	}
	if in.AssessmentType != "entry" && in.TargetLevel == "" {
		return nil, errInvalidInput
	}
	if _, err := db.EsportsPlayer.Get(ctx, in.PlayerID); err != nil {
		return nil, err
	}
	create := db.EsportsAssessment.Create().SetTaskNo("AS" + uuid.NewString()).
		SetPlayerID(in.PlayerID).SetAssessmentType(esportsassessment.AssessmentType(in.AssessmentType)).
		SetTargetLevel(in.TargetLevel).SetGame(in.Game).SetStatus(esportsassessment.StatusPending).
		SetSource(esportsassessment.Source(in.Source)).SetNote(in.Note)
	// 升级考核写入唯一索引兜底键：同一陪玩至多一条进行中升级单（并发双击不会落两条）。
	if in.AssessmentType == "upgrade" {
		create.SetActiveDedupKey(upgradeDedupKey(in.PlayerID))
	}
	// 陪玩自助申请没有操作管理员，adminID=0 时不落 created_by_admin_id（保持 NULL）。
	if adminID > 0 {
		create.SetCreatedByAdminID(adminID)
	}
	if in.ExaminerAdminID != nil {
		create.SetExaminerAdminID(*in.ExaminerAdminID)
	}
	if in.ScheduledAt != nil {
		create.SetScheduledAt(in.ScheduledAt.UTC())
	}
	assessment, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	return assessment, nil
}

// assignAssessment 分配考官并约定考核时间：pending → scheduled。
func assignAssessment(ctx context.Context, db *ent.Client, id, examinerAdminID int, scheduledAt *time.Time, now time.Time) (*ent.EsportsAssessment, error) {
	if id < 1 || examinerAdminID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	assessment, err := tx.EsportsAssessment.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if assessment.Status != esportsassessment.StatusPending {
		return nil, errConflict
	}
	update := tx.EsportsAssessment.Update().Where(
		esportsassessment.IDEQ(id),
		esportsassessment.StatusEQ(esportsassessment.StatusPending),
	).SetExaminerAdminID(examinerAdminID).SetContactedAt(now.UTC()).
		SetStatus(esportsassessment.StatusScheduled).AddVersion(1)
	if scheduledAt != nil {
		update.SetScheduledAt(scheduledAt.UTC())
	}
	if _, err = update.Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAssessment.Get(ctx, id)
}

type assessmentScoreInput struct {
	Dimension string `json:"dimension"`
	Score     int    `json:"score"`
	Comment   string `json:"comment"`
}

// scoreAssessment 考官逐项打分：写入快照权重的各维度分值（评语必填），按权重加权得总分并判定通过；
// 通过则定级开通接单权限，抽查不通过则降级并暂停接单权限。仅分配到的考官可打分。
func scoreAssessment(ctx context.Context, db *ent.Client, id, adminID int, scores []assessmentScoreInput, note string, now time.Time) (*ent.EsportsAssessment, error) {
	note = strings.TrimSpace(note)
	if id < 1 || len(scores) == 0 || len([]rune(note)) > 500 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	assessment, err := tx.EsportsAssessment.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if assessment.Status != esportsassessment.StatusPending && assessment.Status != esportsassessment.StatusScheduled {
		return nil, errConflict
	}
	// 考官必须先分配、且就是当前操作人。未分配考官（ExaminerAdminID == nil）的任务一律不可评分：
	// 否则任何持有评分权限的管理员都能越过「分配考官」直接打分并推动定级，考核流程形同虚设。
	if assessment.ExaminerAdminID == nil || *assessment.ExaminerAdminID != adminID {
		return nil, errForbidden
	}
	weights, err := resolveAssessmentWeights(ctx, tx.Client(), assessment.AssessmentType.String())
	if err != nil {
		return nil, err
	}
	// 加权分子按 int64 累加，最后统一除一次（P0-14）。
	// 旧实现逐维做 `score*weight/10000` 整数除法再累加，每个维度都丢一次余数：
	// 等权回落（1667/1667/1667/1667/1666/1666）下六维满分只得 96、六维均 60 只得 58，
	// 60 分及格线被系统性误判为不通过。这里只保留一次舍入，判定结果不再随维度数放大误差。
	var numerator int64
	seen := map[string]bool{}
	for _, item := range scores {
		if !validDimension(item.Dimension) || seen[item.Dimension] {
			return nil, errInvalidInput
		}
		if item.Score < 0 || item.Score > 100 {
			return nil, errInvalidInput
		}
		item.Comment = strings.TrimSpace(item.Comment)
		if item.Comment == "" || len([]rune(item.Comment)) > 500 {
			return nil, errInvalidInput
		}
		weight, ok := weights[item.Dimension]
		if !ok {
			return nil, errInvalidInput
		}
		seen[item.Dimension] = true
		numerator += int64(item.Score) * int64(weight)
		if _, err = tx.EsportsAssessmentScore.Create().SetAssessmentID(id).
			SetDimension(esportsassessmentscore.Dimension(item.Dimension)).
			SetWeightBp(weight).SetScore(item.Score).SetComment(item.Comment).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
	}
	// 须覆盖全部配置维度（等权回落时即六项），并累计实际参与判定的权重合计。
	weightTotal := int64(0)
	for dim, weight := range weights {
		if weight <= 0 {
			continue
		}
		if !seen[dim] {
			return nil, errInvalidInput
		}
		weightTotal += int64(weight)
	}
	// 定点四舍五入（round-half-up）：(Σ分数×权重 + 总权重/2) / 总权重，全程整数、无浮点参与判定。
	// 权重合计正常为 10000，此处不写死，兼容存量 / 未满 10000 的历史配置。
	total := 0
	if weightTotal > 0 {
		total = int((numerator + weightTotal/2) / weightTotal)
	}
	passScore := assessmentConfigInt(ctx, tx.Client(), assessmentPassScoreKey, defaultAssessmentPassScore)
	passed := total >= passScore
	status := esportsassessment.StatusFailed
	if passed {
		status = esportsassessment.StatusPassed
	}
	if _, err = tx.EsportsAssessment.Update().Where(esportsassessment.IDEQ(id)).
		SetStatus(status).SetTotalScore(total).SetNote(note).SetScoredAt(now.UTC()).
		// 结论落终态：释放升级考核去重键（清空为 NULL），否则该陪玩将永久无法再申请。
		ClearActiveDedupKey().
		AddVersion(1).Save(ctx); err != nil {
		return nil, err
	}
	if err = applyAssessmentOutcome(ctx, tx, assessment, passed, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAssessment.Get(ctx, id)
}

// applyAssessmentOutcome 按考核结论更新陪玩档案：入职/升级/复考通过则定级并开通接单权限；
// 抽查通过仅刷新考核时间，抽查不通过则降一档并暂停接单权限。
// ponytail: 等级→分成率的自动换算未做，具体分成率仍按既有「按陪玩」分成规则维护。
func applyAssessmentOutcome(ctx context.Context, tx *ent.Tx, assessment *ent.EsportsAssessment, passed bool, now time.Time) error {
	player, err := tx.EsportsPlayer.Get(ctx, assessment.PlayerID)
	if err != nil {
		return err
	}
	update := tx.EsportsPlayer.Update().Where(esportsplayer.IDEQ(player.ID)).AddVersion(1)
	levels := skillLevels(ctx, tx.Client())
	switch assessment.AssessmentType {
	case esportsassessment.AssessmentTypeEntry,
		esportsassessment.AssessmentTypeUpgrade,
		esportsassessment.AssessmentTypeRetry:
		if !passed {
			return nil
		}
		// 与 updatePlayer 手工改档同一道门槛：考核通过也不能把未通过实名认证的陪玩设为「合格」。
		// 返回错误即由调用方事务回滚，评分明细与考核状态都不会落地。
		if err := ensurePlayerQualificationAllowed(ctx, tx.Client(), player.ID, esportsplayer.QualificationQualified); err != nil {
			if errors.Is(err, errInvalidInput) {
				return fmt.Errorf("%w：考核通过但陪玩实名认证未通过，不得设为合格", errInvalidInput)
			}
			return err
		}
		level := assessment.TargetLevel
		if !validPlayerLevel(levels, level) {
			level = skillLevelLowest(levels)
		}
		game := strings.TrimSpace(assessment.Game)
		gameLevels := cloneGameLevels(player.GameLevels)
		// 分区等级是接单权威：同一分区的升级必须单调，旧单（尤其是申诉改判）不得把已升上去的分区等级改回旧档。
		// 复核通过的升级单仍要把资格置为「合格」并刷新考核时间。
		if game != "" {
			current := gameLevels[game]
			if assessment.AssessmentType != esportsassessment.AssessmentTypeUpgrade ||
				current == "" ||
				skillLevelRankOrBelow(levels, level) > skillLevelRankOrBelow(levels, current) {
				gameLevels[game] = level
			}
			// 开通该分区：与已有分区取并集，不覆盖陪玩自己关掉的分区。
			if !slices.Contains(player.Games, game) {
				update.SetGames(append(slices.Clone(player.Games), game))
			}
		}
		update.SetGameLevels(gameLevels)
		// level 只是展示用的最高档派生值，不参与接单校验。
		update.SetLevel(highestPlayerGameLevel(levels, gameLevels, player.Level))
		update.SetQualification(esportsplayer.QualificationQualified).
			SetLevelAppraisedAt(now.UTC())
	case esportsassessment.AssessmentTypeSpotCheck:
		if passed {
			update.SetLevelAppraisedAt(now.UTC())
		} else {
			// 抽查不通过按分区降级：只降该次抽查绑定的分区，其余分区不受影响。
			if game := strings.TrimSpace(assessment.Game); game != "" {
				gameLevels := cloneGameLevels(player.GameLevels)
				gameLevels[game] = demoteSkillLevel(levels, playerGameLevel(player, game))
				update.SetGameLevels(gameLevels)
				update.SetLevel(highestPlayerGameLevel(levels, gameLevels, player.Level))
			} else {
				update.SetLevel(demoteSkillLevel(levels, player.Level))
			}
			update.SetQualification(esportsplayer.QualificationSuspended).
				SetLevelAppraisedAt(now.UTC())
		}
	}
	_, err = update.Save(ctx)
	return err
}

// revertAssessmentOutcome 申诉改判「通过 → 不通过」时回滚该次考核带来的业务影响（P0-15 / ）：
//   - 撤销该次考核写入的等级提升与「合格」标记，并把依赖该结论的接单权限置为不可用
//     （qualification → suspended，派单侧只认 qualified，见 dispatch.go 的资格校验）；
//   - 等级只在「当前等级正是本次考核写入的目标档」时回退一档：后续更高档考核的成果不得被旧单改判抹掉
//     （与 applyAssessmentOutcome 的升级单调口径对称）；
//   - 抽查复核的通过结论本就不写等级，改判为不通过按抽查不达标的既有口径降一档。
//
// 只复用陪玩档案上既有的 level / qualification 写入口，不新增字段；「暂停接单」在该档案上就是
// qualification=suspended，其它暂停来源（如信用分、presence）不在本文件可达范围内。
func revertAssessmentOutcome(ctx context.Context, tx *ent.Tx, assessment *ent.EsportsAssessment, now time.Time) error {
	player, err := tx.EsportsPlayer.Get(ctx, assessment.PlayerID)
	if err != nil {
		return err
	}
	levels := skillLevels(ctx, tx.Client())
	// 结论不通过即不得保留接单权限：先无条件收紧为 suspended，再按考核类型回退等级。
	update := tx.EsportsPlayer.Update().Where(esportsplayer.IDEQ(player.ID)).AddVersion(1).
		SetQualification(esportsplayer.QualificationSuspended)
	switch assessment.AssessmentType {
	case esportsassessment.AssessmentTypeEntry,
		esportsassessment.AssessmentTypeUpgrade,
		esportsassessment.AssessmentTypeRetry:
		target := assessment.TargetLevel
		if !validPlayerLevel(levels, target) {
			// 入职缺目标等级时 applyAssessmentOutcome 写的是最低档，改判同样按最低档口径回退。
			target = skillLevelLowest(levels)
		}
		if strings.TrimSpace(assessment.Game) == "" && player.Level == target {
			update.SetLevel(demoteSkillLevel(levels, player.Level))
		}
	case esportsassessment.AssessmentTypeSpotCheck:
		if game := strings.TrimSpace(assessment.Game); game != "" {
			gameLevels := cloneGameLevels(player.GameLevels)
			gameLevels[game] = demoteSkillLevel(levels, playerGameLevel(player, game))
			update.SetGameLevels(gameLevels)
			update.SetLevel(highestPlayerGameLevel(levels, gameLevels, player.Level))
		} else {
			update.SetLevel(demoteSkillLevel(levels, player.Level))
		}
	}
	// 改判不通过要收回该次考核开通的分区与分区等级；同分区若仍有其它已通过考核，
	// 分区保留、等级回落到那些考核的最高目标档。
	if game := strings.TrimSpace(assessment.Game); game != "" {
		rows, queryErr := tx.EsportsAssessment.Query().Where(
			esportsassessment.PlayerIDEQ(player.ID),
			esportsassessment.IDNEQ(assessment.ID),
			esportsassessment.StatusEQ(esportsassessment.StatusPassed),
			esportsassessment.GameEQ(game),
		).All(ctx)
		if queryErr != nil {
			return queryErr
		}
		gameLevels := cloneGameLevels(player.GameLevels)
		if len(rows) == 0 {
			update.SetGames(slices.DeleteFunc(player.Games, func(g string) bool { return g == game }))
			delete(gameLevels, game)
		} else {
			best := ""
			for _, row := range rows {
				if validPlayerLevel(levels, row.TargetLevel) &&
					(best == "" || skillLevelRankOrBelow(levels, row.TargetLevel) > skillLevelRankOrBelow(levels, best)) {
					best = row.TargetLevel
				}
			}
			if best != "" {
				gameLevels[game] = best
			}
		}
		update.SetGameLevels(gameLevels)
		update.SetLevel(highestPlayerGameLevel(levels, gameLevels, player.Level))
	}
	_, err = update.Save(ctx)
	return err
}

// restoreAssessmentOutcome 申诉改判「不通过 → 通过」时补记业务影响（P0-15 / ）：
// 入职 / 升级 / 复考与正常通过同一口径（定级 + 合格 + 实名门槛），抽查复核则解除该次不通过造成的暂停。
// 抽查复核的通过结论不写等级，改判通过也不擅自上抬等级——降级可能另有原因，不能借一次改判抹掉。
func restoreAssessmentOutcome(ctx context.Context, tx *ent.Tx, assessment *ent.EsportsAssessment, now time.Time) error {
	if assessment.AssessmentType != esportsassessment.AssessmentTypeSpotCheck {
		return applyAssessmentOutcome(ctx, tx, assessment, true, now)
	}
	player, err := tx.EsportsPlayer.Get(ctx, assessment.PlayerID)
	if err != nil {
		return err
	}
	update := tx.EsportsPlayer.Update().Where(esportsplayer.IDEQ(player.ID)).AddVersion(1).
		SetLevelAppraisedAt(now.UTC())
	if player.Qualification == esportsplayer.QualificationSuspended {
		// 与正常通过同一道实名门槛：未实名不得设为合格。
		if err = ensurePlayerQualificationAllowed(ctx, tx.Client(), player.ID, esportsplayer.QualificationQualified); err != nil {
			if errors.Is(err, errInvalidInput) {
				return fmt.Errorf("%w：考核改判通过但陪玩实名认证未通过，不得设为合格", errInvalidInput)
			}
			return err
		}
		update.SetQualification(esportsplayer.QualificationQualified)
	}
	_, err = update.Save(ctx)
	return err
}

// ── 申诉 ──

// createAppeal 陪玩对已出结论的考核发起申诉，每个任务仅一次。
func createAppeal(ctx context.Context, db *ent.Client, playerID, assessmentID int, reason string, now time.Time) (*ent.EsportsAssessmentAppeal, error) {
	reason = strings.TrimSpace(reason)
	if playerID < 1 || assessmentID < 1 || reason == "" || len([]rune(reason)) > 500 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	assessment, err := tx.EsportsAssessment.Get(ctx, assessmentID)
	if err != nil {
		return nil, err
	}
	if assessment.PlayerID != playerID {
		return nil, errForbidden
	}
	if assessment.Status != esportsassessment.StatusPassed && assessment.Status != esportsassessment.StatusFailed {
		return nil, errConflict
	}
	if assessment.AppealStatus != esportsassessment.AppealStatusNone {
		return nil, errConflict
	}
	appeal, err := tx.EsportsAssessmentAppeal.Create().SetAssessmentID(assessmentID).
		SetPlayerID(playerID).SetReason(reason).SetStatus(esportsassessmentappeal.StatusPending).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	update := tx.EsportsAssessment.Update().Where(
		esportsassessment.IDEQ(assessmentID),
		esportsassessment.AppealStatusEQ(esportsassessment.AppealStatusNone),
	)
	// 升级考核：申诉待复核期间重新占住去重键，与「进行中」同口径拦截新申请——
	// 否则新单把等级升上去后旧申诉改判会回写更低档位。若该陪玩已有其他进行中升级单（键已被占用），
	// 不重复占键，避免唯一索引误报冲突。
	if assessment.AssessmentType == esportsassessment.AssessmentTypeUpgrade {
		other, findErr := findInProgressUpgradeAssessment(ctx, tx.Client(), playerID, assessmentID)
		if findErr != nil {
			return nil, findErr
		}
		if other == nil {
			update.SetActiveDedupKey(upgradeDedupKey(playerID))
		}
	}
	if _, err = update.SetAppealStatus(esportsassessment.AppealStatusPending).AddVersion(1).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAssessmentAppeal.Get(ctx, appeal.ID)
}

// reviewAppeal 管理员复核申诉：成立则改判结论并重放定级动作，驳回则维持原结论。
func reviewAppeal(ctx context.Context, db *ent.Client, assessmentID, adminID int, approve bool, note string, now time.Time) (*ent.EsportsAssessment, error) {
	note = strings.TrimSpace(note)
	if assessmentID < 1 || len([]rune(note)) > 500 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	assessment, err := tx.EsportsAssessment.Get(ctx, assessmentID)
	if err != nil {
		return nil, err
	}
	if assessment.AppealStatus != esportsassessment.AppealStatusPending {
		return nil, errConflict
	}
	appealStatus := esportsassessment.AppealStatusRejected
	newStatus := assessment.Status
	if approve {
		appealStatus = esportsassessment.AppealStatusApproved
		if assessment.Status == esportsassessment.StatusPassed {
			newStatus = esportsassessment.StatusFailed
		} else {
			newStatus = esportsassessment.StatusPassed
		}
	}
	if _, err = tx.EsportsAssessmentAppeal.Update().Where(
		esportsassessmentappeal.AssessmentIDEQ(assessmentID),
		esportsassessmentappeal.StatusEQ(esportsassessmentappeal.StatusPending),
	).SetStatus(esportsassessmentappeal.Status(appealStatus.String())).
		SetReviewerAdminID(adminID).SetReviewNote(note).SetReviewedAt(now.UTC()).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsAssessment.Update().Where(esportsassessment.IDEQ(assessmentID)).
		SetAppealStatus(appealStatus).SetStatus(newStatus).
		// 申诉复核落终态：释放升级考核去重键（清空为 NULL），否则该陪玩将永久无法再申请。
		ClearActiveDedupKey().
		AddVersion(1).Save(ctx); err != nil {
		return nil, err
	}
	if approve {
		// 改判必须双向回滚业务影响（P0-15）：
		// 通过 → 不通过撤销定级与合格标记并把接单权限置为不可用；不通过 → 通过补记定级并解除暂停。
		if newStatus == esportsassessment.StatusPassed {
			err = restoreAssessmentOutcome(ctx, tx, assessment, now)
		} else {
			err = revertAssessmentOutcome(ctx, tx, assessment, now)
		}
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAssessment.Get(ctx, assessmentID)
}

// ── 客户评价与综合评分 ──

// upsertReview 写入或更新一条评价，同时追加修订历史；同「订单+客户+陪玩」仅保留最新版本。
// 已核验（客户本人提交、计入综合评分）的评价不得被客服代录覆盖（P0-16）：代录只是补充线索，
// 若允许它把 verified 改回 false，等于用未核验内容改写客户真实评价并把它从综合评分里剔除。
// 需要更正已核验评价时只能走评价申诉 / 撤回流程。未核验记录的代录更新照旧保留。
func upsertReview(ctx context.Context, tx *ent.Tx, orderID, customerID, playerID, rating int, comment string, verified bool) (*ent.EsportsPlayerReview, error) {
	existing, err := tx.EsportsPlayerReview.Query().Where(
		esportsplayerreview.OrderIDEQ(orderID),
		esportsplayerreview.CustomerIDEQ(customerID),
		esportsplayerreview.PlayerIDEQ(playerID),
	).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if err == nil && existing.Verified && !verified {
		return nil, fmt.Errorf("%w：该评价已由客户本人核验提交，客服代录不得覆盖；如需更正请走评价申诉或撤回流程", errConflict)
	}
	var review *ent.EsportsPlayerReview
	if ent.IsNotFound(err) {
		review, err = tx.EsportsPlayerReview.Create().SetOrderID(orderID).SetCustomerID(customerID).
			SetPlayerID(playerID).SetRating(rating).SetComment(comment).SetVerified(verified).
			SetStatus(esportsplayerreview.StatusNormal).SetVersion(1).Save(ctx)
		if err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
	} else {
		review, err = tx.EsportsPlayerReview.UpdateOneID(existing.ID).
			SetRating(rating).SetComment(comment).SetVerified(verified).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
	}
	if _, err = tx.EsportsPlayerReviewRevision.Create().SetReviewID(review.ID).
		SetVersion(review.Version).SetRating(rating).SetComment(comment).Save(ctx); err != nil {
		return nil, err
	}
	return review, nil
}

// submitCustomerReview 已核验身份客户在已完成订单上对实际服务成员逐人评价。
func submitCustomerReview(ctx context.Context, db *ent.Client, customerID, orderID, playerID, rating int, comment string) (*ent.EsportsPlayerReview, error) {
	comment = strings.TrimSpace(comment)
	if customerID < 1 || orderID < 1 || playerID < 1 || rating < 1 || rating > 5 || len([]rune(comment)) > 500 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.CustomerID != customerID {
		return nil, errForbidden
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted {
		return nil, errConflict
	}
	members, err := loadOrderMembers(ctx, tx.EsportsServiceSegment, tx.EsportsDispatchSeat, orderID)
	if err != nil {
		return nil, err
	}
	found := false
	for _, member := range members {
		if member.PlayerID == playerID {
			found = true
			break
		}
	}
	if !found {
		return nil, errForbidden
	}
	review, err := upsertReview(ctx, tx, orderID, customerID, playerID, rating, comment, true)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsPlayerReview.Get(ctx, review.ID)
}

type reviewInput struct {
	// OrderNo 管理端代录评价按订单号定位订单。
	OrderNo    string `json:"order_no"`
	CustomerID int    `json:"customer_id"`
	PlayerID   int    `json:"player_id"`
	Rating     int    `json:"rating"`
	Comment    string `json:"comment"`
}

// recordReviewByAdmin 客服代录反馈：标识为未验证，不直接计入陪玩综合评分。
func recordReviewByAdmin(ctx context.Context, db *ent.Client, in reviewInput) (*ent.EsportsPlayerReview, error) {
	in.Comment = strings.TrimSpace(in.Comment)
	orderID, err := orderIDByNo(ctx, db, in.OrderNo)
	if err != nil {
		return nil, err
	}
	if orderID < 1 || in.CustomerID < 1 || in.PlayerID < 1 ||
		in.Rating < 1 || in.Rating > 5 || len([]rune(in.Comment)) > 500 {
		return nil, errInvalidInput
	}
	if _, err := db.EsportsPlayer.Get(ctx, in.PlayerID); err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	review, err := upsertReview(ctx, tx, orderID, in.CustomerID, in.PlayerID, in.Rating, in.Comment, false)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsPlayerReview.Get(ctx, review.ID)
}

// setReviewStatus 管理员复核异常刷评 / 评价申诉：normal / disputed / invalid。
func setReviewStatus(ctx context.Context, db *ent.Client, id int, status string) (*ent.EsportsPlayerReview, error) {
	status = strings.TrimSpace(status)
	if id < 1 || (status != "normal" && status != "disputed" && status != "invalid") {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsPlayerReview.Update().Where(esportsplayerreview.IDEQ(id)).
		SetStatus(esportsplayerreview.Status(status)).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, &ent.NotFoundError{}
	}
	return db.EsportsPlayerReview.Get(ctx, id)
}

type playerRatingView struct {
	PlayerID     int     `json:"player_id"`
	Average      float64 `json:"average"`
	SampleCount  int     `json:"sample_count"`
	Insufficient bool    `json:"insufficient"`
	RuleVersion  string  `json:"rule_version"`
	// 计算周期：与样本数 / 规则版本一并返回，供事后复核「这个分数是哪个窗口算出来的」。
	// PeriodScope 为口径名：sample_window=按纳入样本的时间跨度；empty=无样本，以计算时刻为起止。
	PeriodScope string `json:"period_scope"`
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
	PeriodDays  int    `json:"period_days"`
	ComputedAt  string `json:"computed_at"`
}

// playerRatingSummary 陪玩综合评分：仅纳入已核验身份、状态正常的评价；样本不足时不当作低分。
// 响应同时给出计算周期（口径 + 起止时间 + 跨度天数 + 计算时刻，），字段恒非空。
func playerRatingSummary(ctx context.Context, db *ent.Client, playerID int) (*playerRatingView, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	if _, err := db.EsportsPlayer.Get(ctx, playerID); err != nil {
		return nil, err
	}
	rows, err := db.EsportsPlayerReview.Query().Where(
		esportsplayerreview.PlayerIDEQ(playerID),
		esportsplayerreview.VerifiedEQ(true),
		esportsplayerreview.StatusEQ(esportsplayerreview.StatusNormal),
	).Select(esportsplayerreview.FieldRating, esportsplayerreview.FieldCreatedAt).All(ctx)
	if err != nil {
		return nil, err
	}
	count := len(rows)
	var sum int
	for _, row := range rows {
		sum += row.Rating
	}
	average := 0.0
	if count > 0 {
		average = float64(sum) / float64(count)
	}
	minSamples := assessmentConfigInt(ctx, db, reviewMinSamplesKey, defaultReviewMinSamples)
	scope, start, end, days := ratingPeriod(rows, time.Now().UTC())
	return &playerRatingView{
		PlayerID:     playerID,
		Average:      average,
		SampleCount:  count,
		Insufficient: count < minSamples,
		RuleVersion:  assessmentConfigString(ctx, db, reviewRuleVersionKey, defaultReviewRuleVersion),
		PeriodScope:  scope,
		PeriodStart:  start.Format(time.RFC3339),
		PeriodEnd:    end.Format(time.RFC3339),
		PeriodDays:   days,
		ComputedAt:   time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// ratingPeriod 推导评分计算周期：有样本时取纳入样本 createdAt 的最小/最大值（向上取整天数），
// 无样本时以计算时刻为起止、口径标记 empty，保证响应中的周期字段恒非空。
func ratingPeriod(rows []*ent.EsportsPlayerReview, now time.Time) (string, time.Time, time.Time, int) {
	if len(rows) == 0 {
		return "empty", now, now, 0
	}
	start, end := rows[0].CreatedAt.UTC(), rows[0].CreatedAt.UTC()
	for _, row := range rows {
		at := row.CreatedAt.UTC()
		if at.Before(start) {
			start = at
		}
		if at.After(end) {
			end = at
		}
	}
	days := int((end.Sub(start) + 24*time.Hour - time.Nanosecond) / (24 * time.Hour))
	return "sample_window", start, end, days
}

// ── 查询 ──

// listAssessments 列出考核任务，可按陪玩 / 状态 / 类型过滤。
func listAssessments(ctx context.Context, db *ent.Client, playerID int, status, assessmentType string, limit int) ([]*ent.EsportsAssessment, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsAssessment.Query()
	if playerID > 0 {
		q.Where(esportsassessment.PlayerIDEQ(playerID))
	}
	if status = strings.TrimSpace(status); status != "" {
		q.Where(esportsassessment.StatusEQ(esportsassessment.Status(status)))
	}
	if assessmentType = strings.TrimSpace(assessmentType); assessmentType != "" {
		q.Where(esportsassessment.AssessmentTypeEQ(esportsassessment.AssessmentType(assessmentType)))
	}
	return q.Order(ent.Desc(esportsassessment.FieldID)).Limit(limit).All(ctx)
}

type assessmentDetailView struct {
	Assessment *ent.EsportsAssessment        `json:"assessment"`
	Scores     []*ent.EsportsAssessmentScore `json:"scores"`
	Appeal     *ent.EsportsAssessmentAppeal  `json:"appeal"`
}

// assessmentDetail 返回考核任务 + 逐维度打分 + 申诉记录，构成完整考核记录。
func assessmentDetail(ctx context.Context, db *ent.Client, id int) (*assessmentDetailView, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	assessment, err := db.EsportsAssessment.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	scores, err := db.EsportsAssessmentScore.Query().Where(
		esportsassessmentscore.AssessmentIDEQ(id),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	appeal, err := db.EsportsAssessmentAppeal.Query().Where(
		esportsassessmentappeal.AssessmentIDEQ(id),
	).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if ent.IsNotFound(err) {
		appeal = nil
	}
	return &assessmentDetailView{Assessment: assessment, Scores: scores, Appeal: appeal}, nil
}

// ── 客户自助：我的评价列表 ──

// 评价申诉状态枚举（与客户端契约一致）：none 未申诉 / pending 待复核 / approved 申诉成立 / rejected 申诉驳回。
const (
	userReviewAppealNone     = "none"
	userReviewAppealPending  = "pending"
	userReviewAppealApproved = "approved"
	userReviewAppealRejected = "rejected"
)

// userReviewView 是客户本人评价列表的单条视图。
// 关键补口：申诉接口需要评价 ID，而客户端此前拿不到自己的评价列表，申诉 UI 只能靠客服转述；
// 这里一并给出 appeal_status 与 can_appeal，客户端可自行判断「能不能申诉、申诉走到哪一步」。
type userReviewView struct {
	ReviewID int    `json:"review_id"`
	OrderID  int    `json:"order_id"`
	OrderNo  string `json:"order_no"`
	PlayerID int    `json:"player_id"`
	Rating   int    `json:"rating"`
	Content  string `json:"content"`
	Verified bool   `json:"verified"`
	// Status 是评价的风控状态（normal / disputed / invalid / appeal_pending）：
	// 说明「这条评价是否被判定异常」，与 appeal_status（申诉走到哪一步）是两个维度。
	Status string `json:"status"`
	// AppealStatus 取值 none / pending / approved / rejected。
	AppealStatus string    `json:"appeal_status"`
	AppealID     *int      `json:"appeal_id"`
	CanAppeal    bool      `json:"can_appeal"`
	CreatedAt    time.Time `json:"created_at"`
}

// userReviewAppealStatusOf 由申诉流水推导对外申诉状态；无申诉记录时为 none。
// 评价处于 appeal_pending 但申诉流水缺失（历史脏数据）时按 pending 处理，避免客户端误判为「可再次申诉」。
func userReviewAppealStatusOf(review *ent.EsportsPlayerReview, appeal *ent.EsportsPlayerReviewAppeal) string {
	if appeal != nil {
		switch appeal.Status {
		case esportsplayerreviewappeal.StatusPending:
			return userReviewAppealPending
		case esportsplayerreviewappeal.StatusApproved:
			return userReviewAppealApproved
		case esportsplayerreviewappeal.StatusRejected:
			return userReviewAppealRejected
		}
	}
	if review.Status == esportsplayerreview.StatusAppealPending {
		return userReviewAppealPending
	}
	return userReviewAppealNone
}

// listCustomerReviews 返回当前客户本人的评价列表（倒序），可按本人订单过滤。
// 数据边界：调用方必须传入登录身份解析出的客户 ID；本函数不接受、也不信任客户端传入的客户 ID。
func listCustomerReviews(ctx context.Context, db *ent.Client, customerID, orderID, limit int, now time.Time) ([]userReviewView, error) {
	if customerID < 1 {
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	if orderID > 0 {
		order, err := db.EsportsOrder.Query().Where(
			esportsorder.IDEQ(orderID), esportsorder.CustomerIDEQ(customerID),
		).Only(ctx)
		if ent.IsNotFound(err) {
			return nil, errForbidden
		}
		if err != nil {
			return nil, err
		}
		orderID = order.ID
	}
	q := db.EsportsPlayerReview.Query().Where(esportsplayerreview.CustomerIDEQ(customerID))
	if orderID > 0 {
		q.Where(esportsplayerreview.OrderIDEQ(orderID))
	}
	rows, err := q.Order(ent.Desc(esportsplayerreview.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]userReviewView, 0, len(rows))
	if len(rows) == 0 {
		return views, nil
	}
	orderIDs := make([]int, 0, len(rows))
	reviewIDs := make([]int, 0, len(rows))
	seenOrder := map[int]bool{}
	for _, row := range rows {
		reviewIDs = append(reviewIDs, row.ID)
		if !seenOrder[row.OrderID] {
			seenOrder[row.OrderID] = true
			orderIDs = append(orderIDs, row.OrderID)
		}
	}
	orderNos := map[int]string{}
	if len(orderIDs) > 0 {
		orders, err := db.EsportsOrder.Query().Where(esportsorder.IDIn(orderIDs...)).
			Select(esportsorder.FieldID, esportsorder.FieldOrderNo).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, order := range orders {
			orderNos[order.ID] = order.OrderNo
		}
	}
	appeals := map[int]*ent.EsportsPlayerReviewAppeal{}
	items, err := db.EsportsPlayerReviewAppeal.Query().Where(
		esportsplayerreviewappeal.ReviewIDIn(reviewIDs...),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		appeals[item.ReviewID] = item
	}
	windowDays := reviewAppealWindowDays(ctx, db)
	for _, row := range rows {
		appeal := appeals[row.ID]
		view := userReviewView{
			ReviewID: row.ID, OrderID: row.OrderID, OrderNo: orderNos[row.OrderID],
			PlayerID: row.PlayerID, Rating: row.Rating, Content: row.Comment,
			Verified: row.Verified, Status: row.Status.String(),
			AppealStatus: userReviewAppealStatusOf(row, appeal),
			CreatedAt:    row.CreatedAt.UTC(),
		}
		if appeal != nil {
			appealID := appeal.ID
			view.AppealID = &appealID
		}
		// 可申诉口径与 createReviewAppeal 的拦截条件保持一致：已终局 / 申诉中 / 已有申诉 / 超过时限均不可再申诉。
		view.CanAppeal = appeal == nil &&
			row.Status != esportsplayerreview.StatusInvalid &&
			row.Status != esportsplayerreview.StatusAppealPending &&
			(windowDays <= 0 || !now.UTC().After(row.CreatedAt.UTC().AddDate(0, 0, windowDays)))
		views = append(views, view)
	}
	return views, nil
}

// listReviews 列出评价，可按陪玩 / 订单 / 状态过滤。
func listReviews(ctx context.Context, db *ent.Client, playerID, orderID int, status string, limit int) ([]*ent.EsportsPlayerReview, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsPlayerReview.Query()
	if playerID > 0 {
		q.Where(esportsplayerreview.PlayerIDEQ(playerID))
	}
	if orderID > 0 {
		q.Where(esportsplayerreview.OrderIDEQ(orderID))
	}
	if status = strings.TrimSpace(status); status != "" {
		q.Where(esportsplayerreview.StatusEQ(esportsplayerreview.Status(status)))
	}
	return q.Order(ent.Desc(esportsplayerreview.FieldID)).Limit(limit).All(ctx)
}

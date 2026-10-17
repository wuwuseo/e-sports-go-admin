package esports

import (
	"context"
	"errors"
	"fmt"
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

// 考核维度固定六项（§7.2），与 schema 枚举保持一致。
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

func validPlayerLevel(l string) bool {
	switch l {
	case "novice", "skilled", "senior", "mentor":
		return true
	}
	return false
}

// demoteLevel 抽查不达标时降一档；已是最低档保持 novice（§7.3 长期不达标降级）。
func demoteLevel(current esportsplayer.Level) esportsplayer.Level {
	switch current {
	case esportsplayer.LevelMentor:
		return esportsplayer.LevelSenior
	case esportsplayer.LevelSenior:
		return esportsplayer.LevelSkilled
	default:
		return esportsplayer.LevelNovice
	}
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

// ── 权重配置（§7.2） ──

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

// ── 考核任务（§7.1/§7.2） ──

type assessmentInput struct {
	PlayerID        int        `json:"player_id"`
	AssessmentType  string     `json:"assessment_type"`
	TargetLevel     string     `json:"target_level"`
	Note            string     `json:"note"`
	Source          string     `json:"source"`
	ExaminerAdminID *int       `json:"examiner_admin_id"`
	ScheduledAt     *time.Time `json:"scheduled_at"`
}

// createAssessment 新建考核任务：入职默认目标 novice，状态待分配考官。
func createAssessment(ctx context.Context, db *ent.Client, adminID int, in assessmentInput, now time.Time) (*ent.EsportsAssessment, error) {
	in.AssessmentType = strings.TrimSpace(in.AssessmentType)
	in.TargetLevel = strings.TrimSpace(in.TargetLevel)
	in.Note = strings.TrimSpace(in.Note)
	in.Source = strings.TrimSpace(in.Source)
	if in.Source == "" {
		in.Source = "apply"
	}
	if !validAssessmentType(in.AssessmentType) || (in.Source != "apply" && in.Source != "system") ||
		len([]rune(in.Note)) > 500 {
		return nil, errInvalidInput
	}
	// 入职默认定级 novice；其余类型目标等级必填且合法。
	if in.AssessmentType == "entry" && in.TargetLevel == "" {
		in.TargetLevel = "novice"
	}
	if in.TargetLevel != "" && !validPlayerLevel(in.TargetLevel) {
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
		SetTargetLevel(in.TargetLevel).SetStatus(esportsassessment.StatusPending).
		SetSource(esportsassessment.Source(in.Source)).SetNote(in.Note).
		SetCreatedByAdminID(adminID)
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
	total := 0
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
		total += item.Score * weight / 10000
		if _, err = tx.EsportsAssessmentScore.Create().SetAssessmentID(id).
			SetDimension(esportsassessmentscore.Dimension(item.Dimension)).
			SetWeightBp(weight).SetScore(item.Score).SetComment(item.Comment).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
	}
	// 须覆盖全部配置维度（等权回落时即六项）。
	for dim, weight := range weights {
		if weight > 0 && !seen[dim] {
			return nil, errInvalidInput
		}
	}
	passScore := assessmentConfigInt(ctx, tx.Client(), assessmentPassScoreKey, defaultAssessmentPassScore)
	passed := total >= passScore
	status := esportsassessment.StatusFailed
	if passed {
		status = esportsassessment.StatusPassed
	}
	if _, err = tx.EsportsAssessment.Update().Where(esportsassessment.IDEQ(id)).
		SetStatus(status).SetTotalScore(total).SetNote(note).SetScoredAt(now.UTC()).
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
// 抽查通过仅刷新考核时间，抽查不通过则降一档并暂停接单权限（§7.2/§7.3/§7.4）。
// ponytail: 等级→分成率的自动换算未做，具体分成率仍按既有「按陪玩」分成规则维护。
func applyAssessmentOutcome(ctx context.Context, tx *ent.Tx, assessment *ent.EsportsAssessment, passed bool, now time.Time) error {
	player, err := tx.EsportsPlayer.Get(ctx, assessment.PlayerID)
	if err != nil {
		return err
	}
	update := tx.EsportsPlayer.Update().Where(esportsplayer.IDEQ(player.ID)).AddVersion(1)
	switch assessment.AssessmentType {
	case esportsassessment.AssessmentTypeEntry,
		esportsassessment.AssessmentTypeUpgrade,
		esportsassessment.AssessmentTypeRetry:
		if !passed {
			return nil
		}
		// 与 updatePlayer 手工改档同一道门槛（§6.1）：考核通过也不能把未通过实名认证的陪玩设为「合格」。
		// 返回错误即由调用方事务回滚，评分明细与考核状态都不会落地。
		if err := ensurePlayerQualificationAllowed(ctx, tx.Client(), player.ID, esportsplayer.QualificationQualified); err != nil {
			if errors.Is(err, errInvalidInput) {
				return fmt.Errorf("%w：考核通过但陪玩实名认证未通过，不得设为合格", errInvalidInput)
			}
			return err
		}
		level := assessment.TargetLevel
		if !validPlayerLevel(level) {
			level = string(esportsplayer.LevelNovice)
		}
		update.SetLevel(esportsplayer.Level(level)).
			SetQualification(esportsplayer.QualificationQualified).
			SetLevelAppraisedAt(now.UTC())
	case esportsassessment.AssessmentTypeSpotCheck:
		if passed {
			update.SetLevelAppraisedAt(now.UTC())
		} else {
			update.SetLevel(demoteLevel(player.Level)).
				SetQualification(esportsplayer.QualificationSuspended).
				SetLevelAppraisedAt(now.UTC())
		}
	}
	_, err = update.Save(ctx)
	return err
}

// ── 申诉（§7.4） ──

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
	if _, err = tx.EsportsAssessment.Update().Where(
		esportsassessment.IDEQ(assessmentID),
		esportsassessment.AppealStatusEQ(esportsassessment.AppealStatusNone),
	).SetAppealStatus(esportsassessment.AppealStatusPending).AddVersion(1).Save(ctx); err != nil {
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
		SetAppealStatus(appealStatus).SetStatus(newStatus).AddVersion(1).Save(ctx); err != nil {
		return nil, err
	}
	if approve {
		if err = applyAssessmentOutcome(ctx, tx, assessment, newStatus == esportsassessment.StatusPassed, now); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAssessment.Get(ctx, assessmentID)
}

// ── 客户评价与综合评分（§7.5） ──

// upsertReview 写入或更新一条评价，同时追加修订历史；同「订单+客户+陪玩」仅保留最新版本。
func upsertReview(ctx context.Context, tx *ent.Tx, orderID, customerID, playerID, rating int, comment string, verified bool) (*ent.EsportsPlayerReview, error) {
	existing, err := tx.EsportsPlayerReview.Query().Where(
		esportsplayerreview.OrderIDEQ(orderID),
		esportsplayerreview.CustomerIDEQ(customerID),
		esportsplayerreview.PlayerIDEQ(playerID),
	).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
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
	OrderID    int    `json:"order_id"`
	CustomerID int    `json:"customer_id"`
	PlayerID   int    `json:"player_id"`
	Rating     int    `json:"rating"`
	Comment    string `json:"comment"`
}

// recordReviewByAdmin 客服代录反馈：标识为未验证，不直接计入陪玩综合评分。
func recordReviewByAdmin(ctx context.Context, db *ent.Client, in reviewInput) (*ent.EsportsPlayerReview, error) {
	in.Comment = strings.TrimSpace(in.Comment)
	if in.OrderID < 1 || in.CustomerID < 1 || in.PlayerID < 1 ||
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
	review, err := upsertReview(ctx, tx, in.OrderID, in.CustomerID, in.PlayerID, in.Rating, in.Comment, false)
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
}

// playerRatingSummary 陪玩综合评分：仅纳入已核验身份、状态正常的评价；样本不足时不当作低分。
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
	).Select(esportsplayerreview.FieldRating).All(ctx)
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
	return &playerRatingView{
		PlayerID:     playerID,
		Average:      average,
		SampleCount:  count,
		Insufficient: count < minSamples,
		RuleVersion:  assessmentConfigString(ctx, db, reviewRuleVersionKey, defaultReviewRuleVersion),
	}, nil
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

// assessmentDetail 返回考核任务 + 逐维度打分 + 申诉记录，构成完整考核记录（§7.4）。
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

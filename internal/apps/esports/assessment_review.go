package esports

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsassessmentscore"
	"go-admin/internal/ent/esportsplayerreview"
	"go-admin/internal/ent/esportsplayerreviewappeal"
	"go-admin/internal/global"
)

// 客户评价（§7.5）第 4 轮补齐：陪玩端考核明细（§6.1-13）、评分计算周期（§7.5-04）、
// 异常刷评自动识别 + 评价申诉流水（§7.5-05）。

// ── §6.1-13 陪玩端考核明细（逐维度得分与权重 = 评级依据） ──

// playerAssessmentView 是陪玩端「我的考核」单条视图：主表 + 逐维度得分（含权重）。
// 权重是评级依据的一部分（同一分数在不同权重下的加权结果不同），必须一并下发。
type playerAssessmentView struct {
	Assessment     *ent.EsportsAssessment        `json:"assessment"`
	Scores         []*ent.EsportsAssessmentScore `json:"scores"`
	WeightTotalBP  int                           `json:"weight_total_bp"`
	DimensionCount int                           `json:"dimension_count"`
}

// listPlayerAssessmentDetails 返回陪玩本人的考核明细（主表 + 逐维度得分 + 权重合计）。
// 逐维度明细按固定六维度顺序输出，避免数据库返回顺序抖动导致前端展示顺序不稳定。
func listPlayerAssessmentDetails(ctx context.Context, db *ent.Client, playerID, limit int) ([]playerAssessmentView, error) {
	items, err := listAssessments(ctx, db, playerID, "", "", limit)
	if err != nil {
		return nil, err
	}
	views := make([]playerAssessmentView, 0, len(items))
	if len(items) == 0 {
		return views, nil
	}
	ids := make([]int, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	rows, err := db.EsportsAssessmentScore.Query().Where(
		esportsassessmentscore.AssessmentIDIn(ids...),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	grouped := make(map[int][]*ent.EsportsAssessmentScore, len(ids))
	for _, row := range rows {
		grouped[row.AssessmentID] = append(grouped[row.AssessmentID], row)
	}
	order := make(map[string]int, len(assessmentDimensions))
	for i, dim := range assessmentDimensions {
		order[dim] = i
	}
	for _, item := range items {
		scores := grouped[item.ID]
		sort.SliceStable(scores, func(i, j int) bool {
			oi, iok := order[scores[i].Dimension.String()]
			oj, jok := order[scores[j].Dimension.String()]
			if !iok {
				oi = len(order)
			}
			if !jok {
				oj = len(order)
			}
			if oi != oj {
				return oi < oj
			}
			return scores[i].ID < scores[j].ID
		})
		total := 0
		for _, row := range scores {
			total += row.WeightBp
		}
		views = append(views, playerAssessmentView{
			Assessment: item, Scores: scores, WeightTotalBP: total, DimensionCount: len(scores),
		})
	}
	return views, nil
}

// ── §7.5-05 异常刷评自动识别 ──

const (
	// reviewBrushRuleKey 是刷评识别规则（JSON）；留空 = 关闭自动识别（显式降级，不伪成功）。
	reviewBrushRuleKey = "app.esports.review_brush_rule"
	// reviewAppealWindowDaysKey 是评价申诉时限（天）；0 = 不限时。
	reviewAppealWindowDaysKey = "app.esports.review_appeal_window_days"

	defaultReviewAppealWindowDays = 30
	defaultReviewBrushWindowHours = 24
	defaultReviewBrushMaxPerUser  = 3

	// 命中规则码（写入 EsportsPlayerReview.dispute_rule_code 留痕）。
	reviewBrushRuleCustomerBurst    = "review_burst_customer"
	reviewBrushRuleIdenticalComment = "review_identical_comment"
	// reviewAppealPreviousStatusKey 记在 dispute_detail 中，供驳回复核时复原申诉前状态。
	reviewAppealPreviousStatusKey = "appeal_previous_status"
)

// reviewBrushRule 是刷评识别规则（§7.5-05）。
// JSON：{"window_hours":24,"max_reviews_per_customer":3,"identical_comment":true}
type reviewBrushRule struct {
	WindowHours           int  `json:"window_hours"`
	MaxReviewsPerCustomer int  `json:"max_reviews_per_customer"`
	IdenticalComment      bool `json:"identical_comment"`
}

// resolveReviewBrushRule 读取刷评识别规则：留空 = 关闭（返回 nil）；JSON 非法 = 明确报错，
// 避免运营写错配置后系统静默不识别（§7 不假成功）。
func resolveReviewBrushRule(ctx context.Context, db *ent.Client) (*reviewBrushRule, error) {
	raw := strings.TrimSpace(assessmentConfigString(ctx, db, reviewBrushRuleKey, ""))
	if raw == "" {
		return nil, nil
	}
	var rule reviewBrushRule
	if err := json.Unmarshal([]byte(raw), &rule); err != nil {
		return nil, fmt.Errorf("%w：异常刷评识别规则配置非法（%s）", errNotConfigured, reviewBrushRuleKey)
	}
	if rule.WindowHours <= 0 {
		rule.WindowHours = defaultReviewBrushWindowHours
	}
	if rule.MaxReviewsPerCustomer <= 0 {
		rule.MaxReviewsPerCustomer = defaultReviewBrushMaxPerUser
	}
	return &rule, nil
}

// detectReviewBrushing 单轮异常刷评识别（§7.5-05）：命中规则的评价置 disputed 并留痕，返回标记条数。
//
// 规则（按窗口内同一客户对同一陪玩的评价判定，已终局 invalid / 申诉待复核的评价不参与）：
//  1. review_burst_customer：同客户同陪玩在窗口内的有效评价数 ≥ max_reviews_per_customer；
//  2. review_identical_comment：同客户同陪玩在窗口内出现 ≥2 条完全相同的非空评语（需 identical_comment=true）。
func detectReviewBrushing(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errInvalidInput
	}
	rule, err := resolveReviewBrushRule(ctx, db)
	if err != nil || rule == nil {
		return 0, err
	}
	rows, err := db.EsportsPlayerReview.Query().Where(
		esportsplayerreview.VerifiedEQ(true),
		esportsplayerreview.CreatedAtGTE(now.UTC().Add(-time.Duration(rule.WindowHours)*time.Hour)),
		esportsplayerreview.StatusNotIn(
			esportsplayerreview.StatusInvalid, esportsplayerreview.StatusAppealPending),
	).Order(ent.Asc(esportsplayerreview.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	type groupKey struct{ customerID, playerID int }
	groups := make(map[groupKey][]*ent.EsportsPlayerReview)
	order := make([]groupKey, 0)
	for _, row := range rows {
		key := groupKey{customerID: row.CustomerID, playerID: row.PlayerID}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], row)
	}
	marked := 0
	for _, key := range order {
		items := groups[key]
		targets := make([]*ent.EsportsPlayerReview, 0)
		ruleCode := ""
		reason := ""
		if len(items) >= rule.MaxReviewsPerCustomer {
			targets = items
			ruleCode = reviewBrushRuleCustomerBurst
			reason = fmt.Sprintf("%d 小时内同一客户对同一陪玩评价 %d 条", rule.WindowHours, len(items))
		} else if rule.IdenticalComment {
			byComment := make(map[string][]*ent.EsportsPlayerReview)
			for _, row := range items {
				comment := strings.TrimSpace(row.Comment)
				if comment == "" {
					continue
				}
				byComment[comment] = append(byComment[comment], row)
			}
			for _, bucket := range byComment {
				if len(bucket) >= 2 {
					targets = append(targets, bucket...)
				}
			}
			if len(targets) > 0 {
				ruleCode = reviewBrushRuleIdenticalComment
				reason = fmt.Sprintf("%d 小时内同一客户对同一陪玩出现 %d 条完全相同评语", rule.WindowHours, len(targets))
			}
		}
		for _, row := range targets {
			detail := map[string]interface{}{
				"rule_code":    ruleCode,
				"window_hours": rule.WindowHours,
				"customer_id":  row.CustomerID,
				"player_id":    row.PlayerID,
				"group_count":  len(items),
				"reason":       reason,
			}
			changed, err := db.EsportsPlayerReview.Update().Where(
				esportsplayerreview.IDEQ(row.ID),
				esportsplayerreview.StatusNotIn(
					esportsplayerreview.StatusDisputed, esportsplayerreview.StatusInvalid,
					esportsplayerreview.StatusAppealPending),
			).SetStatus(esportsplayerreview.StatusDisputed).SetAutoDisputed(true).
				SetDisputeRuleCode(ruleCode).SetDisputeDetail(detail).
				SetDisputedAt(now.UTC()).AddVersion(1).Save(ctx)
			if err != nil {
				return marked, err
			}
			marked += changed
		}
	}
	return marked, nil
}

// detectReviewBrushingTask 定时任务入口：需由主控注册为 esports_detect_review_brushing
// （建议 CronJob `0 */5 * * * *`）。
func detectReviewBrushingTask(ctx context.Context, logger io.Writer) error {
	count, err := detectReviewBrushing(ctx, global.ORM, time.Now(), 500)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "识别异常刷评评价：%d 条\n", count)
	}
	return err
}

// ── §7.5-05 评价申诉（发起 → 管理员复核） ──

// assessmentConfigIntAllowZero 读取整数配置并允许显式 0（与 assessmentConfigInt 的「≤0 回落」不同，
// 用于「0 = 不限时」这类语义）；缺失 / 非法回落 fallback，负数按 0 处理。
func assessmentConfigIntAllowZero(ctx context.Context, db *ent.Client, key string, fallback int) int {
	raw := strings.TrimSpace(assessmentConfigString(ctx, db, key, ""))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if value < 0 {
		return 0
	}
	return value
}

// reviewAppealWindowDays 读取评价申诉时限（天）：0 = 不限时，缺失 / 非法回落 30。
func reviewAppealWindowDays(ctx context.Context, db *ent.Client) int {
	return assessmentConfigIntAllowZero(ctx, db, reviewAppealWindowDaysKey, defaultReviewAppealWindowDays)
}

// createReviewAppeal 发起评价申诉（§7.5-05）：客户 / 陪玩本人可对自己涉及的评价申诉，
// 申诉期间评价转 appeal_pending 并保留申诉前状态，供驳回时复原。
func createReviewAppeal(ctx context.Context, db *ent.Client, reviewID int, appellantType string, appellantID int,
	reason, evidenceRef string, now time.Time) (*ent.EsportsPlayerReviewAppeal, error) {
	reason = strings.TrimSpace(reason)
	evidenceRef = strings.TrimSpace(evidenceRef)
	appellantType = strings.TrimSpace(appellantType)
	if reviewID < 1 || appellantID < 1 || reason == "" || len([]rune(reason)) > 500 ||
		len(evidenceRef) > 255 || (appellantType != "customer" && appellantType != "player") {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	review, err := tx.EsportsPlayerReview.Get(ctx, reviewID)
	if err != nil {
		return nil, err
	}
	if appellantType == "customer" && review.CustomerID != appellantID {
		return nil, errForbidden
	}
	if appellantType == "player" && review.PlayerID != appellantID {
		return nil, errForbidden
	}
	if review.Status == esportsplayerreview.StatusInvalid {
		return nil, fmt.Errorf("%w：该评价已终局判定，不可申诉", errInvalidInput)
	}
	if review.Status == esportsplayerreview.StatusAppealPending {
		return nil, fmt.Errorf("%w：该评价已有待复核申诉", errConflict)
	}
	if days := reviewAppealWindowDays(ctx, tx.Client()); days > 0 &&
		now.UTC().After(review.CreatedAt.UTC().AddDate(0, 0, days)) {
		return nil, fmt.Errorf("%w：已超过 %d 天申诉时限", errInvalidInput, days)
	}
	appeal, err := tx.EsportsPlayerReviewAppeal.Create().SetReviewID(reviewID).
		SetAppellantType(esportsplayerreviewappeal.AppellantType(appellantType)).
		SetAppellantID(appellantID).SetOrderID(review.OrderID).
		SetReason(reason).SetEvidenceRef(evidenceRef).
		SetStatus(esportsplayerreviewappeal.StatusPending).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, fmt.Errorf("%w：该评价已有申诉记录", errConflict)
		}
		return nil, err
	}
	detail := map[string]interface{}{}
	for key, value := range review.DisputeDetail {
		detail[key] = value
	}
	detail[reviewAppealPreviousStatusKey] = review.Status.String()
	if _, err = tx.EsportsPlayerReview.Update().Where(esportsplayerreview.IDEQ(reviewID)).
		SetStatus(esportsplayerreview.StatusAppealPending).SetDisputeDetail(detail).
		AddVersion(1).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsPlayerReviewAppeal.Get(ctx, appeal.ID)
}

// reviewReviewAppeal 管理员复核评价申诉（§7.5-05）：成立则复原评价（normal 且清自动判定标记），
// 驳回则复原到申诉前状态（默认 disputed / normal）。复核结论与复核人一并留痕。
func reviewReviewAppeal(ctx context.Context, db *ent.Client, appealID, adminID int, approve bool,
	note string, now time.Time) (*ent.EsportsPlayerReview, error) {
	note = strings.TrimSpace(note)
	if appealID < 1 || len([]rune(note)) > 500 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	appeal, err := tx.EsportsPlayerReviewAppeal.Get(ctx, appealID)
	if err != nil {
		return nil, err
	}
	if appeal.Status != esportsplayerreviewappeal.StatusPending {
		return nil, fmt.Errorf("%w：该申诉已复核", errConflict)
	}
	review, err := tx.EsportsPlayerReview.Get(ctx, appeal.ReviewID)
	if err != nil {
		return nil, err
	}
	if review.Status != esportsplayerreview.StatusAppealPending {
		return nil, fmt.Errorf("%w：评价状态已变化，请刷新后重试", errConflict)
	}
	detail := map[string]interface{}{}
	for key, value := range review.DisputeDetail {
		detail[key] = value
	}
	detail["appeal_decision"] = map[string]interface{}{
		"appeal_id": appealID, "reviewer_admin_id": adminID, "approved": approve,
		"reviewed_at": now.UTC().Format(time.RFC3339),
	}
	appealStatus := esportsplayerreviewappeal.StatusRejected
	restored := esportsplayerreview.StatusDisputed
	if previous, ok := detail[reviewAppealPreviousStatusKey].(string); ok {
		switch previous {
		case esportsplayerreview.StatusNormal.String():
			restored = esportsplayerreview.StatusNormal
		case esportsplayerreview.StatusDisputed.String():
			restored = esportsplayerreview.StatusDisputed
		}
	} else if !review.AutoDisputed {
		restored = esportsplayerreview.StatusNormal
	}
	update := tx.EsportsPlayerReview.Update().Where(
		esportsplayerreview.IDEQ(review.ID),
		esportsplayerreview.StatusEQ(esportsplayerreview.StatusAppealPending),
	).SetStatus(restored).SetDisputeDetail(detail).AddVersion(1)
	if approve {
		appealStatus = esportsplayerreviewappeal.StatusApproved
		update.SetStatus(esportsplayerreview.StatusNormal).SetAutoDisputed(false)
	}
	if _, err = update.Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsPlayerReviewAppeal.Update().Where(
		esportsplayerreviewappeal.IDEQ(appealID),
		esportsplayerreviewappeal.StatusEQ(esportsplayerreviewappeal.StatusPending),
	).SetStatus(appealStatus).SetReviewerAdminID(adminID).
		SetReviewNote(note).SetReviewedAt(now.UTC()).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsPlayerReview.Get(ctx, review.ID)
}

// listReviewAppeals 列出评价申诉（§7.5-05 管理端复核队列），可按 status 过滤。
func listReviewAppeals(ctx context.Context, db *ent.Client, status string, limit int) ([]*ent.EsportsPlayerReviewAppeal, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsPlayerReviewAppeal.Query()
	if status = strings.TrimSpace(status); status != "" {
		if status != "pending" && status != "approved" && status != "rejected" {
			return nil, errInvalidInput
		}
		q.Where(esportsplayerreviewappeal.StatusEQ(esportsplayerreviewappeal.Status(status)))
	}
	return q.Order(ent.Asc(esportsplayerreviewappeal.FieldStatus),
		ent.Desc(esportsplayerreviewappeal.FieldID)).Limit(limit).All(ctx)
}

// reviewAppealAppellant 解析自助申诉主体（§7.5-05）：优先按陪玩解析，其次按已核验客户；
// 都不是则无权申诉。handler 在 assessment_handlers.go 中调用。
func reviewAppealAppellant(ctx context.Context, uid string) (string, int, error) {
	if player, err := currentPlayer(ctx, global.ORM, uid); err == nil {
		return "player", player.ID, nil
	}
	if customer, err := customerForVerifiedUser(ctx, global.ORM, uid); err == nil {
		return "customer", customer.ID, nil
	}
	return "", 0, errForbidden
}

package esports

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsassessment"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/predicate"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

// §7.1-02 / §7.2-01 升级考核：陪玩端自助申请 + 系统按达标条件自动触发。
//
// 申请门槛（服务量 / 评分 / 在岗时长）与冷却时长均按系统配置键读取、代码内回落默认值，
// 沿用 assessmentPassScoreKey 的既有写法（assessmentConfigInt）：≤0 或非法值表示「回落默认」，
// 不能表达「关闭」（要用 0 关闭门槛需另加语义，当前不支持）。
const (
	// upgradeMinServiceUnitsKey 为累计已完成服务量门槛（服务段 reported_units 求和）。
	upgradeMinServiceUnitsKey     = "app.esports.upgrade_apply_min_service_units"
	defaultUpgradeMinServiceUnits = 30
	// upgradeMinRatingScoreKey 为综合评分门槛，口径为 1~5 星折算的百分制分值。
	upgradeMinRatingScoreKey     = "app.esports.upgrade_apply_min_rating_score"
	defaultUpgradeMinRatingScore = 80
	// upgradeMinTenureDaysKey 为在岗（入驻）天数门槛。
	upgradeMinTenureDaysKey     = "app.esports.upgrade_apply_min_tenure_days"
	defaultUpgradeMinTenureDays = 7
	// upgradeCooldownDaysKey 为两次升级考核申请之间的冷却天数。
	upgradeCooldownDaysKey     = "app.esports.upgrade_apply_cooldown_days"
	defaultUpgradeCooldownDays = 30

	// upgradeAutoTriggerBatchCap 是单轮自动触发扫描的候选陪玩上限（限流，避免一次扫全表）。
	upgradeAutoTriggerBatchCap = 200
	// upgradeRatingScorePerStar 把 1~5 星评价折算为百分制分值（5 星 = 100 分）。
	upgradeRatingScorePerStar = 20
	// upgradeAutoTriggerCursorKey 是自动触发扫描的持久游标：已扫过的最大陪玩 ID，
	// 0（或缺失、非法、负数）表示已扫到表尾、下一轮从头扫。由任务自身写回，运维不要手改。
	upgradeAutoTriggerCursorKey = "app.esports.upgrade_auto_trigger_cursor"
)

// upgradeDedupKey 是「同一陪玩至多一条进行中升级考核单」的唯一索引兜底键。
// 存 active_dedup_key 列：升级建单时写入，落终态（结论已出且无待复核申诉）时清空为 NULL。
func upgradeDedupKey(playerID int) string {
	return "upgrade:" + strconv.Itoa(playerID)
}

// upgradeInProgressPredicate 判定升级考核「进行中」：待处理 / 已约考，或结论已出但申诉待复核。
// 申诉待复核的终态单仍可能被改判并回写等级，必须与进行中同口径拦截新申请。
func upgradeInProgressPredicate() predicate.EsportsAssessment {
	return esportsassessment.Or(
		esportsassessment.StatusIn(esportsassessment.StatusPending, esportsassessment.StatusScheduled),
		esportsassessment.AppealStatusEQ(esportsassessment.AppealStatusPending),
	)
}

// findInProgressUpgradeAssessment 返回该陪玩当前进行中的升级考核单（口径见 upgradeInProgressPredicate），
// excludeID > 0 时排除该单自身；未找到返回 (nil, nil)。
func findInProgressUpgradeAssessment(ctx context.Context, db *ent.Client, playerID, excludeID int) (*ent.EsportsAssessment, error) {
	q := db.EsportsAssessment.Query().Where(
		esportsassessment.PlayerIDEQ(playerID),
		esportsassessment.AssessmentTypeEQ(esportsassessment.AssessmentTypeUpgrade),
		upgradeInProgressPredicate(),
	)
	if excludeID > 0 {
		q.Where(esportsassessment.IDNEQ(excludeID))
	}
	item, err := q.Order(ent.Asc(esportsassessment.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return item, nil
}

// resolveExistingUpgrade 把「已存在进行中升级单」转换成对外结果：
// 待处理 / 已约考表示重复提交，幂等返回既有单；申诉待复核则拦截并给出中文提示。
func resolveExistingUpgrade(item *ent.EsportsAssessment) (*ent.EsportsAssessment, error) {
	if item.Status == esportsassessment.StatusPending || item.Status == esportsassessment.StatusScheduled {
		return item, nil
	}
	return nil, fmt.Errorf("%w：升级考核（单号 %s）申诉待复核，请等待复核结论后再申请",
		errInvalidInput, item.TaskNo)
}

// nextUpgradeLevel 返回当前等级的上一档；已是最高档（mentor）返回 false。
func nextUpgradeLevel(current esportsplayer.Level) (esportsplayer.Level, bool) {
	switch current {
	case esportsplayer.LevelNovice:
		return esportsplayer.LevelSkilled, true
	case esportsplayer.LevelSkilled:
		return esportsplayer.LevelSenior, true
	case esportsplayer.LevelSenior:
		return esportsplayer.LevelMentor, true
	default:
		return esportsplayer.LevelMentor, false
	}
}

// playerQualificationLabel 资格枚举的中文提示文案（面向陪玩的拒绝理由）。
func playerQualificationLabel(qualification esportsplayer.Qualification) string {
	switch qualification {
	case esportsplayer.QualificationQualified:
		return "合格"
	case esportsplayer.QualificationSuspended:
		return "已暂停接单"
	default:
		return "待审核"
	}
}

// upgradeEligibility 是升级考核申请门槛的判定快照：服务量 / 评分 / 在岗时长及其门槛值。
type upgradeEligibility struct {
	ServiceUnits    int64 `json:"service_units"`
	MinServiceUnits int   `json:"min_service_units"`
	// RatingScore 为综合评分折算的百分制分值（0~100），RatingAverage 为原始星级。
	RatingScore        int     `json:"rating_score"`
	RatingAverage      float64 `json:"rating_average"`
	RatingSamples      int     `json:"rating_samples"`
	RatingInsufficient bool    `json:"rating_insufficient"`
	MinRatingScore     int     `json:"min_rating_score"`
	TenureDays         int     `json:"tenure_days"`
	MinTenureDays      int     `json:"min_tenure_days"`
}

// qualify 判定是否达到全部三项门槛。
func (g *upgradeEligibility) qualify() bool {
	return len(g.unmet()) == 0
}

// unmet 逐项列出未达门槛的原因（中文，含实际值与门槛值，便于前端直接提示）。
//
// 评分口径沿用 playerRatingSummary 的既有设计：样本不足时「不当作低分」，因此不因缺评价拦截申请，
// 只在实际样本充足且均分低于门槛时拦截（样本数会写进申请备注，便于考官复核）。
func (g *upgradeEligibility) unmet() []string {
	reasons := make([]string, 0, 3)
	if g.ServiceUnits < int64(g.MinServiceUnits) {
		reasons = append(reasons, fmt.Sprintf("累计服务量 %d/%d", g.ServiceUnits, g.MinServiceUnits))
	}
	if !g.RatingInsufficient && g.RatingScore < g.MinRatingScore {
		reasons = append(reasons, fmt.Sprintf("综合评分 %d/%d（%.2f 星 / %d 条）",
			g.RatingScore, g.MinRatingScore, g.RatingAverage, g.RatingSamples))
	}
	if g.TenureDays < g.MinTenureDays {
		reasons = append(reasons, fmt.Sprintf("在岗 %d/%d 天", g.TenureDays, g.MinTenureDays))
	}
	return reasons
}

// summary 生成申请依据备注：把本次判定的实际值留痕，便于考官与审计复核。
func (g *upgradeEligibility) summary() string {
	return fmt.Sprintf("累计服务量 %d、综合评分 %d 分（%.2f 星 / %d 条评价）、在岗 %d 天",
		g.ServiceUnits, g.RatingScore, g.RatingAverage, g.RatingSamples, g.TenureDays)
}

// sumPlayerFinishedServiceUnits 求和陪玩已完成服务段的已上报服务量
// （单位同订单 SKU 的 service_unit_name）；进行中 / 已暂停的服务段不计入，避免未履约量充抵门槛。
func sumPlayerFinishedServiceUnits(ctx context.Context, db *ent.Client, playerID int) (int64, error) {
	var rows []struct {
		Sum int64 `json:"sum"`
	}
	if err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.PlayerIDEQ(playerID),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusFinished),
	).Aggregate(ent.Sum(esportsservicesegment.FieldReportedUnits)).Scan(ctx, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Sum, nil
}

// playerUpgradeEligibility 计算陪玩当前的服务量 / 评分 / 在岗时长与对应门槛。
// 在岗时长按陪玩档案 created_at（入驻时点）到 now 的日历天数计算，按 UTC 口径取整。
func playerUpgradeEligibility(ctx context.Context, db *ent.Client, player *ent.EsportsPlayer, now time.Time) (*upgradeEligibility, error) {
	units, err := sumPlayerFinishedServiceUnits(ctx, db, player.ID)
	if err != nil {
		return nil, err
	}
	rating, err := playerRatingSummary(ctx, db, player.ID)
	if err != nil {
		return nil, err
	}
	tenureDays := int(now.UTC().Sub(player.CreatedAt.UTC()).Hours() / 24)
	if tenureDays < 0 {
		tenureDays = 0
	}
	return &upgradeEligibility{
		ServiceUnits:       units,
		MinServiceUnits:    assessmentConfigInt(ctx, db, upgradeMinServiceUnitsKey, defaultUpgradeMinServiceUnits),
		RatingScore:        int(rating.Average * upgradeRatingScorePerStar),
		RatingAverage:      rating.Average,
		RatingSamples:      rating.SampleCount,
		RatingInsufficient: rating.Insufficient,
		MinRatingScore:     assessmentConfigInt(ctx, db, upgradeMinRatingScoreKey, defaultUpgradeMinRatingScore),
		TenureDays:         tenureDays,
		MinTenureDays:      assessmentConfigInt(ctx, db, upgradeMinTenureDaysKey, defaultUpgradeMinTenureDays),
	}, nil
}

// createUpgradeAssessment 是升级考核单的唯一创建路径（陪玩自助申请与系统自动触发共用），
// 目标档位取当前等级的上一档，状态为待处理（pending），字段拼装复用考官端的 createAssessment。
//
// 幂等与并发：以「同一陪玩至多一条进行中升级单」为口径（进行中 = 待处理 / 已约考 / 申诉待复核）——
//   - 服务层：事务内先查进行中，命中则幂等返回既有单（重复提交不报错、不新建），申诉待复核则拦截；
//   - 数据库层：active_dedup_key 上的唯一索引兜底（可空唯一列，MySQL 5.7/8.0 与 SQLite 都允许多个 NULL，
//     不使用 FOR UPDATE 行锁、也不使用 MySQL 5.7 不支持的部分索引）。并发双击时后到的事务会撞唯一索引，
//     此处回滚后回查并返回既有单，而不是把数据库错误抛给用户。
//
// created 表示本次调用是否真的新建了单据，供自动触发任务计数。
func createUpgradeAssessment(ctx context.Context, db *ent.Client, playerID int, source, note string, now time.Time) (*ent.EsportsAssessment, bool, error) {
	if playerID < 1 {
		return nil, false, errForbidden
	}
	player, err := db.EsportsPlayer.Get(ctx, playerID)
	if ent.IsNotFound(err) {
		return nil, false, errForbidden
	}
	if err != nil {
		return nil, false, err
	}
	target, ok := nextUpgradeLevel(player.Level)
	if !ok {
		return nil, false, fmt.Errorf("%w：当前已是最高等级（%s），无需申请升级考核", errInvalidInput, player.Level)
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	inProgress, err := findInProgressUpgradeAssessment(ctx, tx.Client(), playerID, 0)
	if err != nil {
		return nil, false, err
	}
	if inProgress != nil {
		item, resolveErr := resolveExistingUpgrade(inProgress)
		return item, false, resolveErr
	}
	cooldownDays := assessmentConfigInt(ctx, tx.Client(), upgradeCooldownDaysKey, defaultUpgradeCooldownDays)
	latest, err := tx.EsportsAssessment.Query().Where(
		esportsassessment.PlayerIDEQ(playerID),
		esportsassessment.AssessmentTypeEQ(esportsassessment.AssessmentTypeUpgrade),
	).Order(ent.Desc(esportsassessment.FieldID)).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, false, err
	}
	if err == nil {
		readyAt := latest.CreatedAt.UTC().AddDate(0, 0, cooldownDays)
		if now.UTC().Before(readyAt) {
			// 业务日按 UTC+8 口径展示（§6 编码约定）。
			return nil, false, fmt.Errorf("%w：距上次升级考核未满 %d 天冷却期（%s 后可再次申请）",
				errInvalidInput, cooldownDays, readyAt.In(cstZone).Format("2006-01-02"))
		}
	}
	assessment, err := createAssessment(ctx, tx.Client(), 0, assessmentInput{
		PlayerID:       playerID,
		AssessmentType: "upgrade",
		TargetLevel:    target.String(),
		Source:         source,
		Note:           note,
	}, now)
	if err != nil {
		if errors.Is(err, errConflict) {
			// 唯一索引兜底命中（并发双击的时间隙）：回滚本事务后回查既有进行中单据。
			_ = tx.Rollback()
			existing, queryErr := findInProgressUpgradeAssessment(ctx, db, playerID, 0)
			if queryErr != nil {
				return nil, false, queryErr
			}
			if existing != nil {
				item, resolveErr := resolveExistingUpgrade(existing)
				return item, false, resolveErr
			}
		}
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	created, err := db.EsportsAssessment.Get(ctx, assessment.ID)
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}

// loadUpgradeApplyPlayer 解析并校验自助申请主体：档案存在、资格已合格（定级为合格时已过实名门槛），
// 并显式复核实名认证仍然有效（认证可被撤销，避免拿旧资格绕过 §6.1）。
func loadUpgradeApplyPlayer(ctx context.Context, db *ent.Client, playerID int) (*ent.EsportsPlayer, error) {
	if playerID < 1 {
		return nil, errForbidden
	}
	player, err := db.EsportsPlayer.Get(ctx, playerID)
	if ent.IsNotFound(err) {
		return nil, errForbidden
	}
	if err != nil {
		return nil, err
	}
	if player.Qualification != esportsplayer.QualificationQualified {
		return nil, fmt.Errorf("%w：陪玩资格未通过（当前%s），暂不可申请升级考核",
			errInvalidInput, playerQualificationLabel(player.Qualification))
	}
	verified, err := isPlayerIdentityVerified(ctx, db, playerID)
	if err != nil {
		return nil, err
	}
	if !verified {
		return nil, fmt.Errorf("%w：实名认证未通过，请先完成实名认证后再申请升级考核", errInvalidInput)
	}
	return player, nil
}

// applyPlayerUpgradeAssessment 陪玩自助申请升级考核（§7.1-02）：
// 校验主体资格、服务量 / 评分 / 在岗时长门槛与冷却后，落一条 pending 的 upgrade 考核单。
func applyPlayerUpgradeAssessment(ctx context.Context, db *ent.Client, playerID int, now time.Time) (*ent.EsportsAssessment, error) {
	player, err := loadUpgradeApplyPlayer(ctx, db, playerID)
	if err != nil {
		return nil, err
	}
	eligibility, err := playerUpgradeEligibility(ctx, db, player, now)
	if err != nil {
		return nil, err
	}
	if !eligibility.qualify() {
		return nil, fmt.Errorf("%w：暂未达到升级考核申请门槛（%s）",
			errInvalidInput, strings.Join(eligibility.unmet(), "、"))
	}
	item, _, err := createUpgradeAssessment(ctx, db, playerID, "apply",
		"陪玩自助申请升级考核："+eligibility.summary(), now)
	if err != nil {
		return nil, err
	}
	return item, nil
}

// readUpgradeAutoTriggerCursor 读取自动触发扫描游标（上一轮已扫过的最大陪玩 ID）。
// 缺失 / 非法 / 负数一律按 0 处理（从头扫）。
func readUpgradeAutoTriggerCursor(ctx context.Context, db *ent.Client) (int, error) {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(upgradeAutoTriggerCursorKey)).Only(ctx)
	if ent.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cursor, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || cursor < 0 {
		return 0, nil
	}
	return cursor, nil
}

// writeUpgradeAutoTriggerCursor 持久化扫描游标；cursor 为 0 表示已扫到表尾、下一轮从头开始。
func writeUpgradeAutoTriggerCursor(ctx context.Context, db *ent.Client, cursor int) error {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(upgradeAutoTriggerCursorKey)).Only(ctx)
	if ent.IsNotFound(err) {
		_, err = db.SystemConfig.Create().SetKey(upgradeAutoTriggerCursorKey).
			SetName("升级考核自动触发游标").SetValue(strconv.Itoa(cursor)).
			SetType("number").SetGroup("esports").SetStatus(1).Save(ctx)
		return err
	}
	if err != nil {
		return err
	}
	_, err = db.SystemConfig.UpdateOneID(item.ID).SetValue(strconv.Itoa(cursor)).Save(ctx)
	return err
}

// autoTriggerUpgradeAssessments 扫描达标陪玩，自动生成 pending 升级考核单（§7.2-01 系统触发）。
//
// 游标推进（范式同 ledgerCursorScan）：单轮只处理游标之后的 limit 名候选（按 ID 升序），
// 本批不足 limit 说明已扫到表尾，游标回卷 0、下一轮从头扫；否则写回本批最大 ID 续扫。
// 这样即便游标前方全是「SQL 命中但 Go 层门槛不达标」的陪玩，扫描也会持续推进到后排达标者，
// 不会因为排序+限流被头部永久占满而饿死后排陪玩。
//
// 幂等依据：候选集在 SQL 层排除「已有进行中单 / 申诉待复核 / 冷却期内」的陪玩，建单又统一走
// createUpgradeAssessment（事务内二次查重 + active_dedup_key 唯一索引兜底），重复运行不会重复建单。
func autoTriggerUpgradeAssessments(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 500 {
		return 0, errInvalidInput
	}
	cursor, err := readUpgradeAutoTriggerCursor(ctx, db)
	if err != nil {
		return 0, err
	}
	// 冷却期口径与 createUpgradeAssessment 一致：最近一条升级单创建于 now-cooldownDays 之后的陪玩
	// 本轮一定建不出单，直接在 SQL 层排掉，避免白占单轮额度（appeal pending 同样排掉）。
	cooldownDays := assessmentConfigInt(ctx, db, upgradeCooldownDaysKey, defaultUpgradeCooldownDays)
	blockedRows, err := db.EsportsAssessment.Query().Where(
		esportsassessment.AssessmentTypeEQ(esportsassessment.AssessmentTypeUpgrade),
		esportsassessment.Or(
			upgradeInProgressPredicate(),
			esportsassessment.CreatedAtGT(now.UTC().AddDate(0, 0, -cooldownDays)),
		),
	).Select(esportsassessment.FieldPlayerID).All(ctx)
	if err != nil {
		return 0, err
	}
	blocked := make([]int, 0, len(blockedRows))
	seen := make(map[int]struct{}, len(blockedRows))
	for _, row := range blockedRows {
		if _, ok := seen[row.PlayerID]; ok {
			continue
		}
		seen[row.PlayerID] = struct{}{}
		blocked = append(blocked, row.PlayerID)
	}
	tenureDays := assessmentConfigInt(ctx, db, upgradeMinTenureDaysKey, defaultUpgradeMinTenureDays)
	q := db.EsportsPlayer.Query().Where(
		esportsplayer.QualificationEQ(esportsplayer.QualificationQualified),
		esportsplayer.LevelNEQ(esportsplayer.LevelMentor),
		// 在岗时长门槛与门槛判定同一配置键，SQL 层先裁剪不会漏掉达标者。
		esportsplayer.CreatedAtLTE(now.UTC().AddDate(0, 0, -tenureDays)),
		esportsplayer.IDGT(cursor),
	)
	if len(blocked) > 0 {
		q.Where(esportsplayer.IDNotIn(blocked...))
	}
	candidates, err := q.Order(ent.Asc(esportsplayer.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	// 先推进游标再处理本批：某条候选出错时不会把游标卡死在同一批（回卷机制保证其他候选仍会被扫到）。
	next := 0
	if len(candidates) == limit {
		next = candidates[len(candidates)-1].ID
	}
	if err = writeUpgradeAutoTriggerCursor(ctx, db, next); err != nil {
		return 0, err
	}
	count := 0
	for _, player := range candidates {
		eligibility, err := playerUpgradeEligibility(ctx, db, player, now)
		if err != nil {
			return count, err
		}
		if !eligibility.qualify() {
			continue
		}
		_, created, err := createUpgradeAssessment(ctx, db, player.ID, "system",
			"系统按达标条件自动生成升级考核："+eligibility.summary(), now)
		if err != nil {
			// 冷却期 / 申诉待复核等业务拦截跳过即可，不中断整轮；其余错误上抛交由任务重试。
			if errors.Is(err, errInvalidInput) || errors.Is(err, errConflict) {
				continue
			}
			return count, err
		}
		if created {
			count++
		}
	}
	return count, nil
}

// autoTriggerUpgradeAssessmentsTask 定时任务入口（签名同既有任务，供 tasks.Register 注册）：
// 扫描达标陪玩自动生成升级考核单。需由主控注册为 esports_auto_trigger_upgrade_assessments。
func autoTriggerUpgradeAssessmentsTask(ctx context.Context, logger io.Writer) error {
	count, err := autoTriggerUpgradeAssessments(ctx, global.ORM, time.Now(), upgradeAutoTriggerBatchCap)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理升级考核自动触发：新建 %d 单\n", count)
	}
	return err
}

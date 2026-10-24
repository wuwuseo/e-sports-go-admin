package esports

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsassessment"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayerreview"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

// upgradeAutoTriggerCursorKeyLiteral 与生产代码的升级考核自动触发游标键保持同一字面量：
// 这里用字面量而非常量，避免 RED 阶段引用尚未存在的生产常量而无法编译。
const upgradeAutoTriggerCursorKeyLiteral = "app.esports.upgrade_auto_trigger_cursor"

// seedUpgradePlayer 建一个已入驻的合格陪玩：created_at 是 Immutable 字段，只能在 Create 时显式写入，
// 因此这里不使用通用 seedPlayer；实名认证一并置为通过（§6.1 定级前置条件）。
func seedUpgradePlayer(t *testing.T, db *ent.Client, userID int, level string, createdAt time.Time) *ent.EsportsPlayer {
	t.Helper()
	player := db.EsportsPlayer.Create().SetUserID(userID).SetDisplayName("升级陪玩").
		SetQualification(esportsplayer.QualificationQualified).
		SetPresence(esportsplayer.PresenceOnline).
		SetLevel(level).SetCreatedAt(createdAt.UTC()).SaveX(t.Context())
	seedVerifiedIdentity(t, db, player.ID)
	return player
}

// seedUpgradeServiceSegments 为陪玩落若干已完成服务段（同一订单+陪玩唯一，故按序号区分订单号）。
func seedUpgradeServiceSegments(t *testing.T, db *ent.Client, playerID int, units ...int64) {
	t.Helper()
	ctx := t.Context()
	for i, unit := range units {
		db.EsportsServiceSegment.Create().SetOrderID(7000 + playerID*100 + i).SetPlayerID(playerID).
			SetSeatIndex(1).SetStartedAt(time.Now().UTC()).
			SetStatus(esportsservicesegment.StatusFinished).SetReportedUnits(unit).SaveX(ctx)
	}
}

// seedUpgradeReviews 为陪玩落若干已核验、状态正常的客户评价（计入综合评分）。
func seedUpgradeReviews(t *testing.T, db *ent.Client, playerID, count, rating int) {
	t.Helper()
	ctx := t.Context()
	for i := 0; i < count; i++ {
		db.EsportsPlayerReview.Create().SetOrderID(5000 + playerID*100 + i).SetCustomerID(11).
			SetPlayerID(playerID).SetRating(rating).SetVerified(true).
			SetStatus(esportsplayerreview.StatusNormal).SaveX(ctx)
	}
}

// seedUpgradeEligiblePlayer 建一个达标陪玩：入驻 ≥ 7 天、累计服务量 30（默认门槛）、5 条 5 星评价。
func seedUpgradeEligiblePlayer(t *testing.T, db *ent.Client, userID int, level string, createdAt time.Time) *ent.EsportsPlayer {
	t.Helper()
	player := seedUpgradePlayer(t, db, userID, level, createdAt)
	seedUpgradeServiceSegments(t, db, player.ID, 12, 18)
	seedUpgradeReviews(t, db, player.ID, 5, 5)
	return player
}

// seedUpgradeConfig 写入一条系统配置（门槛 / 冷却用例用）。
func seedUpgradeConfig(t *testing.T, db *ent.Client, key, value string) {
	t.Helper()
	db.SystemConfig.Create().SetKey(key).SetName("升级考核测试配置").SetValue(value).SaveX(t.Context())
}

// 自助申请的门槛与主体资格：非陪玩 / 未实名 / 资格未通过 / 已最高档 / 服务量与在岗时长与评分未达门槛
// 都必须给出明确中文提示且不建单；样本不足沿用「不当作低分」口径；门槛可配置。
func TestPlayerUpgradeApplyEligibilityGuards(t *testing.T) {
	db := openAssessmentDB(t, "esports_upgrade_apply_guards")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	if _, err := applyPlayerUpgradeAssessment(ctx, db, 0, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非法陪玩 ID 未被拦截：%v", err)
	}
	if _, err := applyPlayerUpgradeAssessment(ctx, db, 9999, now); !errors.Is(err, errForbidden) {
		t.Fatalf("不存在的陪玩未被拦截：%v", err)
	}

	// 资格已是合格但实名认证未通过（§6.1）。
	unverified := db.EsportsPlayer.Create().SetUserID(201).SetDisplayName("未实名").
		SetQualification(esportsplayer.QualificationQualified).SetPresence(esportsplayer.PresenceOnline).
		SetLevel("novice").SetCreatedAt(now.AddDate(0, 0, -60)).SaveX(ctx)
	seedUpgradeServiceSegments(t, db, unverified.ID, 40)
	seedUpgradeReviews(t, db, unverified.ID, 5, 5)
	if _, err := applyPlayerUpgradeAssessment(ctx, db, unverified.ID, now); err == nil ||
		!errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "实名认证") {
		t.Fatalf("未实名陪玩未被拦截：%v", err)
	}

	// 资格待审核。
	pending := db.EsportsPlayer.Create().SetUserID(202).SetDisplayName("待审核").
		SetQualification(esportsplayer.QualificationPending).SetPresence(esportsplayer.PresenceOffline).
		SetLevel("novice").SetCreatedAt(now.AddDate(0, 0, -60)).SaveX(ctx)
	seedVerifiedIdentity(t, db, pending.ID)
	if _, err := applyPlayerUpgradeAssessment(ctx, db, pending.ID, now); err == nil ||
		!errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "资格") {
		t.Fatalf("资格未通过的陪玩未被拦截：%v", err)
	}

	// 已是最高等级。
	mentor := seedUpgradeEligiblePlayer(t, db, 203, "mentor", now.AddDate(0, 0, -90))
	if _, err := applyPlayerUpgradeAssessment(ctx, db, mentor.ID, now); err == nil ||
		!strings.Contains(err.Error(), "最高等级") {
		t.Fatalf("最高等级陪玩未被拦截：%v", err)
	}

	// 服务量不足：进行中的服务段不计入门槛。
	lowService := seedUpgradePlayer(t, db, 204, "novice", now.AddDate(0, 0, -90))
	seedUpgradeServiceSegments(t, db, lowService.ID, 5)
	db.EsportsServiceSegment.Create().SetOrderID(8801).SetPlayerID(lowService.ID).SetSeatIndex(1).
		SetStartedAt(now).SetStatus(esportsservicesegment.StatusInService).
		SetReportedUnits(1000).SaveX(ctx)
	seedUpgradeReviews(t, db, lowService.ID, 5, 5)
	if _, err := applyPlayerUpgradeAssessment(ctx, db, lowService.ID, now); err == nil ||
		!strings.Contains(err.Error(), "服务量") {
		t.Fatalf("服务量不足未被拦截：%v", err)
	}

	// 在岗时长不足。
	fresh := seedUpgradeEligiblePlayer(t, db, 205, "novice", now.AddDate(0, 0, -1))
	if _, err := applyPlayerUpgradeAssessment(ctx, db, fresh.ID, now); err == nil ||
		!strings.Contains(err.Error(), "在岗") {
		t.Fatalf("在岗时长不足未被拦截：%v", err)
	}

	// 样本充足但评分低于门槛（3 星 = 60 分 < 默认 80 分）。
	lowRating := seedUpgradePlayer(t, db, 206, "novice", now.AddDate(0, 0, -90))
	seedUpgradeServiceSegments(t, db, lowRating.ID, 30)
	seedUpgradeReviews(t, db, lowRating.ID, 5, 3)
	if _, err := applyPlayerUpgradeAssessment(ctx, db, lowRating.ID, now); err == nil ||
		!strings.Contains(err.Error(), "评分") {
		t.Fatalf("评分低于门槛未被拦截：%v", err)
	}

	// 样本不足不当作低分：无评价但服务量与在岗达标仍可申请。
	noReview := seedUpgradePlayer(t, db, 207, "novice", now.AddDate(0, 0, -90))
	seedUpgradeServiceSegments(t, db, noReview.ID, 30)
	if _, err := applyPlayerUpgradeAssessment(ctx, db, noReview.ID, now); err != nil {
		t.Fatalf("样本不足被当作低分拒绝：%v", err)
	}

	// 门槛可配置：调高服务量门槛后同一类陪玩被拒，提示带出配置值。
	seedUpgradeConfig(t, db, upgradeMinServiceUnitsKey, "999")
	blocked := seedUpgradeEligiblePlayer(t, db, 208, "novice", now.AddDate(0, 0, -90))
	if _, err := applyPlayerUpgradeAssessment(ctx, db, blocked.ID, now); err == nil ||
		!strings.Contains(err.Error(), "999") {
		t.Fatalf("配置化门槛未生效：%v", err)
	}
}

// 自助申请通过后落 pending 升级单（复用考官端创建路径）；重复点击幂等；冷却期内拒绝，届满后可再申请。
func TestPlayerUpgradeApplyCreatesPendingAndEnforcesCooldown(t *testing.T) {
	db := openAssessmentDB(t, "esports_upgrade_apply_create")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedUpgradeEligiblePlayer(t, db, 301, "skilled", now.AddDate(0, 0, -30))

	created, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, now)
	if err != nil {
		t.Fatalf("自助申请升级考核失败：%v", err)
	}
	if created.AssessmentType != esportsassessment.AssessmentTypeUpgrade ||
		created.Status != esportsassessment.StatusPending ||
		created.Source != esportsassessment.SourceApply ||
		created.TargetLevel != "senior" ||
		created.CreatedByAdminID != nil ||
		created.TaskNo == "" {
		t.Fatalf("升级单字段不符：%+v", created)
	}
	if created.Note == "" || !strings.Contains(created.Note, "服务量") {
		t.Fatalf("升级单未留下申请依据备注：%q", created.Note)
	}

	// 幂等：已有进行中的升级单时重复点击返回既有单，不新建、也不报错。
	repeat, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, now)
	if err != nil || repeat.ID != created.ID {
		t.Fatalf("重复申请未幂等返回既有单：%+v, %v", repeat, err)
	}
	if n := db.EsportsAssessment.Query().Where(
		esportsassessment.PlayerIDEQ(player.ID),
	).CountX(ctx); n != 1 {
		t.Fatalf("重复申请产生了多条升级单：%d", n)
	}

	// 上一单落定（failed）后进入冷却期。
	db.EsportsAssessment.UpdateOneID(created.ID).SetStatus(esportsassessment.StatusFailed).SaveX(ctx)
	if _, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, now); err == nil ||
		!errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "冷却") {
		t.Fatalf("冷却期内重复申请未被拦截：%v", err)
	}

	// 冷却期届满（上次升级单 created_at 在 31 天前）后可再次申请。
	expired := seedUpgradeEligiblePlayer(t, db, 302, "novice", now.AddDate(0, 0, -60))
	db.EsportsAssessment.Create().SetTaskNo("AS-UPGRADE-OLD-1").SetPlayerID(expired.ID).
		SetAssessmentType(esportsassessment.AssessmentTypeUpgrade).SetTargetLevel("skilled").
		SetStatus(esportsassessment.StatusFailed).SetSource(esportsassessment.SourceApply).
		SetCreatedAt(now.AddDate(0, 0, -31)).SaveX(ctx)
	again, err := applyPlayerUpgradeAssessment(ctx, db, expired.ID, now)
	if err != nil || again.Status != esportsassessment.StatusPending {
		t.Fatalf("冷却期届满后仍不能申请：%+v, %v", again, err)
	}

	// 冷却时长配置化：90 天冷却下，31 天前的上一单仍在冷却期内。
	strict := seedUpgradeEligiblePlayer(t, db, 303, "novice", now.AddDate(0, 0, -60))
	db.EsportsAssessment.Create().SetTaskNo("AS-UPGRADE-OLD-2").SetPlayerID(strict.ID).
		SetAssessmentType(esportsassessment.AssessmentTypeUpgrade).SetTargetLevel("skilled").
		SetStatus(esportsassessment.StatusFailed).SetSource(esportsassessment.SourceApply).
		SetCreatedAt(now.AddDate(0, 0, -31)).SaveX(ctx)
	seedUpgradeConfig(t, db, upgradeCooldownDaysKey, "90")
	if _, err := applyPlayerUpgradeAssessment(ctx, db, strict.ID, now); err == nil ||
		!strings.Contains(err.Error(), "90 天") {
		t.Fatalf("配置化冷却时长未生效：%v", err)
	}
}

// 系统自动触发：达标陪玩生成 pending 升级单（source=system，目标为上一档），
// 已有进行中升级单 / 未达标 / 资格暂停 / 已达最高档一律不建单；重复运行幂等。
func TestAutoTriggerUpgradeAssessmentsIsIdempotentAndScansEligible(t *testing.T) {
	db := openAssessmentDB(t, "esports_upgrade_auto_trigger")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	eligible := seedUpgradeEligiblePlayer(t, db, 401, "novice", now.AddDate(0, 0, -30))

	// 已有进行中的升级单：幂等依据，不得重复建单。
	inProgress := seedUpgradeEligiblePlayer(t, db, 402, "novice", now.AddDate(0, 0, -30))
	db.EsportsAssessment.Create().SetTaskNo("AS-UPGRADE-IN-PROGRESS").SetPlayerID(inProgress.ID).
		SetAssessmentType(esportsassessment.AssessmentTypeUpgrade).SetTargetLevel("skilled").
		SetStatus(esportsassessment.StatusPending).SaveX(ctx)

	// 未达标：服务量不足。
	short := seedUpgradePlayer(t, db, 403, "novice", now.AddDate(0, 0, -30))
	seedUpgradeServiceSegments(t, db, short.ID, 5)
	seedUpgradeReviews(t, db, short.ID, 5, 5)

	// 资格暂停：不自动触发。
	suspended := seedUpgradePlayer(t, db, 404, "novice", now.AddDate(0, 0, -30))
	seedUpgradeServiceSegments(t, db, suspended.ID, 30)
	seedUpgradeReviews(t, db, suspended.ID, 5, 5)
	db.EsportsPlayer.UpdateOneID(suspended.ID).SetQualification(esportsplayer.QualificationSuspended).SaveX(ctx)

	// 已达最高档：无升级空间。
	mentor := seedUpgradeEligiblePlayer(t, db, 405, "mentor", now.AddDate(0, 0, -30))

	count, err := autoTriggerUpgradeAssessments(ctx, db, now, upgradeAutoTriggerBatchCap)
	if err != nil {
		t.Fatalf("自动触发执行失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("自动触发数量不符：%d", count)
	}
	created := db.EsportsAssessment.Query().Where(
		esportsassessment.PlayerIDEQ(eligible.ID),
	).OnlyX(ctx)
	if created.Status != esportsassessment.StatusPending ||
		created.Source != esportsassessment.SourceSystem ||
		created.TargetLevel != "skilled" {
		t.Fatalf("自动生成的升级单字段不符：%+v", created)
	}
	if n := db.EsportsAssessment.Query().Where(
		esportsassessment.PlayerIDEQ(inProgress.ID),
	).CountX(ctx); n != 1 {
		t.Fatalf("已有进行中升级单的陪玩被重复建单：%d 条", n)
	}
	for _, tc := range []struct {
		playerID int
		name     string
	}{{short.ID, "未达标"}, {suspended.ID, "资格暂停"}, {mentor.ID, "已达最高档"}} {
		if n := db.EsportsAssessment.Query().Where(
			esportsassessment.PlayerIDEQ(tc.playerID),
		).CountX(ctx); n != 0 {
			t.Fatalf("%s 不应建单：%d 条", tc.name, n)
		}
	}

	// 重复运行幂等。
	count, err = autoTriggerUpgradeAssessments(ctx, db, now, upgradeAutoTriggerBatchCap)
	if err != nil || count != 0 {
		t.Fatalf("重复运行不幂等：%d, %v", count, err)
	}
}

// 单轮限流：每轮最多处理 limit 名候选，剩余留待下轮；非法限流参数被拒。
func TestAutoTriggerUpgradeAssessmentsRespectsBatchLimit(t *testing.T) {
	db := openAssessmentDB(t, "esports_upgrade_auto_limit")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		seedUpgradeEligiblePlayer(t, db, 501+i, "novice", now.AddDate(0, 0, -30))
	}

	if _, err := autoTriggerUpgradeAssessments(ctx, db, now, 0); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法限流参数未被拦截：%v", err)
	}
	if _, err := autoTriggerUpgradeAssessments(ctx, db, now, 501); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超上限的限流参数未被拦截：%v", err)
	}
	count, err := autoTriggerUpgradeAssessments(ctx, db, now, 2)
	if err != nil || count != 2 {
		t.Fatalf("单轮限流未生效：%d, %v", count, err)
	}
	count, err = autoTriggerUpgradeAssessments(ctx, db, now, 2)
	if err != nil || count != 1 {
		t.Fatalf("第二轮应处理剩余 1 名：%d, %v", count, err)
	}
	count, err = autoTriggerUpgradeAssessments(ctx, db, now, upgradeAutoTriggerBatchCap)
	if err != nil || count != 0 {
		t.Fatalf("全部处理完后不应再建单：%d, %v", count, err)
	}
}

// 任务函数签名（ctx, logger）可直接被 tasks.Register 注册，并按既有任务风格输出有意义的处理数量文案。
func TestAutoTriggerUpgradeAssessmentsTaskLogsCount(t *testing.T) {
	db := openAssessmentDB(t, "esports_upgrade_auto_task")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })
	ctx := t.Context()
	seedUpgradeEligiblePlayer(t, db, 601, "novice", time.Now().UTC().AddDate(0, 0, -30))

	var log bytes.Buffer
	if err := autoTriggerUpgradeAssessmentsTask(ctx, &log); err != nil {
		t.Fatalf("任务执行失败：%v", err)
	}
	if !strings.Contains(log.String(), "新建 1 单") {
		t.Fatalf("任务日志未输出可读的处理数量文案：%q", log.String())
	}
}

// P0-1 回归（重复提交）：已有进行中的升级单时，第二次自助申请必须幂等返回既有单，
// 既不报错也不新建第二条。
func TestPlayerUpgradeApplySecondSubmissionReturnsExistingSingle(t *testing.T) {
	db := openAssessmentDB(t, "esports_upgrade_apply_second")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedUpgradeEligiblePlayer(t, db, 711, "skilled", now.AddDate(0, 0, -30))

	first, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, now)
	if err != nil {
		t.Fatalf("首次自助申请失败：%v", err)
	}
	second, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, now)
	if err != nil {
		t.Fatalf("重复申请应幂等返回既有单，实际报错：%v", err)
	}
	if second.ID != first.ID || second.TaskNo != first.TaskNo {
		t.Fatalf("重复申请未返回既有单：%d/%s，期望 %d/%s", second.ID, second.TaskNo, first.ID, first.TaskNo)
	}
	if n := countUpgradeAssessments(ctx, db, player.ID); n != 1 {
		t.Fatalf("重复申请产生了 %d 条升级单，期望 1 条", n)
	}
}

// P0-1 回归（唯一索引兜底）：两次「同一陪玩 + 升级考核」建单并发通过服务层查重时，
// 第二条必须被数据库唯一索引拦下并返回业务冲突；非 upgrade 类型不参与该去重。
func TestUpgradeAssessmentActiveSingleIsUnique(t *testing.T) {
	db := openAssessmentDB(t, "esports_upgrade_active_unique")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedUpgradeEligiblePlayer(t, db, 721, "novice", now.AddDate(0, 0, -30))

	if _, err := createAssessment(ctx, db, 0, assessmentInput{
		PlayerID: player.ID, AssessmentType: "upgrade", TargetLevel: "skilled", Source: "system",
	}, now); err != nil {
		t.Fatalf("第一条升级单建单失败：%v", err)
	}
	if _, err := createAssessment(ctx, db, 0, assessmentInput{
		PlayerID: player.ID, AssessmentType: "upgrade", TargetLevel: "skilled", Source: "system",
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("第二条进行中升级单未被唯一索引拦下：%v", err)
	}
	if n := countUpgradeAssessments(ctx, db, player.ID); n != 1 {
		t.Fatalf("并发建单留下了 %d 条进行中升级单，期望 1 条", n)
	}
	// 其他考核类型不去重：入职考核连续两条都必须能建（可空去重键允许多个 NULL）。
	for i := 0; i < 2; i++ {
		if _, err := createAssessment(ctx, db, 11, assessmentInput{
			PlayerID: player.ID, AssessmentType: "entry",
		}, now); err != nil {
			t.Fatalf("第 %d 条入职考核被误拦：%v", i+1, err)
		}
	}
}

// 考官端建单必须落操作管理员 ID（created_by_admin_id），与陪玩自助建单保持 NULL 对称。
func TestCreateAssessmentRecordsCreatedByAdminID(t *testing.T) {
	db := openAssessmentDB(t, "esports_upgrade_created_by_admin")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedUpgradeEligiblePlayer(t, db, 731, "novice", now.AddDate(0, 0, -30))
	byAdmin, err := createAssessment(ctx, db, 11, assessmentInput{
		PlayerID: player.ID, AssessmentType: "upgrade", TargetLevel: "skilled",
	}, now)
	if err != nil {
		t.Fatalf("管理员建单失败：%v", err)
	}
	if byAdmin.CreatedByAdminID == nil || *byAdmin.CreatedByAdminID != 11 {
		t.Fatalf("管理员建单未写入 created_by_admin_id：%+v", byAdmin.CreatedByAdminID)
	}

	selfPlayer := seedUpgradeEligiblePlayer(t, db, 732, "novice", now.AddDate(0, 0, -30))
	bySelf, err := applyPlayerUpgradeAssessment(ctx, db, selfPlayer.ID, now)
	if err != nil {
		t.Fatalf("陪玩自助申请失败：%v", err)
	}
	if bySelf.CreatedByAdminID != nil {
		t.Fatalf("陪玩自助申请不应写入 created_by_admin_id：%+v", *bySelf.CreatedByAdminID)
	}
}

// P0-1 回归（申诉待复核拦截）：终态单的申诉未复核完时不算「进行中」会被漏拦；
// 若此时允许新申请并把等级升上去，旧申诉被批准会把等级绝对写回旧目标档。
func TestUpgradeApplyBlockedWhileAppealPending(t *testing.T) {
	db := openAssessmentDB(t, "esports_upgrade_appeal_pending_block")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedUpgradeEligiblePlayer(t, db, 741, "novice", now.AddDate(0, 0, -60))

	old := db.EsportsAssessment.Create().SetTaskNo("AS-UPGRADE-APPEAL-1").SetPlayerID(player.ID).
		SetAssessmentType(esportsassessment.AssessmentTypeUpgrade).SetTargetLevel("skilled").
		SetStatus(esportsassessment.StatusFailed).SetSource(esportsassessment.SourceApply).
		SetCreatedAt(now.AddDate(0, 0, -40)).SaveX(ctx)
	if _, err := createAppeal(ctx, db, player.ID, old.ID, "成绩有误", now); err != nil {
		t.Fatalf("发起申诉失败：%v", err)
	}
	// 已过冷却期，但申诉待复核仍须拦截。
	if _, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, now); !errors.Is(err, errInvalidInput) ||
		!strings.Contains(err.Error(), "申诉") {
		t.Fatalf("申诉待复核期间的升级申请未被拦截：%v", err)
	}
	// 申诉复核结束（终态、无待复核申诉）后恢复可申请。
	if _, err := reviewAppeal(ctx, db, old.ID, 11, false, "维持原结论", now); err != nil {
		t.Fatalf("复核申诉失败：%v", err)
	}
	if _, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, now); err != nil {
		t.Fatalf("申诉复核结束后仍不能申请：%v", err)
	}
}

// P0-1 回归（等级回写单调）：旧升级单的申诉被批准时不得把已升上去的等级改回旧目标档。
func TestUpgradeAppealApprovalNeverLowersLevel(t *testing.T) {
	db := openAssessmentDB(t, "esports_upgrade_appeal_no_downgrade")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedUpgradePlayer(t, db, 751, "senior", now.AddDate(0, 0, -90))

	// 旧单目标档是 skilled，陪玩其后已升到 senior。
	old := db.EsportsAssessment.Create().SetTaskNo("AS-UPGRADE-APPEAL-2").SetPlayerID(player.ID).
		SetAssessmentType(esportsassessment.AssessmentTypeUpgrade).SetTargetLevel("skilled").
		SetStatus(esportsassessment.StatusFailed).SetSource(esportsassessment.SourceApply).
		SetCreatedAt(now.AddDate(0, 0, -40)).SaveX(ctx)
	if _, err := createAppeal(ctx, db, player.ID, old.ID, "成绩有误", now); err != nil {
		t.Fatalf("发起申诉失败：%v", err)
	}
	reviewed, err := reviewAppeal(ctx, db, old.ID, 11, true, "改判通过", now)
	if err != nil || reviewed.Status != esportsassessment.StatusPassed {
		t.Fatalf("申诉改判失败：%+v, %v", reviewed, err)
	}
	if got := db.EsportsPlayer.GetX(ctx, player.ID).Level; got != "senior" {
		t.Fatalf("旧申诉改判把等级降回了 %s，期望保持 senior", got)
	}
}

// P0-2 回归（游标推进）：单轮上限被「SQL 命中但 Go 层不达标」的头部陪玩占满时，
// 持久游标必须让下一轮扫到 ID 更大的达标陪玩，否则后排达人被永久饿死。
func TestAutoTriggerUpgradeAssessmentsStarvedTailPlayerGetsAssessment(t *testing.T) {
	db := openAssessmentDB(t, "esports_upgrade_auto_cursor")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	// 3 名 SQL 可命中但不达标的陪玩（无已完成服务段 → Go 层门槛恒 false），ID 都小于尾部达人。
	for i := 0; i < 3; i++ {
		seedUpgradePlayer(t, db, 801+i, "novice", now.AddDate(0, 0, -30))
	}
	tail := seedUpgradeEligiblePlayer(t, db, 899, "novice", now.AddDate(0, 0, -30))

	const cap = 3
	if count, err := autoTriggerUpgradeAssessments(ctx, db, now, cap); err != nil || count != 0 {
		t.Fatalf("第一轮不应建单：%d, %v", count, err)
	}
	count, err := autoTriggerUpgradeAssessments(ctx, db, now, cap)
	if err != nil {
		t.Fatalf("第二轮执行失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("游标未推进，尾部达标陪玩被饿死：本轮建单 %d，期望 1", count)
	}
	if n := db.EsportsAssessment.Query().Where(
		esportsassessment.PlayerIDEQ(tail.ID),
	).CountX(ctx); n != 1 {
		t.Fatalf("尾部达标陪玩未拿到自动升级单：%d 条", n)
	}
	// 已扫到表尾：游标回卷 0，下一轮从头开始。
	cursor, err := db.SystemConfig.Query().Where(
		systemconfig.KeyEQ(upgradeAutoTriggerCursorKeyLiteral),
	).Only(ctx)
	if err != nil {
		t.Fatalf("游标未落库：%v", err)
	}
	if strings.TrimSpace(cursor.Value) != "0" {
		t.Fatalf("扫到表尾后游标未回卷：%q", cursor.Value)
	}
	if count, err := autoTriggerUpgradeAssessments(ctx, db, now, cap); err != nil || count != 0 {
		t.Fatalf("回卷后重复运行不应重复建单：%d, %v", count, err)
	}
}

// P0-1 回归（并发提交）：两个并发自助申请只能留下一条 pending 升级单，后到者幂等拿到既有那条。
//
// 内存 SQLite 的共享缓存会让第二个写事务直接报表锁（"database table is locked"），那是测试环境的
// 表级锁而非产品行为（生产是 MySQL 行级锁 + 唯一索引），因此这里按「客户端重试」处理撞锁那一次；
// 数据库层真正的并发兜底（唯一索引拒绝第二条进行中单）见 TestUpgradeAssessmentActiveSingleIsUnique。
func TestPlayerUpgradeApplyConcurrentSubmissionsKeepSinglePending(t *testing.T) {
	db := openAssessmentDB(t, "esports_upgrade_apply_concurrent")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedUpgradeEligiblePlayer(t, db, 771, "skilled", now.AddDate(0, 0, -30))

	submit := func() (*ent.EsportsAssessment, error) {
		var item *ent.EsportsAssessment
		var err error
		for attempt := 0; attempt < 10; attempt++ {
			item, err = applyPlayerUpgradeAssessment(ctx, db, player.ID, now)
			if err == nil || !strings.Contains(err.Error(), "database table is locked") {
				return item, err
			}
			time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
		}
		return item, err
	}

	var wg sync.WaitGroup
	items := make([]*ent.EsportsAssessment, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range items {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			items[i], errs[i] = submit()
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("第 %d 个并发申请未幂等返回既有单：%v", i+1, errs[i])
		}
	}
	if items[0].ID != items[1].ID {
		t.Fatalf("并发申请返回了两条不同单据：%d vs %d", items[0].ID, items[1].ID)
	}
	if n := countUpgradeAssessments(ctx, db, player.ID); n != 1 {
		t.Fatalf("并发申请留下了 %d 条升级单，期望 1 条", n)
	}
}

func countUpgradeAssessments(ctx context.Context, db *ent.Client, playerID int) int {
	return db.EsportsAssessment.Query().Where(
		esportsassessment.PlayerIDEQ(playerID),
		esportsassessment.AssessmentTypeEQ(esportsassessment.AssessmentTypeUpgrade),
	).CountX(ctx)
}

// P0-1 回归（去重键生命周期）：升级单去重键只应在「进行中」期间存在——
// 评分落终态、申诉待复核重新占键、申诉复核落终态清空，漏掉任一路径都会让该陪玩永久无法再申请。
func TestUpgradeAssessmentDedupKeyLifecycleAcrossTerminalPaths(t *testing.T) {
	db := openAssessmentDB(t, "esports_upgrade_dedup_lifecycle")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedUpgradeEligiblePlayer(t, db, 761, "skilled", now.AddDate(0, 0, -90))

	pending, err := applyPlayerUpgradeAssessment(ctx, db, player.ID, now)
	if err != nil {
		t.Fatalf("自助申请失败：%v", err)
	}
	if pending.ActiveDedupKey == nil || *pending.ActiveDedupKey != upgradeDedupKey(player.ID) {
		t.Fatalf("进行中升级单未占住去重键：%+v", pending.ActiveDedupKey)
	}

	// 路径一：评分落终态（passed）→ 键清空，且不再阻塞下一条升级单。
	if _, err := assignAssessment(ctx, db, pending.ID, 11, nil, now); err != nil {
		t.Fatalf("分配考官失败：%v", err)
	}
	scored, err := scoreAssessment(ctx, db, pending.ID, 11, allDimensionScores(100), "晋级通过", now)
	if err != nil || scored.Status != esportsassessment.StatusPassed {
		t.Fatalf("打分通过失败：%+v, %v", scored, err)
	}
	if got := db.EsportsAssessment.GetX(ctx, pending.ID); got.ActiveDedupKey != nil {
		t.Fatalf("评分落终态后去重键未清空：%q", *got.ActiveDedupKey)
	}
	again, err := createAssessment(ctx, db, 11, assessmentInput{
		PlayerID: player.ID, AssessmentType: "upgrade", TargetLevel: "senior",
	}, now)
	if err != nil {
		t.Fatalf("终态后仍无法再建升级单（去重键未释放）：%v", err)
	}

	// 路径二：申诉待复核 → 重新占键；申诉复核落终态 → 清空。
	if _, err := assignAssessment(ctx, db, again.ID, 11, nil, now); err != nil {
		t.Fatalf("分配考官失败：%v", err)
	}
	failed, err := scoreAssessment(ctx, db, again.ID, 11, allDimensionScores(0), "未达标", now)
	if err != nil || failed.Status != esportsassessment.StatusFailed {
		t.Fatalf("打分不通过失败：%+v, %v", failed, err)
	}
	if got := db.EsportsAssessment.GetX(ctx, again.ID); got.ActiveDedupKey != nil {
		t.Fatalf("评分落终态后去重键未清空：%q", *got.ActiveDedupKey)
	}
	if _, err := createAppeal(ctx, db, player.ID, again.ID, "成绩有误", now); err != nil {
		t.Fatalf("发起申诉失败：%v", err)
	}
	if got := db.EsportsAssessment.GetX(ctx, again.ID); got.ActiveDedupKey == nil {
		t.Fatal("申诉待复核未重新占住去重键")
	}
	if _, err := reviewAppeal(ctx, db, again.ID, 11, false, "维持原结论", now); err != nil {
		t.Fatalf("复核申诉失败：%v", err)
	}
	if got := db.EsportsAssessment.GetX(ctx, again.ID); got.ActiveDedupKey != nil {
		t.Fatalf("申诉复核落终态后去重键未清空：%q", *got.ActiveDedupKey)
	}
	if _, err := createAssessment(ctx, db, 11, assessmentInput{
		PlayerID: player.ID, AssessmentType: "upgrade", TargetLevel: "senior",
	}, now); err != nil {
		t.Fatalf("申诉复核结束后去重键仍占用：%v", err)
	}
}

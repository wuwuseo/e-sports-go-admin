package esports

import (
	"errors"
	"testing"
	"time"

	"go-admin/internal/ent/esportsplayeridentity"
	"go-admin/internal/ent/esportsrealnameattempt"
	"go-admin/internal/ent/esportsrealnameverification"
)

func realNameTestNow() time.Time { return time.Now().UTC() }

// 覆盖提交 → 审核 → 重提 → 通过的全流程，以及脱敏与原号不落库。
func TestRealNameManualFlow(t *testing.T) {
	db := newRealNameTestDB(t, "esports_realname_flow")
	defer db.Close()
	ctx := t.Context()
	now := realNameTestNow()
	in := realNameSubmitInput{RealName: "张三", IDNumber: "110101199001011234", Channel: realNameChannelManual, Method: realNameMethodManual}

	view, err := submitRealName(ctx, db, realnameSubjectTypeCustomer, 11, 0, in, now)
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if view.Status != string(esportsrealnameverification.StatusPending) {
		t.Fatalf("状态 %q，期望 pending", view.Status)
	}
	record, err := realNameVerificationFor(ctx, db, realnameSubjectTypeCustomer, 11)
	if err != nil || record == nil {
		t.Fatalf("档案缺失: %v", err)
	}
	if record.IDNumberMasked != "110101********1234" {
		t.Fatalf("脱敏值 %q", record.IDNumberMasked)
	}
	// 全库任何字段都不得出现原始证件号。
	raw, err := db.EsportsRealNameVerification.Get(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw.IDNumberMasked == in.IDNumber {
		t.Fatal("原始证件号被落库")
	}
	if _, err := submitRealName(ctx, db, realnameSubjectTypeCustomer, 11, 0, in, now); !errors.Is(err, errConflict) {
		t.Fatalf("待审重复提交未被拒绝: %v", err)
	}

	if _, err := reviewRealName(ctx, db, 1, record.ID, realNameReviewInput{Approved: false, RejectReason: "证件照片不清晰"}, now); err != nil {
		t.Fatalf("驳回失败: %v", err)
	}
	if _, err := submitRealName(ctx, db, realnameSubjectTypeCustomer, 11, 0, in, now); err != nil {
		t.Fatalf("驳回后重提失败: %v", err)
	}
	if _, err := reviewRealName(ctx, db, 1, record.ID, realNameReviewInput{Approved: true}, now); err != nil {
		t.Fatalf("通过失败: %v", err)
	}
	status, err := realNameStatusFor(ctx, db, realnameSubjectTypeCustomer, 11)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != string(esportsrealnameverification.StatusVerified) {
		t.Fatalf("状态 %q，期望 verified", status.Status)
	}
	if _, err := submitRealName(ctx, db, realnameSubjectTypeCustomer, 11, 0, in, now); !errors.Is(err, errConflict) {
		t.Fatalf("已实名重复提交未被拒绝: %v", err)
	}
}

// 每日提交次数上限：按流水条数统计，达到上限后拒绝新提交。
func TestRealNameDailyAttemptLimit(t *testing.T) {
	db := newRealNameTestDB(t, "esports_realname_limit")
	defer db.Close()
	ctx := t.Context()
	now := realNameTestNow()
	db.SystemConfig.Create().SetKey(realnameDailyAttemptLimitKey).SetName("实名每日上限").SetValue("2").SaveX(ctx)
	in := realNameSubmitInput{RealName: "李四", IDNumber: "110101199001011234", Channel: realNameChannelManual, Method: realNameMethodManual}

	for i := 0; i < 2; i++ {
		if _, err := submitRealName(ctx, db, realnameSubjectTypeCustomer, 12, 0, in, now); err != nil {
			t.Fatalf("第 %d 次提交失败: %v", i+1, err)
		}
		record, err := realNameVerificationFor(ctx, db, realnameSubjectTypeCustomer, 12)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reviewRealName(ctx, db, 1, record.ID, realNameReviewInput{Approved: false, RejectReason: "驳回"}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := submitRealName(ctx, db, realnameSubjectTypeCustomer, 12, 0, in, now); !errors.Is(err, errConflict) {
		t.Fatalf("超限提交未被拒绝: %v", err)
	}
}

// 守卫：开关关闭放行，开启且未实名返回带 scope 的拦截错误。
func TestRealNameGuard(t *testing.T) {
	db := newRealNameTestDB(t, "esports_realname_guard")
	defer db.Close()
	ctx := t.Context()

	if err := ensureRealNameVerified(ctx, db, realnameSubjectTypeCustomer, 21, realNameScopeOrder); err != nil {
		t.Fatalf("默认关闭时应放行: %v", err)
	}
	db.SystemConfig.Create().SetKey(realnameRequiredOrderKey).SetName("下单实名").SetValue("true").SaveX(ctx)
	err := ensureRealNameVerified(ctx, db, realnameSubjectTypeCustomer, 21, realNameScopeOrder)
	var required *realNameRequiredError
	if !errors.As(err, &required) || required.Scope != realNameScopeOrder {
		t.Fatalf("未实名未被拦截: %v", err)
	}
	db.EsportsRealNameVerification.Create().
		SetSubjectType(esportsrealnameverification.SubjectTypeCustomer).SetSubjectID(21).
		SetRealName("王五").SetIDNumberMasked("110101********1234").
		SetChannel(esportsrealnameverification.ChannelManual).SetMethod(esportsrealnameverification.MethodManual).
		SetStatus(esportsrealnameverification.StatusVerified).SaveX(ctx)
	if err := ensureRealNameVerified(ctx, db, realnameSubjectTypeCustomer, 21, realNameScopeOrder); err != nil {
		t.Fatalf("已实名应放行: %v", err)
	}
}

// 超时扫描：pending 置 expired，对应流水置 abandoned。
func TestRealNameExpirePending(t *testing.T) {
	db := newRealNameTestDB(t, "esports_realname_expire")
	defer db.Close()
	ctx := t.Context()
	now := realNameTestNow()

	record := db.EsportsRealNameVerification.Create().
		SetSubjectType(esportsrealnameverification.SubjectTypeCustomer).SetSubjectID(31).
		SetRealName("赵六").SetIDNumberMasked("110101********1234").
		SetChannel(esportsrealnameverification.ChannelThirdParty).SetMethod(esportsrealnameverification.MethodFace).
		SetStatus(esportsrealnameverification.StatusPending).SetProviderRef("face-ref-31").SaveX(ctx)
	db.EsportsRealNameVerification.UpdateOneID(record.ID).SetUpdatedAt(now.Add(-2 * time.Hour)).SaveX(ctx)
	db.EsportsRealNameAttempt.Create().
		SetSubjectType(esportsrealnameattempt.SubjectTypeCustomer).SetSubjectID(31).
		SetChannel(esportsrealnameattempt.ChannelThirdParty).SetMethod(esportsrealnameattempt.MethodFace).
		SetStatus(esportsrealnameattempt.StatusPending).SetProviderRef("face-ref-31").SaveX(ctx)

	count, err := expireRealNamePendingVerifications(ctx, db, now, 10)
	if err != nil || count != 1 {
		t.Fatalf("过期条数 %d, err %v", count, err)
	}
	got, err := db.EsportsRealNameVerification.Get(ctx, record.ID)
	if err != nil || got.Status != esportsrealnameverification.StatusExpired {
		t.Fatalf("档案状态 %v, err %v", got, err)
	}
	attempt, err := db.EsportsRealNameAttempt.Query().
		Where(esportsrealnameattempt.ProviderRefEQ("face-ref-31")).Only(ctx)
	if err != nil || attempt.Status != esportsrealnameattempt.StatusAbandoned {
		t.Fatalf("流水状态 %v, err %v", attempt, err)
	}
	// manual 永不超时。
	manual := db.EsportsRealNameVerification.Create().
		SetSubjectType(esportsrealnameverification.SubjectTypeCustomer).SetSubjectID(32).
		SetRealName("钱七").SetIDNumberMasked("110101********1234").
		SetChannel(esportsrealnameverification.ChannelManual).SetMethod(esportsrealnameverification.MethodManual).
		SetStatus(esportsrealnameverification.StatusPending).SaveX(ctx)
	db.EsportsRealNameVerification.UpdateOneID(manual.ID).SetUpdatedAt(now.Add(-48 * time.Hour)).SaveX(ctx)
	if _, err := expireRealNamePendingVerifications(ctx, db, now, 10); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.EsportsRealNameVerification.Get(ctx, manual.ID); got.Status != esportsrealnameverification.StatusPending {
		t.Fatalf("人工审核不应过期: %v", got.Status)
	}
}

// 回调落定：按 (channel, ref) 幂等更新；未知流水号拒绝。
func TestRealNameSettleAttemptIdempotent(t *testing.T) {
	db := newRealNameTestDB(t, "esports_realname_settle")
	defer db.Close()
	ctx := t.Context()
	now := realNameTestNow()

	record := db.EsportsRealNameVerification.Create().
		SetSubjectType(esportsrealnameverification.SubjectTypeCustomer).SetSubjectID(41).
		SetRealName("孙八").SetIDNumberMasked("110101********1234").
		SetChannel(esportsrealnameverification.ChannelThirdParty).SetMethod(esportsrealnameverification.MethodFace).
		SetStatus(esportsrealnameverification.StatusPending).SetProviderRef("face-ref-41").SaveX(ctx)
	db.EsportsRealNameAttempt.Create().
		SetSubjectType(esportsrealnameattempt.SubjectTypeCustomer).SetSubjectID(41).
		SetChannel(esportsrealnameattempt.ChannelThirdParty).SetMethod(esportsrealnameattempt.MethodFace).
		SetStatus(esportsrealnameattempt.StatusPending).SetProviderRef("face-ref-41").SaveX(ctx)

	if err := settleRealNameAttempt(ctx, db, realNameChannelThirdParty, "face-ref-41", true, "", now); err != nil {
		t.Fatalf("落定失败: %v", err)
	}
	if err := settleRealNameAttempt(ctx, db, realNameChannelThirdParty, "face-ref-41", true, "", now); err != nil {
		t.Fatalf("重复回调应幂等成功: %v", err)
	}
	got, err := db.EsportsRealNameVerification.Get(ctx, record.ID)
	if err != nil || got.Status != esportsrealnameverification.StatusVerified || got.VerifiedAt == nil {
		t.Fatalf("档案状态 %v, err %v", got, err)
	}
	if err := settleRealNameAttempt(ctx, db, realNameChannelThirdParty, "unknown-ref", true, "", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未知流水号未被拒绝: %v", err)
	}
}

// 存量迁移：只迁 verified，重复执行幂等，旧表保持不写。
func TestMigratePlayerIdentityToRealName(t *testing.T) {
	db := newRealNameTestDB(t, "esports_realname_migrate")
	defer db.Close()
	ctx := t.Context()

	db.EsportsPlayerIdentity.Create().SetPlayerID(51).SetRealName("周九").
		SetIDNumberMasked("110101********1234").
		SetStatus(esportsplayeridentity.StatusVerified).SaveX(ctx)
	db.EsportsPlayerIdentity.Create().SetPlayerID(52).SetRealName("吴十").
		SetIDNumberMasked("110101********5678").
		SetStatus(esportsplayeridentity.StatusPendingVerification).SaveX(ctx)

	count, err := MigratePlayerIdentityToRealName(ctx, db)
	if err != nil || count != 1 {
		t.Fatalf("迁移条数 %d, err %v", count, err)
	}
	count, err = MigratePlayerIdentityToRealName(ctx, db)
	if err != nil || count != 0 {
		t.Fatalf("重复迁移条数 %d, err %v", count, err)
	}
	record, err := realNameVerificationFor(ctx, db, realnameSubjectTypePlayer, 51)
	if err != nil || record == nil {
		t.Fatalf("迁移档案缺失: %v", err)
	}
	if record.Channel != esportsrealnameverification.ChannelManual || record.Method != esportsrealnameverification.MethodManual ||
		record.Status != esportsrealnameverification.StatusVerified {
		t.Fatalf("迁移档案口径错误: %+v", record)
	}
	if pending, _ := realNameVerificationFor(ctx, db, realnameSubjectTypePlayer, 52); pending != nil {
		t.Fatal("非 verified 记录不应迁移")
	}
}

package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
)

// seedWecomCorpForTest 写入企微企业主体配置（系统配置键 app.esports.wecom_corp_id）。
func seedWecomCorpForTest(t *testing.T, db *ent.Client, corpID string) {
	t.Helper()
	if err := db.SystemConfig.Create().SetKey(wecomCorpIDKey).SetName("企微企业主体 corpId").
		SetValue(corpID).SetType("string").SetGroup("esports").SetStatus(1).Exec(t.Context()); err != nil {
		t.Fatalf("写入企微 corp_id 配置失败：%v", err)
	}
}

// P1-26：企微上行 corp_id 与系统配置不一致时必须拒绝建档（信任边界），且不落任何客户档案。
func TestWecomIntakeCorpIDMismatchRejected(t *testing.T) {
	db := openCSDB(t, "w4_cs_wecom_corp_mismatch")
	defer db.Close()
	ctx := t.Context()
	seedWecomCorpForTest(t, db, "ww-corp-configured")

	_, created, err := wecomUpsertCustomer(ctx, db, 1, wecomIntakeInput{
		CorpID: "ww-corp-forged", ExternalID: "ext-1", DisplayName: "伪造企业客户",
	}, time.Now())
	if err == nil {
		t.Fatal("corp_id 与配置不一致时应拒绝建档")
	}
	if !errors.Is(err, errForbidden) {
		t.Fatalf("应返回 errForbidden（HTTP 403），实际 %v", err)
	}
	if !strings.Contains(err.Error(), "不一致") || !strings.Contains(err.Error(), wecomCorpIDKey) {
		t.Fatalf("应给出指明配置键的中文提示，实际 %q", err.Error())
	}
	if created {
		t.Fatal("被拒时不得报告 created=true")
	}
	if count, err := db.EsportsCustomer.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("被拒时不得落客户档案，count=%d err=%v", count, err)
	}
}

// P1-26：未配置 corp_id 时按既有降级策略继续建档，但必须明确提示（不得静默）。
func TestWecomIntakeCorpUnconfiguredDegradesWithHint(t *testing.T) {
	db := openCSDB(t, "w4_cs_wecom_corp_unset")
	defer db.Close()
	ctx := t.Context()

	card, created, err := wecomUpsertCustomer(ctx, db, 1, wecomIntakeInput{
		CorpID: "corp-any", ExternalID: "ext-1", DisplayName: "未配置期客户",
	}, time.Now())
	if err != nil {
		t.Fatalf("未配置时应降级建档而非失败：%v", err)
	}
	if !created {
		t.Fatal("未配置时应正常新建客户档案")
	}
	if !strings.Contains(card.Message, "未配置") || !strings.Contains(card.Message, wecomCorpIDKey) {
		t.Fatalf("未配置时应明确提示补齐 corp_id 配置，实际 message=%q", card.Message)
	}
}

// P1-12：同一租户内手机号唯一——同手机号再次企微建档不再新增记录，而是返回既有客户，
// 并补齐企微标识，使后续以「企业主体 + 外部联系人标识」直接幂等命中。
func TestWecomIntakePhoneDedupeReusesCustomer(t *testing.T) {
	db := openCSDB(t, "w4_cs_wecom_phone_dedupe")
	defer db.Close()
	ctx := t.Context()
	seedWecomCorpForTest(t, db, "ww-dedupe")
	now := time.Now()

	existing := db.EsportsCustomer.Create().SetDisplayName("手工建档客户").
		SetOwnerAdminID(1).SetPhone("13500000001").SaveX(ctx)

	card, created, err := wecomUpsertCustomer(ctx, db, 1, wecomIntakeInput{
		CorpID: "ww-dedupe", ExternalID: "ext-new", DisplayName: "企微里的新称呼", Phone: "13500000001",
	}, now)
	if err != nil {
		t.Fatalf("同手机号建档应返回既有客户：%v", err)
	}
	if created {
		t.Fatal("同手机号不得重复建档（created 应为 false）")
	}
	if card.ID != existing.ID {
		t.Fatalf("应命中既有客户 id=%d，got=%d", existing.ID, card.ID)
	}
	if card.MatchedBy != "phone" {
		t.Fatalf("应标记命中口径为 phone，got=%q", card.MatchedBy)
	}
	if !strings.Contains(card.Message, "已存在") || !strings.Contains(card.Message, "未重复建档") {
		t.Fatalf("应给出「已返回既有客户」的中文提示，got=%q", card.Message)
	}
	if count, err := db.EsportsCustomer.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("同手机号只应有一条客户档案，count=%d err=%v", count, err)
	}

	// 企微标识已补齐：第二次同 external_id 直接幂等命中（且仍然是同一条档案）。
	reloaded := db.EsportsCustomer.GetX(ctx, existing.ID)
	if reloaded.WecomCorpID == nil || *reloaded.WecomCorpID != "ww-dedupe" ||
		reloaded.WecomExternalID == nil || *reloaded.WecomExternalID != "ext-new" {
		t.Fatalf("命中既有客户时应补齐企微标识：%+v", reloaded)
	}
	second, created, err := wecomUpsertCustomer(ctx, db, 1, wecomIntakeInput{
		CorpID: "ww-dedupe", ExternalID: "ext-new", DisplayName: "企微里的新称呼", Phone: "13500000001",
	}, now)
	if err != nil || created {
		t.Fatalf("第二次建档应幂等返回既有档案，created=%v err=%v", created, err)
	}
	if second.ID != existing.ID || second.MatchedBy != "wecom" {
		t.Fatalf("第二次应以企微组合幂等命中：id=%d matched_by=%q", second.ID, second.MatchedBy)
	}
}

// P1-12：历史脏数据（同手机号多条档案）不得静默挑一条，必须显式冲突提示。
func TestWecomIntakeDuplicatePhoneConflicts(t *testing.T) {
	db := openCSDB(t, "w4_cs_wecom_phone_conflict")
	defer db.Close()
	ctx := t.Context()
	seedWecomCorpForTest(t, db, "ww-conflict")

	db.EsportsCustomer.Create().SetDisplayName("甲").SetOwnerAdminID(1).SetPhone("13500000009").SaveX(ctx)
	db.EsportsCustomer.Create().SetDisplayName("乙").SetOwnerAdminID(2).SetPhone("13500000009").SaveX(ctx)

	_, _, err := wecomUpsertCustomer(ctx, db, 1, wecomIntakeInput{
		CorpID: "ww-conflict", ExternalID: "ext-9", DisplayName: "重复手机号客户", Phone: "13500000009",
	}, time.Now())
	if !errors.Is(err, errConflict) {
		t.Fatalf("同手机号多条档案应返回冲突，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "合并") {
		t.Fatalf("应提示先合并档案，实际 %q", err.Error())
	}
	if count, err := db.EsportsCustomer.Query().Count(ctx); err != nil || count != 2 {
		t.Fatalf("冲突时不得新增档案，count=%d err=%v", count, err)
	}
}

// P1-12：客户档案改手机号同样受同租户手机号唯一约束；改回自己的号码、留空不受影响。
func TestCustomerProfilePhoneUniqueness(t *testing.T) {
	db := openCSDB(t, "w4_cs_profile_phone_unique")
	defer db.Close()
	ctx := t.Context()

	first := db.EsportsCustomer.Create().SetDisplayName("客户甲").
		SetOwnerAdminID(1).SetPhone("13600000001").SaveX(ctx)
	second := db.EsportsCustomer.Create().SetDisplayName("客户乙").
		SetOwnerAdminID(1).SaveX(ctx)

	_, conflictErr := updateCustomerProfile(ctx, db, 1, false, second.ID, customerProfileInput{
		Phone: "13600000001",
	})
	if !errors.Is(conflictErr, errConflict) {
		t.Fatalf("改成他人已占用的手机号应返回冲突，实际 %v", conflictErr)
	}
	if !strings.Contains(conflictErr.Error(), "已存在客户档案") {
		t.Fatalf("应给出中文冲突提示，实际 %q", conflictErr.Error())
	}
	if got := db.EsportsCustomer.GetX(ctx, second.ID); got.Phone != "" {
		t.Fatalf("冲突请求不得写入手机号，got=%q", got.Phone)
	}

	// 原客户继续保存自己的手机号不受影响。
	if _, err := updateCustomerProfile(ctx, db, 1, false, first.ID, customerProfileInput{
		Phone: "13600000001", ConsumptionPreference: "王者荣耀",
	}); err != nil {
		t.Fatalf("同一客户保存自身手机号应成功：%v", err)
	}
	// 空手机号不参与唯一判定（未采集号码的档案可以有多条）。
	third := db.EsportsCustomer.Create().SetDisplayName("客户丙").SetOwnerAdminID(1).SaveX(ctx)
	if _, err := updateCustomerProfile(ctx, db, 1, false, third.ID, customerProfileInput{
		Phone: "",
	}); err != nil {
		t.Fatalf("空手机号不应触发唯一冲突：%v", err)
	}
}

package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsmemberlevelrule"
)

// 手动调级覆盖期内的等级是权威来源（P1-10）：人工**降级**后成长值再变动、重算也不得反弹。
// 修复前重算按「取最高」处理覆盖记录，人工降级会被下一次重算顶回高等级（只能升不能降）。
func TestMemberManualDowngradeHoldsDuringOverrideWindow(t *testing.T) {
	db := openRound4MemberDB(t, "w4_member_manual_downgrade")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedMemberLevel(t, db, memberLevelInput{Code: "w4-silver", Name: "银卡", GrowthThreshold: 0, DiscountBps: 10000})
	seedMemberLevel(t, db, memberLevelInput{Code: "w4-gold", Name: "金卡", GrowthThreshold: 10000, DiscountBps: 9000})
	customer := saveCustomerOrFail(t, db, 11)

	// 成长值 20000 够金卡。
	db.EsportsGrowthLedger.Create().SetCustomerID(customer.ID).SetDelta(20000).
		SetSourceType(esportsgrowthledger.SourceTypeManual).SetSourceID("w4-down-seed-1").SaveX(ctx)
	if err := recomputeCustomerLevel(ctx, db, customer.ID, now); err != nil {
		t.Fatalf("定级失败：%v", err)
	}
	if got := db.EsportsCustomer.GetX(ctx, customer.ID); got.MemberLevelCode != "w4-gold" {
		t.Fatalf("未按成长值定级：%+v", got)
	}

	// 人工降级到银卡。
	if _, err := setMemberLevelByAdmin(ctx, db, customer.ID, "w4-silver", "客诉协商降级", 11, now); err != nil {
		t.Fatalf("人工降级失败：%v", err)
	}
	// 成长值继续增长后重算：不得把人工降级顶回金卡。
	db.EsportsGrowthLedger.Create().SetCustomerID(customer.ID).SetDelta(5000).
		SetSourceType(esportsgrowthledger.SourceTypeManual).SetSourceID("w4-down-seed-2").SaveX(ctx)
	if err := recomputeCustomerLevel(ctx, db, customer.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("重算失败：%v", err)
	}
	got := db.EsportsCustomer.GetX(ctx, customer.ID)
	if got.MemberLevelCode != "w4-silver" {
		t.Fatalf("人工降级被成长值重算顶回：%s（期望 w4-silver）", got.MemberLevelCode)
	}
	if got.MemberPendingLevelCode != "" || got.MemberDowngradeAfter != nil {
		t.Fatalf("覆盖期内不应登记待降级：%+v", got)
	}

	// 会员详情视图要能说明「覆盖仍在生效」，便于客服解释等级来源。
	view, err := memberViewAt(ctx, db, customer.ID, 20, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("会员详情失败：%v", err)
	}
	if view.LevelCode != "w4-silver" || !view.OverrideActive || view.OverrideLevelCode != "w4-silver" {
		t.Fatalf("覆盖状态展示不正确：%+v", view)
	}
	if view.OverrideExpiresAt == nil {
		t.Fatalf("覆盖到期时间应可见：%+v", view)
	}
}

// 覆盖期到期回落（P1-10）：批量回落任务把停留在人工等级上的客户拉回成长值口径；
// 覆盖期内调用不产生任何改动。
func TestMemberOverrideExpiryFallsBackToGrowth(t *testing.T) {
	db := openRound4MemberDB(t, "w4_member_override_expiry")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	// 覆盖期 2 天，便于用注入时刻跨越边界。
	db.SystemConfig.Create().SetKey(memberOverrideGuardKey).SetName("手动调级覆盖期（天）").
		SetValue("2").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	seedMemberLevel(t, db, memberLevelInput{Code: "w4e-silver", Name: "银卡", GrowthThreshold: 0, DiscountBps: 10000})
	seedMemberLevel(t, db, memberLevelInput{Code: "w4e-gold", Name: "金卡", GrowthThreshold: 10000, DiscountBps: 9000})
	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsGrowthLedger.Create().SetCustomerID(customer.ID).SetDelta(20000).
		SetSourceType(esportsgrowthledger.SourceTypeManual).SetSourceID("w4-expiry-seed-1").SaveX(ctx)
	if err := recomputeCustomerLevel(ctx, db, customer.ID, now); err != nil {
		t.Fatalf("定级失败：%v", err)
	}
	if _, err := setMemberLevelByAdmin(ctx, db, customer.ID, "w4e-silver", "临时降级观察", 11, now); err != nil {
		t.Fatalf("人工降级失败：%v", err)
	}

	// 覆盖期内（第 1 天）：批量回落不做改动。
	if count, err := applyMemberOverrideExpiries(ctx, db, now.AddDate(0, 0, 1), 100); err != nil || count != 0 {
		t.Fatalf("覆盖期内不应回落，count=%d err=%v", count, err)
	}
	if got := db.EsportsCustomer.GetX(ctx, customer.ID); got.MemberLevelCode != "w4e-silver" {
		t.Fatalf("覆盖期内等级被改动：%+v", got)
	}

	// 覆盖到期（第 3 天）：回落到成长值口径（金卡）。
	count, err := applyMemberOverrideExpiries(ctx, db, now.AddDate(0, 0, 3), 100)
	if err != nil {
		t.Fatalf("覆盖到期回落失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("覆盖到期应回落 1 个客户，got=%d", count)
	}
	got := db.EsportsCustomer.GetX(ctx, customer.ID)
	if got.MemberLevelCode != "w4e-gold" {
		t.Fatalf("覆盖到期未回落到成长值口径：%s（期望 w4e-gold）", got.MemberLevelCode)
	}
	// 重复执行幂等：等级已变，不再计数。
	if count, err := applyMemberOverrideExpiries(ctx, db, now.AddDate(0, 0, 3), 100); err != nil || count != 0 {
		t.Fatalf("重复执行应无改动，count=%d err=%v", count, err)
	}
}

// 读取时惰性判定（P1-10 回落机制的另一半）：即使定时任务从未运行，
// 打开会员详情也会把过期覆盖回落为成长值口径结果。
func TestMemberViewLazilySettlesExpiredOverride(t *testing.T) {
	db := openRound4MemberDB(t, "w4_member_override_lazy")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	db.SystemConfig.Create().SetKey(memberOverrideGuardKey).SetName("手动调级覆盖期（天）").
		SetValue("2").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	seedMemberLevel(t, db, memberLevelInput{Code: "w4l-silver", Name: "银卡", GrowthThreshold: 0, DiscountBps: 10000})
	seedMemberLevel(t, db, memberLevelInput{Code: "w4l-gold", Name: "金卡", GrowthThreshold: 10000, DiscountBps: 9000})
	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsGrowthLedger.Create().SetCustomerID(customer.ID).SetDelta(20000).
		SetSourceType(esportsgrowthledger.SourceTypeManual).SetSourceID("w4-lazy-seed-1").SaveX(ctx)
	if err := recomputeCustomerLevel(ctx, db, customer.ID, now); err != nil {
		t.Fatalf("定级失败：%v", err)
	}
	if _, err := setMemberLevelByAdmin(ctx, db, customer.ID, "w4l-silver", "临时降级观察", 11, now); err != nil {
		t.Fatalf("人工降级失败：%v", err)
	}

	view, err := memberViewAt(ctx, db, customer.ID, 20, now.AddDate(0, 0, 3))
	if err != nil {
		t.Fatalf("会员详情失败：%v", err)
	}
	if view.LevelCode != "w4l-gold" {
		t.Fatalf("读取时应已回落为成长值口径：%+v", view)
	}
	if view.OverrideActive {
		t.Fatalf("覆盖到期后 override_active 应为 false：%+v", view)
	}
	if got := db.EsportsCustomer.GetX(ctx, customer.ID); got.MemberLevelCode != "w4l-gold" {
		t.Fatalf("惰性判定应落库回落结果，got=%s", got.MemberLevelCode)
	}
}

// 会员权益读取入口（P1-11）：memberPrivilegeFlags 返回客户全部权益字段，
// 等级停用后统一降为「未生效」，客户档案卡片真实读取同一份快照。
func TestMemberPrivilegeFlagsExposeBenefitFields(t *testing.T) {
	db := openMemberDB(t, "w4_member_privilege_flags")
	defer db.Close()
	ctx := t.Context()

	owner := db.Admin.Create().SetEmail("w4-privilege@example.com").SetPassword("test").
		SetUID("w4-privilege").SetUsername("w4-privilege").SaveX(ctx)
	level := seedMemberLevel(t, db, memberLevelInput{
		Code: "w4-priv", Name: "黑卡", GrowthThreshold: 0, DiscountBps: 9000, PrepayBonusBps: 200,
		PriorityDispatch: true, DedicatedCs: memberBoolPtr(true), PriorityBooking: memberBoolPtr(true),
		DedicatedChannel: memberBoolPtr(true), OfflineEventPriority: memberBoolPtr(true),
	})
	customer := saveCustomerOrFail(t, db, owner.ID)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode(level.Code).SaveX(ctx)

	flags, err := memberPrivilegeFlags(ctx, db, customer.ID)
	if err != nil {
		t.Fatalf("读取权益失败：%v", err)
	}
	// prepay_bonus_bps 已随储值卡下线（2026-10-20），权益快照不再携带开卡赠送比例。
	if !flags.Active || flags.LevelName != "黑卡" || flags.DiscountBps != 9000 {
		t.Fatalf("权益基础字段不正确：%+v", flags)
	}
	if !flags.DedicatedCs || !flags.PriorityBooking || !flags.PriorityDispatch ||
		!flags.DedicatedChannel || !flags.OfflineEventPriority {
		t.Fatalf("权益开关未全部读出：%+v", flags)
	}

	// 客户档案（客服工作台卡片）读取同一份权益快照。
	profile, err := customerProfileOverview(ctx, db, owner.ID, false, customer.ID)
	if err != nil {
		t.Fatalf("客户档案失败：%v", err)
	}
	privilege, ok := profile["member_privilege"].(*memberPrivilegeView)
	if !ok || privilege == nil {
		t.Fatalf("客户档案应返回会员权益快照：%+v", profile)
	}
	if !privilege.DedicatedChannel || !privilege.OfflineEventPriority {
		t.Fatalf("专属通道 / 线下活动优先权益未读到：%+v", privilege)
	}

	// 等级停用 → 未定级待遇：Active=false 且全部权益关闭（不得在权益页宣称已生效）。
	if _, err := db.EsportsMemberLevelRule.Update().Where(
		esportsmemberlevelrule.CodeEQ(level.Code),
	).SetStatus(esportsmemberlevelrule.StatusDisabled).Save(ctx); err != nil {
		t.Fatal(err)
	}
	disabled, err := memberPrivilegeFlags(ctx, db, customer.ID)
	if err != nil {
		t.Fatalf("停用后读取权益失败：%v", err)
	}
	if disabled.Active || disabled.DedicatedChannel || disabled.OfflineEventPriority || disabled.PriorityBooking {
		t.Fatalf("等级停用后权益应全部关闭：%+v", disabled)
	}
}

// 停用等级不影响人工调级落库判断，但也不再作为覆盖来源；这里只校验未定级客户读取不报错。
func TestMemberPrivilegeFlagsUnratedCustomer(t *testing.T) {
	db := openMemberDB(t, "w4_member_privilege_unrated")
	defer db.Close()
	ctx := t.Context()

	customer := db.EsportsCustomer.Create().SetDisplayName("未定级客户").SaveX(ctx)
	flags, err := memberPrivilegeFlags(ctx, db, customer.ID)
	if err != nil {
		t.Fatalf("未定级客户读取权益不应报错：%v", err)
	}
	if flags.Active || flags.LevelCode != "" || flags.DedicatedChannel {
		t.Fatalf("未定级客户不应有任何生效权益：%+v", flags)
	}
	if _, err := memberPrivilegeFlags(ctx, db, 0); err == nil {
		t.Fatal("非法客户 ID 应返回参数错误")
	}
}

package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsgrowthledger"
)

// 等级权益多来源取最高不叠加（§10.5-04）：开卡赠送在「套餐赠送」与「会员等级比例」
// 两个来源间取高——套餐 1% 而等级 5% 时按 5% 发放，套餐 8% 时按 8%，不叠加。
func TestStoredValueBonusTakesHighestOfPackageAndMemberLevel(t *testing.T) {
	db := openStoredValueDB(t, "r4wm_sv_bonus_highest")
	defer db.Close()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()

	level, err := createMemberLevel(ctx, db, memberLevelInput{
		Code: "R4WM-HIGH", Name: "等级 5%", GrowthThreshold: 0, DiscountBps: 10000,
		PrepayBonusBps: 500, SortOrder: 1,
	})
	if err != nil {
		t.Fatalf("创建等级失败：%v", err)
	}
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode(level.Code).SaveX(ctx)

	lowPkg, err := saveStoredValuePackage(ctx, db, storedValuePackageInput{
		Code: "R4WM-PKG-LOW", Name: "套餐赠送 1%", PrincipalCents: 10000, BonusCents: 100,
	})
	if err != nil {
		t.Fatalf("创建套餐失败：%v", err)
	}
	byLowPkg, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "R4WM-QR-1", PackageID: lowPkg.ID,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "r4wm-sv-bonus-1",
	}, now)
	if err != nil {
		t.Fatalf("选套餐开卡失败：%v", err)
	}
	if byLowPkg.BonusRemainingCents != 500 {
		t.Fatalf("套餐赠送 1%% 低于等级比例 5%% 时应按 5%% 发放：得到 %d，期望 500",
			byLowPkg.BonusRemainingCents)
	}
	if byLowPkg.PackageID == nil || *byLowPkg.PackageID != lowPkg.ID {
		t.Fatalf("按等级比例发放时仍应记录套餐 ID：%+v", byLowPkg)
	}

	highPkg, err := saveStoredValuePackage(ctx, db, storedValuePackageInput{
		Code: "R4WM-PKG-HIGH", Name: "套餐赠送 8%", PrincipalCents: 10000, BonusCents: 800,
	})
	if err != nil {
		t.Fatalf("创建套餐失败：%v", err)
	}
	byHighPkg, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "R4WM-QR-2", PackageID: highPkg.ID,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "r4wm-sv-bonus-2",
	}, now)
	if err != nil {
		t.Fatalf("选套餐开卡失败：%v", err)
	}
	if byHighPkg.BonusRemainingCents != 800 {
		t.Fatalf("套餐赠送高于等级比例时应按套餐发放（不叠加）：得到 %d，期望 800",
			byHighPkg.BonusRemainingCents)
	}
}

// 手动调级建立稳定覆盖期（§10.5-04）：覆盖期内成长值重算不得平掉手动调高的等级，
// 也不得登记待降级；覆盖期（可配，默认 90 天）到期后恢复成长值口径并按保护期降级。
func TestManualMemberLevelOverrideHoldsDuringCoverageWindow(t *testing.T) {
	db := openRound4MemberDB(t, "r4wm_member_override")
	defer db.Close()
	ctx := t.Context()
	// 覆盖记录的 created_at 由实体默认值（真实时钟）写入，故本用例以真实时间推算覆盖期窗口。
	now := time.Now().UTC()

	seedMemberLevel(t, db, memberLevelInput{Code: "silver", Name: "银卡", GrowthThreshold: 0, DiscountBps: 10000})
	seedMemberLevel(t, db, memberLevelInput{Code: "gold", Name: "金卡", GrowthThreshold: 10000, DiscountBps: 9000})
	customer := saveCustomerOrFail(t, db, 11)

	// 成长值 1000 只够银卡。
	db.EsportsGrowthLedger.Create().SetCustomerID(customer.ID).SetDelta(1000).
		SetSourceType(esportsgrowthledger.SourceTypeManual).SetSourceID("r4wm-override-seed-1").SaveX(ctx)
	if err := recomputeCustomerLevel(ctx, db, customer.ID, now); err != nil {
		t.Fatalf("定级失败：%v", err)
	}
	if got := db.EsportsCustomer.GetX(ctx, customer.ID); got.MemberLevelCode != "silver" {
		t.Fatalf("未按成长值定级：%+v", got)
	}

	// 手动调高到金卡，随后成长值再变动一次：重算不得侵蚀该覆盖。
	if _, err := setMemberLevelByAdmin(ctx, db, customer.ID, "gold", "客诉补偿", 11, now); err != nil {
		t.Fatalf("手动调级失败：%v", err)
	}
	db.EsportsGrowthLedger.Create().SetCustomerID(customer.ID).SetDelta(500).
		SetSourceType(esportsgrowthledger.SourceTypeManual).SetSourceID("r4wm-override-seed-2").SaveX(ctx)
	if err := recomputeCustomerLevel(ctx, db, customer.ID, now.Add(time.Hour)); err != nil {
		t.Fatalf("重算失败：%v", err)
	}
	got := db.EsportsCustomer.GetX(ctx, customer.ID)
	if got.MemberLevelCode != "gold" || got.MemberPendingLevelCode != "" || got.MemberDowngradeAfter != nil {
		t.Fatalf("覆盖期内手动调级被成长值重算侵蚀：%+v", got)
	}

	// 覆盖期可配：设为 7 天后，第 8 天重算恢复成长值口径（挂起降级，而非立即降级）。
	db.SystemConfig.Create().SetKey("app.esports.member_override_guard_days").
		SetName("手动调级覆盖期（天）").SetValue("7").SetType("number").
		SetGroup("esports").SetStatus(1).SaveX(ctx)
	if err := recomputeCustomerLevel(ctx, db, customer.ID, now.AddDate(0, 0, 8)); err != nil {
		t.Fatalf("覆盖期到期重算失败：%v", err)
	}
	got = db.EsportsCustomer.GetX(ctx, customer.ID)
	if got.MemberLevelCode != "gold" || got.MemberPendingLevelCode != "silver" || got.MemberDowngradeAfter == nil {
		t.Fatalf("覆盖期到期后应恢复成长值口径并挂起降级：%+v", got)
	}
}

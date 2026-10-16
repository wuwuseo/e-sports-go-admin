package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// 未选套餐且未手填赠送时，开卡按会员等级的赠送比例赠送（§10.4）；手填优先；未定级不赠送。
func TestStoredValueOpenMemberBonusRatio(t *testing.T) {
	db := openStoredValueDB(t, "esports_stored_value_member_bonus")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()

	// 会员等级：赠送比例 10%（1000 基点）。
	level, err := createMemberLevel(ctx, db, memberLevelInput{
		Code: "GOLD", Name: "金卡", GrowthThreshold: 0, DiscountBps: 10000,
		PrepayBonusBps: 1000, SortOrder: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode(level.Code).SaveX(ctx)

	// 未选套餐、未手填赠送：本金 10000 → 赠送 1000。
	view, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-B-1", PrincipalCents: 10000,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-bonus-1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if view.BonusRemainingCents != 1000 || view.PrincipalRemainingCents != 10000 {
		t.Fatalf("会员等级赠送比例未生效：%+v", view)
	}

	// 手填赠送时不被覆盖。
	manual, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-B-2", PrincipalCents: 10000, BonusCents: 500,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-bonus-2",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if manual.BonusRemainingCents != 500 {
		t.Fatalf("手填赠送不应被覆盖：%+v", manual)
	}

	// 未定级客户：无赠送。
	plain := saveCustomerOrFail(t, db, 12)
	noLevel, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: plain.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-B-3", PrincipalCents: 10000,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-bonus-3",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if noLevel.BonusRemainingCents != 0 {
		t.Fatalf("未定级客户不应赠送：%+v", noLevel)
	}
}

// 会员等级比例按整数向下取整，且套餐优先于等级比例（§4.4 / §10.4）。
func TestStoredValueBonusRatioFloorAndPackagePriority(t *testing.T) {
	db := openStoredValueDB(t, "esports_stored_value_bonus_floor")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()

	// 比例 333 基点（3.33%）：本金 1000 → 33（1000*333/10000=33.3 向下取整）。
	level, err := createMemberLevel(ctx, db, memberLevelInput{
		Code: "SILVER", Name: "银卡", GrowthThreshold: 0, DiscountBps: 10000,
		PrepayBonusBps: 333, SortOrder: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode(level.Code).SaveX(ctx)

	view, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-F-1", PrincipalCents: 1000,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-floor-1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if view.BonusRemainingCents != 33 {
		t.Fatalf("赠送比例应按整数向下取整：%+v", view)
	}

	// 选套餐时赠送用套餐值，不受会员等级比例影响。
	pkg, err := saveStoredValuePackage(ctx, db, storedValuePackageInput{
		Code: "PKG-B", Name: "套餐卡", PrincipalCents: 1000, BonusCents: 777, SortOrder: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	byPkg, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-F-2", PackageID: pkg.ID,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-floor-2",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if byPkg.BonusRemainingCents != 777 {
		t.Fatalf("选套餐时赠送应取套餐值：%+v", byPkg)
	}
}

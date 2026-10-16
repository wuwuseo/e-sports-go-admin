package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsstoredvaluepackage"
)

// 套餐 CRUD：code 唯一、编辑乐观锁、上下架、在售过滤（§4.4）。
func TestStoredValuePackageCRUD(t *testing.T) {
	db := openStoredValueDB(t, "esports_stored_value_package_crud")
	ctx := t.Context()

	pkg, err := saveStoredValuePackage(ctx, db, storedValuePackageInput{
		Code: "SV100", Name: "100 元卡", PrincipalCents: 10000, BonusCents: 2000, SortOrder: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Status != esportsstoredvaluepackage.StatusOnSale {
		t.Fatalf("新套餐应上架：%+v", pkg)
	}
	if _, err := saveStoredValuePackage(ctx, db, storedValuePackageInput{
		Code: "SV100", Name: "重复", PrincipalCents: 100, BonusCents: 0,
	}); !errors.Is(err, errConflict) {
		t.Fatalf("重复 code 应冲突，err=%v", err)
	}
	if _, err := saveStoredValuePackage(ctx, db, storedValuePackageInput{
		Code: "SVBAD", Name: "本金非法", PrincipalCents: 0, BonusCents: 0,
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("本金非正应被拒，err=%v", err)
	}

	// 编辑乐观锁：版本不一致冲突。
	if _, err := updateStoredValuePackage(ctx, db, pkg.ID, pkg.Version+1, storedValuePackageInput{
		Name: "改", PrincipalCents: 10000, BonusCents: 2000,
	}); !errors.Is(err, errConflict) {
		t.Fatalf("版本不一致应冲突，err=%v", err)
	}
	updated, err := updateStoredValuePackage(ctx, db, pkg.ID, pkg.Version, storedValuePackageInput{
		Name: "100 元卡（改）", PrincipalCents: 10000, BonusCents: 3000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.BonusCents != 3000 || updated.Version != pkg.Version+1 {
		t.Fatalf("编辑结果不正确：%+v", updated)
	}

	// 上下架 + 在售过滤。
	if _, err := setStoredValuePackageStatus(ctx, db, pkg.ID, "off_sale"); err != nil {
		t.Fatal(err)
	}
	onSale, err := listStoredValuePackages(ctx, db, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(onSale) != 0 {
		t.Fatalf("下架后不应出现在在售列表：%+v", onSale)
	}
	all, err := listStoredValuePackages(ctx, db, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("全量列表应含已下架：%+v", all)
	}
}

// 开卡选套餐：本金 / 赠送由套餐决定并记录套餐 ID；下架套餐被拒（§4.4）。
func TestStoredValueOpenWithPackage(t *testing.T) {
	db := openStoredValueDB(t, "esports_stored_value_package_open")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()

	pkg, err := saveStoredValuePackage(ctx, db, storedValuePackageInput{
		Code: "SV200", Name: "200 元卡", PrincipalCents: 20000, BonusCents: 5000, SortOrder: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	view, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-2026-0002", PackageID: pkg.ID,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-pkg-key-1",
	}, now)
	if err != nil {
		t.Fatalf("选套餐开卡应成功，err=%v", err)
	}
	if view.PrincipalRemainingCents != 20000 || view.BonusRemainingCents != 5000 {
		t.Fatalf("套餐本金 / 赠送未生效：%+v", view)
	}
	if view.PackageID == nil || *view.PackageID != pkg.ID {
		t.Fatalf("卡未记录套餐 ID：%+v", view)
	}

	// 下架后开卡被拒。
	if _, err := setStoredValuePackageStatus(ctx, db, pkg.ID, "off_sale"); err != nil {
		t.Fatal(err)
	}
	if _, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-2026-0003", PackageID: pkg.ID,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-pkg-key-2",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("下架套餐开卡应被拒，err=%v", err)
	}
}

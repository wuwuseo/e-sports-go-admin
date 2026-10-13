package esports

import (
	"context"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsstoredvaluepackage"
)

// storedValuePackageInput 是储值卡套餐的创建 / 编辑入参（§4.4）。code 创建后不可改。
type storedValuePackageInput struct {
	Code           string `json:"code"`
	Name           string `json:"name"`
	PrincipalCents int64  `json:"principal_cents"`
	BonusCents     int64  `json:"bonus_cents"`
	SortOrder      int    `json:"sort_order"`
}

func validateStoredValuePackage(in storedValuePackageInput) error {
	if in.Code == "" || len([]rune(in.Code)) > 64 ||
		in.Name == "" || len([]rune(in.Name)) > 120 ||
		in.PrincipalCents <= 0 || in.BonusCents < 0 || in.SortOrder < 0 {
		return errInvalidInput
	}
	return nil
}

// saveStoredValuePackage 新建套餐：code 唯一（重复返回冲突），初始状态 on_sale。
func saveStoredValuePackage(ctx context.Context, db *ent.Client, in storedValuePackageInput) (*ent.EsportsStoredValuePackage, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	if err := validateStoredValuePackage(in); err != nil {
		return nil, err
	}
	pkg, err := db.EsportsStoredValuePackage.Create().SetCode(in.Code).SetName(in.Name).
		SetPrincipalCents(in.PrincipalCents).SetBonusCents(in.BonusCents).
		SetSortOrder(in.SortOrder).SetStatus(esportsstoredvaluepackage.StatusOnSale).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	return pkg, nil
}

// updateStoredValuePackage 编辑套餐：code 不可改，走 version 乐观锁。
func updateStoredValuePackage(ctx context.Context, db *ent.Client, id, version int, in storedValuePackageInput) (*ent.EsportsStoredValuePackage, error) {
	in.Name = strings.TrimSpace(in.Name)
	if id < 1 || version < 1 || in.Name == "" || len([]rune(in.Name)) > 120 ||
		in.PrincipalCents <= 0 || in.BonusCents < 0 || in.SortOrder < 0 {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsStoredValuePackage.Update().Where(
		esportsstoredvaluepackage.IDEQ(id), esportsstoredvaluepackage.VersionEQ(version),
	).SetName(in.Name).SetPrincipalCents(in.PrincipalCents).SetBonusCents(in.BonusCents).
		SetSortOrder(in.SortOrder).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsStoredValuePackage.Get(ctx, id)
}

// setStoredValuePackageStatus 上下架套餐（§4.4 档位可单独上下架）。
func setStoredValuePackageStatus(ctx context.Context, db *ent.Client, id int, status string) (*ent.EsportsStoredValuePackage, error) {
	if status != "on_sale" && status != "off_sale" {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsStoredValuePackage.Update().Where(esportsstoredvaluepackage.IDEQ(id)).
		SetStatus(esportsstoredvaluepackage.Status(status)).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, &ent.NotFoundError{}
	}
	return db.EsportsStoredValuePackage.Get(ctx, id)
}

// listStoredValuePackages 列出套餐；onlyOnSale 为真时只返回在售档位（开卡下拉用）。
func listStoredValuePackages(ctx context.Context, db *ent.Client, onlyOnSale bool) ([]*ent.EsportsStoredValuePackage, error) {
	q := db.EsportsStoredValuePackage.Query()
	if onlyOnSale {
		q.Where(esportsstoredvaluepackage.StatusEQ(esportsstoredvaluepackage.StatusOnSale))
	}
	return q.Order(ent.Asc(esportsstoredvaluepackage.FieldSortOrder), ent.Asc(esportsstoredvaluepackage.FieldID)).All(ctx)
}

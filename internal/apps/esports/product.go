package esports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go-admin/internal/apps/esports/schematier"
	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsproduct"
	"go-admin/internal/ent/esportsproductcategory"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/esportsspecgroup"
)

// 本文件实现商品父实体（§15.1）与规格组（§15.5）及订单逐席角色快照（§15.5）：
//   - EsportsProduct 聚合一个分类下可挂多个规格 SKU 的展示单元；
//   - EsportsSpecGroup 定义多人规格每个席位的角色 / 可替换条件 / 规格选项；
//   - 报单时把 SKU 关联规格组的 seats 快照进订单 seat_roles，历史单缺快照不追溯。

// productChildSkuInput 是商品同一入口维护的子 SKU 定义。
// ID 为 0 表示新建，大于 0 表示编辑既有子 SKU（须属于当前商品）。
type productChildSkuInput struct {
	ID                     int    `json:"id"`
	Code                   string `json:"code"`
	Title                  string `json:"title"`
	ServiceType            string `json:"service_type"`
	Description            string `json:"description"`
	UnitPriceCents         int64  `json:"unit_price_cents"`
	MinUnitPriceCents      int64  `json:"min_unit_price_cents"`
	AdjustmentApprovalBp   int    `json:"adjustment_approval_bp"`
	ServiceUnits           int64  `json:"service_units"`
	ServiceUnitName        string `json:"service_unit_name"`
	RequiredSeats          int    `json:"required_seats"`
	ServiceDurationMinutes int    `json:"service_duration_minutes"`
	CloseRuleCode          string `json:"close_rule_code"`
	SpecGroupID            *int   `json:"spec_group_id"`
	QuotaTotal             int64  `json:"quota_total"`
	SortOrder              int    `json:"sort_order"`
	Status                 string `json:"status"`
}

type productInput struct {
	Name        string                 `json:"name"`
	CategoryID  *int                   `json:"category_id"`
	Description string                 `json:"description"`
	Status      string                 `json:"status"`
	Skus        []productChildSkuInput `json:"skus"`
}

type specGroupInput struct {
	Code   string                `json:"code"`
	Name   string                `json:"name"`
	Seats  []schematier.SeatSpec `json:"seats"`
	Status string                `json:"status"`
}

// productDetailView 是商品详情返回结构：父商品 + 子 SKU + 被引用的规格组。
type productDetailView struct {
	Product    *ent.EsportsProduct     `json:"product"`
	Skus       []*ent.EsportsSku       `json:"skus"`
	SpecGroups []*ent.EsportsSpecGroup `json:"spec_groups"`
}

// validateSeats 校验规格组席位定义：序号从 0 起、不重复、角色必填。
func validateSeats(seats []schematier.SeatSpec) error {
	seen := map[int]bool{}
	for i := range seats {
		seats[i].Role = strings.TrimSpace(seats[i].Role)
		seats[i].Spec = strings.TrimSpace(seats[i].Spec)
		if seats[i].SeatIndex < 0 || seen[seats[i].SeatIndex] {
			return fmt.Errorf("%w：席位序号须从 0 起且不重复", errInvalidInput)
		}
		seen[seats[i].SeatIndex] = true
		if seats[i].Role == "" {
			return fmt.Errorf("%w：席位角色不能为空", errInvalidInput)
		}
	}
	return nil
}

func validateSpecGroupInput(in specGroupInput) (specGroupInput, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	if in.Code == "" || len(in.Code) > 48 || in.Name == "" || len([]rune(in.Name)) > 120 ||
		(in.Status != "enabled" && in.Status != "disabled") {
		return in, errInvalidInput
	}
	if err := validateSeats(in.Seats); err != nil {
		return in, err
	}
	return in, nil
}

func saveSpecGroup(ctx context.Context, db *ent.Client, in specGroupInput) (*ent.EsportsSpecGroup, error) {
	in, err := validateSpecGroupInput(in)
	if err != nil {
		return nil, err
	}
	return db.EsportsSpecGroup.Create().SetCode(in.Code).SetName(in.Name).
		SetSeats(in.Seats).SetStatus(esportsspecgroup.Status(in.Status)).Save(ctx)
}

func updateSpecGroup(ctx context.Context, db *ent.Client, id int, in specGroupInput) (*ent.EsportsSpecGroup, error) {
	in, err := validateSpecGroupInput(in)
	if err != nil {
		return nil, err
	}
	changed, err := db.EsportsSpecGroup.Update().Where(esportsspecgroup.IDEQ(id)).
		SetName(in.Name).SetSeats(in.Seats).SetStatus(esportsspecgroup.Status(in.Status)).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, &ent.NotFoundError{}
	}
	return db.EsportsSpecGroup.Get(ctx, id)
}

func validateProductInput(in productInput) (productInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" || len([]rune(in.Name)) > 160 || len([]rune(in.Description)) > 500 ||
		(in.Status != "draft" && in.Status != "on_sale" && in.Status != "off_sale") {
		return in, errInvalidInput
	}
	if in.CategoryID != nil && *in.CategoryID < 1 {
		return in, errInvalidInput
	}
	return in, nil
}

// normalizeProductChildSku 归一化并校验单个子 SKU 字段，口径与 saveSku 保持一致。
func normalizeProductChildSku(in productChildSkuInput) (productChildSkuInput, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Title = strings.TrimSpace(in.Title)
	in.ServiceUnitName = strings.TrimSpace(in.ServiceUnitName)
	in.Description = strings.TrimSpace(in.Description)
	if in.MinUnitPriceCents == 0 {
		in.MinUnitPriceCents = in.UnitPriceCents
	}
	if in.AdjustmentApprovalBp == 0 {
		in.AdjustmentApprovalBp = 2000
	}
	if in.Code == "" || len(in.Code) > 80 || in.Title == "" || len([]rune(in.Title)) > 160 ||
		in.ServiceUnitName == "" || len([]rune(in.ServiceUnitName)) > 32 || len([]rune(in.Description)) > 500 ||
		in.UnitPriceCents <= 0 || in.MinUnitPriceCents < 1 || in.MinUnitPriceCents > in.UnitPriceCents ||
		in.AdjustmentApprovalBp < 1 || in.AdjustmentApprovalBp > 10000 || in.ServiceUnits <= 0 ||
		in.RequiredSeats < 1 || in.QuotaTotal < -1 ||
		in.ServiceDurationMinutes < 0 || in.ServiceDurationMinutes > 24*60 ||
		(in.ServiceType != "play" && in.ServiceType != "teaching" && in.ServiceType != "entertainment") ||
		(in.Status != "draft" && in.Status != "on_sale" && in.Status != "off_sale") {
		return in, errInvalidInput
	}
	if in.SpecGroupID != nil && *in.SpecGroupID < 1 {
		return in, errInvalidInput
	}
	return in, nil
}

// applyProductChildSkus 在事务内新建/更新商品子 SKU；引用规格组时校验席位数量与 required_seats 一致。
func applyProductChildSkus(ctx context.Context, tx *ent.Tx, productID int, skus []productChildSkuInput) error {
	for i, raw := range skus {
		in, err := normalizeProductChildSku(raw)
		if err != nil {
			return fmt.Errorf("第 %d 个子 SKU：%w", i+1, err)
		}
		if in.SpecGroupID != nil {
			group, err := tx.EsportsSpecGroup.Get(ctx, *in.SpecGroupID)
			if err != nil {
				return err
			}
			if len(group.Seats) != in.RequiredSeats {
				return fmt.Errorf("%w：第 %d 个子 SKU 规格组席位数量与 required_seats 不一致", errInvalidInput, i+1)
			}
		}
		if in.ID > 0 {
			changed, err := tx.EsportsSku.Update().Where(
				esportssku.IDEQ(in.ID), esportssku.ProductIDEQ(productID),
			).SetTitle(in.Title).SetServiceType(esportssku.ServiceType(in.ServiceType)).
				SetDescription(in.Description).SetUnitPriceCents(in.UnitPriceCents).
				SetMinUnitPriceCents(in.MinUnitPriceCents).SetAdjustmentApprovalBp(in.AdjustmentApprovalBp).
				SetServiceUnits(in.ServiceUnits).SetServiceUnitName(in.ServiceUnitName).
				SetRequiredSeats(in.RequiredSeats).SetServiceDurationMinutes(in.ServiceDurationMinutes).
				SetCloseRuleCode(strings.TrimSpace(in.CloseRuleCode)).SetQuotaTotal(in.QuotaTotal).
				SetSortOrder(in.SortOrder).SetStatus(esportssku.Status(in.Status)).AddVersion(1).Save(ctx)
			if err != nil {
				return err
			}
			if changed != 1 {
				return &ent.NotFoundError{}
			}
			continue
		}
		create := tx.EsportsSku.Create().SetCode(in.Code).SetTitle(in.Title).
			SetServiceType(esportssku.ServiceType(in.ServiceType)).SetDescription(in.Description).
			SetUnitPriceCents(in.UnitPriceCents).SetMinUnitPriceCents(in.MinUnitPriceCents).
			SetAdjustmentApprovalBp(in.AdjustmentApprovalBp).SetServiceUnits(in.ServiceUnits).
			SetServiceUnitName(in.ServiceUnitName).SetRequiredSeats(in.RequiredSeats).
			SetServiceDurationMinutes(in.ServiceDurationMinutes).
			SetCloseRuleCode(strings.TrimSpace(in.CloseRuleCode)).SetQuotaTotal(in.QuotaTotal).
			SetSortOrder(in.SortOrder).SetStatus(esportssku.Status(in.Status)).
			SetProductID(productID)
		if in.SpecGroupID != nil {
			create.SetSpecGroupID(*in.SpecGroupID)
		}
		if _, err := create.Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

func saveProduct(ctx context.Context, db *ent.Client, in productInput) (*ent.EsportsProduct, error) {
	in, err := validateProductInput(in)
	if err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	create := tx.EsportsProduct.Create().SetName(in.Name).SetDescription(in.Description).
		SetStatus(esportsproduct.Status(in.Status))
	if in.CategoryID != nil {
		exists, err := tx.EsportsProductCategory.Query().Where(
			esportsproductcategory.IDEQ(*in.CategoryID),
			esportsproductcategory.StatusEQ(esportsproductcategory.StatusEnabled),
		).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, errInvalidInput
		}
		create.SetCategoryID(*in.CategoryID)
	}
	product, err := create.Save(ctx)
	if err != nil {
		return nil, err
	}
	if err := applyProductChildSkus(ctx, tx, product.ID, in.Skus); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsProduct.Get(ctx, product.ID)
}

func updateProduct(ctx context.Context, db *ent.Client, id int, in productInput) (*ent.EsportsProduct, error) {
	in, err := validateProductInput(in)
	if err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	update := tx.EsportsProduct.Update().Where(esportsproduct.IDEQ(id)).
		SetName(in.Name).SetDescription(in.Description).
		SetStatus(esportsproduct.Status(in.Status)).AddVersion(1)
	if in.CategoryID == nil {
		update.ClearCategoryID()
	} else {
		exists, err := tx.EsportsProductCategory.Query().Where(
			esportsproductcategory.IDEQ(*in.CategoryID),
			esportsproductcategory.StatusEQ(esportsproductcategory.StatusEnabled),
		).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, errInvalidInput
		}
		update.SetCategoryID(*in.CategoryID)
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, &ent.NotFoundError{}
	}
	if err := applyProductChildSkus(ctx, tx, id, in.Skus); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsProduct.Get(ctx, id)
}

// productDetail 返回商品详情：父商品 + 子 SKU + 被引用的规格组（去重）。
func productDetail(ctx context.Context, db *ent.Client, id int) (*productDetailView, error) {
	product, err := db.EsportsProduct.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	skus, err := db.EsportsSku.Query().Where(esportssku.ProductIDEQ(id)).
		Order(ent.Asc(esportssku.FieldSortOrder), ent.Asc(esportssku.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	groupIDs := map[int]bool{}
	for _, s := range skus {
		if s.SpecGroupID != nil {
			groupIDs[*s.SpecGroupID] = true
		}
	}
	groups := []*ent.EsportsSpecGroup{}
	if len(groupIDs) > 0 {
		ids := make([]int, 0, len(groupIDs))
		for gid := range groupIDs {
			ids = append(ids, gid)
		}
		groups, err = db.EsportsSpecGroup.Query().Where(esportsspecgroup.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
	}
	return &productDetailView{Product: product, Skus: skus, SpecGroups: groups}, nil
}

// snapshotSeatRolesForOrder 报单后把订单 SKU 关联规格组的席位定义快照进订单 seat_roles。
// 无规格组的 SKU 不写快照（保持为空）；历史单缺快照不追溯，只在报单时调用一次。
func snapshotSeatRolesForOrder(ctx context.Context, db *ent.Client, orderID int) (*ent.EsportsOrder, error) {
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	sku, err := db.EsportsSku.Get(ctx, order.SkuID)
	if err != nil {
		return nil, err
	}
	if sku.SpecGroupID == nil {
		return order, nil
	}
	group, err := db.EsportsSpecGroup.Get(ctx, *sku.SpecGroupID)
	if err != nil {
		return nil, err
	}
	if _, err := db.EsportsOrder.UpdateOneID(orderID).SetSeatRoles(group.Seats).Save(ctx); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// placeOrderWithSeatRoles 在既有报单流程落单后立即快照逐席角色，作为「报单时快照」的入口。
// 主代理接线时可用它替换 placeOrder，或在 placeOrder 事务内追加等价快照逻辑。
func placeOrderWithSeatRoles(ctx context.Context, db *ent.Client, adminID int, in orderInput, key string, now time.Time) (*ent.EsportsOrder, error) {
	order, err := placeOrder(ctx, db, adminID, in, key, now)
	if err != nil {
		return nil, err
	}
	return snapshotSeatRolesForOrder(ctx, db, order.ID)
}

// orderSeatRoles 读取订单逐席角色快照；历史单无快照时返回空切片（不追溯）。
func orderSeatRoles(ctx context.Context, db *ent.Client, orderID int) ([]schematier.SeatSpec, error) {
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.SeatRoles == nil {
		return []schematier.SeatSpec{}, nil
	}
	return order.SeatRoles, nil
}

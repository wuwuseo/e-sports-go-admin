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
	PerSeatServiceUnits    int64  `json:"per_seat_service_units"`
	CostCents              *int64 `json:"cost_cents"`
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

// specGroupInput 规格组入参（§15.5-01）：per_seat_service_units 为规格组级「每席承诺服务量」，
// 落库时逐席补全 SeatSpec.PromisedUnits，保证「Σ 每席承诺量 = 每席量 × 席位数」。
type specGroupInput struct {
	Code                string                `json:"code"`
	Name                string                `json:"name"`
	Seats               []schematier.SeatSpec `json:"seats"`
	PerSeatServiceUnits int64                 `json:"per_seat_service_units"`
	Status              string                `json:"status"`
}

// productDetailView 是商品详情返回结构：父商品 + 子 SKU + 被引用的规格组。
type productDetailView struct {
	Product    *ent.EsportsProduct     `json:"product"`
	Skus       []*ent.EsportsSku       `json:"skus"`
	SpecGroups []*ent.EsportsSpecGroup `json:"spec_groups"`
}

// validateSeats 校验规格组席位定义：序号从 0 起、不重复、角色必填。
// §15.5-01：逐席承诺服务量以规格组级每席量为准（未配置的席位补全，显式配置但不一致的拒绝），
// 保证下游「Σ 每席承诺量 = 每席量 × 席位数」恒成立。
func validateSeats(seats []schematier.SeatSpec, perSeat int64) error {
	if perSeat < 0 {
		return fmt.Errorf("%w：每席承诺服务量不能为负", errInvalidInput)
	}
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
		if seats[i].PromisedUnits < 0 {
			return fmt.Errorf("%w：每席承诺服务量不能为负", errInvalidInput)
		}
	}
	// 规格组未声明每席量时：各席显式配置必须一致，并据此回填组级每席量。
	if perSeat <= 0 {
		for i := range seats {
			if seats[i].PromisedUnits <= 0 {
				continue
			}
			if perSeat <= 0 {
				perSeat = seats[i].PromisedUnits
				continue
			}
			if seats[i].PromisedUnits != perSeat {
				return fmt.Errorf("%w：各席位每席承诺服务量须一致", errInvalidInput)
			}
		}
		return nil
	}
	for i := range seats {
		if seats[i].PromisedUnits <= 0 {
			seats[i].PromisedUnits = perSeat
			continue
		}
		if seats[i].PromisedUnits != perSeat {
			return fmt.Errorf("%w：席位每席承诺服务量须与规格组一致", errInvalidInput)
		}
	}
	return nil
}

// normalizePerSeatUnits 由席位定义反推组级每席承诺量：组级为 0 时取各席显式值（须一致）。
func normalizePerSeatUnits(seats []schematier.SeatSpec, perSeat int64) int64 {
	if perSeat > 0 {
		return perSeat
	}
	for _, seat := range seats {
		if seat.PromisedUnits > 0 {
			return seat.PromisedUnits
		}
	}
	return perSeat
}

func validateSpecGroupInput(in specGroupInput) (specGroupInput, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	if in.Code == "" || len(in.Code) > 48 || in.Name == "" || len([]rune(in.Name)) > 120 ||
		(in.Status != "enabled" && in.Status != "disabled") {
		return in, errInvalidInput
	}
	if err := validateSeats(in.Seats, in.PerSeatServiceUnits); err != nil {
		return in, err
	}
	in.PerSeatServiceUnits = normalizePerSeatUnits(in.Seats, in.PerSeatServiceUnits)
	// 席位已在 validateSeats 内补全承诺量；组级每席量再统一回填一次，规避值语义的切片别名问题。
	if in.PerSeatServiceUnits > 0 {
		for i := range in.Seats {
			if in.Seats[i].PromisedUnits <= 0 {
				in.Seats[i].PromisedUnits = in.PerSeatServiceUnits
			}
		}
	}
	return in, nil
}

func saveSpecGroup(ctx context.Context, db *ent.Client, in specGroupInput) (*ent.EsportsSpecGroup, error) {
	in, err := validateSpecGroupInput(in)
	if err != nil {
		return nil, err
	}
	return db.EsportsSpecGroup.Create().SetCode(in.Code).SetName(in.Name).
		SetSeats(in.Seats).SetPerSeatServiceUnits(in.PerSeatServiceUnits).
		SetStatus(esportsspecgroup.Status(in.Status)).Save(ctx)
}

func updateSpecGroup(ctx context.Context, db *ent.Client, id int, in specGroupInput) (*ent.EsportsSpecGroup, error) {
	in, err := validateSpecGroupInput(in)
	if err != nil {
		return nil, err
	}
	changed, err := db.EsportsSpecGroup.Update().Where(esportsspecgroup.IDEQ(id)).
		SetName(in.Name).SetSeats(in.Seats).SetPerSeatServiceUnits(in.PerSeatServiceUnits).
		SetStatus(esportsspecgroup.Status(in.Status)).AddVersion(1).Save(ctx)
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
		in.RequiredSeats < 1 || in.QuotaTotal < -1 || in.PerSeatServiceUnits < 0 ||
		in.ServiceDurationMinutes < 0 || in.ServiceDurationMinutes > 24*60 ||
		(in.Status != "draft" && in.Status != "on_sale" && in.Status != "off_sale") {
		return in, errInvalidInput
	}
	if err := validateSkuServiceType(in.ServiceType); err != nil {
		return in, err
	}
	if err := validateSkuCostCents(in.CostCents); err != nil {
		return in, err
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
			// §15.5-01：规格组已声明每席承诺服务量时，子 SKU 未单独配置则跟随规格组，避免同规格两套口径。
			if in.PerSeatServiceUnits <= 0 && group.PerSeatServiceUnits > 0 {
				in.PerSeatServiceUnits = group.PerSeatServiceUnits
			}
			if in.PerSeatServiceUnits > 0 && group.PerSeatServiceUnits > 0 &&
				in.PerSeatServiceUnits != group.PerSeatServiceUnits {
				return fmt.Errorf("%w：第 %d 个子 SKU 每席承诺服务量与规格组不一致", errInvalidInput, i+1)
			}
		}
		if in.ID > 0 {
			update := tx.EsportsSku.Update().Where(
				esportssku.IDEQ(in.ID), esportssku.ProductIDEQ(productID),
			).SetTitle(in.Title).SetServiceType(esportssku.ServiceType(in.ServiceType)).
				SetDescription(in.Description).SetUnitPriceCents(in.UnitPriceCents).
				SetMinUnitPriceCents(in.MinUnitPriceCents).SetAdjustmentApprovalBp(in.AdjustmentApprovalBp).
				SetServiceUnits(in.ServiceUnits).SetServiceUnitName(in.ServiceUnitName).
				SetRequiredSeats(in.RequiredSeats).SetPerSeatServiceUnits(in.PerSeatServiceUnits).
				SetServiceDurationMinutes(in.ServiceDurationMinutes).
				SetCloseRuleCode(strings.TrimSpace(in.CloseRuleCode)).SetQuotaTotal(in.QuotaTotal).
				SetSortOrder(in.SortOrder).SetStatus(esportssku.Status(in.Status)).AddVersion(1)
			if in.CostCents == nil {
				update.ClearCostCents()
			} else {
				update.SetCostCents(*in.CostCents)
			}
			changed, err := update.Save(ctx)
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
			SetPerSeatServiceUnits(in.PerSeatServiceUnits).
			SetServiceDurationMinutes(in.ServiceDurationMinutes).
			SetCloseRuleCode(strings.TrimSpace(in.CloseRuleCode)).SetQuotaTotal(in.QuotaTotal).
			SetSortOrder(in.SortOrder).SetStatus(esportssku.Status(in.Status)).
			SetProductID(productID)
		if in.CostCents != nil {
			create.SetCostCents(*in.CostCents)
		}
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

// resolveSeatPromise 解析下单时的每席承诺服务量与逐席快照（§15.3-01 / §15.5-01 / §5.3-05 / §4.2-08）：
//
//	perSeat    订单落库的「每席承诺服务量」；
//	seatTotal  Σ 每席承诺服务量（订单承诺量 = seatTotal × 份数）；
//	seats      补全 PromisedUnits 后的逐席快照（有规格组时非空）。
//
// 口径：
//   - 有规格组：每席量取规格组（其次 SKU），逐席缺失的补全为每席量，Σ 即每席量 × 席位数；
//   - 无规格组：required_seats > 1 时必须显式配置每席量，否则拒绝报单（不静默落 0）；
//   - 无规格组且单人：未配置每席量时回落整单服务量，保持存量单人单口径不变。
func resolveSeatPromise(sku *ent.EsportsSku, group *ent.EsportsSpecGroup) (int64, int64, []schematier.SeatSpec, error) {
	if sku == nil {
		return 0, 0, nil, errInvalidInput
	}
	seats := []schematier.SeatSpec{}
	if group != nil {
		seats = append(seats, group.Seats...)
	}
	perSeat := int64(0)
	if group != nil && group.PerSeatServiceUnits > 0 {
		perSeat = group.PerSeatServiceUnits
	}
	if perSeat <= 0 {
		perSeat = sku.PerSeatServiceUnits
	}
	if perSeat < 0 {
		return 0, 0, nil, fmt.Errorf("%w：每席承诺服务量不能为负", errInvalidInput)
	}
	if len(seats) > 1 && perSeat <= 0 {
		// 规格组未声明每席量时允许各席自带一致口径；缺失或口径不一的多人规格一律拒绝。
		derived := int64(0)
		for _, seat := range seats {
			if seat.PromisedUnits <= 0 || (derived > 0 && seat.PromisedUnits != derived) {
				return 0, 0, nil, fmt.Errorf(
					"%w：多人规格未配置每席承诺服务量，无法报单", errInvalidInput)
			}
			derived = seat.PromisedUnits
		}
		perSeat = derived
	}
	if len(seats) == 0 {
		if perSeat <= 0 {
			if sku.RequiredSeats > 1 {
				return 0, 0, nil, fmt.Errorf(
					"%w：多人 SKU 未配置每席承诺服务量，无法报单", errInvalidInput)
			}
			if sku.ServiceUnits <= 0 {
				return 0, 0, nil, fmt.Errorf("%w：SKU 未配置服务量，无法报单", errInvalidInput)
			}
			return sku.ServiceUnits, sku.ServiceUnits, seats, nil
		}
		return perSeat, perSeat * int64(sku.RequiredSeats), seats, nil
	}
	for i := range seats {
		if seats[i].PromisedUnits <= 0 {
			seats[i].PromisedUnits = perSeat
		}
	}
	total := int64(0)
	for _, seat := range seats {
		total += seat.PromisedUnits
	}
	if total <= 0 {
		return 0, 0, nil, fmt.Errorf("%w：多人规格未配置每席承诺服务量，无法报单", errInvalidInput)
	}
	if perSeat <= 0 {
		perSeat = total
	}
	return perSeat, total, seats, nil
}

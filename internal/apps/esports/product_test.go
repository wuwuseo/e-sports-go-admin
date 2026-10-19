package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/apps/esports/schematier"
	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsproduct"
	"go-admin/internal/ent/migrate"
)

func openProductDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(t.Context())
	return db
}

// childSku 造一个子 SKU 入参。§15.3-01 起多人 SKU 必须声明「每席承诺服务量」，
// 故 seats>1 时按 每席 1 个单位 × 席位数 配置整单服务量（验收口径：多人单缺每席量直接 400）。
func childSku(code string, price int64, seats int) productChildSkuInput {
	units, perSeat := int64(1), int64(0)
	if seats > 1 {
		units, perSeat = int64(seats), 1
	}
	return productChildSkuInput{
		Code: code, Title: "子规格 " + code, ServiceType: "play",
		UnitPriceCents: price, ServiceUnits: units, ServiceUnitName: "局",
		RequiredSeats: seats, PerSeatServiceUnits: perSeat,
		QuotaTotal: -1, Status: "on_sale",
	}
}

// 商品父实体可同一入口挂多个子 SKU，详情返回父商品 + 子 SKU。
func TestProductWithMultipleSkus(t *testing.T) {
	db := openProductDB(t, "esports_product_skus")
	defer db.Close()
	ctx := t.Context()

	product, err := saveProduct(ctx, db, productInput{
		Name: "王者荣耀双人陪玩", Status: "on_sale",
		Skus: []productChildSkuInput{
			childSku("SKU-A", 1000, 1),
			childSku("SKU-B", 2000, 2),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if product.ID < 1 || product.Status != esportsproduct.StatusOnSale {
		t.Fatalf("父商品未正确落库：%+v", product)
	}

	view, err := productDetail(ctx, db, product.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Product.ID != product.ID || len(view.Skus) != 2 {
		t.Fatalf("详情未返回父商品与子 SKU：%+v", view)
	}
	for _, s := range view.Skus {
		if s.ProductID == nil || *s.ProductID != product.ID {
			t.Fatalf("子 SKU 未挂到父商品：%+v", s)
		}
	}
}

// 规格组定义与引用：席位定义可落库，商品子 SKU 引用规格组后详情可带回规格组。
func TestSpecGroupDefinitionAndReference(t *testing.T) {
	db := openProductDB(t, "esports_spec_group")
	defer db.Close()
	ctx := t.Context()

	seats := []schematier.SeatSpec{
		{SeatIndex: 0, Role: "输出", Substitutable: true, Spec: "星耀以上"},
		{SeatIndex: 1, Role: "辅助", Substitutable: false, Spec: "女性陪玩"},
	}
	group, err := saveSpecGroup(ctx, db, specGroupInput{
		Code: "DUO-ROLE", Name: "双人角色规格", Seats: seats, PerSeatServiceUnits: 1, Status: "enabled",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(group.Seats) != 2 || group.Seats[1].Role != "辅助" {
		t.Fatalf("规格组席位未正确保存：%+v", group.Seats)
	}

	product, err := saveProduct(ctx, db, productInput{
		Name: "双人角色商品", Status: "on_sale",
		Skus: []productChildSkuInput{
			func() productChildSkuInput {
				in := childSku("SKU-DUO", 3000, 2)
				gid := group.ID
				in.SpecGroupID = &gid
				return in
			}(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	view, err := productDetail(ctx, db, product.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.SpecGroups) != 1 || view.SpecGroups[0].ID != group.ID {
		t.Fatalf("详情未带回被引用的规格组：%+v", view.SpecGroups)
	}
	if view.Skus[0].SpecGroupID == nil || *view.Skus[0].SpecGroupID != group.ID {
		t.Fatalf("子 SKU 未引用规格组：%+v", view.Skus[0])
	}
}

// 规格组席位序号重复或角色为空被拒；子 SKU 引用规格组时席位数量须与 required_seats 一致。
func TestSpecGroupValidation(t *testing.T) {
	db := openProductDB(t, "esports_spec_validation")
	defer db.Close()
	ctx := t.Context()

	if _, err := saveSpecGroup(ctx, db, specGroupInput{
		Code: "BAD-DUP", Name: "重复席位",
		Seats: []schematier.SeatSpec{
			{SeatIndex: 0, Role: "输出"},
			{SeatIndex: 0, Role: "辅助"},
		},
		Status: "enabled",
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("重复席位序号应被拒：%v", err)
	}

	group, err := saveSpecGroup(ctx, db, specGroupInput{
		Code: "DUO", Name: "双人", Seats: []schematier.SeatSpec{
			{SeatIndex: 0, Role: "输出"},
			{SeatIndex: 1, Role: "辅助"},
		}, PerSeatServiceUnits: 1, Status: "enabled",
	})
	if err != nil {
		t.Fatal(err)
	}

	// required_seats=3 但规格组只有 2 席 → 拒。
	in := childSku("SKU-MISMATCH", 1000, 3)
	gid := group.ID
	in.SpecGroupID = &gid
	if _, err := saveProduct(ctx, db, productInput{
		Name: "席位不匹配商品", Status: "on_sale", Skus: []productChildSkuInput{in},
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("席位数量不匹配应被拒：%v", err)
	}
}

// 多人规格订单报单后快照逐席角色到订单，并能读回展示；历史单缺快照不追溯。
func TestSpecSeatRolesSnapshot(t *testing.T) {
	db := openProductDB(t, "esports_order_seat_roles")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)

	customer := saveCustomerOrFail(t, db, 11)
	seats := []schematier.SeatSpec{
		{SeatIndex: 0, Role: "输出", Substitutable: true, Spec: "星耀以上"},
		{SeatIndex: 1, Role: "辅助", Substitutable: false, Spec: "女性陪玩"},
		{SeatIndex: 2, Role: "指挥", Substitutable: true, Spec: "语音"},
	}
	group, err := saveSpecGroup(ctx, db, specGroupInput{
		Code: "TRIO", Name: "三人规格", Seats: seats, PerSeatServiceUnits: 1, Status: "enabled",
	})
	if err != nil {
		t.Fatal(err)
	}
	skuIn := childSku("SKU-TRIO", 5000, 3)
	skuIn.SpecGroupID = &group.ID
	if _, err := saveProduct(ctx, db, productInput{
		Name: "三人小队", Status: "on_sale", Skus: []productChildSkuInput{skuIn},
	}); err != nil {
		t.Fatal(err)
	}
	sku := db.EsportsSku.Query().OnlyX(ctx)

	order, err := placeOrderWithSeatRoles(ctx, db, 11,
		orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}, "seat-roles-key", now)
	if err != nil {
		t.Fatal(err)
	}
	if order.SeatRoles == nil || len(order.SeatRoles) != 3 {
		t.Fatalf("订单逐席快照未落库：%+v", order.SeatRoles)
	}
	if order.SeatRoles[1].Role != "辅助" || order.SeatRoles[1].Substitutable {
		t.Fatalf("席位快照内容不符：%+v", order.SeatRoles[1])
	}

	got, err := orderSeatRoles(ctx, db, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].SeatIndex != 0 {
		t.Fatalf("读回逐席快照不符：%+v", got)
	}
}

// 无规格组的普通 SKU 报单不写快照；读取返回空列表，历史单不追溯。
func TestSpecSeatRolesNotBackfilled(t *testing.T) {
	db := openProductDB(t, "esports_order_no_seat_roles")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)

	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "SKU-SOLO", Title: "单排商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrderWithSeatRoles(ctx, db, 11,
		orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}, "no-snapshot-key", now)
	if err != nil {
		t.Fatal(err)
	}
	if order.SeatRoles != nil {
		t.Fatalf("无规格组订单不应有快照：%+v", order.SeatRoles)
	}
	seats, err := orderSeatRoles(ctx, db, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seats) != 0 {
		t.Fatalf("历史单无快照应返回空列表：%+v", seats)
	}
}

// 商品上下架：更新状态到 off_sale 再回到 on_sale，版本递增。
func TestProductStatusToggle(t *testing.T) {
	db := openProductDB(t, "esports_product_status")
	defer db.Close()
	ctx := t.Context()

	product, err := saveProduct(ctx, db, productInput{Name: "上架商品", Status: "on_sale"})
	if err != nil {
		t.Fatal(err)
	}
	off, err := updateProduct(ctx, db, product.ID, productInput{Name: "上架商品", Status: "off_sale"})
	if err != nil {
		t.Fatal(err)
	}
	if off.Status != esportsproduct.StatusOffSale || off.Version != 2 {
		t.Fatalf("下架未生效：%+v", off)
	}
	on, err := updateProduct(ctx, db, product.ID, productInput{Name: "上架商品", Status: "on_sale"})
	if err != nil {
		t.Fatal(err)
	}
	if on.Status != esportsproduct.StatusOnSale || on.Version != 3 {
		t.Fatalf("重新上架未生效：%+v", on)
	}
}

package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/apps/esports/schematier"
	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsmemberlevelrule"
	"go-admin/internal/ent/esportssku"
)

// ── 第 2 波：每席承诺服务量 / 快照口径（15.3-01、15.5-01、5.3-05、4.2-08、15.6-03、10.4-08、3.1-04）──

// po2SeedSku 造一个在售 SKU，可指定每席承诺服务量。
func po2SeedSku(t *testing.T, db *ent.Client, code string, seats int, units, perSeat int64) *ent.EsportsSku {
	t.Helper()
	sku, err := saveSku(t.Context(), db, skuInput{
		Code: code, Title: "商品 " + code, ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: units, ServiceUnitName: "局", RequiredSeats: seats,
		PerSeatServiceUnits: perSeat, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	return sku
}

// §15.3-01 / §15.6-03 / §4.2-08：多人 SKU 每席承诺量落库、缺每席量下单 400、收款前复核口径。
func TestRound4W2MultiSeatPerSeatPromise(t *testing.T) {
	db := openProductDB(t, "esports_po2_seat_promise")
	defer db.Close()
	poSeedBaseConfigs(t, db)
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)

	sku := po2SeedSku(t, db, "PO2-TRIO", 3, 6, 2)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"po2-seat-key-001", now)
	if err != nil {
		t.Fatal(err)
	}
	if order.PromisedServiceUnits != 6 || order.PerSeatServiceUnits != 2 {
		t.Fatalf("多人单承诺量口径不符：promised=%d per_seat=%d",
			order.PromisedServiceUnits, order.PerSeatServiceUnits)
	}
	if order.SkuCode != "PO2-TRIO" || order.SkuName != "商品 PO2-TRIO" {
		t.Fatalf("商品快照未落库：code=%q name=%q", order.SkuCode, order.SkuName)
	}

	// P1-24：多人 SKU 创建入口已拒绝「缺每席承诺量」，这类缺陷数据只能来自历史存量，
	// 因此这里直接落库模拟存量坏数据，继续验证下单环节的兜底拦截不被削弱。
	bad := db.EsportsSku.Create().SetCode("PO2-TRIO-NONE").SetTitle("商品 PO2-TRIO-NONE").
		SetServiceType(esportssku.ServiceTypePlay).SetUnitPriceCents(1000).
		SetServiceUnits(6).SetServiceUnitName("局").SetRequiredSeats(3).
		SetPerSeatServiceUnits(0).SetQuotaTotal(-1).SetStatus(esportssku.StatusOnSale).SaveX(ctx)
	_, err = placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: bad.ID, Quantity: 1},
		"po2-seat-key-002", now)
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "每席承诺服务量") {
		t.Fatalf("未配置每席量的多人 SKU 应返回参数错误：%v", err)
	}

	// §4.2-08：收款前复核每席量口径。
	db.EsportsOrder.UpdateOneID(order.ID).SetPerSeatServiceUnits(0).SaveX(ctx)
	if _, err = startOrderPayment(ctx, db, customer.ID, order.ID, "wechat_work", now); !errors.Is(err, errInvalidInput) ||
		!strings.Contains(err.Error(), "每席承诺服务量") {
		t.Fatalf("缺少每席量时不得发起收款：%v", err)
	}
	db.EsportsOrder.UpdateOneID(order.ID).SetPerSeatServiceUnits(2).SetPromisedServiceUnits(5).SaveX(ctx)
	if _, err = startOrderPayment(ctx, db, customer.ID, order.ID, "wechat_work", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("承诺量与每席量不一致时不得发起收款：%v", err)
	}
	// 口径复原后应继续走到「渠道未开通」，证明口径校验已通过。
	db.EsportsOrder.UpdateOneID(order.ID).SetPromisedServiceUnits(6).SaveX(ctx)
	if _, err = startOrderPayment(ctx, db, customer.ID, order.ID, "wechat_work", now); !errors.Is(err, errPaymentGatewayUnavailable) {
		t.Fatalf("口径正确时应继续到渠道校验：%v", err)
	}
}

// §15.5-01 / §5.3-05：规格组级每席承诺量随席位快照进订单，逐席合计等于订单承诺量。
func TestRound4W2SpecGroupPerSeatPromise(t *testing.T) {
	db := openProductDB(t, "esports_po2_specgroup_promise")
	defer db.Close()
	poSeedBaseConfigs(t, db)
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)

	group, err := saveSpecGroup(ctx, db, specGroupInput{
		Code: "PO2-TRIO-G", Name: "PO2 三人规格", PerSeatServiceUnits: 2,
		Seats: []schematier.SeatSpec{
			{SeatIndex: 0, Role: "输出"}, {SeatIndex: 1, Role: "辅助"}, {SeatIndex: 2, Role: "指挥"},
		},
		Status: "enabled",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, seat := range group.Seats {
		if seat.PromisedUnits != 2 {
			t.Fatalf("席位承诺量未按规格组补全：%+v", group.Seats)
		}
	}

	gid := group.ID
	if _, err = saveProduct(ctx, db, productInput{
		Name: "PO2 三人商品", Status: "on_sale",
		Skus: []productChildSkuInput{{
			Code: "PO2-SG-1", Title: "PO2 三人档", ServiceType: "play", UnitPriceCents: 3000,
			ServiceUnits: 6, ServiceUnitName: "局", RequiredSeats: 3, SpecGroupID: &gid,
			PerSeatServiceUnits: 2, QuotaTotal: -1, Status: "on_sale",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	sku := db.EsportsSku.Query().OnlyX(ctx)

	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"po2-sg-key-001", now)
	if err != nil {
		t.Fatal(err)
	}
	if order.PromisedServiceUnits != 6 || order.PerSeatServiceUnits != 2 {
		t.Fatalf("规格组每席量口径不符：promised=%d per_seat=%d",
			order.PromisedServiceUnits, order.PerSeatServiceUnits)
	}
	if order.SpecName != "PO2 三人规格" {
		t.Fatalf("规格名快照未落库：%q", order.SpecName)
	}
	if len(order.SeatRoles) != 3 {
		t.Fatalf("逐席快照未落库：%+v", order.SeatRoles)
	}
	var sum int64
	for _, seat := range order.SeatRoles {
		if seat.PromisedUnits != 2 {
			t.Fatalf("席位快照承诺量不正确：%+v", seat)
		}
		sum += seat.PromisedUnits
	}
	if sum*order.Quantity != order.PromisedServiceUnits {
		t.Fatalf("逐席承诺量合计 %d 与订单承诺量 %d 不一致", sum, order.PromisedServiceUnits)
	}
	seats, err := orderSeatRoles(ctx, db, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seats) != 3 || seats[2].PromisedUnits != 2 {
		t.Fatalf("seat-roles 读回不正确：%+v", seats)
	}

	// 规格组每席量为 0 且席位也未配置时：多人单必须拒绝建单。
	// P1-24 起「多人 SKU 无每席量」在创建/更新入口即被拒，这里直接落库模拟存量坏数据。
	noneGroup, err := saveSpecGroup(ctx, db, specGroupInput{
		Code: "PO2-TRIO-NONE", Name: "PO2 无每席量",
		Seats:  []schematier.SeatSpec{{SeatIndex: 0, Role: "输出"}, {SeatIndex: 1, Role: "辅助"}},
		Status: "enabled",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = saveProduct(ctx, db, productInput{
		Name: "PO2 无每席量商品", Status: "on_sale",
		Skus: []productChildSkuInput{{
			Code: "PO2-SG-NONE", Title: "PO2 无每席量档", ServiceType: "play", UnitPriceCents: 3000,
			ServiceUnits: 2, ServiceUnitName: "局", RequiredSeats: 2, SpecGroupID: &noneGroup.ID,
			QuotaTotal: -1, Status: "on_sale",
		}},
	}); !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "每席承诺服务量") {
		t.Fatalf("未配置每席量的多人子 SKU 应在创建入口被拒：%v", err)
	}
	badSku := db.EsportsSku.Create().SetCode("PO2-SG-NONE").SetTitle("PO2 无每席量档").
		SetServiceType(esportssku.ServiceTypePlay).SetUnitPriceCents(3000).
		SetServiceUnits(2).SetServiceUnitName("局").SetRequiredSeats(2).
		SetSpecGroupID(noneGroup.ID).SetPerSeatServiceUnits(0).
		SetQuotaTotal(-1).SetStatus(esportssku.StatusOnSale).SaveX(ctx)
	if _, err = placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: badSku.ID, Quantity: 1},
		"po2-sg-key-002", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未配置每席量的多人规格单应被拒：%v", err)
	}
}

// §10.4-08：会员等级与折扣基点在下单时快照，事后改规则不追溯历史单。
// §3.1-04：代练（boosting）保留枚举位但禁止上架 / 下单。
func TestRound4W2MemberDiscountSnapshotAndBoosting(t *testing.T) {
	db := openProductDB(t, "esports_po2_member_boosting")
	defer db.Close()
	poSeedBaseConfigs(t, db)
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	poSeedMemberLevel(t, db, "PO2-GOLD", 8000)
	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("PO2-GOLD").SaveX(ctx)
	customer = db.EsportsCustomer.GetX(ctx, customer.ID)

	sku := po2SeedSku(t, db, "PO2-SOLO", 1, 1, 0)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"po2-member-key-001", now)
	if err != nil {
		t.Fatal(err)
	}
	if order.MemberLevelCode != "PO2-GOLD" || order.MemberDiscountBps != 8000 {
		t.Fatalf("会员折扣快照未落库：code=%q bps=%d", order.MemberLevelCode, order.MemberDiscountBps)
	}
	db.EsportsMemberLevelRule.Update().Where(esportsmemberlevelrule.CodeEQ("PO2-GOLD")).
		SetDiscountBps(9000).SaveX(ctx)
	if got := db.EsportsOrder.GetX(ctx, order.ID).MemberDiscountBps; got != 8000 {
		t.Fatalf("改规则后历史单折扣基点被追溯改写：%d", got)
	}

	if _, err = saveSku(ctx, db, skuInput{
		Code: "PO2-BOOST", Title: "代练商品", ServiceType: "boosting", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	}); !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "代练") {
		t.Fatalf("代练 SKU 应返回「未开放」参数错误：%v", err)
	}
}

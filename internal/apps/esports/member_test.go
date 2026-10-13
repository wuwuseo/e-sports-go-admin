package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsmemberleveloverride"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/migrate"
)

func openMemberDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// seedMemberLevel 建一个启用状态的会员等级。
func seedMemberLevel(t *testing.T, db *ent.Client, in memberLevelInput) *ent.EsportsMemberLevelRule {
	t.Helper()
	item, err := createMemberLevel(t.Context(), db, in)
	if err != nil {
		t.Fatalf("创建会员等级失败：%v", err)
	}
	return item
}

// seedCloseableMemberOrder 建一张挂在指定客户的待结单订单（payable 指定），并挂一条带凭证服务段。
func seedCloseableMemberOrder(t *testing.T, db *ent.Client, customerID int, orderNo string, payable int64) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	order := db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(customerID).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(payable).SetPayableCents(payable).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).
		SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).
		SetSystemSeconds(600).SetEvidenceRef("ev.png").SaveX(ctx)
	return order
}

// 折扣额按基点计算，折后应付向下取整。
func TestMemberDiscountCents(t *testing.T) {
	cases := []struct {
		subtotal int64
		bps      int
		want     int64
	}{
		{10000, 10000, 0},   // 原价
		{10000, 9500, 500},  // 95 折
		{10000, 9000, 1000}, // 9 折
		{12345, 9000, 1235}, // 非整除，向下取整
		{10000, 0, 0},       // 0 视作不打折，避免 100% 折扣
		{0, 9000, 0},        // 零金额
	}
	for _, tc := range cases {
		if got := memberDiscountCents(tc.subtotal, tc.bps); got != tc.want {
			t.Fatalf("折扣计算错误（%d, %d）：得到 %d，期望 %d", tc.subtotal, tc.bps, got, tc.want)
		}
	}
}

// 等级配置校验、code 冲突、版本冲突与启停。
func TestMemberLevelValidationAndStatusFlow(t *testing.T) {
	db := openMemberDB(t, "esports_member_crud")
	defer func() { _ = db.Close() }()
	ctx := t.Context()

	invalid := []memberLevelInput{
		{Code: "", Name: "空编码"},
		{Code: "BAD CODE", Name: "非法字符"},
		{Code: "OK", Name: ""},
		{Code: "OK2", Name: "负门槛", GrowthThreshold: -1},
		{Code: "OK3", Name: "折扣越界", DiscountBps: 10001},
	}
	for _, in := range invalid {
		if _, err := createMemberLevel(ctx, db, in); !errors.Is(err, errInvalidInput) {
			t.Fatalf("非法等级配置未被拦截（%s）：%v", in.Code, err)
		}
	}
	if count := db.EsportsMemberLevelRule.Query().CountX(ctx); count != 0 {
		t.Fatalf("校验失败仍写库：%d 条", count)
	}

	created := seedMemberLevel(t, db, memberLevelInput{Code: "silver", Name: "银卡", GrowthThreshold: 10000, DiscountBps: 9500})
	if _, err := createMemberLevel(ctx, db, memberLevelInput{Code: "silver", Name: "重复"}); !errors.Is(err, errConflict) {
		t.Fatalf("重复 code 未被拦截：%v", err)
	}
	if _, err := updateMemberLevel(ctx, db, created.ID, created.Version+5,
		memberLevelInput{Code: "silver", Name: "改错版本"}); !errors.Is(err, errConflict) {
		t.Fatalf("错误版本更新未被拦截：%v", err)
	}
	updated, err := updateMemberLevel(ctx, db, created.ID, created.Version,
		memberLevelInput{Code: "silver", Name: "银卡会员", GrowthThreshold: 12000, DiscountBps: 9200})
	if err != nil || updated.Name != "银卡会员" || updated.GrowthThreshold != 12000 {
		t.Fatalf("正常更新失败：%v %+v", err, updated)
	}
	disabled, err := setMemberLevelStatus(ctx, db, updated.ID, updated.Version, "disabled")
	if err != nil || disabled.Status.String() != "disabled" {
		t.Fatalf("停用失败：%v %+v", err, disabled)
	}
	if _, err := setMemberLevelStatus(ctx, db, disabled.ID, disabled.Version, "unknown"); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法状态未被拦截：%v", err)
	}
}

// 结单计提成长值：首次定级 → 跨门槛即时升级。
func TestCloseOrderAccruesGrowthAndLevelsUp(t *testing.T) {
	db := openMemberDB(t, "esports_member_accrue")
	defer func() { _ = db.Close() }()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	seedMemberLevel(t, db, memberLevelInput{Code: "silver", Name: "银卡", GrowthThreshold: 10000, DiscountBps: 9500})
	seedMemberLevel(t, db, memberLevelInput{Code: "gold", Name: "金卡", GrowthThreshold: 50000, DiscountBps: 9000})
	customer := saveCustomerOrFail(t, db, 11)

	first := seedCloseableMemberOrder(t, db, customer.ID, "member-close-1", 10000)
	if _, err := closeOrder(ctx, db, first.ID, 11, closeOrderInput{Note: "首次结单"}, now); err != nil {
		t.Fatalf("结单失败：%v", err)
	}
	if growth := customerGrowthTotalX(t, db, customer.ID); growth != 10000 {
		t.Fatalf("成长值计提不正确：%d", growth)
	}
	if got := db.EsportsCustomer.GetX(ctx, customer.ID); got.MemberLevelCode != "silver" {
		t.Fatalf("首次定级不正确：%+v", got)
	}

	second := seedCloseableMemberOrder(t, db, customer.ID, "member-close-2", 40000)
	if _, err := closeOrder(ctx, db, second.ID, 11, closeOrderInput{Note: "二次结单"}, now); err != nil {
		t.Fatalf("二次结单失败：%v", err)
	}
	got := db.EsportsCustomer.GetX(ctx, customer.ID)
	if got.MemberLevelCode != "gold" {
		t.Fatalf("跨门槛未即时升级：%+v", got)
	}
	if got.MemberPendingLevelCode != "" || got.MemberDowngradeAfter != nil {
		t.Fatalf("升级后不应残留待降级标记：%+v", got)
	}
	// 升级通知事件（通知客户与对接客服）。
	if count := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("member.level_upgraded")).CountX(ctx); count != 1 {
		t.Fatalf("缺少等级升级通知事件：%d", count)
	}
}

// 成长值冲回触发降级挂起；保护期到期后由定时任务降级；手动调级留痕。
func TestGrowthReversalDowngradeGuardAndManualOverride(t *testing.T) {
	db := openMemberDB(t, "esports_member_downgrade")
	defer func() { _ = db.Close() }()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	seedMemberLevel(t, db, memberLevelInput{Code: "silver", Name: "银卡", GrowthThreshold: 10000, DiscountBps: 9500})
	seedMemberLevel(t, db, memberLevelInput{Code: "gold", Name: "金卡", GrowthThreshold: 50000, DiscountBps: 9000})
	customer := saveCustomerOrFail(t, db, 11)

	// 直接写成长值流水：10 万 → 金卡。
	db.EsportsGrowthLedger.Create().SetCustomerID(customer.ID).SetDelta(100000).
		SetSourceType(esportsgrowthledger.SourceTypeManual).SetSourceID("seed-up").SaveX(ctx)
	if err := recomputeCustomerLevel(ctx, db, customer.ID, now); err != nil {
		t.Fatalf("定级失败：%v", err)
	}
	if got := db.EsportsCustomer.GetX(ctx, customer.ID); got.MemberLevelCode != "gold" {
		t.Fatalf("未定级到金卡：%+v", got)
	}

	// 冲回 9 万 → 成长值 1 万，跌破金卡门槛，应挂起降级而不立即降级。
	db.EsportsGrowthLedger.Create().SetCustomerID(customer.ID).SetDelta(-90000).
		SetSourceType(esportsgrowthledger.SourceTypeManual).SetSourceID("seed-down").SaveX(ctx)
	if err := recomputeCustomerLevel(ctx, db, customer.ID, now); err != nil {
		t.Fatalf("重算失败：%v", err)
	}
	got := db.EsportsCustomer.GetX(ctx, customer.ID)
	if got.MemberLevelCode != "gold" || got.MemberPendingLevelCode != "silver" || got.MemberDowngradeAfter == nil {
		t.Fatalf("降级挂起不正确：%+v", got)
	}
	if count, err := applyMemberDowngrades(ctx, db, now.Add(29*24*time.Hour), 100); err != nil || count != 0 {
		t.Fatalf("保护期内不应降级：%d %v", count, err)
	}
	if count, err := applyMemberDowngrades(ctx, db, now.Add(31*24*time.Hour), 100); err != nil || count != 1 {
		t.Fatalf("保护期到期未降级：%d %v", count, err)
	}
	got = db.EsportsCustomer.GetX(ctx, customer.ID)
	if got.MemberLevelCode != "silver" || got.MemberPendingLevelCode != "" || got.MemberDowngradeAfter != nil {
		t.Fatalf("降级后状态不正确：%+v", got)
	}
	// 重复运行幂等。
	if count, err := applyMemberDowngrades(ctx, db, now.Add(60*24*time.Hour), 100); err != nil || count != 0 {
		t.Fatalf("重复降级未幂等：%d %v", count, err)
	}

	// 手动调级：写覆盖记录并立即生效，不改成长值流水。
	if _, err := setMemberLevelByAdmin(ctx, db, customer.ID, "gold", "老板特批", 11, now); err != nil {
		t.Fatalf("手动调级失败：%v", err)
	}
	if got = db.EsportsCustomer.GetX(ctx, customer.ID); got.MemberLevelCode != "gold" {
		t.Fatalf("手动调级未生效：%+v", got)
	}
	if growth := customerGrowthTotalX(t, db, customer.ID); growth != 10000 {
		t.Fatalf("手动调级不应改成长值流水：%d", growth)
	}
	if count := db.EsportsMemberLevelOverride.Query().Where(
		esportsmemberleveloverride.CustomerIDEQ(customer.ID)).CountX(ctx); count != 1 {
		t.Fatalf("手动调级未留覆盖记录：%d", count)
	}
	if _, err := setMemberLevelByAdmin(ctx, db, customer.ID, "platinum", "不存在等级", 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("不存在等级未被拦截：%v", err)
	}
}

// 报单按会员等级自动算折扣：优惠顺序 规格价 → 会员折扣，payable 与 discount 同步落库。
func TestPlaceOrderAppliesMemberDiscount(t *testing.T) {
	db := openMemberDB(t, "esports_member_place")
	defer func() { _ = db.Close() }()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	seedMemberLevel(t, db, memberLevelInput{Code: "silver", Name: "银卡", GrowthThreshold: 0, DiscountBps: 9000})
	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("silver").SaveX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GAME-MEMBER", Title: "会员折扣测试商品", ServiceType: "play", UnitPriceCents: 10000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"member-place-key-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if order.DiscountCents != 1000 || order.PayableCents != 9000 || order.UnitPriceCents != 10000 {
		t.Fatalf("会员折扣未正确落库：%+v", order)
	}

	// 未定级客户按原价（折扣 0），既有行为不受影响。
	plain := saveCustomerOrFail(t, db, 11)
	plainOrder, err := placeOrder(ctx, db, 11, orderInput{CustomerID: plain.ID, SkuID: sku.ID, Quantity: 1},
		"member-place-key-2", now)
	if err != nil {
		t.Fatal(err)
	}
	if plainOrder.DiscountCents != 0 || plainOrder.PayableCents != 10000 {
		t.Fatalf("未定级客户不应打折：%+v", plainOrder)
	}
}

func customerGrowthTotalX(t *testing.T, db *ent.Client, customerID int) int64 {
	t.Helper()
	total, err := customerGrowthTotal(t.Context(), db, customerID)
	if err != nil {
		t.Fatalf("读取成长值失败：%v", err)
	}
	return total
}

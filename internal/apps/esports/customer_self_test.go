package esports

import (
	"errors"
	"fmt"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/systemconfig"
)

// 订单分组桶映射：非法桶报参数错误，空桶不过滤。
func TestCustomerOrderBucketStatuses(t *testing.T) {
	if statuses, err := customerOrderBucketStatuses("all"); err != nil || statuses != nil {
		t.Fatalf("all 桶不应过滤：%v %v", err, statuses)
	}
	if _, err := customerOrderBucketStatuses("unknown"); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法桶应报参数错误：%v", err)
	}
	for _, bucket := range []string{"pending_payment", "pending_dispatch", "in_progress",
		"pending_close", "completed", "cancelled"} {
		statuses, err := customerOrderBucketStatuses(bucket)
		if err != nil || len(statuses) == 0 {
			t.Fatalf("%s 桶应有映射：%v %v", bucket, err, statuses)
		}
	}
}

// seedCustomerOrder 造一张归属指定客户的订单。
func seedCustomerOrder(t *testing.T, db *ent.Client, orderNo string, customerID int,
	status esportsorder.FulfillmentStatus) *ent.EsportsOrder {
	t.Helper()
	return db.EsportsOrder.Create().
		SetOrderNo(orderNo).SetCustomerID(customerID).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(status).SaveX(t.Context())
}

// 客户自助数据隔离（§13.2）：只能读到本人订单，跨客户详情返回 403。
func TestCustomerSelfOrderIsolation(t *testing.T) {
	db := openAfterSaleDB(t, "esports_customer_self_isolation")
	defer db.Close()
	ctx := t.Context()

	mine := saveCustomerOrFail(t, db, 11)
	other := db.EsportsCustomer.Create().SetDisplayName("他人客户").SaveX(ctx)
	mineOrder := seedCustomerOrder(t, db, "self-mine", mine.ID, esportsorder.FulfillmentStatusCompleted)
	seedCustomerOrder(t, db, "self-other", other.ID, esportsorder.FulfillmentStatusCompleted)

	items, err := listCustomerOrders(ctx, db, mine.ID, "all", 100)
	if err != nil || len(items) != 1 || items[0].ID != mineOrder.ID {
		t.Fatalf("我的订单应只有本人一张：%v %+v", err, items)
	}
	if _, err = getCustomerOrder(ctx, db, mine.ID, mineOrder.ID); err != nil {
		t.Fatalf("本人订单详情应可读：%v", err)
	}
	otherOrderID := db.EsportsOrder.Query().Where(
		esportsorder.OrderNoEQ("self-other")).OnlyX(ctx).ID
	if _, err = getCustomerOrder(ctx, db, mine.ID, otherOrderID); !errors.Is(err, errForbidden) {
		t.Fatalf("他人订单详情应 403：%v", err)
	}
	// 桶过滤：完成单不应出现在进行中桶里。
	if items, err = listCustomerOrders(ctx, db, mine.ID, "in_progress", 100); err != nil || len(items) != 0 {
		t.Fatalf("进行中桶应为空：%v %d", err, len(items))
	}
}

// 客户自助资料与余额（§10 / §11.3）：等级名与下一档门槛按成长值推导，余额本金 / 赠送分列。
func TestCustomerSelfProfileAndWallet(t *testing.T) {
	db := openAfterSaleDB(t, "esports_customer_self_profile")
	defer db.Close()
	ctx := t.Context()

	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsMemberLevelRule.Create().SetCode("vip1").SetName("VIP1").
		SetGrowthThreshold(1000).SetSortOrder(1).SaveX(ctx)
	db.EsportsMemberLevelRule.Create().SetCode("vip2").SetName("VIP2").
		SetGrowthThreshold(5000).SetSortOrder(2).SaveX(ctx)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("vip1").SaveX(ctx)
	for i, delta := range []int64{900, 300} {
		db.EsportsGrowthLedger.Create().SetCustomerID(customer.ID).SetDelta(delta).
			SetSourceType(esportsgrowthledger.SourceTypeOrder).
			SetSourceID(fmt.Sprintf("self-profile-order-%d", i)).SaveX(ctx)
	}
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 3000, BonusCents: 500, Note: "测试充值"},
		"self-profile-seed"); err != nil {
		t.Fatal(err)
	}
	profile, err := customerSelfProfileOf(ctx, db, db.EsportsCustomer.GetX(ctx, customer.ID))
	if err != nil {
		t.Fatal(err)
	}
	if profile.MemberLevelName != "VIP1" || profile.GrowthPoints != 1200 ||
		profile.NextLevelCode != "vip2" || profile.NextLevelGrowth != 5000 ||
		profile.RiskRestricted {
		t.Fatalf("客户资料不正确：%+v", profile)
	}
	wallet, err := customerSelfWalletOf(ctx, db, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.PrincipalCents != 3000 || wallet.BonusCents != 500 ||
		wallet.TotalCents != 3500 {
		t.Fatalf("客户余额视图不正确：%+v", wallet)
	}
	// 被限制自助下单时资料里只读暴露，便于客户端提示。
	db.EsportsCustomer.UpdateOneID(customer.ID).
		SetRiskStatus(esportscustomer.RiskStatusRestricted).SetRiskReason("频繁恶意退款").SaveX(ctx)
	profile, err = customerSelfProfileOf(ctx, db, db.EsportsCustomer.GetX(ctx, customer.ID))
	if err != nil || !profile.RiskRestricted || profile.RiskReason != "频繁恶意退款" {
		t.Fatalf("风险状态应只读暴露：%v %+v", err, profile)
	}
}

// 订单服务成员视图（§7.5 服务评价前置）：本人订单返回成员与是否已评价，他人订单 403。
func TestCustomerOrderMembers(t *testing.T) {
	db := openAfterSaleDB(t, "esports_customer_order_members")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := saveCustomerOrFail(t, db, 11)
	other := db.EsportsCustomer.Create().SetDisplayName("他人客户").SaveX(ctx)
	order := seedCustomerOrder(t, db, "self-member", customer.ID,
		esportsorder.FulfillmentStatusCompleted)
	player := db.EsportsPlayer.Create().SetUserID(9001).SetDisplayName("陪玩甲").SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(player.ID).
		SetSeatIndex(1).SetStartedAt(now).SetSystemSeconds(3600).SaveX(ctx)

	views, err := listCustomerOrderMembers(ctx, db, customer.ID, order.ID)
	if err != nil || len(views) != 1 {
		t.Fatalf("成员视图应有 1 名：%v %+v", err, views)
	}
	if views[0].PlayerID != player.ID || views[0].DisplayName != "陪玩甲" || views[0].Reviewed {
		t.Fatalf("成员视图不正确：%+v", views[0])
	}
	if _, err = listCustomerOrderMembers(ctx, db, other.ID, order.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("他人订单成员应 403：%v", err)
	}
	if _, err = submitCustomerReview(ctx, db, customer.ID, order.ID, player.ID, 5, "服务很好"); err != nil {
		t.Fatalf("提交评价失败：%v", err)
	}
	views, err = listCustomerOrderMembers(ctx, db, customer.ID, order.ID)
	if err != nil || len(views) != 1 || !views[0].Reviewed {
		t.Fatalf("评价后应标记 reviewed：%v %+v", err, views)
	}
}

// 客户自助发起售后（§13.2）：仅本人订单可发起，渠道固定为 customer 且绑定客户。
func TestCreateCustomerAfterSaleSelfService(t *testing.T) {
	db := openAfterSaleDB(t, "esports_customer_self_aftersale")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := saveCustomerOrFail(t, db, 11)
	other := db.EsportsCustomer.Create().SetDisplayName("他人客户").SaveX(ctx)
	mineOrder := seedCustomerOrder(t, db, "self-as-mine", customer.ID,
		esportsorder.FulfillmentStatusCompleted)
	otherOrder := seedCustomerOrder(t, db, "self-as-other", other.ID,
		esportsorder.FulfillmentStatusCompleted)

	if _, err := createCustomerAfterSale(ctx, db, customer, afterSaleInput{
		SourceID: otherOrder.ID, Kind: "refund_request", Demand: "越权发起",
	}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("他人订单发起售后应 403：%v", err)
	}
	item, err := createCustomerAfterSale(ctx, db, customer, afterSaleInput{
		SourceID: mineOrder.ID, Kind: "duration_short", Channel: "admin",
		Demand: "时长不足，申请退款", ExpectedPlan: "按差额退款", Contact: "wx-123",
	}, now)
	if err != nil {
		t.Fatalf("本人订单应可自助发起售后：%v", err)
	}
	if item.Channel.String() != "customer" || item.CustomerID == nil ||
		*item.CustomerID != customer.ID || item.Status.String() != "pending_accept" {
		t.Fatalf("自助售后单不正确：%+v", item)
	}
	// 我的售后列表应能看到该单。
	items, err := listCustomerAfterSales(ctx, db, customer.ID, 100)
	if err != nil || len(items) != 1 || items[0].ID != item.ID {
		t.Fatalf("我的售后列表不正确：%v %+v", err, items)
	}
}

func TestCustomerAfterSaleApplicationWindow(t *testing.T) {
	db := openAfterSaleDB(t, "esports_customer_aftersale_window")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	customer := saveCustomerOrFail(t, db, 11)
	order := seedCustomerOrder(t, db, "self-as-window", customer.ID,
		esportsorder.FulfillmentStatusCompleted)
	db.EsportsOrder.UpdateOneID(order.ID).SetClosedAt(now.AddDate(0, 0, -8)).SaveX(ctx)
	db.SystemConfig.Create().SetKey(customerAfterSaleWindowDaysKey).
		SetName("客户自助售后申请时限").SetValue("7").SetType("number").
		SetGroup("esports").SetStatus(1).SaveX(ctx)
	in := afterSaleInput{SourceID: order.ID, Kind: "duration_short", Demand: "服务时长不足"}
	if _, err := createCustomerAfterSale(ctx, db, customer, in, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超过配置时限的自助售后应拒绝：%v", err)
	}
	// 关闭自助时限后，旧订单仍可申诉；管理员代客户建单本来就不受此自助窗口限制。
	db.SystemConfig.Update().Where(systemconfig.KeyEQ(customerAfterSaleWindowDaysKey)).SetValue("0").ExecX(ctx)
	if _, err := createCustomerAfterSale(ctx, db, customer, in, now); err != nil {
		t.Fatalf("配置为不限时后应允许发起：%v", err)
	}
}

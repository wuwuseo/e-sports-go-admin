package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsgiftorder"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportswallettx"
)

// 售后跨来源越权回归（§13.1 / §13.3）：构造「订单 ID 与礼物 / 储值卡 / 充值流水 ID 同号」的场景，
// 验证不同来源的售后单按各自来源解析真实归属，而不是一律把 source_id 当订单 ID 查订单归属。
//
// 布局：customerA 归属客服 11，其订单 order.ID 与 customerB 的 gift.ID / card.ID / recharge.ID 同为 1；
// customerB 归属客服 22。若把来源 ID 一律当订单 ID，客服 22 会被误拒绝、客服 11 会被误放行。
func TestAfterSaleCrossSourceOwnership(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_cross_source_owner")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customerA := saveCustomerOrFail(t, db, 11)
	customerB := saveCustomerOrFail(t, db, 22)

	// 订单归属于客服 11（responsible_admin_id=11）。
	order := db.EsportsOrder.Create().SetOrderNo("AC-ORDER-1").SetCustomerID(customerA.ID).
		SetSkuID(1).SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SetResponsibleAdminID(11).SaveX(ctx)

	player := seedPlayer(t, db, 8801, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// 礼物 / 储值卡 / 充值流水均归属 customerB（客服 22），且各自表的首行 ID 与订单同号（=1）。
	gift := db.EsportsGiftOrder.Create().SetGiftNo("AC-GIFT-1").SetGiftID(1).
		SetCustomerID(customerB.ID).SetPlayerID(player.ID).
		SetUnitPriceCents(1000).SetAmountCents(1000).
		SetStatus(esportsgiftorder.StatusPaid).SaveX(ctx)

	card := db.EsportsStoredValueCard.Create().SetCardNo("AC-CARD-1").
		SetCustomerID(customerB.ID).SetPaymentChannel("merchant_qr").
		SetPrincipalCents(10000).SetValidFrom(now).SetValidUntil(now.AddDate(0, 0, 30)).SaveX(ctx)

	recharge := db.EsportsWalletTx.Create().SetTxNo("AC-TX-1").
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(customerB.ID).
		SetEntryType(esportswallettx.EntryTypeRecharge).
		SetPrincipalDeltaCents(10000).SetBonusDeltaCents(0).
		SetSourceType("test").SetSourceID("ac-recharge-1").SaveX(ctx)

	if order.ID != gift.ID || order.ID != card.ID || order.ID != recharge.ID {
		t.Fatalf("前置条件不成立：订单 / 礼物 / 卡 / 充值应同号，got order=%d gift=%d card=%d recharge=%d",
			order.ID, gift.ID, card.ID, recharge.ID)
	}

	// 礼物售后：客服 22（礼物归属）可见可操作，客服 11 不可见。
	giftItem, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "gift", SourceID: gift.ID, Kind: "refund_request", Channel: "customer", Demand: "礼物退款",
	}, 22, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadOwnedAfterSale(ctx, db, 22, false, giftItem.ID); err != nil {
		t.Fatalf("礼物归属客服应可见：%v", err)
	}
	if _, err := loadOwnedAfterSale(ctx, db, 11, false, giftItem.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("非礼物归属客服（订单负责客服）不应被误放行：%v", err)
	}

	// 储值卡售后：同样按卡归属解析。
	cardItem, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "stored_value_card", SourceID: card.ID, Kind: "refund_request", Channel: "customer", Demand: "退卡",
	}, 22, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadOwnedAfterSale(ctx, db, 22, false, cardItem.ID); err != nil {
		t.Fatalf("储值卡归属客服应可见：%v", err)
	}
	if _, err := loadOwnedAfterSale(ctx, db, 11, false, cardItem.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("非储值卡归属客服不应被误放行：%v", err)
	}

	// 充值售后：按充值流水反查客户归属。
	rechargeItem, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "recharge", SourceID: recharge.ID, Kind: "refund_request", Channel: "customer", Demand: "充值退款",
	}, 22, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadOwnedAfterSale(ctx, db, 22, false, rechargeItem.ID); err != nil {
		t.Fatalf("充值归属客服应可见：%v", err)
	}
	if _, err := loadOwnedAfterSale(ctx, db, 11, false, rechargeItem.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("非充值归属客服不应被误放行：%v", err)
	}

	// 列表口径：客服 22 应看到三张非订单售后，看不到客服 11 的订单售后。
	rows22, err := listAfterSales(ctx, db, 22, false, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows22) != 3 {
		t.Fatalf("客服 22 应看到 3 张售后（礼物/卡/充值），got %d", len(rows22))
	}
	orderItem, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "refund_request", Channel: "customer", Demand: "订单退款",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	rows11, err := listAfterSales(ctx, db, 11, false, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows11) != 1 || rows11[0].ID != orderItem.ID {
		t.Fatalf("客服 11 应只看到自己的订单售后，got %+v", rows11)
	}
}

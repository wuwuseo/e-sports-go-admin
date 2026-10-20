package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsgiftorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportswallettx"
)

// seedAfterSaleSourceScene 建一条礼物单、一条客户余额充值流水与一条考核记录，
// 用于验证 §13.1 / §13.3 的统一售后来源扩展。
func seedAfterSaleSourceScene(t *testing.T, db *ent.Client, now time.Time) (
	*ent.EsportsCustomer, *ent.EsportsGiftOrder, *ent.EsportsWalletTx, *ent.EsportsAssessment) {
	t.Helper()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	player := seedPlayer(t, db, 9901, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	gift := db.EsportsGiftOrder.Create().SetGiftNo("GIFT-AS-1").SetGiftID(1).
		SetCustomerID(customer.ID).SetPlayerID(player.ID).
		SetUnitPriceCents(1000).SetAmountCents(1000).
		SetStatus(esportsgiftorder.StatusPaid).SaveX(ctx)

	recharge := db.EsportsWalletTx.Create().SetTxNo("TX-AS-1").
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(customer.ID).
		SetEntryType(esportswallettx.EntryTypeRecharge).
		SetPrincipalDeltaCents(10000).SetBonusDeltaCents(0).
		SetSourceType("test").SetSourceID("as-recharge-1").SaveX(ctx)

	assessment := db.EsportsAssessment.Create().SetTaskNo("AS-TASK-1").
		SetPlayerID(player.ID).SetAssessmentType("retry").SaveX(ctx)

	return customer, gift, recharge, assessment
}

// 礼物 / 充值 / 考核来源都能进入统一售后状态机，且正确绑定归属与写留痕。
func TestAfterSaleSupportsNonOrderSources(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_sources")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	customer, gift, recharge, assessment := seedAfterSaleSourceScene(t, db, now)

	giftItem, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "gift", SourceID: gift.ID, Kind: "refund_request", Channel: "customer",
		Demand: "礼物对应的服务未完成，申请退款", ExpectedPlan: "原路退回",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if giftItem.SourceType != esportsaftersale.SourceTypeGift || giftItem.SourceID != gift.ID {
		t.Fatalf("礼物来源未落库：%+v", giftItem)
	}
	if giftItem.CustomerID == nil || *giftItem.CustomerID != customer.ID {
		t.Fatalf("礼物来源未绑定送礼客户：%+v", giftItem)
	}
	if giftItem.PlayerID == nil || *giftItem.PlayerID != gift.PlayerID {
		t.Fatalf("礼物来源未绑定收礼陪玩：%+v", giftItem)
	}
	// 非订单来源没有订单版本可承载，改写事务外盒留痕。
	if count := db.EsportsOutbox.Query().
		Where(esportsoutbox.EventTypeEQ("aftersale.source_created")).CountX(ctx); count != 1 {
		t.Fatalf("礼物来源售后应写一条外盒留痕，实际 %d", count)
	}

	// 同一来源同一问题重复受理被拦截。
	if _, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "gift", SourceID: gift.ID, Kind: "refund_request", Channel: "admin", Demand: "重复受理",
	}, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复礼物售后未拦截：%v", err)
	}

	// 充值来源：由流水反查客户。
	rechargeItem, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "recharge", SourceID: recharge.ID, Kind: "overcharge", Channel: "customer",
		Demand: "充值金额与到账不一致", ExpectedPlan: "核对后补差",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if rechargeItem.CustomerID == nil || *rechargeItem.CustomerID != customer.ID {
		t.Fatalf("充值来源未反查客户：%+v", rechargeItem)
	}

	// 考核处罚来源：无客户归属，绑定陪玩，问题类型限申诉。
	appealItem, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "assessment", SourceID: assessment.ID, Kind: "player_appeal", Channel: "player",
		Demand: "对考核结论有异议，申请复核", ExpectedPlan: "复核改判",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if appealItem.CustomerID != nil {
		t.Fatalf("考核来源不应绑定客户：%+v", appealItem.CustomerID)
	}
	if appealItem.PlayerID == nil || *appealItem.PlayerID != assessment.PlayerID {
		t.Fatalf("考核来源未绑定考核对象：%+v", appealItem)
	}
}

// 来源与问题类型的非法组合、缺单据与越权自助发起都被拦截。
func TestAfterSaleSourceGuards(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_source_guard")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	customer, gift, _, _ := seedAfterSaleSourceScene(t, db, now)

	// 礼物单提不出「考核申诉」。
	if _, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "gift", SourceID: gift.ID, Kind: "player_appeal", Channel: "customer", Demand: "越界",
	}, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("礼物来源的考核申诉应被拒：%v", err)
	}
	// 来源单据不存在。
	if _, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "gift", SourceID: 999999, Kind: "refund_request", Channel: "customer", Demand: "不存在",
	}, 11, now); !ent.IsNotFound(err) {
		t.Fatalf("来源单据不存在应返回 NotFound：%v", err)
	}
	// 未知来源类型。
	if _, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "coupon", SourceID: gift.ID, Kind: "refund_request", Channel: "customer", Demand: "未知来源",
	}, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未知来源类型应被拒：%v", err)
	}

	// 客户自助：本人礼物可发起。
	item, err := createCustomerAfterSale(ctx, db, customer, afterSaleInput{
		SourceType: "gift", SourceID: gift.ID, Kind: "wrong_item", Channel: "admin",
		Demand: "收到的礼物与实际不符", ExpectedPlan: "原路退回",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if item.Channel.String() != "customer" {
		t.Fatalf("自助发起应固定 customer 渠道：%+v", item.Channel)
	}

	// 他人礼物不可自助发起。
	other := db.EsportsCustomer.Create().SetDisplayName("他人客户").SaveX(ctx)
	if _, err := createCustomerAfterSale(ctx, db, other, afterSaleInput{
		SourceType: "gift", SourceID: gift.ID, Kind: "refund_request", Demand: "越权发起",
	}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("他人礼物发起售后应 403：%v", err)
	}
	// 考核来源不由客户自助发起。
	if _, err := createCustomerAfterSale(ctx, db, customer, afterSaleInput{
		SourceType: "assessment", SourceID: 1, Kind: "player_appeal", Demand: "越权发起",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("考核来源不应可自助发起：%v", err)
	}
}

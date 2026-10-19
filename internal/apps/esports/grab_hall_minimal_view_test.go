package esports

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
)

// P1-06：抢单大厅返回专用最小信息视图——不得含 customer_id、分成 / 提成、渠道成本、
// 退款 / 折扣等资金明细字段，只保留抢单必需的订单号、商品名、分区、席位、档期与价格。
func TestGrabHallMinimalViewHidesSensitiveFields(t *testing.T) {
	db := openPlayerTestDB(t, "esports_grab_hall_view")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	player := seedPlayer(t, db, 7701, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "GH-SKU-1", "抢单分区")
	order := db.EsportsOrder.Create().
		SetOrderNo("GH-ORDER-1").SetCustomerID(4242).SetSkuID(sku.ID).
		SetSkuCode(sku.Code).SetSkuName(sku.Title).
		SetQuantity(1).SetUnitPriceCents(9900).SetPayableCents(9900).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetServiceDurationMinutes(60).SetRequestedStartAt(now.Add(2 * time.Hour)).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusGrabbing).
		// 以下都是必须被挡在最小视图之外的敏感 / 资金明细字段。
		SetChannelCostCents(1234).SetCsCommissionAdminID(7).
		SetManualCommissionRuleID(9).SetRefundedCents(100).
		SetDiscountCents(200).SetManualAdjustCents(300).
		SetRemindNote("内部备注：客户很挑剔").
		SaveX(ctx)

	items, err := listGrabHallItems(ctx, db, player.ID, now)
	if err != nil {
		t.Fatalf("抢单大厅应可读取，err=%v", err)
	}
	if len(items) != 1 || items[0].ID != order.ID {
		t.Fatalf("抢单大厅应返回该可抢订单：%+v", items)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(raw)
	for _, forbidden := range []string{
		"customer_id", "commission", "channel_cost_cents", "refund",
		"discount_cents", "manual_adjust_cents", "remind_note",
	} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("抢单大厅响应不得包含字段 %q：%s", forbidden, payload)
		}
	}
	for _, required := range []string{
		"order_no", "sku_name", "unit_price_cents", "payable_cents",
		"required_seats", "service_duration_minutes", "game",
	} {
		if !strings.Contains(payload, required) {
			t.Fatalf("抢单大厅响应应保留抢单必需字段 %q：%s", required, payload)
		}
	}
	var decoded []map[string]any
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded[0]["customer_id"]; ok {
		t.Fatalf("抢单大厅不得回传客户身份：%+v", decoded[0])
	}
	if game, _ := decoded[0]["game"].(string); game != "抢单分区" {
		t.Fatalf("抢单大厅应带游戏分区，got=%q", game)
	}
}

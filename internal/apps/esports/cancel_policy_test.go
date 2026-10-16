package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsquotahold"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/systemconfig"
)

// setCancelPolicy 写入公示取消政策配置，覆盖已有值。
func setCancelPolicy(t *testing.T, db *ent.Client, freeHours, feeBp int) {
	t.Helper()
	ctx := t.Context()
	for _, item := range []struct{ key, name, value string }{
		{cancelFreeHoursKey, "取消政策免费窗口（小时）", strconv.Itoa(freeHours)},
		{cancelFeeBpKey, "取消政策手续费（基点）", strconv.Itoa(feeBp)},
	} {
		db.SystemConfig.Delete().Where(systemconfig.KeyEQ(item.key)).ExecX(ctx)
		db.SystemConfig.Create().SetKey(item.key).SetName(item.name).SetValue(item.value).
			SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	}
}

// 已付款订单取消：按公示政策收取手续费、退回名额、生成可审批的售后退款单，且必须一次性完成。
func TestPaidOrderCancelChargesPublishedFeeAndCreatesRefundTicket(t *testing.T) {
	db := openAfterSaleDB(t, "esports_cancel_policy_fee")
	defer db.Close()
	ctx := t.Context()
	order := seedBalancePaidOrder(t, db, "cancel-fee")
	// 无免费窗口、手续费 50%：应付 2000 → 手续费 1000、应退 1000。
	setCancelPolicy(t, db, 0, 5000)
	now := order.PaidAt.UTC().Add(3 * time.Hour)

	quote, err := previewCancelOrder(ctx, db, order.ID, 11, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if quote.FreeCancel || quote.FeeCents != 1000 || quote.RefundCents != 1000 || !quote.Cancelable {
		t.Fatalf("取消试算金额不正确：%+v", quote)
	}

	cancelled, err := cancelOrder(ctx, db, order.ID, 11, "客户临时有事", now)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.FulfillmentStatus != esportsorder.FulfillmentStatusCancelled ||
		cancelled.RefundStatus != esportsorder.RefundStatusPendingApproval {
		t.Fatalf("已付款取消未落履约状态与退款状态：%+v", cancelled)
	}
	// 售后单承接审批与执行：来源订单、客服入口、绑定订单客户。
	tickets, err := db.EsportsAfterSale.Query().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 1 || tickets[0].Kind != esportsaftersale.KindRefundRequest ||
		tickets[0].Channel != esportsaftersale.ChannelAdmin ||
		tickets[0].CustomerID == nil || *tickets[0].CustomerID != order.CustomerID {
		t.Fatalf("未生成承接审批的售后退款单：%+v", tickets)
	}
	if tickets[0].Status != esportsaftersale.StatusPendingAccept {
		t.Fatalf("售后单初始状态不正确：%+v", tickets[0])
	}
	// 名额退回：占用记录转 returned，SKU 已售数量归零。
	if hold := db.EsportsQuotaHold.Query().OnlyX(ctx); hold.Status != esportsquotahold.StatusReturned {
		t.Fatalf("取消未退回名额占用：%+v", hold)
	}
	if sku := db.EsportsSku.GetX(ctx, order.SkuID); sku.QuotaSold != 0 {
		t.Fatalf("取消未退回已售名额：%+v", sku)
	}
	// 事件留痕：取消事件带政策快照与售后单号，便于事后审计。
	var cancelledEvent *ent.EsportsOrderEvent
	for _, event := range db.EsportsOrderEvent.Query().AllX(ctx) {
		if event.EventType == "order.cancelled" {
			cancelledEvent = event
		}
	}
	if cancelledEvent == nil || cancelledEvent.ActorID != "11" {
		t.Fatalf("缺少 order.cancelled 事件或操作人：%+v", cancelledEvent)
	}
	if cancelledEvent.Details["after_sale_no"] != tickets[0].AfterSaleNo ||
		cancelledEvent.Details["fee_cents"] == nil {
		t.Fatalf("取消事件未记录政策快照：%+v", cancelledEvent.Details)
	}
	if db.EsportsOutbox.Query().Where().CountX(ctx) == 0 {
		t.Fatal("缺少订单取消外盒事件")
	}
	if _, err := cancelOrder(ctx, db, order.ID, 11, "重复取消", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复取消被放行：%v", err)
	}
}

// 免费窗口内取消不收费，全额退回并同样生成售后单。
func TestPaidOrderCancelWithinFreeWindowRefundsInFull(t *testing.T) {
	db := openAfterSaleDB(t, "esports_cancel_policy_free")
	defer db.Close()
	ctx := t.Context()
	order := seedBalancePaidOrder(t, db, "cancel-free")
	setCancelPolicy(t, db, 2, 5000)
	now := order.PaidAt.UTC().Add(30 * time.Minute)

	quote, err := previewCancelOrder(ctx, db, order.ID, 11, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if !quote.FreeCancel || quote.FeeCents != 0 || quote.RefundCents != 2000 {
		t.Fatalf("免费窗口内试算不正确：%+v", quote)
	}
	cancelled, err := cancelOrder(ctx, db, order.ID, 11, "客户改主意", now)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.RefundStatus != esportsorder.RefundStatusPendingApproval {
		t.Fatalf("全额退款未登记退款状态：%+v", cancelled)
	}
	if count := db.EsportsAfterSale.Query().CountX(ctx); count != 1 {
		t.Fatalf("全额退款未生成售后单：%d", count)
	}
}

// 已开工订单不允许客服直接取消，须走售后；订单状态保持不变。
func TestPaidOrderCancelBlockedAfterServiceStarted(t *testing.T) {
	db := openAfterSaleDB(t, "esports_cancel_policy_started")
	defer db.Close()
	ctx := t.Context()
	order := seedBalancePaidOrder(t, db, "cancel-started")
	setCancelPolicy(t, db, 0, 5000)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusInService).SetStartedAt(time.Now().UTC()).SaveX(ctx)

	if _, err := cancelOrder(ctx, db, order.ID, 11, "客户要求取消", time.Now()); !errors.Is(err, errCancelServiceStarted) {
		t.Fatalf("已开工订单未拦截直接取消：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus == esportsorder.FulfillmentStatusCancelled {
		t.Fatalf("被拒的取消改动了订单：%+v", got)
	}
	quote, err := previewCancelOrder(ctx, db, order.ID, 11, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if quote.Cancelable || quote.BlockedReason == "" {
		t.Fatalf("试算未提示已开工不可取消：%+v", quote)
	}
}

// 政策配置非法（费率超过 10000 基点）时拒绝试算，避免用错误费率算退款。
func TestCancelPolicyRejectsInvalidConfig(t *testing.T) {
	db := openAfterSaleDB(t, "esports_cancel_policy_invalid")
	defer db.Close()
	ctx := t.Context()
	order := seedBalancePaidOrder(t, db, "cancel-invalid")
	setCancelPolicy(t, db, 0, 20000)
	if _, err := previewCancelOrder(ctx, db, order.ID, 11, true, time.Now()); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法取消政策未拦截：%v", err)
	}
}

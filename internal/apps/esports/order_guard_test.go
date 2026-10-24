package esports

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/systemconfig"
)

// 重复报单提醒：同客户在时间窗内已有相同报单时拦截并回带相似订单号，显式确认后放行；窗口外不拦。
func TestOrderGuardDuplicateOrderReminder(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_order_guard_duplicate")
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	db.SystemConfig.Create().SetKey(duplicateOrderWindowKey).SetName("重复报单提醒时间窗").
		SetValue("10").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GUARD-DUP", Title: "重复报单商品", ServiceType: "play", UnitPriceCents: 5000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	// now 由调用方传入（handler 取 time.Now()）：二次报单的时刻晚于首单 created_at，故落在 [now-window, now] 内。
	firstAt := time.Now().UTC()
	base := orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}
	first, err := placeOrder(ctx, db, 11, base, "guard-dup-001", firstAt)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Second)

	// 窗口内二次报单：拒绝，且错误里带相似订单号，同时不落单。
	_, err = placeOrder(ctx, db, 11, base, "guard-dup-002", now)
	if !errors.Is(err, errDuplicateOrder) {
		t.Fatalf("窗口内重复报单未拦截：%v", err)
	}
	if !strings.Contains(err.Error(), first.OrderNo) {
		t.Fatalf("重复报单错误未回带相似订单号 %s：%v", first.OrderNo, err)
	}
	if got := db.EsportsOrder.Query().CountX(ctx); got != 1 {
		t.Fatalf("被拦下的重复报单不应落单，当前订单数 %d", got)
	}

	// 不同数量视为不同报单：避免把同客户连续下单误判为重复（本实现「相似」口径为完全相同的报单）。
	another, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 2,
	}, "guard-dup-002b", now.Add(90*time.Second))
	if err != nil {
		t.Fatalf("同 SKU 不同数量不应判为重复报单：%v", err)
	}
	if another.Quantity != 2 {
		t.Fatalf("数量未被快照：%+v", another)
	}

	// 显式确认（confirm_duplicate=true）后放行，并在事件里留痕。
	confirmed, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, ConfirmDuplicate: true,
	}, "guard-dup-003", now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("确认后应放行：%v", err)
	}
	event, err := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(confirmed.ID),
		esportsorderevent.EventTypeEQ("order.created"),
	).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(event.Details["duplicate_confirmed"]) != "true" {
		t.Fatalf("重复报单确认未留痕：%+v", event.Details)
	}

	// 窗口外（11 分钟 > 10 分钟窗口）不拦截。
	if _, err = placeOrder(ctx, db, 11, base, "guard-dup-004", now.Add(11*time.Minute)); err != nil {
		t.Fatalf("窗口外不应拦截：%v", err)
	}

	// 配置为 0 表示关闭提醒。
	db.SystemConfig.Update().Where(
		systemconfig.KeyEQ(duplicateOrderWindowKey),
	).SetValue("0").SaveX(ctx)
	if _, err = placeOrder(ctx, db, 11, base, "guard-dup-005", now.Add(12*time.Minute)); err != nil {
		t.Fatalf("关闭开关后不应拦截：%v", err)
	}
}

// 客户欠款口径：逾期未付款 / 支付异常计入，未到期待付款与已取消订单不计入。
// 审核报告 partial（部分收款）全仓无写入方，已从欠款口径收敛掉，故不再有用例行。
func TestOrderGuardCustomerDebtCaliber(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_order_guard_debt_caliber")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GUARD-DEBT-C", Title: "欠款口径商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		status   esportsorder.PaymentStatus
		deadline time.Time
		cancel   bool
		want     int64
	}{
		{"逾期未付款计入", esportsorder.PaymentStatusUnpaid, now.Add(-time.Minute), false, 12000},
		{"未到期待付款不计入", esportsorder.PaymentStatusUnpaid, now.Add(time.Hour), false, 0},
		{"支付异常计入", esportsorder.PaymentStatusException, now.Add(time.Hour), false, 12000},
		{"已取消不计入", esportsorder.PaymentStatusUnpaid, now.Add(-time.Minute), true, 0},
	}
	for i, tc := range cases {
		customer := saveCustomerOrFail(t, db, 11)
		create := db.EsportsOrder.Create().SetOrderNo(fmt.Sprintf("debt-caliber-%d", i)).
			SetCustomerID(customer.ID).SetSkuID(sku.ID).SetQuantity(1).
			SetUnitPriceCents(12000).SetPayableCents(12000).SetPromisedServiceUnits(1).
			SetRequiredSeats(1).SetPaymentDeadline(tc.deadline.UTC()).
			SetPaymentStatus(tc.status)
		if tc.cancel {
			create = create.SetFulfillmentStatus(esportsorder.FulfillmentStatusCancelled)
		}
		create.SaveX(ctx)
		debt, err := customerOutstandingDebtCents(ctx, db, customer.ID, now)
		if err != nil {
			t.Fatal(err)
		}
		if debt != tc.want {
			t.Fatalf("%s：欠款应为 %d 分，实际 %d 分", tc.name, tc.want, debt)
		}
	}
}

// 客户欠款拦截：欠款超阈值拒绝报单，阈值内 / 关闭开关放行；被拒的报单不落单、不占名额。
func TestOrderGuardCustomerDebtBlocksNewOrder(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_order_guard_debt_block")
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	// 关闭重复报单提醒，隔离欠款拦截这一项校验。
	db.SystemConfig.Create().SetKey(duplicateOrderWindowKey).SetName("重复报单提醒时间窗").
		SetValue("0").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	debtLimit := db.SystemConfig.Create().SetKey(customerDebtLimitKey).SetName("客户欠款拦截阈值").
		SetValue("10000").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)

	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GUARD-DEBT", Title: "欠款拦截商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// 欠款 12000 分（逾期未付款，未取消）。
	overdue := db.EsportsOrder.Create().SetOrderNo("guard-debt-overdue").
		SetCustomerID(customer.ID).SetSkuID(sku.ID).SetQuantity(1).
		SetUnitPriceCents(12000).SetPayableCents(12000).SetPromisedServiceUnits(1).
		SetRequiredSeats(1).SetPaymentDeadline(now.Add(-time.Minute)).SaveX(ctx)

	in := orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}
	if _, err = placeOrder(ctx, db, 11, in, "guard-debt-001", now); !errors.Is(err, errCustomerDebt) {
		t.Fatalf("欠款超阈值未拦截：%v", err)
	}
	if got := db.EsportsOrder.Query().CountX(ctx); got != 1 {
		t.Fatalf("被拦下的报单不应落单，当前订单数 %d", got)
	}
	if got := db.EsportsSku.GetX(ctx, sku.ID).QuotaReserved; got != 0 {
		t.Fatalf("被拦下的报单不应占用名额，quota_reserved=%d", got)
	}

	// 阈值调高到欠款之上 → 放行。
	debtLimit = db.SystemConfig.UpdateOne(debtLimit).SetValue("20000").SaveX(ctx)
	if _, err = placeOrder(ctx, db, 11, in, "guard-debt-002", now); err != nil {
		t.Fatalf("欠款未超阈值应放行：%v", err)
	}

	// 欠款订单取消后不再计入欠款 → 放行。
	debtLimit = db.SystemConfig.UpdateOne(debtLimit).SetValue("10000").SaveX(ctx)
	db.EsportsOrder.UpdateOneID(overdue.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCancelled).SaveX(ctx)
	if _, err = placeOrder(ctx, db, 11, in, "guard-debt-003", now); err != nil {
		t.Fatalf("欠款订单取消后应放行：%v", err)
	}

	// 关闭开关（阈值 0）→ 欠款客户也放行。
	db.EsportsOrder.UpdateOneID(overdue.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingPayment).SaveX(ctx)
	db.SystemConfig.UpdateOne(debtLimit).SetValue("0").SaveX(ctx)
	if _, err = placeOrder(ctx, db, 11, in, "guard-debt-004", now); err != nil {
		t.Fatalf("关闭欠款拦截后应放行：%v", err)
	}
}

// 预约冲突与资源复核：指定陪玩在冲突时段报单被拒，不冲突时段放行，且落为待确认档期的预约单。
func TestOrderGuardAppointmentResourceConflict(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_order_guard_appointment")
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	db.SystemConfig.Create().SetKey(duplicateOrderWindowKey).SetName("重复报单提醒时间窗").
		SetValue("0").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GUARD-APPT", Title: "预约冲突商品", ServiceType: "play", UnitPriceCents: 5000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: -1,
		ServiceDurationMinutes: 60, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	player := seedPlayer(t, db, 9601, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetConcurrentLimit(1).SaveX(ctx)

	now := time.Now().UTC()
	base := now.Add(24 * time.Hour).Truncate(time.Hour)
	// 该陪玩已在 [base, base+60m) 占位：已确认档期的已付款订单 + 已接受席位（与派单同一套容量口径）。
	occupying := seedScheduledPaidOrder(t, db, "guard-appt-occupy", base, 60)
	db.EsportsDispatchSeat.Create().SetOrderID(occupying.ID).SetSeatIndex(1).
		SetPlayerID(player.ID).SetStatus(esportsdispatchseat.StatusAccepted).SaveX(ctx)

	overlapping := base.Add(30 * time.Minute)
	if _, err = placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		PreferredPlayerID: player.ID, RequestedStartAt: &overlapping,
	}, "guard-appt-001", now); !errors.Is(err, errAppointmentResource) || !errors.Is(err, errInvalidInput) {
		t.Fatalf("冲突时段应被资源复核拦截：%v", err)
	}
	// 占位单是 seedScheduledPaidOrder 造的（客户/商品 ID 均为 1，与本次客户/商品撞号），故按单号排除。
	if got := db.EsportsOrder.Query().Where(
		esportsorder.CustomerIDEQ(customer.ID), esportsorder.SkuIDEQ(sku.ID),
		esportsorder.OrderNoNEQ(occupying.OrderNo),
	).CountX(ctx); got != 0 {
		t.Fatalf("被拦下的预约报单不应落单，当前订单数 %d", got)
	}

	// 不冲突时段：放行，且落为待确认档期的预约单（确认前不得派单）。
	apart := base.Add(5 * time.Hour)
	order, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		PreferredPlayerID: player.ID, RequestedStartAt: &apart,
	}, "guard-appt-002", now)
	if err != nil {
		t.Fatalf("不冲突时段应放行：%v", err)
	}
	if order.RequestedStartAt == nil || !order.RequestedStartAt.Equal(apart) ||
		order.AppointmentStatus != esportsorder.AppointmentStatusRequest ||
		order.AppointmentConfirmationDeadline == nil {
		t.Fatalf("预约单档期字段不正确：%+v", order)
	}
	if err = ensureAppointmentConfirmed(order); !errors.Is(err, errConflict) {
		t.Fatalf("未确认档期的预约单不得派单：%v", err)
	}

	// 未指定陪玩时不复核资源（预约单仍须确认档期）。
	if _, err = placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, RequestedStartAt: &overlapping,
	}, "guard-appt-003", now); err != nil {
		t.Fatalf("未指定陪玩时不应做资源复核：%v", err)
	}

	// 预约开始时间已过 → 参数非法。
	past := now.Add(-time.Minute)
	if _, err = placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, RequestedStartAt: &past,
	}, "guard-appt-004", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("预约开始时间早于当前应被拒：%v", err)
	}
}

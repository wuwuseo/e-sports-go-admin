package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportswallettx"
)

// 客户自助站内付款：与管理端共用核销边界，但必须核对订单归属、
// 受站内渠道启停约束，且钱包流水不写后台操作人、订单事件 actor 记为 customer。

// selfPaymentSeedOrder 建一名客户与一张待付款订单（SKU 单价 1000 分 × quantity）。
func selfPaymentSeedOrder(t *testing.T, db *ent.Client, customerID int, quantity int64,
	skuCode, orderKey string, now time.Time) (*ent.EsportsSku, *ent.EsportsOrder) {
	t.Helper()
	ctx := t.Context()
	sku, err := saveSku(ctx, db, skuInput{
		Code: skuCode, Title: "客户自助付款商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 10, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customerID, SkuID: sku.ID, Quantity: quantity},
		orderKey, now)
	if err != nil {
		t.Fatal(err)
	}
	return sku, order
}

func TestCustomerSelfBalancePayment(t *testing.T) {
	db := openWalletTestDB(t, "esports_customer_self_balance")
	defer db.Close()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	other := saveCustomerOrFail(t, db, 11)
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	_, order := selfPaymentSeedOrder(t, db, customer.ID, 2, "CS-BALANCE-1", "cs-balance-order-1", now)

	// 归属：用另一个客户的身份核销 → 拒绝，且订单不变。
	if _, err := payOrderWithBalanceAsCustomer(ctx, db, other.ID, order.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非本人订单应被拒，err=%v", err)
	}
	if recheck := db.EsportsOrder.GetX(ctx, order.ID); recheck.PaymentStatus != esportsorder.PaymentStatusUnpaid {
		t.Fatalf("越权核销不应改变订单：%+v", recheck)
	}
	// 余额不足 → 拒绝。
	if _, err := payOrderWithBalanceAsCustomer(ctx, db, customer.ID, order.ID, now); !errors.Is(err, errInsufficientBalance) {
		t.Fatalf("余额不足应被拒，err=%v", err)
	}
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 2500, Note: "客户充值"}, "cs-balance-recharge"); err != nil {
		t.Fatal(err)
	}
	// ：站内方式受后台启停约束，停用后客户自助路径同样拒绝。
	if err := setPaymentChannelEnabled(ctx, db, walletCustomerBalance, false); err != nil {
		t.Fatal(err)
	}
	if _, err := payOrderWithBalanceAsCustomer(ctx, db, customer.ID, order.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("余额支付停用后应拒绝客户自助核销，err=%v", err)
	}
	if err := setPaymentChannelEnabled(ctx, db, walletCustomerBalance, true); err != nil {
		t.Fatal(err)
	}

	paid, err := payOrderWithBalanceAsCustomer(ctx, db, customer.ID, order.ID, now)
	if err != nil {
		t.Fatalf("客户自助余额核销应成功，err=%v", err)
	}
	if paid.PaymentStatus != esportsorder.PaymentStatusPaid ||
		paid.PaymentChannel != esportsorder.PaymentChannelCustomerBalance ||
		paid.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("核销后订单状态不正确：%+v", paid)
	}
	// 钱包流水：客户不是后台操作人，operator_id 必须为空，备注标记客户自助。
	walletTx := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypeCustomerBalance),
		esportswallettx.OwnerIDEQ(customer.ID),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeConsume),
		esportswallettx.SourceIDEQ(order.OrderNo),
	).OnlyX(ctx)
	if walletTx.OperatorID != nil {
		t.Fatalf("客户自助核销不应写后台操作人：%+v", walletTx.OperatorID)
	}
	if walletTx.Note != "订单余额核销（客户自助）" {
		t.Fatalf("客户自助核销备注不正确：%q", walletTx.Note)
	}
	// 订单事件：actor 必须是 customer 本身。
	eventActor, eventActorID := "", ""
	for _, event := range db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.payment_verified"),
	).AllX(ctx) {
		eventActor, eventActorID = event.ActorType, event.ActorID
	}
	if eventActor != "customer" || eventActorID != strconv.Itoa(customer.ID) {
		t.Fatalf("客户自助核销事件 actor 不正确：%q/%q", eventActor, eventActorID)
	}
}

// 管理端既有路径回归：仍写后台操作人、备注不带「客户自助」。
func TestAdminBalancePaymentKeepsOperator(t *testing.T) {
	db := openWalletTestDB(t, "esports_admin_balance_operator")
	defer db.Close()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	_, order := selfPaymentSeedOrder(t, db, customer.ID, 1, "ADMIN-BALANCE-1", "admin-balance-order-1", now)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 2000, Note: "客户充值"}, "admin-balance-recharge"); err != nil {
		t.Fatal(err)
	}
	if _, err := payOrderWithBalance(ctx, db, 11, order.ID, now); err != nil {
		t.Fatalf("管理端余额核销应成功，err=%v", err)
	}
	walletTx := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypeCustomerBalance),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeConsume),
		esportswallettx.SourceIDEQ(order.OrderNo),
	).OnlyX(ctx)
	if walletTx.OperatorID == nil || *walletTx.OperatorID != 11 {
		t.Fatalf("管理端核销应写后台操作人：%+v", walletTx.OperatorID)
	}
	if walletTx.Note != "订单余额核销" {
		t.Fatalf("管理端核销备注不正确：%q", walletTx.Note)
	}
}

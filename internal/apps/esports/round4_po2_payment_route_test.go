package esports

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportspaymentreceipt"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/global"
)

// ── 第 2 波：客服发起收款入口（4.3-02）与已验签回调按单据类型分流（4.3-03） ──

// po2AdminPaymentApp 构造客服发起收款的最小管理端路由（与 app.go 同路径）。
func po2AdminPaymentApp(admin *ent.Admin, isSuper bool) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Post("/admin/orders/:id/payment/start", ownedOrderHandler(adminStartOrderPaymentHandler))
	return app
}

// ：客服 / 企微侧边栏可为订单发起已开通渠道的在线收款，且与客户自助共用同一套口径校验。
func TestRound4W2AdminStartOrderPayment(t *testing.T) {
	db := openProductDB(t, "esports_po2_admin_payment_start")
	defer db.Close()
	poSeedBaseConfigs(t, db)
	ctx := t.Context()
	now := time.Now().UTC()
	customer := saveCustomerOrFail(t, db, 11)
	sku := po2SeedSku(t, db, "PO2-PAY", 1, 1, 0)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"po2-pay-key-001", now)
	if err != nil {
		t.Fatal(err)
	}

	admin := db.Admin.Create().SetEmail("po2-admin-pay@example.com").SetPassword("test").
		SetUID("po2-admin-pay").SetUsername("po2-admin-pay").
		SetStatus("active").SaveX(ctx)

	// 未开通渠道时：明确失败（503），不返回伪成功。
	if _, err = startOrderPaymentAsOperator(ctx, db, admin.ID, order.ID, "wechat_work", now); !errors.Is(err, errPaymentGatewayUnavailable) {
		t.Fatalf("未开通渠道应明确失败：%v", err)
	}
	// 多人单每席量口径不一致时：参数错误（400），且先于渠道校验被拦下。
	multi := po2SeedSku(t, db, "PO2-PAY-MULTI", 2, 4, 2)
	multiOrder, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: multi.ID, Quantity: 1},
		"po2-pay-key-002", now)
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsOrder.UpdateOneID(multiOrder.ID).SetPerSeatServiceUnits(0).SaveX(ctx)
	if _, err = startOrderPaymentAsOperator(ctx, db, admin.ID, multiOrder.ID, "wechat_work", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("每席量缺失时应返回参数错误：%v", err)
	}

	// HTTP 入口：与 service 层同口径（未开通渠道 → 503）。
	previousORM := global.ORM
	global.ORM = db
	defer func() { global.ORM = previousORM }()
	app := po2AdminPaymentApp(admin, true)
	body, _ := json.Marshal(map[string]any{"channel": "wechat_work"})
	req := httptest.NewRequest("POST", "/admin/orders/"+strconv.Itoa(order.ID)+"/payment/start", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("客服发起收款未开通渠道应返回 503，实际 %d", resp.StatusCode)
	}
}

// ：已验签回调按支付意图所属单据类型分流——充值意图走 settleRechargeIntent 自动入账。
func TestRound4W2VerifiedCallbackRoutesRechargeIntent(t *testing.T) {
	db := openProductDB(t, "esports_po2_pay_route_recharge")
	defer db.Close()
	poSeedBaseConfigs(t, db)
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)

	intentNo := "PO2RCHG00000001"
	intent := db.EsportsPaymentIntent.Create().SetIntentNo(intentNo).
		SetOrderID(rechargeIntentOrderID(customer.ID)).SetConfirmationID(1).SetQuoteVersion(1).
		SetAmountCents(10000).SetChannel("wechat_work").
		SetStatus(esportspaymentintent.StatusPending).
		SetExpiresAt(now.Add(time.Hour)).SaveX(ctx)
	db.EsportsRechargeIntent.Create().SetIntentNo(intentNo).SetCustomerID(customer.ID).
		SetPrincipalCents(10000).SetBonusCents(0).SetSnapshotAt(now).SaveX(ctx)

	outcome, err := applyVerifiedPayment(ctx, db, verifiedPayment{
		IntentNo: intentNo, Channel: "wechat_work", TransactionNo: "PO2-TXN-RCHG-1",
		AmountCents: 10000, ConfirmedAt: now,
	})
	if err != nil {
		t.Fatalf("充值意图回调应按单据类型分流核销：%v", err)
	}
	if outcome.AppliedCents != 10000 || !outcome.OrderDispatched {
		t.Fatalf("充值核销结果不正确：%+v", outcome)
	}
	if got := db.EsportsPaymentIntent.GetX(ctx, intent.ID).Status; got != esportspaymentintent.StatusPaid {
		t.Fatalf("充值支付意图未置为已支付：%s", got)
	}
	if n := db.EsportsWalletTx.Query().Where(esportswallettx.OwnerIDEQ(customer.ID)).CountX(ctx); n != 1 {
		t.Fatalf("充值到账应写一条余额流水，实际 %d", n)
	}
	receipt := db.EsportsPaymentReceipt.Query().
		Where(esportspaymentreceipt.TransactionNoEQ("PO2-TXN-RCHG-1")).OnlyX(ctx)
	if receipt.Status != esportspaymentreceipt.StatusVerified || receipt.AppliedCents != 10000 {
		t.Fatalf("到账回执不正确：%+v", receipt)
	}

	// 同一笔到账重放：幂等返回既有回执，不重复入账。
	again, err := applyVerifiedPayment(ctx, db, verifiedPayment{
		IntentNo: intentNo, Channel: "wechat_work", TransactionNo: "PO2-TXN-RCHG-1",
		AmountCents: 10000, ConfirmedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("重复回调应幂等：%v", err)
	}
	if again.ReceiptID != outcome.ReceiptID {
		t.Fatalf("重复回调产生了新的回执：%d != %d", again.ReceiptID, outcome.ReceiptID)
	}
	if n := db.EsportsWalletTx.Query().Where(esportswallettx.OwnerIDEQ(customer.ID)).CountX(ctx); n != 1 {
		t.Fatalf("重复回调不得重复入账，实际流水 %d", n)
	}
	if n := db.EsportsRechargeIntent.Query().CountX(ctx); n != 1 {
		t.Fatalf("充值意图记录被改动：%d", n)
	}
}

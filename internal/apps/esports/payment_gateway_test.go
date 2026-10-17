package esports

import (
	"bytes"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsledgerentry"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportspaymentreceipt"
	"go-admin/internal/ent/esportsquotahold"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

// fakePaymentChannel 以 httptest 模拟已开通的 http_hmac 渠道：
// 校验下单请求签名，记录下单要素，按 intent_no 返回固定的收银台地址。
type fakePaymentChannel struct {
	mu           sync.Mutex
	appID        string
	secret       string
	hits         int
	lastIntentNo string
	lastAmount   int64
	lastExpires  string
	lastSubject  string
}

func newFakePaymentChannel(t *testing.T, appID, secret string) (*fakePaymentChannel, *httptest.Server) {
	t.Helper()
	channel := &fakePaymentChannel{appID: appID, secret: secret}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/payments" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		timestamp, provided, err := parsePaymentSignature(r.Header.Get("X-Pay-Signature"))
		if err != nil {
			http.Error(w, "signature malformed", http.StatusUnauthorized)
			return
		}
		expected := paymentHMACHex(channel.secret, paymentSignatureCanonical(channel.appID,
			strconv.FormatInt(timestamp, 10), paymentBodySHA256(body)))
		if !hmac.Equal([]byte(expected), []byte(provided)) {
			http.Error(w, "signature mismatch", http.StatusUnauthorized)
			return
		}
		var in paymentGatewayCreateRequest
		if err = json.Unmarshal(body, &in); err != nil {
			http.Error(w, "body malformed", http.StatusBadRequest)
			return
		}
		channel.mu.Lock()
		channel.hits++
		channel.lastIntentNo = in.IntentNo
		channel.lastAmount = in.AmountCents
		channel.lastExpires = in.ExpiresAt
		channel.lastSubject = in.Subject
		channel.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"pay_url": "https://pay.example.com/checkout/" + in.IntentNo,
		})
	}))
	t.Cleanup(server.Close)
	return channel, server
}

func (f *fakePaymentChannel) snapshot() (hits int, intentNo string, amount int64, expires, subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits, f.lastIntentNo, f.lastAmount, f.lastExpires, f.lastSubject
}

// seedGatewayPaymentFixture 造一张待付款订单：成年客户 + 在售 SKU + 名额 hold + 已核验报价确认书。
func seedGatewayPaymentFixture(t *testing.T, db *ent.Client, suffix string) (*ent.EsportsCustomer, *ent.EsportsOrder, *ent.EsportsSku) {
	t.Helper()
	ctx := t.Context()
	if ok, _ := db.SystemConfig.Query().Where(systemconfig.KeyEQ("app.esports.payment_deadline_minutes")).Exist(ctx); !ok {
		db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
			SetValue("60").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	}
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GW-" + suffix, Title: "陪玩一小时", ServiceType: "play", UnitPriceCents: 5000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: 2, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"gateway-order-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsConfirmation.Create().SetCustomerID(customer.ID).
		SetSubjectType("order_quote").SetSubjectID(order.ID).SetSubjectVersion(order.QuoteVersion).
		SetSkuID(sku.ID).SetQuantity(order.Quantity).SetAmountCents(order.PayableCents).
		SetChannel("verified_customer_message").SetEvidenceRef("gateway-evidence-" + suffix).
		SetStatus("verified").SetVerifiedAt(now).SetExpiresAt(order.PaymentDeadline).SaveX(ctx)
	seedActivePaymentAccount(t, db, "wechat_work", "WX-GW-"+suffix)
	return customer, order, sku
}

// seedPaymentGatewayConfig 写入在线收款渠道配置（JSON 对象，按渠道编码索引）。
func seedPaymentGatewayConfig(t *testing.T, db *ent.Client, channel, baseURL, appID, secret string) {
	t.Helper()
	ctx := t.Context()
	value, err := json.Marshal(map[string]paymentGatewayConfig{
		channel: {
			Kind: paymentGatewayKindHTTPHMAC, BaseURL: baseURL, AppID: appID,
			AppSecret: secret, NotifyURL: "https://studio.example.com/api/v1/esports/payment-notify/" + channel,
			Enabled: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(paymentGatewaysConfigKey)).Only(ctx)
	if ent.IsNotFound(err) {
		db.SystemConfig.Create().SetKey(paymentGatewaysConfigKey).SetName("在线收款渠道").
			SetValue(string(value)).SetType("json").SetGroup("esports").SetStatus(1).SaveX(ctx)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	db.SystemConfig.UpdateOneID(item.ID).SetValue(string(value)).ExecX(ctx)
}

// buildNotification 组装渠道回调原始 body 与签名头。
func buildNotification(t *testing.T, appID, secret string, payment verifiedPayment) ([]byte, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"intent_no": payment.IntentNo, "channel": payment.Channel,
		"transaction_no": payment.TransactionNo, "amount_cents": payment.AmountCents,
		"confirmed_at": payment.ConfirmedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	return body, paymentSignatureValue(secret, appID, time.Now().Unix(), body)
}

// TestOrderPaymentGatewayEndToEnd 全链路：发起收款拿 pay_url → 渠道正确签名回调 → 核销落账。
func TestOrderPaymentGatewayEndToEnd(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_gateway_e2e?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	const appID, secret = "studio-esports", "gateway-secret"
	fake, server := newFakePaymentChannel(t, appID, secret)
	seedPaymentGatewayConfig(t, db, "wechat_work", server.URL, appID, secret)
	customer, order, sku := seedGatewayPaymentFixture(t, db, "e2e")
	now := time.Now().UTC()

	view, err := startOrderPayment(ctx, db, customer.ID, order.ID, "wechat_work", now)
	if err != nil {
		t.Fatalf("发起收款失败：%v", err)
	}
	if view.IntentNo == "" || view.Channel != "wechat_work" ||
		view.AmountCents != order.PayableCents || view.ExpiresAt.IsZero() ||
		!strings.Contains(view.PayURL, view.IntentNo) {
		t.Fatalf("发起收款视图不正确：%+v", view)
	}
	hits, intentNo, amount, expires, subject := fake.snapshot()
	if hits != 1 || intentNo != view.IntentNo || amount != order.PayableCents ||
		expires == "" || !strings.Contains(subject, order.OrderNo) {
		t.Fatalf("渠道下单要素不正确：hits=%d intent=%s amount=%d expires=%s subject=%s",
			hits, intentNo, amount, expires, subject)
	}
	intent := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(view.IntentNo)).OnlyX(ctx)
	if intent.Channel != "wechat_work" || intent.Status != esportspaymentintent.StatusPending ||
		intent.OrderID != order.ID || intent.AmountCents != order.PayableCents {
		t.Fatalf("支付意图不正确：%+v", intent)
	}
	if got := db.EsportsSku.GetX(ctx, sku.ID); got.QuotaReserved != 1 || got.QuotaSold != 0 {
		t.Fatalf("发起收款阶段名额状态应保持 reserved：%+v", got)
	}

	body, signature := buildNotification(t, appID, secret, verifiedPayment{
		IntentNo: view.IntentNo, Channel: "wechat_work", TransactionNo: "WX-TX-E2E-0001",
		AmountCents: order.PayableCents, ConfirmedAt: now.Add(time.Minute),
	})
	outcome, err := handlePaymentNotification(ctx, db, "wechat_work", body, signature, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("回调核销失败：%v", err)
	}
	if outcome.AppliedCents != order.PayableCents || outcome.ExceptionCents != 0 ||
		!outcome.OrderDispatched || outcome.ReceiptID == 0 {
		t.Fatalf("核销结果不正确：%+v", outcome)
	}

	paid := db.EsportsOrder.GetX(ctx, order.ID)
	if paid.PaymentStatus != "paid" || paid.FulfillmentStatus != "pending_dispatch" || paid.PaidAt == nil {
		t.Fatalf("订单未转已付款待派单：%+v", paid)
	}
	stock := db.EsportsSku.GetX(ctx, sku.ID)
	if stock.QuotaReserved != 0 || stock.QuotaSold != 1 {
		t.Fatalf("名额未由 reserved 转 sold：%+v", stock)
	}
	if hold := db.EsportsQuotaHold.Query().Where(esportsquotahold.OrderIDEQ(order.ID)).OnlyX(ctx); hold.Status != esportsquotahold.StatusSold {
		t.Fatalf("名额占用未转已售：%+v", hold)
	}
	receipt := db.EsportsPaymentReceipt.Query().Where(
		esportspaymentreceipt.ChannelEQ("wechat_work"),
		esportspaymentreceipt.TransactionNoEQ("WX-TX-E2E-0001"),
	).OnlyX(ctx)
	if receipt.Status != esportspaymentreceipt.StatusVerified ||
		receipt.AppliedCents != order.PayableCents || receipt.ExceptionCents != 0 {
		t.Fatalf("回执不正确：%+v", receipt)
	}
	if got := db.EsportsPaymentIntent.GetX(ctx, intent.ID); got.Status != esportspaymentintent.StatusPaid {
		t.Fatalf("支付意图未转 paid：%+v", got)
	}
	entries := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.SourceTypeEQ("payment_receipt"),
		esportsledgerentry.SourceIDEQ(strconv.Itoa(receipt.ID)),
	).AllX(ctx)
	if len(entries) != 2 {
		t.Fatalf("到账账本应写 2 条（确认资金 + 订单预付），实际 %d：%+v", len(entries), entries)
	}
	if db.EsportsOutbox.Query().CountX(ctx) == 0 {
		t.Fatal("核销后应写外盒事件")
	}
	if db.EsportsPaymentReceipt.Query().CountX(ctx) != 1 {
		t.Fatalf("回执应只有一条，实际 %d", db.EsportsPaymentReceipt.Query().CountX(ctx))
	}
}

// TestOrderPaymentGatewayRejectsBadSignature 签名错误 → 403（errForbidden），订单与名额不变。
func TestOrderPaymentGatewayRejectsBadSignature(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_gateway_bad_sig?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	const appID, secret = "studio-esports", "gateway-secret"
	_, server := newFakePaymentChannel(t, appID, secret)
	seedPaymentGatewayConfig(t, db, "wechat_work", server.URL, appID, secret)
	customer, order, sku := seedGatewayPaymentFixture(t, db, "badsig")
	now := time.Now().UTC()

	view, err := startOrderPayment(ctx, db, customer.ID, order.ID, "wechat_work", now)
	if err != nil {
		t.Fatalf("发起收款失败：%v", err)
	}
	body, _ := buildNotification(t, appID, secret, verifiedPayment{
		IntentNo: view.IntentNo, Channel: "wechat_work", TransactionNo: "WX-TX-BAD-0001",
		AmountCents: order.PayableCents, ConfirmedAt: now.Add(time.Minute),
	})
	forged := paymentSignatureValue("wrong-secret", appID, time.Now().Unix(), body)
	if _, err = handlePaymentNotification(ctx, db, "wechat_work", body, forged, now); !errors.Is(err, errForbidden) {
		t.Fatalf("错误签名应返回 errForbidden：%v", err)
	}
	// 时间戳超出 5 分钟同样视为验签失败（防重放）。
	staleBody, _ := buildNotification(t, appID, secret, verifiedPayment{
		IntentNo: view.IntentNo, Channel: "wechat_work", TransactionNo: "WX-TX-BAD-0002",
		AmountCents: order.PayableCents, ConfirmedAt: now.Add(time.Minute),
	})
	stale := paymentSignatureValue(secret, appID, time.Now().Add(-10*time.Minute).Unix(), staleBody)
	if _, err = handlePaymentNotification(ctx, db, "wechat_work", staleBody, stale, now); !errors.Is(err, errForbidden) {
		t.Fatalf("过期时间戳应返回 errForbidden：%v", err)
	}
	reload := db.EsportsOrder.GetX(ctx, order.ID)
	if reload.PaymentStatus != "unpaid" || reload.FulfillmentStatus != "pending_payment" {
		t.Fatalf("验签失败不得变更订单：%+v", reload)
	}
	if stock := db.EsportsSku.GetX(ctx, sku.ID); stock.QuotaReserved != 1 || stock.QuotaSold != 0 {
		t.Fatalf("验签失败不得变更名额：%+v", stock)
	}
	if db.EsportsPaymentReceipt.Query().CountX(ctx) != 0 || db.EsportsLedgerEntry.Query().CountX(ctx) != 0 {
		t.Fatal("验签失败不得写回执或账本")
	}
}

// TestOrderPaymentGatewayReplayIsIdempotent 同一 transaction_no 重放：订单不二次变更、回执仅一条。
func TestOrderPaymentGatewayReplayIsIdempotent(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_gateway_replay?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	const appID, secret = "studio-esports", "gateway-secret"
	_, server := newFakePaymentChannel(t, appID, secret)
	seedPaymentGatewayConfig(t, db, "wechat_work", server.URL, appID, secret)
	customer, order, sku := seedGatewayPaymentFixture(t, db, "replay")
	now := time.Now().UTC()

	view, err := startOrderPayment(ctx, db, customer.ID, order.ID, "wechat_work", now)
	if err != nil {
		t.Fatalf("发起收款失败：%v", err)
	}
	notice := verifiedPayment{
		IntentNo: view.IntentNo, Channel: "wechat_work", TransactionNo: "WX-TX-REPLAY-0001",
		AmountCents: order.PayableCents, ConfirmedAt: now.Add(time.Minute),
	}
	body, signature := buildNotification(t, appID, secret, notice)
	first, err := handlePaymentNotification(ctx, db, "wechat_work", body, signature, now.Add(2*time.Minute))
	if err != nil || !first.OrderDispatched {
		t.Fatalf("首次核销失败：%+v err=%v", first, err)
	}
	paidVersion := db.EsportsOrder.GetX(ctx, order.ID).Version
	bodyAgain, signatureAgain := buildNotification(t, appID, secret, notice)
	second, err := handlePaymentNotification(ctx, db, "wechat_work", bodyAgain, signatureAgain, now.Add(3*time.Minute))
	if err != nil || second.ReceiptID != first.ReceiptID || second.AppliedCents != first.AppliedCents {
		t.Fatalf("重放应幂等返回既有 outcome：%+v err=%v", second, err)
	}
	reload := db.EsportsOrder.GetX(ctx, order.ID)
	if reload.Version != paidVersion || reload.PaymentStatus != "paid" ||
		reload.FulfillmentStatus != "pending_dispatch" {
		t.Fatalf("重放不得二次变更订单：version=%d %+v", paidVersion, reload)
	}
	if stock := db.EsportsSku.GetX(ctx, sku.ID); stock.QuotaReserved != 0 || stock.QuotaSold != 1 {
		t.Fatalf("重放不得二次扣减名额：%+v", stock)
	}
	if db.EsportsPaymentReceipt.Query().CountX(ctx) != 1 ||
		db.EsportsLedgerEntry.Query().CountX(ctx) != 2 {
		t.Fatalf("重放不得重复写回执 / 账本：receipts=%d ledgers=%d",
			db.EsportsPaymentReceipt.Query().CountX(ctx), db.EsportsLedgerEntry.Query().CountX(ctx))
	}
}

// TestOrderPaymentGatewayUnavailableWhenNotConfigured 渠道停用 / 配置非法或缺字段 → 不可用，
// 且绝不会产生假成功（不建意图、不调渠道、订单保持未付款）。
func TestOrderPaymentGatewayUnavailableWhenNotConfigured(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_gateway_unavailable?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	const appID, secret = "studio-esports", "gateway-secret"
	fake, server := newFakePaymentChannel(t, appID, secret)
	customer, order, _ := seedGatewayPaymentFixture(t, db, "off")
	now := time.Now().UTC()

	// 1) 完全没有配置项。
	if _, err := gatewayFor(ctx, db, "wechat_work"); !errors.Is(err, errPaymentGatewayUnavailable) {
		t.Fatalf("未配置渠道应返回 errPaymentGatewayUnavailable：%v", err)
	}
	if _, err := startOrderPayment(ctx, db, customer.ID, order.ID, "wechat_work", now); !errors.Is(err, errPaymentGatewayUnavailable) {
		t.Fatalf("未配置渠道发起收款应返回 errPaymentGatewayUnavailable：%v", err)
	}

	// 2) 配置非法 JSON：不得 panic。
	db.SystemConfig.Create().SetKey(paymentGatewaysConfigKey).SetName("在线收款渠道").
		SetValue("{not-json").SetType("json").SetGroup("esports").SetStatus(1).SaveX(ctx)
	if _, err := paymentGatewayConfigFor(ctx, db, "wechat_work"); !errors.Is(err, errPaymentGatewayUnavailable) {
		t.Fatalf("非法 JSON 应返回 errPaymentGatewayUnavailable：%v", err)
	}

	// 3) 缺字段（app_secret 为空）。
	db.SystemConfig.Update().Where(systemconfig.KeyEQ(paymentGatewaysConfigKey)).SetValue(fmt.Sprintf(
		`{"wechat_work":{"kind":"http_hmac","base_url":%q,"app_id":"x","app_secret":"","notify_url":"https://studio.example.com/notify","enabled":true}}`,
		server.URL)).SaveX(ctx)
	if _, err := gatewayFor(ctx, db, "wechat_work"); !errors.Is(err, errPaymentGatewayUnavailable) {
		t.Fatalf("缺字段配置应返回 errPaymentGatewayUnavailable：%v", err)
	}

	// 4) 渠道被后台停用（即使配置完整）。
	seedPaymentGatewayConfig(t, db, "wechat_work", server.URL, appID, secret)
	if err := setPaymentChannelEnabled(ctx, db, "wechat_work", false); err != nil {
		t.Fatal(err)
	}
	if _, err := gatewayFor(ctx, db, "wechat_work"); !errors.Is(err, errPaymentGatewayUnavailable) {
		t.Fatalf("停用渠道应返回 errPaymentGatewayUnavailable：%v", err)
	}
	if _, err := startOrderPayment(ctx, db, customer.ID, order.ID, "wechat_work", now); !errors.Is(err, errPaymentGatewayUnavailable) {
		t.Fatalf("停用渠道发起收款应返回 errPaymentGatewayUnavailable：%v", err)
	}
	if hits, _, _, _, _ := fake.snapshot(); hits != 0 {
		t.Fatalf("渠道不可用时不得调用下单接口，实际 %d 次", hits)
	}
	if db.EsportsPaymentIntent.Query().CountX(ctx) != 0 {
		t.Fatal("渠道不可用时不得创建支付意图")
	}
	reload := db.EsportsOrder.GetX(ctx, order.ID)
	if reload.PaymentStatus != "unpaid" || reload.FulfillmentStatus != "pending_payment" {
		t.Fatalf("渠道不可用时订单不得变更：%+v", reload)
	}
	if db.EsportsPaymentReceipt.Query().CountX(ctx) != 0 {
		t.Fatal("渠道不可用时不得产生收款回执")
	}
}

// TestOrderPaymentGatewayUnderpaymentStaysException 到账金额小于应付：不核销，
// 落 exception 回执（差额进暂收负债），订单保持未付款、名额仍 reserved。
func TestOrderPaymentGatewayUnderpaymentStaysException(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_gateway_underpay?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	const appID, secret = "studio-esports", "gateway-secret"
	_, server := newFakePaymentChannel(t, appID, secret)
	seedPaymentGatewayConfig(t, db, "wechat_work", server.URL, appID, secret)
	customer, order, sku := seedGatewayPaymentFixture(t, db, "underpay")
	now := time.Now().UTC()

	view, err := startOrderPayment(ctx, db, customer.ID, order.ID, "wechat_work", now)
	if err != nil {
		t.Fatalf("发起收款失败：%v", err)
	}
	body, signature := buildNotification(t, appID, secret, verifiedPayment{
		IntentNo: view.IntentNo, Channel: "wechat_work", TransactionNo: "WX-TX-UNDER-0001",
		AmountCents: order.PayableCents - 100, ConfirmedAt: now.Add(time.Minute),
	})
	outcome, err := handlePaymentNotification(ctx, db, "wechat_work", body, signature, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("回调处理失败：%v", err)
	}
	if outcome.AppliedCents != 0 || outcome.ExceptionCents != order.PayableCents-100 ||
		outcome.OrderDispatched {
		t.Fatalf("少付不得核销：%+v", outcome)
	}
	receipt := db.EsportsPaymentReceipt.GetX(ctx, outcome.ReceiptID)
	if receipt.Status != esportspaymentreceipt.StatusException || receipt.AppliedCents != 0 {
		t.Fatalf("少付应落 exception 回执：%+v", receipt)
	}
	reload := db.EsportsOrder.GetX(ctx, order.ID)
	if reload.PaymentStatus != "unpaid" || reload.FulfillmentStatus != "pending_payment" {
		t.Fatalf("少付不得变更订单：%+v", reload)
	}
	if stock := db.EsportsSku.GetX(ctx, sku.ID); stock.QuotaReserved != 1 || stock.QuotaSold != 0 {
		t.Fatalf("少付不得变更名额：%+v", stock)
	}
	entries := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.SourceTypeEQ("payment_receipt"),
		esportsledgerentry.SourceIDEQ(strconv.Itoa(receipt.ID)),
	).AllX(ctx)
	if len(entries) != 2 {
		t.Fatalf("少付应写「确认资金 + 暂收负债」2 条账本，实际 %d：%+v", len(entries), entries)
	}
	for _, entry := range entries {
		if entry.EntryRole == "unapplied_amount" && entry.DeltaCents != order.PayableCents-100 {
			t.Fatalf("暂收负债金额不正确：%+v", entry)
		}
	}
}

// postPaymentNotify 以真实 fiber 路由调用回调 handler，返回 HTTP 状态与响应体。
func postPaymentNotify(t *testing.T, app *fiber.App, channel string, body []byte,
	signature, timestamp string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/payment-notify/"+channel, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pay-Signature", signature)
	if timestamp != "" {
		req.Header.Set("X-Pay-Timestamp", timestamp)
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("回调请求失败：%v", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取回调响应失败：%v", err)
	}
	var out map[string]any
	if len(payload) > 0 {
		if err = json.Unmarshal(payload, &out); err != nil {
			t.Fatalf("回调响应不是 JSON：%s", payload)
		}
	}
	return resp.StatusCode, out
}

// TestPaymentGatewayNotifyHandlerStatusCodes 回调 HTTP 映射：
// 200 成功（幂等重放仍 200）/ 400 body 或签名头缺失 / 403 验签失败 / 409 同流水不同金额 / 503 渠道未配置。
func TestPaymentGatewayNotifyHandlerStatusCodes(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_gateway_handler?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	const appID, secret = "studio-esports", "gateway-secret"
	_, server := newFakePaymentChannel(t, appID, secret)
	seedPaymentGatewayConfig(t, db, "wechat_work", server.URL, appID, secret)
	customer, order, _ := seedGatewayPaymentFixture(t, db, "handler")
	now := time.Now().UTC()

	view, err := startOrderPayment(ctx, db, customer.ID, order.ID, "wechat_work", now)
	if err != nil {
		t.Fatalf("发起收款失败：%v", err)
	}
	app := fiber.New()
	app.Post("/payment-notify/:channel", paymentGatewayNotifyHandler)

	notice := verifiedPayment{
		IntentNo: view.IntentNo, Channel: "wechat_work", TransactionNo: "WX-TX-HANDLER-0001",
		AmountCents: order.PayableCents, ConfirmedAt: now.Add(time.Minute),
	}
	body, signature := buildNotification(t, appID, secret, notice)
	timestamp, provided, err := parsePaymentSignature(signature)
	if err != nil {
		t.Fatal(err)
	}
	// 裸 hex 签名 + 独立时间戳头：handler 负责拼装。
	status, payload := postPaymentNotify(t, app, "wechat_work", body, provided, strconv.FormatInt(timestamp, 10))
	if status != http.StatusOK || payload["code"] != float64(1) {
		t.Fatalf("正确签名应 200：status=%d body=%+v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	if data == nil || data["order_dispatched"] != true {
		t.Fatalf("回调响应应携带核销结果：%+v", payload)
	}

	// 同流水同金额重放：幂等 200，回执号不变。
	replayBody, replaySignature := buildNotification(t, appID, secret, notice)
	status, payload = postPaymentNotify(t, app, "wechat_work", replayBody, replaySignature, "")
	if status != http.StatusOK {
		t.Fatalf("幂等重放应 200：status=%d body=%+v", status, payload)
	}
	replayData, _ := payload["data"].(map[string]any)
	if replayData == nil || replayData["receipt_id"] != data["receipt_id"] {
		t.Fatalf("幂等重放应返回同一回执：first=%+v replay=%+v", data, replayData)
	}

	// 同流水不同金额：409。
	conflictBody, conflictSignature := buildNotification(t, appID, secret, verifiedPayment{
		IntentNo: view.IntentNo, Channel: "wechat_work", TransactionNo: notice.TransactionNo,
		AmountCents: order.PayableCents - 100, ConfirmedAt: now.Add(time.Minute),
	})
	if status, _ = postPaymentNotify(t, app, "wechat_work", conflictBody, conflictSignature, ""); status != http.StatusConflict {
		t.Fatalf("同流水不同金额应 409，实际 %d", status)
	}

	// 验签失败：403。
	forgedBody, _ := buildNotification(t, appID, secret, verifiedPayment{
		IntentNo: view.IntentNo, Channel: "wechat_work", TransactionNo: "WX-TX-HANDLER-0002",
		AmountCents: order.PayableCents, ConfirmedAt: now.Add(time.Minute),
	})
	forged := paymentSignatureValue("wrong-secret", appID, time.Now().Unix(), forgedBody)
	if status, _ = postPaymentNotify(t, app, "wechat_work", forgedBody, forged, ""); status != http.StatusForbidden {
		t.Fatalf("验签失败应 403，实际 %d", status)
	}

	// body 为空：400。
	if status, _ = postPaymentNotify(t, app, "wechat_work", nil, signature, ""); status != http.StatusBadRequest {
		t.Fatalf("空 body 应 400，实际 %d", status)
	}

	// 未配置的渠道：503。
	if status, _ = postPaymentNotify(t, app, "merchant_qr", body, signature, ""); status != http.StatusServiceUnavailable {
		t.Fatalf("未配置渠道应 503，实际 %d", status)
	}
}

package esports

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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
	"go-admin/internal/ent/esportscheckout"
	"go-admin/internal/ent/esportscheckoutattempt"
	"go-admin/internal/ent/esportsgiftintent"
	"go-admin/internal/ent/esportsofflinepaymentproof"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportspaymentreceipt"
	"go-admin/internal/ent/esportsrechargeintent"
	"go-admin/internal/global"
)

// ── 测试夹具：服务商配置 / 模拟服务商 ──

func writePaymentProvidersConfig(t *testing.T, db *ent.Client, cfg paymentProvidersConfig) {
	t.Helper()
	// 汇付 RSA PEM 超过 SystemConfig.value 的 2048 字符上限，按运行时口径拆到独立键。
	privateKey, publicKey := cfg.Huifu.PrivateKey, cfg.Huifu.PublicKey
	cfg.Huifu.PrivateKey, cfg.Huifu.PublicKey = "", ""
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("序列化服务商配置失败：%v", err)
	}
	db.SystemConfig.Create().SetKey(paymentProvidersConfigKey).SetName("聚合收款服务商配置").
		SetValue(string(raw)).SetType("string").SetGroup("esports").SetStatus(1).SaveX(t.Context())
	if privateKey != "" {
		db.SystemConfig.Create().SetKey(paymentProviderHuifuPrivateKeyConfigKey).SetName("汇付私钥").
			SetValue(privateKey).SetType("string").SetGroup("esports").SetStatus(1).SaveX(t.Context())
	}
	if publicKey != "" {
		db.SystemConfig.Create().SetKey(paymentProviderHuifuPublicKeyConfigKey).SetName("汇付公钥").
			SetValue(publicKey).SetType("string").SetGroup("esports").SetStatus(1).SaveX(t.Context())
	}
}

// sqbMock 收钱吧模拟服务：按公开签名口径验签请求并签发响应。
type sqbMock struct {
	t              *testing.T
	key            string
	mu             sync.Mutex
	tradeNo        string
	status         string // 查单返回的 order_status
	amount         int64
	payway         string
	failCreate     bool
	precreateCalls int
	queryCalls     int
}

func newSQBMock(t *testing.T, key string) (*sqbMock, *httptest.Server) {
	t.Helper()
	mock := &sqbMock{t: t, key: key, tradeNo: "SQBMOCK" + strconv.FormatInt(time.Now().UnixNano(), 10), status: "CREATED"}
	server := httptest.NewServer(http.HandlerFunc(mock.handle))
	t.Cleanup(server.Close)
	return mock, server
}

func sqbWriteSigned(t *testing.T, w http.ResponseWriter, fields map[string]any, key string) {
	t.Helper()
	body, err := shouqianbaSignedBody(fields, key)
	if err != nil {
		http.Error(w, "sign error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (m *sqbMock) handle(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "body", http.StatusBadRequest)
		return
	}
	if err = shouqianbaVerifySign(raw, m.key); err != nil {
		http.Error(w, "signature", http.StatusForbidden)
		return
	}
	var req map[string]any
	if err = json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "json", http.StatusBadRequest)
		return
	}
	merchantNo, _ := req["client_sn"].(string)
	amountText, _ := req["total_amount"].(string)
	payway, _ := req["payway"].(string)
	cents, _ := strconv.ParseInt(amountText, 10, 64)
	m.mu.Lock()
	switch r.URL.Path {
	case "/upay/v2/precreate":
		m.amount = cents
		m.payway = payway
		m.precreateCalls++
		// 每次预下单生成新的服务商交易号（真实服务商不会给两笔交易同一个号）。
		m.tradeNo = m.tradeNo + "N" + strconv.Itoa(m.precreateCalls)
		fail := m.failCreate
		tradeNo := m.tradeNo
		m.mu.Unlock()
		if fail {
			sqbWriteSigned(m.t, w, map[string]any{
				"result_code": "500", "error_code": "MOCK_CREATE_FAIL", "error_message": "模拟服务商受理失败",
			}, m.key)
			return
		}
		sqbWriteSigned(m.t, w, map[string]any{
			"result_code": "200",
			"biz_response": map[string]any{
				"result_code": "PRECREATE_SUCCESS",
				"data": map[string]any{
					"sn": tradeNo, "client_sn": merchantNo,
					"total_amount": amountText, "qr_code": "weixin://wxpay/bizpayurl?pr=mock",
					"order_status": "CREATED", "payway": payway,
					"ctime": strconv.FormatInt(time.Now().Unix(), 10),
				},
			},
		}, m.key)
	case "/upay/v2/query":
		m.queryCalls++
		status := m.status
		tradeNo := m.tradeNo
		recordedAmount := m.amount
		recordedPayway := m.payway
		m.mu.Unlock()
		sqbWriteSigned(m.t, w, map[string]any{
			"result_code": "200",
			"biz_response": map[string]any{
				"result_code": "SUCCESS",
				"data": map[string]any{
					"sn": tradeNo, "client_sn": merchantNo,
					"total_amount": strconv.FormatInt(recordedAmount, 10),
					"order_status": status, "payway": recordedPayway,
					"ctime": strconv.FormatInt(time.Now().Add(2*time.Second).Unix(), 10),
				},
			},
		}, m.key)
	default:
		m.mu.Unlock()
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (m *sqbMock) notificationBody(merchantNo, tradeNo, status string, cents int64, payway string) []byte {
	fields := map[string]any{
		"client_sn": merchantNo, "sn": tradeNo, "status": status,
		"total_amount": strconv.FormatInt(cents, 10), "payway": payway,
		// 服务商回调时间戳只有秒级：+2s 避免测试里恰好早于报价确认的亚秒时间。
		"ctime": strconv.FormatInt(time.Now().Add(2*time.Second).Unix(), 10),
	}
	body, _ := shouqianbaSignedBody(fields, m.key)
	return body
}

// huifuMock 汇付天下模拟服务：请求用商户公钥验签，响应用平台私钥签名。
type huifuMock struct {
	t               *testing.T
	merchantPub     *rsa.PublicKey
	platformPriv    *rsa.PrivateKey
	merchantPrivPEM string
	platformPubPEM  string
	mu              sync.Mutex
	tradeNo         string
	status          string // trans_stat: I 处理中 / S 成功 / F 失败
	failCreate      bool
	amountYuan      string
}

func generateTestRSA(t *testing.T) (*rsa.PrivateKey, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成测试 RSA 密钥失败：%v", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("编码测试 RSA 公钥失败：%v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return key, string(privPEM), string(pubPEM)
}

func newHuifuMock(t *testing.T) (*huifuMock, *httptest.Server) {
	t.Helper()
	_, merchantPrivPEM, merchantPubPEM := generateTestRSA(t)
	merchantPub, err := parseRSAPublicKeyPEM(merchantPubPEM)
	if err != nil {
		t.Fatalf("解析商户公钥失败：%v", err)
	}
	platformPriv, _, platformPubPEM := generateTestRSA(t)
	mock := &huifuMock{
		t: t, merchantPub: merchantPub, platformPriv: platformPriv,
		merchantPrivPEM: merchantPrivPEM, platformPubPEM: platformPubPEM,
		tradeNo: "HFMOCK" + strconv.FormatInt(time.Now().UnixNano(), 10), status: "I",
	}
	server := httptest.NewServer(http.HandlerFunc(mock.handle))
	t.Cleanup(server.Close)
	return mock, server
}

func (m *huifuMock) writeSigned(w http.ResponseWriter, data map[string]any) {
	sign, err := huifuSign(data, m.platformPriv)
	if err != nil {
		http.Error(w, "sign", http.StatusInternalServerError)
		return
	}
	body, _ := json.Marshal(map[string]any{"sign": sign, "sign_type": "RSA2", "data": data})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (m *huifuMock) handle(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "body", http.StatusBadRequest)
		return
	}
	var envelope map[string]any
	if err = json.Unmarshal(raw, &envelope); err != nil {
		http.Error(w, "json", http.StatusBadRequest)
		return
	}
	data, _ := envelope["data"].(map[string]any)
	sign, _ := envelope["sign"].(string)
	if err = huifuVerify(data, sign, m.merchantPub); err != nil {
		http.Error(w, "signature", http.StatusForbidden)
		return
	}
	merchantNo, _ := data["mer_ord_id"].(string)
	amount, _ := data["trans_amt"].(string)
	tradeType, _ := data["trade_type"].(string)
	m.mu.Lock()
	m.amountYuan = amount
	m.mu.Unlock()
	switch r.URL.Path {
	case "/top/trans/pullPayInfo":
		m.mu.Lock()
		fail := m.failCreate
		tradeNo := m.tradeNo
		m.mu.Unlock()
		if fail {
			m.writeSigned(w, map[string]any{
				"resp_code": "99000000", "sub_resp_code": "99", "sub_resp_desc": "模拟服务商受理失败",
			})
			return
		}
		m.writeSigned(w, map[string]any{
			"resp_code": "00000000", "sub_resp_code": "00", "resp_desc": "success",
			"hf_seq_id": tradeNo, "mer_ord_id": merchantNo, "trans_amt": amount,
			"trans_stat": "I", "trade_type": tradeType,
			"qr_code": "https://qr.mock.huifu/" + merchantNo,
		})
	case "/top/trans/query":
		m.mu.Lock()
		status := m.status
		tradeNo := m.tradeNo
		amountYuan := m.amountYuan
		m.mu.Unlock()
		m.writeSigned(w, map[string]any{
			"resp_code": "00000000", "sub_resp_code": "00", "resp_desc": "success",
			"hf_seq_id": tradeNo, "mer_ord_id": merchantNo, "trans_amt": amountYuan,
			"trans_stat": status, "trade_type": tradeType,
			"trans_time": time.Now().UTC().Format("20060102150405"),
		})
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (m *huifuMock) notificationBody(merchantNo, tradeNo, status string, cents int64, payChannel string) []byte {
	data := map[string]any{
		"resp_code": "00000000", "sub_resp_code": "00",
		"mer_ord_id": merchantNo, "hf_seq_id": tradeNo,
		"trans_amt": huifuAmountString(cents), "trans_stat": status,
		"trade_type": huifuTradeType(payChannel),
		"trans_time": time.Now().UTC().Format("20060102150405"),
	}
	sign, _ := huifuSign(data, m.platformPriv)
	body, _ := json.Marshal(map[string]any{"sign": sign, "sign_type": "RSA2", "data": data})
	return body
}

// postCheckoutWebhook 以真实 HTTP handler 处理一次服务商回调。
func postCheckoutWebhook(t *testing.T, db *ent.Client, path string, handler fiber.Handler, body []byte) (int, string) {
	t.Helper()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })
	app := fiber.New()
	app.Post(path, handler)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("调用回调失败：%v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func shouqianbaTestConfig(server *httptest.Server, key, merchantNo string) shouqianbaProviderConfig {
	return shouqianbaProviderConfig{
		Enabled: true, MerchantNo: merchantNo, AppID: "sqb-app", AppSecret: key,
		NotifyBase: "https://studio.example.com/api/v1/esports", BaseURL: server.URL,
	}
}

func huifuTestConfig(server *httptest.Server, mock *huifuMock) huifuProviderConfig {
	return huifuProviderConfig{
		Enabled: true, MerchantNo: "HF-MERCHANT-1", SysID: "HF-SYS", ProductID: "HF-PRODUCT",
		PrivateKey: mock.merchantPrivPEM, PublicKey: mock.platformPubPEM,
		NotifyBase: "https://studio.example.com/api/v1/esports", BaseURL: server.URL,
	}
}

// ── 订单：动态支付全链路 ──

func TestCheckoutOrderAggregateShouqianba(t *testing.T) {
	db := openWalletTestDB(t, "esports_checkout_order_sqb")
	defer db.Close()
	ctx := t.Context()
	customer, order := seedCustomerPaymentFixture(t, db, "checkout-sqb")
	key := "SQB-TERMINAL-KEY-1"
	sqb, server := newSQBMock(t, key)
	writePaymentProvidersConfig(t, db, paymentProvidersConfig{
		Shouqianba: shouqianbaTestConfig(server, key, "SQB-TERMINAL-1"),
		Priority:   []string{checkoutProviderShouqianba},
	})

	now := time.Now().UTC()
	checkout, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeOrder, BusinessID: order.ID, Mode: checkoutModeAggregate,
	}, now)
	if err != nil {
		t.Fatalf("创建聚合收款入口失败：%v", err)
	}
	if checkout.AmountCents != order.PayableCents || checkout.Status != esportscheckout.StatusOpen {
		t.Fatalf("入口快照不正确：%+v", checkout)
	}
	// 幂等：同业务单再次创建返回同一条入口。
	again, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeOrder, BusinessID: order.ID, Mode: checkoutModeAggregate,
	}, now)
	if err != nil || again.ID != checkout.ID {
		t.Fatalf("重复创建入口不幂等：%+v %v", again, err)
	}

	attempt, err := createCheckoutAttempt(ctx, db, checkout, "wechat", now)
	if err != nil {
		t.Fatalf("创建支付尝试失败：%v", err)
	}
	if attempt.Status != esportscheckoutattempt.StatusPending || attempt.Provider != esportscheckoutattempt.ProviderShouqianba {
		t.Fatalf("支付尝试状态不正确：%+v", attempt)
	}
	if attempt.QrPayload == "" {
		t.Fatalf("正扫尝试应返回二维码内容：%+v", attempt)
	}
	// 同一时刻只允许一条有效尝试：重复请求返回同一条。
	duplicate, err := createCheckoutAttempt(ctx, db, checkout, "wechat", now)
	if err != nil || duplicate.ID != attempt.ID {
		t.Fatalf("重复发起产生第二条有效尝试：%+v %v", duplicate, err)
	}

	// 乱序通知：先到「处理中」不得结算，后到 PAID 才入账。
	pendingBody := sqb.notificationBody(attempt.MerchantNo, sqb.tradeNo, "CREATED", order.PayableCents, "1")
	status, ack := postCheckoutWebhook(t, db, shouqianbaNotifyPath, shouqianbaPaymentNotifyHandler, pendingBody)
	if status != http.StatusOK || ack != "SUCCESS" {
		t.Fatalf("处理中通知应答不正确：%d %q", status, ack)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.PaymentStatus != esportsorder.PaymentStatusUnpaid {
		t.Fatalf("处理中通知不得入账：%+v", got)
	}

	body := sqb.notificationBody(attempt.MerchantNo, sqb.tradeNo, "PAID", order.PayableCents, "1")
	status, ack = postCheckoutWebhook(t, db, shouqianbaNotifyPath, shouqianbaPaymentNotifyHandler, body)
	if status != http.StatusOK || ack != "SUCCESS" {
		t.Fatalf("回调应答不正确：%d %q", status, ack)
	}
	paidOrder := db.EsportsOrder.GetX(ctx, order.ID)
	if paidOrder.PaymentStatus != esportsorder.PaymentStatusPaid ||
		paidOrder.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch ||
		paidOrder.PaymentChannel != esportsorder.PaymentChannelMerchantQr {
		t.Fatalf("订单未按聚合收款核销：%+v", paidOrder)
	}
	paidCheckout := db.EsportsCheckout.GetX(ctx, checkout.ID)
	if paidCheckout.Status != esportscheckout.StatusPaid {
		t.Fatalf("入口未置已支付：%+v", paidCheckout)
	}
	paidAttempt := db.EsportsCheckoutAttempt.GetX(ctx, attempt.ID)
	if paidAttempt.Status != esportscheckoutattempt.StatusPaid ||
		paidAttempt.ProviderTradeNo == nil || *paidAttempt.ProviderTradeNo != sqb.tradeNo {
		t.Fatalf("支付尝试未置已支付：%+v", paidAttempt)
	}
	if got := db.EsportsPaymentReceipt.Query().Where(
		esportspaymentreceipt.ChannelEQ("wechat_merchant_qr"),
		esportspaymentreceipt.TransactionNoEQ(sqb.tradeNo)).CountX(ctx); got != 1 {
		t.Fatalf("应生成一条收款回执，实际 %d", got)
	}

	// 重复回调：幂等成功，不重复入账。
	status, ack = postCheckoutWebhook(t, db, shouqianbaNotifyPath, shouqianbaPaymentNotifyHandler, body)
	if status != http.StatusOK || ack != "SUCCESS" {
		t.Fatalf("重复回调应答不正确：%d %q", status, ack)
	}
	if got := db.EsportsPaymentReceipt.Query().Where(
		esportspaymentreceipt.ChannelEQ("wechat_merchant_qr"),
		esportspaymentreceipt.TransactionNoEQ(sqb.tradeNo)).CountX(ctx); got != 1 {
		t.Fatalf("重复回调产生了第二条回执：%d", got)
	}
}

func TestCheckoutWebhookRejectsBadSignatureAndAmountMismatch(t *testing.T) {
	db := openWalletTestDB(t, "esports_checkout_order_bad")
	defer db.Close()
	ctx := t.Context()
	customer, order := seedCustomerPaymentFixture(t, db, "checkout-bad")
	key := "SQB-TERMINAL-KEY-2"
	sqb, server := newSQBMock(t, key)
	writePaymentProvidersConfig(t, db, paymentProvidersConfig{
		Shouqianba: shouqianbaTestConfig(server, key, "SQB-TERMINAL-2"),
		Priority:   []string{checkoutProviderShouqianba},
	})
	now := time.Now().UTC()
	checkout, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeOrder, BusinessID: order.ID, Mode: checkoutModeAggregate,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := createCheckoutAttempt(ctx, db, checkout, "wechat", now)
	if err != nil {
		t.Fatal(err)
	}

	// 错误密钥签名：拒绝且不结算。
	bad := (&sqbMock{t: t, key: "WRONG-KEY"}).notificationBody(
		attempt.MerchantNo, sqb.tradeNo, "PAID", order.PayableCents, "1")
	status, ack := postCheckoutWebhook(t, db, shouqianbaNotifyPath, shouqianbaPaymentNotifyHandler, bad)
	if status != http.StatusInternalServerError || ack != "FAIL" {
		t.Fatalf("签名错误应拒绝回调：%d %q", status, ack)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.PaymentStatus != esportsorder.PaymentStatusUnpaid {
		t.Fatalf("签名错误不得入账：%+v", got)
	}

	// 金额不符：转异常事件，不重复入账。
	mismatch := sqb.notificationBody(attempt.MerchantNo, sqb.tradeNo, "PAID", order.PayableCents-100, "1")
	status, ack = postCheckoutWebhook(t, db, shouqianbaNotifyPath, shouqianbaPaymentNotifyHandler, mismatch)
	if status != http.StatusOK || ack != "SUCCESS" {
		t.Fatalf("异常到账应停止服务商重试：%d %q", status, ack)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.PaymentStatus != esportsorder.PaymentStatusUnpaid {
		t.Fatalf("金额不符不得入账：%+v", got)
	}
	if got := db.EsportsCheckoutAttempt.GetX(ctx, attempt.ID); got.Status != esportscheckoutattempt.StatusFailed {
		t.Fatalf("异常到账应把尝试置 failed：%+v", got)
	}
	if got := db.EsportsOutbox.Query().Where().AllX(ctx); len(got) == 0 {
		t.Fatal("异常到账应写外盒事件供财务处理")
	}
	found := false
	for _, event := range db.EsportsOutbox.Query().AllX(ctx) {
		if event.EventType == "checkout.payment_exception" {
			found = true
		}
	}
	if !found {
		t.Fatal("缺少 checkout.payment_exception 异常事件")
	}
}

func TestCheckoutQueryCompensationSettlesOrder(t *testing.T) {
	db := openWalletTestDB(t, "esports_checkout_order_query")
	defer db.Close()
	ctx := t.Context()
	customer, order := seedCustomerPaymentFixture(t, db, "checkout-query")
	key := "SQB-TERMINAL-KEY-3"
	sqb, server := newSQBMock(t, key)
	sqb.status = "PAID"
	writePaymentProvidersConfig(t, db, paymentProvidersConfig{
		Shouqianba: shouqianbaTestConfig(server, key, "SQB-TERMINAL-3"),
		Priority:   []string{checkoutProviderShouqianba},
	})
	now := time.Now().UTC()
	checkout, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeOrder, BusinessID: order.ID, Mode: checkoutModeAggregate,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = createCheckoutAttempt(ctx, db, checkout, "wechat", now); err != nil {
		t.Fatal(err)
	}
	queried, settled, _, err := compensateCheckoutPayments(ctx, db, now.Add(time.Minute), 50)
	if err != nil {
		t.Fatalf("查单补偿失败：%v", err)
	}
	if queried != 1 || settled != 1 {
		t.Fatalf("查单补偿计数不正确：查单 %d 入账 %d", queried, settled)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.PaymentStatus != esportsorder.PaymentStatusPaid {
		t.Fatalf("查单确认后未入账：%+v", got)
	}
	if sqb.queryCalls != 1 {
		t.Fatalf("应调用一次查单，实际 %d", sqb.queryCalls)
	}
}

// 迟到的支付通知：尝试已过期仍按真实到账结算（订单在支付时限内即可入账）。
func TestCheckoutLateNotificationAfterAttemptExpiry(t *testing.T) {
	db := openWalletTestDB(t, "esports_checkout_late")
	defer db.Close()
	ctx := t.Context()
	customer, order := seedCustomerPaymentFixture(t, db, "checkout-late")
	key := "SQB-TERMINAL-KEY-LATE"
	sqb, server := newSQBMock(t, key)
	writePaymentProvidersConfig(t, db, paymentProvidersConfig{
		Shouqianba: shouqianbaTestConfig(server, key, "SQB-TERMINAL-LATE"),
		Priority:   []string{checkoutProviderShouqianba},
	})
	now := time.Now().UTC()
	checkout, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeOrder, BusinessID: order.ID, Mode: checkoutModeAggregate,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := createCheckoutAttempt(ctx, db, checkout, "wechat", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = expireCheckoutAttempt(ctx, db, attempt.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	body := sqb.notificationBody(attempt.MerchantNo, sqb.tradeNo, "PAID", order.PayableCents, "1")
	status, ack := postCheckoutWebhook(t, db, shouqianbaNotifyPath, shouqianbaPaymentNotifyHandler, body)
	if status != http.StatusOK || ack != "SUCCESS" {
		t.Fatalf("迟到回调应答不正确：%d %q", status, ack)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.PaymentStatus != esportsorder.PaymentStatusPaid {
		t.Fatalf("迟到到账未入账：%+v", got)
	}
	if got := db.EsportsCheckoutAttempt.GetX(ctx, attempt.ID); got.Status != esportscheckoutattempt.StatusPaid {
		t.Fatalf("迟到到账后尝试应为 paid：%+v", got)
	}
}

// ── 服务商受理失败：受理前允许按 priority 切换 ──

func TestCheckoutFallsBackToNextProviderBeforeAcceptance(t *testing.T) {
	db := openWalletTestDB(t, "esports_checkout_fallback")
	defer db.Close()
	ctx := t.Context()
	customer, order := seedCustomerPaymentFixture(t, db, "checkout-fallback")
	sqbKey := "SQB-TERMINAL-KEY-4"
	sqb, sqbServer := newSQBMock(t, sqbKey)
	sqb.failCreate = true
	huifu, huifuServer := newHuifuMock(t)
	writePaymentProvidersConfig(t, db, paymentProvidersConfig{
		Shouqianba: shouqianbaTestConfig(sqbServer, sqbKey, "SQB-TERMINAL-4"),
		Huifu:      huifuTestConfig(huifuServer, huifu),
		Priority:   []string{checkoutProviderShouqianba, checkoutProviderHuifu},
	})
	now := time.Now().UTC()
	checkout, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeOrder, BusinessID: order.ID, Mode: checkoutModeAggregate,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = createCheckoutAttempt(ctx, db, checkout, "wechat", now); err == nil {
		t.Fatal("收钱吧受理失败时应返回错误")
	}
	failed, err := db.EsportsCheckoutAttempt.Query().Where(
		esportscheckoutattempt.CheckoutIDEQ(checkout.ID)).Only(ctx)
	if err != nil || failed.Status != esportscheckoutattempt.StatusFailed {
		t.Fatalf("受理失败应留下 failed 尝试：%+v %v", failed, err)
	}
	fallback, err := createCheckoutAttempt(ctx, db, checkout, "wechat", now.Add(time.Second))
	if err != nil {
		t.Fatalf("切换到汇付天下失败：%v", err)
	}
	if fallback.Provider != esportscheckoutattempt.ProviderHuifu ||
		fallback.Status != esportscheckoutattempt.StatusPending {
		t.Fatalf("未按 priority 切换到下一家：%+v", fallback)
	}
}

// 网络超时 / 响应丢失属于「受理结果未知」：尝试保持 creating，不允许静默改投下一家。
func TestCheckoutDoesNotSwitchProviderWhenAcceptanceUnknown(t *testing.T) {
	db := openWalletTestDB(t, "esports_checkout_ambiguous")
	defer db.Close()
	ctx := t.Context()
	customer, order := seedCustomerPaymentFixture(t, db, "checkout-ambiguous")
	huifu, huifuServer := newHuifuMock(t)
	writePaymentProvidersConfig(t, db, paymentProvidersConfig{
		// 127.0.0.1:1 连接被拒：模拟「请求发出但没拿到受理结果」。
		Shouqianba: shouqianbaProviderConfig{
			Enabled: true, MerchantNo: "SQB-TERMINAL-AMB", AppSecret: "SQB-KEY-AMB",
			NotifyBase: "https://studio.example.com/api/v1/esports", BaseURL: "http://127.0.0.1:1",
		},
		Huifu:    huifuTestConfig(huifuServer, huifu),
		Priority: []string{checkoutProviderShouqianba, checkoutProviderHuifu},
	})
	now := time.Now().UTC()
	checkout, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeOrder, BusinessID: order.ID, Mode: checkoutModeAggregate,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = createCheckoutAttempt(ctx, db, checkout, "wechat", now); err == nil {
		t.Fatal("服务商不可达时应返回错误")
	}
	attempts := db.EsportsCheckoutAttempt.Query().Where(
		esportscheckoutattempt.CheckoutIDEQ(checkout.ID)).AllX(ctx)
	if len(attempts) != 1 {
		t.Fatalf("受理结果未知时不应创建第二家尝试：%+v", attempts)
	}
	if attempts[0].Provider != esportscheckoutattempt.ProviderShouqianba ||
		attempts[0].Status != esportscheckoutattempt.StatusCreating {
		t.Fatalf("受理结果未知的尝试应保持 creating 且仍属原服务商：%+v", attempts[0])
	}
	// 再次发起仍复用同一尝试（同一商户单号）重试，不切汇付天下。
	if _, err = createCheckoutAttempt(ctx, db, checkout, "wechat", now.Add(time.Second)); err == nil {
		t.Fatal("服务商不可达时应再次返回错误")
	}
	if got := db.EsportsCheckoutAttempt.Query().Where(
		esportscheckoutattempt.CheckoutIDEQ(checkout.ID),
		esportscheckoutattempt.ProviderEQ(esportscheckoutattempt.ProviderHuifu)).CountX(ctx); got != 0 {
		t.Fatalf("受理结果未知时不得改投汇付天下，实际 %d 条", got)
	}
}

// ── 充值：创建意图 → 聚合收款 → 余额入账 ──

func TestCheckoutRechargeHuifuCreditsWallet(t *testing.T) {
	db := openWalletTestDB(t, "esports_checkout_recharge")
	defer db.Close()
	ctx := t.Context()
	customer, _ := seedCustomerPaymentFixture(t, db, "checkout-recharge")
	now := time.Now().UTC()
	rechargeView, err := createRechargeIntent(ctx, db, customer, rechargeIntentInput{
		PrincipalCents: 5000, PaymentChannel: "merchant_qr", IdempotencyKey: "checkout-recharge-key-1",
	}, now)
	if err != nil {
		t.Fatalf("创建充值意图失败：%v", err)
	}
	recharge := db.EsportsRechargeIntent.Query().Where(
		esportsrechargeintent.IntentNoEQ(rechargeView.IntentNo)).OnlyX(ctx)

	huifu, server := newHuifuMock(t)
	writePaymentProvidersConfig(t, db, paymentProvidersConfig{
		Huifu:    huifuTestConfig(server, huifu),
		Priority: []string{checkoutProviderHuifu},
	})
	checkout, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeRecharge, BusinessID: recharge.ID, Mode: checkoutModeAggregate,
	}, now)
	if err != nil {
		t.Fatalf("充值创建聚合入口失败：%v", err)
	}
	attempt, err := createCheckoutAttempt(ctx, db, checkout, "alipay", now)
	if err != nil {
		t.Fatalf("充值创建支付尝试失败：%v", err)
	}
	body := huifu.notificationBody(attempt.MerchantNo, huifu.tradeNo, "S", 5000, "alipay")
	status, ack := postCheckoutWebhook(t, db, huifuNotifyPath, huifuPaymentNotifyHandler, body)
	if status != http.StatusOK || !strings.Contains(ack, "00000000") {
		t.Fatalf("汇付回调应答不正确：%d %q", status, ack)
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil || balance.PrincipalCents != 5000 {
		t.Fatalf("充值未入账：%+v %v", balance, err)
	}
	if got := db.EsportsCheckout.GetX(ctx, checkout.ID); got.Status != esportscheckout.StatusPaid {
		t.Fatalf("充值入口未置已支付：%+v", got)
	}
	// 重复回调幂等：余额不再增加。
	postCheckoutWebhook(t, db, huifuNotifyPath, huifuPaymentNotifyHandler, body)
	if balance, err = walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID); err != nil || balance.PrincipalCents != 5000 {
		t.Fatalf("重复回调重复入账：%+v %v", balance, err)
	}
}

// 充值意图以非聚合渠道发起时，选择聚合模式会在受理前显式切到聚合商户码并正常入账。
func TestCheckoutRechargeSwitchesPendingIntentChannel(t *testing.T) {
	db := openWalletTestDB(t, "esports_checkout_recharge_switch")
	defer db.Close()
	ctx := t.Context()
	customer, _ := seedCustomerPaymentFixture(t, db, "checkout-recharge-switch")
	now := time.Now().UTC()
	rechargeView, err := createRechargeIntent(ctx, db, customer, rechargeIntentInput{
		PrincipalCents: 3000, PaymentChannel: "wechat_work", IdempotencyKey: "checkout-recharge-switch-1",
	}, now)
	if err != nil {
		t.Fatalf("创建充值意图失败：%v", err)
	}
	recharge := db.EsportsRechargeIntent.Query().Where(
		esportsrechargeintent.IntentNoEQ(rechargeView.IntentNo)).OnlyX(ctx)
	key := "SQB-TERMINAL-KEY-SWITCH"
	sqb, server := newSQBMock(t, key)
	writePaymentProvidersConfig(t, db, paymentProvidersConfig{
		Shouqianba: shouqianbaTestConfig(server, key, "SQB-TERMINAL-SWITCH"),
		Priority:   []string{checkoutProviderShouqianba},
	})
	checkout, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeRecharge, BusinessID: recharge.ID, Mode: checkoutModeAggregate,
	}, now)
	if err != nil {
		t.Fatalf("充值聚合入口创建失败：%v", err)
	}
	intent := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(recharge.IntentNo)).OnlyX(ctx)
	if intent.Channel != "merchant_qr" {
		t.Fatalf("聚合模式应把待支付充值意图渠道切到 merchant_qr，got=%s", intent.Channel)
	}
	attempt, err := createCheckoutAttempt(ctx, db, checkout, "wechat", now)
	if err != nil {
		t.Fatal(err)
	}
	body := sqb.notificationBody(attempt.MerchantNo, sqb.tradeNo, "PAID", 3000, "1")
	status, ack := postCheckoutWebhook(t, db, shouqianbaNotifyPath, shouqianbaPaymentNotifyHandler, body)
	if status != http.StatusOK || ack != "SUCCESS" {
		t.Fatalf("充值回调应答不正确：%d %q", status, ack)
	}
	if balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID); err != nil || balance.PrincipalCents != 3000 {
		t.Fatalf("切换渠道后充值未入账：%+v %v", balance, err)
	}
}

// ── 礼物：确认单 → 聚合收款 → 现有礼物核销成单 ──

func TestCheckoutGiftShouqianbaSendsGift(t *testing.T) {
	db := openGiftDB(t, "esports_checkout_gift")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	account := db.User.Create().SetEmail("checkout-gift@example.com").SetUID("checkout-gift-user").
		SetStatus("active").SaveX(ctx)
	customer := seedGiftCustomer(t, db, account.ID)
	db.EsportsIdentityBinding.Create().SetCustomerID(customer.ID).SetProvider("user").SetSubjectScope("").
		SetExternalSubject(account.UID).SetVerificationMethod("verified_account_link").SetVerifiedAt(now).SaveX(ctx)
	player := seedPlayer(t, db, account.ID+100, "qualified", "online")
	gift := seedGift(t, db, giftInput{Code: "CHECKOUT-GIFT", Name: "火箭", PriceCents: 10000,
		CommissionMode: "percent", CommissionBp: 5000})
	intent, err := createGiftIntent(ctx, db, 11, false, giftIntentInput{
		CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1,
		ClientKey: "checkout-gift-intent-1",
	}, now)
	if err != nil {
		t.Fatalf("创建礼物确认单失败：%v", err)
	}
	if _, err = confirmGiftIntent(ctx, db, account.UID, intent.ID, 1, true, now); err != nil {
		t.Fatalf("客户确认礼物失败：%v", err)
	}

	key := "SQB-TERMINAL-KEY-5"
	sqb, server := newSQBMock(t, key)
	writePaymentProvidersConfig(t, db, paymentProvidersConfig{
		Shouqianba: shouqianbaTestConfig(server, key, "SQB-TERMINAL-5"),
		Priority:   []string{checkoutProviderShouqianba},
	})
	checkout, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeGift, BusinessID: intent.ID, Mode: checkoutModeAggregate,
	}, now)
	if err != nil {
		t.Fatalf("礼物创建聚合入口失败：%v", err)
	}
	attempt, err := createCheckoutAttempt(ctx, db, checkout, "wechat", now)
	if err != nil {
		t.Fatalf("礼物创建支付尝试失败：%v", err)
	}
	body := sqb.notificationBody(attempt.MerchantNo, sqb.tradeNo, "PAID", 10000, "1")
	status, ack := postCheckoutWebhook(t, db, shouqianbaNotifyPath, shouqianbaPaymentNotifyHandler, body)
	if status != http.StatusOK || ack != "SUCCESS" {
		t.Fatalf("礼物回调应答不正确：%d %q", status, ack)
	}
	if got := db.EsportsGiftOrder.Query().CountX(ctx); got != 1 {
		t.Fatalf("礼物应成单一次，实际 %d", got)
	}
	latest := db.EsportsGiftIntent.GetX(ctx, intent.ID)
	if latest.Status != esportsgiftintent.StatusConsumed || latest.PaymentStatus != esportsgiftintent.PaymentStatusVerified {
		t.Fatalf("礼物确认单未核销：%+v", latest)
	}
	if got := db.EsportsCheckout.GetX(ctx, checkout.ID); got.Status != esportscheckout.StatusPaid {
		t.Fatalf("礼物入口未置已支付：%+v", got)
	}
	// 重复回调不重复送礼。
	postCheckoutWebhook(t, db, shouqianbaNotifyPath, shouqianbaPaymentNotifyHandler, body)
	if got := db.EsportsGiftOrder.Query().CountX(ctx); got != 1 {
		t.Fatalf("重复回调重复送礼：%d", got)
	}

	// 核验后成单中断的补偿：收款已 verified 但礼物单未生成时，重试回调必须补送礼而不是当作成功。
	secondIntent, err := createGiftIntent(ctx, db, 11, false, giftIntentInput{
		CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1,
		ClientKey: "checkout-gift-intent-2",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = confirmGiftIntent(ctx, db, account.UID, secondIntent.ID, 1, true, now); err != nil {
		t.Fatal(err)
	}
	secondCheckout, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeGift, BusinessID: secondIntent.ID, Mode: checkoutModeAggregate,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	secondAttempt, err := createCheckoutAttempt(ctx, db, secondCheckout, "wechat", now)
	if err != nil {
		t.Fatal(err)
	}
	recoveryTradeNo := "SQB-RECOVERY-TRADE"
	// 模拟服务商已受理、但本站回写交易号前中断：尝试上没有交易号，回调按服务商交易号补记。
	db.EsportsCheckoutAttempt.UpdateOneID(secondAttempt.ID).ClearProviderTradeNo().ExecX(ctx)
	db.EsportsGiftIntent.UpdateOneID(secondIntent.ID).
		SetPaymentStatus(esportsgiftintent.PaymentStatusVerified).
		SetPaymentRef(recoveryTradeNo).ExecX(ctx)
	recoveryBody := sqb.notificationBody(secondAttempt.MerchantNo, recoveryTradeNo, "PAID", 10000, "1")
	status, ack = postCheckoutWebhook(t, db, shouqianbaNotifyPath, shouqianbaPaymentNotifyHandler, recoveryBody)
	if status != http.StatusOK || ack != "SUCCESS" {
		t.Fatalf("补偿回调应答不正确：%d %q", status, ack)
	}
	if got := db.EsportsGiftOrder.Query().CountX(ctx); got != 2 {
		t.Fatalf("核验后未成单的礼物应在重试回调时补送礼，实际礼物单 %d", got)
	}
}

// ── 互斥：固定码凭证 ↔ 动态支付 ──

func TestCheckoutMutualExclusionBetweenDynamicAndManual(t *testing.T) {
	db := openWalletTestDB(t, "esports_checkout_exclusive")
	defer db.Close()
	ctx := t.Context()
	customer, order := seedCustomerPaymentFixture(t, db, "checkout-exclusive")
	key := "SQB-TERMINAL-KEY-6"
	_, server := newSQBMock(t, key)
	writePaymentProvidersConfig(t, db, paymentProvidersConfig{
		Shouqianba: shouqianbaTestConfig(server, key, "SQB-TERMINAL-6"),
		Priority:   []string{checkoutProviderShouqianba},
	})
	now := time.Now().UTC()

	// 固定码模式入口不允许发起动态支付。
	manual, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeOrder, BusinessID: order.ID, Mode: checkoutModeManual,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = createCheckoutAttempt(ctx, db, manual, "wechat", now); err == nil {
		t.Fatal("固定码模式入口不应允许发起动态支付")
	}

	// 切换为聚合模式后创建有效尝试；此时固定码凭证提交与旧在线收款入口都应被拒绝。
	checkout, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeOrder, BusinessID: order.ID, Mode: checkoutModeAggregate,
	}, now)
	if err != nil {
		t.Fatalf("切换为聚合模式失败：%v", err)
	}
	if _, err = createCheckoutAttempt(ctx, db, checkout, "wechat", now); err != nil {
		t.Fatal(err)
	}
	if _, err = startOrderPaymentCore(ctx, db, customer.ID, order.ID, "wechat_work", now); err == nil {
		t.Fatal("动态支付在途时旧在线收款入口应被拒绝")
	}
	if _, err = submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: 1, TransactionNo: "SQB-MANUAL-1", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{1},
	}, now); err == nil {
		t.Fatal("动态支付在途时不得提交固定码凭证")
	}

	// 反过来：已有待核验凭证时，不能再创建聚合入口。
	otherCustomer, otherOrder := seedCustomerPaymentFixture(t, db, "checkout-exclusive-2")
	db.EsportsOfflinePaymentProof.Create().
		SetProofNo("OP-CHECKOUT-MANUAL").SetOrderID(otherOrder.ID).SetIntentID(1).SetAccountID(1).
		SetChannel(esportsofflinepaymentproof.ChannelMerchantQr).SetTransactionNo("MANUAL-TX-1").
		SetAmountCents(otherOrder.PayableCents).SetProofRef("proof-ref-12345678").
		SetStatus(esportsofflinepaymentproof.StatusPendingVerification).SaveX(ctx)
	if _, err = createOrReuseCheckout(ctx, db, otherCustomer, checkoutCreateInput{
		BusinessType: checkoutBusinessTypeOrder, BusinessID: otherOrder.ID, Mode: checkoutModeAggregate,
	}, now); err == nil {
		t.Fatal("已有待核验固定码凭证时不应允许创建聚合入口")
	}
}

// ── 配置脱敏 / 路由声明 ──

func TestPaymentProvidersMaskedViewNeverEchoesSecrets(t *testing.T) {
	cfg := paymentProvidersConfig{
		Shouqianba: shouqianbaProviderConfig{
			Enabled: true, MerchantNo: "SQB-MERCHANT", AppID: "SQB-APP",
			AppSecret: "SUPER-SECRET-TERMINAL-KEY", NotifyBase: "https://example.com/api",
		},
		Huifu: huifuProviderConfig{
			Enabled: true, MerchantNo: "HF-MERCHANT", SysID: "HF-SYS", ProductID: "HF-PRODUCT",
			PrivateKey: "PRIVATE-KEY-CONTENT", PublicKey: "PUBLIC-KEY-CONTENT",
			NotifyBase: "https://example.com/api",
		},
	}
	view := maskedPaymentProviders(cfg)
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SUPER-SECRET-TERMINAL-KEY", "PRIVATE-KEY-CONTENT", "PUBLIC-KEY-CONTENT"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("脱敏视图回显了密钥：%s", raw)
		}
	}
	if len(view.Providers) != 2 || !view.Channels["wechat"] || !view.Channels["alipay"] {
		t.Fatalf("脱敏视图不完整：%+v", view)
	}
}

func TestCheckoutNotifyRoutesDeclaredBeforeGenericChannelRoute(t *testing.T) {
	app := esportsAppDeclaration(t)
	index := map[string]int{}
	for i, route := range app.Routes {
		index[route.Method+" "+route.Path] = i
	}
	generic, ok := index["POST /payment-notify/:channel"]
	if !ok {
		t.Fatal("缺少通用在线收款回调路由")
	}
	for _, path := range []string{shouqianbaNotifyPath, huifuNotifyPath} {
		idx, ok := index["POST "+path]
		if !ok {
			t.Fatalf("缺少聚合收款回调路由 POST %s", path)
		}
		if idx > generic {
			t.Fatalf("聚合收款回调 %s 必须声明在 /payment-notify/:channel 之前，否则会被通用路由吞掉", path)
		}
	}
	if _, ok := index["GET /admin/payment-providers"]; !ok {
		t.Fatal("缺少服务商配置脱敏视图路由")
	}
}

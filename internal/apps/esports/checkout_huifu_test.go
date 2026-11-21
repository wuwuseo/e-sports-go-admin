package esports

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHuifuDougongSigningContract(t *testing.T) {
	mock, server := newHuifuMock(t)
	p, err := newHuifuProvider(huifuTestConfig(server, mock))
	if err != nil {
		t.Fatal(err)
	}
	// 固定原文独立约束排序、中文、HTML 字符、嵌套 JSON 字符串和大整数。
	const canonical = `{"amount":"0.01","count":9007199254740993,"desc":"陪玩<&>","extra":"{\"z\":1,\"a\":2}"}`
	const wire = `{"extra":"{\"z\":1,\"a\":2}", "desc":"陪玩<&>", "count":9007199254740993, "amount":"0.01"}`
	signed := huifuFixtureSign(t, []byte(canonical), mock.platformPriv)
	body, _ := json.Marshal(map[string]any{"data": json.RawMessage(wire), "sign": signed})
	fields, err := p.parseSignedEnvelope(body, false)
	if err != nil || fields["count"] != json.Number("9007199254740993") {
		t.Fatalf("同步排序验签失败: %v", err)
	}
	encoded, _ := huifuJSON(fields)
	if string(encoded) != canonical {
		t.Fatalf("待签名原文不符: %s", encoded)
	}
	// 异步回调必须验原文，不能重新排序后再验。
	body, _ = json.Marshal(map[string]string{"resp_data": wire, "sign": huifuFixtureSign(t, []byte(wire), mock.platformPriv)})
	if _, err = p.parseSignedEnvelope(body, true); err != nil {
		t.Fatal(err)
	}
	bad, _ := json.Marshal(map[string]string{"resp_data": wire, "sign": signed})
	if _, err = p.parseSignedEnvelope(bad, true); !errors.Is(err, errForbidden) {
		t.Fatalf("异步误用了排序验签: %v", err)
	}
	if _, err = p.parseSignedEnvelope(bytes.Replace(body, []byte("0.01"), []byte("9.99"), 1), true); !errors.Is(err, errForbidden) {
		t.Fatalf("篡改未拦截: %v", err)
	}
	legacy, _ := json.Marshal(map[string]string{"data": canonical, "sign": signed})
	if _, err = p.parseSignedEnvelope(legacy, false); err == nil {
		t.Fatal("不得接受旧版字符串 data")
	}
}

func TestHuifuDougongKeyAndAmountContract(t *testing.T) {
	key, _, _ := generateTestRSA(t)
	priv, _ := x509.MarshalPKCS8PrivateKey(key)
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if parsed, err := parseRSAPrivateKeyPEM(base64.StdEncoding.EncodeToString(priv)); err != nil || parsed.N.Cmp(key.N) != 0 {
		t.Fatal("未兼容沙箱 PKCS8 Base64 私钥")
	}
	if parsed, err := parseRSAPublicKeyPEM(base64.StdEncoding.EncodeToString(pub)); err != nil || parsed.N.Cmp(key.N) != 0 {
		t.Fatal("未兼容沙箱 X509 Base64 公钥")
	}
	for _, raw := range []string{"-1.00", "1.001", "1e2", "", "1.", "92233720368547758.08", "NaN"} {
		if huifuAmountCents(raw) != 0 {
			t.Errorf("非法金额未拒绝: %q", raw)
		}
	}
	if huifuAmountCents("123.45") != 12345 || huifuAmountCents("0.01") != 1 {
		t.Fatal("分转换不正确")
	}
}

func TestHuifuDougongCallbackScope(t *testing.T) {
	mock, server := newHuifuMock(t)
	p, _ := newHuifuProvider(huifuTestConfig(server, mock))
	for _, tc := range []struct{ name, merchant, amount, trade string }{
		{"wrong_merchant", "OTHER", "1.00", "HF-1"},
		{"fractional_cents", "HF-MERCHANT-1", "1.001", "HF-1"},
		{"missing_trade", "HF-MERCHANT-1", "1.00", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"resp_code": "00000000", "huifu_id": tc.merchant, "req_seq_id": "LOCAL-ORDER", "hf_seq_id": tc.trade, "trans_amt": tc.amount, "trans_stat": "S", "trans_type": "A_NATIVE"})
			body := url.Values{"resp_data": {string(raw)}, "sign": {huifuFixtureSign(t, raw, mock.platformPriv)}}.Encode()
			if _, err := p.VerifyNotification(t.Context(), nil, []byte(body)); err == nil {
				t.Fatal("非法回调未拒绝")
			}
		})
	}
	failed := mock.notificationBody("LOCAL-ORDER", "", "F", 0, "alipay")
	if notice, err := p.VerifyNotification(t.Context(), nil, failed); err != nil || notice.Paid {
		t.Fatalf("失败回调解析错误: %v", err)
	}
}

func TestHuifuDougongUnknownResponseDoesNotAllowFailover(t *testing.T) {
	mock, server := newHuifuMock(t)
	p, _ := newHuifuProvider(huifuTestConfig(server, mock))
	for _, code := range []string{"20000000", "98888888", "91111119", "UNKNOWN", "10000000"} {
		t.Run(code, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mock.writeSigned(w, map[string]any{"resp_code": code}) }))
			defer endpoint.Close()
			p.baseURL = endpoint.URL
			_, err := p.call(context.Background(), "/v4/trade/payment/create", map[string]any{"req_seq_id": "TEST"})
			if err == nil || errors.Is(err, errCheckoutProviderRejected) != (code == "10000000") {
				t.Fatalf("受理未知时错误改投: %v", err)
			}
		})
	}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"resp_code":"10000000"}}`))
	}))
	defer endpoint.Close()
	p.baseURL = endpoint.URL
	_, err := p.call(t.Context(), "/v4/trade/payment/create", map[string]any{})
	if err == nil || errors.Is(err, errCheckoutProviderRejected) {
		t.Fatal("未签名错误不得触发改投")
	}
}

func TestHuifuDougongQueryUsesOriginalCSTDate(t *testing.T) {
	mock, server := newHuifuMock(t)
	p, _ := newHuifuProvider(huifuTestConfig(server, mock))
	original := time.Date(2026, 11, 2, 16, 1, 0, 0, time.UTC) // 北京时间 11 月 3 日
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env struct {
			Data map[string]string `json:"data"`
		}
		_ = json.NewDecoder(r.Body).Decode(&env)
		if r.URL.Path != "/v4/trade/payment/scanpay/query" || env.Data["req_date"] != "20261103" || env.Data["req_seq_id"] != "ORIGINAL" || env.Data["org_req_date"] != "" {
			t.Error("查单未使用原订单日期和流水号")
		}
		mock.writeSigned(w, map[string]any{"resp_code": "00000000", "huifu_id": "HF-MERCHANT-1", "req_seq_id": "ORIGINAL", "req_date": "20261103", "trans_stat": "S", "hf_seq_id": "HF-ORIGINAL", "trans_amt": "1.00", "trade_type": "A_NATIVE"})
	}))
	defer endpoint.Close()
	p.baseURL = endpoint.URL
	if notice, err := p.QueryPayment(t.Context(), "ORIGINAL", original); err != nil || !notice.Paid {
		t.Fatalf("跨日查单失败: %v", err)
	}
	if checkoutProviderSupports("huifu", "wechat") || !checkoutProviderSupports("huifu", "alipay") {
		t.Fatal("渠道能力与实现不一致")
	}
	if strings.HasPrefix(p.notifyURL(), "virgo://") {
		t.Fatal("斗拱通知地址不得携带 SPIN 前缀")
	}
}

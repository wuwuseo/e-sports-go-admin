package esports

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscheckout"
	"go-admin/internal/ent/esportsrechargeintent"
	"go-admin/internal/global"
)

// 显式启用时直连官方本地沙箱二进制；业务数据仅写入内存 SQLite。
// HUIFU_LOCAL_SANDBOX_CREDENTIALS 指向控制台导出的本机凭证，绝不使用生产密钥。
func TestHuifuLocalSandbox(t *testing.T) {
	credentialPath := os.Getenv("HUIFU_LOCAL_SANDBOX_CREDENTIALS")
	if credentialPath == "" {
		t.Skip("设置 HUIFU_LOCAL_SANDBOX_CREDENTIALS 后运行官方本地沙箱联测")
	}
	raw, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal("无法读取本地沙箱凭证")
	}
	var credentials struct {
		GatewayURL  string `json:"gateway_url"`
		SysID       string `json:"sys_id"`
		ProductID   string `json:"product_id"`
		HuifuID     string `json:"huifu_id"`
		PrivateKey  string `json:"merchant_private_key"`
		PublicKey   string `json:"merchant_public_key"`
		SkillSource string `json:"skill_source"`
	}
	if json.Unmarshal(raw, &credentials) != nil {
		t.Fatal("沙箱凭证格式错误")
	}
	gateway, err := url.Parse(credentials.GatewayURL)
	if err != nil || gateway.Scheme != "http" || gateway.Hostname() != "127.0.0.1" || gateway.User != nil || gateway.RawQuery != "" || gateway.Fragment != "" {
		t.Fatal("集成测试仅允许直连 127.0.0.1 本地沙箱")
	}
	// 管理令牌只在本机文件和内存中读取，不进入测试输出或报告。
	runtimePath := os.Getenv("HUIFU_LOCAL_SANDBOX_RUNTIME")
	if runtimePath == "" {
		runtimePath = filepath.Join(filepath.Dir(credentialPath), "runtime.jsonl")
	}
	runtimeRaw, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatal("无法读取沙箱启动状态")
	}
	var runtime struct {
		ControlURL string `json:"control_url"`
		AdminToken string `json:"admin_token"`
		CSRFToken  string `json:"csrf_token"`
	}
	for _, line := range bytes.Split(runtimeRaw, []byte("\n")) {
		if json.Unmarshal(line, &runtime) == nil && runtime.AdminToken != "" {
			break
		}
	}
	control, err := url.Parse(runtime.ControlURL)
	if err != nil || control.Scheme != "http" || control.Hostname() != "127.0.0.1" || control.User != nil || runtime.AdminToken == "" || runtime.CSRFToken == "" {
		t.Fatal("需要本地沙箱控制面令牌")
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	adminPost := func(path string, body any) map[string]any {
		t.Helper()
		payload, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, runtime.ControlURL+path, bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+runtime.AdminToken)
		req.Header.Set("X-Huifu-Sandbox-CSRF", runtime.CSRFToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal("沙箱控制面请求失败")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("沙箱控制面返回 HTTP %d", resp.StatusCode)
		}
		var result map[string]any
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result) != nil {
			t.Fatal("沙箱控制面响应格式错误")
		}
		return result
	}
	db := openWalletTestDB(t, "esports_huifu_official_local_sandbox")
	defer db.Close()
	ctx := context.WithValue(t.Context(), checkoutClientIPKey{}, "127.0.0.1")
	customer, _ := seedCustomerPaymentFixture(t, db, "huifu-official-local")
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	app := fiber.New()
	app.Post(huifuNotifyPath, huifuPaymentNotifyHandler)
	var lastCallback []byte
	var callbackMu sync.Mutex
	var dropNotify atomic.Bool
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dropNotify.Load() {
			http.Error(w, "local test drops notification", http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		callbackMu.Lock()
		lastCallback = append([]byte(nil), body...)
		callbackMu.Unlock()
		request := httptest.NewRequest(http.MethodPost, huifuNotifyPath, bytes.NewReader(body))
		request.Header.Set("Content-Type", r.Header.Get("Content-Type"))
		resp, err := app.Test(request, fiber.TestConfig{Timeout: 10 * time.Second})
		if err != nil {
			http.Error(w, "callback handler failed", 500)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer receiver.Close()
	writePaymentProvidersConfig(t, db, paymentProvidersConfig{Huifu: huifuProviderConfig{
		Enabled: true, MerchantNo: credentials.HuifuID, SysID: credentials.SysID, ProductID: credentials.ProductID,
		PrivateKey: credentials.PrivateKey, PublicKey: credentials.PublicKey, BaseURL: credentials.GatewayURL, NotifyBase: receiver.URL,
		SkillSource: credentials.SkillSource,
	}, Priority: []string{checkoutProviderHuifu}})
	newAttempt := func(cents int64) (*ent.EsportsCheckout, *ent.EsportsCheckoutAttempt) {
		t.Helper()
		now := time.Now().UTC()
		view, err := createRechargeIntent(ctx, db, customer, rechargeIntentInput{PrincipalCents: cents, PaymentChannel: "merchant_qr", IdempotencyKey: "local-sandbox-" + strconv.FormatInt(now.UnixNano(), 10)}, now)
		if err != nil {
			t.Fatal(err)
		}
		recharge := db.EsportsRechargeIntent.Query().Where(esportsrechargeintent.IntentNoEQ(view.IntentNo)).OnlyX(ctx)
		checkout, err := createOrReuseCheckout(ctx, db, customer, checkoutCreateInput{BusinessType: checkoutBusinessTypeRecharge, BusinessID: recharge.ID, Mode: checkoutModeAggregate}, now)
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := createCheckoutAttempt(ctx, db, checkout, "alipay", now)
		if err != nil {
			t.Fatal(err)
		}
		if attempt.QrPayload == "" {
			t.Fatal("沙箱未返回二维码")
		}
		return checkout, attempt
	}
	assertBalance := func(want int64) {
		t.Helper()
		balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
		if err != nil || balance.PrincipalCents != want {
			t.Fatalf("钱包应为 %d 分，实际 %+v，错误 %v", want, balance, err)
		}
	}
	checkout, attempt := newAttempt(5000)
	assertBalance(0)
	deliver := func(outcome string) map[string]any {
		return adminPost("/__admin/deliver", map[string]string{"channel": "notify", "entity_type": "payment", "req_seq_id": attempt.MerchantNo, "outcome": outcome})
	}
	if result := deliver("success"); result["ok"] != true {
		t.Fatalf("沙箱通知投递失败: %v", result["error"])
	}
	assertBalance(5000)
	if db.EsportsCheckout.GetX(ctx, checkout.ID).Status != esportscheckout.StatusPaid {
		t.Fatal("通知后入口未支付")
	}
	if result := deliver("success"); result["ok"] != true {
		t.Fatal("重复通知 ACK 失败")
	}
	if result := deliver("failure"); result["ok"] != true {
		t.Fatal("迟到失败通知 ACK 失败")
	}
	assertBalance(5000)
	// 复用沙箱实际签名报文篡改金额，必须拒绝且保持余额。
	callbackMu.Lock()
	form, err := url.ParseQuery(string(lastCallback))
	callbackMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var tampered map[string]any
	_ = json.Unmarshal([]byte(form.Get("resp_data")), &tampered)
	tampered["trans_amt"] = "9999.99"
	changed, _ := json.Marshal(tampered)
	form.Set("resp_data", string(changed))
	resp, err := client.Post(receiver.URL+huifuNotifyPath, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("篡改沙箱签名通知未拒绝")
	}
	assertBalance(5000)
	t.Log("通过：真实沙箱下单及响应验签、成功通知、重复通知、迟到失败、篡改签名拦截、钱包幂等")
	// 模拟通知丢失，由现有定时补偿入口主动查单并入账。
	dropNotify.Store(true)
	checkout, _ = newAttempt(3000)
	for i := 0; i < 2; i++ {
		if _, _, _, err := compensateCheckoutPayments(ctx, db, time.Now().UTC(), 100); err != nil {
			t.Fatal(err)
		}
	}
	assertBalance(8000)
	if db.EsportsCheckout.GetX(ctx, checkout.ID).Status != esportscheckout.StatusPaid {
		t.Fatal("查单补偿未完成")
	}
	if _, _, _, err := compensateCheckoutPayments(ctx, db, time.Now().UTC(), 100); err != nil {
		t.Fatal(err)
	}
	assertBalance(8000)
	t.Log("通过：通知缺失时查单补偿入账及重复补偿幂等")
	adminPost("/__admin/report", map[string]any{})
}

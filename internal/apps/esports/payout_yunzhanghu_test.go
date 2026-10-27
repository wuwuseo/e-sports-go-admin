package esports

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayerpayoutaccount"
	"go-admin/internal/ent/esportswithdrawal"
	"go-admin/internal/ent/esportswithdrawalpayout"
	"go-admin/internal/global"
)

// ── 测试辅助 ──

// yunzhanghuTestKeys 现场生成商户私钥与云账户私钥（不硬编码任何密钥）。
func yunzhanghuTestKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	merchant, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return merchant, provider
}

func yunzhanghuPEMPrivate(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	return string(pem.EncodeToMemory(block))
}

func yunzhanghuPEMPublic(t *testing.T, key *rsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// setupYunzhanghuProvider 写入云账户配置与密钥并构造适配器。
func setupYunzhanghuProvider(t *testing.T, db *ent.Client, baseURL string,
	merchant, provider *rsa.PrivateKey) *yunzhanghuProvider {
	t.Helper()
	setWithdrawalConfig(t, db, "app.esports.payout_provider", payoutProviderYunzhanghu)
	setPayoutYunzhanghuConfig(t, db, yunzhanghuPayoutConfig{
		Enabled: true, DealerID: "D1", AppKey: "APP-KEY", BaseURL: baseURL,
	})
	setWithdrawalConfig(t, db, yunzhanghuPayoutPrivateKeyConfigKey, yunzhanghuPEMPrivate(t, merchant))
	setWithdrawalConfig(t, db, yunzhanghuPayoutPublicKeyConfigKey, yunzhanghuPEMPublic(t, &provider.PublicKey))
	p, err := newYunzhanghuProvider(t.Context(), db)
	if err != nil {
		t.Fatalf("构造云账户适配器失败：%v", err)
	}
	return p
}

// yunzhanghuSignedEnvelope 用云账户私钥签名响应 / 回调报文。
func yunzhanghuSignedEnvelope(t *testing.T, key *rsa.PrivateKey, code, message, data string) []byte {
	t.Helper()
	sign, err := yunzhanghuSign(map[string]any{"code": code, "message": message, "data": data}, key)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{
		"code": code, "message": message, "data": data, "sign": sign,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func yunzhanghuBatchItems() []payoutBatchItem {
	return []payoutBatchItem{{
		OrderNo: "O-1", SignNo: "SIGN-1", AmountCents: 4000,
		AccountType: "bank_card", AccountName: "张三", Remark: "代发",
	}}
}

// ── 提交批次 ──

// 未启用配置时明确报中文错误且一次请求都不发。
func TestYunzhanghuPayoutSubmitRequiresConfig(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	db := openWithdrawalDB(t, "yzh_payout_disabled")
	defer db.Close()
	setPayoutYunzhanghuConfig(t, db, yunzhanghuPayoutConfig{Enabled: false, BaseURL: server.URL})
	p, err := newYunzhanghuProvider(t.Context(), db)
	if err != nil {
		t.Fatalf("配置不完整时仍应可构造适配器：%v", err)
	}
	if _, err = p.SubmitBatch(t.Context(), payoutBatchRequest{BatchNo: "B-1", DealerID: "D1", Items: yunzhanghuBatchItems()}); !errors.Is(err, errPayoutProviderDisabled) ||
		!strings.Contains(err.Error(), "云账户代发未启用或配置不完整") {
		t.Fatalf("未启用时应返回明确中文错误，got %v", err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("未启用时不得发起请求，实际请求 %d 次", n)
	}
}

// 成功提交：请求签名可验证，响应逐笔区分 accepted / rejected / unknown。
func TestYunzhanghuPayoutSubmitBatchResult(t *testing.T) {
	merchant, providerKey := yunzhanghuTestKeys(t)
	var requestSigned atomic.Bool
	data := `{"provider_batch_no":"PB-1","items":[` +
		`{"order_no":"O-1","status":"accepted"},` +
		`{"order_no":"O-2","status":"rejected","code":"4001","message":"收款账户异常"},` +
		`{"order_no":"O-3","status":"processing"}]}`
	responseBody := yunzhanghuSignedEnvelope(t, providerKey, "0000", "成功", data)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DealerID string `json:"dealer_id"`
			BatchNo  string `json:"batch_no"`
			Data     string `json:"data"`
			Sign     string `json:"sign"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("请求正文不是 JSON：%v", err)
		}
		if err := yunzhanghuVerify(map[string]any{
			"dealer_id": req.DealerID, "batch_no": req.BatchNo, "data": req.Data,
		}, req.Sign, &merchant.PublicKey); err == nil {
			requestSigned.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()

	db := openWithdrawalDB(t, "yzh_payout_batch_ok")
	defer db.Close()
	p := setupYunzhanghuProvider(t, db, server.URL, merchant, providerKey)
	result, err := p.SubmitBatch(t.Context(), payoutBatchRequest{BatchNo: "B-1", DealerID: "D1", Items: yunzhanghuBatchItems()})
	if err != nil {
		t.Fatalf("提交批次应成功：%v", err)
	}
	if !requestSigned.Load() {
		t.Fatal("请求未按商户私钥签名")
	}
	if result.ProviderBatchNo != "PB-1" || len(result.Accepted) != 1 || result.Accepted[0].OrderNo != "O-1" ||
		len(result.Rejected) != 1 || result.Rejected[0].OrderNo != "O-2" || result.Rejected[0].Code != "4001" ||
		len(result.Unknown) != 1 || result.Unknown[0] != "O-3" {
		t.Fatalf("批次结果解析不正确：%+v", result)
	}
}

// 明确业务拒绝码归为 rejected。
func TestYunzhanghuPayoutSubmitRejected(t *testing.T) {
	merchant, providerKey := yunzhanghuTestKeys(t)
	rejectedBody := yunzhanghuSignedEnvelope(t, providerKey, "4001", "余额不足", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(rejectedBody)
	}))
	defer server.Close()

	db := openWithdrawalDB(t, "yzh_payout_batch_rejected")
	defer db.Close()
	p := setupYunzhanghuProvider(t, db, server.URL, merchant, providerKey)
	_, err := p.SubmitBatch(t.Context(), payoutBatchRequest{BatchNo: "B-2", DealerID: "D1", Items: yunzhanghuBatchItems()})
	if !errors.Is(err, errPayoutProviderRejected) || errors.Is(err, errPayoutProviderUnknown) {
		t.Fatalf("明确业务拒绝应归 rejected，got %v", err)
	}
}

// 超时属于「结果未知」，绝不能当明确拒绝。
func TestYunzhanghuPayoutSubmitTimeoutUnknown(t *testing.T) {
	merchant, providerKey := yunzhanghuTestKeys(t)
	timeoutBody := yunzhanghuSignedEnvelope(t, providerKey, "0000", "成功", "{}")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(timeoutBody)
	}))
	defer server.Close()

	db := openWithdrawalDB(t, "yzh_payout_batch_timeout")
	defer db.Close()
	p := setupYunzhanghuProvider(t, db, server.URL, merchant, providerKey)
	p.client = &http.Client{Timeout: 30 * time.Millisecond}
	_, err := p.SubmitBatch(t.Context(), payoutBatchRequest{BatchNo: "B-3", DealerID: "D1", Items: yunzhanghuBatchItems()})
	if !errors.Is(err, errPayoutProviderUnknown) || errors.Is(err, errPayoutProviderRejected) {
		t.Fatalf("超时应归为结果未知，got %v", err)
	}
}

// ── 打款回调 ──

// seedYunzhanghuPayout 造「已审核通过 + 已提交打款」的提现执行单，并挂上云账户单号。
func seedYunzhanghuPayout(t *testing.T, db *ent.Client, userID int, key, orderNo string) (*ent.EsportsWithdrawal, *ent.EsportsWithdrawalPayout) {
	t.Helper()
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, userID, 10000)
	// 云账户渠道下提现要求已签约账户，先激活一个银行卡账户。
	if err := activatePlayerPayoutAccount(ctx, db, player.ID, "bank_card", "SIGN-"+key, "张三", "****1234"); err != nil {
		t.Fatalf("激活签约账户失败：%v", err)
	}
	item, err := applyWithdrawal(ctx, db, player.ID, validApply(key, 4000), time.Now())
	if err != nil {
		t.Fatalf("申请提现失败：%v", err)
	}
	if _, err = reviewWithdrawal(ctx, db, 7, item.ID, withdrawalReviewInput{Approve: true}, time.Now()); err != nil {
		t.Fatalf("审核通过失败：%v", err)
	}
	payout, err := requestWithdrawalPayout(ctx, db, 8, item.ID, requestWithdrawalPayoutInput{
		Channel: "bank_card", TransactionNo: "YZH-TEMP", AmountCents: 4000,
	}, time.Now())
	if err != nil {
		t.Fatalf("提交打款失败：%v", err)
	}
	db.EsportsWithdrawalPayout.UpdateOneID(payout.ID).
		SetProvider(payoutProviderYunzhanghu).SetProviderOrderNo(orderNo).SaveX(ctx)
	return db.EsportsWithdrawal.GetX(ctx, item.ID), db.EsportsWithdrawalPayout.GetX(ctx, payout.ID)
}

// postYunzhanghuCallback 以真实 Fiber 路由调用云账户回调 handler。
func postYunzhanghuCallback(t *testing.T, app *fiber.App, path string, body []byte) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("回调请求失败：%v", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取回调响应失败：%v", err)
	}
	return resp.StatusCode, string(payload)
}

// postYunzhanghuNotify 调用代发回调。
func postYunzhanghuNotify(t *testing.T, app *fiber.App, body []byte) (int, string) {
	t.Helper()
	return postYunzhanghuCallback(t, app, "/payment-notify/yunzhanghu", body)
}

// 正确签名 → 落账；重复回调 → 只落账一次；错误签名 → 不落账。
func TestYunzhanghuPayoutNotifySettleAndIdempotent(t *testing.T) {
	merchant, providerKey := yunzhanghuTestKeys(t)
	db := openWithdrawalDB(t, "yzh_payout_notify_paid")
	defer db.Close()
	setupYunzhanghuProvider(t, db, "https://payout.example.com", merchant, providerKey)
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	wd, payout := seedYunzhanghuPayout(t, db, 9501, "yzh-notify-paid", "YZH-ORDER-1")
	app := fiber.New()
	app.Post("/payment-notify/yunzhanghu", handleYunzhanghuPayoutNotify)

	body := yunzhanghuSignedEnvelope(t, providerKey, "0000", "成功",
		`{"order_no":"YZH-ORDER-1","status":"paid","paid_at":"2026-10-27 12:00:00"}`)
	status, respBody := postYunzhanghuNotify(t, app, body)
	if status != http.StatusOK || !strings.Contains(respBody, `"code":"SUCCESS"`) {
		t.Fatalf("正确签名应应答成功：status=%d body=%s", status, respBody)
	}
	got := db.EsportsWithdrawalPayout.GetX(t.Context(), payout.ID)
	if got.Status != esportswithdrawalpayout.StatusPaid || got.ProviderStatus != "paid" ||
		got.TransactionNo != "YZH-ORDER-1" || got.PaidAt == nil || got.PaidByAdminID != nil {
		t.Fatalf("回调成功后打款单落账不正确：%+v", got)
	}
	if reload := db.EsportsWithdrawal.GetX(t.Context(), wd.ID); reload.Status != esportswithdrawal.StatusPaid ||
		reload.PayoutRef != "YZH-ORDER-1" || reload.PaidAt == nil {
		t.Fatalf("回调成功后提现单未置 paid：%+v", reload)
	}
	settledVersion, settledAt := got.Version, *got.PaidAt

	// 重复回调：幂等应答成功，版本与到账时间不变。
	status, respBody = postYunzhanghuNotify(t, app, body)
	if status != http.StatusOK || !strings.Contains(respBody, `"code":"SUCCESS"`) {
		t.Fatalf("重复回调应幂等应答成功：status=%d body=%s", status, respBody)
	}
	got = db.EsportsWithdrawalPayout.GetX(t.Context(), payout.ID)
	if got.Version != settledVersion || !got.PaidAt.Equal(settledAt) {
		t.Fatalf("重复回调不应重复落账：version=%d→%d paidAt=%v→%v",
			settledVersion, got.Version, settledAt, got.PaidAt)
	}

	// 错误签名：应答失败且不修改任何业务状态。
	_, otherKey := yunzhanghuTestKeys(t)
	badBody := yunzhanghuSignedEnvelope(t, otherKey, "0000", "成功",
		`{"order_no":"YZH-ORDER-1","status":"paid"}`)
	status, respBody = postYunzhanghuNotify(t, app, badBody)
	if status != http.StatusBadRequest || !strings.Contains(respBody, `"code":"FAIL"`) {
		t.Fatalf("错误签名应应答失败：status=%d body=%s", status, respBody)
	}
	if got = db.EsportsWithdrawalPayout.GetX(t.Context(), payout.ID); got.Version != settledVersion {
		t.Fatalf("验签失败不得修改打款单：%+v", got)
	}
}

// 通知明确失败：打款单置 pay_failed + fail_code，提现单保持 approved。
func TestYunzhanghuPayoutNotifyFailed(t *testing.T) {
	merchant, providerKey := yunzhanghuTestKeys(t)
	db := openWithdrawalDB(t, "yzh_payout_notify_failed")
	defer db.Close()
	setupYunzhanghuProvider(t, db, "https://payout.example.com", merchant, providerKey)
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	wd, payout := seedYunzhanghuPayout(t, db, 9502, "yzh-notify-failed", "YZH-ORDER-2")
	app := fiber.New()
	app.Post("/payment-notify/yunzhanghu", handleYunzhanghuPayoutNotify)

	body := yunzhanghuSignedEnvelope(t, providerKey, "0000", "成功",
		`{"order_no":"YZH-ORDER-2","status":"failed","fail_code":"ACCOUNT_INVALID","msg":"收款账户异常"}`)
	status, respBody := postYunzhanghuNotify(t, app, body)
	if status != http.StatusOK || !strings.Contains(respBody, `"code":"SUCCESS"`) {
		t.Fatalf("失败通知也应答成功停止重试：status=%d body=%s", status, respBody)
	}
	got := db.EsportsWithdrawalPayout.GetX(t.Context(), payout.ID)
	if got.Status != esportswithdrawalpayout.StatusPayFailed || got.FailCode != "ACCOUNT_INVALID" ||
		got.ProviderStatus != "failed" {
		t.Fatalf("失败通知落账不正确：%+v", got)
	}
	if reload := db.EsportsWithdrawal.GetX(t.Context(), wd.ID); reload.Status != esportswithdrawal.StatusApproved {
		t.Fatalf("打款失败时提现单应保持 approved，got=%s", reload.Status)
	}
}

// 找不到打款记录：应答成功（避免无限重试）且不 panic。
func TestYunzhanghuPayoutNotifyUnknownOrderAcksSuccess(t *testing.T) {
	merchant, providerKey := yunzhanghuTestKeys(t)
	db := openWithdrawalDB(t, "yzh_payout_notify_unknown")
	defer db.Close()
	setupYunzhanghuProvider(t, db, "https://payout.example.com", merchant, providerKey)
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	app := fiber.New()
	app.Post("/payment-notify/yunzhanghu", handleYunzhanghuPayoutNotify)
	body := yunzhanghuSignedEnvelope(t, providerKey, "0000", "成功",
		`{"order_no":"NOT-EXIST","status":"paid"}`)
	status, respBody := postYunzhanghuNotify(t, app, body)
	if status != http.StatusOK || !strings.Contains(respBody, `"code":"SUCCESS"`) {
		t.Fatalf("未知单号应应答成功停止重试：status=%d body=%s", status, respBody)
	}
	if n := db.EsportsWithdrawalPayout.Query().CountX(t.Context()); n != 0 {
		t.Fatalf("未知单号不得写任何记录：%d", n)
	}
}

// ── 签约回调 ──

const yunzhanghuSignNotifyPath = "/payment-notify/yunzhanghu/sign"

// seedPendingPayoutAccount 造一条 pending 签约账户（模拟已跳转托管页但结果未回写）。
func seedPendingPayoutAccount(t *testing.T, db *ent.Client, playerID int, accountType string) {
	t.Helper()
	db.EsportsPlayerPayoutAccount.Create().
		SetPlayerID(playerID).SetProvider(payoutProviderYunzhanghu).
		SetAccountType(esportsplayerpayoutaccount.AccountType(accountType)).
		SetStatus(esportsplayerpayoutaccount.StatusPending).SaveX(t.Context())
}

// yunzhanghuSignBody 拼装签约回调报文（用云账户私钥签名）。
func yunzhanghuSignBody(t *testing.T, key *rsa.PrivateKey, payload string) []byte {
	t.Helper()
	return yunzhanghuSignedEnvelope(t, key, "0000", "成功", payload)
}

func yunzhanghuSignPayload(playerID int, accountType, signNo, accountName, accountNo, status string) string {
	return fmt.Sprintf(`{"player_id":%d,"account_type":%q,"sign_no":%q,"account_name":%q,"account_no":%q,"status":%q}`,
		playerID, accountType, signNo, accountName, accountNo, status)
}

// 正确签名 + success → 回填并置 active；重复回调幂等；错误签名不改任何状态。
func TestYunzhanghuSignNotifyActivateAndIdempotent(t *testing.T) {
	merchant, providerKey := yunzhanghuTestKeys(t)
	db := openWithdrawalDB(t, "yzh_sign_notify_ok")
	defer db.Close()
	setupYunzhanghuProvider(t, db, "https://payout.example.com", merchant, providerKey)
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	player := seedCatfoodPlayer(t, db, 9601, 1000)
	seedPendingPayoutAccount(t, db, player.ID, "bank_card")
	app := fiber.New()
	app.Post(yunzhanghuSignNotifyPath, handleYunzhanghuSignNotify)

	body := yunzhanghuSignBody(t, providerKey,
		yunzhanghuSignPayload(player.ID, "bank_card", "SIGN-YZH-1", "张三", "6222020202021234", "success"))
	status, respBody := postYunzhanghuCallback(t, app, yunzhanghuSignNotifyPath, body)
	if status != http.StatusOK || !strings.Contains(respBody, `"code":"SUCCESS"`) {
		t.Fatalf("正确签名应应答成功：status=%d body=%s", status, respBody)
	}
	got := payoutAccountOfPlayer(t, db, player.ID, "bank_card")
	if got.Status != esportsplayerpayoutaccount.StatusActive || got.ProviderSignNo != "SIGN-YZH-1" ||
		got.AccountName != "张三" || got.AccountNoMasked != "****1234" || got.SignedAt == nil {
		t.Fatalf("签约回调落库不正确：%+v", got)
	}
	version, signedAt := got.Version, *got.SignedAt

	// 重复回调：幂等应答成功，数据与版本不变。
	status, respBody = postYunzhanghuCallback(t, app, yunzhanghuSignNotifyPath, body)
	if status != http.StatusOK || !strings.Contains(respBody, `"code":"SUCCESS"`) {
		t.Fatalf("重复回调应幂等应答成功：status=%d body=%s", status, respBody)
	}
	if got = payoutAccountOfPlayer(t, db, player.ID, "bank_card"); got.Version != version ||
		!got.SignedAt.Equal(signedAt) || got.ProviderSignNo != "SIGN-YZH-1" {
		t.Fatalf("重复回调不应重复写：version=%d→%d signedAt=%v→%v", version, got.Version, signedAt, got.SignedAt)
	}

	// 错误签名：应答失败且账户保持 active 原值（签约号不被替换）。
	_, otherKey := yunzhanghuTestKeys(t)
	badBody := yunzhanghuSignBody(t, otherKey,
		yunzhanghuSignPayload(player.ID, "bank_card", "SIGN-FORGED", "李四", "6222020202025678", "success"))
	status, respBody = postYunzhanghuCallback(t, app, yunzhanghuSignNotifyPath, badBody)
	if status != http.StatusBadRequest || !strings.Contains(respBody, `"code":"FAIL"`) {
		t.Fatalf("错误签名应应答失败：status=%d body=%s", status, respBody)
	}
	if got = payoutAccountOfPlayer(t, db, player.ID, "bank_card"); got.ProviderSignNo != "SIGN-YZH-1" ||
		got.AccountName != "张三" || got.Version != version {
		t.Fatalf("验签失败不得修改签约账户：%+v", got)
	}
}

// status=failed → 不落库，账户仍 pending，应答成功停止重试。
func TestYunzhanghuSignNotifyFailedNoWrite(t *testing.T) {
	merchant, providerKey := yunzhanghuTestKeys(t)
	db := openWithdrawalDB(t, "yzh_sign_notify_failed")
	defer db.Close()
	setupYunzhanghuProvider(t, db, "https://payout.example.com", merchant, providerKey)
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	player := seedCatfoodPlayer(t, db, 9602, 1000)
	seedPendingPayoutAccount(t, db, player.ID, "bank_card")
	app := fiber.New()
	app.Post(yunzhanghuSignNotifyPath, handleYunzhanghuSignNotify)

	body := yunzhanghuSignBody(t, providerKey,
		yunzhanghuSignPayload(player.ID, "bank_card", "SIGN-FAILED", "张三", "6222020202021234", "failed"))
	status, respBody := postYunzhanghuCallback(t, app, yunzhanghuSignNotifyPath, body)
	if status != http.StatusOK || !strings.Contains(respBody, `"code":"SUCCESS"`) {
		t.Fatalf("签约失败通知也应答成功停止重试：status=%d body=%s", status, respBody)
	}
	got := payoutAccountOfPlayer(t, db, player.ID, "bank_card")
	if got.Status != esportsplayerpayoutaccount.StatusPending || got.ProviderSignNo != "" {
		t.Fatalf("status=failed 不得落库：%+v", got)
	}
}

// 非法 account_type → 应答成功但账户不变更。
func TestYunzhanghuSignNotifyInvalidAccountTypeAcksSuccess(t *testing.T) {
	merchant, providerKey := yunzhanghuTestKeys(t)
	db := openWithdrawalDB(t, "yzh_sign_notify_bad_type")
	defer db.Close()
	setupYunzhanghuProvider(t, db, "https://payout.example.com", merchant, providerKey)
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	player := seedCatfoodPlayer(t, db, 9603, 1000)
	app := fiber.New()
	app.Post(yunzhanghuSignNotifyPath, handleYunzhanghuSignNotify)

	body := yunzhanghuSignBody(t, providerKey,
		yunzhanghuSignPayload(player.ID, "wechat", "SIGN-WECHAT", "张三", "6222020202021234", "success"))
	status, respBody := postYunzhanghuCallback(t, app, yunzhanghuSignNotifyPath, body)
	if status != http.StatusOK || !strings.Contains(respBody, `"code":"SUCCESS"`) {
		t.Fatalf("非法账户类型应答成功避免重试风暴：status=%d body=%s", status, respBody)
	}
	if n := db.EsportsPlayerPayoutAccount.Query().CountX(t.Context()); n != 0 {
		t.Fatalf("非法账户类型不得落库：%d", n)
	}
}

// 未配置云账户 → 失败应答且不 panic、不写库。
func TestYunzhanghuSignNotifyUnconfiguredAcksFailure(t *testing.T) {
	merchant, _ := yunzhanghuTestKeys(t)
	db := openWithdrawalDB(t, "yzh_sign_notify_unconfigured")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	app := fiber.New()
	app.Post(yunzhanghuSignNotifyPath, handleYunzhanghuSignNotify)
	body := yunzhanghuSignBody(t, merchant,
		yunzhanghuSignPayload(1, "bank_card", "SIGN-ANY", "张三", "6222020202021234", "success"))
	status, respBody := postYunzhanghuCallback(t, app, yunzhanghuSignNotifyPath, body)
	if status != http.StatusBadRequest || !strings.Contains(respBody, `"code":"FAIL"`) {
		t.Fatalf("未配置云账户应应答失败：status=%d body=%s", status, respBody)
	}
	if n := db.EsportsPlayerPayoutAccount.Query().CountX(t.Context()); n != 0 {
		t.Fatalf("未配置云账户不得落库：%d", n)
	}
}

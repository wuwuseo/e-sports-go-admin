package esports

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/systemconfig"
)

// 在线收款渠道接入（§4.3）：
// 1. 渠道配置集中在系统配置项 app.esports.payment_gateways（JSON 对象，按渠道编码索引）；
// 2. 只有「后台已开通 + 配置完整 + 存在启用中的收款账户」的渠道才可发起收款，
//    任何一步不满足都返回 errPaymentGatewayUnavailable，严禁退化为假成功网关；
// 3. 回调验签通过后才进入 applyVerifiedPayment —— 全系统唯一的到账核销落账边界。

// paymentGatewaysConfigKey 在线收款渠道配置项键。
const paymentGatewaysConfigKey = "app.esports.payment_gateways"

const (
	// paymentGatewayKindHTTPHMAC 通用 HTTP + HMAC-SHA256 签名渠道。
	paymentGatewayKindHTTPHMAC = "http_hmac"
	// paymentGatewayTimeout 渠道下单请求超时。
	paymentGatewayTimeout = 10 * time.Second
	// paymentNotifyTolerance 回调时间戳允许的时钟偏移，超出即判定为重放。
	paymentNotifyTolerance = 5 * time.Minute
	// paymentSignatureVersion 签名版本前缀，便于渠道侧平滑升级算法。
	paymentSignatureVersion = "v1"
	// paymentGatewayMaxResponseBytes 渠道响应体读取上限，避免异常响应撑爆内存。
	paymentGatewayMaxResponseBytes = 1 << 20
)

// paymentGatewayConfig 单个渠道的接入配置。
//
// 配置示例（SystemConfig.key = app.esports.payment_gateways）：
//
//	{
//	  "wechat_work": {
//	    "kind": "http_hmac",
//	    "base_url": "https://pay.example.com",
//	    "app_id": "studio-esports",
//	    "app_secret": "******",
//	    "notify_url": "https://studio.example.com/api/v1/esports/payment-notify/wechat_work",
//	    "enabled": true
//	  }
//	}
type paymentGatewayConfig struct {
	Kind      string `json:"kind"`
	BaseURL   string `json:"base_url"`
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
	NotifyURL string `json:"notify_url"`
	Enabled   bool   `json:"enabled"`
}

// validate 校验 http_hmac 渠道必填字段；enabled=false 视为未开通。
func (c paymentGatewayConfig) validate() error {
	if !c.Enabled {
		return fmt.Errorf("%w：收款渠道未启用", errPaymentGatewayUnavailable)
	}
	if strings.TrimSpace(c.Kind) != paymentGatewayKindHTTPHMAC {
		return fmt.Errorf("%w：不支持的收款渠道类型 %q", errPaymentGatewayUnavailable, c.Kind)
	}
	baseURL, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" {
		return fmt.Errorf("%w：收款渠道 base_url 无效", errPaymentGatewayUnavailable)
	}
	if strings.TrimSpace(c.AppID) == "" || strings.TrimSpace(c.AppSecret) == "" {
		return fmt.Errorf("%w：收款渠道 app_id / app_secret 缺失", errPaymentGatewayUnavailable)
	}
	notifyURL, err := url.Parse(strings.TrimSpace(c.NotifyURL))
	if err != nil || (notifyURL.Scheme != "http" && notifyURL.Scheme != "https") || notifyURL.Host == "" {
		return fmt.Errorf("%w：收款渠道 notify_url 无效", errPaymentGatewayUnavailable)
	}
	return nil
}

// paymentGatewayConfigFor 读取单渠道配置。配置项缺失、JSON 非法、渠道不存在或字段不完整
// 一律返回 errPaymentGatewayUnavailable，绝不 panic，也绝不返回「可用」的默认配置。
func paymentGatewayConfigFor(ctx context.Context, db *ent.Client, channel string) (paymentGatewayConfig, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return paymentGatewayConfig{}, errPaymentGatewayUnavailable
	}
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(paymentGatewaysConfigKey)).Only(ctx)
	if ent.IsNotFound(err) {
		return paymentGatewayConfig{}, fmt.Errorf("%w：未配置在线收款渠道", errPaymentGatewayUnavailable)
	}
	if err != nil {
		return paymentGatewayConfig{}, err
	}
	gateways := map[string]paymentGatewayConfig{}
	if err = json.Unmarshal([]byte(strings.TrimSpace(item.Value)), &gateways); err != nil {
		return paymentGatewayConfig{}, fmt.Errorf("%w：收款渠道配置不是合法 JSON", errPaymentGatewayUnavailable)
	}
	cfg, ok := gateways[channel]
	if !ok {
		return paymentGatewayConfig{}, fmt.Errorf("%w：渠道 %s 未配置", errPaymentGatewayUnavailable, channel)
	}
	if err = cfg.validate(); err != nil {
		return paymentGatewayConfig{}, err
	}
	return cfg, nil
}

// gatewayFor 按渠道编码构造网关。渠道未开通（paymentChannelEnabled=false）或未配置时
// 返回 errPaymentGatewayUnavailable；不存在「空实现 / 假成功」的兜底分支。
func gatewayFor(ctx context.Context, db *ent.Client, channel string) (PaymentGateway, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" || !validPaymentChannel(channel) {
		return nil, fmt.Errorf("%w：未知收款渠道 %q", errPaymentGatewayUnavailable, channel)
	}
	if !paymentChannelEnabled(ctx, db, channel) {
		return nil, fmt.Errorf("%w：收款方式 %s 已停用", errPaymentGatewayUnavailable, channel)
	}
	cfg, err := paymentGatewayConfigFor(ctx, db, channel)
	if err != nil {
		return nil, err
	}
	switch cfg.Kind {
	case paymentGatewayKindHTTPHMAC:
		return &httpHMACGateway{
			channel: channel, baseURL: strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
			appID: strings.TrimSpace(cfg.AppID), appSecret: cfg.AppSecret,
			notifyURL: strings.TrimSpace(cfg.NotifyURL),
			client:    &http.Client{Timeout: paymentGatewayTimeout},
			now:       time.Now,
		}, nil
	default:
		return nil, fmt.Errorf("%w：不支持的收款渠道类型 %q", errPaymentGatewayUnavailable, cfg.Kind)
	}
}

// paymentSignatureCanonical 计算待签名串。
//
// 签名口径（http_hmac，双向一致）：
//  1. body_sha256 = hex(SHA-256(原始 body))，回调侧必须使用「收到的原始字节」，
//     不得先 JSON 反序列化再重排，避免字段顺序 / 空白差异导致验签漂移；
//  2. 待签名参数固定为 app_id / body_sha256 / timestamp（unix 秒），
//     按参数名字典序（ASCII 升序）拼接为 key=urlencode(value)，以 "&" 连接；
//  3. X-Pay-Signature = hex(HMAC-SHA256(app_secret, canonical))，随请求头下发；
//     回调验签时用同一 canonical 重算并做常量时间比较。
//
// 因此 body 与 timestamp 都参与签名：篡改任一方都会导致校验失败。
func paymentSignatureCanonical(appID, timestamp, bodySHA256 string) string {
	params := url.Values{}
	params.Set("app_id", appID)
	params.Set("body_sha256", bodySHA256)
	params.Set("timestamp", timestamp)
	return params.Encode() // url.Values.Encode 按键字典序输出
}

func paymentBodySHA256(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func paymentHMACHex(secret string, canonical string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

// paymentSignatureValue 生成回调校验入参使用的复合签名串：t=<unix>,v1=<hex>。
func paymentSignatureValue(secret, appID string, timestamp int64, payload []byte) string {
	ts := strconv.FormatInt(timestamp, 10)
	signature := paymentHMACHex(secret, paymentSignatureCanonical(appID, ts, paymentBodySHA256(payload)))
	return "t=" + ts + "," + paymentSignatureVersion + "=" + signature
}

// parsePaymentSignature 解析 t=<unix>,v1=<hex> 复合签名串。
func parsePaymentSignature(value string) (int64, string, error) {
	var timestamp int64
	var signature string
	for _, part := range strings.Split(strings.TrimSpace(value), ",") {
		key, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "t":
			parsed, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
			if err != nil {
				return 0, "", err
			}
			timestamp = parsed
		case paymentSignatureVersion:
			signature = strings.TrimSpace(val)
		}
	}
	if timestamp <= 0 || signature == "" {
		return 0, "", errors.New("签名串格式无效")
	}
	return timestamp, signature, nil
}

// httpHMACGateway 通用 HTTP + HMAC-SHA256 渠道适配器。
type httpHMACGateway struct {
	channel   string
	baseURL   string
	appID     string
	appSecret string
	notifyURL string
	client    *http.Client
	now       func() time.Time
}

type paymentGatewayCreateRequest struct {
	AppID       string `json:"app_id"`
	Channel     string `json:"channel"`
	IntentNo    string `json:"intent_no"`
	AmountCents int64  `json:"amount_cents"`
	ExpiresAt   string `json:"expires_at"`
	Subject     string `json:"subject"`
	NotifyURL   string `json:"notify_url"`
}

// CreatePayment 调渠道 POST {base_url}/payments 下单，返回渠道收银台 pay_url。
// 非 2xx 或 pay_url 为空都视为失败（不得当作成功继续放行核销）。
func (g *httpHMACGateway) CreatePayment(ctx context.Context, intentNo string,
	amountCents int64, expiresAt time.Time, subject string) (string, error) {
	if strings.TrimSpace(intentNo) == "" || amountCents <= 0 || expiresAt.IsZero() {
		return "", errInvalidInput
	}
	payload, err := json.Marshal(paymentGatewayCreateRequest{
		AppID: g.appID, Channel: g.channel, IntentNo: strings.TrimSpace(intentNo),
		AmountCents: amountCents, ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano),
		Subject: strings.TrimSpace(subject), NotifyURL: g.notifyURL,
	})
	if err != nil {
		return "", err
	}
	timestamp := g.now().Unix()
	timestampText := strconv.FormatInt(timestamp, 10)
	signature := paymentHMACHex(g.appSecret,
		paymentSignatureCanonical(g.appID, timestampText, paymentBodySHA256(payload)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/payments", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Pay-App-Id", g.appID)
	req.Header.Set("X-Pay-Timestamp", timestampText)
	req.Header.Set("X-Pay-Signature", "t="+timestampText+","+paymentSignatureVersion+"="+signature)
	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("收款渠道请求失败：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, paymentGatewayMaxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("收款渠道响应读取失败：%w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("收款渠道下单失败：HTTP %d %s", resp.StatusCode, truncateText(string(body), 200))
	}
	var out struct {
		PayURL string `json:"pay_url"`
	}
	if err = json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("收款渠道响应不是合法 JSON：%w", err)
	}
	payURL := strings.TrimSpace(out.PayURL)
	if payURL == "" {
		return "", errors.New("收款渠道未返回 pay_url")
	}
	return payURL, nil
}

// httpHMACNotification 渠道回调载荷。
type httpHMACNotification struct {
	IntentNo      string `json:"intent_no"`
	Channel       string `json:"channel"`
	TransactionNo string `json:"transaction_no"`
	AmountCents   int64  `json:"amount_cents"`
	ConfirmedAt   string `json:"confirmed_at"`
}

// VerifyNotification 校验回调：先验签（含 5 分钟防重放），再解析并校验收款要素。
// 签名不符 / 时间戳过期一律返回 errForbidden 类错误。
func (g *httpHMACGateway) VerifyNotification(ctx context.Context, payload []byte,
	signature string) (verifiedPayment, error) {
	_ = ctx
	timestamp, provided, err := parsePaymentSignature(signature)
	if err != nil || strings.TrimSpace(provided) == "" {
		return verifiedPayment{}, fmt.Errorf("%w：回调签名格式无效", errForbidden)
	}
	now := g.now()
	if skew := now.Sub(time.Unix(timestamp, 0)); skew > paymentNotifyTolerance || skew < -paymentNotifyTolerance {
		return verifiedPayment{}, fmt.Errorf("%w：回调时间戳超出 5 分钟窗口", errForbidden)
	}
	expected := paymentHMACHex(g.appSecret, paymentSignatureCanonical(g.appID,
		strconv.FormatInt(timestamp, 10), paymentBodySHA256(payload)))
	if !hmac.Equal([]byte(expected), []byte(provided)) {
		return verifiedPayment{}, fmt.Errorf("%w：回调签名校验失败", errForbidden)
	}
	var notice httpHMACNotification
	if err = json.Unmarshal(payload, &notice); err != nil {
		return verifiedPayment{}, fmt.Errorf("%w：回调内容不是合法 JSON", errInvalidInput)
	}
	notice.IntentNo = strings.TrimSpace(notice.IntentNo)
	notice.Channel = strings.TrimSpace(notice.Channel)
	notice.TransactionNo = strings.TrimSpace(notice.TransactionNo)
	if notice.IntentNo == "" || notice.Channel == "" || notice.TransactionNo == "" ||
		notice.AmountCents <= 0 || strings.TrimSpace(notice.ConfirmedAt) == "" {
		return verifiedPayment{}, errInvalidInput
	}
	confirmedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(notice.ConfirmedAt))
	if err != nil {
		return verifiedPayment{}, fmt.Errorf("%w：confirmed_at 不是合法 RFC3339 时间", errInvalidInput)
	}
	return verifiedPayment{
		IntentNo: notice.IntentNo, Channel: notice.Channel,
		TransactionNo: notice.TransactionNo, AmountCents: notice.AmountCents,
		ConfirmedAt: confirmedAt.UTC(),
	}, nil
}

// truncateText 截断渠道响应片段用于错误信息，避免把整段响应塞进日志。
func truncateText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

// orderPaymentStartView 发起收款返回视图。
type orderPaymentStartView struct {
	IntentNo    string    `json:"intent_no"`
	Channel     string    `json:"channel"`
	PayURL      string    `json:"pay_url"`
	AmountCents int64     `json:"amount_cents"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// startOrderPayment 客户为本人待付款订单发起在线收款（§4.3）：
// 校验归属 / 状态 / 渠道开通 / 启用账户 → 同一事务内保障支付意图（金额与报价确认书版本一致）
// → 调渠道下单拿 pay_url。
//
// pay_url 不落本地：渠道侧按 intent_no 可重取收银台地址，且 schema 不新增字段；
// 重复发起会复用同一 pending 意图，渠道按 intent_no 幂等返码即可。
func startOrderPayment(ctx context.Context, db *ent.Client, customerID, orderID int,
	channel string, now time.Time) (*orderPaymentStartView, error) {
	channel = strings.TrimSpace(channel)
	if customerID < 1 || orderID < 1 || !validPaymentChannel(channel) {
		return nil, errInvalidInput
	}
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.CustomerID != customerID {
		return nil, errForbidden
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid {
		return nil, errConflict
	}
	if now.UTC().After(order.PaymentDeadline) {
		return nil, fmt.Errorf("%w：订单已超过支付时限", errConflict)
	}
	gateway, err := gatewayFor(ctx, db, channel)
	if err != nil {
		return nil, err
	}
	accounts, err := listPaymentAccounts(ctx, db, channel, "active", 1)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("%w：渠道 %s 暂无启用中的收款账户", errPaymentGatewayUnavailable, channel)
	}
	sku, err := db.EsportsSku.Get(ctx, order.SkuID)
	if err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	intent, err := ensureOrderPaymentIntent(ctx, tx, order, channel, now)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	payURL, err := gateway.CreatePayment(ctx, intent.IntentNo, intent.AmountCents,
		intent.ExpiresAt, sku.Title+"（订单 "+order.OrderNo+"）")
	if err != nil {
		return nil, err
	}
	return &orderPaymentStartView{
		IntentNo: intent.IntentNo, Channel: channel, PayURL: payURL,
		AmountCents: intent.AmountCents, ExpiresAt: intent.ExpiresAt,
	}, nil
}

// handlePaymentNotification 在线渠道回调验签 + 核销（§4.3）：
// 取网关 → 验签（签名 / 5 分钟窗口）→ 校验渠道一致 → applyVerifiedPayment。
// 幂等由 applyVerifiedPayment 的回执唯一索引与 priorReceipt 承接：同一 transaction_no
// 重放返回既有 outcome；同流水号不同金额等冲突返回 errConflict。
func handlePaymentNotification(ctx context.Context, db *ent.Client, channel string,
	body []byte, signature string, now time.Time) (*paymentOutcome, error) {
	channel = strings.TrimSpace(channel)
	if !validPaymentChannel(channel) || len(bytes.TrimSpace(body)) == 0 ||
		strings.TrimSpace(signature) == "" {
		return nil, errInvalidInput
	}
	gateway, err := gatewayFor(ctx, db, channel)
	if err != nil {
		return nil, err
	}
	payment, err := gateway.VerifyNotification(ctx, body, signature)
	if err != nil {
		return nil, err
	}
	if payment.Channel != channel {
		return nil, fmt.Errorf("%w：回调渠道与验签渠道不一致", errForbidden)
	}
	if !now.IsZero() && payment.ConfirmedAt.After(now.Add(paymentNotifyTolerance)) {
		return nil, fmt.Errorf("%w：回调到账时间晚于当前时间过多", errForbidden)
	}
	return applyVerifiedPayment(ctx, db, payment)
}

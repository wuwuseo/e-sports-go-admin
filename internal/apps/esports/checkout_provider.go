package esports

import (
	"bytes"
	"context"
	"crypto"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/systemconfig"
)

// 聚合收款服务商适配层。
//
// 设计口径（用户确认的业务规格）：
//  1. 本站为订单 / 充值 / 礼物生成「按单通用入口码」，入口码只承载 checkout 身份；
//  2. 扫码页按微信 / 支付宝环境向服务商创建该笔交易（CreatePayment），返回收银台地址 / 二维码内容；
//  3. 只有服务商验签回调（VerifyNotification）或服务端查单（QueryPayment）确认成功才入账，
//     再由 checkout.go 分派到 applyVerifiedPayment / settleRechargeIntent / 礼物核销链路；
//  4. 密钥只从系统配置 app.esports.payment_providers 读取，任何接口响应与错误信息都不得回显。
//
// 协议来源（2026-10 按公开文档字段映射实现，未与真实商户联调，上线前必须用沙箱商户回归）：
//   - 收钱吧：预下单 POST /upay/v2/precreate、查单 POST /upay/v2/query、WAP 支付
//     https://open.shouqianba.com/api-docs/payment/141（142 WAP 与查单）；
//     签名口径：sign = upper(MD5(JSON 原文 + terminal_key))，响应同样带 sign 供商户验签；
//   - 汇付天下 SPIN：聚合正扫 POST /top/trans/pullPayInfo
//     https://spin.cloudpnr.com/topds/aggregatePullPay.html，异步通知
//     https://spin.cloudpnr.com/topds/device/messageApiIndex.html；
//     签名口径：sign_type=RSA2，参数按字典序拼接后 SHA256withRSA 签名，Base64 传输，
//     响应 / 通知用汇付公钥验签。
//
// 两家服务商的商户单号（merchant_no）都是本站生成且全站唯一；服务商交易号（provider_trade_no）
// 与 provider 组成唯一键，用于「同一笔交易不会被两个业务单核销」的兜底。

const (
	// paymentProvidersConfigKey 聚合收款服务商配置项键（只在这里保存密钥）。
	paymentProvidersConfigKey = "app.esports.payment_providers"
	// paymentProviderHuifuPrivateKeyConfigKey / PublicKey：汇付 RSA 密钥体积超过
	// SystemConfig.value 的 2048 字符上限，单独成键存放（2048 位 RSA 私钥 PEM 约 1.7KB）。
	// payment_providers.huifu.private_key / public_key 仍可作为内联覆盖（仅适合测试短密钥）。
	paymentProviderHuifuPrivateKeyConfigKey = "app.esports.payment_provider_huifu_private_key"
	paymentProviderHuifuPublicKeyConfigKey  = "app.esports.payment_provider_huifu_public_key"

	checkoutProviderShouqianba = "shouqianba"
	checkoutProviderHuifu      = "huifu"

	checkoutProviderTimeout          = 10 * time.Second
	checkoutProviderMaxResponseBytes = 1 << 20

	defaultShouqianbaBaseURL = "https://open.shouqianba.com"
	defaultHuifuBaseURL      = "https://spin.cloudpnr.com"

	shouqianbaNotifyPath = "/payment-notify/shouqianba"
	huifuNotifyPath      = "/payment-notify/huifu"
)

// errCheckoutProviderRejected 服务商「明确拒绝受理」：只有这种错误才允许把尝试置 failed 并
// 按 priority 切到下一家。网络超时 / 响应读取失败属于「受理结果未知」，尝试保持 creating，
// 下次请求用同一商户单号重试或由查单补偿收敛，绝不静默改投另一家。
var errCheckoutProviderRejected = errors.New("服务商明确拒绝受理")

// checkoutPaymentRequest 向服务商创建一笔交易。
type checkoutPaymentRequest struct {
	MerchantNo  string
	PayChannel  string
	AmountCents int64
	Subject     string
	ExpiresAt   time.Time
	NotifyURL   string
}

// checkoutPaymentResult 服务商受理结果：收银台地址与二维码内容至少一个非空。
type checkoutPaymentResult struct {
	ProviderTradeNo string
	PayURL          string
	QRPayload       string
}

// checkoutNotification 服务商回调 / 查单的统一结果。
// Paid=true 才允许进入结算；Status 保留服务商原始状态便于异常排查（不含密钥）。
type checkoutNotification struct {
	Provider        string
	MerchantNo      string
	ProviderTradeNo string
	AmountCents     int64
	PayChannel      string
	Status          string
	Paid            bool
	PaidAt          time.Time
}

// checkoutProvider 是服务商适配器契约：创建交易、查单、验签回调、回调应答。
type checkoutProvider interface {
	Name() string
	CreatePayment(ctx context.Context, req checkoutPaymentRequest) (*checkoutPaymentResult, error)
	QueryPayment(ctx context.Context, merchantNo string) (*checkoutNotification, error)
	VerifyNotification(ctx context.Context, headers map[string]string, body []byte) (*checkoutNotification, error)
	NotifyAck(ok bool) (status int, body string)
}

// ── 配置 ──

// shouqianbaProviderConfig 收钱吧接入配置（app.esports.payment_providers.shouqianba）。
// merchant_no=终端号 terminal_sn；app_id=服务商分配的应用号；app_secret=终端密钥 terminal_key（签名用）。
type shouqianbaProviderConfig struct {
	Enabled    bool   `json:"enabled"`
	MerchantNo string `json:"merchant_no"`
	AppID      string `json:"app_id"`
	AppSecret  string `json:"app_secret"`
	NotifyBase string `json:"notify_base"`
	// BaseURL 仅用于沙箱 / 联调覆盖，缺省走官方地址。
	BaseURL string `json:"base_url"`
}

func (c shouqianbaProviderConfig) configured() bool {
	return c.Enabled && strings.TrimSpace(c.MerchantNo) != "" &&
		strings.TrimSpace(c.AppSecret) != "" && strings.TrimSpace(c.NotifyBase) != ""
}

// huifuProviderConfig 汇付天下 SPIN 接入配置（app.esports.payment_providers.huifu）。
// merchant_no=huifu_id；sys_id / product_id 为接入方标识；private_key 为商户 RSA 私钥 PEM，
// public_key 为汇付 RSA 公钥 PEM（验签回调与查单响应）。
type huifuProviderConfig struct {
	Enabled    bool   `json:"enabled"`
	MerchantNo string `json:"merchant_no"`
	SysID      string `json:"sys_id"`
	ProductID  string `json:"product_id"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	NotifyBase string `json:"notify_base"`
	BaseURL    string `json:"base_url"`
}

func (c huifuProviderConfig) configured() bool {
	return c.Enabled && strings.TrimSpace(c.MerchantNo) != "" &&
		strings.TrimSpace(c.PrivateKey) != "" && strings.TrimSpace(c.PublicKey) != "" &&
		strings.TrimSpace(c.NotifyBase) != ""
}

// paymentProvidersConfig 聚合收款服务商配置根对象。
// priority 决定「同一笔业务单在服务商受理前」的尝试顺序；已受理的尝试不允许改投另一家。
type paymentProvidersConfig struct {
	Shouqianba shouqianbaProviderConfig `json:"shouqianba"`
	Huifu      huifuProviderConfig      `json:"huifu"`
	Priority   []string                 `json:"priority"`
}

// providerPriority 返回去重后的服务商顺序；未配置时按收钱吧 → 汇付天下。
func (c paymentProvidersConfig) providerPriority() []string {
	known := map[string]bool{checkoutProviderShouqianba: true, checkoutProviderHuifu: true}
	seen := map[string]bool{}
	out := make([]string, 0, 2)
	for _, raw := range c.Priority {
		name := strings.ToLower(strings.TrimSpace(raw))
		if !known[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range []string{checkoutProviderShouqianba, checkoutProviderHuifu} {
		if !seen[name] {
			out = append(out, name)
		}
	}
	return out
}

// loadPaymentProvidersConfig 读取服务商配置。配置行缺失返回空配置（全部未启用），
// JSON 非法返回明确错误，绝不把「读坏了」当成「可用」。
func loadPaymentProvidersConfig(ctx context.Context, db *ent.Client) (paymentProvidersConfig, error) {
	var cfg paymentProvidersConfig
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(paymentProvidersConfigKey)).Only(ctx)
	if ent.IsNotFound(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	raw := strings.TrimSpace(item.Value)
	if raw == "" {
		return cfg, nil
	}
	if err = json.Unmarshal([]byte(raw), &cfg); err != nil {
		return paymentProvidersConfig{}, fmt.Errorf("%w：聚合收款服务商配置不是合法 JSON", errPaymentGatewayUnavailable)
	}
	// 汇付 RSA 密钥优先取内联值（测试短密钥），缺省回落到单独的系统配置键
	// （2048 位 RSA PEM 超出单行 value 长度上限）。
	if strings.TrimSpace(cfg.Huifu.PrivateKey) == "" {
		cfg.Huifu.PrivateKey = systemConfigValue(ctx, db, paymentProviderHuifuPrivateKeyConfigKey)
	}
	if strings.TrimSpace(cfg.Huifu.PublicKey) == "" {
		cfg.Huifu.PublicKey = systemConfigValue(ctx, db, paymentProviderHuifuPublicKeyConfigKey)
	}
	return cfg, nil
}

// systemConfigValue 读取单个系统配置项文本；缺失 / 读取失败返回空串。
func systemConfigValue(ctx context.Context, db *ent.Client, key string) string {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(item.Value)
}

// maskedPaymentProviderView 服务商配置的脱敏视图：只给「是否已配置」，不回显任何密钥。
type maskedPaymentProviderView struct {
	Provider             string `json:"provider"`
	Enabled              bool   `json:"enabled"`
	Configured           bool   `json:"configured"`
	MerchantConfigured   bool   `json:"merchant_configured"`
	AppIDConfigured      bool   `json:"app_id_configured"`
	SecretConfigured     bool   `json:"secret_configured"`
	PrivateKeyConfigured bool   `json:"private_key_configured"`
	PublicKeyConfigured  bool   `json:"public_key_configured"`
	NotifyBase           string `json:"notify_base"`
	BaseURL              string `json:"base_url"`
}

// paymentProvidersView 后台只读视图（GET /admin/payment-providers）。
type paymentProvidersView struct {
	Priority  []string                    `json:"priority"`
	Providers []maskedPaymentProviderView `json:"providers"`
	Channels  map[string]bool             `json:"channels"`
}

// maskedPaymentProviders 组装脱敏视图；NotifyBase / BaseURL 不是密钥，可回显便于核对回调地址。
func maskedPaymentProviders(cfg paymentProvidersConfig) paymentProvidersView {
	view := paymentProvidersView{
		Priority:  cfg.providerPriority(),
		Providers: make([]maskedPaymentProviderView, 0, 2),
		Channels:  map[string]bool{"wechat": false, "alipay": false},
	}
	if cfg.Shouqianba.Enabled || strings.TrimSpace(cfg.Shouqianba.MerchantNo) != "" ||
		strings.TrimSpace(cfg.Shouqianba.AppSecret) != "" {
		view.Providers = append(view.Providers, maskedPaymentProviderView{
			Provider: checkoutProviderShouqianba, Enabled: cfg.Shouqianba.Enabled,
			Configured:         cfg.Shouqianba.configured(),
			MerchantConfigured: strings.TrimSpace(cfg.Shouqianba.MerchantNo) != "",
			AppIDConfigured:    strings.TrimSpace(cfg.Shouqianba.AppID) != "",
			SecretConfigured:   strings.TrimSpace(cfg.Shouqianba.AppSecret) != "",
			NotifyBase:         strings.TrimSpace(cfg.Shouqianba.NotifyBase),
			BaseURL:            strings.TrimSpace(cfg.Shouqianba.BaseURL),
		})
	}
	if cfg.Huifu.Enabled || strings.TrimSpace(cfg.Huifu.MerchantNo) != "" ||
		strings.TrimSpace(cfg.Huifu.PrivateKey) != "" {
		view.Providers = append(view.Providers, maskedPaymentProviderView{
			Provider: checkoutProviderHuifu, Enabled: cfg.Huifu.Enabled,
			Configured:           cfg.Huifu.configured(),
			MerchantConfigured:   strings.TrimSpace(cfg.Huifu.MerchantNo) != "",
			AppIDConfigured:      strings.TrimSpace(cfg.Huifu.SysID) != "" || strings.TrimSpace(cfg.Huifu.ProductID) != "",
			PrivateKeyConfigured: strings.TrimSpace(cfg.Huifu.PrivateKey) != "",
			PublicKeyConfigured:  strings.TrimSpace(cfg.Huifu.PublicKey) != "",
			NotifyBase:           strings.TrimSpace(cfg.Huifu.NotifyBase),
			BaseURL:              strings.TrimSpace(cfg.Huifu.BaseURL),
		})
	}
	if len(availableProviderNames(cfg)) > 0 {
		view.Channels["wechat"] = true
		view.Channels["alipay"] = true
	}
	return view
}

// availableProviderNames 返回已启用且配置完整的服务商（按 priority 顺序）。
func availableProviderNames(cfg paymentProvidersConfig) []string {
	names := make([]string, 0, 2)
	for _, name := range cfg.providerPriority() {
		switch name {
		case checkoutProviderShouqianba:
			if cfg.Shouqianba.configured() {
				names = append(names, name)
			}
		case checkoutProviderHuifu:
			if cfg.Huifu.configured() {
				names = append(names, name)
			}
		}
	}
	return names
}

// checkoutProviderFactory 按名称构造适配器；未启用 / 配置不完整返回 errPaymentGatewayUnavailable。
func checkoutProviderFactory(cfg paymentProvidersConfig, name string) (checkoutProvider, error) {
	switch name {
	case checkoutProviderShouqianba:
		if !cfg.Shouqianba.configured() {
			return nil, fmt.Errorf("%w：收钱吧聚合收款未启用或配置不完整", errPaymentGatewayUnavailable)
		}
		return newShouqianbaProvider(cfg.Shouqianba), nil
	case checkoutProviderHuifu:
		if !cfg.Huifu.configured() {
			return nil, fmt.Errorf("%w：汇付天下聚合收款未启用或配置不完整", errPaymentGatewayUnavailable)
		}
		return newHuifuProvider(cfg.Huifu)
	default:
		return nil, fmt.Errorf("%w：未知聚合收款服务商 %q", errPaymentGatewayUnavailable, name)
	}
}

// checkoutProviderFor 读取配置并构造指定服务商；配置损坏 / 未启用都明确失败。
func checkoutProviderFor(ctx context.Context, db *ent.Client, name string) (checkoutProvider, error) {
	cfg, err := loadPaymentProvidersConfig(ctx, db)
	if err != nil {
		return nil, err
	}
	return checkoutProviderFactory(cfg, strings.ToLower(strings.TrimSpace(name)))
}

// availableCheckoutProviders 按优先级返回全部可用服务商。
func availableCheckoutProviders(ctx context.Context, db *ent.Client) ([]checkoutProvider, error) {
	cfg, err := loadPaymentProvidersConfig(ctx, db)
	if err != nil {
		return nil, err
	}
	providers := make([]checkoutProvider, 0, 2)
	for _, name := range availableProviderNames(cfg) {
		provider, buildErr := checkoutProviderFactory(cfg, name)
		if buildErr != nil {
			return nil, buildErr
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

// checkoutChannelAvailability 扫码页可用的支付渠道：任一可用服务商都同时支持微信 / 支付宝。
func checkoutChannelAvailability(ctx context.Context, db *ent.Client) (wechat, alipay bool, providers []string, err error) {
	cfg, err := loadPaymentProvidersConfig(ctx, db)
	if err != nil {
		return false, false, nil, err
	}
	providers = availableProviderNames(cfg)
	usable := len(providers) > 0
	return usable, usable, providers, nil
}

// ── 收钱吧 ──

type shouqianbaProvider struct {
	baseURL     string
	merchantNo  string
	appID       string
	terminalKey string
	notifyBase  string
	client      *http.Client
	now         func() time.Time
}

func newShouqianbaProvider(cfg shouqianbaProviderConfig) *shouqianbaProvider {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultShouqianbaBaseURL
	}
	return &shouqianbaProvider{
		baseURL: baseURL, merchantNo: strings.TrimSpace(cfg.MerchantNo),
		appID: strings.TrimSpace(cfg.AppID), terminalKey: cfg.AppSecret,
		notifyBase: strings.TrimRight(strings.TrimSpace(cfg.NotifyBase), "/"),
		client:     &http.Client{Timeout: checkoutProviderTimeout},
		now:        time.Now,
	}
}

func (p *shouqianbaProvider) Name() string { return checkoutProviderShouqianba }

func (p *shouqianbaProvider) notifyURL() string { return p.notifyBase + shouqianbaNotifyPath }

// shouqianbaPayway 收钱吧支付方式编码：1=微信，3=支付宝。
func shouqianbaPayway(payChannel string) string {
	if payChannel == "alipay" {
		return "3"
	}
	return "1"
}

// shouqianbaPayChannel 把收钱吧 payway 还原为本站 pay_channel；未知返回空串。
func shouqianbaPayChannel(payway string) string {
	switch strings.TrimSpace(payway) {
	case "1":
		return "wechat"
	case "3":
		return "alipay"
	default:
		return ""
	}
}

func shouqianbaMD5(payload []byte, key string) string {
	sum := md5.Sum(append(append([]byte{}, payload...), []byte(key)...))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// shouqianbaSignedBody 对「除 sign 外的字段」按 Go map 的稳定键序序列化后签名，再附加 sign 字段。
// 签名串口径与公开文档一致：upper(MD5(bodyJSON + terminal_key))。
func shouqianbaSignedBody(fields map[string]any, key string) ([]byte, error) {
	delete(fields, "sign")
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	fields["sign"] = shouqianbaMD5(payload, key)
	return json.Marshal(fields)
}

// shouqianbaVerifySign 校验服务商响应 / 通知：去掉 sign 后重新序列化并重算 MD5。
func shouqianbaVerifySign(raw []byte, key string) error {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("%w：服务商内容不是合法 JSON", errInvalidInput)
	}
	provided, _ := fields["sign"].(string)
	if strings.TrimSpace(provided) == "" {
		return fmt.Errorf("%w：服务商内容缺少签名", errForbidden)
	}
	delete(fields, "sign")
	payload, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("%w：服务商内容无法重新序列化", errInvalidInput)
	}
	expected := shouqianbaMD5(payload, key)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(strings.ToUpper(strings.TrimSpace(provided)))) != 1 {
		return fmt.Errorf("%w：服务商签名校验失败", errForbidden)
	}
	return nil
}

// shouqianbaEnvelope 收钱吧统一响应外壳。
type shouqianbaEnvelope struct {
	ResultCode   string `json:"result_code"`
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
	BizResponse  struct {
		ResultCode   string `json:"result_code"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
		Data         struct {
			Sn           string `json:"sn"`
			ClientSn     string `json:"client_sn"`
			OrderStatus  string `json:"order_status"`
			TotalAmount  string `json:"total_amount"`
			QrCode       string `json:"qr_code"`
			WapPayURL    string `json:"wap_pay_url"`
			Payway       string `json:"payway"`
			Status       string `json:"status"`
			Ctime        string `json:"ctime"`
			ErrorMessage string `json:"error_message"`
		} `json:"data"`
	} `json:"biz_response"`
}

func (p *shouqianbaProvider) post(ctx context.Context, path string, fields map[string]any) (*shouqianbaEnvelope, error) {
	body, err := shouqianbaSignedBody(fields, p.terminalKey)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("收钱吧请求失败：%w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, checkoutProviderMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("收钱吧响应读取失败：%w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("收钱吧接口失败：HTTP %d %s", resp.StatusCode, truncateText(string(raw), 200))
	}
	if err = shouqianbaVerifySign(raw, p.terminalKey); err != nil {
		return nil, err
	}
	var envelope shouqianbaEnvelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("收钱吧响应不是合法 JSON：%w", err)
	}
	if envelope.ResultCode != "" && envelope.ResultCode != "200" {
		return nil, fmt.Errorf("%w：收钱吧接口返回失败：%s %s", errCheckoutProviderRejected,
			envelope.ErrorCode, truncateText(envelope.ErrorMessage, 120))
	}
	return &envelope, nil
}

// CreatePayment 预下单：POST /upay/v2/precreate，返回二维码内容（正扫）与 WAP 收银台地址（如有）。
func (p *shouqianbaProvider) CreatePayment(ctx context.Context, req checkoutPaymentRequest) (*checkoutPaymentResult, error) {
	if strings.TrimSpace(req.MerchantNo) == "" || req.AmountCents <= 0 || req.PayChannel == "" {
		return nil, errInvalidInput
	}
	envelope, err := p.post(ctx, "/upay/v2/precreate", map[string]any{
		"terminal_sn":  p.merchantNo,
		"client_sn":    req.MerchantNo,
		"total_amount": strconv.FormatInt(req.AmountCents, 10),
		"subject":      truncateText(req.Subject, 120),
		"operator":     "esports",
		"notify_url":   p.notifyURL(),
		"payway":       shouqianbaPayway(req.PayChannel),
	})
	if err != nil {
		return nil, err
	}
	data := envelope.BizResponse.Data
	resultCode := strings.TrimSpace(envelope.BizResponse.ResultCode)
	if resultCode != "" && !strings.Contains(resultCode, "SUCCESS") {
		return nil, fmt.Errorf("%w：收钱吧预下单失败：%s %s", errCheckoutProviderRejected,
			envelope.BizResponse.ErrorCode,
			truncateText(envelope.BizResponse.ErrorMessage+data.ErrorMessage, 120))
	}
	if strings.TrimSpace(data.QrCode) == "" && strings.TrimSpace(data.WapPayURL) == "" {
		return nil, fmt.Errorf("%w：收钱吧未返回二维码或收银台地址", errPaymentGatewayUnavailable)
	}
	return &checkoutPaymentResult{
		ProviderTradeNo: strings.TrimSpace(data.Sn),
		PayURL:          strings.TrimSpace(data.WapPayURL),
		QRPayload:       strings.TrimSpace(data.QrCode),
	}, nil
}

// QueryPayment 查单：POST /upay/v2/query。PAID 才算成功；CREATED 继续等待。
func (p *shouqianbaProvider) QueryPayment(ctx context.Context, merchantNo string) (*checkoutNotification, error) {
	if strings.TrimSpace(merchantNo) == "" {
		return nil, errInvalidInput
	}
	envelope, err := p.post(ctx, "/upay/v2/query", map[string]any{
		"terminal_sn": p.merchantNo,
		"client_sn":   merchantNo,
	})
	if err != nil {
		return nil, err
	}
	data := envelope.BizResponse.Data
	status := strings.ToUpper(strings.TrimSpace(data.OrderStatus))
	notice := &checkoutNotification{
		Provider: checkoutProviderShouqianba, MerchantNo: strings.TrimSpace(data.ClientSn),
		ProviderTradeNo: strings.TrimSpace(data.Sn),
		PayChannel:      shouqianbaPayChannel(data.Payway),
		Status:          status, PaidAt: p.now().UTC(),
	}
	if notice.MerchantNo == "" {
		notice.MerchantNo = merchantNo
	}
	notice.AmountCents, _ = strconv.ParseInt(strings.TrimSpace(data.TotalAmount), 10, 64)
	notice.Paid = status == "PAID"
	if ctime := strings.TrimSpace(data.Ctime); ctime != "" {
		if ts, parseErr := strconv.ParseInt(ctime, 10, 64); parseErr == nil && ts > 0 {
			notice.PaidAt = time.Unix(ts, 0).UTC()
		}
	}
	return notice, nil
}

// VerifyNotification 收钱吧异步通知：验签后按 status=PAID 判定成功。
func (p *shouqianbaProvider) VerifyNotification(ctx context.Context, headers map[string]string, body []byte) (*checkoutNotification, error) {
	_ = ctx
	_ = headers
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errInvalidInput
	}
	if err := shouqianbaVerifySign(body, p.terminalKey); err != nil {
		return nil, err
	}
	var notice struct {
		ClientSn    string `json:"client_sn"`
		Sn          string `json:"sn"`
		Status      string `json:"status"`
		TotalAmount string `json:"total_amount"`
		Payway      string `json:"payway"`
		Ctime       string `json:"ctime"`
	}
	if err := json.Unmarshal(body, &notice); err != nil {
		return nil, fmt.Errorf("%w：收钱吧通知不是合法 JSON", errInvalidInput)
	}
	result := &checkoutNotification{
		Provider: checkoutProviderShouqianba, MerchantNo: strings.TrimSpace(notice.ClientSn),
		ProviderTradeNo: strings.TrimSpace(notice.Sn),
		PayChannel:      shouqianbaPayChannel(notice.Payway),
		Status:          strings.ToUpper(strings.TrimSpace(notice.Status)),
		Paid:            strings.EqualFold(strings.TrimSpace(notice.Status), "PAID"),
		PaidAt:          p.now().UTC(),
	}
	result.AmountCents, _ = strconv.ParseInt(strings.TrimSpace(notice.TotalAmount), 10, 64)
	if ctime := strings.TrimSpace(notice.Ctime); ctime != "" {
		if ts, parseErr := strconv.ParseInt(ctime, 10, 64); parseErr == nil && ts > 0 {
			result.PaidAt = time.Unix(ts, 0).UTC()
		}
	}
	if result.MerchantNo == "" || result.ProviderTradeNo == "" || result.AmountCents <= 0 {
		return nil, errInvalidInput
	}
	return result, nil
}

// NotifyAck 收钱吧要求回调应答 "SUCCESS"（非 SUCCESS 会重试）。
func (p *shouqianbaProvider) NotifyAck(ok bool) (int, string) {
	if ok {
		return http.StatusOK, "SUCCESS"
	}
	return http.StatusInternalServerError, "FAIL"
}

// ── 汇付天下 SPIN ──

type huifuProvider struct {
	baseURL    string
	merchantNo string
	sysID      string
	productID  string
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	notifyBase string
	client     *http.Client
	now        func() time.Time
}

func newHuifuProvider(cfg huifuProviderConfig) (*huifuProvider, error) {
	privateKey, err := parseRSAPrivateKeyPEM(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("%w：汇付天下商户私钥无法解析", errPaymentGatewayUnavailable)
	}
	publicKey, err := parseRSAPublicKeyPEM(cfg.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w：汇付天下公钥无法解析", errPaymentGatewayUnavailable)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultHuifuBaseURL
	}
	return &huifuProvider{
		baseURL: baseURL, merchantNo: strings.TrimSpace(cfg.MerchantNo),
		sysID: strings.TrimSpace(cfg.SysID), productID: strings.TrimSpace(cfg.ProductID),
		privateKey: privateKey, publicKey: publicKey,
		notifyBase: strings.TrimRight(strings.TrimSpace(cfg.NotifyBase), "/"),
		client:     &http.Client{Timeout: checkoutProviderTimeout},
		now:        time.Now,
	}, nil
}

func (p *huifuProvider) Name() string { return checkoutProviderHuifu }

func (p *huifuProvider) notifyURL() string { return p.notifyBase + huifuNotifyPath }

func parseRSAPrivateKeyPEM(raw string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(raw)))
	if block == nil {
		return nil, errors.New("私钥不是 PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("私钥不是 RSA")
	}
	return key, nil
}

func parseRSAPublicKeyPEM(raw string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(raw)))
	if block == nil {
		return nil, errors.New("公钥不是 PEM")
	}
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PublicKey); ok {
			return rsaKey, nil
		}
		return nil, errors.New("公钥不是 RSA")
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("证书公钥不是 RSA")
	}
	return rsaKey, nil
}

// huifuCanonical 汇付签名待签串：参数名 ASCII 升序，剔除 sign / sign_type 与空值，
// 以 key=value 用 & 连接。非标量（嵌套对象）不参与签名，避免序列化差异导致验签漂移。
func huifuCanonical(fields map[string]any) string {
	keys := make([]string, 0, len(fields))
	for key, value := range fields {
		switch value.(type) {
		case string, json.Number, float64, bool, nil:
		default:
			continue
		}
		switch key {
		case "sign", "sign_type":
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := strings.TrimSpace(fmt.Sprint(fields[key]))
		if value == "" {
			continue
		}
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, "&")
}

func huifuSign(fields map[string]any, key *rsa.PrivateKey) (string, error) {
	digest := sha256.Sum256([]byte(huifuCanonical(fields)))
	signature, err := rsa.SignPKCS1v15(nil, key, cryptoSHA256, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func huifuVerify(fields map[string]any, signature string, key *rsa.PublicKey) error {
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return fmt.Errorf("%w：汇付天下内容缺少签名", errForbidden)
	}
	raw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("%w：汇付天下签名不是合法 Base64", errForbidden)
	}
	digest := sha256.Sum256([]byte(huifuCanonical(fields)))
	if err = rsa.VerifyPKCS1v15(key, cryptoSHA256, digest[:], raw); err != nil {
		return fmt.Errorf("%w：汇付天下签名校验失败", errForbidden)
	}
	return nil
}

// cryptoSHA256 引用标准库常量，避免在签名与验签两处各写一份。
const cryptoSHA256 = crypto.SHA256

// huifuAmountString 分 → 元字符串（两位小数）。
func huifuAmountString(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// huifuAmountCents 元字符串 → 分；非法返回 0（调用方按金额不符处理）。
func huifuAmountCents(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	parts := strings.SplitN(raw, ".", 2)
	yuan, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0
	}
	var fen int64
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 2 {
			frac = frac[:2]
		}
		for len(frac) < 2 {
			frac += "0"
		}
		fen, err = strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0
		}
	}
	return yuan*100 + fen
}

// huifuTradeType 聚合正扫：微信 W_NATIVE，支付宝 A_NATIVE。
func huifuTradeType(payChannel string) string {
	if payChannel == "alipay" {
		return "A_NATIVE"
	}
	return "W_NATIVE"
}

// huifuPayChannel 从 trade_type / pay_type 还原支付渠道。
func huifuPayChannel(raw string) string {
	value := strings.ToUpper(strings.TrimSpace(raw))
	switch {
	case value == "":
		return ""
	case strings.HasPrefix(value, "A"), strings.Contains(value, "ALIPAY"):
		return "alipay"
	case strings.HasPrefix(value, "W"), strings.Contains(value, "WECHAT"), strings.Contains(value, "WX"):
		return "wechat"
	default:
		return ""
	}
}

// huifuCall 发送已签名的请求并校验响应签名；返回 data 字段（兼容平铺响应）。
func (p *huifuProvider) call(ctx context.Context, path string, data map[string]any) (map[string]any, error) {
	fields := map[string]any{}
	for key, value := range data {
		fields[key] = value
	}
	if p.sysID != "" {
		fields["sys_id"] = p.sysID
	}
	if p.productID != "" {
		fields["product_id"] = p.productID
	}
	signature, err := huifuSign(fields, p.privateKey)
	if err != nil {
		return nil, err
	}
	envelope := map[string]any{"sign": signature, "sign_type": "RSA2", "data": fields}
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("汇付天下请求失败：%w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, checkoutProviderMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("汇付天下响应读取失败：%w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("汇付天下接口失败：HTTP %d %s", resp.StatusCode, truncateText(string(raw), 200))
	}
	return p.parseSignedEnvelope(raw)
}

// parseSignedEnvelope 解析汇付响应 / 通知：优先 data 包裹，其次平铺字段；都做公钥验签。
func (p *huifuProvider) parseSignedEnvelope(raw []byte) (map[string]any, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("%w：汇付天下内容不是合法 JSON", errInvalidInput)
	}
	signature, _ := envelope["sign"].(string)
	data := map[string]any{}
	if nested, ok := envelope["data"].(map[string]any); ok {
		for key, value := range nested {
			data[key] = value
		}
	} else {
		for key, value := range envelope {
			if key == "sign" || key == "sign_type" {
				continue
			}
			data[key] = value
		}
	}
	// 验签对象与签名对象保持一致：请求签名覆盖 data 内字段 + sys_id/product_id；
	// 响应 / 通知的签名只覆盖 data 内字段（服务商不会回传 sys_id/product_id）。
	if err := huifuVerify(data, signature, p.publicKey); err != nil {
		return nil, err
	}
	if code, _ := data["resp_code"].(string); strings.TrimSpace(code) != "" &&
		!strings.Contains(strings.TrimSpace(code), "00000000") && strings.TrimSpace(code) != "00" {
		desc, _ := data["resp_desc"].(string)
		return nil, fmt.Errorf("%w：汇付天下接口返回失败：%s %s", errCheckoutProviderRejected,
			code, truncateText(desc, 120))
	}
	if code, _ := data["sub_resp_code"].(string); strings.TrimSpace(code) != "" && strings.TrimSpace(code) != "00" {
		desc, _ := data["sub_resp_desc"].(string)
		return nil, fmt.Errorf("%w：汇付天下接口返回失败：%s %s", errCheckoutProviderRejected,
			code, truncateText(desc, 120))
	}
	return data, nil
}

func huifuString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := data[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// CreatePayment 聚合正扫：POST /top/trans/pullPayInfo，返回 qr_code（正扫）与支付信息。
func (p *huifuProvider) CreatePayment(ctx context.Context, req checkoutPaymentRequest) (*checkoutPaymentResult, error) {
	if strings.TrimSpace(req.MerchantNo) == "" || req.AmountCents <= 0 || req.PayChannel == "" {
		return nil, errInvalidInput
	}
	now := p.now().UTC()
	expire := req.ExpiresAt.UTC()
	if expire.Before(now.Add(time.Minute)) {
		expire = now.Add(time.Minute)
	}
	data, err := p.call(ctx, "/top/trans/pullPayInfo", map[string]any{
		"req_seq_id":      req.MerchantNo,
		"req_date":        now.Format("20060102"),
		"huifu_id":        p.merchantNo,
		"mer_ord_id":      req.MerchantNo,
		"trade_type":      huifuTradeType(req.PayChannel),
		"trans_amt":       huifuAmountString(req.AmountCents),
		"goods_desc":      truncateText(req.Subject, 120),
		"risk_check_info": "{}",
		"notify_url":      p.notifyURL(),
		"time_expire":     expire.Format("20060102150405"),
		"mer_priv":        "esports",
	})
	if err != nil {
		return nil, err
	}
	result := &checkoutPaymentResult{
		ProviderTradeNo: huifuString(data, "hf_seq_id", "party_order_id"),
		QRPayload:       huifuString(data, "qr_code"),
	}
	// pay_info 是 JSAPI 支付参数（JSON 字符串），原样作为 pay_url 交给扫码页的跳转分支；
	// 若服务商只返回二维码则 qr_payload 为唯一支付入口。
	result.PayURL = huifuString(data, "pay_info")
	if result.PayURL == "" {
		result.PayURL = huifuString(data, "qr_code")
	}
	if result.QRPayload == "" && result.PayURL == "" {
		return nil, fmt.Errorf("%w：汇付天下未返回二维码或支付信息", errPaymentGatewayUnavailable)
	}
	return result, nil
}

// QueryPayment 查单：POST /top/trans/query（按商户单号 + 商户号 + 请求日期）。
func (p *huifuProvider) QueryPayment(ctx context.Context, merchantNo string) (*checkoutNotification, error) {
	if strings.TrimSpace(merchantNo) == "" {
		return nil, errInvalidInput
	}
	now := p.now().UTC()
	data, err := p.call(ctx, "/top/trans/query", map[string]any{
		"req_seq_id": "Q" + merchantNo,
		"req_date":   now.Format("20060102"),
		"huifu_id":   p.merchantNo,
		"mer_ord_id": merchantNo,
	})
	if err != nil {
		return nil, err
	}
	return huifuNotificationOf(data, merchantNo, now), nil
}

func huifuNotificationOf(data map[string]any, fallbackMerchantNo string, now time.Time) *checkoutNotification {
	merchantNo := huifuString(data, "mer_ord_id", "req_seq_id")
	if merchantNo == "" {
		merchantNo = fallbackMerchantNo
	}
	status := strings.ToUpper(huifuString(data, "trans_stat", "order_status"))
	notice := &checkoutNotification{
		Provider: checkoutProviderHuifu, MerchantNo: merchantNo,
		ProviderTradeNo: huifuString(data, "hf_seq_id", "party_order_id"),
		PayChannel:      huifuPayChannel(huifuString(data, "trade_type", "pay_type")),
		Status:          status,
		Paid:            status == "S" || status == "SUCCESS" || status == "PAID",
		PaidAt:          now,
	}
	notice.AmountCents = huifuAmountCents(huifuString(data, "trans_amt", "order_amt", "settlement_amt"))
	if raw := huifuString(data, "trans_time", "end_time"); raw != "" {
		if parsed, err := time.ParseInLocation("20060102150405", raw, time.UTC); err == nil {
			notice.PaidAt = parsed
		}
	}
	return notice
}

// VerifyNotification 汇付异步通知：验签后 trans_stat=S 判定成功。
func (p *huifuProvider) VerifyNotification(ctx context.Context, headers map[string]string, body []byte) (*checkoutNotification, error) {
	_ = ctx
	_ = headers
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errInvalidInput
	}
	data, err := p.parseSignedEnvelope(body)
	if err != nil {
		return nil, err
	}
	notice := huifuNotificationOf(data, "", p.now().UTC())
	if notice.MerchantNo == "" || notice.ProviderTradeNo == "" || notice.AmountCents <= 0 {
		return nil, errInvalidInput
	}
	return notice, nil
}

// NotifyAck 汇付天下要求回调应答 JSON：resp_code=00000000 表示已处理。
func (p *huifuProvider) NotifyAck(ok bool) (int, string) {
	if ok {
		return http.StatusOK, `{"resp_code":"00000000","resp_desc":"success"}`
	}
	return http.StatusInternalServerError, `{"resp_code":"99000000","resp_desc":"fail"}`
}

// ── 商户单号 ──

// newCheckoutMerchantNo 生成本站商户单号：服务商前缀 + 时间 + 随机十六进制。
// 超时重试必须使用新号（服务商侧已受理的旧单号不允许复用）。
func newCheckoutMerchantNo(provider string, now time.Time) string {
	prefix := "SQB"
	if provider == checkoutProviderHuifu {
		prefix = "HF"
	}
	random := make([]byte, 5)
	_, _ = rand.Read(random)
	return prefix + now.UTC().Format("20060102150405") + strings.ToUpper(hex.EncodeToString(random))
}

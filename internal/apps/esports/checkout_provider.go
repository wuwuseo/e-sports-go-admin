package esports

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
//   - 汇付天下斗拱 V4：POST /v4/trade/payment/create，查单 /v4/trade/payment/scanpay/query；
//     https://paas.huifu.com/docs/lightning/；data 为对象，排序 JSON + SHA256withRSA，
//     同步响应排序验签，异步通知 resp_data 原文验签，ACK=RECV_ORD_ID_请求流水号。
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
	defaultHuifuBaseURL      = "https://api.huifu.com"

	shouqianbaNotifyPath = "/payment-notify/shouqianba"
	huifuNotifyPath      = "/payment-notify/huifu"
)

// errCheckoutProviderRejected 服务商「明确拒绝受理」：只有这种错误才允许把尝试置 failed 并
// 按 priority 切到下一家。网络超时 / 响应读取失败属于「受理结果未知」，尝试保持 creating，
// 下次请求用同一商户单号重试或由查单补偿收敛，绝不静默改投另一家。
var errCheckoutProviderRejected = errors.New("服务商明确拒绝受理")

// checkoutPaymentRequest 向服务商创建一笔交易。
type checkoutPaymentRequest struct {
	RequestedAt time.Time
	ClientIP    string
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
	RequestID       string
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
	QueryPayment(ctx context.Context, merchantNo string, requestedAt time.Time) (*checkoutNotification, error)
	VerifyNotification(ctx context.Context, headers map[string]string, body []byte) (*checkoutNotification, error)
	NotifyAck(ok bool, requestID string) (status int, body string)
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

// huifuProviderConfig 斗拱 V4 配置（app.esports.payment_providers.huifu）。
// merchant_no=huifu_id；密钥支持 PEM 或官方 SDK 的 Base64 DER 格式。
type huifuProviderConfig struct {
	Enabled     bool   `json:"enabled"`
	MerchantNo  string `json:"merchant_no"`
	SysID       string `json:"sys_id"`
	ProductID   string `json:"product_id"`
	PrivateKey  string `json:"private_key"`
	PublicKey   string `json:"public_key"`
	SkillSource string `json:"skill_source,omitempty"`
	NotifyBase  string `json:"notify_base"`
	BaseURL     string `json:"base_url"`
}

func (c huifuProviderConfig) configured() bool {
	return c.Enabled && strings.TrimSpace(c.MerchantNo) != "" &&
		strings.TrimSpace(c.SysID) != "" && strings.TrimSpace(c.ProductID) != "" &&
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
			AppIDConfigured:      strings.TrimSpace(cfg.Huifu.SysID) != "" && strings.TrimSpace(cfg.Huifu.ProductID) != "",
			PrivateKeyConfigured: strings.TrimSpace(cfg.Huifu.PrivateKey) != "",
			PublicKeyConfigured:  strings.TrimSpace(cfg.Huifu.PublicKey) != "",
			NotifyBase:           strings.TrimSpace(cfg.Huifu.NotifyBase),
			BaseURL:              strings.TrimSpace(cfg.Huifu.BaseURL),
		})
	}
	for _, name := range availableProviderNames(cfg) {
		view.Channels["wechat"] = view.Channels["wechat"] || checkoutProviderSupports(name, "wechat")
		view.Channels["alipay"] = view.Channels["alipay"] || checkoutProviderSupports(name, "alipay")
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
func availableCheckoutProviders(ctx context.Context, db *ent.Client, payChannel string) ([]checkoutProvider, error) {
	cfg, err := loadPaymentProvidersConfig(ctx, db)
	if err != nil {
		return nil, err
	}
	providers := make([]checkoutProvider, 0, 2)
	for _, name := range availableProviderNames(cfg) {
		if !checkoutProviderSupports(name, payChannel) {
			continue
		}
		provider, buildErr := checkoutProviderFactory(cfg, name)
		if buildErr != nil {
			return nil, buildErr
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

// checkoutChannelAvailability 仅展示当前服务商真正支持的支付方式。
func checkoutChannelAvailability(ctx context.Context, db *ent.Client) (wechat, alipay bool, providers []string, err error) {
	cfg, err := loadPaymentProvidersConfig(ctx, db)
	if err != nil {
		return false, false, nil, err
	}
	providers = availableProviderNames(cfg)
	for _, name := range providers {
		wechat = wechat || checkoutProviderSupports(name, "wechat")
		alipay = alipay || checkoutProviderSupports(name, "alipay")
	}
	return wechat, alipay, providers, nil
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
func (p *shouqianbaProvider) QueryPayment(ctx context.Context, merchantNo string, requestedAt time.Time) (*checkoutNotification, error) {
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
func (p *shouqianbaProvider) NotifyAck(ok bool, requestID string) (int, string) {
	if ok {
		return http.StatusOK, "SUCCESS"
	}
	return http.StatusInternalServerError, "FAIL"
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

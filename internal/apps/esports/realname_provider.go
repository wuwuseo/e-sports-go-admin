package esports

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
	"go-admin/internal/ent/systemconfig"
)

// 统一实名渠道接入。
//
// 渠道配置集中在系统配置项 app.esports.realname_providers（JSON 对象，按渠道编码索引）；
// manual 人工审核永远注册（兜底路径），其余渠道必须「后台已开通 + 配置完整」才入注册表，
// 用户端只展示注册表内的渠道。任何渠道异常都返回 errRealNameProviderUnavailable，
// 绝不静默通过。

// realnameProvidersConfigKey 实名渠道配置项键（须为字面量，供配置声明校验识别）。
const realnameProvidersConfigKey = "app.esports.realname_providers"

const (
	// 渠道编码。
	realNameChannelWechat     = "wechat"
	realNameChannelAlipay     = "alipay"
	realNameChannelThirdParty = "third_party"
	realNameChannelManual     = "manual"
	// 核验方式。
	realNameMethodTwoFactor = "two_factor"
	realNameMethodFace      = "face"
	realNameMethodManual    = "manual"
	// 核验结果。
	realNameOutcomePassed  = "passed"
	realNameOutcomeFailed  = "failed"
	realNameOutcomePending = "pending"
	// realNameProviderTimeout 渠道核验请求超时；超时即失败，不重试。
	realNameProviderTimeout = 5 * time.Second
	// realNameProviderMaxResponseBytes 渠道响应体读取上限。
	realNameProviderMaxResponseBytes = 1 << 20
)

var (
	// errRealNameProviderUnavailable 实名渠道异常：不得静默通过，提示用户稍后重试或转人工。
	errRealNameProviderUnavailable = errors.New("实名核验服务暂不可用，请稍后重试或选择人工审核")
	// errRealNameRequired 业务前置条件未满足：需先完成实名认证。
	errRealNameRequired = errors.New("请先完成实名认证")
	// errRealNameRejected 渠道核验未通过：落 rejected 并回 400。
	errRealNameRejected = errors.New("实名信息核验未通过")
)

// realNameProviderInput 渠道核验入参。IDNumber 仅在请求生命周期内使用，不落库。
type realNameProviderInput struct {
	SubjectType string
	SubjectID   int
	RealName    string
	IDNumber    string
	Method      string
	NotifyURL   string
}

// realNameProviderResult 渠道核验结果。
type realNameProviderResult struct {
	Outcome     string
	ProviderRef string
	RedirectURL string
	QRContent   string
	Message     string
}

// realNameProvider 实名渠道统一接口：同步二要素、异步人脸（跳转 / 二维码 / 回调 / 轮询）共用。
type realNameProvider interface {
	Code() string
	Supports(method string) bool
	Start(ctx context.Context, in realNameProviderInput) (*realNameProviderResult, error)
	Query(ctx context.Context, ref string) (*realNameProviderResult, error)
}

// realNameChannelOption 用户端可选的渠道与方式。
type realNameChannelOption struct {
	Code    string   `json:"code"`
	Name    string   `json:"name"`
	Methods []string `json:"methods"`
}

// realNameProviderRegistry 渠道注册表：只含当前可用的渠道。
type realNameProviderRegistry struct {
	providers map[string]realNameProvider
	options   []realNameChannelOption
}

func (r *realNameProviderRegistry) register(provider realNameProvider, option realNameChannelOption) {
	r.providers[option.Code] = provider
	r.options = append(r.options, option)
}

// get 取渠道并校验其支持所选核验方式。
func (r *realNameProviderRegistry) get(channel, method string) (realNameProvider, error) {
	provider, ok := r.providers[channel]
	if !ok {
		return nil, fmt.Errorf("%w：该实名渠道暂不可用，请选择其他渠道", errInvalidInput)
	}
	if !provider.Supports(method) {
		return nil, fmt.Errorf("%w：该实名渠道不支持所选核验方式", errInvalidInput)
	}
	return provider, nil
}

// realNameProviderConfig 单个渠道配置。
//
// 配置示例（SystemConfig.key = app.esports.realname_providers）：
//
//	{
//	  "third_party": {
//	    "enabled": true,
//	    "base_url": "https://verify.example.com",
//	    "app_id": "studio-esports",
//	    "app_secret": "******",
//	    "notify_url": "https://studio.example.com/api/v1/esports/realname/callback/third_party"
//	  }
//	}
type realNameProviderConfig struct {
	Enabled   bool   `json:"enabled"`
	BaseURL   string `json:"base_url"`
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
	NotifyURL string `json:"notify_url"`
}

// validate 校验 third_party 渠道配置完整性。
func (c realNameProviderConfig) validate() error {
	baseURL, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" {
		return fmt.Errorf("%w：实名渠道 base_url 无效", errRealNameProviderUnavailable)
	}
	if strings.TrimSpace(c.AppID) == "" || strings.TrimSpace(c.AppSecret) == "" {
		return fmt.Errorf("%w：实名渠道 app_id / app_secret 缺失", errRealNameProviderUnavailable)
	}
	return nil
}

// realNameProviderConfigs 读取全部渠道配置：缺失或 JSON 非法返回 nil（退化为仅 manual）。
func realNameProviderConfigs(ctx context.Context, db *ent.Client) map[string]realNameProviderConfig {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(realnameProvidersConfigKey)).Only(ctx)
	if err != nil {
		return nil
	}
	configs := map[string]realNameProviderConfig{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(item.Value)), &configs); err != nil {
		return nil
	}
	return configs
}

// realNameProvidersFor 按配置装配渠道注册表。manual 无条件注册；
// third_party / wechat / alipay 需「enabled && 配置完整」，微信支付宝另需 ready() 为真。
// 任何装配失败都静默退化为「仅 manual」，兜底路径永远可用。
func realNameProvidersFor(ctx context.Context, db *ent.Client) *realNameProviderRegistry {
	registry := &realNameProviderRegistry{providers: map[string]realNameProvider{}}
	registry.register(manualRealNameProvider{}, realNameChannelOption{
		Code: realNameChannelManual, Name: "人工审核", Methods: []string{realNameMethodManual},
	})
	configs := realNameProviderConfigs(ctx, db)
	if cfg, ok := configs[realNameChannelThirdParty]; ok && cfg.Enabled {
		if err := cfg.validate(); err == nil {
			registry.register(newThirdPartyRealNameProvider(cfg), realNameChannelOption{
				Code: realNameChannelThirdParty, Name: "第三方核验",
				Methods: []string{realNameMethodTwoFactor, realNameMethodFace},
			})
		}
	}
	if cfg, ok := configs[realNameChannelWechat]; ok && cfg.Enabled && cfg.validate() == nil && wechatRealNameReady() {
		registry.register(newWechatRealNameProvider(cfg), realNameChannelOption{
			Code: realNameChannelWechat, Name: "微信实名",
			Methods: []string{realNameMethodTwoFactor, realNameMethodFace},
		})
	}
	if cfg, ok := configs[realNameChannelAlipay]; ok && cfg.Enabled && cfg.validate() == nil && alipayRealNameReady() {
		registry.register(newAlipayRealNameProvider(cfg), realNameChannelOption{
			Code: realNameChannelAlipay, Name: "支付宝实名",
			Methods: []string{realNameMethodTwoFactor, realNameMethodFace},
		})
	}
	return registry
}

// manualRealNameProvider 人工审核：提交即待审，由管理端审核落定。
type manualRealNameProvider struct{}

func (manualRealNameProvider) Code() string { return realNameChannelManual }

func (manualRealNameProvider) Supports(method string) bool { return method == realNameMethodManual }

func (manualRealNameProvider) Start(context.Context, realNameProviderInput) (*realNameProviderResult, error) {
	return &realNameProviderResult{Outcome: realNameOutcomePending, Message: "已提交人工审核，请等待审核结果"}, nil
}

func (manualRealNameProvider) Query(context.Context, string) (*realNameProviderResult, error) {
	return &realNameProviderResult{Outcome: realNameOutcomePending, Message: "审核中，请等待审核结果"}, nil
}

// thirdPartyRealNameProvider 通用第三方核验渠道（HTTP + HMAC-SHA256）。
type thirdPartyRealNameProvider struct {
	baseURL   string
	appID     string
	appSecret string
	notifyURL string
	client    *http.Client
	now       func() time.Time
}

func newThirdPartyRealNameProvider(cfg realNameProviderConfig) *thirdPartyRealNameProvider {
	return &thirdPartyRealNameProvider{
		baseURL:   strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		appID:     strings.TrimSpace(cfg.AppID),
		appSecret: cfg.AppSecret,
		notifyURL: strings.TrimSpace(cfg.NotifyURL),
		client:    &http.Client{Timeout: realNameProviderTimeout},
		now:       time.Now,
	}
}

func (*thirdPartyRealNameProvider) Code() string { return realNameChannelThirdParty }

func (*thirdPartyRealNameProvider) Supports(method string) bool {
	return method == realNameMethodTwoFactor || method == realNameMethodFace
}

// thirdPartyVerifyRequest 渠道核验请求体。notify_url 仅人脸异步核验需要。
type thirdPartyVerifyRequest struct {
	Name      string `json:"name"`
	IDNumber  string `json:"id_number"`
	Method    string `json:"method"`
	BizID     string `json:"biz_id"`
	NotifyURL string `json:"notify_url,omitempty"`
}

// thirdPartyVerifyResponse 渠道核验响应体。
type thirdPartyVerifyResponse struct {
	Passed      bool   `json:"passed"`
	Pending     bool   `json:"pending"`
	ProviderRef string `json:"provider_ref"`
	RedirectURL string `json:"redirect_url"`
	QRContent   string `json:"qr_content"`
	Message     string `json:"message"`
}

// Start 发起核验：biz_id 由主体 + 纳秒时戳组成，便于渠道侧对账。
func (p *thirdPartyRealNameProvider) Start(ctx context.Context, in realNameProviderInput) (*realNameProviderResult, error) {
	payload, err := json.Marshal(thirdPartyVerifyRequest{
		Name:      in.RealName,
		IDNumber:  in.IDNumber,
		Method:    in.Method,
		BizID:     fmt.Sprintf("%s:%d:%d", in.SubjectType, in.SubjectID, p.now().UnixNano()),
		NotifyURL: p.notifyURL,
	})
	if err != nil {
		return nil, fmt.Errorf("%w：实名核验请求构造失败", errRealNameProviderUnavailable)
	}
	raw, err := p.do(ctx, http.MethodPost, p.baseURL+"/verify", payload)
	if err != nil {
		return nil, err
	}
	return realNameResultFromResponse(raw)
}

// Query 轮询核验结果（人脸异步场景）。
func (p *thirdPartyRealNameProvider) Query(ctx context.Context, ref string) (*realNameProviderResult, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("%w：核验流水号缺失", errInvalidInput)
	}
	raw, err := p.do(ctx, http.MethodGet, p.baseURL+"/verify/"+url.PathEscape(ref), nil)
	if err != nil {
		return nil, err
	}
	return realNameResultFromResponse(raw)
}

// do 发送签名请求。超时 / 非 2xx / 响应过大一律按渠道不可用处理，不重试。
func (p *thirdPartyRealNameProvider) do(ctx context.Context, method, target string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w：实名核验请求构造失败", errRealNameProviderUnavailable)
	}
	timestamp := strconv.FormatInt(p.now().Unix(), 10)
	nonce, err := realNameNonce()
	if err != nil {
		return nil, fmt.Errorf("%w：实名核验请求构造失败", errRealNameProviderUnavailable)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-Id", p.appID)
	req.Header.Set("X-Timestamp", timestamp)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Signature", realNameHMACHex(p.appSecret, realNameSignatureCanonical(p.appID, timestamp, nonce, string(payload))))
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w：实名核验请求失败", errRealNameProviderUnavailable)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, realNameProviderMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w：实名核验响应读取失败", errRealNameProviderUnavailable)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w：实名核验服务返回 %d", errRealNameProviderUnavailable, resp.StatusCode)
	}
	return raw, nil
}

// realNameSignatureCanonical 待签名串：app_id、timestamp、nonce、body 以换行连接。
func realNameSignatureCanonical(appID, timestamp, nonce, body string) string {
	return strings.Join([]string{appID, timestamp, nonce, body}, "\n")
}

// realNameHMACHex 计算 hex(HMAC-SHA256(secret, canonical))。
func realNameHMACHex(secret, canonical string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

// realNameNonce 生成 16 位十六进制随机串。
func realNameNonce() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// parseRealNameCallbackTimestamp 解析回调时间戳（Unix 秒）。
func parseRealNameCallbackTimestamp(raw string) (time.Time, error) {
	seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(seconds, 0), nil
}

// realNameSignatureEqual 恒定位数比较签名，避免长度差异提前返回。
func realNameSignatureEqual(expected, actual string) bool {
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(strings.TrimSpace(expected))),
		[]byte(strings.ToLower(strings.TrimSpace(actual)))) == 1
}

// realNameResultFromResponse 解析渠道响应：三态明确；响应非法按渠道不可用处理（绝不静默通过）。
func realNameResultFromResponse(raw []byte) (*realNameProviderResult, error) {
	var body thirdPartyVerifyResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("%w：实名核验服务返回内容异常", errRealNameProviderUnavailable)
	}
	result := &realNameProviderResult{
		ProviderRef: strings.TrimSpace(body.ProviderRef),
		RedirectURL: strings.TrimSpace(body.RedirectURL),
		QRContent:   strings.TrimSpace(body.QRContent),
		Message:     strings.TrimSpace(body.Message),
	}
	switch {
	case body.Passed:
		result.Outcome = realNameOutcomePassed
	case body.Pending:
		// 异步核验：必须给出跳转地址或二维码，否则前端无从继续，按渠道异常处理。
		if result.RedirectURL == "" && result.QRContent == "" {
			return nil, fmt.Errorf("%w：实名核验服务未返回跳转地址", errRealNameProviderUnavailable)
		}
		result.Outcome = realNameOutcomePending
	default:
		result.Outcome = realNameOutcomeFailed
		if result.Message == "" {
			result.Message = "实名信息核验未通过"
		}
	}
	return result, nil
}

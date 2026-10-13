package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 短信文案默认截断长度（按电信规范单条 70 字，超出走长短信）
const smsContentMaxRunes = 140

// SMSNotifier 短信渠道
//
// 通过 config.yml `notify.sms` 段配置。为兼容多家服务商，采用「网关 HTTP 接口」模式：
// 服务端以 POST JSON 调用配置的 endpoint，由内部网关适配阿里云/腾讯云等厂商 SDK。
// 这样无需在本项目引入各厂商 SDK，也不泄露密钥到业务代码。
type SMSNotifier struct {
	cfg notifierConfig
	// sender 实际投递实现，可被测试替换
	sender smsSender
}

// smsSender 短信投递的最小抽象，便于单测注入
type smsSender interface {
	Send(ctx context.Context, phone string, payload NotifyPayload) error
}

// NewSMSNotifier 构造短信渠道
func NewSMSNotifier(cfg notifierConfig) *SMSNotifier {
	n := &SMSNotifier{cfg: cfg}
	if cfg.smsEnabled && cfg.smsEndpoint != "" {
		n.sender = &httpSMSender{
			endpoint:  cfg.smsEndpoint,
			provider:  cfg.smsProvider,
			accessKey: cfg.smsAccessKey,
			secretKey: cfg.smsSecretKey,
			signName:  cfg.smsSignName,
			template:  cfg.smsTemplate,
			timeout:   cfg.smsTimeout,
			client:    &http.Client{Timeout: cfg.smsTimeout},
		}
	}
	return n
}

// Name 渠道标识
func (n *SMSNotifier) Name() string { return ChannelSMS }

// Enabled 需同时满足「开关打开」且「投递实现就绪」
func (n *SMSNotifier) Enabled() bool {
	return n != nil && n.cfg.smsEnabled && n.sender != nil
}

// Send 发送短信；接收者从 Extra.phone 读取
func (n *SMSNotifier) Send(ctx context.Context, payload NotifyPayload) error {
	if !n.Enabled() {
		return ErrNotifierDisabled
	}
	phone := recipientFromExtra(payload.Extra, "phone", "mobile", "tel")
	if phone == "" {
		return ErrNotifierNoRecipient
	}
	return n.sender.Send(ctx, phone, payload)
}

// httpSMSender 通过网关 HTTP 接口投递短信
type httpSMSender struct {
	endpoint  string
	provider  string
	accessKey string
	secretKey string
	signName  string
	template  string
	timeout   time.Duration
	client    *http.Client
}

// smsGatewayRequest 网关请求体
type smsGatewayRequest struct {
	Phone    string            `json:"phone"`
	Content  string            `json:"content"`
	SignName string            `json:"sign_name,omitempty"`
	Template string            `json:"template_code,omitempty"`
	Provider string            `json:"provider,omitempty"`
	Extra    map[string]string `json:"extra,omitempty"`
}

// smsGatewayResponse 网关响应体（约定 code=0 为成功）
type smsGatewayResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Msg     string `json:"msg"`
}

func (s *httpSMSender) Send(ctx context.Context, phone string, payload NotifyPayload) error {
	reqBody := smsGatewayRequest{
		Phone:    phone,
		Content:  buildSMSContent(payload),
		SignName: s.signName,
		Template: s.template,
		Provider: s.provider,
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("序列化短信请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("构造短信请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.accessKey != "" {
		req.Header.Set("X-Access-Key", s.accessKey)
	}
	if s.secretKey != "" {
		req.Header.Set("X-Secret-Key", s.secretKey)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("短信网关请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("短信网关返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var parsed smsGatewayResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		// 网关返回非 JSON（如纯文本 ok），视为成功
		return nil
	}
	if parsed.Code != 0 {
		msg := parsed.Message
		if msg == "" {
			msg = parsed.Msg
		}
		return fmt.Errorf("短信网关业务失败 code=%d: %s", parsed.Code, msg)
	}
	return nil
}

// buildSMSContent 拼装短信正文，超长截断避免计费异常
func buildSMSContent(payload NotifyPayload) string {
	text := strings.TrimSpace(payload.Content)
	if text == "" {
		text = strings.TrimSpace(payload.Title)
	}
	body := strings.TrimSpace(payload.Title + " " + text)
	if title := strings.TrimSpace(payload.Title); title != "" && body == "" {
		body = title
	}
	runes := []rune(body)
	if len(runes) > smsContentMaxRunes {
		return string(runes[:smsContentMaxRunes])
	}
	return body
}

// recipientFromExtra 从扩展字段中按候选键顺序取收件人
func recipientFromExtra(extra map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := extra[k]; ok {
			if s, ok := v.(string); ok {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

// mailSender 邮件投递最小抽象
type mailSender interface {
	Send(ctx context.Context, email string, payload NotifyPayload) error
}

// MailNotifier 邮件渠道
//
// 同样走网关 HTTP 接口模式（notify.mail.endpoint），避免在业务进程内做 SMTP 连接管理。
type MailNotifier struct {
	cfg    notifierConfig
	sender mailSender
}

// NewMailNotifier 构造邮件渠道
func NewMailNotifier(cfg notifierConfig) *MailNotifier {
	n := &MailNotifier{cfg: cfg}
	if cfg.mailEnabled && cfg.mailEndpoint != "" {
		n.sender = &httpMailSender{
			endpoint: cfg.mailEndpoint,
			token:    cfg.mailToken,
			from:     cfg.mailFrom,
			client:   &http.Client{Timeout: cfg.mailTimeout},
		}
	}
	return n
}

// Name 渠道标识
func (n *MailNotifier) Name() string { return ChannelMail }

// Enabled 开关 + 投递实现就绪
func (n *MailNotifier) Enabled() bool {
	return n != nil && n.cfg.mailEnabled && n.sender != nil
}

// Send 发送邮件；接收者从 Extra.email 读取
func (n *MailNotifier) Send(ctx context.Context, payload NotifyPayload) error {
	if !n.Enabled() {
		return ErrNotifierDisabled
	}
	email := recipientFromExtra(payload.Extra, "email", "mail")
	if email == "" {
		return ErrNotifierNoRecipient
	}
	return n.sender.Send(ctx, email, payload)
}

// httpMailSender 通过网关投递邮件
type httpMailSender struct {
	endpoint string
	token    string
	from     string
	client   *http.Client
}

type mailGatewayRequest struct {
	To      string `json:"to"`
	From    string `json:"from,omitempty"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	IsHTML  bool   `json:"is_html"`
}

func (s *httpMailSender) Send(ctx context.Context, email string, payload NotifyPayload) error {
	body := payload.ContentHTML
	isHTML := strings.TrimSpace(body) != ""
	if !isHTML {
		body = payload.Content
	}
	reqBody := mailGatewayRequest{
		To:      email,
		From:    s.from,
		Subject: payload.Title,
		Body:    body,
		IsHTML:  isHTML,
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("序列化邮件请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("构造邮件请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("邮件网关请求失败: %w", err)
	}
	defer resp.Body.Close()
	body2, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("邮件网关返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body2)))
	}
	return nil
}

// pushSender 极光推送投递最小抽象
type pushSender interface {
	Send(ctx context.Context, payload NotifyPayload) error
}

// PushNotifier 极光推送（JPush）渠道
//
// 通过 REST API v3 投递，接收者优先使用 Extra.registration_id / alias / tag，
// 未提供时回退到 UserUID（约定为 alias）。
type PushNotifier struct {
	cfg    notifierConfig
	sender pushSender
}

// NewPushNotifier 构造极光推送渠道
func NewPushNotifier(cfg notifierConfig) *PushNotifier {
	n := &PushNotifier{cfg: cfg}
	if cfg.pushEnabled && cfg.jpushAppKey != "" && cfg.jpushMaster != "" {
		n.sender = &jpushSender{
			appKey:     cfg.jpushAppKey,
			masterKey:  cfg.jpushMaster,
			host:       cfg.jpushHost,
			httpClient: &http.Client{Timeout: cfg.pushTimeout},
		}
	}
	return n
}

// Name 渠道标识
func (n *PushNotifier) Name() string { return ChannelPush }

// Enabled 开关 + 凭证 + 投递实现就绪
func (n *PushNotifier) Enabled() bool {
	return n != nil && n.cfg.pushEnabled && n.sender != nil
}

// Send 下发推送
func (n *PushNotifier) Send(ctx context.Context, payload NotifyPayload) error {
	if !n.Enabled() {
		return ErrNotifierDisabled
	}
	return n.sender.Send(ctx, payload)
}

// defaultJPushHost 极光推送 API 默认地址（国内）
const defaultJPushHost = "https://api.jpush.cn"

// jpushSender 极光推送 REST API v3 实现
type jpushSender struct {
	appKey     string
	masterKey  string
	host       string
	httpClient *http.Client
}

type jpushAudience struct {
	RegistrationID []string `json:"registration_id,omitempty"`
	Alias          []string `json:"alias,omitempty"`
	Tag            []string `json:"tag,omitempty"`
}

type jpushNotification struct {
	Alert    string                 `json:"alert"`
	Android  map[string]any         `json:"android,omitempty"`
	IOS      map[string]any         `json:"ios,omitempty"`
	Extras   map[string]any         `json:"extras,omitempty"`
}

type jpushPushRequest struct {
	Platform     any               `json:"platform"`
	Audience     jpushAudience     `json:"audience"`
	Notification jpushNotification `json:"notification"`
	Options      map[string]any    `json:"options,omitempty"`
}

type jpushResponse struct {
	SendNo string `json:"sendno"`
	MsgID  string `json:"msg_id"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *jpushSender) Send(ctx context.Context, payload NotifyPayload) error {
	audience := buildJPushAudience(payload)
	if len(audience.RegistrationID) == 0 && len(audience.Alias) == 0 && len(audience.Tag) == 0 {
		return ErrNotifierNoRecipient
	}

	alert := strings.TrimSpace(payload.Title)
	if alert == "" {
		alert = strings.TrimSpace(payload.Content)
	}
	extras := map[string]any{
		"type":       payload.Type,
		"level":      payload.Level,
		"message_id": payload.MessageID,
	}
	for k, v := range payload.Extra {
		if _, exists := extras[k]; !exists {
			extras[k] = v
		}
	}

	reqBody := jpushPushRequest{
		// 同时下发 Android 与 iOS；生产可按 App 实际上架平台裁剪
		Platform: []string{"android", "ios"},
		Audience: audience,
		Notification: jpushNotification{
			Alert:   alert,
			Android: map[string]any{"alert": alert, "title": payload.Title},
			IOS: map[string]any{
				"alert": alert,
				"sound": "default",
				"badge": "+1",
			},
			Extras: extras,
		},
		Options: map[string]any{
			"apns_production": true,
			"time_to_live":    86400,
		},
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("序列化极光推送请求失败: %w", err)
	}
	endpoint := strings.TrimRight(s.host, "/") + "/v3/push"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("构造极光推送请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(s.appKey, s.masterKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("极光推送请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var parsed jpushResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("极光推送返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil
	}
	if parsed.Error != nil && parsed.Error.Code != 0 {
		return fmt.Errorf("极光推送失败 code=%d: %s", parsed.Error.Code, parsed.Error.Message)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("极光推送返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// buildJPushAudience 依据扩展字段选择推送目标，优先级 registration_id > alias > tag > uid
func buildJPushAudience(payload NotifyPayload) jpushAudience {
	if regID := recipientFromExtra(payload.Extra, "registration_id", "registrationId"); regID != "" {
		return jpushAudience{RegistrationID: splitAndTrim(regID)}
	}
	if alias := recipientFromExtra(payload.Extra, "alias", "push_alias"); alias != "" {
		return jpushAudience{Alias: splitAndTrim(alias)}
	}
	if tag := recipientFromExtra(payload.Extra, "tag", "push_tag"); tag != "" {
		return jpushAudience{Tag: splitAndTrim(tag)}
	}
	if payload.UserUID != "" {
		// 约定：UserUID 即极光 alias
		return jpushAudience{Alias: []string{payload.UserUID}}
	}
	return jpushAudience{}
}

// splitAndTrim 逗号分隔值切分去空
func splitAndTrim(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// 编译期接口实现断言
var (
	_ Notifier = (*InAppNotifier)(nil)
	_ Notifier = (*SMSNotifier)(nil)
	_ Notifier = (*MailNotifier)(nil)
	_ Notifier = (*PushNotifier)(nil)
)

// ensure url import used (kept for future endpoint validation)
var _ = url.Parse

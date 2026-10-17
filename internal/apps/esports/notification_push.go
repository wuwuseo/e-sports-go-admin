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
	"strings"
	"time"

	"go-admin/internal/ent"
)

// 通知推送网关（§通知）：读取 SystemConfig 的 app.esports.notification_push_webhook，
// 逐通道 POST 业务通知。配置示例：
//
//	{"url":"https://push.example.com/esports/notify","secret":"whsec_xxx","timeout_seconds":5}
//
// 未配置时该通道视为未启用（errNotificationWebhookNotConfigured），不阻断站内信投递。
// 非 2xx / 超时返回错误，由 EsportsOutbox 的既有退避重试与死信机制接管。

const notificationPushWebhookConfigKey = "app.esports.notification_push_webhook"

const defaultNotificationWebhookTimeout = 5 * time.Second

var errNotificationWebhookNotConfigured = errors.New("通知推送 webhook 未配置")

// notificationWebhookConfig 是推送网关配置。
type notificationWebhookConfig struct {
	URL            string `json:"url"`
	Secret         string `json:"secret"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (c notificationWebhookConfig) timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return defaultNotificationWebhookTimeout
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// notificationWebhookRequest 是推送网关的请求体，只携带业务可读字段与收件人角色/地址，
// 不包含完整事件载荷。
type notificationWebhookRequest struct {
	Channel    string                         `json:"channel"`
	EventType  string                         `json:"event_type"`
	Recipients []notificationWebhookRecipient `json:"recipients"`
	Title      string                         `json:"title"`
	Summary    string                         `json:"summary"`
	OrderNo    string                         `json:"order_no"`
}

type notificationWebhookRecipient struct {
	Role    string `json:"role"`
	Address string `json:"address"`
}

// webhookNotificationSender 通过 HTTP 推送通知；每个通道一次请求（收件人合并到同一次推送）。
type webhookNotificationSender struct {
	db     *ent.Client
	client *http.Client
}

func newWebhookNotificationSender(db *ent.Client) *webhookNotificationSender {
	return &webhookNotificationSender{db: db}
}

func (s *webhookNotificationSender) Send(ctx context.Context, channel, eventType, aggregateRef string, payload map[string]interface{}) error {
	recipients, err := resolveNotificationRecipients(ctx, s.db, eventType, aggregateRef, payload)
	if err != nil {
		return err
	}
	return s.send(ctx, channel, eventType, aggregateRef, payload, recipients)
}

// send 使用已解析的收件人推送，供组合投递器复用（避免重复解析）。
func (s *webhookNotificationSender) send(ctx context.Context, channel, eventType, aggregateRef string, payload map[string]interface{}, recipients []notificationRecipient) error {
	cfg, err := s.config(ctx)
	if err != nil {
		return err
	}
	request := notificationWebhookRequest{
		Channel:    channel,
		EventType:  eventType,
		Recipients: make([]notificationWebhookRecipient, 0, len(recipients)),
		Title:      notificationEventTitle(eventType),
		Summary:    notificationEventSummary(eventType, aggregateRef, payload),
		OrderNo:    notificationOrderNo(eventType, aggregateRef, payload),
	}
	for _, recipient := range recipients {
		request.Recipients = append(request.Recipients, notificationWebhookRecipient{
			Role:    strings.Join(recipient.Roles, ","),
			Address: recipient.Address,
		})
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Esports-Signature", notificationWebhookSignature(cfg.Secret, body))
	client := s.client
	if client == nil {
		client = &http.Client{Timeout: cfg.timeout()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("推送通知失败: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("推送通知失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *webhookNotificationSender) config(ctx context.Context) (notificationWebhookConfig, error) {
	raw := assessmentConfigString(ctx, s.db, notificationPushWebhookConfigKey, "")
	if strings.TrimSpace(raw) == "" {
		return notificationWebhookConfig{}, errNotificationWebhookNotConfigured
	}
	var cfg notificationWebhookConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return notificationWebhookConfig{}, fmt.Errorf("通知推送 webhook 配置无效: %w", err)
	}
	cfg.URL = strings.TrimSpace(cfg.URL)
	if cfg.URL == "" {
		return notificationWebhookConfig{}, errNotificationWebhookNotConfigured
	}
	return cfg, nil
}

// notificationWebhookSignature 计算 HMAC-SHA256(secret, body)，输出 `sha256=<hex>`。
func notificationWebhookSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

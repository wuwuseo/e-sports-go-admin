package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/log"
	"go.uber.org/zap"
)

// ========== 通知渠道抽象层 ==========
//
// MessageService 只负责「落库 + 按偏好分发」，具体投递方式由 Notifier 实现。
// 站内信（SSE 实时推送）是默认渠道，短信 / 邮件 / 极光推送等为可选渠道，
// 未配置时自动禁用，不影响主流程。

// 渠道相关领域错误
var (
	ErrNotifierDisabled    = errors.New("通知渠道未启用")
	ErrNotifierNotReady    = errors.New("通知渠道缺少必要配置")
	ErrNotifierNoRecipient = errors.New("通知渠道缺少收件人信息")
)

// 渠道名称常量（与 MessagePreference 的渠道开关字段一一对应）
const (
	ChannelInApp = "inapp" // 站内信（SSE 实时推送）
	ChannelSMS   = "sms"   // 短信
	ChannelMail  = "mail"  // 邮件
	ChannelPush  = "push"  // 极光推送（App 端）
)

// NotifyPayload 渠道投递载荷（与站内信落库结构解耦）
//
// 渠道实现只应读取自己需要的字段：站内信需要 MessageID 用于事件 ID，
// 短信需要 Content 的纯文本摘要，邮件需要 ContentHTML，极光需要 Title+Content。
type NotifyPayload struct {
	// MessageID 站内信主键；非站内信渠道可用于幂等去重（0 表示尚未落库）
	MessageID int
	// UserUID 接收者 UID
	UserUID string
	// Type 消息类型 system / business / task
	Type string
	// Level 消息级别 info / warning / error
	Level string
	// Title 标题
	Title string
	// Content 纯文本内容
	Content string
	// ContentHTML 富文本内容（可为空）
	ContentHTML string
	// Extra 业务扩展字段
	Extra map[string]any
	// CreatedAt 消息创建时间
	CreatedAt time.Time
}

// Notifier 通知渠道统一接口
//
// 约定：
//   - Name 返回渠道标识，需与 ChannelXxx 常量一致；
//   - Enabled 返回渠道当前是否可投递（用于启动期与运行期判定）；
//   - Send 执行一次投递，失败应返回 error 供上层记录，但不得影响其他渠道；
//   - 实现必须是并发安全的。
type Notifier interface {
	Name() string
	Enabled() bool
	Send(ctx context.Context, payload NotifyPayload) error
}

// NotifierRegistry 渠道注册表，负责按用户偏好选择渠道并分发投递
type NotifierRegistry struct {
	notifiers map[string]Notifier
	// disabled 记录因配置缺失而不可用的渠道名，便于启动期日志提示
	disabled []string
}

// NewNotifierRegistry 创建空注册表
func NewNotifierRegistry() *NotifierRegistry {
	return &NotifierRegistry{notifiers: make(map[string]Notifier)}
}

// Register 注册渠道；name 为空或 notifier 为 nil 时忽略。
// 若渠道存在但未启用（Enabled()==false），仍会注册但记入 disabled 列表，
// 便于在日志中区分「未启用」与「未注册」。
func (r *NotifierRegistry) Register(notifier Notifier) {
	if r == nil || notifier == nil {
		return
	}
	name := strings.TrimSpace(notifier.Name())
	if name == "" {
		return
	}
	r.notifiers[name] = notifier
	if !notifier.Enabled() {
		r.disabled = append(r.disabled, name)
	}
}

// Get 按名称取渠道
func (r *NotifierRegistry) Get(name string) (Notifier, bool) {
	if r == nil {
		return nil, false
	}
	n, ok := r.notifiers[name]
	return n, ok
}

// Names 返回已注册的渠道名列表
func (r *NotifierRegistry) Names() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.notifiers))
	for name := range r.notifiers {
		out = append(out, name)
	}
	return out
}

// Disabled 返回已注册但未启用的渠道名（缺配置）
func (r *NotifierRegistry) Disabled() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.disabled...)
}

// Dispatch 向指定渠道投递；渠道不存在或未启用时静默跳过。
// 单个渠道失败不会中断其他渠道，返回按渠道名索引的错误集合。
func (r *NotifierRegistry) Dispatch(ctx context.Context, channel string, payload NotifyPayload) error {
	if r == nil {
		return nil
	}
	n, ok := r.notifiers[channel]
	if !ok {
		return nil
	}
	if !n.Enabled() {
		return nil
	}
	if err := n.Send(ctx, payload); err != nil {
		log.Warn("通知渠道投递失败",
			zap.String("channel", channel),
			zap.String("user_uid", payload.UserUID),
			zap.Int("message_id", payload.MessageID),
			zap.Error(err),
		)
		return fmt.Errorf("%s 渠道投递失败: %w", channel, err)
	}
	return nil
}

// ========== 偏好 → 渠道 决策 ==========

// ShouldDeliver 判断某用户偏好下，指定渠道是否应当投递
//
// 决策顺序：
//  1. 免打扰时段（仅影响即时触达渠道，站内信始终落库）
//  2. 消息类型订阅开关（system / business / task）
//  3. 渠道开关（channel_sms / channel_mail / channel_push）
//  4. 渠道自身启用状态
//
// at 为判定时间点（一般传 time.Now()，便于测试注入）。
func (r *NotifierRegistry) ShouldDeliver(pref *MessagePreferenceResponse, msgType string, channel string, at time.Time) bool {
	// 渠道是否注册且启用
	if n, ok := r.Get(channel); !ok || !n.Enabled() {
		return false
	}
	if pref == nil {
		// 无偏好记录（含全员广播）：站内信默认投递，外部渠道默认不投递以避免打扰
		return channel == ChannelInApp
	}
	// 消息类型订阅
	if !isSubscribedType(pref, msgType) {
		return false
	}
	// 外部渠道在免打扰时段内不投递；站内信不受免打扰影响
	if channel != ChannelInApp && pref.IsDoNotDisturb(at) {
		return false
	}
	return pref.ChannelEnabled(channel)
}

// IsDoNotDisturb 判断给定时间是否落在免打扰时段内
//
// 支持跨零点区间（如 22:00 ~ 07:00）。起止任一为空视为未启用免打扰。
func (p *MessagePreferenceResponse) IsDoNotDisturb(at time.Time) bool {
	if p == nil {
		return false
	}
	start := strings.TrimSpace(p.DoNotDisturbStart)
	end := strings.TrimSpace(p.DoNotDisturbEnd)
	if start == "" || end == "" {
		return false
	}
	startMin, ok1 := parseHHMM(start)
	endMin, ok2 := parseHHMM(end)
	if !ok1 || !ok2 {
		return false
	}
	if startMin == endMin {
		return false
	}
	cur := at.Hour()*60 + at.Minute()
	if startMin < endMin {
		// 同日区间，如 09:00 ~ 18:00
		return cur >= startMin && cur < endMin
	}
	// 跨零点区间，如 22:00 ~ 07:00
	return cur >= startMin || cur < endMin
}

// ChannelEnabled 判断用户是否开启了指定渠道
func (p *MessagePreferenceResponse) ChannelEnabled(channel string) bool {
	if p == nil {
		return false
	}
	switch channel {
	case ChannelInApp:
		// 站内信没有独立开关，受 desktop_notify 控制
		return p.DesktopNotify
	case ChannelSMS:
		return p.ChannelSMS
	case ChannelMail:
		return p.ChannelMail
	case ChannelPush:
		return p.ChannelPush
	}
	return false
}

// parseHHMM 解析 HH:mm 为当日分钟数
func parseHHMM(v string) (int, bool) {
	if len(v) != 5 || v[2] != ':' {
		return 0, false
	}
	h := int(v[0]-'0')*10 + int(v[1]-'0')
	m := int(v[3]-'0')*10 + int(v[4]-'0')
	if v[0] < '0' || v[0] > '9' || v[1] < '0' || v[1] > '9' ||
		v[3] < '0' || v[3] > '9' || v[4] < '0' || v[4] > '9' {
		return 0, false
	}
	if h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// ========== 渠道配置读取 ==========

// notifierConfig 通知渠道统一配置快照
type notifierConfig struct {
	smsEnabled   bool
	smsProvider  string
	smsEndpoint  string
	smsAccessKey string
	smsSecretKey string
	smsSignName  string
	smsTemplate  string
	smsTimeout   time.Duration

	mailEnabled  bool
	mailEndpoint string
	mailToken    string
	mailFrom     string
	mailTimeout  time.Duration

	pushEnabled    bool
	jpushAppKey    string
	jpushMaster    string
	jpushHost      string
	pushTimeout    time.Duration
	pushProduction bool // APNs 是否生产环境（false 时推送走沙箱）
}

// loadNotifierConfig 从 config.yml 读取 notify.* 配置段
func loadNotifierConfig() notifierConfig {
	cfg := notifierConfig{
		smsEnabled:   config.GetBool("notify.sms.enabled"),
		smsProvider:  strings.TrimSpace(config.GetString("notify.sms.provider")),
		smsEndpoint:  strings.TrimSpace(config.GetString("notify.sms.endpoint")),
		smsAccessKey: strings.TrimSpace(config.GetString("notify.sms.access_key")),
		smsSecretKey: strings.TrimSpace(config.GetString("notify.sms.secret_key")),
		smsSignName:  strings.TrimSpace(config.GetString("notify.sms.sign_name")),
		smsTemplate:  strings.TrimSpace(config.GetString("notify.sms.template_code")),

		mailEnabled:  config.GetBool("notify.mail.enabled"),
		mailEndpoint: strings.TrimSpace(config.GetString("notify.mail.endpoint")),
		mailToken:    strings.TrimSpace(config.GetString("notify.mail.token")),
		mailFrom:     strings.TrimSpace(config.GetString("notify.mail.from")),

		pushEnabled: config.GetBool("notify.push.enabled"),
		jpushAppKey: strings.TrimSpace(config.GetString("notify.push.app_key")),
		jpushMaster: strings.TrimSpace(config.GetString("notify.push.master_secret")),
		jpushHost:   strings.TrimSpace(config.GetString("notify.push.host")),
		// apns_production 默认值由 config.yml 显式声明（true=生产 APNs）
		pushProduction: config.GetBool("notify.push.production"),
	}
	cfg.smsTimeout = durationSeconds(config.GetInt("notify.sms.timeout"), 10)
	cfg.mailTimeout = durationSeconds(config.GetInt("notify.mail.timeout"), 10)
	cfg.pushTimeout = durationSeconds(config.GetInt("notify.push.timeout"), 10)
	if cfg.jpushHost == "" {
		cfg.jpushHost = defaultJPushHost
	}
	return cfg
}

// durationSeconds 秒数转 Duration，非正数取默认值
func durationSeconds(sec int, def int) time.Duration {
	if sec <= 0 {
		sec = def
	}
	return time.Duration(sec) * time.Second
}

// BuildNotifiers 依据配置构建全部可用渠道，返回注册表
//
// 站内信渠道由调用方注入（因为需要复用应用级 SSE Hub）。
func BuildNotifiers(hub SSEHub) *NotifierRegistry {
	cfg := loadNotifierConfig()
	reg := NewNotifierRegistry()
	reg.Register(NewInAppNotifier(hub))
	reg.Register(NewSMSNotifier(cfg))
	reg.Register(NewMailNotifier(cfg))
	reg.Register(NewPushNotifier(cfg))
	return reg
}

// logNotifierStartup 输出渠道启用状态，便于运维排查
func logNotifierStartup(reg *NotifierRegistry) {
	if reg == nil {
		return
	}
	log.Info("通知渠道初始化完成",
		zap.Strings("registered", reg.Names()),
		zap.Strings("disabled_missing_config", reg.Disabled()),
	)
}

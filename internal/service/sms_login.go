package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	"github.com/eko/gocache/lib/v4/store"
	cmfCache "github.com/wuwuseo/cmf/cache"
	cmfConfig "github.com/wuwuseo/cmf/config"
)

// ============ 短信验证码登录通道（平台级 user-auth） ============
//
// 存储选型：**复用既有缓存基础设施**（appInstance.Cache，默认 memory / 可切 redis），不新增 Ent 表：
//  1. 「短时效 + 一次性」天然是 TTL 语义，缓存驱动自带过期，不需要扫表清理；
//  2. 验证码是瞬时凭据、没有只增账本价值，为它新增 schema 要跑 go generate + 迁移，收益为负；
//  3. 缓存写入失败时发码**明确失败**（不假成功），不会出现「用户收不到码却提示已发送」。
//
// 一次性口径：命中后**先删除再签发**；同一进程内按手机号摘要串行化（smsLoginKeyedMutex），
// 因此并发重放同一验证码只有一次能成功。多实例 + Redis 存储时缓存的 Get/Delete 非原子，
// 跨实例同时校验同一个码存在极小双花窗口（见交付报告「残留风险」）。

const (
	// login_sms_code_ttl_seconds 验证码有效期（秒）；<=0 / 非法回落 300。
	smsCodeTTLConfigKey = "login_sms_code_ttl_seconds"
	// login_sms_send_interval_seconds 同一手机号发码最小间隔（秒）；<=0 / 非法回落 60。
	smsCodeIntervalConfigKey = "login_sms_send_interval_seconds"
	// login_sms_max_verify_attempts 同一验证码最大校验次数；<=0 / 非法回落 5。
	smsCodeMaxAttemptsConfigKey = "login_sms_max_verify_attempts"
	// login_sms_phone_daily_limit 同一手机号每日发送上限；0 = 不限，非法/缺失回落 10。
	smsCodePhoneDailyConfigKey = "login_sms_phone_daily_limit"
	// login_sms_ip_daily_limit 同一 IP 每日发送上限；0 = 不限，非法/缺失回落 20。
	smsCodeIPDailyConfigKey = "login_sms_ip_daily_limit"

	smsCodeTTLDefault         = 300
	smsCodeIntervalDefault    = 60
	smsCodeMaxAttemptsDefault = 5
	smsCodePhoneDailyDefault  = 10
	smsCodeIPDailyDefault     = 20

	smsCodeDigits = 6

	// 缓存键前缀；手机号一律经 smsLoginPhoneDigest 摘要后入键，键里不出现完整手机号。
	smsCodeKeyPrefix         = "user:login:sms:code:"
	smsCodeIntervalKeyPrefix = "user:login:sms:interval:"
	smsCodePhoneCountPrefix  = "user:login:sms:phone-count:"
	smsCodeIPCountPrefix     = "user:login:sms:ip-count:"

	// smsLoginBusinessZoneOffset 业务日时区偏移（UTC+8），与仓库既有业务日口径一致。
	smsLoginBusinessZoneOffset = 8 * 3600
)

var (
	// ErrInvalidPhone 手机号格式不正确。
	ErrInvalidPhone = errors.New("手机号格式不正确")
	// ErrSMSChannelUnavailable 短信网关未配置：明确失败，不伪成功。
	ErrSMSChannelUnavailable = errors.New("短信渠道未配置")
	// ErrSMSStoreUnavailable 验证码存储不可用：明确失败，不放行。
	ErrSMSStoreUnavailable = errors.New("验证码存储不可用")
	// ErrSMSCodeTooFrequent 距上次发码不足间隔。
	ErrSMSCodeTooFrequent = errors.New("验证码发送过于频繁")
	// ErrSMSCodeSendLimit 手机号或 IP 的当日发送次数已达上限。
	ErrSMSCodeSendLimit = errors.New("验证码发送次数已达上限")
	// ErrSMSCodeInvalid 码登录统一失败口径：**所有**校验失败都返回它，
	// 使「手机号是否已注册」无法通过错误差异被探测。
	ErrSMSCodeInvalid = errors.New("手机号或验证码错误")

	smsCodeInputPattern = regexp.MustCompile(`^\d{6}$`)
)

// SendSMSCodeRequest 发送短信验证码请求
type SendSMSCodeRequest struct {
	Phone string `json:"phone" validate:"required,len=11,numeric"`
}

// SMSLoginRequest 短信验证码登录请求
type SMSLoginRequest struct {
	Phone string `json:"phone" validate:"required,len=11,numeric"`
	Code  string `json:"code" validate:"required,len=6,numeric"`
}

// SendSMSCodeResponse 发码响应：只回答「是否已发送」，**不含验证码本身**。
type SendSMSCodeResponse struct {
	Sent      bool `json:"sent"`
	ExpiresIn int  `json:"expires_in"`
}

// smsCodeRecord 验证码存储记录：只存验证码摘要，不存明文。
type smsCodeRecord struct {
	CodeHash  string `json:"code_hash"`
	Attempts  int    `json:"attempts"`
	ExpiresAt int64  `json:"expires_at"`
}

// SMSCodeStore 验证码存储抽象（复用既有缓存基础设施，单测可注入替身）。
type SMSCodeStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// cacheSMSCodeStore 基于既有 cmf 缓存的实现（memory / redis 驱动皆可）。
type cacheSMSCodeStore struct {
	cache *cmfCache.Cache[[]byte]
}

// NewCacheSMSCodeStore 用既有缓存构造验证码存储；缓存为空时返回 nil（发码/登录将明确失败）。
func NewCacheSMSCodeStore(cache *cmfCache.Cache[[]byte]) SMSCodeStore {
	if cache == nil {
		return nil
	}
	return &cacheSMSCodeStore{cache: cache}
}

func (s *cacheSMSCodeStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if s == nil || s.cache == nil {
		return nil, false, ErrSMSStoreUnavailable
	}
	value, err := s.cache.Get(ctx, key)
	if err != nil {
		// 未命中的错误形态随驱动而异（redis=store.NotFound、memory=bigcache.ErrEntryNotFound），
		// 与既有服务（blacklist / permission）一致：Get 错误一律按「无记录」处理；
		// 真正的存储不可用会在写入路径显式失败，不会被静默吞掉。
		return nil, false, nil
	}
	if len(value) == 0 {
		return nil, false, nil
	}
	return value, true, nil
}

func (s *cacheSMSCodeStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if s == nil || s.cache == nil {
		return ErrSMSStoreUnavailable
	}
	if ttl <= 0 {
		return fmt.Errorf("验证码有效期非法: %s", ttl)
	}
	return s.cache.Set(ctx, key, value, store.WithExpiration(ttl))
}

func (s *cacheSMSCodeStore) Delete(ctx context.Context, key string) error {
	if s == nil || s.cache == nil {
		return ErrSMSStoreUnavailable
	}
	return s.cache.Delete(ctx, key)
}

// smsLoginKeyedMutex 按手机号摘要串行化发码/校验，避免同一验证码被并发消费两次。
// 采用引用计数避免被攻击者用海量手机号撑爆 map。
type smsLoginKeyedMutex struct {
	mu      sync.Mutex
	entries map[string]*smsLoginKeyedMutexEntry
}

type smsLoginKeyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

func newSMSLoginKeyedMutex() *smsLoginKeyedMutex {
	return &smsLoginKeyedMutex{entries: make(map[string]*smsLoginKeyedMutexEntry)}
}

// lock 获取 key 上的互斥锁，返回释放函数。
func (m *smsLoginKeyedMutex) lock(key string) func() {
	m.mu.Lock()
	entry, ok := m.entries[key]
	if !ok {
		entry = &smsLoginKeyedMutexEntry{}
		m.entries[key] = entry
	}
	entry.refs++
	m.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		m.mu.Lock()
		entry.refs--
		if entry.refs <= 0 {
			delete(m.entries, key)
		}
		m.mu.Unlock()
	}
}

// SMSCodeService 短信验证码登录服务（发码 + 码登录）。
type SMSCodeService struct {
	client    *ent.Client
	store     SMSCodeStore
	notifiers *NotifierRegistry
	now       func() time.Time
	generate  func() (string, error)
	locks     *smsLoginKeyedMutex
}

// NewSMSCodeService 构造短信验证码登录服务。
func NewSMSCodeService(client *ent.Client, store SMSCodeStore, notifiers *NotifierRegistry) *SMSCodeService {
	return &SMSCodeService{
		client:    client,
		store:     store,
		notifiers: notifiers,
		now:       time.Now,
		generate:  generateSMSCode,
		locks:     newSMSLoginKeyedMutex(),
	}
}

// SendCode 发送登录验证码：返回是否已发送（**不返回验证码**）。
//
// 顺序即安全口径：先判渠道与限流 → 落库验证码 → 占位发码间隔 → 投递 → 成功后计当日额度。
// 任一环节失败都明确返回错误并回收已写入的键，绝不返回「已发送」。
func (s *SMSCodeService) SendCode(ctx context.Context, phone, ip string) (*SendSMSCodeResponse, error) {
	phone = strings.TrimSpace(phone)
	if !phonePattern.MatchString(phone) {
		return nil, ErrInvalidPhone
	}
	if s == nil || s.notifiers == nil {
		return nil, ErrSMSChannelUnavailable
	}
	notifier, ok := s.notifiers.Get(ChannelSMS)
	if !ok || notifier == nil || !notifier.Enabled() {
		// 不假成功：网关未配置时明确失败，绝不返回「已发送」。
		return nil, ErrSMSChannelUnavailable
	}
	if s.store == nil {
		return nil, ErrSMSStoreUnavailable
	}

	ttl := time.Duration(s.configInt(ctx, smsCodeTTLConfigKey, smsCodeTTLDefault, false)) * time.Second
	interval := time.Duration(s.configInt(ctx, smsCodeIntervalConfigKey, smsCodeIntervalDefault, false)) * time.Second
	maxAttempts := s.configInt(ctx, smsCodeMaxAttemptsConfigKey, smsCodeMaxAttemptsDefault, false)
	phoneLimit := s.configInt(ctx, smsCodePhoneDailyConfigKey, smsCodePhoneDailyDefault, true)
	ipLimit := s.configInt(ctx, smsCodeIPDailyConfigKey, smsCodeIPDailyDefault, true)

	unlock := s.locks.lock(smsLoginPhoneDigest(phone))
	defer unlock()

	now := s.now()
	day, dayTTL := smsLoginBusinessDay(now)

	if _, exists, err := s.store.Get(ctx, smsCodeIntervalKey(phone)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSMSStoreUnavailable, err)
	} else if exists {
		return nil, ErrSMSCodeTooFrequent
	}

	phoneCount, err := s.counterValue(ctx, smsCodePhoneCountKey(phone, day))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSMSStoreUnavailable, err)
	}
	if phoneLimit > 0 && phoneCount >= phoneLimit {
		return nil, ErrSMSCodeSendLimit
	}
	ipKey := ""
	ipCount := 0
	if strings.TrimSpace(ip) != "" && ipLimit > 0 {
		ipKey = smsCodeIPCountKey(ip, day)
		if ipCount, err = s.counterValue(ctx, ipKey); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSMSStoreUnavailable, err)
		}
		if ipCount >= ipLimit {
			return nil, ErrSMSCodeSendLimit
		}
	}

	code, err := s.generate()
	if err != nil {
		return nil, fmt.Errorf("生成短信验证码失败: %w", err)
	}
	record, err := json.Marshal(smsCodeRecord{
		CodeHash:  smsCodeHash(phone, code),
		Attempts:  maxAttempts,
		ExpiresAt: now.Add(ttl).Unix(),
	})
	if err != nil {
		return nil, fmt.Errorf("序列化短信验证码记录失败: %w", err)
	}

	codeKey := smsCodeKey(phone)
	intervalKey := smsCodeIntervalKey(phone)
	written := make([]string, 0, 2)
	rollback := func(failure error) (*SendSMSCodeResponse, error) {
		for _, key := range written {
			_ = s.store.Delete(ctx, key)
		}
		return nil, failure
	}
	if err := s.store.Set(ctx, codeKey, record, ttl); err != nil {
		return rollback(fmt.Errorf("%w: %v", ErrSMSStoreUnavailable, err))
	}
	written = append(written, codeKey)
	if err := s.store.Set(ctx, intervalKey, []byte("1"), interval); err != nil {
		return rollback(fmt.Errorf("%w: %v", ErrSMSStoreUnavailable, err))
	}
	written = append(written, intervalKey)

	if err := notifier.Send(ctx, NotifyPayload{
		Type:    MessageTypeSystem,
		Level:   MessageLevelInfo,
		Title:   "登录验证码",
		Content: fmt.Sprintf("您的登录验证码是 %s，%s内有效。请勿向任何人泄露。", code, smsLoginTTLText(ttl)),
		Extra:   map[string]any{"phone": phone},
	}); err != nil {
		return rollback(fmt.Errorf("短信验证码发送失败: %w", err))
	}

	// 投递成功后才计当日额度：渠道故障不占用用户额度。
	if err := s.store.Set(ctx, smsCodePhoneCountKey(phone, day), []byte(strconv.Itoa(phoneCount+1)), dayTTL); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSMSStoreUnavailable, err)
	}
	if ipKey != "" {
		if err := s.store.Set(ctx, ipKey, []byte(strconv.Itoa(ipCount+1)), dayTTL); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSMSStoreUnavailable, err)
		}
	}

	return &SendSMSCodeResponse{Sent: true, ExpiresIn: int(ttl / time.Second)}, nil
}

// LoginBySMS 手机号 + 验证码登录：校验一次性验证码，成功时签发**与密码登录同一套**令牌。
//
// 安全口径：一经验证码校验失败（缺失 / 过期 / 次数用尽 / 不匹配）与
// 「验证码正确但手机号不存在或不可用」**返回同一个 ErrSMSCodeInvalid**，
// 调用方无法据此判断手机号是否已注册。
func (s *SMSCodeService) LoginBySMS(ctx context.Context, phone, code string) (*TokenResponse, error) {
	phone = strings.TrimSpace(phone)
	code = strings.TrimSpace(code)
	if !phonePattern.MatchString(phone) || !smsCodeInputPattern.MatchString(code) {
		return nil, ErrSMSCodeInvalid
	}
	if s == nil || s.store == nil {
		return nil, ErrSMSStoreUnavailable
	}

	unlock := s.locks.lock(smsLoginPhoneDigest(phone))
	defer unlock()

	now := s.now()
	codeKey := smsCodeKey(phone)
	raw, ok, err := s.store.Get(ctx, codeKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSMSStoreUnavailable, err)
	}
	if !ok {
		return nil, ErrSMSCodeInvalid
	}

	var record smsCodeRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.CodeHash == "" {
		_ = s.store.Delete(ctx, codeKey)
		return nil, ErrSMSCodeInvalid
	}
	if now.Unix() > record.ExpiresAt {
		_ = s.store.Delete(ctx, codeKey)
		return nil, ErrSMSCodeInvalid
	}
	if subtle.ConstantTimeCompare([]byte(record.CodeHash), []byte(smsCodeHash(phone, code))) != 1 {
		record.Attempts--
		remaining := time.Unix(record.ExpiresAt, 0).Sub(now)
		if record.Attempts <= 0 || remaining <= 0 {
			_ = s.store.Delete(ctx, codeKey)
		} else {
			updated, marshalErr := json.Marshal(record)
			if marshalErr != nil {
				return nil, fmt.Errorf("序列化短信验证码记录失败: %w", marshalErr)
			}
			if err := s.store.Set(ctx, codeKey, updated, remaining); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrSMSStoreUnavailable, err)
			}
		}
		return nil, ErrSMSCodeInvalid
	}

	// 命中即消费（先删除再签发）：同一验证码不可重放。
	if err := s.store.Delete(ctx, codeKey); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSMSStoreUnavailable, err)
	}
	if s.client == nil {
		return nil, errors.New("数据库未初始化")
	}

	u, err := s.client.User.Query().Where(user.PhoneEQ(phone)).Only(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return nil, fmt.Errorf("查询用户失败: %w", err)
		}
		s.recordLoginAttempt(ctx, maskSMSLoginPhone(phone), 0, false, "账号不存在")
		return nil, ErrSMSCodeInvalid
	}
	if u.Status != user.StatusActive {
		s.recordLoginAttempt(ctx, u.Username, u.ID, false, "账号已禁用")
		return nil, ErrSMSCodeInvalid
	}

	cfg, err := smsLoginAppConfig()
	if err != nil {
		return nil, err
	}
	tokens, err := createTokenPair(
		u.UID, u.Username, cfg.App.Name, cfg.App.Secret,
		time.Duration(cfg.App.LoginExpires)*time.Second,
		time.Duration(cfg.App.RefreshExpires)*time.Second,
		func() time.Time { return now },
		u.TokenVersion,
	)
	if err != nil {
		return nil, err
	}

	// 与密码登录同口径：补写最近登录时间与来源 IP
	update := s.client.User.UpdateOne(u).SetLastLogin(now)
	if meta, ok := loginMetaFromContext(ctx); ok && meta.IP != "" {
		update = update.SetLastIP(meta.IP)
	}
	if _, err := update.Save(ctx); err != nil {
		return nil, fmt.Errorf("更新登录信息失败: %w", err)
	}
	s.recordLoginAttempt(ctx, u.Username, u.ID, true, "")
	return tokens, nil
}

// recordLoginAttempt 记录码登录成败日志；失败路径只写脱敏手机号，绝不写完整手机号。
func (s *SMSCodeService) recordLoginAttempt(ctx context.Context, account string, userID int, success bool, message string) {
	recordUserLoginAttempt(ctx, account, userID, success, message)
}

// configInt 读取整数型系统配置：缺失 / 非数字 / 负数回落默认值；
// allowZero=true 时接受 0（用于「0 = 不限」的日上限）。
func (s *SMSCodeService) configInt(ctx context.Context, key string, fallback int, allowZero bool) int {
	if s == nil || s.client == nil {
		return fallback
	}
	item, err := s.client.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if err != nil {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || value < 0 {
		return fallback
	}
	if value == 0 && !allowZero {
		return fallback
	}
	return value
}

// counterValue 读取当日计数；无记录 / 非法值按 0 处理。
func (s *SMSCodeService) counterValue(ctx context.Context, key string) (int, error) {
	raw, ok, err := s.store.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || value < 0 {
		return 0, nil
	}
	return value, nil
}

// smsLoginAppConfig 取用户端 JWT 配置（签发令牌复用全局 config 服务，与密码登录同源）。
func smsLoginAppConfig() (*cmfConfig.Config, error) {
	if global.Bootstrap == nil {
		return nil, errors.New("配置服务不可用")
	}
	raw, ok := global.Bootstrap.GetService("config")
	if !ok || raw == nil {
		return nil, errors.New("配置服务不可用")
	}
	cfg, ok := raw.(*cmfConfig.Config)
	if !ok || cfg == nil {
		return nil, errors.New("配置服务不可用")
	}
	return cfg, nil
}

// generateSMSCode 生成 6 位数字验证码（crypto/rand，无模偏差）。
func generateSMSCode() (string, error) {
	limit := big.NewInt(1)
	for i := 0; i < smsCodeDigits; i++ {
		limit.Mul(limit, big.NewInt(10))
	}
	value, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	return fmt.Sprintf("%0*d", smsCodeDigits, value), nil
}

// smsCodeHash 验证码摘要（手机号参与加盐），存储中不出现明文验证码。
func smsCodeHash(phone, code string) string {
	sum := sha256.Sum256([]byte(phone + "|" + code))
	return hex.EncodeToString(sum[:])
}

// smsLoginPhoneDigest 手机号摘要，用于缓存键（键里不出现完整手机号）。
func smsLoginPhoneDigest(phone string) string {
	sum := sha256.Sum256([]byte("sms-login|" + strings.TrimSpace(phone)))
	return hex.EncodeToString(sum[:])
}

func smsCodeKey(phone string) string {
	return smsCodeKeyPrefix + smsLoginPhoneDigest(phone)
}

func smsCodeIntervalKey(phone string) string {
	return smsCodeIntervalKeyPrefix + smsLoginPhoneDigest(phone)
}

func smsCodePhoneCountKey(phone, day string) string {
	return smsCodePhoneCountPrefix + smsLoginPhoneDigest(phone) + ":" + day
}

func smsCodeIPCountKey(ip, day string) string {
	return smsCodeIPCountPrefix + strings.TrimSpace(ip) + ":" + day
}

// maskSMSLoginPhone 手机号脱敏：日志与登录日志只允许出现脱敏号。
func maskSMSLoginPhone(phone string) string {
	phone = strings.TrimSpace(phone)
	if len(phone) != 11 {
		return "***"
	}
	return phone[:3] + "****" + phone[7:]
}

// smsLoginBusinessDay 返回当前业务日（UTC+8，格式 yyyymmdd）与到次日零点的 TTL。
func smsLoginBusinessDay(now time.Time) (string, time.Duration) {
	zone := time.FixedZone("UTC+8", smsLoginBusinessZoneOffset)
	local := now.In(zone)
	next := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, zone)
	return local.Format("20060102"), next.Sub(now)
}

// smsLoginTTLText 把有效期转成短信正文里的中文文案。
func smsLoginTTLText(ttl time.Duration) string {
	if ttl < time.Minute {
		return fmt.Sprintf("%d秒", int(ttl/time.Second))
	}
	return fmt.Sprintf("%d分钟", int((ttl+time.Minute-1)/time.Minute))
}

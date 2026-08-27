package internal

import (
	"os"
	"strings"
	"testing"

	"github.com/wuwuseo/cmf/config"
	cmflog "github.com/wuwuseo/cmf/log"
	"go.uber.org/zap"
)

// ============================================================================
//  ensureAppSecrets / generateRandomSecret / initialAdminPassword 缺口测试
//
//  背景：这些函数属于系统启动阶段的 JWT 密钥与初始凭证安全基线，
//  任一行为漂移都会造成：
//    - 弱密钥或空密钥被接受 → 所有签发 token 实际可伪造；
//    - 强密钥被错误替换 → 重启后已有用户会话全部失效；
//    - 生成函数长度/编码规则变化 → 多节点环境密钥强度不一致。
//
//  本文件的断言全部锁定语义：已知弱密钥必须被替换、强密钥保持不动、
//  生成密钥的 hex 长度与 byteLen*2 对应、环境变量读取优先于随机生成。
// ============================================================================

// ---------------------------------------------------------------------------
// 测试 setup
// ---------------------------------------------------------------------------

func setupSecretsTest(t *testing.T) {
	t.Helper()
	prevApp := appInstance
	appInstance = &App{Logger: cmflog.GetDefault()}
	t.Cleanup(func() { appInstance = prevApp })
}

// ---------------------------------------------------------------------------
// generateRandomSecret 长度与十六进制编码一致性
// ---------------------------------------------------------------------------

// TestGenerateRandomSecret_LengthMatchesByteLenTimesTwo 验证返回的 hex 字符串
// 长度等于 byteLen*2，确保"1字节=2hex字符"的编码契约。
func TestGenerateRandomSecret_LengthMatchesByteLenTimesTwo(t *testing.T) {
	setupSecretsTest(t)
	for _, tt := range []struct {
		byteLen int
	}{
		{byteLen: 0},
		{byteLen: 8},
		{byteLen: 12},
		{byteLen: 32},
		{byteLen: 64},
	} {
		got := generateRandomSecret(tt.byteLen)
		if len(got) != tt.byteLen*2 {
			t.Fatalf("byteLen=%d: 期望 hex 长度 %d，实际 %d (value=%q)",
				tt.byteLen, tt.byteLen*2, len(got), got)
		}
	}
}

// TestGenerateRandomSecret_IsValidHex 验证输出完全由十六进制字符组成。
// rand.Read 失败（极少发生）时函数会 panic，此测试也隐式覆盖正常路径。
func TestGenerateRandomSecret_IsValidHex(t *testing.T) {
	setupSecretsTest(t)
	got := generateRandomSecret(32)
	const hexdigits = "0123456789abcdef"
	for i := 0; i < len(got); i++ {
		c := got[i]
		if !strings.ContainsRune(hexdigits, rune(c)) {
			t.Fatalf("generateRandomSecret 输出含非十六进制字符 %q（位置 %d）完整值=%q", c, i, got)
		}
	}
}

// TestGenerateRandomSecret_NonTrivialCollisions 验证两次调用输出不同。
// 64位hex（32字节）随机值碰撞概率可忽略；若相等则 rand.Read 可能被
// 错误替换为确定性函数，属于严重安全回归。
func TestGenerateRandomSecret_NonTrivialCollisions(t *testing.T) {
	setupSecretsTest(t)
	a := generateRandomSecret(32)
	b := generateRandomSecret(32)
	if a == b {
		t.Fatalf("两次 generateRandomSecret(32) 得到相同值 %q，CSPRNG 可能被替换", a)
	}
}

// ---------------------------------------------------------------------------
// ensureAppSecrets：空/弱密钥被替换，强密钥保留
// ---------------------------------------------------------------------------

func TestEnsureAppSecrets_ReplacesEmptySecrets(t *testing.T) {
	setupSecretsTest(t)
	cfg := &config.Config{}
	// App.Secret / App.AdminSecret 零值均为空字符串

	ensureAppSecrets(cfg)

	if cfg.App.Secret == "" {
		t.Fatal("app.secret 为空时 ensureAppSecrets 应替换为非空")
	}
	if cfg.App.AdminSecret == "" {
		t.Fatal("app.admin_secret 为空时 ensureAppSecrets 应替换为非空")
	}
	// 替换后的值应为 32 字节 = 64 个 hex 字符
	if len(cfg.App.Secret) != 64 {
		t.Fatalf("替换后的 app.secret 长度应为 64 hex，实际 %d", len(cfg.App.Secret))
	}
	if len(cfg.App.AdminSecret) != 64 {
		t.Fatalf("替换后的 app.admin_secret 长度应为 64 hex，实际 %d", len(cfg.App.AdminSecret))
	}
}

// TestEnsureAppSecrets_ReplacesKnownWeakSecrets 锁定"弱密钥黑名单"语义：
// secret / admin-secret / admin-secret-key 这三个历史遗留默认值必须被替换。
func TestEnsureAppSecrets_ReplacesKnownWeakSecrets(t *testing.T) {
	setupSecretsTest(t)
	weakSecrets := []string{"secret", "admin-secret-key", "admin-secret"}
	for _, weak := range weakSecrets {
		t.Run("weak="+weak, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.App.Secret = weak
			cfg.App.AdminSecret = weak

			ensureAppSecrets(cfg)

			if cfg.App.Secret == weak {
				t.Fatalf("弱密钥 %q 作为 app.secret 时应被替换", weak)
			}
			if cfg.App.AdminSecret == weak {
				t.Fatalf("弱密钥 %q 作为 app.admin_secret 时应被替换", weak)
			}
		})
	}
}

// TestEnsureAppSecrets_StrongSecretsUntouched 验证强密钥（不在黑名单且非空）
// 不会被替换。若误替换，每次重启都会签发新密钥，导致全部已有 token 失效。
func TestEnsureAppSecrets_StrongSecretsUntouched(t *testing.T) {
	setupSecretsTest(t)
	strongUser := "this-is-a-very-strong-user-secret-123456"
	strongAdmin := "this-is-a-very-strong-admin-secret-789ABC"

	cfg := &config.Config{}
	cfg.App.Secret = strongUser
	cfg.App.AdminSecret = strongAdmin

	ensureAppSecrets(cfg)

	if cfg.App.Secret != strongUser {
		t.Fatalf("强 app.secret 不应被替换，原始=%q，处理后=%q", strongUser, cfg.App.Secret)
	}
	if cfg.App.AdminSecret != strongAdmin {
		t.Fatalf("强 app.admin_secret 不应被替换，原始=%q，处理后=%q", strongAdmin, cfg.App.AdminSecret)
	}
}

// TestEnsureAppSecrets_IndependentReplacement 验证 app.secret 与
// app.admin_secret 相互独立：只有弱的一方被替换，强的一方保持不变。
func TestEnsureAppSecrets_IndependentReplacement(t *testing.T) {
	setupSecretsTest(t)

	// 情形 A：用户密钥弱，管理员密钥强
	cfgA := &config.Config{}
	cfgA.App.Secret = "secret"
	cfgA.App.AdminSecret = "strong-admin-secret-xyz"
	ensureAppSecrets(cfgA)
	if cfgA.App.Secret == "secret" {
		t.Fatal("A: 弱 app.secret 应被替换")
	}
	if cfgA.App.AdminSecret != "strong-admin-secret-xyz" {
		t.Fatal("A: 强 app.admin_secret 不应被触动")
	}

	// 情形 B：管理员密钥弱，用户密钥强
	cfgB := &config.Config{}
	cfgB.App.Secret = "strong-user-secret-abc"
	cfgB.App.AdminSecret = "admin-secret"
	ensureAppSecrets(cfgB)
	if cfgB.App.Secret != "strong-user-secret-abc" {
		t.Fatal("B: 强 app.secret 不应被触动")
	}
	if cfgB.App.AdminSecret == "admin-secret" {
		t.Fatal("B: 弱 app.admin_secret 应被替换")
	}
}

// TestEnsureAppSecrets_BlacklistedSubstringsNotMisclassified 防止未来有人
// 把精确比较改成 strings.Contains 造成误判："my-secret" 不是已知弱密钥。
func TestEnsureAppSecrets_BlacklistedSubstringsNotMisclassified(t *testing.T) {
	setupSecretsTest(t)
	safe := []string{"my-secret", "secret-plus", "xadmin-secret-keyx", "admin-secret-extra"}
	for _, s := range safe {
		t.Run("safe="+s, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.App.Secret = s
			ensureAppSecrets(cfg)
			if cfg.App.Secret != s {
				t.Fatalf("%q 不在 weakSecrets 精确匹配中，不应被替换", s)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// initialAdminPassword：环境变量优先于随机生成
// ---------------------------------------------------------------------------

// recordingLogger 记录所有日志调用的消息与字段，用于断言敏感值未进入日志管道。
// 历史缺陷（VULN-H-001）将明文初始密码作为 zap.String("password", ...) 写入 Logger，
// 日志文件保留 180 天，任何读到日志的主体即可直接接管超级管理员账户。
type recordingLogger struct {
	entries []string
}

func (l *recordingLogger) record(msg string, fields []zap.Field) {
	entry := msg
	for _, f := range fields {
		// zap.Field.String 对 String 字段返回原始值，可捕获密码泄露
		entry += " " + f.Key + "=" + f.String
	}
	l.entries = append(l.entries, entry)
}

func (l *recordingLogger) Debug(msg string, fields ...zap.Field) { l.record(msg, fields) }
func (l *recordingLogger) Info(msg string, fields ...zap.Field)  { l.record(msg, fields) }
func (l *recordingLogger) Warn(msg string, fields ...zap.Field)  { l.record(msg, fields) }
func (l *recordingLogger) Error(msg string, fields ...zap.Field) { l.record(msg, fields) }
func (l *recordingLogger) Fatal(msg string, fields ...zap.Field) { l.record(msg, fields) }
func (l *recordingLogger) With(fields ...zap.Field) cmflog.Logger { return l }
func (l *recordingLogger) Sync() error                            { return nil }

// TestInitialAdminPassword_NotPersistedToLog 安全回归：随机生成的初始密码
// 绝不允许出现在任何日志输出中（含消息体与结构化字段）。
func TestInitialAdminPassword_NotPersistedToLog(t *testing.T) {
	setupSecretsTest(t)
	recorder := &recordingLogger{}
	appInstance.Logger = recorder

	prev, wasSet := os.LookupEnv("ADMIN_INITIAL_PASSWORD")
	os.Unsetenv("ADMIN_INITIAL_PASSWORD")
	t.Cleanup(func() {
		if wasSet {
			os.Setenv("ADMIN_INITIAL_PASSWORD", prev)
		}
	})

	pwd := initialAdminPassword()
	if pwd == "" {
		t.Fatal("未设置环境变量时应生成非空随机密码")
	}
	if len(recorder.entries) == 0 {
		t.Fatal("应至少记录一条不含密码的告警日志")
	}
	for _, entry := range recorder.entries {
		if strings.Contains(entry, pwd) {
			t.Fatalf("初始密码泄露到日志管道: %q", entry)
		}
	}
}

// TestInitialAdminPassword_RespectsEnvVar 验证设置了 ADMIN_INITIAL_PASSWORD
// 时，优先使用该值（不再生成随机密码写入日志）。
func TestInitialAdminPassword_RespectsEnvVar(t *testing.T) {
	setupSecretsTest(t)
	const wanted = "Env-Passw0rd-With-Length"
	prev, wasSet := os.LookupEnv("ADMIN_INITIAL_PASSWORD")
	os.Setenv("ADMIN_INITIAL_PASSWORD", wanted)
	t.Cleanup(func() {
		if wasSet {
			os.Setenv("ADMIN_INITIAL_PASSWORD", prev)
		} else {
			os.Unsetenv("ADMIN_INITIAL_PASSWORD")
		}
	})

	got := initialAdminPassword()
	if got != wanted {
		t.Fatalf("设置环境变量后 initialAdminPassword 应返回 %q，实际 %q", wanted, got)
	}
}

// TestInitialAdminPassword_UnsetEnvGeneratesNonEmpty 验证未设置环境变量时
// 生成非空字符串作为一次性初始密码。这里不检查具体长度/格式以避免耦合
// generateRandomSecret 内部实现（该实现已有独立测试）。
func TestInitialAdminPassword_UnsetEnvGeneratesNonEmpty(t *testing.T) {
	setupSecretsTest(t)
	prev, wasSet := os.LookupEnv("ADMIN_INITIAL_PASSWORD")
	os.Unsetenv("ADMIN_INITIAL_PASSWORD")
	t.Cleanup(func() {
		if wasSet {
			os.Setenv("ADMIN_INITIAL_PASSWORD", prev)
		}
	})

	got := initialAdminPassword()
	if got == "" {
		t.Fatal("未设置环境变量时 initialAdminPassword 不应返回空字符串")
	}
}

// TestInitialAdminPassword_EmptyEnvVarTreatedAsUnset 验证环境变量显式设为
// 空字符串时仍走"生成随机密码"分支，防止部署时设置了空值但预期有默认值。
// ponytail: os.Getenv 无法区分"未设置"和"显式为空"，当前实现按空字符串=弱密码
// 处理（直接返回空会被 crypto.HashPassword 拒绝），所以必须生成随机值。
func TestInitialAdminPassword_EmptyEnvVarTreatedAsUnset(t *testing.T) {
	setupSecretsTest(t)
	prev, wasSet := os.LookupEnv("ADMIN_INITIAL_PASSWORD")
	os.Setenv("ADMIN_INITIAL_PASSWORD", "")
	t.Cleanup(func() {
		if wasSet {
			os.Setenv("ADMIN_INITIAL_PASSWORD", prev)
		} else {
			os.Unsetenv("ADMIN_INITIAL_PASSWORD")
		}
	})

	got := initialAdminPassword()
	if got == "" {
		t.Fatal("环境变量显式为空时 initialAdminPassword 仍应生成非空随机密码")
	}
}

// ---------------------------------------------------------------------------
// knownWeakSecrets 映射大小锁定：防新增弱密钥时忘记同步测试
// ---------------------------------------------------------------------------

// TestKnownWeakSecrets_EntriesCount 锁定 weakSecrets 的数量。
// 当有人新增一个弱密钥条目时，此测试必须同步更新以提醒审查者显式关注。
func TestKnownWeakSecrets_EntriesCount(t *testing.T) {
	const wantCount = 4 // "", "secret", "admin-secret-key", "admin-secret"
	if got := len(knownWeakSecrets); got != wantCount {
		t.Fatalf("knownWeakSecrets 条目数从 %d 变为 %d —— 若为新增请同步更新 ensureAppSecrets 相关的边界测试", wantCount, got)
	}
}

// TestKnownWeakSecrets_ExactSet 逐个断言 weakSecrets 的 key 集合与内容，
// 防止有人误删空字符串或历史默认值。
func TestKnownWeakSecrets_ExactSet(t *testing.T) {
	want := map[string]bool{
		"":                 true,
		"secret":           true,
		"admin-secret-key": true,
		"admin-secret":     true,
	}
	for k := range want {
		if !knownWeakSecrets[k] {
			t.Fatalf("knownWeakSecrets 缺少预期条目 %q", k)
		}
	}
	for k := range knownWeakSecrets {
		if !want[k] {
			t.Fatalf("knownWeakSecrets 包含未预期条目 %q（请同步更新边界测试）", k)
		}
	}
}

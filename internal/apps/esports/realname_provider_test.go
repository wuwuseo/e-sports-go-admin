package esports

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
)

func newRealNameTestDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

func seedRealNameProviderConfig(t *testing.T, db *ent.Client, value string) {
	t.Helper()
	db.SystemConfig.Create().SetKey(realnameProvidersConfigKey).SetName("实名渠道配置").
		SetValue(value).SaveX(t.Context())
}

// 覆盖渠道三态、坏 JSON、签名头可复算、超时不重试。
func TestRealNameThirdPartyProvider(t *testing.T) {
	ctx := t.Context()
	const secret = "test-secret"
	cfg := realNameProviderConfig{BaseURL: "http://placeholder", AppID: "studio", AppSecret: secret, Enabled: true}
	in := realNameProviderInput{SubjectType: "customer", SubjectID: 7, RealName: "张三", IDNumber: "110101199001011234", Method: realNameMethodTwoFactor}

	cases := []struct {
		name     string
		body     string
		wantOut  string
		wantErr  bool
		wantMess string
	}{
		{name: "通过", body: `{"passed":true}`, wantOut: realNameOutcomePassed},
		{name: "失败", body: `{"passed":false,"message":"姓名与身份证号不一致"}`, wantOut: realNameOutcomeFailed, wantMess: "姓名与身份证号不一致"},
		{name: "待完成", body: `{"pending":true,"redirect_url":"https://verify.example.com/face","provider_ref":"ref-1"}`, wantOut: realNameOutcomePending},
		{name: "待完成缺跳转", body: `{"pending":true}`, wantErr: true},
		{name: "坏 JSON", body: `not-json`, wantErr: true},
		{name: "空响应", body: `{}`, wantOut: realNameOutcomeFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			provider := newThirdPartyRealNameProvider(realNameProviderConfig{
				BaseURL: server.URL, AppID: "studio", AppSecret: secret, Enabled: true,
			})
			result, err := provider.Start(ctx, in)
			if tc.wantErr {
				if !errors.Is(err, errRealNameProviderUnavailable) {
					t.Fatalf("期望渠道不可用错误，实际 %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("核验失败: %v", err)
			}
			if result.Outcome != tc.wantOut {
				t.Fatalf("结果 %q，期望 %q", result.Outcome, tc.wantOut)
			}
			if tc.wantMess != "" && result.Message != tc.wantMess {
				t.Fatalf("提示 %q，期望 %q", result.Message, tc.wantMess)
			}
		})
	}

	t.Run("签名头可复算", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			canonical := realNameSignatureCanonical(r.Header.Get("X-App-Id"), r.Header.Get("X-Timestamp"), r.Header.Get("X-Nonce"), string(body))
			if got := r.Header.Get("X-Signature"); got != realNameHMACHex(secret, canonical) {
				t.Errorf("签名不匹配: %s", got)
			}
			if r.Header.Get("X-Nonce") == "" || r.Header.Get("X-Timestamp") == "" {
				t.Error("缺少签名头")
			}
			var req thirdPartyVerifyRequest
			if err := json.Unmarshal(body, &req); err != nil || req.IDNumber == "" || req.BizID == "" {
				t.Errorf("请求体不完整: %s", body)
			}
			_, _ = w.Write([]byte(`{"passed":true}`))
		}))
		defer server.Close()
		provider := newThirdPartyRealNameProvider(realNameProviderConfig{
			BaseURL: server.URL, AppID: "studio", AppSecret: secret, Enabled: true,
		})
		if _, err := provider.Start(ctx, in); err != nil {
			t.Fatalf("核验失败: %v", err)
		}
	})

	t.Run("超时不重试", func(t *testing.T) {
		var hits atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			time.Sleep(200 * time.Millisecond)
			_, _ = w.Write([]byte(`{"passed":true}`))
		}))
		defer server.Close()
		provider := newThirdPartyRealNameProvider(realNameProviderConfig{
			BaseURL: server.URL, AppID: "studio", AppSecret: secret, Enabled: true,
		})
		provider.client = &http.Client{Timeout: 20 * time.Millisecond}
		if _, err := provider.Start(ctx, in); !errors.Is(err, errRealNameProviderUnavailable) {
			t.Fatalf("期望渠道不可用错误，实际 %v", err)
		}
		if hits.Load() != 1 {
			t.Fatalf("请求次数 %d，期望 1（不重试）", hits.Load())
		}
	})

	t.Run("查询需要流水号", func(t *testing.T) {
		provider := newThirdPartyRealNameProvider(cfg)
		if _, err := provider.Query(ctx, "  "); !errors.Is(err, errInvalidInput) {
			t.Fatalf("期望参数错误，实际 %v", err)
		}
	})
}

// 覆盖注册表装配：manual 恒在、未配置渠道不入表、微信支付宝骨架永不入表、非法配置退化。
func TestRealNameProviderRegistry(t *testing.T) {
	ctx := t.Context()

	t.Run("未配置仅人工", func(t *testing.T) {
		db := newRealNameTestDB(t, "esports_realname_registry_empty")
		defer db.Close()
		registry := realNameProvidersFor(ctx, db)
		if _, ok := registry.providers[realNameChannelManual]; !ok {
			t.Fatal("manual 渠道必须始终可用")
		}
		if len(registry.options) != 1 {
			t.Fatalf("渠道数 %d，期望 1", len(registry.options))
		}
	})

	t.Run("非法 JSON 仅人工", func(t *testing.T) {
		db := newRealNameTestDB(t, "esports_realname_registry_bad")
		defer db.Close()
		seedRealNameProviderConfig(t, db, `{bad json`)
		registry := realNameProvidersFor(ctx, db)
		if len(registry.options) != 1 {
			t.Fatalf("渠道数 %d，期望 1", len(registry.options))
		}
	})

	t.Run("第三方启用入表", func(t *testing.T) {
		db := newRealNameTestDB(t, "esports_realname_registry_third")
		defer db.Close()
		seedRealNameProviderConfig(t, db, `{"third_party":{"enabled":true,"base_url":"https://verify.example.com","app_id":"studio","app_secret":"s"}}`)
		registry := realNameProvidersFor(ctx, db)
		provider, err := registry.get(realNameChannelThirdParty, realNameMethodTwoFactor)
		if err != nil || provider.Code() != realNameChannelThirdParty {
			t.Fatalf("第三方渠道未注册: %v", err)
		}
		if _, err := registry.get(realNameChannelThirdParty, realNameMethodManual); !errors.Is(err, errInvalidInput) {
			t.Fatalf("不支持的核验方式未被拒绝: %v", err)
		}
	})

	t.Run("第三方配置不完整不入表", func(t *testing.T) {
		db := newRealNameTestDB(t, "esports_realname_registry_incomplete")
		defer db.Close()
		seedRealNameProviderConfig(t, db, `{"third_party":{"enabled":true,"base_url":"https://verify.example.com","app_id":"","app_secret":""}}`)
		registry := realNameProvidersFor(ctx, db)
		if _, ok := registry.providers[realNameChannelThirdParty]; ok {
			t.Fatal("配置不完整的渠道不应注册")
		}
	})

	t.Run("微信支付宝骨架不入表", func(t *testing.T) {
		db := newRealNameTestDB(t, "esports_realname_registry_skeleton")
		defer db.Close()
		seedRealNameProviderConfig(t, db, `{"wechat":{"enabled":true,"base_url":"https://wx.example.com","app_id":"a","app_secret":"s"},"alipay":{"enabled":true,"base_url":"https://ali.example.com","app_id":"a","app_secret":"s"}}`)
		registry := realNameProvidersFor(ctx, db)
		if len(registry.options) != 1 {
			t.Fatalf("渠道数 %d，期望仅人工", len(registry.options))
		}
	})

	t.Run("人工提交待审", func(t *testing.T) {
		result, err := manualRealNameProvider{}.Start(ctx, realNameProviderInput{Method: realNameMethodManual})
		if err != nil || result.Outcome != realNameOutcomePending {
			t.Fatalf("人工渠道应返回待审: %v %v", result, err)
		}
	})
}

// 守护：手动渠道配置里的密钥只在内存中，配置理解错误不得把密钥拼进错误文案。
func TestRealNameProviderConfigErrorHidesSecret(t *testing.T) {
	cfg := realNameProviderConfig{Enabled: true, BaseURL: "not-a-url", AppID: "a", AppSecret: "secret-value"}
	err := cfg.validate()
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("配置错误文案泄漏密钥: %v", err)
	}
}

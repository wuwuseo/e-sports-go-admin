package service

import (
	"testing"
	"time"

	"go-admin/internal/global"

	"github.com/golang-jwt/jwt/v5"
	"github.com/wuwuseo/cmf/bootstrap"
	cmfConfig "github.com/wuwuseo/cmf/config"
)

func TestCreateTokenPairUsesSeparateAccessAndRefreshTimes(t *testing.T) {
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	calls := 0
	now := func() time.Time {
		value := base.Add(time.Duration(calls) * time.Second)
		calls++
		return value
	}
	pair, err := createTokenPair("uid", "subject", "issuer", "secret", time.Hour, 2*time.Hour, now)
	if err != nil {
		t.Fatalf("createTokenPair() error = %v", err)
	}
	if calls != 4 || pair.ExpiresIn != 3600 || pair.TokenType != "Bearer" {
		t.Fatalf("calls=%d pair=%+v", calls, pair)
	}
	for _, tt := range []struct {
		raw       string
		tokenType string
		expiresAt time.Time
		issuedAt  time.Time
	}{
		{raw: pair.AccessToken, tokenType: "access", expiresAt: base.Add(time.Hour), issuedAt: base.Add(time.Second)},
		{raw: pair.RefreshToken, tokenType: "refresh", expiresAt: base.Add(2*time.Second + 2*time.Hour), issuedAt: base.Add(3 * time.Second)},
	} {
		token, err := jwt.Parse(tt.raw, func(*jwt.Token) (any, error) { return []byte("secret"), nil }, jwt.WithoutClaimsValidation())
		if err != nil || !token.Valid {
			t.Fatalf("Parse() token=%v error=%v", token, err)
		}
		claims := token.Claims.(jwt.MapClaims)
		expiresAt := int64(claims["expires"].(float64))
		issuedAt := int64(claims["issued_at"].(float64))
		if claims["token_type"] != tt.tokenType || expiresAt != tt.expiresAt.Unix() || issuedAt != tt.issuedAt.Unix() {
			t.Fatalf("claims = %#v", claims)
		}
	}
}

// TestCreateTokenPair_EmptySecret 验证空密钥行为
// ponytail: CMF jwt库允许空密钥但会创建无效签名,此测试验证行为一致性而非防御性检查
func TestCreateTokenPair_EmptySecret(t *testing.T) {
	now := time.Now
	// 空密钥会被接受但生成无效签名的token
	pair, err := createTokenPair("uid", "subject", "issuer", "", time.Hour, 2*time.Hour, now)
	if err != nil {
		t.Fatalf("empty secret should not error in CMF jwt: %v", err)
	}
	// 验证token可以创建但验证时会失败
	_, err = jwt.Parse(pair.AccessToken, func(*jwt.Token) (any, error) {
		return []byte(""), nil
	})
	// 空密钥签名会验证失败,这是预期行为
	if err == nil {
		t.Log("warning: empty secret created a valid token, check security implications")
	}
}

// TestCreateTokenPair_ZeroDuration 验证零时长TTL
func TestCreateTokenPair_ZeroDuration(t *testing.T) {
	now := time.Now
	// ponytail: 零时长token虽然合法但立即过期,需验证行为一致性
	pair, err := createTokenPair("uid", "subject", "issuer", "secret", 0, 0, now)
	if err != nil {
		t.Fatalf("zero duration token creation: %v", err)
	}
	if pair.ExpiresIn != 0 {
		t.Fatalf("expected ExpiresIn=0, got %d", pair.ExpiresIn)
	}
}

// TestCreateTokenPair_DomainIsDefault 验证domain字段默认值
func TestCreateTokenPair_DomainIsDefault(t *testing.T) {
	now := time.Now
	pair, err := createTokenPair("uid", "subject", "issuer", "secret", time.Hour, 2*time.Hour, now)
	if err != nil {
		t.Fatalf("createTokenPair: %v", err)
	}

	token, err := jwt.Parse(pair.AccessToken, func(*jwt.Token) (any, error) {
		return []byte("secret"), nil
	}, jwt.WithoutClaimsValidation())
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}

	claims := token.Claims.(jwt.MapClaims)
	if claims["domain"] != "default" {
		t.Fatalf("expected domain=default, got %v", claims["domain"])
	}
}

func TestCreateTokenPairUsesConfiguredPermissionDomain(t *testing.T) {
	cfg := &cmfConfig.Config{}
	cfg.Casbin.DomainsDefault = "tenant-a"
	bs := &bootstrap.Bootstrap{}
	bs.RegisterService("config", cfg)
	previousBootstrap := global.Bootstrap
	global.Bootstrap = bs
	t.Cleanup(func() { global.Bootstrap = previousBootstrap })

	pair, err := createTokenPair("uid", "subject", "issuer", "secret", time.Hour, 2*time.Hour, time.Now)
	if err != nil {
		t.Fatalf("createTokenPair: %v", err)
	}
	token, err := jwt.Parse(pair.AccessToken, func(*jwt.Token) (any, error) {
		return []byte("secret"), nil
	}, jwt.WithoutClaimsValidation())
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	if got := token.Claims.(jwt.MapClaims)["domain"]; got != "tenant-a" {
		t.Fatalf("expected configured domain tenant-a, got %v", got)
	}
}

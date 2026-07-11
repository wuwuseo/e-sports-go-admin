package service

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
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

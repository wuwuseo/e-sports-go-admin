package handler

import (
	"strings"
	"testing"
	"time"
)

func TestDecryptLoginCredentialsValidationOrder(t *testing.T) {
	now := time.Now().Unix()
	tests := []struct {
		name      string
		timestamp int64
		nonce     string
		keyID     string
		want      string
	}{
		{name: "missing timestamp", nonce: "unused", keyID: "unused", want: "Missing timestamp"},
		{name: "expired timestamp", timestamp: now - 301, nonce: "unused", keyID: "unused", want: "Timestamp expired"},
		{name: "missing nonce", timestamp: now, keyID: "unused", want: "Missing nonce"},
		{name: "missing key", timestamp: now, nonce: "missing-key-" + t.Name(), want: "Missing key_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := decryptLoginCredentials("identity", "password", tt.timestamp, tt.nonce, tt.keyID, "email")
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDecryptLoginCredentialsConsumesNonceBeforeKeyValidation(t *testing.T) {
	nonce := "consumed-" + t.Name()
	_, _, first := decryptLoginCredentials("identity", "password", time.Now().Unix(), nonce, "", "email")
	_, _, second := decryptLoginCredentials("identity", "password", time.Now().Unix(), nonce, "", "email")
	if first == nil || first.Error() != "Missing key_id" {
		t.Fatalf("first error = %v", first)
	}
	if second == nil || !strings.Contains(second.Error(), "Nonce already used") {
		t.Fatalf("second error = %v", second)
	}
}

package oauth

import (
	"bytes"
	"testing"
)

func TestSecretCipherRoundTrip(t *testing.T) {
	cipher, err := NewSecretCipher(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatalf("NewSecretCipher: %v", err)
	}

	encrypted, err := cipher.Encrypt("github-client-secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if encrypted == "github-client-secret" || bytes.Contains([]byte(encrypted), []byte("github-client-secret")) {
		t.Fatal("ciphertext leaked plaintext")
	}

	decrypted, err := cipher.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted != "github-client-secret" {
		t.Fatalf("unexpected plaintext %q", decrypted)
	}
}

func TestSecretCipherRejectsInvalidKeyLength(t *testing.T) {
	if _, err := NewSecretCipher([]byte("too-short")); err == nil {
		t.Fatal("expected invalid key length error")
	}
}

package oauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const secretCipherVersion = "v1"

var ErrInvalidMasterKey = errors.New("OAuth config master key must decode to 32 bytes")

type SecretCipher struct {
	aead cipher.AEAD
}

func NewSecretCipher(key []byte) (*SecretCipher, error) {
	if len(key) != 32 {
		return nil, ErrInvalidMasterKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SecretCipher{aead: aead}, nil
}

func NewSecretCipherFromEnv() (*SecretCipher, error) {
	encoded := strings.TrimSpace(os.Getenv("OAUTH_CONFIG_MASTER_KEY"))
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode OAuth config master key: %w", ErrInvalidMasterKey)
	}
	return NewSecretCipher(key)
}

func (c *SecretCipher) Encrypt(plaintext string) (string, error) {
	if c == nil || c.aead == nil {
		return "", ErrInvalidMasterKey
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nil, nonce, []byte(plaintext), nil)
	payload := append(nonce, sealed...)
	return secretCipherVersion + ":" + base64.RawURLEncoding.EncodeToString(payload), nil
}

func (c *SecretCipher) Decrypt(ciphertext string) (string, error) {
	if c == nil || c.aead == nil {
		return "", ErrInvalidMasterKey
	}
	version, encoded, ok := strings.Cut(ciphertext, ":")
	if !ok || version != secretCipherVersion {
		return "", errors.New("unsupported OAuth secret ciphertext")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode OAuth secret ciphertext: %w", err)
	}
	if len(payload) < c.aead.NonceSize() {
		return "", errors.New("invalid OAuth secret ciphertext")
	}
	nonce := payload[:c.aead.NonceSize()]
	plaintext, err := c.aead.Open(nil, nonce, payload[c.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("decrypt OAuth secret ciphertext failed")
	}
	return string(plaintext), nil
}

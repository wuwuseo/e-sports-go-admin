package utils

import (
	"strings"
	"testing"

	"github.com/wuwuseo/cmf/crypto"
)

func TestGenerateStrongPassword(t *testing.T) {
	for range 20 {
		pwd, err := GenerateStrongPassword(16)
		if err != nil {
			t.Fatalf("GenerateStrongPassword() error = %v", err)
		}
		if len(pwd) != 16 {
			t.Fatalf("len = %d, want 16", len(pwd))
		}
		if err := ValidatePasswordStrength(pwd); err != nil {
			t.Fatalf("生成的密码未通过强度校验: %v (pwd=%q)", err, pwd)
		}
	}
}

func TestGenerateStrongPassword_Unique(t *testing.T) {
	first, err := GenerateStrongPassword(16)
	if err != nil {
		t.Fatalf("GenerateStrongPassword() error = %v", err)
	}
	second, err := GenerateStrongPassword(16)
	if err != nil {
		t.Fatalf("GenerateStrongPassword() error = %v", err)
	}
	if first == second {
		t.Fatal("两次生成的密码不应相同")
	}
}

func TestGenerateStrongPassword_InvalidLength(t *testing.T) {
	if _, err := GenerateStrongPassword(3); err == nil {
		t.Fatal("长度低于最低要求时应返回错误")
	}
}

func TestGenerateStrongPassword_CharactersSafe(t *testing.T) {
	pwd, err := GenerateStrongPassword(16)
	if err != nil {
		t.Fatalf("GenerateStrongPassword() error = %v", err)
	}
	// 生成的临时密码会通过 HTTP 响应一次性返回给管理员，
	// 只允许字母数字与常见安全符号，避免空格/引号等展示歧义。
	for _, r := range pwd {
		if strings.ContainsRune(" \t\"'\\", r) {
			t.Fatalf("密码包含易混淆字符 %q (pwd=%q)", r, pwd)
		}
	}
}

func TestValidatePasswordStrength_AcceptsGenerated(t *testing.T) {
	pwd, err := GenerateStrongPassword(16)
	if err != nil {
		t.Fatalf("GenerateStrongPassword() error = %v", err)
	}
	hashed, salt, err := crypto.HashPassword(pwd)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	match, err := crypto.VerifyPassword(pwd, hashed, salt)
	if err != nil || !match {
		t.Fatalf("生成的密码应可正常参与哈希校验: match=%v err=%v", match, err)
	}
}

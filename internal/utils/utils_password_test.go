package utils

import (
	"testing"
)

func TestValidatePasswordStrength_Empty(t *testing.T) {
	err := ValidatePasswordStrength("")
	if err == nil {
		t.Fatal("empty password should fail")
	}
}

func TestValidatePasswordStrength_TooShort(t *testing.T) {
	err := ValidatePasswordStrength("Abc1")
	if err == nil {
		t.Fatal("short password should fail")
	}
}

func TestValidatePasswordStrength_TooLong(t *testing.T) {
	longPwd := make([]byte, 130)
	for i := range longPwd {
		longPwd[i] = 'a'
	}
	err := ValidatePasswordStrength(string(longPwd))
	if err == nil {
		t.Fatal("long password should fail")
	}
}

func TestValidatePasswordStrength_Weak(t *testing.T) {
	cases := []string{"abcdefgh", "ABCDEFGH", "12345678", "abc12345"}
	for _, pwd := range cases {
		err := ValidatePasswordStrength(pwd)
		if err == nil {
			t.Errorf("weak password %q should fail", pwd)
		}
	}
}

func TestValidatePasswordStrength_Strong(t *testing.T) {
	cases := []string{"Abc12345", "Abc!@#456", "123!@#Abc", "Abc12!@#"}
	for _, pwd := range cases {
		err := ValidatePasswordStrength(pwd)
		if err != nil {
			t.Errorf("strong password %q should pass: %v", pwd, err)
		}
	}
}

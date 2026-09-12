package utils

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
)

// 强密码字符集：四类字符各占一段，不含空格、引号、反斜杠等易混淆字符，
// 以便初始化密码通过接口一次性返回给管理员时无展示歧义。
const (
	strongPasswordLower   = "abcdefghijklmnopqrstuvwxyz"
	strongPasswordUpper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	strongPasswordDigits  = "0123456789"
	strongPasswordSymbols = "!@#$%^*_-+=?"
	// 验证强度要求最少 8 位；临时密码默认再留一倍余量。
	minStrongPasswordLength = 8
)

// GenerateStrongPassword 生成指定长度的随机强密码：
// 保证同时包含小写、大写、数字和特殊字符，可通过 ValidatePasswordStrength 校验。
func GenerateStrongPassword(length int) (string, error) {
	if length < minStrongPasswordLength {
		return "", fmt.Errorf("密码长度不能低于 %d 位", minStrongPasswordLength)
	}

	groups := []string{
		strongPasswordLower,
		strongPasswordUpper,
		strongPasswordDigits,
		strongPasswordSymbols,
	}
	all := stringsConcat(groups...)

	chars := make([]byte, 0, length)
	for _, group := range groups {
		c, err := randomChar(group)
		if err != nil {
			return "", err
		}
		chars = append(chars, c)
	}
	for len(chars) < length {
		c, err := randomChar(all)
		if err != nil {
			return "", err
		}
		chars = append(chars, c)
	}

	shuffleBytes(chars)
	return string(chars), nil
}

func stringsConcat(parts ...string) string {
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	buf := make([]byte, 0, total)
	for _, p := range parts {
		buf = append(buf, p...)
	}
	return string(buf)
}

func randomChar(charset string) (byte, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
	if err != nil {
		return 0, errors.New("生成随机密码失败")
	}
	return charset[n.Int64()], nil
}

// shuffleBytes 使用 crypto/rand 做 Fisher-Yates 洗牌，
// 避免四段字符固定出现在开头导致模式可预测。
func shuffleBytes(chars []byte) {
	for i := len(chars) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			// 随机源异常时保持原顺序：密码仍满足字符分类要求
			return
		}
		j := n.Int64()
		chars[i], chars[j] = chars[j], chars[i]
	}
}

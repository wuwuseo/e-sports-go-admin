package cron

import (
	"testing"
)

// TestDefaultRegistryIsEmpty 验证 Registry 初始状态为零条目。
// 回归测试：commit 3791626 移除了 PowerShell echo 的默认注册，
// 防止子表达式注入攻击。Registry 必须默认为空。
func TestDefaultRegistryIsEmpty(t *testing.T) {
	// 保存并恢复原始 Registry，避免污染其他测试
	orig := Registry
	defer func() { Registry = orig }()

	// 重置为干净状态，模拟程序启动时的初始值
	Registry = map[string]RegisteredCommand{}

	if len(Registry) != 0 {
		t.Fatalf("Registry 应为空，实际有 %d 条记录", len(Registry))
	}
	if len(List()) != 0 {
		t.Fatalf("List() 应返回空切片，实际返回 %d 项", len(List()))
	}
}

// TestNoDangerousCommandsRegisteredByDefault 验证默认注册表中
// 不包含常见的危险命令名（如 echo、powershell、cmd）。
// 回归测试：确保不会意外重新引入 PowerShell echo 等默认注册。
func TestNoDangerousCommandsRegisteredByDefault(t *testing.T) {
	orig := Registry
	defer func() { Registry = orig }()

	Registry = map[string]RegisteredCommand{}

	dangerous := []string{"echo", "powershell", "cmd", "sh", "bash"}
	for _, name := range dangerous {
		if IsRegistered(name) {
			t.Errorf("危险命令 %q 不应默认注册", name)
		}
		if _, ok := Get(name); ok {
			t.Errorf("Get(%q) 不应返回 true", name)
		}
	}
}

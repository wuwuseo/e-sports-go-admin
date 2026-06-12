package cron

import (
	"fmt"
	"os/exec"
	"runtime"
)

// RegisteredCommand 定义注册的安全命令
type RegisteredCommand struct {
	Path        string   // 命令的绝对路径
	Args        []string // 固定前置参数
	MaxArgs     int      // 最大用户参数数量，0 表示无限制（不推荐）
	Description string   // 命令描述
}

// Registry 全局安全命令注册表（仅允许注册过的命令被执行）
var Registry = map[string]RegisteredCommand{}

func init() {
	registerDefaultEcho()
}

func registerDefaultEcho() {
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("powershell.exe"); err == nil {
			RegisterWithArgs("echo", p, []string{"-NoProfile", "-NonInteractive", "-Command", "Write-Output"}, 20, "输出文本")
		}
		return
	}

	if p, err := exec.LookPath("echo"); err == nil {
		Register("echo", p, 20, "输出文本")
	}
}

// Register 注册一个安全命令
// name: 命令注册名（对应 cron job 的 command 字段第一段）
// path: 命令的绝对路径
// maxArgs: 最大参数数量，设为 0 表示参数数量无限制
// description: 命令描述
func Register(name string, path string, maxArgs int, description string) {
	RegisterWithArgs(name, path, nil, maxArgs, description)
}

// RegisterWithArgs 注册一个带固定前置参数的安全命令
func RegisterWithArgs(name string, path string, args []string, maxArgs int, description string) {
	Registry[name] = RegisteredCommand{
		Path:        path,
		Args:        args,
		MaxArgs:     maxArgs,
		Description: description,
	}
}

// Get 获取已注册的命令
func Get(name string) (RegisteredCommand, bool) {
	cmd, ok := Registry[name]
	return cmd, ok
}

// List 列出所有已注册的命令名称
func List() []string {
	names := make([]string, 0, len(Registry))
	for name := range Registry {
		names = append(names, name)
	}
	return names
}

// IsRegistered 检查命令名是否已注册
func IsRegistered(name string) bool {
	_, ok := Registry[name]
	return ok
}

// ValidateArgs 验证参数数量是否在允许范围内
func (c *RegisteredCommand) ValidateArgs(args []string) error {
	if c.MaxArgs > 0 && len(args) > c.MaxArgs {
		return fmt.Errorf("参数数量超限：最多允许 %d 个参数，实际提供了 %d 个", c.MaxArgs, len(args))
	}
	return nil
}

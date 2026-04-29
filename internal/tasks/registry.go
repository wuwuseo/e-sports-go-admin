package tasks

import (
	"context"
	"io"
)

// TaskFunc 定义 Go task 的函数签名
type TaskFunc func(ctx context.Context, logger io.Writer) error

// Registry 全局 task 注册表
var Registry = make(map[string]TaskFunc)

// Register 注册一个 Go task
func Register(name string, fn TaskFunc) {
	Registry[name] = fn
}

// Get 获取已注册的 task
func Get(name string) (TaskFunc, bool) {
	fn, ok := Registry[name]
	return fn, ok
}

// List 列出所有已注册的 task 名称
func List() []string {
	names := make([]string, 0, len(Registry))
	for name := range Registry {
		names = append(names, name)
	}
	return names
}

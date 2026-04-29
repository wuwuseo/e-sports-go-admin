package cron

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"go-admin/internal/tasks"
)

// Executor 定义执行器接口
type Executor interface {
	Execute(ctx context.Context, command string, logger io.Writer) error
}

// ShellExecutor 执行 shell 命令
type ShellExecutor struct{}

// Execute 使用系统 shell 执行命令
func (e *ShellExecutor) Execute(ctx context.Context, command string, logger io.Writer) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Stdout = logger
	cmd.Stderr = logger
	return cmd.Run()
}

// CurlExecutor 执行 HTTP 请求
type CurlExecutor struct{}

// curlCommand 定义 curl 命令的 JSON 格式
type curlCommand struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Timeout int               `json:"timeout,omitempty"` // 秒
}

// Execute 解析 JSON 配置并执行 HTTP 请求
func (e *CurlExecutor) Execute(ctx context.Context, command string, logger io.Writer) error {
	var cmd curlCommand
	if err := json.Unmarshal([]byte(command), &cmd); err != nil {
		return fmt.Errorf("解析curl命令失败: %w", err)
	}

	if cmd.URL == "" {
		return fmt.Errorf("URL不能为空")
	}
	if cmd.Method == "" {
		cmd.Method = "GET"
	}
	if cmd.Timeout <= 0 {
		cmd.Timeout = 30
	}

	timeout := time.Duration(cmd.Timeout) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var bodyReader io.Reader
	if cmd.Body != "" {
		bodyReader = strings.NewReader(cmd.Body)
	}

	req, err := http.NewRequestWithContext(ctx, cmd.Method, cmd.URL, bodyReader)
	if err != nil {
		return fmt.Errorf("创建HTTP请求失败: %w", err)
	}

	for k, v := range cmd.Headers {
		req.Header.Set(k, v)
	}

	fmt.Fprintf(logger, "[%s] %s %s\n", time.Now().Format(time.DateTime), cmd.Method, cmd.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("执行HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	fmt.Fprintf(logger, "[%s] 响应状态: %s\n", time.Now().Format(time.DateTime), resp.Status)

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, resp.Body); err != nil {
		return fmt.Errorf("读取响应体失败: %w", err)
	}

	body := buf.String()
	if len(body) > 4096 {
		body = body[:4096] + "\n... (truncated)"
	}
	fmt.Fprintf(logger, "[%s] 响应内容:\n%s\n", time.Now().Format(time.DateTime), body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP请求返回错误状态码: %d", resp.StatusCode)
	}

	return nil
}

// TaskExecutor 执行注册的 Go task
type TaskExecutor struct{}

// Execute 从 tasks.Registry 查找 task 函数并执行
func (e *TaskExecutor) Execute(ctx context.Context, command string, logger io.Writer) error {
	fn, ok := tasks.Get(command)
	if !ok {
		return fmt.Errorf("Go task '%s' 未注册", command)
	}
	return fn(ctx, logger)
}

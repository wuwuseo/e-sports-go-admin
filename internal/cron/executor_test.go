package cron

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"go-admin/internal/tasks"
)

// --- CommandExecutor Tests ---

func TestCommandRegistry_EchoRegisteredByDefault(t *testing.T) {
	cmd, ok := Get("echo")
	if !ok {
		t.Fatal("expected echo registered by default")
	}
	if cmd.Path == "" {
		t.Fatal("expected echo path not empty")
	}
	if runtime.GOOS == "windows" && len(cmd.Args) == 0 {
		t.Fatal("expected windows echo to include fixed powershell args")
	}
}

func TestCommandExecutor_RegisteredSimpleCommand(t *testing.T) {
	// 查找系统可用的 echo 命令
	p, err := exec.LookPath("echo")
	if err != nil {
		t.Skipf("echo command not found in PATH, skipping integration test: %v", err)
	}
	Register("echo_test", p, 5, "Echo test command")
	defer delete(Registry, "echo_test")

	executor := &CommandExecutor{}
	var buf bytes.Buffer

	err = executor.Execute(context.Background(), "echo_test hello", &buf)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	output := strings.TrimSpace(buf.String())
	if output != "hello" {
		t.Fatalf("expected output 'hello', got: %q", output)
	}
}

func TestCommandExecutor_RegisteredCommandWithFixedArgs(t *testing.T) {
	p, err := exec.LookPath("echo")
	if err != nil {
		t.Skipf("echo command not found in PATH, skipping integration test: %v", err)
	}
	RegisterWithArgs("echo_prefix_test", p, []string{"fixed"}, 5, "Echo with fixed args test")
	defer delete(Registry, "echo_prefix_test")

	executor := &CommandExecutor{}
	var buf bytes.Buffer

	err = executor.Execute(context.Background(), "echo_prefix_test user", &buf)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	output := strings.TrimSpace(buf.String())
	if output != "fixed user" {
		t.Fatalf("expected output 'fixed user', got: %q", output)
	}
}

func TestCommandExecutor_UnregisteredCommand(t *testing.T) {
	executor := &CommandExecutor{}
	var buf bytes.Buffer

	err := executor.Execute(context.Background(), "unregistered_command_xyz_12345", &buf)
	if err == nil {
		t.Fatal("expected error for unregistered command, got nil")
	}
	if !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("expected '未注册' in error, got: %v", err)
	}
}

func TestCommandExecutor_TooManyArgs(t *testing.T) {
	Register("maxargs_test", "echo", 1, "Max args test")
	defer delete(Registry, "maxargs_test")

	executor := &CommandExecutor{}
	var buf bytes.Buffer

	err := executor.Execute(context.Background(), "maxargs_test a b c", &buf)
	if err == nil {
		t.Fatal("expected error for too many args, got nil")
	}
	if !strings.Contains(err.Error(), "参数数量超限") {
		t.Fatalf("expected '参数数量超限' in error, got: %v", err)
	}
}

func TestCommandExecutor_EmptyCommand(t *testing.T) {
	executor := &CommandExecutor{}
	var buf bytes.Buffer

	err := executor.Execute(context.Background(), "   ", &buf)
	if err == nil {
		t.Fatal("expected error for empty command, got nil")
	}
	if !strings.Contains(err.Error(), "命令不能为空") {
		t.Fatalf("expected '命令不能为空' in error, got: %v", err)
	}
}

// --- CurlExecutor Tests ---

// curlCommandMarshal 辅助函数，构建 curl 命令 JSON
func curlCommandMarshal(method, url, body string, headers map[string]string) string {
	cmd := curlCommand{
		Method:  method,
		URL:     url,
		Headers: headers,
		Body:    body,
	}
	data, _ := json.Marshal(cmd)
	return string(data)
}

func TestCurlExecutor_InvalidJSON(t *testing.T) {
	executor := &CurlExecutor{}
	var buf bytes.Buffer

	err := executor.Execute(context.Background(), "not-valid-json{{{", &buf)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "解析curl命令失败") {
		t.Fatalf("expected parse error message, got: %v", err)
	}
}

func TestCurlExecutor_MissingURL(t *testing.T) {
	executor := &CurlExecutor{}
	var buf bytes.Buffer

	cmd, _ := json.Marshal(curlCommand{
		Method: "GET",
	})
	err := executor.Execute(context.Background(), string(cmd), &buf)
	if err == nil {
		t.Fatal("expected error for missing URL, got nil")
	}
	if !strings.Contains(err.Error(), "URL不能为空") {
		t.Fatalf("expected 'URL不能为空' in error, got: %v", err)
	}
}

func TestCurlExecutor_URLValidationBlocked(t *testing.T) {
	executor := &CurlExecutor{}
	var buf bytes.Buffer

	// 测试被封锁的内网IP URL
	cmd := curlCommandMarshal("GET", "http://127.0.0.1:8080/api", "", nil)
	err := executor.Execute(context.Background(), cmd, &buf)
	if err == nil {
		t.Fatal("expected error for blocked localhost URL, got nil")
	}
	if !strings.Contains(err.Error(), "URL验证失败") {
		t.Fatalf("expected 'URL验证失败' in error, got: %v", err)
	}
}

func TestCurlExecutor_URLValidationBlockedMetadata(t *testing.T) {
	executor := &CurlExecutor{}
	var buf bytes.Buffer

	// 测试被封锁的云元数据URL
	cmd := curlCommandMarshal("GET", "http://169.254.169.254/latest/meta-data/", "", nil)
	err := executor.Execute(context.Background(), cmd, &buf)
	if err == nil {
		t.Fatal("expected error for blocked metadata URL, got nil")
	}
	if !strings.Contains(err.Error(), "URL验证失败") {
		t.Fatalf("expected 'URL验证失败' in error, got: %v", err)
	}
}

func TestCurlExecutor_URLValidationBlockedFileProtocol(t *testing.T) {
	executor := &CurlExecutor{}
	var buf bytes.Buffer

	// 测试被封锁的 file:// 协议
	cmd := curlCommandMarshal("GET", "file:///etc/passwd", "", nil)
	err := executor.Execute(context.Background(), cmd, &buf)
	if err == nil {
		t.Fatal("expected error for file protocol, got nil")
	}
	if !strings.Contains(err.Error(), "URL验证失败") {
		t.Fatalf("expected 'URL验证失败' in error, got: %v", err)
	}
}

// --- validateURL 单元测试 ---

func TestValidateURL_ValidHTTPS(t *testing.T) {
	if err := validateURL("https://example.com/api"); err != nil {
		t.Fatalf("expected no error for valid URL, got: %v", err)
	}
}

func TestValidateURL_InvalidScheme(t *testing.T) {
	err := validateURL("ftp://example.com/file")
	if err == nil {
		t.Fatal("expected error for ftp scheme")
	}
	if !strings.Contains(err.Error(), "协议") {
		t.Fatalf("expected protocol error, got: %v", err)
	}
}

func TestValidateURL_Localhost(t *testing.T) {
	err := validateURL("http://localhost:8080")
	if err == nil {
		t.Fatal("expected error for localhost")
	}
}

func TestValidateURL_PrivateIP(t *testing.T) {
	err := validateURL("http://192.168.1.1/admin")
	if err == nil {
		t.Fatal("expected error for private IP")
	}
}

func TestValidateURL_CloudMetadataIP(t *testing.T) {
	err := validateURL("http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Fatal("expected error for metadata IP")
	}
}

func TestValidateURL_InvalidPort(t *testing.T) {
	err := validateURL("https://example.com:3000")
	if err == nil {
		t.Fatal("expected error for invalid port")
	}
	if !strings.Contains(err.Error(), "端口") {
		t.Fatalf("expected port error, got: %v", err)
	}
}

// --- isSafeHost 单元测试 ---

func TestIsSafeHost_LocalDomain(t *testing.T) {
	if err := isSafeHost("localhost"); err == nil {
		t.Fatal("expected error for localhost")
	}
}

func TestIsSafeHost_PublicDomain(t *testing.T) {
	if err := isSafeHost("example.com"); err != nil {
		t.Fatalf("expected no error for public domain, got: %v", err)
	}
}

func TestIsSafeHost_LoopbackIP(t *testing.T) {
	if err := isSafeHost("127.0.0.1"); err == nil {
		t.Fatal("expected error for loopback IP")
	}
}

func TestIsSafeHost_PrivateIP(t *testing.T) {
	if err := isSafeHost("10.0.0.1"); err == nil {
		t.Fatal("expected error for private IP")
	}
}

func TestIsSafeHost_PublicIP(t *testing.T) {
	if err := isSafeHost("8.8.8.8"); err != nil {
		t.Fatalf("expected no error for public IP, got: %v", err)
	}
}

// --- TaskExecutor Tests ---

func TestTaskExecutor_RegisteredTask(t *testing.T) {
	tasks.Register("test_executor_task", func(ctx context.Context, logger io.Writer) error {
		logger.Write([]byte("task executed successfully"))
		return nil
	})
	defer delete(tasks.Registry, "test_executor_task")

	executor := &TaskExecutor{}
	var buf bytes.Buffer

	err := executor.Execute(context.Background(), "test_executor_task", &buf)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if buf.String() != "task executed successfully" {
		t.Fatalf("expected 'task executed successfully', got: %q", buf.String())
	}
}

func TestTaskExecutor_UnregisteredTask(t *testing.T) {
	executor := &TaskExecutor{}
	var buf bytes.Buffer

	err := executor.Execute(context.Background(), "nonexistent_task_xyz", &buf)
	if err == nil {
		t.Fatal("expected error for unregistered task, got nil")
	}
	if !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("expected '未注册' error, got: %v", err)
	}
}

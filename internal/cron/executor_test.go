package cron

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-admin/internal/tasks"
)

// --- ShellExecutor Tests ---

func TestShellExecutor_SimpleCommand(t *testing.T) {
	executor := &ShellExecutor{}
	var buf bytes.Buffer

	err := executor.Execute(context.Background(), "echo hello", &buf)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	output := strings.TrimSpace(buf.String())
	if output != "hello" {
		t.Fatalf("expected output 'hello', got: %q", output)
	}
}

func TestShellExecutor_FailedCommand(t *testing.T) {
	executor := &ShellExecutor{}
	var buf bytes.Buffer

	err := executor.Execute(context.Background(), "nonexistent_command_xyz_12345", &buf)
	if err == nil {
		t.Fatal("expected error for non-existent command, got nil")
	}
}

// --- CurlExecutor Tests ---

func TestCurlExecutor_GetRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	executor := &CurlExecutor{}
	var buf bytes.Buffer

	cmd, _ := json.Marshal(curlCommand{
		Method: "GET",
		URL:    server.URL,
	})

	err := executor.Execute(context.Background(), string(cmd), &buf)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `{"status":"ok"}`) {
		t.Fatalf("expected response body in output, got: %s", output)
	}
	if !strings.Contains(output, "200") {
		t.Fatalf("expected status 200 in output, got: %s", output)
	}
}

func TestCurlExecutor_PostRequest(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("accepted"))
	}))
	defer server.Close()

	executor := &CurlExecutor{}
	var buf bytes.Buffer

	cmd, _ := json.Marshal(curlCommand{
		Method: "POST",
		URL:    server.URL,
		Body:   `{"key":"value"}`,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
	})

	err := executor.Execute(context.Background(), string(cmd), &buf)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if receivedBody != `{"key":"value"}` {
		t.Fatalf("expected body '{\"key\":\"value\"}', got: %q", receivedBody)
	}
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

func TestCurlExecutor_ErrorStatusCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer server.Close()

	executor := &CurlExecutor{}
	var buf bytes.Buffer

	cmd, _ := json.Marshal(curlCommand{
		URL: server.URL,
	})

	err := executor.Execute(context.Background(), string(cmd), &buf)
	if err == nil {
		t.Fatal("expected error for 500 status, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected error with status code 500, got: %v", err)
	}
}

// --- TaskExecutor Tests ---

func TestTaskExecutor_RegisteredTask(t *testing.T) {
	// Register a test task
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

package cron

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateLogFile(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewJobLogger(tmpDir, 24*time.Hour)

	f, filename, err := logger.CreateLogFile(1)
	if err != nil {
		t.Fatalf("CreateLogFile failed: %v", err)
	}
	defer f.Close()

	// Write content
	_, err = f.WriteString("test log content")
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Verify filename ends with .log
	if !strings.HasSuffix(filename, ".log") {
		t.Fatalf("expected .log suffix, got: %s", filename)
	}

	// Verify file exists on disk
	filePath := filepath.Join(tmpDir, "job-1", filename)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatalf("log file should exist at %s", filePath)
	}
}

func TestListLogs(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewJobLogger(tmpDir, 24*time.Hour)

	// Create multiple log files with slight delay for distinct timestamps
	jobDir := filepath.Join(tmpDir, "job-1")
	os.MkdirAll(jobDir, 0755)

	files := []string{"20240101-100000.log", "20240102-100000.log", "20240103-100000.log"}
	for _, name := range files {
		f, err := os.Create(filepath.Join(jobDir, name))
		if err != nil {
			t.Fatalf("create file failed: %v", err)
		}
		f.WriteString("content")
		f.Close()
	}

	logs, err := logger.ListLogs(1)
	if err != nil {
		t.Fatalf("ListLogs failed: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(logs))
	}

	// Verify sorted by time descending (most recent ModTime first)
	for i := 0; i < len(logs)-1; i++ {
		if logs[i].CreatedAt.Before(logs[i+1].CreatedAt) {
			t.Fatalf("logs should be sorted by time descending")
		}
	}
}

func TestListLogs_NoDir(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewJobLogger(tmpDir, 24*time.Hour)

	logs, err := logger.ListLogs(999)
	if err != nil {
		t.Fatalf("ListLogs should not error for non-existent dir: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected 0 logs, got %d", len(logs))
	}
}

func TestReadLog(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewJobLogger(tmpDir, 24*time.Hour)

	// Create log file manually
	jobDir := filepath.Join(tmpDir, "job-1")
	os.MkdirAll(jobDir, 0755)
	os.WriteFile(filepath.Join(jobDir, "test.log"), []byte("hello world"), 0644)

	content, err := logger.ReadLog(1, "test.log")
	if err != nil {
		t.Fatalf("ReadLog failed: %v", err)
	}
	if content != "hello world" {
		t.Fatalf("expected 'hello world', got: %q", content)
	}
}

func TestReadLog_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewJobLogger(tmpDir, 24*time.Hour)

	_, err := logger.ReadLog(1, "../../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
	if !strings.Contains(err.Error(), "无效的文件名") {
		t.Fatalf("expected invalid filename error, got: %v", err)
	}
}

func TestTailLog(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewJobLogger(tmpDir, 24*time.Hour)

	// Create log file with multiple lines
	jobDir := filepath.Join(tmpDir, "job-1")
	os.MkdirAll(jobDir, 0755)

	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	os.WriteFile(filepath.Join(jobDir, "20240101-120000.log"), []byte(strings.Join(lines, "\n")), 0644)

	// Tail last 5 lines
	result, err := logger.TailLog(1, 5)
	if err != nil {
		t.Fatalf("TailLog failed: %v", err)
	}

	resultLines := strings.Split(result, "\n")
	if len(resultLines) != 5 {
		t.Fatalf("expected 5 lines, got %d: %q", len(resultLines), result)
	}
	if resultLines[0] != "line 16" {
		t.Fatalf("expected first tail line to be 'line 16', got: %q", resultLines[0])
	}
	if resultLines[4] != "line 20" {
		t.Fatalf("expected last tail line to be 'line 20', got: %q", resultLines[4])
	}
}

func TestTailLog_NoLogs(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewJobLogger(tmpDir, 24*time.Hour)

	_, err := logger.TailLog(999, 10)
	if err == nil {
		t.Fatal("expected error when no logs exist")
	}
}

func TestCleanOldLogs(t *testing.T) {
	tmpDir := t.TempDir()
	// Very short retention: 1 nanosecond
	logger := NewJobLogger(tmpDir, 1*time.Nanosecond)

	// Create a log file
	jobDir := filepath.Join(tmpDir, "job-1")
	os.MkdirAll(jobDir, 0755)
	os.WriteFile(filepath.Join(jobDir, "old.log"), []byte("old content"), 0644)

	// Wait a tiny bit to ensure file is older than retention
	time.Sleep(10 * time.Millisecond)

	err := logger.CleanOldLogs()
	if err != nil {
		t.Fatalf("CleanOldLogs failed: %v", err)
	}

	// Verify old file is removed
	entries, _ := os.ReadDir(jobDir)
	// The job dir itself might be removed too if empty
	if len(entries) > 0 {
		t.Fatalf("expected old log to be cleaned, found %d files", len(entries))
	}
}

func TestCleanOldLogs_KeepsRecent(t *testing.T) {
	tmpDir := t.TempDir()
	// Long retention
	logger := NewJobLogger(tmpDir, 24*time.Hour)

	// Create a log file
	jobDir := filepath.Join(tmpDir, "job-1")
	os.MkdirAll(jobDir, 0755)
	os.WriteFile(filepath.Join(jobDir, "recent.log"), []byte("recent content"), 0644)

	err := logger.CleanOldLogs()
	if err != nil {
		t.Fatalf("CleanOldLogs failed: %v", err)
	}

	// Verify recent file is kept
	content, err := os.ReadFile(filepath.Join(jobDir, "recent.log"))
	if err != nil {
		t.Fatalf("recent log should still exist: %v", err)
	}
	if string(content) != "recent content" {
		t.Fatalf("content mismatch")
	}
}

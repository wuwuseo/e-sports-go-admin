package cron

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LogFileInfo 日志文件信息
type LogFileInfo struct {
	Filename  string    `json:"filename"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// JobLogger 管理任务的独立日志文件
type JobLogger struct {
	baseDir   string        // 日志根目录如 ./data/logs/cron/
	retention time.Duration // 保留时长
}

// NewJobLogger 创建日志管理器
func NewJobLogger(baseDir string, retention time.Duration) *JobLogger {
	return &JobLogger{
		baseDir:   baseDir,
		retention: retention,
	}
}

// CreateLogFile 为一次任务执行创建日志文件
// 路径: {baseDir}/job-{id}/{timestamp}.log
func (l *JobLogger) CreateLogFile(jobID int) (*os.File, string, error) {
	dir := filepath.Join(l.baseDir, fmt.Sprintf("job-%d", jobID))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, "", fmt.Errorf("创建日志目录失败: %w", err)
	}

	filename := time.Now().Format("20060102-150405") + ".log"
	filePath := filepath.Join(dir, filename)

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_SYNC, 0644)
	if err != nil {
		return nil, "", fmt.Errorf("创建日志文件失败: %w", err)
	}

	return f, filename, nil
}

// ListLogs 列出某个任务的所有日志文件（按时间倒序）
func (l *JobLogger) ListLogs(jobID int) ([]LogFileInfo, error) {
	dir := filepath.Join(l.baseDir, fmt.Sprintf("job-%d", jobID))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []LogFileInfo{}, nil
		}
		return nil, fmt.Errorf("读取日志目录失败: %w", err)
	}

	logs := make([]LogFileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		logs = append(logs, LogFileInfo{
			Filename:  entry.Name(),
			Size:      info.Size(),
			CreatedAt: info.ModTime(),
		})
	}

	// 按时间倒序排列
	sort.Slice(logs, func(i, j int) bool {
		return logs[i].CreatedAt.After(logs[j].CreatedAt)
	})

	return logs, nil
}

// ReadLog 读取指定日志文件内容
func (l *JobLogger) ReadLog(jobID int, filename string) (string, error) {
	// 安全校验：防止路径遍历
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") || strings.Contains(filename, "\\") {
		return "", fmt.Errorf("无效的文件名")
	}

	filePath := filepath.Join(l.baseDir, fmt.Sprintf("job-%d", jobID), filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("日志文件不存在")
		}
		return "", fmt.Errorf("读取日志文件失败: %w", err)
	}

	return string(data), nil
}

// TailLog 读取某个任务最新日志文件的最后 N 行
func (l *JobLogger) TailLog(jobID int, lines int) (string, error) {
	logs, err := l.ListLogs(jobID)
	if err != nil {
		return "", err
	}
	if len(logs) == 0 {
		return "", fmt.Errorf("没有日志文件")
	}

	// 获取最新的日志文件
	latestFile := logs[0].Filename
	filePath := filepath.Join(l.baseDir, fmt.Sprintf("job-%d", jobID), latestFile)

	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("打开日志文件失败: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var allLines []string
	for scanner.Scan() {
		allLines = append(allLines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("读取日志文件失败: %w", err)
	}

	if lines <= 0 {
		lines = 100
	}

	start := len(allLines) - lines
	if start < 0 {
		start = 0
	}

	return strings.Join(allLines[start:], "\n"), nil
}

// CleanOldLogs 清理超过保留时长的日志文件
func (l *JobLogger) CleanOldLogs() error {
	entries, err := os.ReadDir(l.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取日志根目录失败: %w", err)
	}

	cutoff := time.Now().Add(-l.retention)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "job-") {
			continue
		}

		jobDir := filepath.Join(l.baseDir, entry.Name())
		files, err := os.ReadDir(jobDir)
		if err != nil {
			continue
		}

		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".log") {
				continue
			}
			info, err := file.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				os.Remove(filepath.Join(jobDir, file.Name()))
			}
		}

		// 如果目录为空则删除
		remaining, _ := os.ReadDir(jobDir)
		if len(remaining) == 0 {
			os.Remove(jobDir)
		}
	}

	return nil
}

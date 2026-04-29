package tasks

import (
	"context"
	"fmt"
	"io"
	"time"
)

func init() {
	Register("cleanup_expired_logs", CleanupExpiredLogs)
	Register("health_check", HealthCheck)
}

// CleanupExpiredLogs 示例任务: 清理过期日志
func CleanupExpiredLogs(ctx context.Context, logger io.Writer) error {
	fmt.Fprintf(logger, "[%s] 开始清理过期日志...\n", time.Now().Format(time.DateTime))
	// 实际清理逻辑由具体业务实现
	fmt.Fprintf(logger, "[%s] 清理完成\n", time.Now().Format(time.DateTime))
	return nil
}

// HealthCheck 示例任务: 健康检查
func HealthCheck(ctx context.Context, logger io.Writer) error {
	fmt.Fprintf(logger, "[%s] 执行健康检查...\n", time.Now().Format(time.DateTime))
	fmt.Fprintf(logger, "[%s] 系统运行正常\n", time.Now().Format(time.DateTime))
	return nil
}

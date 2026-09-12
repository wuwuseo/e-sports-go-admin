// Package logcleanup 注册过期日志清理任务。
//
// 独立成包是因为 internal/tasks 必须保持叶子依赖（cron/executor、queue 等都引用它），
// 而清理逻辑依赖 internal/service；本包通过 init() 向 tasks.Registry 注册，
// 由 main.go blank import 触发。
package logcleanup

import (
	"context"
	"fmt"
	"io"
	"time"

	"go-admin/internal/service"
	"go-admin/internal/tasks"

	cmfConfig "github.com/wuwuseo/cmf/config"
)

func init() {
	tasks.Register("cleanup_expired_logs", CleanupExpiredLogs)
}

// logRetentionDays 读取日志保留天数（config.yml 的 log.retention_days，单位天）。
// 未配置（0）时默认保留 30 天；显式配置为负数表示不自动清理。
func logRetentionDays() int {
	days := cmfConfig.GetInt("log.retention_days")
	if days == 0 {
		return 30
	}
	return days
}

// CleanupExpiredLogs 清理超过保留期的操作日志与登录日志
func CleanupExpiredLogs(ctx context.Context, logger io.Writer) error {
	days := logRetentionDays()
	if days < 0 {
		fmt.Fprintf(logger, "[%s] log.retention_days=%d < 0，跳过日志清理\n", time.Now().Format(time.DateTime), days)
		return nil
	}

	fmt.Fprintf(logger, "[%s] 开始清理 %d 天前的日志...\n", time.Now().Format(time.DateTime), days)
	cutoff := time.Now().AddDate(0, 0, -days)

	deletedOp, err := service.CleanupOperationLogsBefore(ctx, cutoff)
	if err != nil {
		fmt.Fprintf(logger, "[%s] 清理操作日志失败: %v\n", time.Now().Format(time.DateTime), err)
		return err
	}

	deletedLogin, err := service.CleanupLoginLogsBefore(ctx, cutoff)
	if err != nil {
		fmt.Fprintf(logger, "[%s] 清理登录日志失败: %v\n", time.Now().Format(time.DateTime), err)
		return err
	}

	fmt.Fprintf(logger, "[%s] 清理完成：操作日志 %d 条，登录日志 %d 条\n", time.Now().Format(time.DateTime), deletedOp, deletedLogin)
	return nil
}

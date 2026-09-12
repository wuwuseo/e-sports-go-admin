package service

import (
	"context"
	"fmt"
	"time"

	"go-admin/internal/ent/loginlog"
	"go-admin/internal/ent/operationlog"
	"go-admin/internal/global"
)

// CleanupOperationLogsBefore 删除指定时间之前的操作日志，返回删除条数。
// 供日志保留期清理任务调用；DB 未初始化时安全跳过。
func CleanupOperationLogsBefore(ctx context.Context, cutoff time.Time) (int, error) {
	if global.ORM == nil {
		return 0, nil
	}
	deleted, err := global.ORM.OperationLog.Delete().
		Where(operationlog.CreatedAtLT(cutoff)).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("清理过期操作日志失败: %w", err)
	}
	return deleted, nil
}

// CleanupLoginLogsBefore 删除指定时间之前的登录日志，返回删除条数。
// 供日志保留期清理任务调用；DB 未初始化时安全跳过。
func CleanupLoginLogsBefore(ctx context.Context, cutoff time.Time) (int, error) {
	if global.ORM == nil {
		return 0, nil
	}
	deleted, err := global.ORM.LoginLog.Delete().
		Where(loginlog.CreatedAtLT(cutoff)).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("清理过期登录日志失败: %w", err)
	}
	return deleted, nil
}

package tasks

import (
	"context"
	"fmt"
	"io"
	"time"
)

func init() {
	Register("health_check", HealthCheck)
}

// HealthCheck 示例任务: 健康检查
func HealthCheck(ctx context.Context, logger io.Writer) error {
	fmt.Fprintf(logger, "[%s] 执行健康检查...\n", time.Now().Format(time.DateTime))
	fmt.Fprintf(logger, "[%s] 系统运行正常\n", time.Now().Format(time.DateTime))
	return nil
}

package service

import (
	"testing"

	"go-admin/internal/ent/operationlog"
	"go-admin/internal/global"
)

// TestOperationLogService_WriteLog_ParsesUserIDFromUID 覆盖 writeLog 中修复的
// user_id 解析逻辑。历史版本恒写 0 导致按操作者过滤失效；修复后 UserUID 为
// 纯数字时落库为 user_id。回归保护：防止 user_id 退回 0。
func TestOperationLogService_WriteLog_ParsesUserIDFromUID(t *testing.T) {
	setupOperationLogTest(t)
	ctx := t.Context()
	svc := NewOperationLogServiceWithOptions(OperationLogOptions{
		Workers: 0, MonitorInterval: 0,
	})
	defer svc.Shutdown(ctx)

	cases := []struct {
		name    string
		userUID string
		wantID  int
	}{
		{"纯数字UID解析为user_id", "999", 999},
		{"空UID保持0", "", 0},
		{"非数字UID保持0", "abc", 0},
		{"混合字符保持0", "12a", 0},
		{"零值视为0", "0", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// ponytail: 用用例名+UID 作唯一用户名，避免 sub-test 命中同一行
			uniq := "gap-" + tc.name + "-" + tc.userUID
			entry := &LogEntry{
				UserUID:    tc.userUID,
				Username:   uniq,
				Module:     "user",
				Action:     "更新",
				Method:     "POST",
				Path:       "/api/v1/admin/auth/users/1",
				IP:         "127.0.0.1",
				StatusCode: 200,
				DurationMs: 5,
			}
			if err := svc.writeLog(entry); err != nil {
				t.Fatalf("writeLog 失败: %v", err)
			}
			got, err := global.ORM.OperationLog.Query().
				Where(operationlog.Username(uniq)).
				Only(ctx)
			if err != nil {
				t.Fatalf("查询日志失败: %v", err)
			}
			if got.UserID != tc.wantID {
				t.Errorf("UserUID=%q 期望 user_id=%d, 实际=%d",
					tc.userUID, tc.wantID, got.UserID)
			}
		})
	}
}

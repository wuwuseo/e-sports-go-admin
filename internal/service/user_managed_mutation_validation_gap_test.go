package service

import (
	"strings"
	"testing"

	"go-admin/internal/ent/user"
)

// TestUserService_UpdateManagedUser_RejectsWeakPassword 覆盖 UpdateManagedUser
// 的密码强度校验路径。已有测试仅覆盖强密码成功路径；此测试确保弱密码会
// 被 ValidatePasswordStrength 拒绝，且不会修改用户密码字段。
// ponytail: 弱密码场景下写入路径应被截断在 ValidatePasswordStrength 处。
func TestUserService_UpdateManagedUser_RejectsWeakPassword(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	target := createTestUserForUserService(t, client,
		"weak-pw@example.com", "uid-weak", "weakpwuser", user.StatusActive)
	originalPwd := target.Password
	originalSalt := target.PasswordSalt

	weak := stringPointer("12345678") // 数字单一类，不满足 3 类要求
	err := NewUserService().UpdateManagedUser(ctx, target.ID, &UpdateManagedUserRequest{
		Password: weak,
	})
	if err == nil {
		t.Fatal("弱密码应被拒绝，但未返回错误")
	}
	if !strings.Contains(err.Error(), "密码") {
		t.Errorf("错误信息应说明密码问题，实际: %v", err)
	}

	// 校验用户密码字段未被修改
	got, err := client.User.Get(ctx, target.ID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if got.Password != originalPwd || got.PasswordSalt != originalSalt {
		t.Errorf("弱密码被拒后，密码字段不应被修改: pwd changed=%v salt changed=%v",
			got.Password != originalPwd, got.PasswordSalt != originalSalt)
	}
}

// TestUserService_UpdateManagedUser_EmptyAllFieldsIsNoop 覆盖请求中所有字段
// 均为 nil 的幂等场景：应不报错且不修改用户任何字段。
// ponytail: 防御"前端把 nil 当 '不修改'" 的语义回归。
func TestUserService_UpdateManagedUser_EmptyAllFieldsIsNoop(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	target := createTestUserForUserService(t, client,
		"noop@example.com", "uid-noop", "noopuser", user.StatusActive)
	before, err := client.User.Get(ctx, target.ID)
	if err != nil {
		t.Fatalf("查询初始用户失败: %v", err)
	}

	if err := NewUserService().UpdateManagedUser(ctx, target.ID, &UpdateManagedUserRequest{}); err != nil {
		t.Fatalf("全部 nil 字段应不报错，实际: %v", err)
	}
	after, err := client.User.Get(ctx, target.ID)
	if err != nil {
		t.Fatalf("查询更新后用户失败: %v", err)
	}
	if after.Email != before.Email || after.Username != before.Username ||
		after.Nickname != before.Nickname || after.Phone != before.Phone ||
		after.Avatar != before.Avatar || after.Password != before.Password {
		t.Errorf("全 nil 字段不应修改任何值: before=%+v after=%+v", before, after)
	}
}

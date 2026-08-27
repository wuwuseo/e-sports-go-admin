package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent/department"
)

// TestDepartmentTreeInScope_SelfScopeReturnsEmptyTree 验证 DataScopeSelf 返回空树。
// departmentIDsForScope 对 DataScopeSelf 返回 (nil, true)，即 scoped=true 但 allowedIDs 为空，
// tree 方法因此返回空切片。这是安全关键行为：仅自己权限的用户不应看到任何部门。
func TestDepartmentTreeInScope_SelfScopeReturnsEmptyTree(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()

	// 先创建一些部门，确保数据库非空
	root := createDepartmentForTest(t, svc, "总部", 0, 1)
	createDepartmentForTest(t, svc, "研发部", root.ID, 1)

	tree, err := svc.TreeInScope(ctx, &EffectiveDataScope{
		Scope: DataScopeSelf,
	}, &ListDepartmentsRequest{Status: string(department.StatusActive)})
	if err != nil {
		t.Fatalf("TreeInScope 返回错误: %v", err)
	}
	if len(tree) != 0 {
		t.Fatalf("DataScopeSelf 应返回空树，实际返回 %d 个节点", len(tree))
	}
}

// TestConfigGetByGroup_HidesReservedCaptchaKeys 验证 GetByGroup 过滤 captcha.* 保留键。
// List 已有 TestConfigServiceListHidesReservedCaptchaKeys 覆盖，
// 但 GetByGroup 的过滤行为此前未被测试。
func TestConfigGetByGroup_HidesReservedCaptchaKeys(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	// 直接写入一条 captcha.* 保留键配置（绕过 Create 的保留键检查）
	_, err := client.SystemConfig.Create().
		SetName("captcha").
		SetKey("captcha.scene.admin_login").
		SetValue(`{"enabled":true}`).
		SetType("json").
		SetGroup("auth").
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("写入保留键配置失败: %v", err)
	}

	// 写入同组普通配置
	_, err = client.SystemConfig.Create().
		SetName("登录限制").
		SetKey("auth_max_retries").
		SetValue("5").
		SetType("string").
		SetGroup("auth").
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("写入普通配置失败: %v", err)
	}

	svc := NewConfigService(nil)
	items, err := svc.GetByGroup(ctx, "auth")
	if err != nil {
		t.Fatalf("GetByGroup 返回错误: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("GetByGroup 应只返回1条普通配置，实际返回 %d 条", len(items))
	}
	if items[0].Key != "auth_max_retries" {
		t.Fatalf("期望返回 key=%q, 实际 key=%q", "auth_max_retries", items[0].Key)
	}
}

// TestConfigUpdate_ChangingKeyToReservedPrefixRejected 验证将普通配置的 key
// 更新为 captcha.* 前缀时被拒绝。Update 方法中已有的保留键检查
// （item.Key 本身是保留键 → 拒绝）被 TestConfigServiceRejectsReservedCaptchaKeys
// 覆盖，但"把普通 key 改成保留前缀"的路径此前未被测试。
func TestConfigUpdate_ChangingKeyToReservedPrefixRejected(t *testing.T) {
	client := setupConfigTestDB(t)
	ctx := t.Context()

	// 创建一条普通配置
	item, err := client.SystemConfig.Create().
		SetName("普通配置").
		SetKey("normal_key").
		SetValue("val").
		SetType("string").
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建普通配置失败: %v", err)
	}

	svc := NewConfigService(nil)
	reservedKey := "captcha.scene.new"
	err = svc.Update(ctx, item.ID, &UpdateConfigRequest{
		Key: &reservedKey,
	})
	if !errors.Is(err, ErrConfigReserved) {
		t.Fatalf("将 key 改为 captcha.* 前缀应返回 ErrConfigReserved，实际: %v", err)
	}
}

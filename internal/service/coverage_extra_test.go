package service

import (
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupFinal3(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

// ======================== ThirdPartyUser 绑定/解绑额外测试 ========================

func TestThirdPartyUserService_Bind_WithAllFields(t *testing.T) {
	client := setupFinal3(t)
	ctx := withPermissionSystemOperator(t.Context())
	svc := NewThirdPartyUserService()

	u, err := client.User.Create().
		SetEmail("bind-full@test.com").
		SetUID("uid-bind-full").
		SetUsername("bindfull").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	resp, err := svc.BindThirdPartyUser(ctx, &BindThirdPartyRequest{
		UserID:       u.ID,
		Platform:     "wechat",
		Openid:       "full-openid-123",
		PlatformName: "微信",
		Unionid:      "union-456",
		Nickname:     "测试用户",
		AvatarURL:    "https://example.com/avatar.jpg",
		Source:       "miniprogram",
		SourceUserID: "src-789",
		Company:      "测试公司",
	})
	if err != nil {
		t.Fatalf("绑定第三方用户失败: %v", err)
	}
	if resp.Platform != "wechat" {
		t.Errorf("期望平台=wechat, 实际=%s", resp.Platform)
	}
	if resp.Openid != "full-openid-123" {
		t.Errorf("期望Openid=full-openid-123, 实际=%s", resp.Openid)
	}
}

// ======================== ConfigService GetByKey 有效测试 ========================

func TestConfigService_GetByKey_Exists(t *testing.T) {
	client := setupFinal3(t)
	ctx := withPermissionSystemOperator(t.Context())
	svc := NewConfigService(nil)

	_, err := client.SystemConfig.Create().
		SetName("存在配置").
		SetKey("exists_key").
		SetValue("exists_value").
		SetType("string").
		SetGroup("test").
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建配置失败: %v", err)
	}

	resp, err := svc.GetByKey(ctx, "exists_key")
	if err != nil {
		t.Fatalf("获取配置失败: %v", err)
	}
	if resp.Value != "exists_value" {
		t.Errorf("期望Value=exists_value, 实际=%s", resp.Value)
	}
}

// ======================== SystemPermission Delete更多路径 ========================

func TestSystemPermissionService_Delete_LeafNode(t *testing.T) {
	client := setupFinal3(t)
	ctx := withPermissionSystemOperator(t.Context())
	svc := NewSystemPermissionService()

	p, err := client.SystemPermission.Create().
		SetDomain("default").
		SetType("menu").
		SetName("叶子节点").
		SetCode("leaf-node").
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建权限失败: %v", err)
	}

	err = svc.Delete(ctx, "", p.ID)
	if err != nil {
		t.Fatalf("删除叶子节点失败: %v", err)
	}
}

// ======================== DictDataList 额外路径 ========================

func TestDictDataService_List_StatusFilter(t *testing.T) {
	_ = setupFinal3(t)
	ctx := withPermissionSystemOperator(t.Context())
	svc := NewDictDataService()

	statusOn := 1
	statusOff := 0
	svc.Create(ctx, &CreateDictDataRequest{DictType: "status_test", Label: "启用数据", Value: "on", Sort: nil, Status: &statusOn, IsDefault: nil})
	svc.Create(ctx, &CreateDictDataRequest{DictType: "status_test", Label: "禁用数据", Value: "off", Sort: nil, Status: &statusOff, IsDefault: nil})

	resp, err := svc.List(ctx, &ListDictDataRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
		},
		Status: &statusOn,
	})
	if err != nil {
		t.Fatalf("按状态筛选失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望1条启用数据, 实际为 %d", resp.Total)
	}
}

// ======================== 额外的 ListPermissions Type过滤测试 ========================

func TestPermissionService_ListPermissions_TypeFilter(t *testing.T) {
	client := setupFinal3(t)
	ctx := withPermissionSystemOperator(t.Context())
	svc := NewPermissionService(nil)

	_, err := client.Role.Create().
		SetName("type-filter-role").
		SetDisplayName("类型过滤角色").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc.CreatePermission(ctx, "", &CreatePermissionRequest{RoleName: "type-filter-role", Domain: "default", Type: PermissionTypeAPI, Object: "/api/1", Action: "GET"})
	svc.CreatePermission(ctx, "", &CreatePermissionRequest{RoleName: "type-filter-role", Domain: "default", Type: PermissionTypeMenu, Object: "/menu/1", Action: "menu"})

	resp, err := svc.ListPermissions(ctx, "", &ListPermissionsRequest{
		PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
		Type:              PermissionTypeAPI,
	})
	if err != nil {
		t.Fatalf("按类型筛选失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望1个API权限, 实际为 %d", resp.Total)
	}
}

package service

import (
	"context"
	"fmt"
	"sort"

	"go-admin/internal/ent"
	"go-admin/internal/ent/systempermission"
	"go-admin/internal/global"
)

func getEnabledSystemMenuPermissions(ctx context.Context, domain string) ([]*PermissionResponse, error) {
	if global.ORM == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	query := global.ORM.SystemPermission.Query().
		Where(
			systempermission.TypeEQ(string(PermissionTypeMenu)),
			systempermission.StatusEQ(1),
		)

	if domain != "" {
		query = query.Where(systempermission.Or(
			systempermission.DomainEQ(domain),
			systempermission.IsCommonEQ(true),
		))
	}

	permissions, err := query.
		Order(ent.Asc(systempermission.FieldSortOrder), ent.Asc(systempermission.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取系统菜单权限失败: %w", err)
	}

	// 去重: 使用map按Object去重
	seen := make(map[string]struct{})
	data := make([]*PermissionResponse, 0, len(permissions))
	for _, p := range permissions {
		object := systemPermissionObject(p)
		if _, exists := seen[object]; exists {
			continue
		}
		seen[object] = struct{}{}
		data = append(data, &PermissionResponse{
			ID:          p.ID,
			Domain:      p.Domain,
			Type:        PermissionTypeMenu,
			Object:      object,
			Action:      string(PermissionTypeMenu),
			Description: p.Description,
		})
	}

	// 按Object排序确保结果稳定
	sort.Slice(data, func(i, j int) bool {
		return data[i].Object < data[j].Object
	})

	return data, nil
}

func getEnabledSystemButtonPermissions(ctx context.Context, domain string) ([]*PermissionResponse, error) {
	if global.ORM == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	query := global.ORM.SystemPermission.Query().
		Where(
			systempermission.TypeEQ(string(PermissionTypeButton)),
			systempermission.StatusEQ(1),
		)

	if domain != "" {
		query = query.Where(systempermission.Or(
			systempermission.DomainEQ(domain),
			systempermission.IsCommonEQ(true),
		))
	}

	permissions, err := query.
		Order(ent.Asc(systempermission.FieldSortOrder), ent.Asc(systempermission.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取系统按钮权限失败: %w", err)
	}

	// 去重: 使用map按Object去重
	seen := make(map[string]struct{})
	data := make([]*PermissionResponse, 0, len(permissions))
	for _, p := range permissions {
		object := systemPermissionObject(p)
		if _, exists := seen[object]; exists {
			continue
		}
		seen[object] = struct{}{}
		data = append(data, &PermissionResponse{
			ID:          p.ID,
			Domain:      p.Domain,
			Type:        PermissionTypeButton,
			Object:      object,
			Action:      string(PermissionTypeButton),
			Description: p.Description,
		})
	}

	// 按Object排序确保结果稳定
	sort.Slice(data, func(i, j int) bool {
		return data[i].Object < data[j].Object
	})

	return data, nil
}

func systemPermissionObject(p *ent.SystemPermission) string {
	if p.MenuPath != "" {
		return p.MenuPath
	}
	if p.Code != "" {
		return p.Code
	}
	return p.Name
}

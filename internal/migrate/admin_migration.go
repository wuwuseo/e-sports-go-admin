package migrate

import (
	"context"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/userrole"
)

// MigrateUsersToAdmins 将拥有管理员角色（is_system=true）的用户迁移到管理员表
func MigrateUsersToAdmins(ctx context.Context, client *ent.Client) error {
	systemRoles, err := client.Role.Query().
		Where(role.IsSystem(true)).
		All(ctx)
	if err != nil {
		return err
	}

	if len(systemRoles) == 0 {
		return nil
	}

	roleIDs := make([]int, len(systemRoles))
	for i, r := range systemRoles {
		roleIDs[i] = r.ID
	}

	userRoles, err := client.UserRole.Query().
		Where(userrole.RoleIDIn(roleIDs...)).
		WithUser().
		All(ctx)
	if err != nil {
		return err
	}

	processedUserIDs := make(map[int]bool)

	for _, ur := range userRoles {
		if ur.Edges.User == nil {
			continue
		}
		u := ur.Edges.User

		if processedUserIDs[u.ID] {
			continue
		}
		processedUserIDs[u.ID] = true

		exists, err := client.Admin.Query().
			Where(admin.EmailEQ(u.Email)).
			Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			continue
		}

		status := admin.Status(u.Status)
		_, err = client.Admin.Create().
			SetEmail(u.Email).
			SetPassword(u.Password).
			SetPasswordSalt(u.PasswordSalt).
			SetUID(u.UID).
			SetUsername(u.Username).
			SetNickname(u.Nickname).
			SetName(u.Name).
			SetPhone(u.Phone).
			SetStatus(status).
			Save(ctx)
		if err != nil {
			return err
		}
	}

	return nil
}

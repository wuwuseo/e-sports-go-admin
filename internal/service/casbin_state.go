package service

import (
	"errors"
	"fmt"

	"go-admin/internal/global"

	"github.com/casbin/casbin/v3"
)

func replaceCasbinUserRoles(enforcer *casbin.Enforcer, userID, domain string, roleNames []string) error {
	return global.MutateCasbinPolicy(domain, enforcer, func() error {
		var syncErr error
		if _, syncErr = enforcer.DeleteRolesForUserInDomain(userID, domain); syncErr != nil {
			return syncErr
		}
		for _, roleName := range roleNames {
			if _, syncErr = enforcer.AddRoleForUserInDomain(userID, roleName, domain); syncErr != nil {
				return syncErr
			}
		}
		return nil
	})
}

func addCasbinUserRoles(enforcer *casbin.Enforcer, userID, domain string, roleNames []string) error {
	return global.MutateCasbinPolicy(domain, enforcer, func() error {
		for _, roleName := range roleNames {
			if _, err := enforcer.AddRoleForUserInDomain(userID, roleName, domain); err != nil {
				return err
			}
		}
		return nil
	})
}

func removeCasbinUserRoles(enforcer *casbin.Enforcer, userID, domain string) error {
	return global.MutateCasbinPolicy(domain, enforcer, func() error {
		_, err := enforcer.DeleteRolesForUserInDomain(userID, domain)
		return err
	})
}

func replaceCasbinRoleUsers(enforcer *casbin.Enforcer, roleName, domain string, userIDs []string) error {
	return global.MutateCasbinPolicy(domain, enforcer, func() error {
		var syncErr error
		if _, syncErr = enforcer.RemoveFilteredGroupingPolicy(1, roleName, domain); syncErr != nil {
			return syncErr
		}
		for _, userID := range userIDs {
			if _, syncErr = enforcer.AddRoleForUserInDomain(userID, roleName, domain); syncErr != nil {
				return syncErr
			}
		}
		return nil
	})
}

func enforceCasbinWithRoles(enforcer *casbin.Enforcer, userID, domain, object, action string, desiredRoles []string) (bool, error) {
	var (
		allowed bool
		err     error
	)
	global.WithCasbinWrite(func() {
		existingRoles := enforcer.GetRolesForUserInDomain(userID, domain)
		if len(missingPermissionRoleNames(desiredRoles, existingRoles)) > 0 || len(missingPermissionRoleNames(existingRoles, desiredRoles)) > 0 {
			if _, err = enforcer.DeleteRolesForUserInDomain(userID, domain); err != nil {
				return
			}
			for _, roleName := range desiredRoles {
				if _, err = enforcer.AddRoleForUserInDomain(userID, roleName, domain); err != nil {
					return
				}
			}
		}
		allowed, err = enforcer.Enforce(userID, domain, object, action)
	})
	if err != nil {
		return false, fmt.Errorf("同步或检查 Casbin 权限失败: %w", err)
	}
	return allowed, nil
}

func mutateCasbinPolicy(domain string, mutate func(*casbin.Enforcer) (bool, error)) (bool, error) {
	if global.EnforcerManager == nil {
		return false, errors.New("Casbin EnforcerManager 未初始化")
	}
	enforcer, err := global.EnforcerManager.GetEnforcer(domain)
	if err != nil {
		return false, err
	}
	var changed bool
	err = global.MutateCasbinPolicy(domain, enforcer, func() error {
		var mutateErr error
		changed, mutateErr = mutate(enforcer)
		return mutateErr
	})
	return changed, err
}

func addCasbinPolicy(domain string, rule []string) (bool, error) {
	return mutateCasbinPolicy(domain, func(enforcer *casbin.Enforcer) (bool, error) {
		return enforcer.AddPolicy(stringSliceToInterfaces(rule)...)
	})
}

func removeCasbinPolicy(domain string, rule []string) (bool, error) {
	return mutateCasbinPolicy(domain, func(enforcer *casbin.Enforcer) (bool, error) {
		return enforcer.RemovePolicy(stringSliceToInterfaces(rule)...)
	})
}

func updateCasbinPolicy(domain string, oldRule, newRule []string) (bool, error) {
	return mutateCasbinPolicy(domain, func(enforcer *casbin.Enforcer) (bool, error) {
		return enforcer.UpdatePolicy(oldRule, newRule)
	})
}

func addCasbinPolicies(domain string, rules [][]string) (bool, error) {
	return mutateCasbinPolicy(domain, func(enforcer *casbin.Enforcer) (bool, error) {
		return enforcer.AddPolicies(rules)
	})
}

func removeCasbinPolicies(domain string, rules [][]string) (bool, error) {
	return mutateCasbinPolicy(domain, func(enforcer *casbin.Enforcer) (bool, error) {
		return enforcer.RemovePolicies(rules)
	})
}

func stringSliceToInterfaces(values []string) []interface{} {
	result := make([]interface{}, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

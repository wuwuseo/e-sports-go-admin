package ent

import (
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/attachment"
	"go-admin/internal/ent/attachmentgroup"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/cronjob"
	"go-admin/internal/ent/dictdata"
	"go-admin/internal/ent/message"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/operationlog"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/ent/user"
	"go-admin/internal/ent/userrole"
)

// InitTablePrefix 延迟初始化表前缀。
// 必须在配置加载完成后、首次使用 ent 查询或 Schema.Create 之前调用。
// 该函数会更新所有 ent 生成包中的 TablePrefix、Table 变量以及 migrate 中的表名。
func InitTablePrefix(prefix string) {
	// 更新各实体包的 TablePrefix 和 Table
	admin.TablePrefix = prefix
	admin.Table = prefix + "admins"

	adminrole.TablePrefix = prefix
	adminrole.Table = prefix + "admin_roles"

	attachment.TablePrefix = prefix
	attachment.Table = prefix + "attachments"

	attachmentgroup.TablePrefix = prefix
	attachmentgroup.Table = prefix + "attachment_groups"

	casbinrule.TablePrefix = prefix
	casbinrule.Table = prefix + "casbin_rules"

	cronjob.TablePrefix = prefix
	cronjob.Table = prefix + "cron_jobs"

	dictdata.TablePrefix = prefix
	dictdata.Table = prefix + "dict_data"

	message.TablePrefix = prefix
	message.Table = prefix + "messages"

	operationlog.TablePrefix = prefix
	operationlog.Table = prefix + "operation_logs"

	role.TablePrefix = prefix
	role.Table = prefix + "roles"

	systemconfig.TablePrefix = prefix
	systemconfig.Table = prefix + "system_configs"

	user.TablePrefix = prefix
	user.Table = prefix + "users"

	userrole.TablePrefix = prefix
	userrole.Table = prefix + "user_roles"

	// 更新 migrate 包的 TablePrefix 和所有表的 Name 字段
	migrate.TablePrefix = prefix
	migrate.AdminsTable.Name = prefix + "admins"
	migrate.AdminRolesTable.Name = prefix + "admin_roles"
	migrate.AttachmentsTable.Name = prefix + "attachments"
	migrate.AttachmentGroupsTable.Name = prefix + "attachment_groups"
	migrate.CasbinRulesTable.Name = prefix + "casbin_rules"
	migrate.CronJobsTable.Name = prefix + "cron_jobs"
	migrate.DictDataTable.Name = prefix + "dict_data"
	migrate.MessagesTable.Name = prefix + "messages"
	migrate.OperationLogsTable.Name = prefix + "operation_logs"
	migrate.RolesTable.Name = prefix + "roles"
	migrate.SystemConfigsTable.Name = prefix + "system_configs"
	migrate.UsersTable.Name = prefix + "users"
	migrate.UserRolesTable.Name = prefix + "user_roles"
}

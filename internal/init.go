package internal

import (
	"context"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/crypto"
	"github.com/wuwuseo/cmf/orm"
	"go.uber.org/zap"

	"go-admin/internal/casbin/adapter"
	"go-admin/internal/cron"
	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/dictdata"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/ent/systempermission"
	"go-admin/internal/ent/user"
	"go-admin/internal/ent/userrole"
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/wuwuseo/cmf/sse"
)

var appInstance *App

// SetApp 设置 Wire 注入的应用实例
func SetApp(app *App) {
	appInstance = app
}

// GetApp 获取当前应用实例
func GetApp() *App {
	return appInstance
}

func Init(config *config.Config) error {
	// 如果 appInstance 未设置，创建一个最小实例（兼容旧模式）
	if appInstance == nil {
		appInstance = &App{
			Config: config,
		}
	}

	// 初始化数据库：优先使用 Wire 注入的 EntClient
	if appInstance.EntClient != nil {
		global.ORM = appInstance.EntClient
		appInstance.Logger.Info("using Wire-injected EntClient")
	} else {
		if err := initDatabase(config); err != nil {
			return err
		}
	}

	// 初始化 Casbin：优先使用 Wire 注入的 EnforcerManager
	if appInstance.EnforcerManager != nil {
		global.EnforcerManager = appInstance.EnforcerManager
		appInstance.Logger.Info("using Wire-injected EnforcerManager")
	} else {
		if err := initCasbin(config); err != nil {
			return err
		}
	}

	// 初始化定时任务管理器
	cronManager := cron.NewCronManager(global.ORM, "./data/logs/cron", 7*24*time.Hour)
	if err := cronManager.Start(); err != nil {
		return err
	}
	global.CronManager = cronManager

	// 初始化Nonce存储（防止重放攻击）
	crypto.InitNonceStore(5 * time.Minute)
	appInstance.Logger.Info("Nonce store initialized")

	// 启动分片上传会话过期清理协程
	service.StartChunkSessionCleanup()
	appInstance.Logger.Info("chunk session cleanup goroutine started")

	// 初始化密钥管理器（支持密钥轮换）
	// 密钥有效期7天，每24小时轮换一次
	crypto.InitKeyManager(7*24*time.Hour, 24*time.Hour)
	appInstance.Logger.Info("Key manager initialized with 24h rotation cycle")

	// 初始化 SSE Hub 与 Ticket 存储（站内消息实时推送）
	if appInstance.Hub == nil {
		appInstance.Hub = sse.NewHub(sse.WithLogger(appInstance.Logger))
	}
	if appInstance.TicketStore == nil {
		appInstance.TicketStore = sse.NewTicketStore(5 * time.Minute)
	}
	go appInstance.TicketStore.StartCleanup(context.Background())
	appInstance.Logger.Info("SSE Hub 与 TicketStore 初始化完成",
		zap.String("hub_ptr", fmt.Sprintf("%p", appInstance.Hub)),
		zap.String("ticket_store_ptr", fmt.Sprintf("%p", appInstance.TicketStore)),
	)

	// 初始化种子数据
	if err := initSeedData(); err != nil {
		service.StopChunkSessionCleanup()
		appInstance.Logger.Error("failed to initialize seed data", zap.Error(err))
		return err
	}

	// 初始化字典种子数据
	if err := initDictSeedData(); err != nil {
		service.StopChunkSessionCleanup()
		appInstance.Logger.Error("failed to initialize dict seed data", zap.Error(err))
		return err
	}

	// 初始化动态配置种子数据
	if err := initSystemConfigSeedData(); err != nil {
		service.StopChunkSessionCleanup()
		appInstance.Logger.Error("failed to initialize system config seed data", zap.Error(err))
		return err
	}

	return nil
}

// initDatabase 手动初始化数据库（兼容旧模式，当 Wire 未注入时使用）
func initDatabase(config *config.Config) error {
	// 延迟初始化表前缀
	ent.InitTablePrefix(orm.GetTablePrefix())

	driver := orm.GetDatabaseConfig([]string{config.Database.Default}, config).Driver
	appInstance.Logger.Info("using database driver", zap.String("driver", driver))
	db, err := orm.GetSqlDb(driver, orm.GetDatabaseSourceDns(config, config.Database.Default))
	if err != nil {
		return err
	}
	drv := entsql.OpenDB(driver, db)
	client := ent.NewClient(ent.Driver(drv))
	if schemaErr := client.Schema.Create(context.Background()); schemaErr != nil {
		appInstance.Logger.Fatal("failed creating schema resources", zap.Error(schemaErr))
	}
	global.ORM = client
	return nil
}

// initCasbin 手动初始化 Casbin（兼容旧模式，当 Wire 未注入时使用）
func initCasbin(config *config.Config) error {
	appInstance.Logger.Info("initializing Casbin Enforcer Manager...")

	adp, err := adapter.NewAdapterWithClient(global.ORM)
	if err != nil {
		appInstance.Logger.Error("failed to create Casbin Ent adapter", zap.Error(err))
		return err
	}

	global.EnforcerManager = cmfCasbin.InitEnforcerManager(adp, config)

	appInstance.Logger.Info("Casbin Enforcer Manager initialized successfully")
	return nil
}

// initSeedData 初始化超级管理员种子数据
func initSeedData() error {
	ctx := context.Background()
	client := global.ORM

	appInstance.Logger.Info("initializing seed data...")

	// 1. 创建系统角色 "admin"
	adminRole, err := client.Role.Query().
		Where(role.NameEQ("admin")).
		Only(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return fmt.Errorf("failed to query admin role: %w", err)
		}
		// 角色不存在，创建
		adminRole, err = client.Role.Create().
			SetName("admin").
			SetDisplayName("超级管理员").
			SetDescription("系统超级管理员，拥有所有权限").
			SetIsSystem(true).
			SetStatus(role.StatusActive).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to create admin role: %w", err)
		}
		appInstance.Logger.Info("admin role created", zap.Int("id", adminRole.ID))
	} else {
		appInstance.Logger.Warn("admin role already exists, skipping", zap.Int("id", adminRole.ID))
	}

	// 2. 创建管理员用户（使用 First 避免 username 重复导致 NotSingularError）
	adminUser, err := client.User.Query().
		Where(user.UsernameEQ("admin")).
		First(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return fmt.Errorf("failed to query admin user: %w", err)
		}
		// 用户不存在，创建
		hashedPassword, salt, err := crypto.HashPassword("admin")
		if err != nil {
			return fmt.Errorf("failed to hash password: %w", err)
		}
		adminUID := utils.GenerateUID()
		adminUser, err = client.User.Create().
			SetEmail("admin@example.com").
			SetUsername("admin").
			SetPassword(hashedPassword).
			SetPasswordSalt(salt).
			SetNickname("管理员").
			SetUID(adminUID).
			SetStatus(user.StatusActive).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to create admin user: %w", err)
		}
		appInstance.Logger.Info("admin user created", zap.Int("id", adminUser.ID))
	} else {
		// 用户已存在，确保 UID 已设置
		if adminUser.UID == "" {
			adminUID := utils.GenerateUID()
			adminUser, err = adminUser.Update().SetUID(adminUID).Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to set UID for existing admin user: %w", err)
			}
			appInstance.Logger.Info("admin user UID initialized", zap.String("uid", adminUser.UID))
		}
		appInstance.Logger.Warn("admin user already exists, skipping", zap.Int("id", adminUser.ID))
	}

	// 2b. 创建管理员记录到 admins 表（分表后的管理员登录入口）
	adminAccount, err := client.Admin.Query().
		Where(admin.UsernameEQ("admin")).
		First(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return fmt.Errorf("failed to query admin account: %w", err)
		}
		hashedPassword, salt, err := crypto.HashPassword("admin")
		if err != nil {
			return fmt.Errorf("failed to hash admin password: %w", err)
		}
		adminUID := utils.GenerateUID()
		adminAccount, err = client.Admin.Create().
			SetEmail("admin@example.com").
			SetUsername("admin").
			SetPassword(hashedPassword).
			SetPasswordSalt(salt).
			SetNickname("管理员").
			SetUID(adminUID).
			SetStatus(admin.StatusActive).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to create admin account: %w", err)
		}
		appInstance.Logger.Info("admin account created in admins table", zap.Int("id", adminAccount.ID), zap.String("uid", adminUID))
	} else {
		if adminAccount.UID == "" {
			adminUID := utils.GenerateUID()
			adminAccount, err = adminAccount.Update().SetUID(adminUID).Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to set UID for existing admin account: %w", err)
			}
			appInstance.Logger.Info("admin account UID initialized", zap.String("uid", adminAccount.UID))
		}
		appInstance.Logger.Warn("admin account already exists in admins table, skipping", zap.Int("id", adminAccount.ID))
	}

	// 3. 创建 user_role 关联
	userRoleExists, err := client.UserRole.Query().
		Where(
			userrole.UserIDEQ(adminUser.ID),
			userrole.RoleIDEQ(adminRole.ID),
		).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to query user_role: %w", err)
	}
	if !userRoleExists {
		_, err = client.UserRole.Create().
			SetIsPrimary(true).
			SetUserID(adminUser.ID).
			SetRoleID(adminRole.ID).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to create user_role: %w", err)
		}
		appInstance.Logger.Info("admin user_role created",
			zap.Int("user_id", adminUser.ID),
			zap.Int("role_id", adminRole.ID),
		)
	} else {
		appInstance.Logger.Warn("admin user_role already exists, skipping")
	}

	// 3b. 创建 admin_role 关联
	adminRoleExists, err := client.AdminRole.Query().
		Where(
			adminrole.AdminIDEQ(adminAccount.ID),
			adminrole.RoleIDEQ(adminRole.ID),
		).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to query admin_role: %w", err)
	}
	if !adminRoleExists {
		_, err = client.AdminRole.Create().
			SetIsPrimary(true).
			SetAdminID(adminAccount.ID).
			SetRoleID(adminRole.ID).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to create admin_role: %w", err)
		}
		appInstance.Logger.Info("admin_role created",
			zap.Int("admin_id", adminAccount.ID),
			zap.Int("role_id", adminRole.ID),
		)
	} else {
		appInstance.Logger.Warn("admin_role already exists, skipping")
	}

	// 4. 创建 Casbin 权限规则
	enforcer, err := global.EnforcerManager.GetEnforcer("default")
	if err != nil {
		appInstance.Logger.Error("failed to get casbin enforcer", zap.Error(err))
		return fmt.Errorf("failed to get casbin enforcer: %w", err)
	}

	// 4.1 策略规则（p 类型）：admin 角色拥有所有权限
	policyAdded, err := enforcer.AddPolicy("admin", "default", "*", "*")
	if err != nil {
		return fmt.Errorf("failed to create casbin policy rule: %w", err)
	}
	if policyAdded {
		appInstance.Logger.Info("casbin policy rule created for admin role")
	} else {
		appInstance.Logger.Warn("casbin policy rule for admin role already exists, skipping")
	}

	// 4.2 角色分配规则（g 类型）：将用户 UID 分配到 admin 角色
	userUIDStr := adminUser.UID
	groupingAdded, err := enforcer.AddRoleForUserInDomain(userUIDStr, "admin", "default")
	if err != nil {
		return fmt.Errorf("failed to create casbin grouping rule: %w", err)
	}
	if groupingAdded {
		appInstance.Logger.Info("casbin grouping rule created",
			zap.String("uid", userUIDStr),
			zap.String("role", "admin"),
		)
	} else {
		appInstance.Logger.Warn("casbin grouping rule already exists, skipping",
			zap.String("uid", userUIDStr),
		)
	}

	// 4.3 管理员账号角色分配规则（g 类型）：将管理员 UID 分配到 admin 角色
	adminUIDStr := adminAccount.UID
	adminGroupingAdded, err := enforcer.AddRoleForUserInDomain(adminUIDStr, "admin", "default")
	if err != nil {
		return fmt.Errorf("failed to create admin casbin grouping rule: %w", err)
	}
	if adminGroupingAdded {
		appInstance.Logger.Info("admin casbin grouping rule created",
			zap.String("uid", adminUIDStr),
			zap.String("role", "admin"),
		)
	} else {
		appInstance.Logger.Warn("admin casbin grouping rule already exists, skipping",
			zap.String("uid", adminUIDStr),
		)
	}

	// 5. 初始化权限定义表（system_permissions）
	if err := service.InitSystemPermissions(ctx); err != nil {
		appInstance.Logger.Error("failed to initialize system permissions", zap.Error(err))
		return fmt.Errorf("failed to initialize system permissions: %w", err)
	}
	appInstance.Logger.Info("system permissions initialized successfully")

	// 6. 初始化普通管理员角色（normal_admin），并将所有系统权限分配给该角色
	if err := initNormalAdminRole(ctx, client); err != nil {
		appInstance.Logger.Error("failed to initialize normal_admin role", zap.Error(err))
		return fmt.Errorf("failed to initialize normal_admin role: %w", err)
	}

	appInstance.Logger.Info("seed data initialization completed")
	return nil
}

// initDictSeedData 初始化字典种子数据（操作日志模块等）
func initDictSeedData() error {
	ctx := context.Background()
	client := global.ORM

	appInstance.Logger.Info("initializing dict seed data...")

	// 操作日志模块字典
	operLogModules := []struct {
		label string
		value string
		sort  int
	}{
		{"认证", "auth", 1},
		{"用户", "user", 2},
		{"角色", "role", 3},
		{"权限", "permission", 4},
		{"附件", "attachment", 5},
		{"定时任务", "cron", 6},
		{"系统", "system", 7},
	}

	for _, m := range operLogModules {
		exists, err := client.DictData.Query().
			Where(
				dictdata.DictTypeEQ("sys_oper_log_module"),
				dictdata.ValueEQ(m.value),
			).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("failed to query dict data for %s: %w", m.value, err)
		}
		if !exists {
			_, err := client.DictData.Create().
				SetLabel(m.label).
				SetValue(m.value).
				SetDictType("sys_oper_log_module").
				SetSort(m.sort).
				SetStatus(1).
				SetIsDefault(false).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to create dict data for %s: %w", m.value, err)
			}
			appInstance.Logger.Info("dict data created",
				zap.String("dict_type", "sys_oper_log_module"),
				zap.String("label", m.label),
				zap.String("value", m.value),
			)
		} else {
			appInstance.Logger.Warn("dict data already exists, skipping",
				zap.String("dict_type", "sys_oper_log_module"),
				zap.String("value", m.value),
			)
		}
	}

	// 系统配置分组字典
	configGroups := []struct {
		label string
		value string
		sort  int
	}{
		{"基本配置", "basic", 1},
		{"附件配置", "attachment", 2},
	}

	for _, cg := range configGroups {
		exists, err := client.DictData.Query().
			Where(
				dictdata.DictTypeEQ("sys_config_group"),
				dictdata.ValueEQ(cg.value),
			).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("failed to query dict data for config group %s: %w", cg.value, err)
		}
		if !exists {
			_, err := client.DictData.Create().
				SetLabel(cg.label).
				SetValue(cg.value).
				SetDictType("sys_config_group").
				SetSort(cg.sort).
				SetStatus(1).
				SetIsDefault(false).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to create dict data for config group %s: %w", cg.value, err)
			}
			appInstance.Logger.Info("dict data created",
				zap.String("dict_type", "sys_config_group"),
				zap.String("label", cg.label),
				zap.String("value", cg.value),
			)
		} else {
			appInstance.Logger.Warn("dict data already exists, skipping",
				zap.String("dict_type", "sys_config_group"),
				zap.String("value", cg.value),
			)
		}
	}

	appInstance.Logger.Info("dict seed data initialization completed")
	return nil
}

// initSystemConfigSeedData 初始化动态配置种子数据
func initSystemConfigSeedData() error {
	ctx := context.Background()
	client := global.ORM

	appInstance.Logger.Info("initializing system config seed data...")

	// 基本配置项
	basicConfigs := []struct {
		name   string
		key    string
		value  string
		group  string
		typeV  string
		remark string
	}{
		{"站点名称", "site_name", "xxx站", "basic", "string", "网站名称，显示在页面标题和页脚"},
		{"备案信息", "icp_info", "", "basic", "string", "ICP备案号，如：京ICP备XXXXXXXX号"},
	}

	// 附件配置项
	attachmentConfigs := []struct {
		name   string
		key    string
		value  string
		group  string
		typeV  string
		remark string
	}{
		{"分片上传阈值", "attachment.chunk_threshold", "5242880", "attachment", "number", "文件大小超过此阈值（字节）自动切换分片上传，默认5MB"},
		{"分片大小", "attachment.chunk_size", "10485760", "attachment", "number", "分片上传时每片大小（字节），默认10MB"},
	}

	allConfigs := append(basicConfigs, attachmentConfigs...)

	for _, c := range allConfigs {
		exists, err := client.SystemConfig.Query().
			Where(systemconfig.KeyEQ(c.key)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("检查系统配置键 %s 失败: %w", c.key, err)
		}
		if !exists {
			_, err := client.SystemConfig.Create().
				SetName(c.name).
				SetKey(c.key).
				SetValue(c.value).
				SetGroup(c.group).
				SetType(c.typeV).
				SetStatus(1).
				SetRemark(c.remark).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("创建系统配置 %s 失败: %w", c.key, err)
			}
			appInstance.Logger.Info("system config created",
				zap.String("key", c.key),
				zap.String("value", c.value),
			)
		} else {
			appInstance.Logger.Warn("system config already exists, skipping",
				zap.String("key", c.key),
			)
		}
	}

	appInstance.Logger.Info("system config seed data initialization completed")
	return nil
}

// initNormalAdminRole 初始化"普通管理员"角色，并将所有系统权限分配给该角色
func initNormalAdminRole(ctx context.Context, client *ent.Client) error {
	const (
		normalAdminRoleName = "normal_admin"
		domain              = "default"
	)

	// 1. 创建（或获取）普通管理员角色
	normalAdminRole, err := client.Role.Query().
		Where(role.NameEQ(normalAdminRoleName)).
		Only(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return fmt.Errorf("failed to query normal_admin role: %w", err)
		}
		normalAdminRole, err = client.Role.Create().
			SetName(normalAdminRoleName).
			SetDisplayName("普通管理员").
			SetDescription("普通管理员，默认拥有系统所有功能权限，可按需调整").
			SetIsSystem(true).
			SetStatus(role.StatusActive).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to create normal_admin role: %w", err)
		}
		appInstance.Logger.Info("normal_admin role created", zap.Int("id", normalAdminRole.ID))
	} else {
		appInstance.Logger.Warn("normal_admin role already exists, skipping",
			zap.Int("id", normalAdminRole.ID))
	}

	// 2. 读取所有启用的系统权限（菜单、按钮、API）
	perms, err := client.SystemPermission.Query().
		Where(systempermission.StatusEQ(1)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query system permissions: %w", err)
	}

	// 3. 将所有系统权限转换为 Casbin 策略并写入（已存在的跳过）
	created, skipped := 0, 0
	for _, p := range perms {
		object, action, permType := buildCasbinTupleFromSystemPermission(p)
		if object == "" {
			continue
		}

		exists, err := client.CasbinRule.Query().
			Where(
				casbinrule.PtypeEQ("p"),
				casbinrule.V0EQ(normalAdminRoleName),
				casbinrule.V1EQ(domain),
				casbinrule.V2EQ(object),
				casbinrule.V3EQ(action),
			).Exist(ctx)
		if err != nil {
			return fmt.Errorf("failed to query casbin rule for normal_admin: %w", err)
		}
		if exists {
			skipped++
			continue
		}

		_, err = client.CasbinRule.Create().
			SetPtype("p").
			SetV0(normalAdminRoleName).
			SetV1(domain).
			SetV2(object).
			SetV3(action).
			SetV4(permType).
			SetV5(p.Description).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to create casbin rule for normal_admin: %w", err)
		}
		created++
	}
	appInstance.Logger.Info("normal_admin permissions assigned",
		zap.Int("created", created),
		zap.Int("skipped", skipped),
		zap.Int("total", len(perms)),
	)

	// 4. 如果 EnforcerManager 存在，重新加载策略
	if global.EnforcerManager != nil {
		enforcer, err := global.EnforcerManager.GetEnforcer(domain)
		if err != nil {
			appInstance.Logger.Error("failed to get casbin enforcer for normal_admin", zap.Error(err))
			return fmt.Errorf("failed to get casbin enforcer: %w", err)
		}
		if err := enforcer.LoadPolicy(); err != nil {
			appInstance.Logger.Error("failed to reload casbin policy after normal_admin init", zap.Error(err))
			return fmt.Errorf("failed to reload casbin policy: %w", err)
		}
		appInstance.Logger.Info("casbin policy reloaded after normal_admin initialization")
	}

	return nil
}

// buildCasbinTupleFromSystemPermission 根据系统权限定义构造 Casbin 策略的 (object, action, type) 三元组
// 与 service.systemPermissionObject 保持一致：菜单/按钮以 MenuPath/Code/Name 作为对象，API 以 Path 作为对象
func buildCasbinTupleFromSystemPermission(p *ent.SystemPermission) (object, action, permType string) {
	switch p.Type {
	case "menu":
		object = firstNonEmpty(p.MenuPath, p.Code, p.Name)
		action = "menu"
		permType = "menu"
	case "button":
		object = firstNonEmpty(p.Code, p.Name)
		action = "button"
		permType = "button"
	case "api":
		object = p.Path
		action = p.Method
		permType = "api"
	}
	return object, action, permType
}

// firstNonEmpty 返回首个非空字符串
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

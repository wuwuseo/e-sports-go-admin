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
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/dictdata"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/ent/user"
	"go-admin/internal/ent/userrole"
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"
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

	// 2. 创建管理员用户
	adminUser, err := client.User.Query().
		Where(user.UsernameEQ("admin")).
		Only(ctx)
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

	// 4. 创建 Casbin 权限规则
	// 4.1 策略规则（p 类型）：admin 角色拥有所有权限
	policyExists, err := client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ("admin"),
			casbinrule.V1EQ("default"),
			casbinrule.V2EQ("*"),
			casbinrule.V3EQ("*"),
		).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to query casbin policy rule: %w", err)
	}
	if !policyExists {
		_, err = client.CasbinRule.Create().
			SetPtype("p").
			SetV0("admin").
			SetV1("default").
			SetV2("*").
			SetV3("*").
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to create casbin policy rule: %w", err)
		}
		appInstance.Logger.Info("casbin policy rule created for admin role")
	} else {
		appInstance.Logger.Warn("casbin policy rule for admin role already exists, skipping")
	}

	// 4.2 角色分配规则（g 类型）：将用户 UID 分配到 admin 角色
	userUIDStr := adminUser.UID
	groupingExists, err := client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("g"),
			casbinrule.V0EQ(userUIDStr),
			casbinrule.V1EQ("admin"),
			casbinrule.V2EQ("default"),
		).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to query casbin grouping rule: %w", err)
	}
	if !groupingExists {
		_, err = client.CasbinRule.Create().
			SetPtype("g").
			SetV0(userUIDStr).
			SetV1("admin").
			SetV2("default").
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to create casbin grouping rule: %w", err)
		}
		appInstance.Logger.Info("casbin grouping rule created",
			zap.String("uid", userUIDStr),
			zap.String("role", "admin"),
		)
	} else {
		appInstance.Logger.Warn("casbin grouping rule already exists, skipping",
			zap.String("uid", userUIDStr),
		)
	}

	// 如果 EnforcerManager 存在，重新加载策略
	if global.EnforcerManager != nil {
		enforcer, err := global.EnforcerManager.GetEnforcer("default")
		if err != nil {
			appInstance.Logger.Error("failed to get casbin enforcer", zap.Error(err))
			return fmt.Errorf("failed to get casbin enforcer: %w", err)
		}
		if err := enforcer.LoadPolicy(); err != nil {
			appInstance.Logger.Error("failed to reload casbin policy", zap.Error(err))
			return fmt.Errorf("failed to reload casbin policy: %w", err)
		}
		appInstance.Logger.Info("casbin policy reloaded successfully")
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

	for _, c := range basicConfigs {
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

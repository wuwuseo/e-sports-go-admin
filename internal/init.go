package internal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/crypto"
	"github.com/wuwuseo/cmf/orm"
	"go.uber.org/zap"

	"go-admin/internal/apps"
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
	adminqueue "go-admin/internal/queue"
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

	// 安全校验：确保 JWT 密钥不为空或已知弱默认值，必要时生成随机密钥
	ensureAppSecrets(config)

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

	// 初始化任务队列（asynq/Redis）：桥接 internal/tasks 注册表并启动 worker
	if err := adminqueue.Setup(config); err != nil {
		return fmt.Errorf("任务队列初始化失败: %w", err)
	}
	appInstance.Logger.Info("任务队列初始化完成")

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

	// 初始化通知渠道注册表（站内信 + 短信 / 邮件 / 极光推送，按 notify.* 配置启用）
	appInstance.Notifiers = service.BuildNotifiers(appInstance.Hub)
	appInstance.Logger.Info("通知渠道注册完成",
		zap.Strings("channels", appInstance.Notifiers.Names()),
		zap.Strings("disabled_missing_config", appInstance.Notifiers.Disabled()),
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

	dbManager, err := orm.NewDBManager(config)
	if err != nil {
		return err
	}
	// 复用 Wire Provider 的创建逻辑（含自动迁移），连接由 DBManager 统一管理
	client, err := NewEntClient(config, dbManager)
	if err != nil {
		return err
	}
	global.ORM = client
	appInstance.DB = dbManager
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

// knownWeakSecrets 记录已知的弱默认密钥，启动时检测并替换
var knownWeakSecrets = map[string]bool{
	"":                 true,
	"secret":           true,
	"admin-secret-key": true,
	"admin-secret":     true,
}

// generateRandomSecret 生成指定字节长度的随机十六进制密钥
func generateRandomSecret(byteLen int) string {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("生成随机密钥失败: %v", err))
	}
	return hex.EncodeToString(b)
}

// ensureAppSecrets 校验 JWT 密钥安全性：若为空或已知弱默认值则生成随机密钥并告警
func ensureAppSecrets(config *config.Config) {
	if knownWeakSecrets[config.App.Secret] {
		config.App.Secret = generateRandomSecret(32)
		appInstance.Logger.Warn("app.secret 为空或使用已知弱默认值，已自动生成随机密钥",
			zap.String("hint", "请在 config.yml 或环境变量 CMF_APP_SECRET 中设置强密钥；重启后已有 token 将失效"),
		)
	}
	if knownWeakSecrets[config.App.AdminSecret] {
		config.App.AdminSecret = generateRandomSecret(32)
		appInstance.Logger.Warn("app.admin_secret 为空或使用已知弱默认值，已自动生成随机密钥",
			zap.String("hint", "请在 config.yml 或环境变量 CMF_APP_ADMIN_SECRET 中设置强密钥；重启后已有管理员 token 将失效"),
		)
	}
}

// initialAdminPassword 读取初始管理员密码：优先从环境变量 ADMIN_INITIAL_PASSWORD 获取，
// 未设置时生成随机密码并仅在标准输出一次性展示（不写入日志文件），避免使用硬编码弱密码
func initialAdminPassword() string {
	if pwd := os.Getenv("ADMIN_INITIAL_PASSWORD"); pwd != "" {
		return pwd
	}
	pwd := generateRandomSecret(12)
	// 安全基线：日志文件保留 180 天，明文密码一旦落盘即等同超级管理员凭证泄露，
	// 因此初始密码只允许走标准输出，绝不进入 Logger 管道。
	fmt.Fprintf(os.Stdout, "[初始管理员] username=admin password=%s （仅此一次输出到控制台，请立即保存并登录后修改）\n", pwd)
	appInstance.Logger.Warn("ADMIN_INITIAL_PASSWORD 环境变量未设置，已生成随机初始管理员密码并输出到控制台（未写入日志），请登录后立即修改",
		zap.String("username", "admin"),
	)
	return pwd
}

// initSeedData 初始化超级管理员种子数据
func initSeedData() error {
	ctx := context.Background()
	client := global.ORM
	domain := "default"
	if appInstance != nil && appInstance.Config != nil && appInstance.Config.Casbin.DomainsDefault != "" {
		domain = appInstance.Config.Casbin.DomainsDefault
	}

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
		Where(user.Or(user.UsernameEQ("admin"), user.EmailEQ("admin@example.com"))).
		First(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return fmt.Errorf("failed to query admin user: %w", err)
		}
		// 用户不存在，创建
		hashedPassword, salt, err := crypto.HashPassword(initialAdminPassword())
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
		Where(admin.Or(admin.UsernameEQ("admin"), admin.EmailEQ("admin@example.com"))).
		First(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return fmt.Errorf("failed to query admin account: %w", err)
		}
		hashedPassword, salt, err := crypto.HashPassword(initialAdminPassword())
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
	enforcer, err := global.EnforcerManager.GetEnforcer(domain)
	if err != nil {
		appInstance.Logger.Error("failed to get casbin enforcer", zap.Error(err))
		return fmt.Errorf("failed to get casbin enforcer: %w", err)
	}

	// 4.1 策略规则（p 类型）：admin 角色拥有所有权限
	policyAdded, err := enforcer.AddPolicy("admin", domain, "*", "*")
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
	groupingAdded, err := enforcer.AddRoleForUserInDomain(userUIDStr, "admin", domain)
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
	adminGroupingAdded, err := enforcer.AddRoleForUserInDomain(adminUIDStr, "admin", domain)
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
	if err := apps.Provision(ctx, appInstance.Config, client); err != nil {
		return fmt.Errorf("initialize source feature sets: %w", err)
	}

	// 6. 初始化普通管理员角色（normal_admin），并将所有系统权限分配给该角色
	if err := initNormalAdminRole(ctx, client, domain); err != nil {
		appInstance.Logger.Error("failed to initialize normal_admin role", zap.Error(err))
		return fmt.Errorf("failed to initialize normal_admin role: %w", err)
	}

	// 7. 初始化最小权限角色（客服 / 财务 / 考官 / 运营编辑 + 客服/财务只读）
	if err := initMinimalRoles(ctx, client, domain); err != nil {
		appInstance.Logger.Error("failed to initialize minimal privilege roles", zap.Error(err))
		return fmt.Errorf("failed to initialize minimal privilege roles: %w", err)
	}

	appInstance.Logger.Info("seed data initialization completed")
	return nil
}

// minimalRoleSpec 描述一类按显式白名单播种的最小权限角色。
//
// 与 normal_admin 不同，这里绝不使用「全部非敏感权限」式的通配授权：
// permissionCodes 就是该角色的全部授权来源，权限点按 esports 权限 code 精确列出，
// 白名单之外的能力（改价审批、退款审批、结算锁定/发放、收款账户审核、发布审核等）
// 一律不授予。code 不存在时只记日志跳过，绝不报错，以便功能集未启用时也能建角色。
type minimalRoleSpec struct {
	name        string
	displayName string
	description string
	// permissionCodes 是「按权限 code 精确列举」的白名单；
	// 排单 / 报单等权限各自独立成项，因此必须逐个 code 列出，不允许用前缀通配。
	permissionCodes []string
}

// minimalRoleSpecs 返回内置最小权限角色清单（顺序即播种与日志顺序）。
//
// 排单权限独立成项：esports:dispatch:* 与 esports:report:*（报单）
// 是两套互不包含的权限项，报单角色不会自动获得排单能力，反之亦然。
func minimalRoleSpecs() []minimalRoleSpec {
	return []minimalRoleSpec{
		{
			name:        "cs_admin",
			displayName: "客服",
			description: "客服：报单、排单、订单服务、代客收款与售后受理；不含改价/退款审批、凭证核验、结算锁定发放、收款账户审核等资金审批权限",
			permissionCodes: []string{
				// 菜单（报单与排单各自独立成项）
				"esports:report:menu", "esports:order:menu", "esports:dispatch:menu",
				"esports:cs:menu", "esports:aftersale:menu", "esports:player:menu",
				// 报单与客户档案
				"esports:orders:create", "esports:orders:add",
				"esports:customers:list", "esports:customers:create", "esports:customers:add",
				"esports:customers:profile", "esports:customers:profile_update",
				"esports:customers:followups", "esports:customers:followup_create",
				"esports:customers:wecom_intake",
				"esports:age_checks:create", "esports:age_checks:list", "esports:age_checks:review",
				"esports:age_checks:add", "esports:age_checks:approve",
				// 订单读取与履约操作（不含价格与资金核销）
				"esports:orders:list", "esports:orders:get", "esports:orders:operate",
				"esports:orders:collaborators",
				"esports:orders:collaborator_add", "esports:orders:collaborator_remove",
				"esports:orders:cancel", "esports:orders:cancel_preview",
				"esports:orders:reschedule", "esports:orders:reschedule_list",
				"esports:orders:reschedule_propose", "esports:orders:reschedule_withdraw",
				"esports:orders:appointment_confirm",
				"esports:orders:addons", "esports:orders:addon_request", "esports:orders:addon_cancel",
				"esports:orders:close", "esports:orders:close_form", "esports:orders:close_conditions",
				"esports:orders:close_approval",
				"esports:orders:service_pause_admin", "esports:orders:service_resume_admin",
				"esports:orders:service_timing_correction", "esports:orders:service_terminate",
				// 只读的履约辅助：档期超时处置可选方式、服务段计时快照（更正前的当前值与上界）
				"esports:orders:appointment_disposal_options", "esports:orders:service_timing",
				"esports:orders:deposit", "esports:orders:restore",
				// 排单（独立权限项）
				"esports:dispatch:board", "esports:dispatch:candidates", "esports:dispatch:teammate_candidates",
				"esports:dispatch:team_dispatch", "esports:dispatch:seats", "esports:dispatch:assign",
				"esports:dispatch:revoke", "esports:dispatch:claim", "esports:dispatch:release_pool",
				"esports:dispatch:transfer", "esports:dispatch:transfer_decide", "esports:dispatch:transfer_manage",
				"esports:dispatch:transfers", "esports:dispatch:remind", "esports:dispatch:publish_grab",
				"esports:dispatch:priority_list", "esports:dispatch:priority_notices",
				"esports:dispatch:priority_release", "esports:dispatch:operate",
				// 客服工作台与话术
				"esports:cs:workbench", "esports:cs:manage", "esports:cs:risk_customers",
				"esports:scripts:list", "esports:scripts:create", "esports:scripts:update", "esports:scripts:status",
				"esports:wecom:js_config", "esports:wecom:payment_methods",
				// 陪玩档案（不含考核配置与信用复核）
				"esports:players:list", "esports:players:create", "esports:players:update",
				"esports:players:identity_view", "esports:players:identity_review", "esports:players:manage",
				// 售后受理与方案（不含审批/执行/复核裁定）
				"esports:aftersales:list", "esports:aftersales:detail", "esports:aftersales:create",
				"esports:aftersales:accept", "esports:aftersales:plan", "esports:aftersales:withdraw",
				"esports:aftersales:actions_list", "esports:aftersales:operate",
				"esports:aftersales:makeups", "esports:aftersales:makeup_start", "esports:aftersales:makeup_complete",
				"esports:aftersales:review_list", "esports:aftersales:review_request",
				// 代客确认：把「待客户确认」推进到「待审批」；审批（approve/reject）仍归财务，两者不得混用。
				"esports:aftersales:confirm",
				// 目录与报价读取：报单页第一步就要拉 SKU 并预览报价，缺 list 会让整个报单流程 403。
				"esports:products:list", "esports:orders:quote_preview",
				// 代客收款：客服需要替客户走余额核销并提交转账凭证；
				// 这里只授予「发起与提交」，凭证核验、收款账户审核、退款审批等资金审批仍归财务。
				"esports:orders:pay_balance", "esports:payments:order_start",
				"esports:payments:proof_submit", "esports:payments:proofs",
				// 预约档期处置：超时未确认档期的退还/改期由责任客服处理。
				"esports:orders:appointment_dispose",
				// 礼物：代客送礼与礼物单查看；档位配置、争议复核裁定仍归运营/超管。
				"esports:gift:menu",
				"esports:gifts:list", "esports:gifts:orders", "esports:gifts:board",
				"esports:gifts:intents", "esports:gifts:intent_create", "esports:gifts:send",
				"esports:gifts:disputes", "esports:gifts:dispute_create",
				// 会员：查看客户会员权益与专属预约（调级仍归运营）。
				"esports:member:menu", "esports:members:detail", "esports:members:levels",
				"esports:members:priority_bookings", "esports:members:player_availability",
			},
		},
		{
			name:        "finance_admin",
			displayName: "财务",
			description: "财务：资金与结算查看、到账核销、提现审核与打款、财务报表明细；不含改价审批、退款审批、结算锁定、收款账户审核",
			permissionCodes: []string{
				// 菜单
				"esports:wallet:menu", "esports:settlement:menu", "esports:payment:menu",
				"esports:stats:menu", "esports:order:menu",
				// 资金查看（只读）
				"esports:wallets:list", "esports:wallets:transactions", "esports:wallets:ledger_allocations",
				"esports:wallets:refund_quote",
				// 订单读取与渠道成本登记
				"esports:orders:list", "esports:orders:get", "esports:orders:channel-cost",
				// 收款账户与到账核销（不含账户新增/审核/停用）
				"esports:payments:accounts", "esports:payments:channels", "esports:payments:readiness",
				"esports:payments:proofs", "esports:payments:proof_verify", "esports:payments:proof_reject",
				"esports:payments:reversal_list",
				// 结算与提现
				"esports:settlements:list", "esports:settlements:detail", "esports:settlements:entries",
				"esports:settlements:rules", "esports:settlements:cycles", "esports:settlements:close_rules",
				"esports:settlements:cs_entries", "esports:settlements:cs_summary",
				// 客服提成明细的争议冻结 / 解冻（资金争议处置，归财务）
				"esports:settlements:cs_entry_freeze", "esports:settlements:cs_entry_unfreeze",
				"esports:settlements:withdrawals", "esports:settlements:withdrawal_detail",
				"esports:settlements:withdrawal_review", "esports:settlements:withdraw_manage",
				"esports:settlements:withdrawal_pay", "esports:settlements:payout_request",
				"esports:settlements:payout_list", "esports:settlements:payout_paid", "esports:settlements:payout_failed",
				"esports:settlements:external_refund_list", "esports:settlements:external_refund_execute",
				// 充值退款执行批次查询（与外部退款执行同属财务出款口）
				"esports:settlements:recharge_refund_execute", "esports:settlements:recharge_refund_executions",
				// 财务报表
				"esports:stats:view", "esports:stats:finance", "esports:stats:export",
			},
		},
		{
			name:        "examiner_admin",
			displayName: "考官",
			description: "考官：考核任务查看、分配考官、打分与申诉复核、评分查看；不含考核规则配置与资金权限",
			permissionCodes: []string{
				"esports:assessment:menu", "esports:player:menu",
				"esports:assessments:list", "esports:assessments:detail", "esports:assessments:assign",
				"esports:assessments:score", "esports:assessments:appeal_review",
				"esports:assessments:rating", "esports:assessments:reviews_list", "esports:assessments:rules_list",
				"esports:assessments:manage",
				"esports:players:list", "esports:players:identity_view",
			},
		},
		{
			name:        "operation_editor_admin",
			displayName: "运营编辑",
			description: "运营编辑：页面装修草稿、素材与发布/回滚申请；审核发布保留给管理员（不含发布审核与资金权限）",
			permissionCodes: []string{
				"esports:operations:menu", "esports:page:menu",
				"esports:pages:list", "esports:pages:detail", "esports:pages:versions",
				"esports:pages:draft_save", "esports:pages:publish", "esports:pages:rollback",
				"esports:pages:assets", "esports:pages:asset_create", "esports:pages:asset_delete",
				"esports:pages:manage", "esports:pages:publish_submit", "esports:pages:rollback_btn",
			},
		},
		{
			name:        "cs_readonly",
			displayName: "客服只读",
			description: "客服只读：仅可查看页面装修（文档、详情、发布记录），服务端一律拒绝对装修写接口的调用",
			permissionCodes: []string{
				"esports:operations:menu", "esports:page:menu",
				"esports:pages:list", "esports:pages:detail", "esports:pages:versions",
			},
		},
		{
			name:        "finance_readonly",
			displayName: "财务只读",
			description: "财务只读：仅可查看页面装修（文档、详情、发布记录），服务端一律拒绝对装修写接口的调用",
			permissionCodes: []string{
				"esports:operations:menu", "esports:page:menu",
				"esports:pages:list", "esports:pages:detail", "esports:pages:versions",
			},
		},
	}
}

// initMinimalRoles 幂等播种最小权限角色。
//
// 幂等与兼容口径（与 initNormalAdminRole 一致）：
//   - 角色已存在时整角色跳过：既有的手工授权（增删改）一律不被覆盖或回填；
//   - 角色新建时只按显式白名单逐条插入缺失的 casbin 规则，已存在规则不重复插入；
//   - 白名单 code 在系统权限表中不存在（功能集未启用）时只记日志，不报错、不伪造授权；
//   - 完成后重载 casbin 策略，使新授权立即生效（已登录账号需重新加载策略，见升级说明）。
func initMinimalRoles(ctx context.Context, client *ent.Client, domains ...string) error {
	domain := "default"
	if len(domains) > 0 && domains[0] != "" {
		domain = domains[0]
	}

	perms, err := client.SystemPermission.Query().
		Where(systempermission.StatusEQ(1)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query system permissions for minimal roles: %w", err)
	}
	byCode := make(map[string]*ent.SystemPermission, len(perms))
	for _, p := range perms {
		byCode[p.Code] = p
	}

	totalCreated, totalSkipped, totalMissing := 0, 0, 0
	for _, spec := range minimalRoleSpecs() {
		roleRow, err := client.Role.Query().Where(role.NameEQ(spec.name)).Only(ctx)
		roleCreated := false
		if err != nil {
			if !ent.IsNotFound(err) {
				return fmt.Errorf("failed to query role %s: %w", spec.name, err)
			}
			roleRow, err = client.Role.Create().
				SetName(spec.name).
				SetDisplayName(spec.displayName).
				SetDescription(spec.description).
				SetIsSystem(true).
				SetStatus(role.StatusActive).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("failed to create role %s: %w", spec.name, err)
			}
			roleCreated = true
			appInstance.Logger.Info("minimal privilege role created",
				zap.String("role", spec.name), zap.Int("id", roleRow.ID))
		} else {
			appInstance.Logger.Warn("minimal privilege role already exists, preserving assigned permissions",
				zap.String("role", spec.name), zap.Int("id", roleRow.ID))
		}
		if !roleCreated {
			continue
		}

		created, skipped, missing := 0, 0, 0
		for _, code := range spec.permissionCodes {
			p, ok := byCode[code]
			if !ok {
				missing++
				appInstance.Logger.Warn("minimal privilege role permission not declared yet, skipping",
					zap.String("role", spec.name), zap.String("code", code))
				continue
			}
			object, action, permType := buildCasbinTupleFromSystemPermission(p)
			if object == "" {
				missing++
				continue
			}
			exists, err := client.CasbinRule.Query().
				Where(
					casbinrule.PtypeEQ("p"),
					casbinrule.V0EQ(spec.name),
					casbinrule.V1EQ(domain),
					casbinrule.V2EQ(object),
					casbinrule.V3EQ(action),
				).Exist(ctx)
			if err != nil {
				return fmt.Errorf("failed to query casbin rule for %s: %w", spec.name, err)
			}
			if exists {
				skipped++
				continue
			}
			if _, err := client.CasbinRule.Create().
				SetPtype("p").
				SetV0(spec.name).
				SetV1(domain).
				SetV2(object).
				SetV3(action).
				SetV4(permType).
				SetV5(p.Description).
				Save(ctx); err != nil {
				return fmt.Errorf("failed to create casbin rule for %s: %w", spec.name, err)
			}
			created++
		}
		totalCreated += created
		totalSkipped += skipped
		totalMissing += missing
		appInstance.Logger.Info("minimal privilege role permissions assigned",
			zap.String("role", spec.name),
			zap.Int("created", created),
			zap.Int("skipped", skipped),
			zap.Int("missing", missing),
			zap.Int("total_whitelist", len(spec.permissionCodes)),
		)
	}

	appInstance.Logger.Info("minimal privilege roles initialization completed",
		zap.Int("roles", len(minimalRoleSpecs())),
		zap.Int("created", totalCreated),
		zap.Int("skipped", totalSkipped),
		zap.Int("missing", totalMissing),
	)

	if global.EnforcerManager != nil {
		enforcer, err := global.EnforcerManager.GetEnforcer(domain)
		if err != nil {
			appInstance.Logger.Error("failed to get casbin enforcer for minimal roles", zap.Error(err))
			return fmt.Errorf("failed to get casbin enforcer for minimal roles: %w", err)
		}
		if err := enforcer.LoadPolicy(); err != nil {
			appInstance.Logger.Error("failed to reload casbin policy after minimal roles init", zap.Error(err))
			return fmt.Errorf("failed to reload casbin policy after minimal roles init: %w", err)
		}
		appInstance.Logger.Info("casbin policy reloaded after minimal privilege roles initialization")
	}

	return nil
}

// initDictSeedData 初始化字典种子数据（操作日志模块等）
func initDictSeedData() error {
	ctx := context.Background()
	client := global.ORM
	appInstance.Logger.Info("initializing dict seed data...")
	items := []struct {
		dictType string
		label    string
		value    string
		sort     int
	}{
		{"sys_oper_log_module", "认证", "auth", 1},
		{"sys_oper_log_module", "用户", "user", 2},
		{"sys_oper_log_module", "角色", "role", 3},
		{"sys_oper_log_module", "权限", "permission", 4},
		{"sys_oper_log_module", "附件", "attachment", 5},
		{"sys_oper_log_module", "定时任务", "cron", 6},
		{"sys_oper_log_module", "任务队列", "queue", 8},
		{"sys_oper_log_module", "系统", "system", 7},
		{"sys_config_group", "基本配置", "basic", 1},
		{"sys_config_group", "附件配置", "attachment", 2},
		{"slide_group", "首页 banner", "home", 1},
	}
	for _, item := range items {
		subject := item.value
		if item.dictType == "sys_config_group" {
			subject = "config group " + subject
		}
		exists, err := client.DictData.Query().
			Where(
				dictdata.DictTypeEQ(item.dictType),
				dictdata.ValueEQ(item.value),
			).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("failed to query dict data for %s: %w", subject, err)
		}
		if exists {
			appInstance.Logger.Warn("dict data already exists, skipping",
				zap.String("dict_type", item.dictType),
				zap.String("value", item.value),
			)
			continue
		}
		if _, err := client.DictData.Create().
			SetLabel(item.label).
			SetValue(item.value).
			SetDictType(item.dictType).
			SetSort(item.sort).
			SetStatus(1).
			SetIsDefault(false).
			Save(ctx); err != nil {
			return fmt.Errorf("failed to create dict data for %s: %w", subject, err)
		}
		appInstance.Logger.Info("dict data created",
			zap.String("dict_type", item.dictType),
			zap.String("label", item.label),
			zap.String("value", item.value),
		)
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

	captchaConfigs := []struct {
		name   string
		key    string
		value  string
		group  string
		typeV  string
		remark string
	}{
		{
			"用户登录验证码",
			"captcha.scene.user_login",
			`{"enabled":false,"provider":"image_alphanumeric","challenge_ttl":120,"proof_ttl":90}`,
			"captcha",
			"json",
			"用户登录验证码场景，前端完成协议接入后再启用",
		},
		{
			"管理员登录验证码",
			"captcha.scene.admin_login",
			`{"enabled":false,"provider":"image_alphanumeric","challenge_ttl":120,"proof_ttl":90}`,
			"captcha",
			"json",
			"管理员登录验证码场景，前端完成协议接入后再启用",
		},
	}

	// 短信验证码登录配置项（，平台级 user-auth，键不带功能集前缀）
	smsLoginConfigs := []struct {
		name   string
		key    string
		value  string
		group  string
		typeV  string
		remark string
	}{
		{"短信验证码有效期", "login_sms_code_ttl_seconds", "300", "user_auth", "number", "登录验证码有效期（秒），缺省或非法回落 300"},
		{"短信验证码发送间隔", "login_sms_send_interval_seconds", "60", "user_auth", "number", "同一手机号两次发码的最小间隔（秒），缺省或非法回落 60"},
		{"短信验证码最大校验次数", "login_sms_max_verify_attempts", "5", "user_auth", "number", "同一验证码最多允许校验失败的次数，用尽即作废，缺省或非法回落 5"},
		{"短信验证码手机号日上限", "login_sms_phone_daily_limit", "10", "user_auth", "number", "同一手机号每日发码次数上限（按 UTC+8 自然日计），0 表示不限，缺省或非法回落 10"},
		{"短信验证码 IP 日上限", "login_sms_ip_daily_limit", "20", "user_auth", "number", "同一 IP 每日发码次数上限（按 UTC+8 自然日计），0 表示不限，缺省或非法回落 20"},
	}

	allConfigs := append(basicConfigs, attachmentConfigs...)
	allConfigs = append(allConfigs, captchaConfigs...)
	allConfigs = append(allConfigs, smsLoginConfigs...)

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

// initNormalAdminRole seeds the built-in normal_admin role without granting
// plugin management by default. Existing role assignments are preserved because
// missing rules may have been deliberately removed by a super admin.
func initNormalAdminRole(ctx context.Context, client *ent.Client, domains ...string) error {
	const normalAdminRoleName = "normal_admin"
	domain := "default"
	if len(domains) > 0 && domains[0] != "" {
		domain = domains[0]
	}
	roleCreated := false

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
			SetDescription("普通管理员，默认拥有基础管理权限，可按需调整").
			SetIsSystem(true).
			SetStatus(role.StatusActive).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("failed to create normal_admin role: %w", err)
		}
		roleCreated = true
		appInstance.Logger.Info("normal_admin role created", zap.Int("id", normalAdminRole.ID))
	} else {
		appInstance.Logger.Warn("normal_admin role already exists, preserving assigned permissions",
			zap.Int("id", normalAdminRole.ID))
	}

	// 2. 读取所有启用的系统权限（菜单、按钮、API）
	perms, err := client.SystemPermission.Query().
		Where(systempermission.StatusEQ(1)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("failed to query system permissions: %w", err)
	}

	removedSeededExcluded, err := cleanupNormalAdminSeededExcludedPermissions(ctx, client, normalAdminRoleName, domain, perms)
	if err != nil {
		return err
	}

	created, skipped, excluded := 0, 0, 0
	if roleCreated {
		for _, p := range perms {
			if !shouldSeedNormalAdminPermission(p) {
				excluded++
				continue
			}

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
	}
	appInstance.Logger.Info("normal_admin permissions assigned",
		zap.Bool("role_created", roleCreated),
		zap.Int("created", created),
		zap.Int("skipped", skipped),
		zap.Int("excluded", excluded),
		zap.Int("removed_seeded_excluded", removedSeededExcluded),
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

func cleanupNormalAdminSeededExcludedPermissions(ctx context.Context, client *ent.Client, roleName, domain string, perms []*ent.SystemPermission) (int, error) {
	removed := 0
	for _, p := range perms {
		if shouldSeedNormalAdminPermission(p) || p.Description == "" {
			continue
		}

		object, action, _ := buildCasbinTupleFromSystemPermission(p)
		if object == "" {
			continue
		}

		deleted, err := client.CasbinRule.Delete().
			Where(
				casbinrule.PtypeEQ("p"),
				casbinrule.V0EQ(roleName),
				casbinrule.V1EQ(domain),
				casbinrule.V2EQ(object),
				casbinrule.V3EQ(action),
				casbinrule.V5EQ(p.Description),
			).Exec(ctx)
		if err != nil {
			return removed, fmt.Errorf("failed to clean seeded normal_admin permission %s %s: %w", object, action, err)
		}
		removed += deleted
	}
	return removed, nil
}

func shouldSeedNormalAdminPermission(p *ent.SystemPermission) bool {
	if isPluginManagementSystemPermission(p) {
		return false
	}
	// 高风险权限 API（系统权限定义 CRUD、角色权限分配、Casbin 规则 CRUD、OAuth 配置）
	// 不授予 normal_admin，仅超级管理员可持有，防止普通管理员越权修改权限定义
	if p.Type == "api" && service.IsSensitivePermissionObject(p.Path) {
		return false
	}
	return true
}

func isPluginManagementSystemPermission(p *ent.SystemPermission) bool {
	code := strings.TrimSpace(p.Code)
	if code == "plugin" || code == "system:plugin" || strings.HasPrefix(code, "plugin:") {
		return true
	}
	if strings.TrimSpace(p.MenuPath) == "/system/plugin" {
		return true
	}
	if strings.HasPrefix(strings.TrimSpace(p.Path), "/api/v1/admin/auth/plugins") {
		return true
	}
	return false
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

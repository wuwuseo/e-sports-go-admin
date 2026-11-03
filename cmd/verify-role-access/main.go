// verify-role-access 角色权限端到端验证工具。
//
// 对每个启用角色准备一组验证账号（默认 2 个，admin 复用超管本身），然后逐个登录，
// 按「菜单路由 → API 权限 → 数据权限」三层断言：
//
//  1. 菜单路由：/admin/auth/user/menu-permissions 返回的全部菜单，必须与角色在
//     Casbin 里的 menu 授权及父级闭包一致（既不缺菜单，也不多越权菜单）。
//  2. API 权限：每个角色抽一条「应有权限」的 GET 接口验证放行，再抽一条「不应有权限」的
//     GET 接口验证被 403 拒绝；后者是越权检测的关键。
//  3. 数据权限：管理员列表按角色 data_scope 收敛（self 只能看到自己）；拥有客户档案权限的
//     角色额外验证同角色两个账号之间的 owner_admin_id 隔离——A 看不到、也读不了 B 的客户。
//
// 账号准备是幂等的：同名账号存在就重置密码并重新绑定角色，角色绑定直接写 admin_roles，
// 运行中的后端会在下一次请求里按 DB 真值自动修复 Casbin 的 g 规则。
//
// 用法：
//
//	设置 VERIFY_ADMIN_PASSWORD、VERIFY_ACCOUNT_PASSWORD 后运行：
//	go run ./cmd/verify-role-access -report data/role-access.json
//	go run ./cmd/verify-role-access -only cs_admin,finance_admin
//
// 默认会写库（创建/更新验证账号与角色绑定）；验证完成后输出终端报告，
// 任一项失败时进程退出码为 1，可直接接入 CI 或发布前检查。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/go-sql-driver/mysql"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/orm"

	"go-admin/internal/ent"
	_ "go-admin/internal/ent/runtime"
)

func main() {
	cfg, err := parseFlags()
	if err != nil {
		fmt.Fprintln(os.Stderr, "参数错误:", err)
		os.Exit(2)
	}

	appConfig := config.NewConfig()
	dbManager, err := orm.NewDBManager(appConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "连接数据库失败:", err)
		os.Exit(1)
	}
	defer dbManager.Close()
	db, err := dbManager.DB()
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取数据库连接失败:", err)
		os.Exit(1)
	}
	// 验证工具只连接现有表，不运行应用初始化、迁移或种子任务。
	ent.InitTablePrefix(orm.GetTablePrefix())
	client := ent.NewClient(ent.Driver(entsql.OpenDB(orm.GetDatabaseConfig(nil, appConfig).Driver, db)))
	defer client.Close()

	ctx := context.Background()
	runner := &runner{cfg: cfg, db: client, domain: cfg.domain}

	if err := runner.run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "验证中断:", err)
		os.Exit(1)
	}
	if !runner.allPassed() {
		os.Exit(1)
	}
}

// options 命令行参数。
type options struct {
	baseURL       string
	adminUsername string
	adminPassword string
	password      string
	accounts      int
	only          []string
	keepData      bool
	cleanup       bool
	timeout       time.Duration
	domain        string
	reportPath    string
}

func parseFlags() (*options, error) {
	opt := &options{}
	flag.StringVar(&opt.baseURL, "base-url", "http://127.0.0.1:9501", "后端地址（后端需已启动）")
	flag.StringVar(&opt.adminUsername, "admin-username", "admin", "超级管理员账号（用于登录与 admin 角色验证）")
	flag.StringVar(&opt.adminPassword, "admin-password", "", "超级管理员密码（必填，也可用环境变量 VERIFY_ADMIN_PASSWORD）")
	flag.StringVar(&opt.password, "password", "", "验证账号统一密码（必填，也可用环境变量 VERIFY_ACCOUNT_PASSWORD）")
	flag.IntVar(&opt.accounts, "accounts", 2, "每个角色创建的账号数（>=2 才能验证同角色数据隔离）")
	var only string
	flag.StringVar(&only, "only", "", "只验证指定角色，逗号分隔；留空验证全部启用角色")
	flag.BoolVar(&opt.keepData, "keep-data", true, "保留验证过程中创建的客户档案（默认保留，便于复查）")
	flag.BoolVar(&opt.cleanup, "cleanup", false, "验证结束后删除验证账号（客户档案策略见 -keep-data）")
	flag.DurationVar(&opt.timeout, "timeout", 20*time.Second, "单次 HTTP 请求超时")
	flag.StringVar(&opt.domain, "domain", "default", "Casbin 域")
	flag.StringVar(&opt.reportPath, "report", "", "将验证结果保存为 JSON（不包含密码或令牌）")
	flag.Parse()

	if opt.adminPassword == "" {
		opt.adminPassword = os.Getenv("VERIFY_ADMIN_PASSWORD")
	}
	if opt.adminPassword == "" {
		return nil, fmt.Errorf("必须通过 -admin-password 或环境变量 VERIFY_ADMIN_PASSWORD 提供超级管理员密码")
	}
	if opt.password == "" {
		opt.password = os.Getenv("VERIFY_ACCOUNT_PASSWORD")
	}
	if opt.password == "" {
		return nil, fmt.Errorf("必须通过 -password 或环境变量 VERIFY_ACCOUNT_PASSWORD 提供验证账号密码")
	}
	if opt.accounts < 1 {
		return nil, fmt.Errorf("-accounts 必须 >= 1")
	}
	opt.baseURL = strings.TrimRight(strings.TrimSpace(opt.baseURL), "/")
	if only != "" {
		for _, p := range strings.Split(only, ",") {
			if v := strings.TrimSpace(p); v != "" {
				opt.only = append(opt.only, v)
			}
		}
	}
	return opt, nil
}

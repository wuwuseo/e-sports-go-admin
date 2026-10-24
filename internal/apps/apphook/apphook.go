// Package apphook 定义功能集在宿主侧的扩展钩子。cmf 的 App 声明（cmfapp.Register）
// 只覆盖路由 / 配置 / 权限三类；字典种子、退役权限、Casbin 规则迁移、定时任务与
// 业务基准数据等「宿主侧播种」能力由本包承接：各功能集在自己的包内实现 Extension
// 并在 init 中 Register，宿主 internal/apps 的 Provision 按功能集编码统一执行，
// 宿主代码本身保持功能集无关。
package apphook

import (
	"context"

	cmfapp "github.com/wuwuseo/cmf/app"

	"go-admin/internal/ent"
)

// DictSeed 功能集级字典种子：随功能集启停（仅 Enabled 的功能集会种入），
// 幂等（dict_type + value 查重）。
type DictSeed struct {
	DictType string
	Label    string
	Value    string
	Sort     int
}

// PolicyMigration 单条 Casbin p 规则的迁移：旧路径/权限码平移到新值，保留角色授权。
type PolicyMigration struct {
	From   string
	To     string
	Method string
	Kind   string
}

// PolicyRule 需要直接删除的 Casbin api 规则：权限模型收敛后无新路径可平移的历史残留。
// Method 为空表示任意方法。
type PolicyRule struct {
	Path   string
	Method string
}

// MenuPathMigration 菜单路径变更后的角色授权平移（角色授权以菜单路径为 Casbin object，
// 路径改了但角色规则没跟着走，非超管角色会丢掉该菜单）。
type MenuPathMigration struct {
	From string
	To   string
}

// CronSeed 描述功能集自带的一个定时任务种子：新库首次 Provision 时按 name 幂等建
// CronJob 行并注册进运行中的调度器，避免「任务函数已注册、却从未被调度」。
type CronSeed struct {
	// Name 必须与功能集在 tasks.Register 注册的任务名完全一致（CronJob.command 即任务名）。
	Name string
	// Expression 为 6 段 cron 表达式（含秒），与宿主 internal/cron 的 WithSeconds() 解析器一致。
	Expression string
	// Description 供管理端定时任务列表展示。
	Description string
}

// Extension 功能集的宿主侧扩展钩子。功能集实现全部方法，只涉及部分能力时
// 返回零值即可（如无字典种子返回 nil、无特殊配置播种返回 handled=false）。
type Extension interface {
	// DictSeeds 返回功能集的字典种子。
	DictSeeds() []DictSeed
	// SeedConfig 处理需要特殊播种逻辑的配置项（如随机生成的密钥）；
	// 返回 handled=true 表示已处理，宿主跳过通用的「缺失才创建」逻辑。
	SeedConfig(ctx context.Context, client *ent.Client, key string, item cmfapp.ConfigItem) (handled bool, err error)
	// RetiredPermissions 返回已合并/下线的权限码，宿主删除对应权限行。
	RetiredPermissions() []string
	// PolicyMigrations 返回 Casbin p 规则迁移（旧路径 → 新路径，保留角色授权）。
	PolicyMigrations() []PolicyMigration
	// RetiredPolicies 返回需要直接删除的 Casbin api 规则。
	RetiredPolicies() []PolicyRule
	// MenuPathMigrations 返回菜单路径变更后的角色授权平移。
	MenuPathMigrations() []MenuPathMigration
	// CronSeeds 返回功能集自带的定时任务种子。
	CronSeeds() []CronSeed
	// SeedBaseline 播种「新库必需」的业务基准数据（幂等，已有数据一律不改动）。
	SeedBaseline(ctx context.Context, client *ent.Client) error
}

var registry = map[string]Extension{}

// Register 注册功能集的宿主侧扩展，code 必须与 cmfapp.Register 的一致。
// 在功能集包的 init 中调用（与 cmfapp.Register 同处），进程启动后即可查得。
func Register(code string, ext Extension) {
	registry[code] = ext
}

// Lookup 返回功能集的扩展钩子；未注册返回 nil。
func Lookup(code string) Extension {
	return registry[code]
}

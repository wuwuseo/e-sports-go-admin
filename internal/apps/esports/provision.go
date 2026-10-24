// 本文件是 esports 功能集的宿主侧扩展：字典种子、特殊配置播种、权限/Casbin 迁移、
// 定时任务与业务基准数据都随功能集声明（apphook.Extension），宿主 internal/apps
// 只按功能集编码统一执行，自身不再包含本功能集的任何特判。
package esports

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	cmfapp "github.com/wuwuseo/cmf/app"

	"go-admin/internal/apps/apphook"
	"go-admin/internal/ent"
	"go-admin/internal/ent/systemconfig"
)

// esportsExtension 本功能集在宿主侧的扩展钩子。
type esportsExtension struct{}

func (esportsExtension) DictSeeds() []apphook.DictSeed {
	// 系统配置页按 sys_config_group 字典渲染分组 tab；esports 组的配置项
	// （如 app.esports.payment_gateways 在线收款渠道）在该 tab 下编辑。
	return []apphook.DictSeed{
		{DictType: "sys_config_group", Label: "业务配置", Value: "esports", Sort: 3},
	}
}

// SeedConfig 只特判页面预览签名密钥：该密钥不能存源码固定值，首次播种随机生成
// （其余配置项返回 handled=false，交给宿主通用的「缺失才创建」逻辑）。
func (esportsExtension) SeedConfig(ctx context.Context, client *ent.Client, key string, item cmfapp.ConfigItem) (bool, error) {
	if item.Key != "page_preview_secret" {
		return false, nil
	}
	return true, seedPagePreviewSecret(ctx, client, key, item)
}

// seedPagePreviewSecret gives each installation its own persistent signing key.
// Older installations may already have an empty seed row; fill only that value.
func seedPagePreviewSecret(ctx context.Context, client *ent.Client, key string, item cmfapp.ConfigItem) error {
	row, err := client.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("query config: %w", err)
	}
	if err == nil && strings.TrimSpace(row.Value) != "" {
		return nil
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Errorf("generate random signing key: %w", err)
	}
	secret := hex.EncodeToString(bytes)
	if row != nil {
		// Preserve a value set by an administrator after the read.
		if _, err := client.SystemConfig.Update().
			Where(systemconfig.KeyEQ(key), systemconfig.ValueEQ(row.Value)).
			SetValue(secret).Save(ctx); err != nil {
			return fmt.Errorf("update empty config: %w", err)
		}
		return nil
	}
	if _, err := client.SystemConfig.Create().SetKey(key).SetName(item.Name).
		SetValue(secret).SetType(item.Type).SetGroup(item.Group).
		SetRemark(item.Remark).SetStatus(1).Save(ctx); err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	return nil
}

// RetiredPermissions 记录本功能集已合并/下线的权限码。种子权限只做创建与更新、
// 不删除历史行，不显式清理会在超管菜单树里留下孤儿菜单。
func (esportsExtension) RetiredPermissions() []string {
	return []string{
		"esports:pages:home_menu", "esports:products:detail", "esports:orders:seat_roles",
		"esports:spec_groups:list", "esports:spec_groups:create", "esports:spec_groups:update",
		"esports:skus:list", "esports:skus:create", "esports:skus:update",
		"esports:skus:suspension_request", "esports:skus:suspension_list",
		"esports:skus:suspension_detail", "esports:skus:suspension_decision",
		"esports:skus:suspension_cancel", "esports:skus:suspension_disposal",
		"esports:skus:add", "esports:skus:edit",
		// 技能等级改独立菜单后清理的历史残留：商品模块下旧「配置技能等级」按钮
		// （商品门槛选项走公开读 /skill-levels，无需权限）。
		"esports:products:skill_levels", "esports:products:skill_levels_manage",
	}
}

// PolicyMigrations 旧商品接口改名时保留角色授权（接口路径与权限码同步更名）。
func (esportsExtension) PolicyMigrations() []apphook.PolicyMigration {
	return []apphook.PolicyMigration{
		{From: "/api/v1/esports/admin/skus", To: "/api/v1/esports/admin/products", Method: "GET", Kind: "api"},
		{From: "/api/v1/esports/admin/skus", To: "/api/v1/esports/admin/products", Method: "POST", Kind: "api"},
		{From: "/api/v1/esports/admin/skus/{id}", To: "/api/v1/esports/admin/products/{id}", Method: "PUT", Kind: "api"},
		{From: "/api/v1/esports/admin/skus/{id}/suspensions", To: "/api/v1/esports/admin/products/{id}/suspensions", Method: "POST", Kind: "api"},
		{From: "/api/v1/esports/admin/sku-suspensions", To: "/api/v1/esports/admin/product-suspensions", Method: "GET", Kind: "api"},
		{From: "/api/v1/esports/admin/sku-suspensions/{id}", To: "/api/v1/esports/admin/product-suspensions/{id}", Method: "GET", Kind: "api"},
		{From: "/api/v1/esports/admin/sku-suspensions/{id}/decision", To: "/api/v1/esports/admin/product-suspensions/{id}/decision", Method: "POST", Kind: "api"},
		{From: "/api/v1/esports/admin/sku-suspensions/{id}/cancel", To: "/api/v1/esports/admin/product-suspensions/{id}/cancel", Method: "POST", Kind: "api"},
		{From: "/api/v1/esports/admin/sku-suspensions/{id}/orders/{orderId}/disposal", To: "/api/v1/esports/admin/product-suspensions/{id}/orders/{orderId}/disposal", Method: "POST", Kind: "api"},
		{From: "esports:skus:add", To: "esports:products:add", Method: "button", Kind: "button"},
		{From: "esports:skus:edit", To: "esports:products:edit", Method: "button", Kind: "button"},
	}
}

// RetiredPolicies 权限模型收敛后不再存在的历史端点规则，直接删除（无新路径可平移）。
func (esportsExtension) RetiredPolicies() []apphook.PolicyRule {
	return []apphook.PolicyRule{
		{Path: "/api/v1/esports/admin/spec-groups"},
		{Path: "/api/v1/esports/admin/spec-groups/{id}"},
		{Path: "/api/v1/esports/admin/products/{id}", Method: "GET"},
		{Path: "/api/v1/esports/admin/orders/{id}/seat-roles"},
	}
}

// MenuPathMigrations 菜单路径变更后的角色授权平移：角色授权以菜单路径为 Casbin
// object，路径改了但角色规则没跟着走，非超管角色会丢掉该菜单。
func (esportsExtension) MenuPathMigrations() []apphook.MenuPathMigration {
	return []apphook.MenuPathMigration{
		{From: "/esports/pages", To: "/esports/pages/home"},
	}
}

// CronSeeds 返回本功能集的定时任务种子（清单见 cron_seeds.go）。
func (esportsExtension) CronSeeds() []apphook.CronSeed { return CronSeeds() }

// SeedBaseline 为「新库必需」的基准数据播种（幂等，已有数据一律不改动）。
// 与定时任务种子的分工：前者保证任务会被调度，本方法保证**业务本身可用**。
// 以会员等级为例：全新库里等级表为空，客户将永远无法定级，折扣、优先派单、
// 专属客服等按等级生效的权益会**全部静默失效**（审计 D14），因此功能启用时必须播种五档默认等级。
func (esportsExtension) SeedBaseline(ctx context.Context, client *ent.Client) error {
	// 技能等级已改专用表驱动：把旧的系统配置 app.esports.skill_levels 一次性搬入
	// EsportsSkillLevel 并删除配置记录（表已有数据时只删记录，幂等）。
	if err := MigrateSkillLevelsFromConfig(ctx, client); err != nil {
		return fmt.Errorf("migrate skill levels: %w", err)
	}
	if _, err := SeedMemberLevels(ctx, client); err != nil {
		return fmt.Errorf("seed member levels: %w", err)
	}
	if _, err := SeedRechargeCampaign(ctx, client); err != nil {
		return fmt.Errorf("seed recharge campaign: %w", err)
	}
	return nil
}

func init() {
	apphook.Register("esports", esportsExtension{})
}

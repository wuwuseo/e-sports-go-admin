package esports

import (
	"context"
	"fmt"

	cmfapp "github.com/wuwuseo/cmf/app"

	"go-admin/internal/ent"
	"go-admin/internal/ent/systemconfig"
)

// 功能集配置声明自检（§18-02 的另一半）。
//
// apps.Provision 只为「已在功能集 Config 中声明」的键创建 SystemConfig 行，
// 因此声明是「后台可配置」的前提：读了却没声明的键在后台根本不存在，运维只能手写数据库。
// 本检查核对「声明 ↔ 落库」是否一致，缺失通常意味着 Provision 未执行、库被重置或行被手工删除。
//
// 口径：不阻断交易（交易主链路只依赖 payment_deadline_minutes，另有专门检查），故 Blocking=false。

// esportsDeclaredConfigKeys 返回本功能集声明过的全部系统配置键（含 app.esports. 前缀）。
func esportsDeclaredConfigKeys() []string {
	for _, app := range cmfapp.Registered() {
		if app.Code != "esports" {
			continue
		}
		keys := make([]string, 0, len(app.Config))
		for _, item := range app.Config {
			keys = append(keys, "app."+app.Code+"."+item.Key)
		}
		return keys
	}
	return nil
}

// declaredConfigCheck 生成「功能集配置项」自检项；未注册功能集时返回 nil（不产出检查项）。
func declaredConfigCheck(ctx context.Context, db *ent.Client) *readinessCheck {
	declared := esportsDeclaredConfigKeys()
	if len(declared) == 0 {
		return nil
	}
	// SystemConfig.key 唯一，故命中行数即「已落库的声明键数」。
	present := 0
	if rows, err := db.SystemConfig.Query().Where(systemconfig.KeyIn(declared...)).All(ctx); err == nil {
		present = len(rows)
	}
	check := &readinessCheck{
		Key: "config_declared", Name: "功能集配置项", Status: readinessReady,
		Detail: fmt.Sprintf("已声明 %d 个配置键并全部落库", len(declared)),
	}
	if present != len(declared) {
		check.Status = readinessMissing
		check.Detail = fmt.Sprintf("已声明 %d 个配置键，仅 %d 个落库，后台无法配置缺失项", len(declared), present)
		check.Action = "重启服务以执行功能集 Provision 补齐系统配置行，或确认系统配置行未被手工删除"
	}
	return check
}

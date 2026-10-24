package esports

import (
	"testing"

	"go-admin/internal/ent/systemconfig"
)

// ：功能集配置声明的另一半——自检必须暴露「声明了但没落库」的配置键。
// apps.Provision 只在键被声明时建 SystemConfig 行；行缺失（Provision 未执行、库被重置、
// 行被手工删除）时后台无法配置该键，运维只能手写数据库，因此必须在上线自检里可见。
func TestReadinessReportsDeclaredConfigRowsMissing(t *testing.T) {
	db := openAfterSaleDB(t, "esports_readiness_declared_config")
	defer db.Close()
	ctx := t.Context()

	declared := esportsDeclaredConfigKeys()
	if len(declared) == 0 {
		t.Fatal("esports 功能集未声明任何配置键")
	}

	// 声明键全部落库 → ready。
	for _, key := range declared {
		db.SystemConfig.Create().SetKey(key).SetName(key).SetValue("0").
			SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	}
	report := systemReadiness(ctx, db, func(string) string { return "" })
	check, ok := readinessOf(report, "config_declared")
	if !ok || check.Status != readinessReady {
		t.Fatalf("声明键全部落库时应 ready：%+v", check)
	}

	// 删掉其中一行 → 必须报缺失，且不阻断上线（交易主链路只依赖支付时限，另有专门检查）。
	db.SystemConfig.Delete().Where(systemconfig.KeyEQ(declared[0])).ExecX(ctx)
	report = systemReadiness(ctx, db, func(string) string { return "" })
	check, ok = readinessOf(report, "config_declared")
	if !ok || check.Status != readinessMissing {
		t.Fatalf("缺少配置行时应报缺失：%+v", check)
	}
	if check.Blocking {
		t.Fatalf("配置项缺失不应阻断上线：%+v", check)
	}
}

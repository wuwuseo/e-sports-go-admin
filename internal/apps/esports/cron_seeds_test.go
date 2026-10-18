package esports

import (
	"strings"
	"testing"

	robfigcron "github.com/robfig/cron/v3"

	"go-admin/internal/tasks"
)

// cronSeedsParser 与 internal/cron 的 robfigcron.New(WithSeconds()) 保持同一解析口径：
// 6 段表达式（秒 分 时 日 月 周）。
func cronSeedsParser() robfigcron.Parser {
	return robfigcron.NewParser(
		robfigcron.Second | robfigcron.Minute | robfigcron.Hour |
			robfigcron.Dom | robfigcron.Month | robfigcron.Dow,
	)
}

// 定时任务种子必须可用、唯一且与注册表一致，否则「任务注册了却从不调度」（审计 D12）。
func TestCronSeedsAreValidAndComplete(t *testing.T) {
	seeds := CronSeeds()
	if len(seeds) == 0 {
		t.Fatal("定时任务种子不能为空：功能集的任务会全部处于未调度状态")
	}
	parser := cronSeedsParser()
	seen := make(map[string]bool, len(seeds))
	for _, seed := range seeds {
		if !strings.HasPrefix(seed.Name, "esports_") {
			t.Fatalf("任务名应以 esports_ 开头：%q", seed.Name)
		}
		if seen[seed.Name] {
			t.Fatalf("任务名重复：%q", seed.Name)
		}
		seen[seed.Name] = true
		if _, err := parser.Parse(seed.Expression); err != nil {
			t.Fatalf("任务 %s 的 cron 表达式非法（须为 6 段含秒）：%q: %v", seed.Name, seed.Expression, err)
		}
		if strings.TrimSpace(seed.Description) == "" {
			t.Fatalf("任务 %s 缺少说明文案", seed.Name)
		}
	}

	// 注册表在功能集开启（ESPORTS_EXPERIMENTAL=1）时才有 esports_ 任务；
	// 关闭时不注册任务，此处跳过交叉校验。
	var registered []string
	for _, name := range tasks.List() {
		if strings.HasPrefix(name, "esports_") {
			registered = append(registered, name)
		}
	}
	if len(registered) == 0 {
		t.Log("功能集未开启，跳过「种子 ↔ 注册表」一致性校验")
		return
	}
	for _, name := range registered {
		if !seen[name] {
			t.Fatalf("已注册任务 %s 缺少定时任务种子，将永远不会被调度", name)
		}
	}
	if len(seen) != len(registered) {
		t.Fatalf("种子数 %d 与注册任务数 %d 不一致", len(seen), len(registered))
	}
}

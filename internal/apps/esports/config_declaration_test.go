package esports

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	cmfapp "github.com/wuwuseo/cmf/app"
)

// esportsDynamicConfigPrefixes 列出「按运行时参数拼接、无法静态声明」的配置键前缀。
//   - app.esports.payment_channel.<channel>：由收款账户按渠道动态创建，声明某个固定键名没有意义。
//   - app.esports.withdrawal_channel.<channel>：渠道是固定枚举（bank_card/wechat/alipay），代码按前缀拼键读取。
//     这里豁免的只是「裸前缀」这个正则误报——三个真实渠道键仍必须显式声明在 Config 里，
//     否则后台配不了提现渠道开关（§6.3-05），运维只能手写数据库。
var esportsDynamicConfigPrefixes = []string{"payment_channel", "withdrawal_channel"}

// esportsRetiredConfigKeys 列出「历史遗留、只在启动迁移中读取（搬完即删）」的退役配置键。
// 它们不应再声明进 Config（否则系统配置页会重新出现可编辑入口，与新的专用页面双入口打架）：
//   - skill_levels：技能等级已改专用表 EsportsSkillLevel 驱动，迁移见 MigrateSkillLevelsFromConfig。
var esportsRetiredConfigKeys = []string{"skill_levels"}

// esportsConfigKeyLiteral 只匹配真实的字符串字面量，避免把注释里提到的键名当成读取点。
var esportsConfigKeyLiteral = regexp.MustCompile(`"app\.esports\.([a-z0-9_]+)`)

// esportsReadConfigKeys 扫描本包非测试源码，返回「裸键 → 引用位置」。
func esportsReadConfigKeys(t *testing.T) map[string][]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取包目录失败：%v", err)
	}
	keys := map[string][]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			for _, match := range esportsConfigKeyLiteral.FindAllStringSubmatch(line, -1) {
				key := match[1]
				dynamic := false
				for _, prefix := range esportsDynamicConfigPrefixes {
					if key == prefix {
						dynamic = true
						break
					}
				}
				retired := false
				for _, retiredKey := range esportsRetiredConfigKeys {
					if key == retiredKey {
						retired = true
						break
					}
				}
				if dynamic || retired {
					continue
				}
				keys[key] = append(keys[key], name+":"+strconv.Itoa(i+1))
			}
		}
	}
	return keys
}

// TestEsportsConfigKeysAreDeclared 锁定 §18-02：后台只能配置已在功能集 Config 中声明的键。
// apps.Provision 只为已声明的键建 SystemConfig 行，因此「读了但没声明」的键在后台根本不存在，
// 运维只能手写数据库（README 多处却要求运维去设置它们）。本测试断言「读取 ⊆ 声明」。
//
// 不依赖 ESPORTS_EXPERIMENTAL 开关：注册发生在 init()，与启用状态无关。
func TestEsportsConfigKeysAreDeclared(t *testing.T) {
	found := false
	declared := map[string]bool{}
	for _, app := range cmfapp.Registered() {
		if app.Code != "esports" {
			continue
		}
		found = true
		for _, item := range app.Config {
			declared[item.Key] = true
		}
	}
	if !found {
		t.Fatal("esports 功能集未注册")
	}

	missing := make([]string, 0)
	for key, refs := range esportsReadConfigKeys(t) {
		if declared[key] {
			continue
		}
		sorted := append([]string(nil), refs...)
		sort.Strings(sorted)
		missing = append(missing, "  app.esports."+key+"  ← "+strings.Join(sorted, ", "))
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("有 %d 个配置键被代码读取但未在功能集 Config 中声明，后台无法配置：\n%s",
			len(missing), strings.Join(missing, "\n"))
	}
}

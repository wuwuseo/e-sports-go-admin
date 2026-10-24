package esports

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/global"
)

// 技能等级（§5.5 / §7.3）由系统配置驱动：等级数量、编码与显示名可自定义，
// 配置顺序即高低序（下标小 = 低档）。商品门槛、订单快照与陪玩档案只存 code 字符串，
// 所有比较按本配置的序数进行；比较语义的 fail-closed 口径见 checkPlayerGameLevel。
const skillLevelsConfigKey = "app.esports.skill_levels"

// SkillLevel 单档技能等级：code 落库（商品门槛 / 订单快照 / 陪玩档案），name 仅作显示。
type SkillLevel struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// defaultSkillLevels 内置默认四级，配置缺失或非法时整体回落，保证系统始终可用。
var defaultSkillLevels = []SkillLevel{
	{Code: "novice", Name: "新手"},
	{Code: "skilled", Name: "熟练"},
	{Code: "senior", Name: "资深"},
	{Code: "mentor", Name: "导师"},
}

var skillLevelCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// parseSkillLevels 校验等级配置 JSON：1~12 档；编码小写字母开头的字母数字下划线（≤32），
// 显示名非空（≤32 字）；编码唯一。顺序即高低序，不做排序调整。
func parseSkillLevels(raw string) ([]SkillLevel, error) {
	var levels []SkillLevel
	if err := json.Unmarshal([]byte(raw), &levels); err != nil {
		return nil, fmt.Errorf("%w：技能等级配置必须是 [{\"code\",\"name\"}] 数组", errInvalidInput)
	}
	if len(levels) < 1 || len(levels) > 12 {
		return nil, fmt.Errorf("%w：技能等级须 1~12 档", errInvalidInput)
	}
	seen := make(map[string]struct{}, len(levels))
	normalized := make([]SkillLevel, 0, len(levels))
	for _, item := range levels {
		code := strings.ToLower(strings.TrimSpace(item.Code))
		name := strings.TrimSpace(item.Name)
		if !skillLevelCodePattern.MatchString(code) || name == "" || len([]rune(name)) > 32 {
			return nil, fmt.Errorf("%w：技能等级编码须小写字母开头的字母数字下划线（≤32），显示名非空（≤32 字）", errInvalidInput)
		}
		if _, dup := seen[code]; dup {
			return nil, fmt.Errorf("%w：技能等级编码重复：%s", errInvalidInput, code)
		}
		seen[code] = struct{}{}
		normalized = append(normalized, SkillLevel{Code: code, Name: name})
	}
	return normalized, nil
}

// skillLevels 读取当前等级配置；缺失或非法时回落内置默认四级（配置读失败不阻断业务）。
func skillLevels(ctx context.Context, db *ent.Client) []SkillLevel {
	raw := assessmentConfigString(ctx, db, skillLevelsConfigKey, "")
	if raw != "" {
		if levels, err := parseSkillLevels(raw); err == nil && len(levels) > 0 {
			return levels
		}
	}
	return append([]SkillLevel(nil), defaultSkillLevels...)
}

// skillLevelRank 返回编码在配置中的序数（0 = 最低档）；ok=false 表示该编码不在当前配置中。
func skillLevelRank(levels []SkillLevel, code string) (int, bool) {
	for i := range levels {
		if levels[i].Code == code {
			return i, true
		}
	}
	return 0, false
}

// skillLevelLowest / skillLevelHighest 取最低 / 最高档编码；空配置兜底 novice（仅防御，正常不可达）。
func skillLevelLowest(levels []SkillLevel) string {
	if len(levels) > 0 {
		return levels[0].Code
	}
	return "novice"
}

func skillLevelHighest(levels []SkillLevel) string {
	if len(levels) > 0 {
		return levels[len(levels)-1].Code
	}
	return "mentor"
}

// nextSkillLevel 返回当前档的上一档；已是最高档返回 false（无升级空间）。
func nextSkillLevel(levels []SkillLevel, current string) (string, bool) {
	rank, ok := skillLevelRank(levels, current)
	if !ok || rank+1 >= len(levels) {
		return current, false
	}
	return levels[rank+1].Code, true
}

// demoteSkillLevel 返回当前档的下一档（降一档）；已是最低档或编码失效时保持最低档。
func demoteSkillLevel(levels []SkillLevel, current string) string {
	rank, ok := skillLevelRank(levels, current)
	if !ok || rank < 1 {
		return skillLevelLowest(levels)
	}
	return levels[rank-1].Code
}

// skillLevelRankOrBelow 返回编码序数；编码不在当前配置中（如配置删档后的历史数据）
// 按 -1 处理，即低于一切有效档——失效档陪玩接不了任何已知门槛的单。
func skillLevelRankOrBelow(levels []SkillLevel, code string) int {
	rank, ok := skillLevelRank(levels, code)
	if !ok {
		return -1
	}
	return rank
}

// skillLevelOptions 展开为带序数的响应视图（公开读接口与管理端共用）。
func skillLevelOptions(levels []SkillLevel) []map[string]any {
	out := make([]map[string]any, 0, len(levels))
	for i, item := range levels {
		out = append(out, map[string]any{"code": item.Code, "name": item.Name, "rank": i})
	}
	return out
}

// ── HTTP 入口 ──

// publicSkillLevels 公开读：管理端表单选项与 H5 等级文案共用，按高低序返回。
// 配置本体在「系统配置」页（esports 组的 app.esports.skill_levels）编辑。
func publicSkillLevels(c fiber.Ctx) error {
	levels := skillLevels(c.Context(), global.ORM)
	return reply(c, fiber.Map{"items": skillLevelOptions(levels)})
}

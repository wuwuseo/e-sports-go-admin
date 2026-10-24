package esports

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsskilllevel"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

// 技能等级（§5.5 / §7.3）由专用表 EsportsSkillLevel 驱动（管理端「技能等级」页维护，
// 历史上曾存系统配置 app.esports.skill_levels，启动迁移一次性搬入该表并删除旧记录）：
// 等级数量、编码与显示名可自定义，sort_order 升序即高低序（下标小 = 低档）。
// 商品门槛、订单快照与陪玩档案只存 code 字符串，所有比较按本配置的序数进行；
// 比较语义的 fail-closed 口径见 checkPlayerGameLevel。
const skillLevelsConfigKey = "app.esports.skill_levels"

// SkillLevel 单档技能等级：code 落库（商品门槛 / 订单快照 / 陪玩档案），name 仅作显示。
type SkillLevel struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// defaultSkillLevels 内置默认四级，表空或读失败时整体回落，保证系统始终可用。
var defaultSkillLevels = []SkillLevel{
	{Code: "novice", Name: "新手"},
	{Code: "skilled", Name: "熟练"},
	{Code: "senior", Name: "资深"},
	{Code: "mentor", Name: "导师"},
}

var skillLevelCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// normalizeSkillLevels 校验并归一等级列表：1~12 档；编码小写字母开头的字母数字
// 下划线（≤32），显示名非空（≤32 字）；编码唯一。顺序即高低序，不做排序调整。
// 管理端整体保存与旧系统配置 JSON 迁移共用同一校验口径。
func normalizeSkillLevels(levels []SkillLevel) ([]SkillLevel, error) {
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

// skillLevels 读取当前等级配置（按 sort_order 升序）；表空或读失败时回落内置
// 默认四级（读失败不阻断业务）。
func skillLevels(ctx context.Context, db *ent.Client) []SkillLevel {
	rows, err := db.EsportsSkillLevel.Query().
		Order(esportsskilllevel.BySortOrder(), esportsskilllevel.ByID()).
		All(ctx)
	if err != nil || len(rows) == 0 {
		return append([]SkillLevel(nil), defaultSkillLevels...)
	}
	levels := make([]SkillLevel, 0, len(rows))
	for _, row := range rows {
		levels = append(levels, SkillLevel{Code: row.Code, Name: row.Name})
	}
	return levels
}

// saveSkillLevels 整体保存等级配置：事务内全删重建，sort_order 按提交顺序重排
// （列表序即高低序）。校验走 normalizeSkillLevels（重复编码 / 非法格式 / 档位数
// 越界整体拒绝），删除已有档与新增档在同一个事务里，不留中间态。
func saveSkillLevels(ctx context.Context, db *ent.Client, levels []SkillLevel) ([]SkillLevel, error) {
	normalized, err := normalizeSkillLevels(levels)
	if err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.EsportsSkillLevel.Delete().Exec(ctx); err != nil {
		return nil, err
	}
	bulk := make([]*ent.EsportsSkillLevelCreate, 0, len(normalized))
	for i, item := range normalized {
		bulk = append(bulk, tx.EsportsSkillLevel.Create().
			SetCode(item.Code).SetName(item.Name).SetSortOrder(i))
	}
	if _, err := tx.EsportsSkillLevel.CreateBulk(bulk...).Save(ctx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return normalized, nil
}

// MigrateSkillLevelsFromConfig 一次性把旧系统配置 app.esports.skill_levels 搬入
// 专用表并删除配置记录（幂等，随 Provision 每次启动执行）。表已有数据视为迁移
// 完成；配置值非法时读取侧本就整体回落内置四级，同样删除记录避免误改入口残留。
func MigrateSkillLevelsFromConfig(ctx context.Context, client *ent.Client) error {
	row, err := client.SystemConfig.Query().Where(systemconfig.KeyEQ(skillLevelsConfigKey)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("query skill levels config: %w", err)
	}
	if err := client.SystemConfig.DeleteOne(row).Exec(ctx); err != nil {
		return fmt.Errorf("delete skill levels config: %w", err)
	}
	exists, err := client.EsportsSkillLevel.Query().Exist(ctx)
	if err != nil {
		return fmt.Errorf("query skill levels: %w", err)
	}
	if exists || strings.TrimSpace(row.Value) == "" {
		return nil
	}
	var levels []SkillLevel
	if err := json.Unmarshal([]byte(row.Value), &levels); err != nil {
		// 旧值不是合法 JSON：不猜测内容，保持内置默认档，等管理员在新页面重配。
		return nil
	}
	normalized, err := normalizeSkillLevels(levels)
	if err != nil {
		return nil
	}
	bulk := make([]*ent.EsportsSkillLevelCreate, 0, len(normalized))
	for i, item := range normalized {
		bulk = append(bulk, client.EsportsSkillLevel.Create().
			SetCode(item.Code).SetName(item.Name).SetSortOrder(i))
	}
	if _, err := client.EsportsSkillLevel.CreateBulk(bulk...).Save(ctx); err != nil {
		return fmt.Errorf("migrate skill levels: %w", err)
	}
	return nil
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
// 配置本体在「技能等级」页（会员等级下方菜单）编辑。
func publicSkillLevels(c fiber.Ctx) error {
	levels := skillLevels(c.Context(), global.ORM)
	return reply(c, fiber.Map{"items": skillLevelOptions(levels)})
}

// adminSkillLevels 管理端读：返回当前生效配置；表空（从未保存过）时返回内置
// 默认四级并标记 defaulted=true，提示管理员保存后才算自定义配置。
func adminSkillLevels(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	exists, err := global.ORM.EsportsSkillLevel.Query().Exist(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	levels := skillLevels(c.Context(), global.ORM)
	return reply(c, fiber.Map{"items": skillLevelOptions(levels), "defaulted": !exists})
}

// updateSkillLevelsHandler 管理端整体保存：请求体 {"items":[{"code","name"}]}，
// 列表序即高低序（第 0 项为最低档）。
func updateSkillLevelsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in struct {
		Items []SkillLevel `json:"items"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	levels, err := saveSkillLevels(c.Context(), global.ORM, in.Items)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"items": skillLevelOptions(levels)})
}

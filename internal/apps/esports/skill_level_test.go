package esports

import (
	"errors"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsplayer"
)

// 技能等级配置驱动（§5.5 / §7.3）：配置解析校验、序数比较、升降档、
// 派单 fail-closed（门槛档被删）与系统配置回读。
func TestSkillLevelsConfigDriven(t *testing.T) {
	db := openProductDB(t, "esports_skill_levels")
	defer db.Close()
	ctx := t.Context()

	// 非法配置逐条拒绝：空数组 / 重复编码 / 非法编码 / 空显示名。
	for name, raw := range map[string]string{
		"空数组":   `[]`,
		"重复编码":  `[{"code":"novice","name":"新手"},{"code":"novice","name":"见习"}]`,
		"非法编码":  `[{"code":"9lhd","name":"九段"}]`,
		"空显示名":  `[{"code":"gold","name":""}]`,
		"超过12档": `[{"code":"a1","name":"一"},{"code":"a2","name":"二"},{"code":"a3","name":"三"},{"code":"a4","name":"四"},{"code":"a5","name":"五"},{"code":"a6","name":"六"},{"code":"a7","name":"七"},{"code":"a8","name":"八"},{"code":"a9","name":"九"},{"code":"a10","name":"十"},{"code":"a11","name":"十一"},{"code":"a12","name":"十二"},{"code":"a13","name":"十三"}]`,
	} {
		if _, err := parseSkillLevels(raw); !errors.Is(err, errInvalidInput) {
			t.Fatalf("%s 应返回参数错误：%v", name, err)
		}
	}

	// 写入合法配置（与系统配置页同一写入口径）：顺序即高低序，含大小写与空白归一。
	rawConfig := `[{"code":" Bronze ","name":"青铜"},{"code":"SILVER","name":"白银"},{"code":"gold","name":"黄金"}]`
	db.SystemConfig.Create().SetKey(skillLevelsConfigKey).SetName("技能等级配置").
		SetValue(rawConfig).SetType("string").SetGroup("esports").SetStatus(1).SaveX(ctx)
	if _, err := parseSkillLevels(rawConfig); err != nil {
		t.Fatal(err)
	}

	levels := skillLevels(ctx, db)
	if skillLevelLowest(levels) != "bronze" || skillLevelHighest(levels) != "gold" {
		t.Fatalf("最低/最高档取值错误：%s/%s", skillLevelLowest(levels), skillLevelHighest(levels))
	}
	if rank, ok := skillLevelRank(levels, "silver"); !ok || rank != 1 {
		t.Fatalf("silver 序数应为 1：%d/%v", rank, ok)
	}
	if _, ok := skillLevelRank(levels, "novice"); ok {
		t.Fatal("配置中不存在的编码不应命中")
	}
	if next, ok := nextSkillLevel(levels, "bronze"); !ok || next != "silver" {
		t.Fatalf("bronze 的上一档应为 silver：%q/%v", next, ok)
	}
	if _, ok := nextSkillLevel(levels, "gold"); ok {
		t.Fatal("gold 已是最高档，不应有升级空间")
	}
	if got := demoteSkillLevel(levels, "gold"); got != "silver" {
		t.Fatalf("gold 降档应为 silver：%q", got)
	}
	if got := demoteSkillLevel(levels, "bronze"); got != "bronze" {
		t.Fatalf("最低档降档应保持：%q", got)
	}
	// 失效档（配置删档后的历史数据）：序数 -1，降档回落最低档。
	if skillLevelRankOrBelow(levels, "mentor") != -1 {
		t.Fatal("失效档序数应为 -1")
	}
	if got := demoteSkillLevel(levels, "mentor"); got != "bronze" {
		t.Fatalf("失效档降档应回落最低档：%q", got)
	}

	// 派单 fail-closed：门槛档被删 → 任何陪玩都被拒，并给出明确中文原因。
	player := db.EsportsPlayer.Create().SetUserID(71).SetDisplayName("等级陪玩").
		SetQualification(esportsplayer.QualificationQualified).
		SetPresence(esportsplayer.PresenceOnline).
		SetLevel("silver").SaveX(ctx)
	if err := checkPlayerGameLevel(levels, player, "", "novice"); !errors.Is(err, errForbidden) ||
		!strings.Contains(err.Error(), "已不在等级配置") {
		t.Fatalf("门槛档被删应 fail closed：%v", err)
	}
	// 空门槛跳过等级校验；在配置内的门槛正常比较。
	if err := checkPlayerGameLevel(levels, player, "", ""); err != nil {
		t.Fatalf("空门槛应跳过等级校验：%v", err)
	}
	if err := checkPlayerGameLevel(levels, player, "", "gold"); !errors.Is(err, errForbidden) {
		t.Fatalf("silver 陪玩不应通过 gold 门槛：%v", err)
	}
	if err := checkPlayerGameLevel(levels, player, "", "silver"); err != nil {
		t.Fatalf("同级门槛应放行：%v", err)
	}

	// 配置缺失时整体回落内置四级（系统配置页误填同理，读侧已做容错）。
	if got := skillLevelLowest(defaultSkillLevels); got != "novice" {
		t.Fatalf("默认四级最低档应为 novice：%q", got)
	}
}

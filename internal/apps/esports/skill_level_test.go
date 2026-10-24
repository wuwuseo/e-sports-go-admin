package esports

import (
	"errors"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsskilllevel"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/systemconfig"
)

// 技能等级表驱动：保存校验、序数比较、升降档、
// 派单 fail-closed（门槛档被删）、旧系统配置一次性迁移。
func TestSkillLevelsTableDriven(t *testing.T) {
	db := openProductDB(t, "esports_skill_levels")
	defer db.Close()
	ctx := t.Context()

	// 非法配置逐条拒绝：空列表 / 重复编码 / 非法编码 / 空显示名 / 超过 12 档。
	for name, items := range map[string][]SkillLevel{
		"空列表": {},
		"重复编码": {{"novice", "新手"}, {"NOVICE ", "见习"}},
		"非法编码": {{"9lhd", "九段"}},
		"空显示名": {{"gold", " "}},
		"超过12档": {
			{"a1", "一"}, {"a2", "二"}, {"a3", "三"}, {"a4", "四"}, {"a5", "五"}, {"a6", "六"},
			{"a7", "七"}, {"a8", "八"}, {"a9", "九"}, {"a10", "十"}, {"a11", "十一"}, {"a12", "十二"},
			{"a13", "十三"},
		},
	} {
		if _, err := saveSkillLevels(ctx, db, items); !errors.Is(err, errInvalidInput) {
			t.Fatalf("%s 应返回参数错误：%v", name, err)
		}
	}
	// 全部拒绝后表仍为空：读取回落内置四级。
	if levels := skillLevels(ctx, db); skillLevelLowest(levels) != "novice" || skillLevelHighest(levels) != "mentor" {
		t.Fatalf("非法保存后应回落内置四级：%s/%s", skillLevelLowest(levels), skillLevelHighest(levels))
	}

	// 保存合法配置（与管理端同一写入口径）：顺序即高低序，含大小写与空白归一。
	levels, err := saveSkillLevels(ctx, db, []SkillLevel{
		{" Bronze ", "青铜"}, {"SILVER", "白银"}, {"gold", "黄金"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if levels[0].Code != "bronze" || levels[1].Code != "silver" {
		t.Fatalf("编码归一错误：%v", levels)
	}
	if got, err := db.EsportsSkillLevel.Query().Order(esportsskilllevel.BySortOrder()).First(ctx); err != nil || got.Code != "bronze" || got.SortOrder != 0 {
		t.Fatalf("最低档应落表 bronze/sort_order=0：%v/%v", got, err)
	}

	levels = skillLevels(ctx, db)
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
	// 失效档（删档后的历史数据）：序数 -1，降档回落最低档。
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

	// 整体保存会全删重建：改档后旧编码失效、新编码按提交顺序取档。
	if _, err := saveSkillLevels(ctx, db, []SkillLevel{{"iron", "黑铁"}, {"bronze", "青铜"}}); err != nil {
		t.Fatal(err)
	}
	levels = skillLevels(ctx, db)
	if skillLevelLowest(levels) != "iron" || skillLevelHighest(levels) != "bronze" {
		t.Fatalf("重建后最低/最高档错误：%s/%s", skillLevelLowest(levels), skillLevelHighest(levels))
	}
	if got := db.EsportsSkillLevel.Query().CountX(ctx); got != 2 {
		t.Fatalf("重建后应只有 2 档：%d", got)
	}
}

// MigrateSkillLevelsFromConfig：旧系统配置一次性搬入专用表并删除配置记录；
// 表已有数据时只删记录；旧值非法时不导入（回落内置四级）。
func TestMigrateSkillLevelsFromConfig(t *testing.T) {
	db := openProductDB(t, "esports_skill_levels_migrate")
	defer db.Close()
	ctx := t.Context()

	seedConfig := func(t *testing.T, value string) {
		t.Helper()
		db.SystemConfig.Create().SetKey(skillLevelsConfigKey).SetName("技能等级配置").
			SetValue(value).SetType("string").SetGroup("esports").SetStatus(1).SaveX(ctx)
	}
	configExists := func() bool {
		return db.SystemConfig.Query().Where(systemconfig.KeyEQ(skillLevelsConfigKey)).ExistX(ctx)
	}

	// 合法旧值：搬入表（顺序即高低序）并删除配置记录；重复执行幂等。
	seedConfig(t, `[{"code":" Bronze ","name":"青铜"},{"code":"SILVER","name":"白银"},{"code":"gold","name":"黄金"}]`)
	for i := 0; i < 2; i++ {
		if err := MigrateSkillLevelsFromConfig(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	if configExists() {
		t.Fatal("迁移后旧配置记录应被删除")
	}
	levels := skillLevels(ctx, db)
	if skillLevelLowest(levels) != "bronze" || skillLevelHighest(levels) != "gold" {
		t.Fatalf("迁移结果错误：%s/%s", skillLevelLowest(levels), skillLevelHighest(levels))
	}

	// 表已有数据时：只删记录，不动表内配置。
	if _, err := saveSkillLevels(ctx, db, []SkillLevel{{"iron", "黑铁"}}); err != nil {
		t.Fatal(err)
	}
	seedConfig(t, `[{"code":"novice","name":"新手"}]`)
	if err := MigrateSkillLevelsFromConfig(ctx, db); err != nil {
		t.Fatal(err)
	}
	if configExists() {
		t.Fatal("残留配置记录应被删除")
	}
	if levels := skillLevels(ctx, db); len(levels) != 1 || levels[0].Code != "iron" {
		t.Fatalf("表已有数据时不应被迁移覆盖：%v", levels)
	}

	// 旧值非法：不导入，配置记录同样删除，读取回落内置四级。
	db.EsportsSkillLevel.Delete().ExecX(ctx)
	seedConfig(t, `not-a-json`)
	if err := MigrateSkillLevelsFromConfig(ctx, db); err != nil {
		t.Fatal(err)
	}
	if configExists() {
		t.Fatal("非法旧值也应清理配置记录")
	}
	if got := db.EsportsSkillLevel.Query().CountX(ctx); got != 0 {
		t.Fatalf("非法旧值不应导入任何档：%d", got)
	}
	if got := skillLevelLowest(defaultSkillLevels); got != "novice" {
		t.Fatalf("默认四级最低档应为 novice：%q", got)
	}
}

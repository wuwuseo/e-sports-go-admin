package esports

import (
	"testing"

	"go-admin/internal/ent/esportsproductcategory"
)

// 游戏分区种子：新库补齐「三角洲行动 / 三角洲行动手游」两条启用分区，
// 与商品分类 game 口径一致；重复播种不新增、不覆盖运营改动。
func TestSeedGameZonesIdempotent(t *testing.T) {
	db := openRound4MemberDB(t, "r4wm_game_zone_seed")
	defer db.Close()
	ctx := t.Context()

	created, err := SeedGameZones(ctx, db)
	if err != nil {
		t.Fatalf("播种游戏分区失败：%v", err)
	}
	if created != 2 {
		t.Fatalf("首次播种应新建两个分区：得到 %d 个", created)
	}
	for _, game := range []string{"三角洲行动", "三角洲行动手游"} {
		row, err := db.EsportsProductCategory.Query().
			Where(esportsproductcategory.GameEQ(game)).Only(ctx)
		if err != nil {
			t.Fatalf("缺少游戏分区 %s：%v", game, err)
		}
		if row.Status != esportsproductcategory.StatusEnabled {
			t.Fatalf("分区 %s 应为启用状态：%+v", game, row)
		}
	}

	// 幂等：运营改过名字的分区不被覆盖，重复播种也不新增。
	first := db.EsportsProductCategory.Query().
		Where(esportsproductcategory.CodeEQ("GAME_DELTA_FORCE")).OnlyX(ctx)
	if _, err := updateCategory(ctx, db, first.ID, categoryInput{
		Name: "三角洲行动（改）", Game: "三角洲行动", SortOrder: 1, Status: "enabled",
	}); err != nil {
		t.Fatalf("运营改动分区失败：%v", err)
	}
	again, err := SeedGameZones(ctx, db)
	if err != nil || again != 0 {
		t.Fatalf("重复播种应为空操作：新建 %d 个 err=%v", again, err)
	}
	kept := db.EsportsProductCategory.Query().
		Where(esportsproductcategory.CodeEQ("GAME_DELTA_FORCE")).OnlyX(ctx)
	if kept.Name != "三角洲行动（改）" {
		t.Fatalf("重复播种覆盖了运营改动：%+v", kept)
	}
}

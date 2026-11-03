package esports

import (
	"context"
	"fmt"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsproductcategory"
)

// 默认游戏分区种子：分区以商品分类的 game 字段为口径（无独立分区表），
// 启用功能集时补齐缺失项；已存在的一律保留运营改动（改名 / 排序 / 停用）。
var gameZoneSeeds = []categoryInput{
	{Code: "GAME_DELTA_FORCE", Name: "三角洲行动", Game: "三角洲行动", SortOrder: 1, Status: "enabled"},
	{Code: "GAME_DELTA_FORCE_MOBILE", Name: "三角洲行动手游", Game: "三角洲行动手游", SortOrder: 2, Status: "enabled"},
}

// SeedGameZones 幂等播种默认游戏分区（商品分类），只补建缺失的 code，重复执行安全。
// code 唯一索引兜底并发重复播种。返回本次新建的分区数。
func SeedGameZones(ctx context.Context, db *ent.Client) (int, error) {
	if db == nil {
		return 0, errInvalidInput
	}
	created := 0
	for _, seed := range gameZoneSeeds {
		code := strings.TrimSpace(seed.Code)
		exists, err := db.EsportsProductCategory.Query().
			Where(esportsproductcategory.CodeEQ(code)).Exist(ctx)
		if err != nil {
			return created, fmt.Errorf("查询游戏分区 %s 失败: %w", code, err)
		}
		if exists {
			continue
		}
		if _, err = saveCategory(ctx, db, seed); err != nil {
			// 并发播种：唯一索引拒绝时视为已存在，不视为失败。
			if ent.IsConstraintError(err) || err == errConflict {
				continue
			}
			return created, fmt.Errorf("播种游戏分区 %s 失败: %w", code, err)
		}
		created++
	}
	return created, nil
}

package esports

import (
	"context"
	"fmt"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsmemberlevelrule"
)

// 五档默认会员等级。门槛单位为分（与成长值同构），折扣用基点（10000 = 原价），
// 开卡赠送比例用基点。运营可在后台改门槛 / 折扣 / 权益 / 启停，播种只在缺失时补建。
var memberLevelSeeds = []memberLevelInput{
	{Code: "normal", Name: "普通", GrowthThreshold: 0, DiscountBps: 10000,
		PrepayBonusBps: 0, SortOrder: 1},
	{Code: "silver", Name: "银卡", GrowthThreshold: 10_000, DiscountBps: 9800,
		PrepayBonusBps: 100, SortOrder: 2},
	{Code: "gold", Name: "金卡", GrowthThreshold: 50_000, DiscountBps: 9500,
		PrepayBonusBps: 200, PriorityDispatch: true, PriorityBooking: memberBoolValue(true), SortOrder: 3},
	{Code: "platinum", Name: "铂金", GrowthThreshold: 200_000, DiscountBps: 9200,
		PrepayBonusBps: 300, PriorityDispatch: true, DedicatedCs: memberBoolValue(true),
		PriorityBooking: memberBoolValue(true), SortOrder: 4},
	{Code: "diamond", Name: "钻石", GrowthThreshold: 500_000, DiscountBps: 8800,
		PrepayBonusBps: 500, PriorityDispatch: true, DedicatedCs: memberBoolValue(true),
		PriorityBooking: memberBoolValue(true), SortOrder: 5},
}

func memberBoolValue(value bool) *bool { return &value }

// SeedMemberLevels 幂等播种五档默认会员等级（普通 / 银卡 / 金卡 / 铂金 / 钻石，）。
// 功能启用时（Provision）调用：新库没有等级规则会导致客户永不定级、会员权益全不生效。
// 只补建缺失的 code，已存在的一律保留运营改动（门槛 / 折扣 / 权益 / 启停），重复执行安全；
// code 唯一索引兜底并发重复播种。返回本次新建的档位数。
func SeedMemberLevels(ctx context.Context, db *ent.Client) (int, error) {
	if db == nil {
		return 0, errInvalidInput
	}
	created := 0
	for _, seed := range memberLevelSeeds {
		code := strings.TrimSpace(seed.Code)
		exists, err := db.EsportsMemberLevelRule.Query().
			Where(esportsmemberlevelrule.CodeEQ(code)).Exist(ctx)
		if err != nil {
			return created, fmt.Errorf("查询会员等级 %s 失败: %w", code, err)
		}
		if exists {
			continue
		}
		if _, err = createMemberLevel(ctx, db, seed); err != nil {
			// 并发播种：唯一索引拒绝时视为已存在，不视为失败。
			if ent.IsConstraintError(err) || err == errConflict {
				continue
			}
			return created, fmt.Errorf("播种会员等级 %s 失败: %w", code, err)
		}
		created++
	}
	return created, nil
}

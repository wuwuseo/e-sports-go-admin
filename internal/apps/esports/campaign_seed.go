package esports

import (
	"context"
	"fmt"
	"time"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
)

// SeedRechargeCampaign 为全新安装补一条可编辑的充值活动示例。
// 活动保持草稿，运营确认赠送规则与活动期后再启用；已有活动时不添加示例。
func SeedRechargeCampaign(ctx context.Context, db *ent.Client) (bool, error) {
	if db == nil {
		return false, errInvalidInput
	}
	exists, err := db.EsportsRechargeCampaign.Query().Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("查询充值活动失败: %w", err)
	}
	if exists {
		return false, nil
	}

	now := time.Now().UTC()
	_, err = createCampaign(ctx, db, campaignInput{
		Code:              "RECHARGE_100_GIFT_10",
		Name:              "满100送10（示例）",
		ActivityType:      "full_gift",
		Tiers:             []schema.CampaignTier{{ThresholdCents: 10_000, BonusCents: 1_000}},
		Audience:          "all",
		TargetAccountType: "customer_balance",
		StackRule:         "exclusive",
		BonusValidDays:    30,
		StartsAt:          now,
		EndsAt:            now.AddDate(1, 0, 0),
	})
	if err != nil {
		// 多实例同时启动时，唯一 code 冲突表示示例已由另一实例播种。
		if ent.IsConstraintError(err) || err == errConflict {
			return false, nil
		}
		return false, fmt.Errorf("播种充值活动失败: %w", err)
	}
	return true, nil
}

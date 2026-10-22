package esports

import (
	"testing"
	"time"

	"go-admin/internal/ent/esportsrechargecampaign"
)

func TestSeedRechargeCampaignIdempotent(t *testing.T) {
	db := openCampaignDB(t)
	ctx := t.Context()

	created, err := SeedRechargeCampaign(ctx, db)
	if err != nil || !created {
		t.Fatalf("首次播种充值活动失败: created=%v err=%v", created, err)
	}
	item := db.EsportsRechargeCampaign.Query().OnlyX(ctx)
	if item.Code != "RECHARGE_100_GIFT_10" || item.Status != esportsrechargecampaign.StatusDraft ||
		item.ActivityType != esportsrechargecampaign.ActivityTypeFullGift ||
		item.Audience != esportsrechargecampaign.AudienceAll ||
		item.TargetAccountType != esportsrechargecampaign.TargetAccountTypeCustomerBalance ||
		item.StackRule != esportsrechargecampaign.StackRuleExclusive ||
		len(item.Tiers) != 1 || item.Tiers[0].ThresholdCents != 10_000 ||
		item.Tiers[0].BonusCents != 1_000 || item.BonusValidDays != 30 ||
		!item.StartsAt.Before(item.EndsAt) {
		t.Fatalf("充值活动示例内容不正确: %+v", item)
	}

	if _, err := db.EsportsRechargeCampaign.UpdateOne(item).SetName("运营已修改").Save(ctx); err != nil {
		t.Fatal(err)
	}
	created, err = SeedRechargeCampaign(ctx, db)
	if err != nil || created {
		t.Fatalf("重复播种应为空操作: created=%v err=%v", created, err)
	}
	if count := db.EsportsRechargeCampaign.Query().CountX(ctx); count != 1 {
		t.Fatalf("重复播种后应只有一条活动，实际 %d", count)
	}
	if got := db.EsportsRechargeCampaign.GetX(ctx, item.ID).Name; got != "运营已修改" {
		t.Fatalf("重复播种覆盖了运营修改: %s", got)
	}
}

func TestSeedRechargeCampaignSkipsExistingCampaign(t *testing.T) {
	db := openCampaignDB(t)
	ctx := t.Context()
	if _, err := createCampaign(ctx, db, baseCampaignInput("EXISTING", time.Now())); err != nil {
		t.Fatal(err)
	}
	created, err := SeedRechargeCampaign(ctx, db)
	if err != nil || created {
		t.Fatalf("已有活动时不应再添加示例: created=%v err=%v", created, err)
	}
	if count := db.EsportsRechargeCampaign.Query().CountX(ctx); count != 1 {
		t.Fatalf("已有活动应保持一条，实际 %d", count)
	}
}

package esports

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsrechargecampaign"
)

// 公开礼物目录只返回在售礼物，且不暴露分成等内部字段。
func TestPublicGiftsOnlyOnSaleAndNoCommission(t *testing.T) {
	db := openGiftDB(t, "esports_public_gifts")
	defer db.Close()
	ctx := t.Context()

	seedGift(t, db, giftInput{Code: "PUB-ON", Name: "在售", PriceCents: 1000, CommissionMode: "percent", CommissionBp: 6000})
	off := seedGift(t, db, giftInput{Code: "PUB-OFF", Name: "下架", PriceCents: 3000, CommissionMode: "percent", CommissionBp: 6000})
	if _, err := setGiftStatus(ctx, db, off.ID, off.Version, "off_sale"); err != nil {
		t.Fatal(err)
	}
	draft := seedGift(t, db, giftInput{Code: "PUB-D2", Name: "草稿", PriceCents: 4000, CommissionMode: "percent", CommissionBp: 6000})
	if _, err := setGiftStatus(ctx, db, draft.ID, draft.Version, "draft"); err != nil {
		t.Fatal(err)
	}

	items, err := listGifts(ctx, db, "on_sale", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Status.String() != "on_sale" {
			t.Fatalf("公开礼物列表混入非在售礼物：%+v", item.Status)
		}
	}
	if len(items) != 1 || items[0].Code != "PUB-ON" {
		t.Fatalf("公开礼物列表数量/内容错误：%d", len(items))
	}

	views := make([]publicGiftView, 0, len(items))
	for _, item := range items {
		views = append(views, toPublicGiftView(item))
	}
	raw, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "commission") || strings.Contains(string(raw), "sort_order") {
		t.Fatalf("公开礼物视图泄漏内部分成/排序字段：%s", string(raw))
	}
}

// 公开陪玩推荐只返回合格且在线，且绝不泄漏 user_id；支持按游戏分区过滤（games 空=不限）。
func TestPublicPlayersQualifiedOnlineNoUserID(t *testing.T) {
	db := openGiftDB(t, "esports_public_players")
	defer db.Close()
	ctx := t.Context()

	qualifiedOnline := seedPlayer(t, db, 9001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seedPlayer(t, db, 9002, esportsplayer.QualificationQualified, esportsplayer.PresenceOffline)
	seedPlayer(t, db, 9003, esportsplayer.QualificationPending, esportsplayer.PresenceOnline)
	seedPlayer(t, db, 9004, esportsplayer.QualificationSuspended, esportsplayer.PresenceOnline)

	views, err := listPublicPlayers(ctx, db, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].ID != qualifiedOnline.ID {
		t.Fatalf("公开陪玩列表未正确过滤 qualified+online：%+v", views)
	}
	if !views[0].Online || views[0].Qualification != "qualified" || views[0].Presence != "online" {
		t.Fatalf("公开陪玩视图字段错误：%+v", views[0])
	}
	raw, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "user_id") {
		t.Fatalf("公开陪玩视图泄漏 user_id：%s", string(raw))
	}

	// 游戏分区过滤：games 含目标分区，或 games 为空（不限分区）都应命中。
	unbounded := seedPlayer(t, db, 9100, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(qualifiedOnline.ID).SetGames([]string{"英雄联盟"}).ExecX(ctx)
	db.EsportsPlayer.UpdateOneID(unbounded.ID).SetGames([]string{}).ExecX(ctx)

	hit, err := listPublicPlayers(ctx, db, "英雄联盟", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(hit) != 2 {
		t.Fatalf("按「英雄联盟」过滤应命中含该分区与不限分区的陪玩，得到 %d", len(hit))
	}
	miss, err := listPublicPlayers(ctx, db, "王者荣耀", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(miss) != 1 || miss[0].ID != unbounded.ID {
		t.Fatalf("按「王者荣耀」过滤只应命中不限分区的陪玩：%+v", miss)
	}
}

// 公开活动列表只返回 status=active 且当前在活动期内的活动。
func TestPublicCampaignsOnlyActiveWithinPeriod(t *testing.T) {
	db := openGiftDB(t, "esports_public_campaigns")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	seedCampaign := func(code string, starts, ends time.Time, status string) *ent.EsportsRechargeCampaign {
		in := baseCampaignInput(code, now)
		in.StartsAt = starts
		in.EndsAt = ends
		item, err := createCampaign(ctx, db, in)
		if err != nil {
			t.Fatalf("创建活动失败：%v", err)
		}
		if status != "draft" {
			item, err = setCampaignStatus(ctx, db, item.ID, item.Version, status)
			if err != nil {
				t.Fatalf("设置活动状态失败：%v", err)
			}
		}
		return item
	}

	seedCampaign("PUB-ACTIVE", now.Add(-time.Hour), now.Add(time.Hour), "active")
	seedCampaign("PUB-FUTURE", now.Add(time.Hour), now.Add(2*time.Hour), "active")
	seedCampaign("PUB-PAST", now.Add(-2*time.Hour), now.Add(-time.Hour), "active")
	seedCampaign("PUB-DRAFT", now.Add(-time.Hour), now.Add(time.Hour), "draft")
	seedCampaign("PUB-PAUSED", now.Add(-time.Hour), now.Add(time.Hour), "paused")

	items, err := listActiveCampaigns(ctx, db, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Code != "PUB-ACTIVE" {
		t.Fatalf("公开活动列表未正确过滤 active 且期内：%+v", items)
	}
	if items[0].Status != esportsrechargecampaign.StatusActive {
		t.Fatalf("公开活动返回了非 active 状态：%+v", items[0].Status)
	}
}

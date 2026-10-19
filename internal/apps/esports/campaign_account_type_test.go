package esports

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/global"
)

// P1-19 活动账户类型：支持「客户余额」与「陪玩猫粮」两类，未知目标账户类型显式拒绝并指出支持值。
func TestCampaignTargetAccountTypeAcceptsBothAndRejectsUnknown(t *testing.T) {
	db := openCampaignDB(t)
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	player := seedPlayer(t, db, 211, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// 猫粮活动可创建并落库。
	catfood, err := createCampaign(ctx, db, campaignInput{
		Code: "ACC-CATFOOD", Name: "猫粮奖励活动", ActivityType: "limited",
		Tiers:             []schema.CampaignTier{{ThresholdCents: 1000, BonusCents: 100}},
		Audience:          "player",
		AudiencePlayerIDs: []int{player.ID},
		TargetAccountType: "player_catfood",
		StackRule:         "stack",
		StartsAt:          now, EndsAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("猫粮活动创建失败：%v", err)
	}
	if catfood.TargetAccountType != "player_catfood" {
		t.Fatalf("目标账户未落库：%+v", catfood)
	}
	view := toCampaignView(catfood)
	if view.TargetAccountType != "player_catfood" || len(view.AudiencePlayerIDs) != 1 ||
		view.AudiencePlayerIDs[0] != player.ID {
		t.Fatalf("活动视图未下发账户类型 / 陪玩受众：%+v", view)
	}

	// 客户余额活动（留空目标账户）仍按客户余额落库，兼容存量。
	balance, err := createCampaign(ctx, db, baseCampaignInput("ACC-BALANCE", now))
	if err != nil {
		t.Fatalf("客户余额活动创建失败：%v", err)
	}
	if balance.TargetAccountType != "customer_balance" {
		t.Fatalf("留空目标账户应回落客户余额：%+v", balance)
	}

	// 未知目标账户类型必须显式拒绝，并指出支持值。
	unknown := baseCampaignInput("ACC-UNKNOWN", now)
	unknown.TargetAccountType = "player_gold"
	_, err = createCampaign(ctx, db, unknown)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("未知账户类型应拒绝：%v", err)
	}
	if !strings.Contains(err.Error(), "player_gold") ||
		!strings.Contains(err.Error(), "customer_balance") || !strings.Contains(err.Error(), "player_catfood") {
		t.Fatalf("未知账户类型错误未指出支持值：%v", err)
	}
	if got := db.EsportsRechargeCampaign.Query().CountX(ctx); got != 2 {
		t.Fatalf("被拒的活动不应落库：%d", got)
	}
}

// P1-19 按活动充值的账户类型：客户余额 / 陪玩猫粮都接受，未知与缺失显式拒绝。
func TestCampaignRechargeAccountType(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		want  string
		wants bool
	}{
		{"customer_balance", walletCustomerBalance, true},
		{"player_catfood", walletPlayerCatfood, true},
		{" player_catfood ", walletPlayerCatfood, true},
		{"", "", false},
		{"player_gold", "", false},
		{"stored_value_card", "", false},
	} {
		got, err := campaignRechargeAccountType(tc.raw)
		if tc.wants {
			if err != nil || got != tc.want {
				t.Fatalf("%q 应被接受为 %q，got=%q err=%v", tc.raw, tc.want, got, err)
			}
			continue
		}
		if !errors.Is(err, errInvalidInput) {
			t.Fatalf("%q 应拒绝：%v", tc.raw, err)
		}
		if tc.raw != "" && !strings.Contains(err.Error(), tc.raw) {
			t.Fatalf("%q 的错误未指出被拒的账户类型：%v", tc.raw, err)
		}
	}
}

// P1-19 发放路径（HTTP 层）：按活动充值接口接受客户余额与陪玩猫粮两类账户并实际发放奖励，
// 未知 / 缺失账户类型返回 400 且文案指出支持值。
func TestCampaignRechargeHandlerRoutesBothAccountTypes(t *testing.T) {
	db := openCampaignDB(t)
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	previousORM := global.ORM
	global.ORM = db
	defer func() { global.ORM = previousORM }()

	admin := seedAdmin(t, db, "campaign-recharge-admin")
	customer := saveCustomerOrFail(t, db, admin.ID)
	player := seedPlayer(t, db, 212, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// 客户余额活动 + 猫粮活动各一条，都命中 1000 分实充赠 100 分。
	balanceCampaign := baseCampaignInput("ACC-API-BAL", now)
	balanceCampaign.Tiers = []schema.CampaignTier{{ThresholdCents: 1000, BonusCents: 100}}
	createActiveCampaign(t, db, balanceCampaign)
	catfoodCampaign := campaignInput{
		Code: "ACC-API-PLAYER", Name: "猫粮奖励活动", ActivityType: "limited",
		Tiers:             []schema.CampaignTier{{ThresholdCents: 1000, BonusCents: 100}},
		Audience:          "player",
		AudiencePlayerIDs: []int{player.ID},
		TargetAccountType: "player_catfood", StackRule: "stack",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
	}
	created, err := createCampaign(ctx, db, catfoodCampaign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = setCampaignStatus(ctx, db, created.ID, created.Version, "active"); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", true)
		return c.Next()
	})
	app.Post("/admin/wallets/:id/recharge-campaigns", rechargeWalletByCampaignHandler)

	post := func(accountType string, ownerID int, key string) (int, string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"principal_cents": 1000, "note": "活动奖励"})
		path := "/admin/wallets/" + strconv.Itoa(ownerID) + "/recharge-campaigns?account_type=" + accountType
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
		if err != nil {
			t.Fatalf("请求失败：%v", err)
		}
		defer resp.Body.Close()
		var out struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out.Message
	}

	if status, msg := post(walletCustomerBalance, customer.ID, "acc-api-bal-1"); status != http.StatusOK {
		t.Fatalf("客户余额按活动充值应成功：%d %s", status, msg)
	}
	customerBalance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if customerBalance.BonusCents != 100 {
		t.Fatalf("客户余额活动奖励未发放：%+v", customerBalance)
	}

	if status, msg := post(walletPlayerCatfood, player.ID, "acc-api-player-1"); status != http.StatusOK {
		t.Fatalf("陪玩猫粮按活动充值应成功：%d %s", status, msg)
	}
	catfoodBalance, err := walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if catfoodBalance.PrincipalCents != 1000 || catfoodBalance.BonusCents != 100 {
		t.Fatalf("陪玩猫粮活动奖励未发放：%+v", catfoodBalance)
	}

	if status, msg := post("player_gold", player.ID, "acc-api-bad-1"); status != http.StatusBadRequest ||
		!strings.Contains(msg, "player_gold") {
		t.Fatalf("未知账户类型应 400 并指出类型：%d %s", status, msg)
	}
	if status, msg := post("", player.ID, "acc-api-bad-2"); status != http.StatusBadRequest ||
		!strings.Contains(msg, "account_type") {
		t.Fatalf("缺失账户类型应 400 并提示传参：%d %s", status, msg)
	}
}

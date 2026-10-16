package esports

import (
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/systemconfig"
)

// walletAccountViewOf 读取某类钱包账户的视图（测试辅助）。
func walletAccountViewOf(t *testing.T, db *ent.Client, accountType string) *walletAccountView {
	t.Helper()
	ctx := t.Context()
	account := db.EsportsWalletAccount.Query().Where().AllX(ctx)
	for _, item := range account {
		if item.AccountType.String() == accountType {
			view, err := walletView(ctx, db, item)
			if err != nil {
				t.Fatal(err)
			}
			return view
		}
	}
	t.Fatalf("未找到钱包账户：%s", accountType)
	return nil
}

// 钱包展示口径（§11.4）：默认展示名与固定倍率换算、工作室配置覆盖、非法配置回落默认。
func TestWalletDisplayUsesStudioConfigAndFixedRate(t *testing.T) {
	db := openAfterSaleDB(t, "esports_wallet_display")
	defer db.Close()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 2050, Note: "展示配置测试充值"}, "wallet-display-1"); err != nil {
		t.Fatal(err)
	}

	// 默认：余额 / 每单位 100 分 → 2050 分 = 20.50。
	view := walletAccountViewOf(t, db, walletCustomerBalance)
	if view.Display.DisplayName != "余额" || view.Display.UnitName != "元" ||
		view.Display.UnitCents != 100 || view.Display.DisplayAmount != "20.50" ||
		view.Display.Icon == "" || view.Display.Description == "" {
		t.Fatalf("默认展示口径不正确：%+v", view.Display)
	}

	// 工作室配置：改名换图标文案，并把倍率改为每单位 50 分 → 2050 分 = 41.00。
	db.SystemConfig.Delete().Where(systemconfig.KeyEQ(walletDisplayConfigKey)).ExecX(ctx)
	db.SystemConfig.Create().SetKey(walletDisplayConfigKey).SetName("钱包展示配置").
		SetValue(`{"customer_balance":{"display_name":"抖币","icon":"coin","description":"平台点数","unit_name":"抖币","unit_cents":50}}`).
		SetType("json").SetGroup("esports").SetStatus(1).SaveX(ctx)
	view = walletAccountViewOf(t, db, walletCustomerBalance)
	if view.Display.DisplayName != "抖币" || view.Display.Icon != "coin" ||
		view.Display.UnitName != "抖币" || view.Display.UnitCents != 50 ||
		view.Display.DisplayAmount != "41.00" {
		t.Fatalf("工作室展示配置未生效：%+v", view.Display)
	}
	// 未配置的账户类型仍用默认。
	player := seedPlayer(t, db, 4901, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	if _, err := walletRecharge(ctx, db, 11, walletPlayerCatfood, player.ID,
		walletInput{PrincipalCents: 1, Note: "猫粮充值"}, "wallet-display-2"); err != nil {
		t.Fatal(err)
	}
	if view = walletAccountViewOf(t, db, walletPlayerCatfood); view.Display.DisplayName != "猫粮" {
		t.Fatalf("未配置账户类型未回落默认：%+v", view.Display)
	}

	// 部分字段配置：只覆盖展示名，其余回落默认。
	db.SystemConfig.Update().Where(systemconfig.KeyEQ(walletDisplayConfigKey)).
		SetValue(`{"customer_balance":{"display_name":"余额宝"}}`).ExecX(ctx)
	view = walletAccountViewOf(t, db, walletCustomerBalance)
	if view.Display.DisplayName != "余额宝" || view.Display.UnitCents != 100 ||
		view.Display.DisplayAmount != "20.50" || view.Display.UnitName != "元" {
		t.Fatalf("部分展示配置未按字段回落：%+v", view.Display)
	}

	// 非法 JSON 与非法倍率都回落默认。
	db.SystemConfig.Update().Where(systemconfig.KeyEQ(walletDisplayConfigKey)).
		SetValue(`{not json`).ExecX(ctx)
	if view = walletAccountViewOf(t, db, walletCustomerBalance); view.Display.DisplayName != "余额" {
		t.Fatalf("非法 JSON 未回落默认：%+v", view.Display)
	}
	db.SystemConfig.Update().Where(systemconfig.KeyEQ(walletDisplayConfigKey)).
		SetValue(`{"customer_balance":{"display_name":"零倍率","unit_cents":0}}`).ExecX(ctx)
	view = walletAccountViewOf(t, db, walletCustomerBalance)
	if view.Display.DisplayName != "零倍率" || view.Display.UnitCents != 100 ||
		view.Display.DisplayAmount != "20.50" {
		t.Fatalf("非法倍率未回落默认：%+v", view.Display)
	}
}

// 固定倍率换算的整数运算边界：非整除倍率向下取整到分位。
func TestWalletDisplayAmountRounding(t *testing.T) {
	for _, tc := range []struct {
		cents, unit int64
		want        string
	}{
		{2050, 100, "20.50"},
		{2050, 50, "41.00"},
		{1, 100, "0.01"},
		{0, 100, "0.00"},
		{-150, 100, "-1.50"},
		{100, 30, "3.33"},
	} {
		if got := walletDisplayAmount(tc.cents, tc.unit); got != tc.want {
			t.Fatalf("换算 %d 分 / %d 分每单位 = %s，期望 %s", tc.cents, tc.unit, got, tc.want)
		}
	}
	if got := walletDisplayAmount(100, 0); got != "1.00" {
		t.Fatalf("非法倍率未回落 100：%s", got)
	}
}

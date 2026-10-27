package esports

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayerpayoutaccount"
	"go-admin/internal/ent/esportswithdrawal"
)

// setPayoutYunzhanghuConfig 写入云账户代发配置 JSON。
func setPayoutYunzhanghuConfig(t *testing.T, db *ent.Client, cfg yunzhanghuPayoutConfig) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	setWithdrawalConfig(t, db, "app.esports.payout_provider_yunzhanghu", string(raw))
}

// payoutAccountOfPlayer 读取某陪玩指定账户类型的签约记录。
func payoutAccountOfPlayer(t *testing.T, db *ent.Client, playerID int, accountType string) *ent.EsportsPlayerPayoutAccount {
	t.Helper()
	item, err := db.EsportsPlayerPayoutAccount.Query().Where(
		esportsplayerpayoutaccount.PlayerIDEQ(playerID),
		esportsplayerpayoutaccount.ProviderEQ(payoutProviderYunzhanghu),
		esportsplayerpayoutaccount.AccountTypeEQ(esportsplayerpayoutaccount.AccountType(accountType)),
	).Only(t.Context())
	if err != nil {
		t.Fatalf("读取签约账户失败：%v", err)
	}
	return item
}

// 未开启云账户代发时不得签发签约地址，也不应落下 pending 记录。
func TestPayoutAccountSignURLRequiresYunzhanghuConfig(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		cfg      yunzhanghuPayoutConfig
		wantMsg  string
	}{
		{"人工打款渠道", "manual", yunzhanghuPayoutConfig{Enabled: true, SignBase: "https://sign.example.com/sign"}, "云账户代发"},
		{"云账户未启用", "yunzhanghu", yunzhanghuPayoutConfig{Enabled: false, SignBase: "https://sign.example.com/sign"}, "未开启"},
		{"签约地址为空", "yunzhanghu", yunzhanghuPayoutConfig{Enabled: true}, "签约地址"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openWithdrawalDB(t, fmt.Sprintf("payout_sign_cfg_%d", i))
			defer db.Close()
			ctx := t.Context()
			player := seedCatfoodPlayer(t, db, 9301, 1000)
			setWithdrawalConfig(t, db, "app.esports.payout_provider", tc.provider)
			setPayoutYunzhanghuConfig(t, db, tc.cfg)
			if _, _, err := playerPayoutSignURL(ctx, db, player.ID, "bank_card"); !errors.Is(err, errInvalidInput) ||
				!strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("%s：应返回明确中文错误，got %v", tc.name, err)
			}
			if n := db.EsportsPlayerPayoutAccount.Query().CountX(ctx); n != 0 {
				t.Fatalf("%s：被拒的签约不应落 pending 记录：%d", tc.name, n)
			}
		})
	}
}

// 签约地址拼装、重复取址复用记录、激活回填、解约清理。
func TestPayoutAccountSignActivateAndUnbind(t *testing.T) {
	db := openWithdrawalDB(t, "payout_account_lifecycle")
	defer db.Close()
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9311, 1000)
	setWithdrawalConfig(t, db, "app.esports.payout_provider", "yunzhanghu")
	setPayoutYunzhanghuConfig(t, db, yunzhanghuPayoutConfig{
		Enabled: true, DealerID: "D1", NotifyBase: "https://h5.example.com",
		SignBase: "https://sign.example.com/contract",
	})

	account, link, err := playerPayoutSignURL(ctx, db, player.ID, "bank_card")
	if err != nil {
		t.Fatalf("已开启云账户代发应能取签约地址：%v", err)
	}
	if account.Status != esportsplayerpayoutaccount.StatusPending {
		t.Fatalf("首次签约应先落 pending：%+v", account)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("签约地址不是合法 URL：%v", err)
	}
	query := parsed.Query()
	if query.Get("dealer_id") != "D1" || query.Get("player_id") != fmt.Sprint(player.ID) ||
		query.Get("account_type") != "bank_card" ||
		query.Get("return_url") != "https://h5.example.com/player/wallet" {
		t.Fatalf("签约地址参数拼接不正确：%s", link)
	}
	// 重复取签约地址复用同一账户，不新增记录。
	if _, _, err := playerPayoutSignURL(ctx, db, player.ID, "bank_card"); err != nil {
		t.Fatalf("重复取签约地址应成功：%v", err)
	}
	if n := db.EsportsPlayerPayoutAccount.Query().CountX(ctx); n != 1 {
		t.Fatalf("重复签约地址应复用同一账户：%d", n)
	}

	// 回填签约号：完整卡号只落后四位。
	if err := activatePlayerPayoutAccount(ctx, db, player.ID, "bank_card", "SIGN-001", "张三", "6222020202021234"); err != nil {
		t.Fatalf("激活签约账户应成功：%v", err)
	}
	got := payoutAccountOfPlayer(t, db, player.ID, "bank_card")
	if got.Status != esportsplayerpayoutaccount.StatusActive || got.ProviderSignNo != "SIGN-001" ||
		got.AccountNoMasked != "****1234" || got.SignedAt == nil {
		t.Fatalf("激活后账户信息不正确：%+v", got)
	}

	// 解约：状态 disabled，签约号与脱敏账号清空，户名与签约时间保留用于审计。
	if err := unbindPlayerPayoutAccount(ctx, db, player.ID, "bank_card"); err != nil {
		t.Fatalf("解约应成功：%v", err)
	}
	got = payoutAccountOfPlayer(t, db, player.ID, "bank_card")
	if got.Status != esportsplayerpayoutaccount.StatusDisabled || got.ProviderSignNo != "" ||
		got.AccountNoMasked != "" || got.AccountName != "张三" || got.SignedAt == nil {
		t.Fatalf("解约后账户信息不正确：%+v", got)
	}
}

// 脱敏函数：完整卡号只留后四位，短串与非数字内容原样保留。
func TestMaskPayoutAccountNo(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"完整银行卡号只留后四位", "6222020202021234", "****1234"},
		{"短账号原样返回", "1234", "1234"},
		{"无数字内容原样返回", "account-name", "account-name"},
		{"空串", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := maskPayoutAccountNo(tc.in); got != tc.want {
				t.Fatalf("掩码结果不正确：in=%q got=%q want=%q", tc.in, got, tc.want)
			}
		})
	}
}

// 云账户代发未签约时提现被拒且不扣猫粮；人工打款模式不受影响。
func TestWithdrawalRequiresSignedPayoutAccountUnderYunzhanghu(t *testing.T) {
	db := openWithdrawalDB(t, "payout_account_withdrawal")
	defer db.Close()
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9321, 10000)
	now := time.Now()

	// 默认 manual 模式：没有签约账户也能提现，行为与改动前一致。
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("payout-manual-0001", 1000), now); err != nil {
		t.Fatalf("人工打款模式不应受签约限制：%v", err)
	}

	setWithdrawalConfig(t, db, "app.esports.payout_provider", "yunzhanghu")
	setPayoutYunzhanghuConfig(t, db, yunzhanghuPayoutConfig{
		Enabled: true, DealerID: "D1", SignBase: "https://sign.example.com/contract",
	})
	before, err := walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("payout-yzh-0001", 1000), now); !errors.Is(err, errInvalidInput) ||
		!strings.Contains(err.Error(), "请先完成收款账户签约") {
		t.Fatalf("未签约时提现应被拒：%v", err)
	}
	after, err := walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.total() != before.total() {
		t.Fatalf("被拒的提现不得扣减猫粮：before=%d after=%d", before.total(), after.total())
	}
	if n := db.EsportsWithdrawal.Query().Where(
		esportswithdrawal.PlayerIDEQ(player.ID),
	).CountX(ctx); n != 1 {
		t.Fatalf("被拒的提现不应落单：%d", n)
	}

	// pending 尚未生效，仍应被拒。
	if _, _, err := playerPayoutSignURL(ctx, db, player.ID, "bank_card"); err != nil {
		t.Fatalf("取签约地址失败：%v", err)
	}
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("payout-yzh-0002", 1000), now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("pending 账户不得用于提现：%v", err)
	}

	// 签约生效后可提现，并在提现单上留签约账户快照。
	if err := activatePlayerPayoutAccount(ctx, db, player.ID, "bank_card", "SIGN-002", "张三", "****1234"); err != nil {
		t.Fatal(err)
	}
	item, err := applyWithdrawal(ctx, db, player.ID, validApply("payout-yzh-0003", 1000), now)
	if err != nil {
		t.Fatalf("已签约后提现应成功：%v", err)
	}
	account := payoutAccountOfPlayer(t, db, player.ID, "bank_card")
	if item.PayoutAccountID == nil || *item.PayoutAccountID != account.ID {
		t.Fatalf("提现单未记录签约账户快照：%+v", item)
	}

	// 解约后再提现被拒。
	if err := unbindPlayerPayoutAccount(ctx, db, player.ID, "bank_card"); err != nil {
		t.Fatal(err)
	}
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("payout-yzh-0004", 1000), now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("解约后提现应被拒：%v", err)
	}

	// 切回人工打款后恢复原有行为。
	setWithdrawalConfig(t, db, "app.esports.payout_provider", "manual")
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("payout-yzh-0005", 1000), now); err != nil {
		t.Fatalf("人工打款模式不应受签约限制：%v", err)
	}
}

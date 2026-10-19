package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// 提现方式按工作室能力开通（§6.3-05）：渠道开关关闭时申请必须被拒且不动猫粮；
// 开通后可申请；从未配置开关的渠道保持既有行为（不阻断存量部署）；白名单外渠道仍被拒。
func TestWithdrawalChannelRequiresStudioCapability(t *testing.T) {
	db := openWithdrawalDB(t, "r4wm_withdrawal_channel")
	defer db.Close()
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9201, 10000)
	now := time.Now()

	setWithdrawalConfig(t, db, "app.esports.withdrawal_channel.wechat", "disabled")
	blocked := validApply("r4wm-wd-channel-0001", 1000)
	blocked.Channel = "wechat"
	blocked.AccountRef = "wx-openid-r4wm"
	if _, err := applyWithdrawal(ctx, db, player.ID, blocked, now); !errors.Is(err, errInvalidInput) ||
		!strings.Contains(err.Error(), "未开通") {
		t.Fatalf("未开通渠道的提现申请应被拒：%v", err)
	}
	if count := db.EsportsWithdrawal.Query().CountX(ctx); count != 0 {
		t.Fatalf("被拒的申请不应落单：%d", count)
	}
	balance, err := walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID)
	if err != nil {
		t.Fatalf("读取猫粮余额失败：%v", err)
	}
	if balance.total() != 10000 {
		t.Fatalf("被拒的申请不应扣减猫粮：%d", balance.total())
	}

	// 开通后可申请。
	setWithdrawalConfig(t, db, "app.esports.withdrawal_channel.wechat", "enabled")
	allowed := validApply("r4wm-wd-channel-0002", 1000)
	allowed.Channel = "wechat"
	allowed.AccountRef = "wx-openid-r4wm"
	item, err := applyWithdrawal(ctx, db, player.ID, allowed, now)
	if err != nil {
		t.Fatalf("已开通渠道的申请应成功：%v", err)
	}
	if item.Channel.String() != "wechat" {
		t.Fatalf("提现渠道落库不正确：%+v", item)
	}

	// 未配置开关的渠道（bank_card）保持既有行为：默认开通，不阻断存量部署。
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("r4wm-wd-channel-0003", 1000), now); err != nil {
		t.Fatalf("未配置开关的渠道应保持默认开通：%v", err)
	}

	// 三渠道白名单之外的渠道仍被拒。
	unknown := validApply("r4wm-wd-channel-0004", 1000)
	unknown.Channel = "usdt"
	if _, err := applyWithdrawal(ctx, db, player.ID, unknown, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("白名单外渠道应被拒：%v", err)
	}
}

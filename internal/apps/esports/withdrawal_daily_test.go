package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// 每日提现次数上限（§6.3）：当日达到上限后再申请被拒，次日自然日重置。
func TestWithdrawalDailyLimit(t *testing.T) {
	db := openWithdrawalDB(t, "esports_withdrawal_daily_limit")
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9006, 100000)
	now := time.Now()

	setWithdrawalConfig(t, db, withdrawalDailyLimitKey, "1")
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-daily-0001", 1000), now); err != nil {
		t.Fatalf("首次提现应成功，err=%v", err)
	}
	// 同日第二次：超过每日上限。
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-daily-0002", 1000), now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("同日超过每日上限应被拒，err=%v", err)
	}

	// 次日（UTC+8 自然日重置）可再次申请。
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-daily-0003", 1000),
		now.Add(24*time.Hour)); err != nil {
		t.Fatalf("次日应可再次提现，err=%v", err)
	}

	// 上限设回 0（不限）后，同日可多次申请。
	setWithdrawalConfig(t, db, withdrawalDailyLimitKey, "0")
	day3 := now.Add(48 * time.Hour)
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-daily-0004", 1000), day3); err != nil {
		t.Fatalf("不限次数时应可申请，err=%v", err)
	}
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-daily-0005", 1000), day3); err != nil {
		t.Fatalf("不限次数时同日再次申请应可，err=%v", err)
	}
}

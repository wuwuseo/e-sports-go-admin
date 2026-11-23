package esports

import (
	"regexp"
	"testing"
	"time"
)

// 订单号格式：前缀 + yyyyMMddHHmmss（UTC+8）+ 客户ID + 8 位随机数字。
func TestNewOrderNoFormat(t *testing.T) {
	db := openWalletTestDB(t, "esports_order_no_format")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 11, 23, 19, 12, 34, 0, cstZone)

	no := newOrderNo(ctx, db, 1234, now)
	if !regexp.MustCompile(`^ES202611231912341234\d{8}$`).MatchString(no) {
		t.Fatalf("订单号格式不符合预期：%s", no)
	}

	// UTC 时间必须换算到业务时区再格式化，否则会比实际下单时间早 8 小时。
	utc := time.Date(2026, 11, 23, 11, 12, 34, 0, time.UTC)
	if got := newOrderNo(ctx, db, 7, utc); got[:17] != "ES202611231912347" {
		t.Fatalf("UTC 时间未按 UTC+8 格式化：%s", got)
	}
}

// 前缀可配置：只保留大写字母与数字，超长截断，空值回落默认。
func TestNewOrderNoPrefixFromConfig(t *testing.T) {
	db := openWalletTestDB(t, "esports_order_no_prefix")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 11, 23, 19, 12, 34, 0, cstZone)

	setPrefix := func(value string) int {
		t.Helper()
		return db.SystemConfig.Create().
			SetKey(orderNoPrefixConfigKey).
			SetName("订单号前缀").
			SetValue(value).
			SetType("string").
			SetGroup("esports").
			SetStatus(1).
			SaveX(ctx).ID
	}

	id := setPrefix("  ab-12!c9  ")
	if got := newOrderNo(ctx, db, 1, now); got[:8] != "AB12C920" {
		t.Fatalf("前缀未按规则规整：%s", got)
	}

	db.SystemConfig.UpdateOneID(id).SetValue("WORLDTOOLONG").ExecX(ctx)
	if got := newOrderNo(ctx, db, 1, now); got[:8] != "WORLDTOO" {
		t.Fatalf("前缀未按 8 位截断：%s", got)
	}

	db.SystemConfig.UpdateOneID(id).SetValue("///").ExecX(ctx)
	if got := newOrderNo(ctx, db, 1, now); got[:2] != "ES" {
		t.Fatalf("非法前缀应回落默认值：%s", got)
	}
}

// 同一客户同一秒内生成的订单号也必须互不相同（随机段兜底）。
func TestNewOrderNoUniqueWithinSameSecond(t *testing.T) {
	db := openWalletTestDB(t, "esports_order_no_unique")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 11, 23, 19, 12, 34, 0, cstZone)

	seen := make(map[string]struct{}, 500)
	for i := 0; i < 500; i++ {
		no := newOrderNo(ctx, db, 42, now)
		if _, dup := seen[no]; dup {
			t.Fatalf("同一秒内出现重复订单号：%s", no)
		}
		seen[no] = struct{}{}
	}
}

package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/migrate"
)

func openGiftDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// seedGiftCustomer 建一个绑定已验证身份（user_id）的成年客户。
func seedGiftCustomer(t *testing.T, db *ent.Client, userID int) *ent.EsportsCustomer {
	t.Helper()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	customer = db.EsportsCustomer.UpdateOneID(customer.ID).SetUserID(userID).SaveX(ctx)
	return customer
}

// seedGift 建一个上架礼物。
func seedGift(t *testing.T, db *ent.Client, in giftInput) *ent.EsportsGift {
	t.Helper()
	item, err := createGift(t.Context(), db, in, "on_sale")
	if err != nil {
		t.Fatalf("创建礼物失败：%v", err)
	}
	return item
}

// 礼物分成按礼物自身配置计算：percent 用万分比、fixed 取固定金额，且不超过礼物金额。
func TestGiftIncome(t *testing.T) {
	cases := []struct {
		amount int64
		mode   string
		bp     int
		cents  int64
		want   int64
	}{
		{10000, "percent", 6000, 0, 6000}, // 六成
		{10000, "fixed", 0, 3000, 3000},   // 固定 30 元
		{10000, "fixed", 0, 20000, 10000}, // 固定超过金额被夹到金额上限
		{10000, "percent", 0, 0, 0},       // 未配置比例
		{0, "percent", 6000, 0, 0},        // 零金额
		{12345, "percent", 6000, 0, 7407}, // 非整除，向下取整
	}
	for _, tc := range cases {
		if got := giftIncome(tc.amount, tc.mode, tc.bp, tc.cents); got != tc.want {
			t.Fatalf("礼物收入计算错误（%d, %s）：得到 %d，期望 %d", tc.amount, tc.mode, got, tc.want)
		}
	}
}

// 送礼必须原子扣减本金余额、生成礼物收入与成长值，且重复提交幂等。
func TestSendGiftDeductsAndAccrues(t *testing.T) {
	db := openGiftDB(t, "esports_gift_send")
	defer db.Close()
	ctx := t.Context()
	customer := seedGiftCustomer(t, db, 101)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 20000, Note: "测试充值"}, "recharge-key-1"); err != nil {
		t.Fatal(err)
	}
	player := seedPlayer(t, db, 202, "qualified", "online")
	gift := seedGift(t, db, giftInput{
		Code: "GIFT-ROCKET", Name: "火箭", PriceCents: 10000,
		CommissionMode: "percent", CommissionBp: 6000,
	})
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	in := giftSendInput{CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, ClientKey: "gift-key-0001"}
	order, err := sendGift(ctx, db, 11, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if order.AmountCents != 10000 || order.IncomeCents != 6000 || order.PrincipalCents != 10000 ||
		order.BonusCents != 0 || order.PlayerName != player.DisplayName || order.ReviewStatus != "normal" {
		t.Fatalf("礼物单据快照错误：%+v", order)
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.PrincipalCents != 10000 || balance.BonusCents != 0 {
		t.Fatalf("余额未按本金扣减：%+v", balance)
	}
	if db.EsportsCommissionEntry.Query().Where(esportscommissionentry.KindEQ(esportscommissionentry.KindGift)).CountX(ctx) != 1 {
		t.Fatal("未生成礼物分成明细")
	}
	if growth, err := customerGrowthTotal(ctx, db, customer.ID); err != nil || growth != 10000 {
		t.Fatalf("成长值未按礼物金额计提：%d, %v", growth, err)
	}
	if db.EsportsOutbox.Query().Where(esportsoutbox.EventTypeEQ("gift.paid")).CountX(ctx) != 1 {
		t.Fatal("未写通知事件")
	}
	again, err := sendGift(ctx, db, 11, in, now)
	if err != nil || again.ID != order.ID {
		t.Fatalf("幂等重试生成了不同礼物单据：%+v, %v", again, err)
	}
	if db.EsportsGiftOrder.Query().CountX(ctx) != 1 {
		t.Fatal("幂等重试重复落库")
	}
}

// 送礼的各项边界：本人送礼、未二次确认的大额、未成年、停售礼物、未验证身份与赠送权益支付均被拦截。
func TestSendGiftGuards(t *testing.T) {
	db := openGiftDB(t, "esports_gift_guards")
	defer db.Close()
	ctx := t.Context()
	customer := seedGiftCustomer(t, db, 101)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 500000, Note: "测试充值"}, "recharge-key-2"); err != nil {
		t.Fatal(err)
	}
	selfPlayer := seedPlayer(t, db, 101, "qualified", "online") // 与客户同一已验证身份
	otherPlayer := seedPlayer(t, db, 202, "qualified", "online")
	gift := seedGift(t, db, giftInput{Code: "GIFT-A", Name: "礼物A", PriceCents: 10000, CommissionMode: "percent", CommissionBp: 6000})
	highGift := seedGift(t, db, giftInput{Code: "GIFT-B", Name: "礼物B", PriceCents: 300000, CommissionMode: "percent", CommissionBp: 6000, HighValue: true})
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	base := giftSendInput{CustomerID: customer.ID, GiftID: gift.ID, PlayerID: otherPlayer.ID, Quantity: 1, ClientKey: "guard-key-001"}

	selfIn := base
	selfIn.PlayerID = selfPlayer.ID
	if _, err := sendGift(ctx, db, 11, selfIn, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("本人送礼未被拦截：%v", err)
	}
	highIn := base
	highIn.GiftID = highGift.ID
	if _, err := sendGift(ctx, db, 11, highIn, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("高额礼物未二次确认未被拦截：%v", err)
	}
	confirmedIn := highIn
	confirmedIn.Confirmed = true
	confirmedIn.ClientKey = "guard-key-002"
	if _, err := sendGift(ctx, db, 11, confirmedIn, now); err != nil {
		t.Fatalf("二次确认后的高额礼物被误拦：%v", err)
	}
	// 未绑定身份的客户不可送礼。
	raw := saveCustomerOrFail(t, db, 12)
	noID := base
	noID.CustomerID = raw.ID
	noID.ClientKey = "guard-key-003"
	if _, err := sendGift(ctx, db, 11, noID, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未验证身份客户送礼未被拦截：%v", err)
	}
	// 未成年客户不可送礼。
	minor := seedGiftCustomer(t, db, 303)
	db.EsportsCustomer.UpdateOneID(minor.ID).SetAgeStatus("minor").SaveX(ctx)
	minorIn := base
	minorIn.CustomerID = minor.ID
	minorIn.ClientKey = "guard-key-004"
	if _, err := sendGift(ctx, db, 11, minorIn, now); !errors.Is(err, errAgeUnverified) {
		t.Fatalf("未成年客户送礼未被拦截：%v", err)
	}
	// 停售礼物不可送。
	offGift := seedGift(t, db, giftInput{Code: "GIFT-C", Name: "礼物C", PriceCents: 10000, CommissionMode: "percent", CommissionBp: 6000})
	if _, err := setGiftStatus(ctx, db, offGift.ID, offGift.Version, "off_sale"); err != nil {
		t.Fatal(err)
	}
	offIn := base
	offIn.GiftID = offGift.ID
	offIn.ClientKey = "guard-key-005"
	if _, err := sendGift(ctx, db, 11, offIn, now); !errors.Is(err, errConflict) {
		t.Fatalf("停售礼物送礼未被拦截：%v", err)
	}
	// 赠送权益不可用于送礼：只有赠送、没有本金的客户被拒。
	poor := seedGiftCustomer(t, db, 404)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, poor.ID,
		walletInput{BonusCents: 50000, Note: "赠送"}, "recharge-key-3"); err != nil {
		t.Fatal(err)
	}
	poorIn := base
	poorIn.CustomerID = poor.ID
	poorIn.ClientKey = "guard-key-006"
	if _, err := sendGift(ctx, db, 11, poorIn, now); !errors.Is(err, errInsufficientBalance) {
		t.Fatalf("赠送权益余额送礼未被拦截：%v", err)
	}
}

// 防刷榜标记、复核释放分成、礼物退款冲回分成与成长值。
func TestGiftFlagReviewRefundAndLeaderboard(t *testing.T) {
	db := openGiftDB(t, "esports_gift_flow")
	defer db.Close()
	ctx := t.Context()
	customer := seedGiftCustomer(t, db, 101)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 1000000, Note: "测试充值"}, "recharge-key-4"); err != nil {
		t.Fatal(err)
	}
	player := seedPlayer(t, db, 202, "qualified", "online")
	gift := seedGift(t, db, giftInput{Code: "GIFT-D", Name: "礼物D", PriceCents: 10000, CommissionMode: "percent", CommissionBp: 6000})
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	var orders []*ent.EsportsGiftOrder
	for i := 0; i < giftFlagPairLimit+1; i++ {
		in := giftSendInput{CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1,
			ClientKey: "flow-key-" + string(rune('a'+i)) + "0000"}
		order, err := sendGift(ctx, db, 11, in, now)
		if err != nil {
			t.Fatalf("第 %d 笔送礼失败：%v", i+1, err)
		}
		orders = append(orders, order)
	}
	// 前 giftFlagPairLimit 笔正常，第 limit+1 笔被标记待复核且不生成分成。
	flagged := orders[len(orders)-1]
	if flagged.ReviewStatus != "flagged" {
		t.Fatalf("异常集中的礼物未被标记：%+v", flagged.ReviewStatus)
	}
	if got := db.EsportsCommissionEntry.Query().Where(esportscommissionentry.KindEQ(esportscommissionentry.KindGift)).CountX(ctx); got != giftFlagPairLimit {
		t.Fatalf("待复核礼物不应生成分成，已有 %d 条", got)
	}
	// 复核通过后释放分成。
	reviewed, err := reviewGiftOrder(ctx, db, 11, flagged.ID, "approve", now)
	if err != nil || reviewed.ReviewStatus != "approved" {
		t.Fatalf("复核释放失败：%+v, %v", reviewed, err)
	}
	if got := db.EsportsCommissionEntry.Query().Where(esportscommissionentry.KindEQ(esportscommissionentry.KindGift)).CountX(ctx); got != giftFlagPairLimit+1 {
		t.Fatalf("复核通过未释放分成，当前 %d 条", got)
	}
	if _, err := reviewGiftOrder(ctx, db, 11, flagged.ID, "approve", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复复核未被拦截：%v", err)
	}

	// 退款：退还本金、冲回分成与成长值。
	target := orders[0]
	refunded, err := refundGift(ctx, db, 11, target.ID, "客户申请退款", now)
	if err != nil || refunded.Status != "refunded" || refunded.RefundCents != 10000 {
		t.Fatalf("礼物退款失败：%+v, %v", refunded, err)
	}
	if got := db.EsportsCommissionEntry.Query().Where(esportscommissionentry.KindEQ(esportscommissionentry.KindGiftReversal)).CountX(ctx); got != 1 {
		t.Fatalf("退款未冲回礼物分成：%d", got)
	}
	reversal := db.EsportsCommissionEntry.Query().Where(esportscommissionentry.KindEQ(esportscommissionentry.KindGiftReversal)).OnlyX(ctx)
	if reversal.AmountCents != -6000 {
		t.Fatalf("冲回金额错误：%d", reversal.AmountCents)
	}
	growthTotal := int64(giftFlagPairLimit+1) * 10000
	if growth, err := customerGrowthTotal(ctx, db, customer.ID); err != nil || growth != growthTotal-10000 {
		t.Fatalf("成长值未按退款冲回：%d, %v", growth, err)
	}
	if _, err := refundGift(ctx, db, 11, target.ID, "重复退款", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复退款未被拦截：%v", err)
	}

	// 榜单：陪玩收礼榜排除已全额退款的那笔。
	rows, err := giftLeaderboard(ctx, db, "player", "daily", now, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("榜单查询失败：%+v, %v", rows, err)
	}
	if rows[0].OwnerID != player.ID || rows[0].AmountCents != int64(giftFlagPairLimit)*10000 || rows[0].Count != giftFlagPairLimit {
		t.Fatalf("榜单汇总错误：%+v", rows[0])
	}
	// 成长值流水来源包含礼物。
	if db.EsportsGrowthLedger.Query().Where(esportsgrowthledger.SourceTypeEQ(esportsgrowthledger.SourceTypeGift)).CountX(ctx) != giftFlagPairLimit+2 {
		t.Fatal("礼物成长值流水条数不符")
	}
}

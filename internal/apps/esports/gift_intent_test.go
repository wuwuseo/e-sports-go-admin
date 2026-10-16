package esports

import (
	"errors"
	"testing"
	"time"

	"go-admin/internal/ent/esportsgiftintent"
	"go-admin/internal/ent/esportsidentitybinding"
)

func TestGiftIntentRequiresCustomerConfirmationAndConsumesOnce(t *testing.T) {
	db := openGiftDB(t, "esports_gift_intent")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	account := db.User.Create().SetEmail("gift-intent@example.com").SetUID("gift-intent-user").SetStatus("active").SaveX(ctx)
	other := db.User.Create().SetEmail("gift-other@example.com").SetUID("gift-other-user").SetStatus("active").SaveX(ctx)
	_ = other
	customer := seedGiftCustomer(t, db, account.ID)
	db.EsportsIdentityBinding.Create().SetCustomerID(customer.ID).SetProvider("user").SetSubjectScope("").
		SetExternalSubject(account.UID).SetVerificationMethod("verified_account_link").SetVerifiedAt(now).SaveX(ctx)
	player := seedPlayer(t, db, account.ID+100, "qualified", "online")
	gift := seedGift(t, db, giftInput{Code: "CONFIRM-GIFT", Name: "火箭", PriceCents: 10000, CommissionMode: "percent", CommissionBp: 6000, HighValue: true})
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 20000, Note: "测试充值"}, "gift-intent-recharge"); err != nil {
		t.Fatal(err)
	}
	in := giftIntentInput{CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, ClientKey: "intent-key-001"}
	intent, err := createGiftIntent(ctx, db, 11, false, in, now)
	if err != nil || intent.Status != esportsgiftintent.StatusPending || intent.AmountCents != 10000 {
		t.Fatalf("创建确认单失败：%+v %v", intent, err)
	}
	if _, err := executeGiftIntent(ctx, db, 11, false, intent.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("客服未经客户确认即扣款：%v", err)
	}
	if balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID); err != nil || balance.PrincipalCents != 20000 {
		t.Fatalf("确认前余额变化：%+v %v", balance, err)
	}
	if _, err := confirmGiftIntent(ctx, db, "gift-other-user", intent.ID, 1, true, now); !errors.Is(err, errForbidden) {
		t.Fatalf("他人确认成功：%v", err)
	}
	if _, err := confirmGiftIntent(ctx, db, account.UID, intent.ID, 1, false, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未明确接受却确认：%v", err)
	}
	confirmed, err := confirmGiftIntent(ctx, db, account.UID, intent.ID, 1, true, now)
	if err != nil || confirmed.Status != esportsgiftintent.StatusConfirmed || confirmed.ConfirmationRef == "" {
		t.Fatalf("客户确认失败：%+v %v", confirmed, err)
	}
	if _, err := executeGiftIntent(ctx, db, 12, false, intent.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("其他客服执行成功：%v", err)
	}
	order, err := executeGiftIntent(ctx, db, 11, false, intent.ID, now)
	if err != nil || order.GiftIntentID == nil || *order.GiftIntentID != intent.ID {
		t.Fatalf("确认后未送礼：%+v %v", order, err)
	}
	again, err := executeGiftIntent(ctx, db, 11, false, intent.ID, now)
	if err != nil || again.ID != order.ID {
		t.Fatalf("重复执行未幂等：%+v %v", again, err)
	}
	if balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID); err != nil || balance.PrincipalCents != 10000 {
		t.Fatalf("余额扣款次数错误：%+v %v", balance, err)
	}
	if db.EsportsGiftOrder.Query().CountX(ctx) != 1 {
		t.Fatal("生成重复送礼单")
	}
	next, err := createGiftIntent(ctx, db, 11, false, giftIntentInput{CustomerID: customer.ID,
		GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, ClientKey: "intent-key-revoked"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := confirmGiftIntent(ctx, db, account.UID, next.ID, 1, true, now); err != nil {
		t.Fatal(err)
	}
	db.EsportsIdentityBinding.Update().Where(esportsidentitybinding.CustomerIDEQ(customer.ID)).
		SetStatus(esportsidentitybinding.StatusRevoked).ExecX(ctx)
	if _, err := executeGiftIntent(ctx, db, 11, false, next.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("身份绑定撤销后仍执行送礼：%v", err)
	}
}

func TestGiftIntentRejectsExpiredOrChangedGift(t *testing.T) {
	db := openGiftDB(t, "esports_gift_intent_stale")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	account := db.User.Create().SetEmail("stale-gift@example.com").SetUID("stale-gift-user").SetStatus("active").SaveX(ctx)
	customer := seedGiftCustomer(t, db, account.ID)
	db.EsportsIdentityBinding.Create().SetCustomerID(customer.ID).SetProvider("user").SetSubjectScope("").
		SetExternalSubject(account.UID).SetVerificationMethod("verified_account_link").SetVerifiedAt(now).SaveX(ctx)
	player := seedPlayer(t, db, account.ID+100, "qualified", "online")
	gift := seedGift(t, db, giftInput{Code: "STALE-GIFT", Name: "礼物", PriceCents: 1000, CommissionMode: "percent", CommissionBp: 5000})
	first, err := createGiftIntent(ctx, db, 11, false, giftIntentInput{CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, ClientKey: "stale-intent-1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := confirmGiftIntent(ctx, db, account.UID, first.ID, 1, true, now.Add(31*time.Minute)); !errors.Is(err, errConflict) {
		t.Fatalf("过期确认成功：%v", err)
	}
	second, err := createGiftIntent(ctx, db, 11, false, giftIntentInput{CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, ClientKey: "stale-intent-2"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := confirmGiftIntent(ctx, db, account.UID, second.ID, 1, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := updateGift(ctx, db, gift.ID, gift.Version, giftInput{Code: gift.Code, Name: "新礼物", PriceCents: 2000, CommissionMode: "percent", CommissionBp: 5000}); err != nil {
		t.Fatal(err)
	}
	if _, err := executeGiftIntent(ctx, db, 11, false, second.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("客户未确认的新金额执行成功：%v", err)
	}
}

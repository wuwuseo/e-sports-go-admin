package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"go-admin/internal/ent/esportsledgerallocation"
	"go-admin/internal/ent/esportsstoredvaluecard"
)

func TestGiftStoredValuePrincipalAndOriginalRefund(t *testing.T) {
	db := openGiftDB(t, "esports_gift_stored_value")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	account := db.User.Create().SetEmail("gift-card@example.com").SetUID("gift-card-user").SetStatus("active").SaveX(ctx)
	customer := seedGiftCustomer(t, db, account.ID)
	db.EsportsIdentityBinding.Create().SetCustomerID(customer.ID).SetProvider("user").SetSubjectScope("").
		SetExternalSubject(account.UID).SetVerificationMethod("verified_account_link").SetVerifiedAt(now).SaveX(ctx)
	player := seedPlayer(t, db, account.ID+100, "qualified", "online")
	gift := seedGift(t, db, giftInput{Code: "CARD-GIFT", Name: "卡礼物", PriceCents: 8000,
		CommissionMode: "percent", CommissionBp: 5000})
	card := db.EsportsStoredValueCard.Create().SetCardNo("SV-GIFT-1").SetCustomerID(customer.ID).
		SetPaymentChannel(esportsstoredvaluecard.PaymentChannelMerchantQr).
		SetPrincipalCents(10000).SetBonusCents(5000).SetAllowGifts(true).SetContractConfirmed(true).
		SetStatus(esportsstoredvaluecard.StatusActive).SetValidFrom(now.Add(-time.Hour)).
		SetValidUntil(now.AddDate(0, 1, 0)).SaveX(ctx)
	db.EsportsWalletTx.Create().SetTxNo("gift-card-seed").SetAccountType("stored_value_card").
		SetOwnerID(card.ID).SetEntryType("recharge").SetPrincipalDeltaCents(10000).SetBonusDeltaCents(5000).
		SetSourceType("stored_value_card_open").SetSourceID("gift-card-open").SaveX(ctx)
	db.EsportsLedgerEntry.Create().SetAccountType(storedValuePrepayAccount).SetAccountID(strconv.Itoa(card.ID)).
		SetSourceType("stored_value_card_open").SetSourceID("gift-card-open").SetEntryRole("card_open").
		SetDeltaCents(15000).SaveX(ctx)
	intent, err := createGiftIntent(ctx, db, 11, false, giftIntentInput{CustomerID: customer.ID,
		GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, PaymentSource: "stored_value_card_principal",
		StoredValueCardID: card.ID, ClientKey: "gift-card-intent-1"}, now)
	if err != nil || intent.StoredValueCardID == nil || *intent.StoredValueCardID != card.ID || intent.StoredValueCardNo != card.CardNo {
		t.Fatalf("卡送礼确认单不正确：%+v %v", intent, err)
	}
	if _, err := confirmGiftIntent(ctx, db, account.UID, intent.ID, intent.Version, true, now); err != nil {
		t.Fatal(err)
	}
	order, err := executeGiftIntent(ctx, db, 11, false, intent.ID, now)
	if err != nil || order.StoredValueCardID == nil || *order.StoredValueCardID != card.ID || order.BonusCents != 0 {
		t.Fatalf("卡送礼未正确核销：%+v %v", order, err)
	}
	bal, err := storedValueCardBalance(ctx, db, card.ID)
	if err != nil || bal.PrincipalCents != 2000 || bal.BonusCents != 5000 {
		t.Fatalf("扣款动用了赠送权益：%+v %v", bal, err)
	}
	allocations := db.EsportsLedgerAllocation.Query().Where(esportsledgerallocation.ConsumeSourceIDEQ(order.GiftNo)).AllX(ctx)
	if len(allocations) != 1 || allocations[0].AmountCents != 8000 {
		t.Fatalf("礼物本金未落批次分摊：%+v", allocations)
	}
	// §9.2-01 口径更新：卡内本金不足时**不再在发起阶段直接报本金不足**，确认单照常落库
	// （客户可改走「发起收款」补资金）；但「本金不足不得用赠送权益送礼」这条安全口径不变——
	// 核销成单时必须仍然失败。规格依据：台账 9.2-01「余额与卡本金均不足的客户可对礼物单发起收款
	// 并核销成单（当前直接报本金不足）」。
	insufficient, err := createGiftIntent(ctx, db, 11, false, giftIntentInput{CustomerID: customer.ID,
		GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, PaymentSource: "stored_value_card_principal",
		StoredValueCardID: card.ID, ClientKey: "gift-card-intent-2"}, now)
	if err != nil {
		t.Fatalf("本金不足时应可落确认单并发起收款：%v", err)
	}
	if _, err := confirmGiftIntent(ctx, db, account.UID, insufficient.ID, insufficient.Version, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := executeGiftIntent(ctx, db, 11, false, insufficient.ID, now); !errors.Is(err, errInsufficientBalance) {
		t.Fatalf("本金不足却使用赠送权益送礼：%v", err)
	}
	if _, err := refundGift(ctx, db, 11, true, order.ID, "原路退回", now); err != nil {
		t.Fatal(err)
	}
	bal, err = storedValueCardBalance(ctx, db, card.ID)
	if err != nil || bal.PrincipalCents != 10000 || bal.BonusCents != 5000 {
		t.Fatalf("礼物退款未原路返卡：%+v %v", bal, err)
	}
	db.EsportsStoredValueCard.UpdateOneID(card.ID).SetAllowGifts(false).ExecX(ctx)
	if _, err := createGiftIntent(ctx, db, 11, false, giftIntentInput{CustomerID: customer.ID,
		GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, PaymentSource: "stored_value_card_principal",
		StoredValueCardID: card.ID, ClientKey: "gift-card-intent-3"}, now); !errors.Is(err, errStoredValueUnusable) {
		t.Fatalf("合同未授权的卡可以送礼：%v", err)
	}
}

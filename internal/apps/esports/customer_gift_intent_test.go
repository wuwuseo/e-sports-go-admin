package esports

import (
	"errors"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsgiftintent"
)

// seedVerifiedCustomer 建一个已绑定登录身份（active 用户 + 客户 + 活跃身份绑定）的成年客户。
func seedVerifiedCustomer(t *testing.T, db *ent.Client, uid string) (*ent.User, *ent.EsportsCustomer) {
	t.Helper()
	ctx := t.Context()
	account := db.User.Create().SetEmail(uid + "@example.com").SetUID(uid).SetStatus("active").SaveX(ctx)
	customer := seedGiftCustomer(t, db, account.ID)
	db.EsportsIdentityBinding.Create().SetCustomerID(customer.ID).SetProvider("user").SetSubjectScope("").
		SetExternalSubject(uid).SetVerificationMethod("verified_account_link").SetVerifiedAt(time.Now().UTC()).SaveX(ctx)
	return account, customer
}

// 客户自助创建礼物确认单：余额本金路径走通且幂等，同键换内容冲突。
func TestCustomerGiftIntentBalancePrincipalAndIdempotency(t *testing.T) {
	db := openGiftDB(t, "esports_customer_gift_intent")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	account, customer := seedVerifiedCustomer(t, db, "cust-gift-balance")
	player := seedPlayer(t, db, account.ID+100, "qualified", "online")
	gift := seedGift(t, db, giftInput{Code: "CUST-GIFT", Name: "火箭", PriceCents: 10000, CommissionMode: "percent", CommissionBp: 6000})
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 20000, Note: "测试充值"}, "cust-gift-recharge"); err != nil {
		t.Fatal(err)
	}

	in := customerGiftIntentInput{GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, ClientKey: "cust-gift-key-001"}
	intent, err := createCustomerGiftIntent(ctx, db, account.UID, in, now)
	if err != nil {
		t.Fatalf("客户自助创建确认单失败：%v", err)
	}
	if intent.Status != esportsgiftintent.StatusPending || intent.CustomerID != customer.ID ||
		intent.PaymentSource != "customer_balance_principal" || intent.AmountCents != 10000 {
		t.Fatalf("确认单快照错误：%+v", intent)
	}

	// 幂等：同一 client_key 重复提交返回同一单据。
	again, err := createCustomerGiftIntent(ctx, db, account.UID, in, now)
	if err != nil || again.ID != intent.ID {
		t.Fatalf("幂等重试未返回同一确认单：%+v %v", again, err)
	}
	if db.EsportsGiftIntent.Query().CountX(ctx) != 1 {
		t.Fatal("幂等重试重复落库")
	}
	// 同键换内容冲突。
	conflict := in
	conflict.Quantity = 2
	if _, err := createCustomerGiftIntent(ctx, db, account.UID, conflict, now); !errors.Is(err, errConflict) {
		t.Fatalf("同 client_key 换内容未被拦截：%v", err)
	}
	// §9.2-01 口径更新：余额（与卡本金）不足时**不再直接报本金不足**，仍可落确认单，
	// 由客户改走「发起收款」补齐资金；真正的资金校验在核销成单时强制执行。
	// 规格依据：台账 9.2-01「余额与卡本金均不足的客户可对礼物单发起收款并核销成单（当前直接报本金不足）」。
	short := customerGiftIntentInput{GiftID: gift.ID, PlayerID: player.ID, Quantity: 3, ClientKey: "cust-gift-key-short"}
	shortIntent, err := createCustomerGiftIntent(ctx, db, account.UID, short, now)
	if err != nil {
		t.Fatalf("余额不足时应可创建确认单并发起收款：%v", err)
	}
	if shortIntent.PaymentChannel != "none" || shortIntent.PaymentStatus != "not_required" {
		t.Fatalf("未发起收款的确认单不应带收款状态：%+v", shortIntent)
	}
	if sufficient, hint, err := customerGiftIntentFunding(ctx, db, shortIntent); err != nil || sufficient || hint == "" {
		t.Fatalf("本金不足判定不符：%v %q %v", sufficient, hint, err)
	}
	// 资金仍不足时直接送礼必须被拦住（不得伪成功）。
	if _, err := executeGiftIntent(ctx, db, 11, false, shortIntent.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("未确认的确认单被执行：%v", err)
	}
}

// 客户自助创建礼物确认单：他人不可代创建、非成年与本人送礼均被拒绝。
func TestCustomerGiftIntentGuards(t *testing.T) {
	db := openGiftDB(t, "esports_customer_gift_guards")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	account, customer := seedVerifiedCustomer(t, db, "cust-gift-guard")
	player := seedPlayer(t, db, account.ID+100, "qualified", "online")
	gift := seedGift(t, db, giftInput{Code: "CUST-GUARD-GIFT", Name: "礼物", PriceCents: 1000, CommissionMode: "percent", CommissionBp: 5000})
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 5000, Note: "测试充值"}, "cust-guard-recharge"); err != nil {
		t.Fatal(err)
	}

	// 他人（无绑定客户）不可代创建。
	if _, err := createCustomerGiftIntent(ctx, db, "cust-gift-stranger", customerGiftIntentInput{
		GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, ClientKey: "cust-guard-other"}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("他人代创建未被拦截：%v", err)
	}

	// 本人送礼被拒。
	selfPlayer := seedPlayer(t, db, account.ID, "qualified", "online")
	if _, err := createCustomerGiftIntent(ctx, db, account.UID, customerGiftIntentInput{
		GiftID: gift.ID, PlayerID: selfPlayer.ID, Quantity: 1, ClientKey: "cust-guard-self"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("本人送礼未被拦截：%v", err)
	}

	// 非成年被拒（余额充足，仍以年龄拦截优先）。
	_, minor := seedVerifiedCustomer(t, db, "cust-gift-minor")
	db.EsportsCustomer.UpdateOneID(minor.ID).SetAgeStatus("minor").ExecX(ctx)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, minor.ID,
		walletInput{PrincipalCents: 5000, Note: "测试充值"}, "cust-minor-recharge"); err != nil {
		t.Fatal(err)
	}
	if _, err := createCustomerGiftIntent(ctx, db, "cust-gift-minor", customerGiftIntentInput{
		GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, ClientKey: "cust-guard-minor"}, now); !errors.Is(err, errAgeUnverified) {
		t.Fatalf("未成年客户送礼未被拦截：%v", err)
	}
}

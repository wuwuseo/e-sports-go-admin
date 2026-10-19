package esports

import (
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsgiftintent"
	"go-admin/internal/ent/esportsledgerallocation"
	"go-admin/internal/ent/esportsledgerentry"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/esportswallettx"
)

// countLedgerEntriesByRole 统计某入口角色的批次账本条目数。
func countLedgerEntriesByRole(t *testing.T, db *ent.Client, role string) int {
	t.Helper()
	return db.EsportsLedgerEntry.Query().
		Where(esportsledgerentry.EntryRoleEQ(role)).CountX(t.Context())
}

// P0-3 回归：余额本金支付的礼物必须在批次账本记「且只记」一条 balance_gift_consume，
// 并登记批次分摊明细；账本核验（reconcileLedgers）不得再把该笔出账判成「未登记」。
func TestBalanceGiftWritesSingleBatchLedgerEntry(t *testing.T) {
	db := openGiftDB(t, "esports_gift_balance_ledger")
	defer db.Close()
	ctx := t.Context()
	customer := seedGiftCustomer(t, db, 101)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 20000, Note: "礼物批次账本测试充值"}, "gift-balance-ledger-recharge"); err != nil {
		t.Fatal(err)
	}
	player := seedPlayer(t, db, 202, "qualified", "online")
	gift := seedGift(t, db, giftInput{Code: "BALANCE-LEDGER", Name: "余额礼物", PriceCents: 3000,
		CommissionMode: "percent", CommissionBp: 5000})
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	order, err := sendGift(ctx, db, 11, giftSendInput{CustomerID: customer.ID, GiftID: gift.ID,
		PlayerID: player.ID, Quantity: 1, ClientKey: "gift-balance-ledger-1"}, now)
	if err != nil {
		t.Fatal(err)
	}

	// 存在且仅存在一条余额礼物的批次账本条目。
	if got := countLedgerEntriesByRole(t, db, "balance_gift_consume"); got != 1 {
		t.Fatalf("余额支付礼物应产生且仅产生一条 balance_gift_consume 条目：%d", got)
	}
	entry := db.EsportsLedgerEntry.Query().
		Where(esportsledgerentry.EntryRoleEQ("balance_gift_consume")).OnlyX(ctx)
	if entry.AccountType != walletCustomerBalance || entry.AccountID != strconv.Itoa(customer.ID) ||
		entry.DeltaCents != -3000 || entry.PrincipalDeltaCents != -3000 || entry.BonusDeltaCents != 0 {
		t.Fatalf("余额礼物批次条目口径不正确：%+v", entry)
	}
	// 条目来源须是真实存在的钱包流水号，账本核验才能把它认成「已登记」而不是「未登记」。
	if !db.EsportsWalletTx.Query().Where(esportswallettx.TxNoEQ(entry.SourceID)).ExistX(ctx) {
		t.Fatalf("批次条目来源 %q 不是本笔礼物钱包流水号", entry.SourceID)
	}

	// 分摊明细：一笔 3000 全在本金侧。
	allocations := db.EsportsLedgerAllocation.Query().Where(
		esportsledgerallocation.AccountTypeEQ(walletCustomerBalance),
		esportsledgerallocation.AccountIDEQ(strconv.Itoa(customer.ID)),
		esportsledgerallocation.ConsumeSourceTypeEQ("gift"),
		esportsledgerallocation.ConsumeSourceIDEQ(order.GiftNo),
	).AllX(ctx)
	if len(allocations) != 1 || allocations[0].AmountCents != 3000 ||
		allocations[0].PrincipalCents != 3000 || allocations[0].BonusCents != 0 {
		t.Fatalf("余额礼物未登记批次分摊或构成不正确：%+v", allocations)
	}

	// 未走卡片路径：不得出现 card_gift_consume（互斥、不重复记账）。
	if got := countLedgerEntriesByRole(t, db, "card_gift_consume"); got != 0 {
		t.Fatalf("余额支付礼物不得写卡片批次条目：%d", got)
	}

	// 账本核验：不再报「出账未登记批次账本或分摊明细」，也不因此算出差额。
	result, err := reconcileLedgers(ctx, db, 200)
	if err != nil {
		t.Fatal(err)
	}
	if result.ScannedWalletSums < 1 {
		t.Fatalf("账本核验未扫描到任何账户，用例未覆盖到目标口径：%+v", result)
	}
	for _, anomaly := range result.Anomalies {
		if anomaly.Kind == ledgerAnomalyWalletOutflowUnbooked {
			t.Fatalf("余额礼物仍被核验判为未登记出账：%+v", anomaly)
		}
		if anomaly.Kind == ledgerAnomalyWalletBatchMismatch && anomaly.AccountType == walletCustomerBalance {
			t.Fatalf("余额礼物导致钱包流水与批次账本求和不符：%+v", anomaly)
		}
	}
}

// 储值卡本金支付的礼物只记 card_gift_consume，不产生余额条目（防双记）。
func TestStoredValueCardGiftWritesOnlyCardLedger(t *testing.T) {
	db := openGiftDB(t, "esports_gift_card_ledger_only")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	customer := seedGiftCustomer(t, db, 101)
	player := seedPlayer(t, db, 202, "qualified", "online")
	gift := seedGift(t, db, giftInput{Code: "CARD-LEDGER", Name: "卡礼物", PriceCents: 8000,
		CommissionMode: "percent", CommissionBp: 5000})
	card := db.EsportsStoredValueCard.Create().SetCardNo("SV-LEDGER-1").SetCustomerID(customer.ID).
		SetPaymentChannel(esportsstoredvaluecard.PaymentChannelMerchantQr).
		SetPrincipalCents(10000).SetBonusCents(5000).SetAllowGifts(true).SetContractConfirmed(true).
		SetStatus(esportsstoredvaluecard.StatusActive).SetValidFrom(now.Add(-time.Hour)).
		SetValidUntil(now.AddDate(0, 1, 0)).SaveX(ctx)
	db.EsportsWalletTx.Create().SetTxNo("gift-card-ledger-seed").SetAccountType("stored_value_card").
		SetOwnerID(card.ID).SetEntryType("recharge").SetPrincipalDeltaCents(10000).SetBonusDeltaCents(5000).
		SetSourceType("stored_value_card_open").SetSourceID("gift-card-ledger-open").SaveX(ctx)
	db.EsportsLedgerEntry.Create().SetAccountType(storedValuePrepayAccount).SetAccountID(strconv.Itoa(card.ID)).
		SetSourceType("stored_value_card_open").SetSourceID("gift-card-ledger-open").SetEntryRole("card_open").
		SetDeltaCents(15000).SetPrincipalDeltaCents(10000).SetBonusDeltaCents(5000).SaveX(ctx)

	order, err := sendGift(ctx, db, 11, giftSendInput{CustomerID: customer.ID, GiftID: gift.ID,
		PlayerID: player.ID, Quantity: 1, PaymentSource: "stored_value_card_principal",
		StoredValueCardID: card.ID, ClientKey: "gift-card-ledger-1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if order.StoredValueCardID == nil || *order.StoredValueCardID != card.ID || order.PaymentSource != "stored_value_card_principal" {
		t.Fatalf("卡支付礼物未记录卡来源：%+v", order)
	}
	// 只有卡片路径的批次条目。
	if got := countLedgerEntriesByRole(t, db, "card_gift_consume"); got != 1 {
		t.Fatalf("储值卡支付礼物应产生且仅产生一条 card_gift_consume：%d", got)
	}
	if got := countLedgerEntriesByRole(t, db, "balance_gift_consume"); got != 0 {
		t.Fatalf("储值卡支付礼物不得再记余额批次条目（双记）：%d", got)
	}
	entry := db.EsportsLedgerEntry.Query().
		Where(esportsledgerentry.EntryRoleEQ("card_gift_consume")).OnlyX(ctx)
	if entry.AccountType != storedValuePrepayAccount || entry.AccountID != strconv.Itoa(card.ID) ||
		entry.DeltaCents != -8000 || entry.PrincipalDeltaCents != -8000 {
		t.Fatalf("卡片礼物批次条目口径不正确：%+v", entry)
	}
	// 卡内余额只被扣一次（赠送权益不动）。
	balance, err := storedValueCardBalance(ctx, db, card.ID)
	if err != nil || balance.PrincipalCents != 2000 || balance.BonusCents != 5000 {
		t.Fatalf("卡余额被重复扣减或动用了赠送权益：%+v %v", balance, err)
	}
}

// 外部收款的礼物既不产生余额流水也不产生批次账本 / 分摊（钱没进入预付账户）。
func TestExternalGiftWritesNoBatchLedger(t *testing.T) {
	db := openGiftDB(t, "esports_gift_external_ledger")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	account := db.User.Create().SetEmail("gift-external@example.com").
		SetUID("gift-external-user").SetStatus("active").SaveX(ctx)
	customer := seedGiftCustomer(t, db, account.ID)
	db.EsportsIdentityBinding.Create().SetCustomerID(customer.ID).SetProvider("user").SetSubjectScope("").
		SetExternalSubject(account.UID).SetVerificationMethod("verified_account_link").
		SetVerifiedAt(now).SaveX(ctx)
	player := seedPlayer(t, db, account.ID+100, "qualified", "online")
	gift := seedGift(t, db, giftInput{Code: "EXTERNAL-GIFT", Name: "外部收款礼物", PriceCents: 5000,
		CommissionMode: "percent", CommissionBp: 5000})
	// 直接落一张「已确认 + 已核验到账」的礼物确认单（外部收款的成单前提）。
	intent := db.EsportsGiftIntent.Create().SetCustomerID(customer.ID).SetGiftID(gift.ID).
		SetGiftName(gift.Name).SetGiftVersion(gift.Version).
		SetPlayerID(player.ID).SetPlayerName(player.DisplayName).
		SetQuantity(1).SetUnitPriceCents(5000).SetAmountCents(5000).
		SetPaymentSource("customer_balance_principal").
		SetPaymentChannel(esportsgiftintent.PaymentChannelMerchantQr).
		SetPaymentStatus(esportsgiftintent.PaymentStatusVerified).SetPaymentRef("EXT-PAY-1").
		SetCreatedByAdminID(11).SetClientKey("gift-external-intent-1").
		SetStatus(esportsgiftintent.StatusConfirmed).SetConfirmationRef("gift-external-confirm-1").
		SetConfirmedAt(now).SetExpiresAt(now.Add(time.Hour)).SaveX(ctx)

	order, err := sendGift(ctx, db, 11, giftSendInput{CustomerID: customer.ID, GiftID: gift.ID,
		PlayerID: player.ID, Quantity: 1, PaymentSource: giftPaymentSourceExternal,
		AuthorizationID: intent.ID, ClientKey: "gift-external-1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if order.PaymentSource != giftPaymentSourceExternal || order.PrincipalCents != 0 || order.PaymentRef != "EXT-PAY-1" {
		t.Fatalf("外部收款礼物口径不正确：%+v", order)
	}
	if got := countLedgerEntriesByRole(t, db, "balance_gift_consume") +
		countLedgerEntriesByRole(t, db, "card_gift_consume"); got != 0 {
		t.Fatalf("外部收款礼物不应产生批次账本条目：%d", got)
	}
	if got := db.EsportsWalletTx.Query().Where(esportswallettx.SourceIDEQ(order.GiftNo)).CountX(ctx); got != 0 {
		t.Fatalf("外部收款礼物不应产生钱包流水：%d", got)
	}
	if got := db.EsportsLedgerAllocation.Query().
		Where(esportsledgerallocation.ConsumeSourceIDEQ(order.GiftNo)).CountX(ctx); got != 0 {
		t.Fatalf("外部收款礼物不应产生批次分摊：%d", got)
	}
}

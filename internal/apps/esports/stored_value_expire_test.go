package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/migrate"
)

func openStoredValueExpireDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// 到期作废赠送权益（§4.4）：卡过期时状态转 expired，未消费赠送写作废流水，本金保留待退卡；
// 重复运行不重复作废。
func TestStoredValueCardExpireVoidsBonus(t *testing.T) {
	db := openStoredValueExpireDB(t, "esports_stored_value_expire_bonus")
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)

	card, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-BONUS", PrincipalCents: 2000, BonusCents: 500,
		ContractConfirmed: true, ValidityDays: 10, IdempotencyKey: "sv-bonus-key",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	// 回填有效期到过去，模拟历史数据到期。
	db.EsportsStoredValueCard.UpdateOneID(card.ID).
		SetValidFrom(now.UTC().AddDate(0, -2, 0)).
		SetValidUntil(now.UTC().Add(-time.Hour)).SaveX(ctx)

	count, err := expireStoredValueCards(ctx, db, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("应处理 1 张到期卡，got=%d", count)
	}
	if got := db.EsportsStoredValueCard.GetX(ctx, card.ID).Status; got != esportsstoredvaluecard.StatusExpired {
		t.Fatalf("卡状态应为 expired，got=%s", got)
	}
	balance, err := storedValueCardBalance(ctx, db, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.PrincipalCents != 2000 || balance.BonusCents != 0 {
		t.Fatalf("到期应作废赠送、保留本金：%+v", balance)
	}
	// 作废流水：entry_type=expire，仅赠送负数。
	tx := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypeStoredValueCard),
		esportswallettx.OwnerIDEQ(card.ID),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeExpire),
	).OnlyX(ctx)
	if tx.PrincipalDeltaCents != 0 || tx.BonusDeltaCents != -500 ||
		tx.SourceType != "stored_value_expire" {
		t.Fatalf("作废流水不正确：%+v", tx)
	}

	// 重复运行：卡已非 active，不再处理，也不重复作废。
	again, err := expireStoredValueCards(ctx, db, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("重复运行不应再处理，got=%d", again)
	}
	if got := db.EsportsStoredValueCard.GetX(ctx, card.ID); got.Status != esportsstoredvaluecard.StatusExpired {
		t.Fatalf("重复运行后状态应保持 expired，got=%s", got.Status)
	}
	if n := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeExpire),
		esportswallettx.OwnerIDEQ(card.ID),
	).CountX(ctx); n != 1 {
		t.Fatalf("作废流水应仅 1 条，got=%d", n)
	}
}

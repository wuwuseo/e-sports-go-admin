package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/esportsstoredvalueexception"
	"go-admin/internal/ent/esportswallettx"
)

// 本文件是 round4 wallet-member 桶 §4.6-06（储值卡一致性异常队列）的验收回归测试。

// sveQueueRows 读出异常队列全部记录（按 ID 升序，便于断言入队顺序）。
func sveQueueRows(t *testing.T, db *ent.Client) []*ent.EsportsStoredValueException {
	t.Helper()
	return db.EsportsStoredValueException.Query().
		Order(ent.Asc(esportsstoredvalueexception.FieldID)).AllX(t.Context())
}

// sveSeedCardOrder 造一张「标记为储值卡支付 + 已关联指定卡」的订单。
func sveSeedCardOrder(t *testing.T, db *ent.Client, orderNo string, cardID int) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	order := db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SetPaymentChannel(esportsorder.PaymentChannelStoredValueCard).
		SetStoredValueCardID(cardID).SaveX(ctx)
	return order
}

// sveSeedActiveCard 开一张已核验生效的储值卡（余额为正）。
func sveSeedActiveCard(t *testing.T, db *ent.Client, adminID int, principalCents int64, idemKey string) *storedValueCardView {
	t.Helper()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, adminID)
	card, err := openVerifiedStoredValueCard(t, ctx, db, adminID, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "SVE-" + idemKey, PrincipalCents: principalCents,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: idemKey,
	}, time.Now())
	if err != nil {
		t.Fatalf("测试开卡失败：%v", err)
	}
	return card
}

// §4.6-06：结单被储值卡一致性拦下时必须落一条可跟进异常；重复触发只累加 occurrences，不新增行。
func TestSveCloseBlockQueuesExceptionOnce(t *testing.T) {
	db := openStoredValueDB(t, "esports_sve_close_queue")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	card := sveSeedActiveCard(t, db, 11, 3000, "sve-close-open-1")
	order := seedPendingCloseOrder(t, db, "sve-close-1", "ev-1.png", 600)
	db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentChannel(esportsorder.PaymentChannelStoredValueCard).
		SetStoredValueCardID(card.ID).SaveX(ctx)

	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("缺储值卡核销流水应拦下结单，err=%v", err)
	}
	rows := sveQueueRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("结单被拦下应入队 1 条异常，实际 %d 条", len(rows))
	}
	got := rows[0]
	if got.Kind != esportsstoredvalueexception.KindSettlementBlocked {
		t.Fatalf("结单闸口拦截的异常类型应为 settlement_blocked，got=%s", got.Kind)
	}
	if got.CardID != card.ID || got.OrderID != order.ID {
		t.Fatalf("异常记录未带上卡 / 订单：%+v", got)
	}
	if got.Status != esportsstoredvalueexception.StatusOpen || got.Occurrences != 1 {
		t.Fatalf("新入队异常应为 open 且 occurrences=1：%+v", got)
	}
	if got.DedupKey == nil || *got.DedupKey != strconv.Itoa(card.ID)+":"+strconv.Itoa(order.ID)+":settlement_blocked" {
		t.Fatalf("去重键口径错误：%+v", got.DedupKey)
	}
	if got.Detail["mismatch_kind"] != string(esportsstoredvalueexception.KindCardLinkMismatch) {
		t.Fatalf("异常明细未记录具体不一致类型：%+v", got.Detail)
	}
	if got.LastHitAt == nil || got.LastHitAt.IsZero() {
		t.Fatalf("异常记录缺少最近命中时间：%+v", got.LastHitAt)
	}

	// 再次结单仍被拦：同一（卡 + 订单 + 类型）只累加不新增。
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "再次结单"}, now.Add(time.Minute)); !errors.Is(err, errInvalidInput) {
		t.Fatalf("第二次结单仍应被拦下，err=%v", err)
	}
	rows = sveQueueRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("重复触发不应新增异常行，实际 %d 条", len(rows))
	}
	if rows[0].Occurrences != 2 {
		t.Fatalf("重复触发应累加 occurrences 到 2，got=%d", rows[0].Occurrences)
	}
	if rows[0].LastHitAt == nil || !rows[0].LastHitAt.After(now) {
		t.Fatalf("重复触发应刷新最近命中时间：%+v", rows[0].LastHitAt)
	}
}

// §4.6-06：全量核验发现「卡标记为已用尽但流水派生余额仍为正」时入队 card_flag_mismatch。
func TestSveReconcileQueuesCardFlagMismatch(t *testing.T) {
	db := openStoredValueDB(t, "esports_sve_card_flag")
	defer db.Close()
	ctx := t.Context()

	card := sveSeedActiveCard(t, db, 11, 3000, "sve-flag-open-1")
	db.EsportsStoredValueCard.UpdateOneID(card.ID).
		SetStatus(esportsstoredvaluecard.StatusExhausted).SaveX(ctx)

	if _, err := reconcileLedgers(ctx, db, 100); err != nil {
		t.Fatalf("核验失败：%v", err)
	}
	rows := sveQueueRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("卡标记与流水不符应入队 1 条异常，实际 %d 条", len(rows))
	}
	if rows[0].Kind != esportsstoredvalueexception.KindCardFlagMismatch || rows[0].CardID != card.ID {
		t.Fatalf("异常应为 card_flag_mismatch 且带上卡 ID：%+v", rows[0])
	}
}

// §4.6-06：全量核验逐卡复检该卡名下订单，订单已关联卡却缺核销流水时入队 card_link_mismatch。
func TestSveReconcileQueuesCardLinkMismatchForMissingWriteOff(t *testing.T) {
	db := openStoredValueDB(t, "esports_sve_card_link")
	defer db.Close()
	ctx := t.Context()

	card := sveSeedActiveCard(t, db, 11, 3000, "sve-link-open-1")
	order := sveSeedCardOrder(t, db, "sve-link-order-1", card.ID)

	if _, err := reconcileLedgers(ctx, db, 100); err != nil {
		t.Fatalf("核验失败：%v", err)
	}
	rows := sveQueueRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("订单与卡的关联缺核销流水应入队 1 条异常，实际 %d 条", len(rows))
	}
	if rows[0].Kind != esportsstoredvalueexception.KindCardLinkMismatch ||
		rows[0].CardID != card.ID || rows[0].OrderID != order.ID {
		t.Fatalf("异常应为 card_link_mismatch 且带上卡 / 订单：%+v", rows[0])
	}
}

// §4.6-06：全量核验发现卡账户余额为负（超扣 / 重复冲正）时入队 write_off_mismatch；
// 健康卡不得产生任何待跟进异常（避免把既有「未登记」口径噪音灌进队列）。
func TestSveReconcileQueuesWriteOffMismatchForCardAccountAnomaly(t *testing.T) {
	db := openStoredValueDB(t, "esports_sve_write_off")
	defer db.Close()
	ctx := t.Context()

	card := sveSeedActiveCard(t, db, 11, 3000, "sve-wo-open-1")
	if _, err := reconcileLedgers(ctx, db, 100); err != nil {
		t.Fatalf("核验失败：%v", err)
	}
	if rows := sveQueueRows(t, db); len(rows) != 0 {
		t.Fatalf("健康储值卡不应入队异常：%+v", rows)
	}

	// 制造超扣：卡内追加一笔超过余额的出账流水 → 派生余额为负。
	db.EsportsWalletTx.Create().SetTxNo("sve-wo-tamper").
		SetAccountType(esportswallettx.AccountTypeStoredValueCard).SetOwnerID(card.ID).
		SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-9000).SetBonusDeltaCents(0).
		SetSourceType("order").SetSourceID("sve-wo-tamper-order").
		SetBalanceBeforeCents(3000).SetBalanceAfterCents(-6000).
		SetOperatorID(11).SetNote("测试超扣").SaveX(ctx)

	if _, err := reconcileLedgers(ctx, db, 100); err != nil {
		t.Fatalf("核验失败：%v", err)
	}
	rows := sveQueueRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("卡账户余额为负应入队 1 条异常，实际 %d 条", len(rows))
	}
	if rows[0].Kind != esportsstoredvalueexception.KindWriteOffMismatch || rows[0].CardID != card.ID {
		t.Fatalf("异常应为 write_off_mismatch 且带上卡 ID：%+v", rows[0])
	}
}

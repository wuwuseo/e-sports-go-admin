package esports

import (
	"context"
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsledgerentry"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/migrate"
)

func openStoredValueDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(t.Context())
	return db
}

func TestStoredValueCardListScopedToCurrentCustomerOwner(t *testing.T) {
	db := openStoredValueDB(t, "esports_stored_value_scope")
	defer db.Close()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()
	card, err := openStoredValueCard(ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr", PaymentRef: "SCOPE-RECEIPT",
		PrincipalCents: 1000, ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "scope-card-open-1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := listStoredValueCards(ctx, db, 22, false, 0, "", 0, 10, now); err != nil || len(rows) != 0 {
		t.Fatalf("其他客服看到储值卡：%+v %v", rows, err)
	}
	if rows, err := listStoredValueCards(ctx, db, 11, false, 0, "", 0, 10, now); err != nil || len(rows) != 1 || rows[0].ID != card.ID {
		t.Fatalf("负责客服看不到储值卡：%+v %v", rows, err)
	}
	db.EsportsCustomer.UpdateOneID(customer.ID).SetOwnerAdminID(22).ExecX(ctx)
	if rows, err := listStoredValueCards(ctx, db, 11, false, 0, "", 0, 10, now); err != nil || len(rows) != 0 {
		t.Fatalf("转移后旧客服仍能看到储值卡：%+v %v", rows, err)
	}
}

func openVerifiedStoredValueCard(t *testing.T, ctx context.Context, db *ent.Client, adminID int, in storedValueOpenInput, now time.Time) (*storedValueCardView, error) {
	t.Helper()
	view, err := openStoredValueCard(ctx, db, adminID, in, now)
	if err != nil || in.PaymentChannel == "customer_balance" {
		return view, err
	}
	if view.Status != esportsstoredvaluecard.StatusPendingVerification || view.BalanceCents != 0 {
		t.Fatalf("外部收款申请应待核验且余额为零：%+v", view)
	}
	verified, err := verifyStoredValueCardPayment(ctx, db, adminID+1, view.ID, true, in.PaymentRef,
		view.PrincipalCents, "对照实际到账流水和收款账户核验", now)
	if err != nil {
		t.Fatalf("测试核验开卡失败：%v", err)
	}
	return verified, nil
}

func TestStoredValueExternalPaymentRequiresIndependentVerification(t *testing.T) {
	db := openStoredValueDB(t, "esports_stored_value_payment_verification")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()
	in := storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "REAL-RECEIPT-1", PrincipalCents: 10000, BonusCents: 2000,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-verification-1",
	}
	pending, err := openStoredValueCard(ctx, db, 11, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != esportsstoredvaluecard.StatusPendingVerification || pending.BalanceCents != 0 || pending.ConsumedCents != 0 {
		t.Fatalf("申请阶段不得发放卡内余额：%+v", pending)
	}
	if got := db.EsportsWalletTx.Query().CountX(ctx); got != 0 {
		t.Fatalf("申请阶段不得产生钱包流水，got=%d", got)
	}
	if got := db.EsportsLedgerEntry.Query().CountX(ctx); got != 0 {
		t.Fatalf("申请阶段不得产生预付款账本，got=%d", got)
	}
	if _, err = verifyStoredValueCardPayment(ctx, db, 11, pending.ID, true, in.PaymentRef, 10000, "核对收款账户和到账时间", now); !errors.Is(err, errForbidden) {
		t.Fatalf("申请人自审应被拒，err=%v", err)
	}
	if _, err = verifyStoredValueCardPayment(ctx, db, 22, pending.ID, true, in.PaymentRef, 9999, "核对收款账户和到账时间", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("实收金额不符不得激活，err=%v", err)
	}
	verifiedAt := now.Add(48 * time.Hour)
	active, err := verifyStoredValueCardPayment(ctx, db, 22, pending.ID, true, in.PaymentRef, 10000, "核对收款账户和到账时间", verifiedAt)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != esportsstoredvaluecard.StatusActive || active.BalanceCents != 12000 || active.PaymentVerifiedByAdminID == nil || *active.PaymentVerifiedByAdminID != 22 {
		t.Fatalf("核验后才应激活并入账：%+v", active)
	}
	if !active.ValidFrom.Equal(verifiedAt.UTC()) || !active.ValidUntil.Equal(verifiedAt.UTC().AddDate(0, 0, 30)) {
		t.Fatalf("有效天数应从实际激活日开始计算：%v ~ %v", active.ValidFrom, active.ValidUntil)
	}
	if _, err = verifyStoredValueCardPayment(ctx, db, 23, pending.ID, true, in.PaymentRef, 10000, "重复核验同一笔到账流水", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复核验不得重复入账，err=%v", err)
	}
	in.IdempotencyKey = "sv-verification-2"
	second, err := openStoredValueCard(ctx, db, 11, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = verifyStoredValueCardPayment(ctx, db, 22, second.ID, true, in.PaymentRef, 10000, "试图复用已核验流水号", now); !errors.Is(err, errConflict) {
		t.Fatalf("同一到账流水不得激活第二张卡，err=%v", err)
	}
	if got := db.EsportsStoredValueCard.GetX(ctx, second.ID).Status; got != esportsstoredvaluecard.StatusPendingVerification {
		t.Fatalf("冲突后第二张卡须保持待核验，got=%s", got)
	}
	rejected, err := verifyStoredValueCardPayment(ctx, db, 22, second.ID, false, "", 0, "收款流水已用于其他开卡申请", now)
	if err != nil || rejected.Status != esportsstoredvaluecard.StatusRejected || rejected.BalanceCents != 0 {
		t.Fatalf("驳回应保持无余额，card=%+v err=%v", rejected, err)
	}
}

// 外部收款开卡：本金与赠送分列入账、预付款批次落账，凭证与合同必填，幂等键重复被拒。
func TestStoredValueOpenExternalChannel(t *testing.T) {
	db := openStoredValueDB(t, "esports_stored_value_open")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()

	view, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-2026-0001", PrincipalCents: 10000, BonusCents: 2000,
		ContractConfirmed: true, ValidityDays: 30, Note: "开卡", IdempotencyKey: "sv-open-key-1",
	}, now)
	if err != nil {
		t.Fatalf("开卡应成功，err=%v", err)
	}
	if view.PrincipalRemainingCents != 10000 || view.BonusRemainingCents != 2000 || view.BalanceCents != 12000 {
		t.Fatalf("开卡余额派生不正确：%+v", view)
	}
	if view.ConsumedCents != 0 || view.Status != esportsstoredvaluecard.StatusActive {
		t.Fatalf("新卡应为可用且未消费：%+v", view)
	}
	// 赠送权益随卡有效期到期。
	tx, err := db.EsportsWalletTx.Query().Where().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tx) != 1 || tx[0].BonusExpiresAt == nil {
		t.Fatalf("应写入一条带赠送到期时间的开卡流水，got=%+v", tx)
	}
	batches, err := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.EntryRoleEQ("card_open")).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || batches[0].DeltaCents != 12000 || batches[0].FundBatch != "merchant_qr:QR-2026-0001" {
		t.Fatalf("预付款批次账本不正确：%+v", batches)
	}

	// 缺凭证、未确认合同都不得写卡。
	if _, err = openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "corporate_transfer",
		PrincipalCents: 1000, ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-open-key-no-ref",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("外部收款缺凭证应被拒，err=%v", err)
	}
	if _, err = openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-2", PrincipalCents: 1000, ValidityDays: 30, IdempotencyKey: "sv-open-key-no-contract",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未确认合同应被拒，err=%v", err)
	}
	// 同一幂等键重复提交只入账一次。
	if _, err = openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-2026-0001", PrincipalCents: 10000, BonusCents: 2000,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-open-key-1",
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复幂等键应冲突，err=%v", err)
	}
}

// 用余额购卡属账户间划转：同事务扣减余额本金、增加卡本金并产生配对流水；不接受赠送。
func TestStoredValueOpenByBalanceTransfer(t *testing.T) {
	db := openStoredValueDB(t, "esports_stored_value_transfer")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()

	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 5000, Note: "充值"}, "sv-recharge-key"); err != nil {
		t.Fatal(err)
	}
	view, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "customer_balance",
		PrincipalCents: 3000, ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-transfer-key",
	}, now)
	if err != nil {
		t.Fatalf("余额划转开卡应成功，err=%v", err)
	}
	if view.PrincipalRemainingCents != 3000 {
		t.Fatalf("卡本金应为 3000，got=%d", view.PrincipalRemainingCents)
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 5000 充值 − 3000 划转 = 2000，划转只动本金。
	if balance.PrincipalCents != 2000 || balance.BonusCents != 0 {
		t.Fatalf("划转后余额本金应为 2000，got=%+v", balance)
	}
	// 配对流水：余额支出与卡入账各一条，批次账本记来源为余额划转。
	pairs, err := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.EntryRoleEQ("stored_value_transfer_out")).Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pairs != 1 {
		t.Fatalf("应有一条余额划转支出账本，got=%d", pairs)
	}

	// 划转不得产生赠送权益。
	if _, err = openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "customer_balance",
		PrincipalCents: 100, BonusCents: 100, ContractConfirmed: true,
		ValidityDays: 30, IdempotencyKey: "sv-transfer-bonus-key",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("余额购卡带赠送应被拒，err=%v", err)
	}
	if _, err = openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "customer_balance",
		PrincipalCents: 99999, ContractConfirmed: true, ValidityDays: 30,
		IdempotencyKey: "sv-transfer-poor-key",
	}, now); !errors.Is(err, errInsufficientBalance) {
		t.Fatalf("余额不足应被拒，err=%v", err)
	}
}

// 退卡：试算可退 = 未消费本金、赠送作废；审核不得自审，通过后本金退回、卡转已退。
func TestStoredValueRefundFlow(t *testing.T) {
	db := openStoredValueDB(t, "esports_stored_value_refund")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()

	card, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "wechat_work",
		PaymentRef: "WX-1", PrincipalCents: 10000, BonusCents: 2000,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-refund-open-key",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	quote, refundable, err := storedValueRefundQuoteFor(ctx, db, card.EsportsStoredValueCard, now)
	if err != nil {
		t.Fatal(err)
	}
	if refundable != 10000 || quote["bonus_forfeited_cents"] != int64(2000) {
		t.Fatalf("退卡试算应为可退本金 10000、作废赠送 2000，got=%d / %v", refundable, quote["bonus_forfeited_cents"])
	}

	pending, err := requestStoredValueRefund(ctx, db, 11, card.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != esportsstoredvaluecard.StatusRefundPending {
		t.Fatalf("申请后应为待审，got=%s", pending.Status)
	}
	// 申请人本人不得审核。
	if _, err = decideStoredValueRefund(ctx, db, 11, card.ID, true, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("自审应被拒，err=%v", err)
	}
	// 退卡出账（审核通过即「已出账」）须由财务角色执行，并登记打款凭证 / 银行流水号（P1，§4.4）。
	poSeedConfig(t, db, financeReportRoleNamesKey, "sv-refund-auditor")
	role := db.Role.Create().SetName("sv-refund-auditor").SetDisplayName("退卡出账员").
		SetStatus("active").SaveX(ctx)
	auditor := seedAdminAccount(t, db, "sv-refund-auditor")
	db.AdminRole.Create().SetAdminID(auditor.ID).SetRoleID(role.ID).SaveX(ctx)
	refunded, err := decideStoredValueRefund(ctx, db, auditor.ID, card.ID, true, now,
		storedValueRefundPayout{PayoutRef: "BANK-2026-000001", PayoutNote: "网银转账"})
	if err != nil {
		t.Fatalf("审核通过应成功，err=%v", err)
	}
	if refunded.Status != esportsstoredvaluecard.StatusRefunded ||
		refunded.RefundedCents != 10000 || refunded.BalanceCents != 0 {
		t.Fatalf("退卡结果不正确：%+v", refunded)
	}
	// 已退卡不可再次申请。
	if _, err = requestStoredValueRefund(ctx, db, 11, card.ID, now); !errors.Is(err, errStoredValueUnusable) {
		t.Fatalf("已退卡再申请应被拒，err=%v", err)
	}

	// 驳回路径：状态还原为可用，余额不变。
	second, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "wechat_work",
		PaymentRef: "WX-2", PrincipalCents: 5000, ContractConfirmed: true,
		ValidityDays: 30, IdempotencyKey: "sv-refund-reject-key",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = requestStoredValueRefund(ctx, db, 11, second.ID, now); err != nil {
		t.Fatal(err)
	}
	rejected, err := decideStoredValueRefund(ctx, db, 22, second.ID, false, now)
	if err != nil {
		t.Fatalf("驳回应成功，err=%v", err)
	}
	if rejected.Status != esportsstoredvaluecard.StatusActive || rejected.BalanceCents != 5000 {
		t.Fatalf("驳回后应还原为可用且余额不变：%+v", rejected)
	}
}

// 储值卡核销订单：置订单标记与关联卡、扣减卡内余额、走完名额占用，并拒绝越权与重复核销。
func TestStoredValuePayOrder(t *testing.T) {
	db := openStoredValueDB(t, "esports_stored_value_pay")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()
	sku, err := saveSku(ctx, db, skuInput{
		Code: "SV-PAY-1", Title: "储值卡核销商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 10, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	card, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-PAY-1", PrincipalCents: 3000, ContractConfirmed: true,
		ValidityDays: 30, IdempotencyKey: "sv-pay-open-key",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"sv-pay-order-key", now)
	if err != nil {
		t.Fatal(err)
	}
	paid, err := payOrderWithStoredValueCard(ctx, db, 11, order.ID, card.ID, now)
	if err != nil {
		t.Fatalf("储值卡核销应成功，err=%v", err)
	}
	if paid.PaymentStatus != esportsorder.PaymentStatusPaid ||
		paid.PaymentChannel != esportsorder.PaymentChannelStoredValueCard ||
		paid.StoredValueCardID == nil || *paid.StoredValueCardID != card.ID ||
		paid.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("核销后订单标记不正确：%+v", paid)
	}
	if paid.CsCommissionRuleLockedAt == nil || paid.CsCommissionAdminID == nil ||
		*paid.CsCommissionAdminID != 11 || paid.CsCommissionRuleID != nil {
		t.Fatalf("储值卡核销未锁定当时客服提成口径：%+v", paid)
	}
	remaining, err := storedValueCardBalance(ctx, db, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if remaining.total() != 2000 {
		t.Fatalf("核销 1000 后卡余额应为 2000，got=%d", remaining.total())
	}
	if err = storedValueOrderConsistent(ctx, db, paid); err != nil {
		t.Fatalf("标记 / 关联卡 / 流水一致时校验应通过，err=%v", err)
	}
	// 重复核销同一订单被幂等索引拒绝。
	if _, err = payOrderWithStoredValueCard(ctx, db, 11, order.ID, card.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复核销应冲突，err=%v", err)
	}

	// 适用范围不命中：卡只限指定 SKU。
	scoped, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-PAY-2", PrincipalCents: 3000, ApplicableScope: []int{999999},
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-pay-scope-key",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"sv-pay-order-key-2", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = payOrderWithStoredValueCard(ctx, db, 11, second.ID, scoped.ID, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("不在适用范围内的商品应被拒，err=%v", err)
	}
	// 余额不足：卡内 3000 < 订单一单 5000。
	bigSku, err := saveSku(ctx, db, skuInput{
		Code: "SV-PAY-BIG", Title: "高价商品", ServiceType: "play", UnitPriceCents: 5000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 10, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	third, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: bigSku.ID, Quantity: 1},
		"sv-pay-order-key-3", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = payOrderWithStoredValueCard(ctx, db, 11, third.ID, card.ID, now); !errors.Is(err, errInsufficientBalance) {
		t.Fatalf("卡余额不足应被拒，err=%v", err)
	}

	// 结单一致性：抹掉关联卡后应拒绝结单。
	db.EsportsOrder.UpdateOneID(paid.ID).ClearStoredValueCardID().SaveX(ctx)
	if err = storedValueOrderConsistent(ctx, db, db.EsportsOrder.GetX(ctx, paid.ID)); err == nil {
		t.Fatal("标记为储值卡但无关联卡时应校验失败")
	}
}

// 储值卡订单退款原路退回：售后退款回原卡账户（而非客户余额），并冲回预付款批次。
func TestStoredValueOrderRefundReturnsToCard(t *testing.T) {
	db := openStoredValueDB(t, "esports_stored_value_refund_order")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()
	sku, err := saveSku(ctx, db, skuInput{
		Code: "SV-REFUND-ORDER", Title: "储值卡退款商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 10, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	card, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-REFUND", PrincipalCents: 3000, BonusCents: 1000,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "sv-refund-order-open",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"sv-refund-order-key", now)
	if err != nil {
		t.Fatal(err)
	}
	paid, err := payOrderWithStoredValueCard(ctx, db, 11, order.ID, card.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	// 应付 1000、先赠送后本金：赠送扣 1000、本金不动。
	after, err := storedValueCardBalance(ctx, db, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PrincipalCents != 3000 || after.BonusCents != 0 {
		t.Fatalf("核销后应为本金 3000 / 赠送 0，got=%+v", after)
	}
	// 售后执行「退原核销款」：只用到售后单的编号与退款金额，直接构造即可。
	item := &ent.EsportsAfterSale{AfterSaleNo: "AS-SV-1", RefundCents: 400}
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = executeRefundOriginal(ctx, tx, item, paid, 22); err != nil {
		_ = tx.Rollback()
		t.Fatalf("储值卡订单退款应成功，err=%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	refunded, err := storedValueCardBalance(ctx, db, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 原核销从赠送扣，退款镜像先本金后赠送 → 退款落到赠送。
	if refunded.PrincipalCents != 3000 || refunded.BonusCents != 400 {
		t.Fatalf("退款应回原卡且为赠送 400，got=%+v", refunded)
	}
	// 预付款批次同步冲回。
	batches, err := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.EntryRoleEQ("card_refund_return")).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || batches[0].DeltaCents != 400 {
		t.Fatalf("应有一条 400 分的批次冲回，got=%+v", batches)
	}
}

// 到期：过期卡被任务标记为 expired（不动金额），临近到期的可用卡计入客服待办提醒。
func TestStoredValueExpireAndDueReminder(t *testing.T) {
	db := openStoredValueDB(t, "esports_stored_value_expire")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Now()

	due, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-DUE", PrincipalCents: 1000, ContractConfirmed: true,
		ValidityDays: 10, IdempotencyKey: "sv-due-key",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	// 开卡接口不允许过去有效期，先正常开卡再回填有效期，模拟历史数据。
	oldCard, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "QR-OLD", PrincipalCents: 2000, ContractConfirmed: true,
		ValidityDays: 5, IdempotencyKey: "sv-expired-key",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	expired := db.EsportsStoredValueCard.UpdateOneID(oldCard.ID).
		SetValidFrom(now.UTC().AddDate(0, -2, 0)).
		SetValidUntil(now.UTC().Add(-time.Hour)).SaveX(ctx)

	count, err := expireStoredValueCards(ctx, db, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("应标记 1 张过期卡，got=%d", count)
	}
	if got := db.EsportsStoredValueCard.GetX(ctx, expired.ID).Status; got != esportsstoredvaluecard.StatusExpired {
		t.Fatalf("过期卡状态应为 expired，got=%s", got)
	}
	// 未过期的卡不受影响。
	if got := db.EsportsStoredValueCard.GetX(ctx, due.ID).Status; got != esportsstoredvaluecard.StatusActive {
		t.Fatalf("未过期卡应保持可用，got=%s", got)
	}
	// 过期只改状态，金额不动：未消费本金仍可走退卡。
	balance, err := storedValueCardBalance(ctx, db, expired.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.total() != 2000 {
		t.Fatalf("过期不应作废金额，got=%d", balance.total())
	}
	dueCount, err := countStoredValueCardsDue(ctx, db, 11, true, now, storedValueDueDays)
	if err != nil {
		t.Fatal(err)
	}
	if dueCount != 1 {
		t.Fatalf("应有 1 张临近到期卡进入提醒，got=%d", dueCount)
	}
	// 非超管只看归属本人客户的卡。
	other, err := countStoredValueCardsDue(ctx, db, 77, false, now, storedValueDueDays)
	if err != nil {
		t.Fatal(err)
	}
	if other != 0 {
		t.Fatalf("他人客服不应看到该卡，got=%d", other)
	}
}

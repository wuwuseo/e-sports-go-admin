package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsledgerentry"
	"go-admin/internal/ent/esportswallettx"
)

// 按充值批次还原的退款试算（§4.5）：多批次充值 + 部分消费后，逐批给出已分摊额、批次剩余、
// 可退本金与作废赠送，且各批次可退本金之和等于账户派生本金余额（覆盖批次 FIFO 分摊）。
func TestRechargeRefundQuoteBatchFIFO(t *testing.T) {
	db := openStoredValueDB(t, "esports_recharge_refund_quote_fifo")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)

	// 批次一：纯本金 10000；批次二：本金 5000 + 赠送 1000（批次金额 6000）。
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 10000, Note: "首次充值"}, "rrq-recharge-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 5000, BonusCents: 1000, Note: "二次充值"}, "rrq-recharge-2"); err != nil {
		t.Fatal(err)
	}
	sku, err := saveSku(ctx, db, skuInput{
		Code: "RRQ-FIFO", Title: "批次退款试算商品", ServiceType: "play", UnitPriceCents: 8000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"rrq-order-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := payOrderWithBalance(ctx, db, 11, order.ID, now); err != nil {
		t.Fatal(err)
	}
	// 默认先赠送后本金：应付 8000 → 赠送扣 1000、本金扣 7000，余本金 8000 / 赠送 0。
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.PrincipalCents != 8000 || balance.BonusCents != 0 {
		t.Fatalf("核销后余额不正确：%+v", balance)
	}

	quote, err := rechargeRefundQuoteFor(ctx, db, walletCustomerBalance, strconv.Itoa(customer.ID), now)
	if err != nil {
		t.Fatalf("批次退款试算应成功，err=%v", err)
	}
	if len(quote.Batches) != 2 {
		t.Fatalf("应还原两个充值批次：%+v", quote.Batches)
	}
	// FIFO 分摊：8000 全部落在最早的批次一。
	first, second := quote.Batches[0], quote.Batches[1]
	if first.AllocatedCents != 8000 || first.RemainingByAllocationCents != 2000 {
		t.Fatalf("批次一的已分摊 / 剩余不正确：%+v", first)
	}
	if second.AllocatedCents != 0 || second.RemainingByAllocationCents != 6000 {
		t.Fatalf("批次二不应被分摊：%+v", second)
	}
	// 余额口径还原：批次一本金 10000 − 7000 = 3000；批次二本金 5000、赠送 1000 被先扣。
	if first.PrincipalCents != 10000 || first.BonusCents != 0 ||
		first.PrincipalRemainingCents != 3000 || first.BonusRemainingCents != 0 {
		t.Fatalf("批次一本金 / 赠送还原不正确：%+v", first)
	}
	if second.PrincipalCents != 5000 || second.BonusCents != 1000 ||
		second.PrincipalRemainingCents != 5000 || second.BonusRemainingCents != 0 {
		t.Fatalf("批次二本金 / 赠送还原不正确：%+v", second)
	}
	if first.RefundableCents != 3000 || second.RefundableCents != 5000 ||
		first.BonusForfeitedCents != 0 || second.BonusForfeitedCents != 0 {
		t.Fatalf("逐批可退本金 / 作废赠送不正确：%+v / %+v", first, second)
	}
	if quote.RefundablePrincipalCents != 8000 || quote.ForfeitedBonusCents != 0 {
		t.Fatalf("合计应与账户派生余额一致：%+v", quote)
	}
	if quote.BatchPrincipalRemainingCents != balance.PrincipalCents ||
		quote.BatchBonusRemainingCents != balance.BonusCents {
		t.Fatalf("批次还原合计应与余额一致：%+v / %+v", quote.Basis, balance)
	}
	if len(quote.Anomalies) != 0 {
		t.Fatalf("正常账本不应报批次异常：%+v", quote.Anomalies)
	}
	if quote.Basis.Version != rechargeRefundQuoteVersion || quote.QuotedAt.IsZero() {
		t.Fatalf("试算应带口径版本与生成时刻：%+v", quote.Basis)
	}
	// 纯只读：重复试算结论一致，且不产生新账本 / 流水分摊。
	again, err := rechargeRefundQuoteFor(ctx, db, walletCustomerBalance, strconv.Itoa(customer.ID), now)
	if err != nil {
		t.Fatal(err)
	}
	if again.RefundablePrincipalCents != quote.RefundablePrincipalCents ||
		len(again.Batches) != len(quote.Batches) {
		t.Fatalf("重复试算结论应一致：%+v / %+v", again, quote)
	}
}

// 赠送过期批次的处理：已到期批次的剩余赠送标记为已过期并计入作废额、不折现；
// 到期作废流水写入后，批次剩余赠送与账户赠送余额同步归零。
func TestRechargeRefundQuoteExpiredBonusForfeited(t *testing.T) {
	db := openStoredValueDB(t, "esports_recharge_refund_quote_expired")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)

	// 手工造一个「本金 3000 + 赠送 2000」且赠送已于昨天到期的充值批次（活动充值路径当前不写批次，
	// 这里直接落账本 + 流水，等价于带有效期的充值批次）。
	expiredAt := now.AddDate(0, 0, -1)
	db.EsportsWalletTx.Create().SetTxNo("rrq-expired-tx").
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(customer.ID).
		SetEntryType(esportswallettx.EntryTypeRecharge).
		SetPrincipalDeltaCents(3000).SetBonusDeltaCents(2000).
		SetSourceType("manual_recharge").SetSourceID("rrq-expired-recharge").
		SetBonusExpiresAt(expiredAt).SaveX(ctx)
	db.EsportsLedgerEntry.Create().SetAccountType(walletCustomerBalance).SetAccountID(strconv.Itoa(customer.ID)).
		SetSourceType("wallet_tx").SetSourceID("rrq-expired-tx").SetEntryRole("recharge_batch").
		SetDeltaCents(5000).SetFundBatch("rrq-expired-recharge").SaveX(ctx)

	quote, err := rechargeRefundQuoteFor(ctx, db, walletCustomerBalance, strconv.Itoa(customer.ID), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(quote.Batches) != 1 {
		t.Fatalf("应还原一个批次：%+v", quote.Batches)
	}
	batch := quote.Batches[0]
	if batch.BonusExpiresAt == nil || !batch.BonusExpired {
		t.Fatalf("批次的赠送应标记为已过期：%+v", batch)
	}
	if batch.RefundableCents != 3000 || batch.BonusForfeitedCents != 2000 {
		t.Fatalf("过期赠送应计入作废、不折现为可退本金：%+v", batch)
	}
	if quote.RefundablePrincipalCents != 3000 || quote.ForfeitedBonusCents != 2000 {
		t.Fatalf("合计不正确：%+v", quote)
	}
	if len(quote.Anomalies) != 0 {
		t.Fatalf("批次与余额一致时不应报异常：%+v", quote.Anomalies)
	}

	// 到期作废任务写入负数赠送流水后，批次剩余赠送与账户赠送余额同步归零。
	if _, err := expireOwnerBonus(ctx, db, customer.ID, now); err != nil {
		t.Fatal(err)
	}
	after, err := rechargeRefundQuoteFor(ctx, db, walletCustomerBalance, strconv.Itoa(customer.ID), now)
	if err != nil {
		t.Fatal(err)
	}
	if after.Batches[0].BonusRemainingCents != 0 || after.ForfeitedBonusCents != 0 ||
		after.RefundablePrincipalCents != 3000 {
		t.Fatalf("作废后批次赠送应归零、本金不变：%+v", after)
	}
}

// 储值卡退卡的批次维度试算与既有退卡试算口径一致（未消费本金可退、未消费赠送作废），
// 并给出预付款批次（account_type=stored_value_prepay）的逐批还原明细。
func TestRechargeRefundQuoteStoredValueCardMatchesRefundQuote(t *testing.T) {
	db := openStoredValueDB(t, "esports_recharge_refund_quote_card")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)

	card, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "RRQ-CARD-1", PrincipalCents: 10000, BonusCents: 2000,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "rrq-card-open-1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	sku, err := saveSku(ctx, db, skuInput{
		Code: "RRQ-CARD", Title: "储值卡批次试算商品", ServiceType: "play", UnitPriceCents: 3000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"rrq-card-order-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := payOrderWithStoredValueCard(ctx, db, 11, order.ID, card.ID, now); err != nil {
		t.Fatal(err)
	}
	// 先赠送后本金：卡消费 3000 → 赠送扣 2000、本金扣 1000，余本金 9000 / 赠送 0。
	_, refundable, err := storedValueRefundQuoteFor(ctx, db, card.EsportsStoredValueCard, now)
	if err != nil {
		t.Fatal(err)
	}
	if refundable != 9000 {
		t.Fatalf("既有退卡试算可退本金应为 9000，got=%d", refundable)
	}

	quote, err := rechargeRefundQuoteFor(ctx, db, storedValuePrepayAccount, strconv.Itoa(card.ID), now)
	if err != nil {
		t.Fatalf("储值卡批次试算应成功，err=%v", err)
	}
	if quote.CardNo != card.CardNo || len(quote.Batches) != 1 {
		t.Fatalf("应定位到该卡的开卡批次：%+v", quote)
	}
	batch := quote.Batches[0]
	if batch.EntryRole != "card_open" || batch.OriginalCents != 12000 ||
		batch.PrincipalCents != 10000 || batch.BonusCents != 2000 {
		t.Fatalf("开卡批次拆分不正确：%+v", batch)
	}
	if batch.AllocatedCents != 3000 || batch.RemainingByAllocationCents != 9000 {
		t.Fatalf("卡消费应落批次分摊：%+v", batch)
	}
	if batch.RefundableCents != 9000 || batch.BonusForfeitedCents != 0 {
		t.Fatalf("逐批可退 / 作废不正确：%+v", batch)
	}
	// 与既有退卡试算同口径：可退本金一致、未消费赠送全部作废。
	if quote.RefundablePrincipalCents != refundable {
		t.Fatalf("批次试算可退本金 %d 应与既有退卡试算 %d 一致",
			quote.RefundablePrincipalCents, refundable)
	}
	if quote.ForfeitedBonusCents != 0 {
		t.Fatalf("赠送已被消费，作废额应为 0：%+v", quote)
	}
	if len(quote.Anomalies) != 0 {
		t.Fatalf("正常卡不应报批次异常：%+v", quote.Anomalies)
	}

	// 未消费的卡：可退本金 = 开卡本金，作废赠送 = 开卡赠送，同样与既有试算一致。
	fresh, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "RRQ-CARD-2", PrincipalCents: 5000, BonusCents: 1000,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "rrq-card-open-2",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	freshQuote, err := rechargeRefundQuoteFor(ctx, db, storedValuePrepayAccount, strconv.Itoa(fresh.ID), now)
	if err != nil {
		t.Fatal(err)
	}
	if freshQuote.RefundablePrincipalCents != 5000 || freshQuote.ForfeitedBonusCents != 1000 {
		t.Fatalf("未消费卡应可退本金 5000、作废赠送 1000：%+v", freshQuote)
	}
}

// 批次数据异常：分摊额超过批次额时报明确异常并标记（不静默给出错误结论）。
func TestRechargeRefundQuoteFlagsOverAllocatedBatch(t *testing.T) {
	db := openStoredValueDB(t, "esports_recharge_refund_quote_overallocation")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 5000, Note: "充值"}, "rrq-recharge-over"); err != nil {
		t.Fatal(err)
	}
	// 伪造一条大于批次金额的分摊（正常业务路径的 available 判定写不出来）。
	batch := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.EntryRoleEQ("recharge_batch")).OnlyX(ctx)
	db.EsportsLedgerAllocation.Create().
		SetAccountType(walletCustomerBalance).SetAccountID(strconv.Itoa(customer.ID)).
		SetConsumeSourceType("order").SetConsumeSourceID("RRQ-FORGED-OVER").
		SetBatchSourceType(batch.SourceType).SetBatchSourceID(batch.SourceID).
		SetAmountCents(batch.DeltaCents + 2000).SaveX(ctx)

	quote, err := rechargeRefundQuoteFor(ctx, db, walletCustomerBalance, strconv.Itoa(customer.ID), now)
	if err != nil {
		t.Fatalf("试算本身不应失败，异常应体现在结果里，err=%v", err)
	}
	if len(quote.Anomalies) != 1 || quote.Anomalies[0].Kind != refundQuoteAnomalyOverAllocated {
		t.Fatalf("应报分摊超批次额异常：%+v", quote.Anomalies)
	}
	batchQuote := quote.Batches[0]
	if !batchQuote.OverAllocated || batchQuote.RemainingByAllocationCents != 0 {
		t.Fatalf("超额批次应标记且纯分摊剩余归零：%+v", batchQuote)
	}
	// 余额口径仍然可信：账户本金余额 5000，无余额变动 → 可退本金仍是 5000。
	if quote.RefundablePrincipalCents != 5000 || batchQuote.PrincipalRemainingCents != 5000 {
		t.Fatalf("异常数据不应污染余额口径结论：%+v", quote)
	}
}

// 账户余额大于批次合计（存在未落批次的正向资金）时显式报异常，不静默按批次结论对外。
func TestRechargeRefundQuoteFlagsUnbatchedBalance(t *testing.T) {
	db := openStoredValueDB(t, "esports_recharge_refund_quote_unbatched")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 5000, Note: "充值"}, "rrq-recharge-batch"); err != nil {
		t.Fatal(err)
	}
	// 活动充值当前只写钱包流水、不写批次账本：模拟一笔 3000 的未落批次资金。
	db.EsportsWalletTx.Create().SetTxNo("rrq-campaign-tx").
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(customer.ID).
		SetEntryType(esportswallettx.EntryTypeRecharge).
		SetPrincipalDeltaCents(3000).SetBonusDeltaCents(0).
		SetSourceType(campaignSourceType).SetSourceID("rrq-campaign-recharge").SaveX(ctx)

	quote, err := rechargeRefundQuoteFor(ctx, db, walletCustomerBalance, strconv.Itoa(customer.ID), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(quote.Batches) != 1 || quote.Batches[0].PrincipalRemainingCents != 5000 {
		t.Fatalf("应只还原落批次的部分：%+v", quote.Batches)
	}
	if len(quote.Anomalies) != 1 || quote.Anomalies[0].Kind != refundQuoteAnomalyUnbatchedFunds {
		t.Fatalf("应报余额无批次可追溯异常：%+v", quote.Anomalies)
	}
	if quote.RefundablePrincipalCents != 8000 || quote.BatchPrincipalRemainingCents != 5000 {
		t.Fatalf("合计取余额口径、批次合计取批次口径：%+v", quote)
	}
}

// 试算入参校验：账户类型不支持或账户不存在时分别返回参数错误与 NotFound。
func TestRechargeRefundQuoteValidatesAccount(t *testing.T) {
	db := openStoredValueDB(t, "esports_recharge_refund_quote_validate")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	if _, err := rechargeRefundQuoteFor(ctx, db, walletPlayerCatfood, "1", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("陪玩猫粮账户无退款语义，应返回参数错误，err=%v", err)
	}
	if _, err := rechargeRefundQuoteFor(ctx, db, walletCustomerBalance, "0", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法账户 ID 应返回参数错误，err=%v", err)
	}
	if _, err := rechargeRefundQuoteFor(ctx, db, walletCustomerBalance, "999", now); !ent.IsNotFound(err) {
		t.Fatalf("账户不存在应返回 NotFound，err=%v", err)
	}
}

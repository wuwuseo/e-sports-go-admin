package esports

import (
	"context"
	"io"
	"testing"
	"time"

	"go-admin/internal/ent/esportspayoutbatch"
	"go-admin/internal/ent/esportswithdrawal"
	"go-admin/internal/ent/esportswithdrawalpayout"
)

// ── 定时查单补偿 ──

// payoutBatchManualProvider 非云账户渠道的假服务商（只用于验证任务整体跳过）。
type payoutBatchManualProvider struct{ *payoutBatchFakeProvider }

func (p *payoutBatchManualProvider) Name() string { return payoutProviderManual }

// 提交结果未知且已超 2 分钟的批次会被定时任务收敛：查单到账 → 批次 settled、提现单 paid。
func TestPayoutBatchTaskConvergesStaleSubmitting(t *testing.T) {
	db := openWithdrawalDB(t, "esports_payout_task_stale")
	defer db.Close()
	ctx := t.Context()
	withdrawal := seedBatchWithdrawal(t, db, 9701, "pb-task-stale", 4000, 7, true)
	// 直接造一个「提交结果未知且已超 2 分钟」的 submitting 批次与 pay_requested 明细
	// （created_at 在 ent 里不可更新，只能建单时指定）。
	batch := db.EsportsPayoutBatch.Create().
		SetBatchNo("PB-TASK-STALE").
		SetIdemKey("yunzhanghu:" + currentPayoutWeek() + ":task-stale").
		SetProvider(payoutProviderYunzhanghu).
		SetStatus(esportspayoutbatch.StatusSubmitting).
		SetTotalCount(1).
		SetTotalCents(4000).
		SetCreatedAt(time.Now().UTC().Add(-3 * time.Minute)).
		SaveX(ctx)
	db.EsportsWithdrawalPayout.Create().
		SetPayoutNo("WP-TASK-STALE").
		SetWithdrawalID(withdrawal.ID).
		SetBatchID(batch.ID).
		SetProvider(payoutProviderYunzhanghu).
		SetProviderOrderNo("PB-TASK-STALE-1").
		SetChannel(esportswithdrawalpayout.ChannelBankCard).
		SetTransactionNo("PB-TASK-STALE-1").
		SetAmountCents(4000).
		SetStatus(esportswithdrawalpayout.StatusPayRequested).
		SetSubmittedByAdminID(9).
		SaveX(ctx)

	paid := &payoutBatchFakeProvider{query: func(_ context.Context, orderNo string) (*payoutOrderResult, error) {
		return &payoutOrderResult{OrderNo: orderNo, Status: yunzhanghuPayoutPaidStatus, PaidAt: time.Now().UTC()}, nil
	}}
	run := esportsSyncPayoutBatches(ctx, db, paid, time.Now(), io.Discard)
	if run.Scanned != 1 || run.Converged != 1 || run.Failed != 0 {
		t.Fatalf("查单补偿汇总不正确：%+v", run)
	}
	reloaded := db.EsportsPayoutBatch.GetX(ctx, batch.ID)
	if reloaded.Status != esportspayoutbatch.StatusSettled || reloaded.SettledAt == nil {
		t.Fatalf("全部到账后批次应为 settled：%+v", reloaded)
	}
	payouts := payoutBatchPayouts(t, db, batch.ID)
	if len(payouts) != 1 || payouts[0].Status != esportswithdrawalpayout.StatusPaid || payouts[0].PaidAt == nil {
		t.Fatalf("打款明细未落账：%+v", payouts)
	}
	if reload := db.EsportsWithdrawal.GetX(ctx, withdrawal.ID); reload.Status != esportswithdrawal.StatusPaid {
		t.Fatalf("提现单应同步置 paid，got=%s", reload.Status)
	}
}

// 查单明确失败：明细 pay_failed + fail_code，批次 failed，提现单保持 approved。
func TestPayoutBatchTaskMarksExplicitFailure(t *testing.T) {
	db := openWithdrawalDB(t, "esports_payout_task_failed")
	defer db.Close()
	ctx := t.Context()
	withdrawal := seedBatchWithdrawal(t, db, 9711, "pb-task-failed", 4000, 7, true)

	batch, _, err := createPayoutBatch(ctx, db, payoutBatchAcceptAll(), 9, currentPayoutWeek(), nil, time.Now())
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	failed := &payoutBatchFakeProvider{query: func(_ context.Context, orderNo string) (*payoutOrderResult, error) {
		return &payoutOrderResult{
			OrderNo: orderNo, Status: yunzhanghuPayoutFailedStatus,
			FailCode: "ACCOUNT_CLOSED", FailMsg: "收款账户已注销",
		}, nil
	}}
	run := esportsSyncPayoutBatches(ctx, db, failed, time.Now(), io.Discard)
	if run.Converged != 1 || run.Failed != 0 {
		t.Fatalf("查单补偿汇总不正确：%+v", run)
	}
	reloaded := db.EsportsPayoutBatch.GetX(ctx, batch.ID)
	if reloaded.Status != esportspayoutbatch.StatusFailed {
		t.Fatalf("全部失败后批次应为 failed，got=%s", reloaded.Status)
	}
	payouts := payoutBatchPayouts(t, db, batch.ID)
	if len(payouts) != 1 || payouts[0].Status != esportswithdrawalpayout.StatusPayFailed ||
		payouts[0].FailCode != "ACCOUNT_CLOSED" {
		t.Fatalf("明细应为 pay_failed 且带 fail_code：%+v", payouts)
	}
	if reload := db.EsportsWithdrawal.GetX(ctx, withdrawal.ID); reload.Status != esportswithdrawal.StatusApproved {
		t.Fatalf("明确失败时提现单应保持 approved，got=%s", reload.Status)
	}
}

// 查单 pending：明细保持 pay_requested，批次与提现单都不变。
func TestPayoutBatchTaskKeepsPending(t *testing.T) {
	db := openWithdrawalDB(t, "esports_payout_task_pending")
	defer db.Close()
	ctx := t.Context()
	withdrawal := seedBatchWithdrawal(t, db, 9721, "pb-task-pending", 4000, 7, true)

	batch, _, err := createPayoutBatch(ctx, db, payoutBatchAcceptAll(), 9, currentPayoutWeek(), nil, time.Now())
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	pending := &payoutBatchFakeProvider{query: func(_ context.Context, orderNo string) (*payoutOrderResult, error) {
		return &payoutOrderResult{OrderNo: orderNo, Status: yunzhanghuPayoutPendingStatus}, nil
	}}
	run := esportsSyncPayoutBatches(ctx, db, pending, time.Now(), io.Discard)
	if run.Converged != 1 || run.Failed != 0 {
		t.Fatalf("查单补偿汇总不正确：%+v", run)
	}
	reloaded := db.EsportsPayoutBatch.GetX(ctx, batch.ID)
	if reloaded.Status != esportspayoutbatch.StatusSubmitted || reloaded.SettledAt != nil {
		t.Fatalf("pending 时批次应保持 submitted：%+v", reloaded)
	}
	for _, payout := range payoutBatchPayouts(t, db, batch.ID) {
		if payout.Status != esportswithdrawalpayout.StatusPayRequested {
			t.Fatalf("pending 时明细应保持 pay_requested，got=%s", payout.Status)
		}
	}
	if reload := db.EsportsWithdrawal.GetX(ctx, withdrawal.ID); reload.Status != esportswithdrawal.StatusApproved {
		t.Fatalf("pending 时提现单应保持 approved，got=%s", reload.Status)
	}
}

// 当前渠道不是云账户：整轮跳过且不报错，不改任何状态。
func TestPayoutBatchTaskSkipsNonYunzhanghuChannel(t *testing.T) {
	db := openWithdrawalDB(t, "esports_payout_task_manual")
	defer db.Close()
	ctx := t.Context()
	seedBatchWithdrawal(t, db, 9731, "pb-task-manual", 4000, 7, true)

	batch, _, err := createPayoutBatch(ctx, db, payoutBatchAcceptAll(), 9, currentPayoutWeek(), nil, time.Now())
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	queries := 0
	inner := &payoutBatchFakeProvider{query: func(_ context.Context, orderNo string) (*payoutOrderResult, error) {
		queries++
		return &payoutOrderResult{OrderNo: orderNo, Status: yunzhanghuPayoutPaidStatus}, nil
	}}
	run := esportsSyncPayoutBatches(ctx, db, &payoutBatchManualProvider{inner}, time.Now(), io.Discard)
	if run != (payoutBatchSyncRun{}) {
		t.Fatalf("非云账户渠道应整轮跳过：%+v", run)
	}
	if queries != 0 {
		t.Fatalf("非云账户渠道不应发起查单，got=%d 次", queries)
	}
	reloaded := db.EsportsPayoutBatch.GetX(ctx, batch.ID)
	if reloaded.Status != esportspayoutbatch.StatusSubmitted {
		t.Fatalf("跳过时批次状态不得变化，got=%s", reloaded.Status)
	}
	if payout := payoutBatchPayouts(t, db, batch.ID)[0]; payout.Status != esportswithdrawalpayout.StatusPayRequested {
		t.Fatalf("跳过时明细状态不得变化，got=%s", payout.Status)
	}
}

// 幂等：连续跑两轮，第二轮不再查单、不重复落账，版本号不变。
func TestPayoutBatchTaskIdempotent(t *testing.T) {
	db := openWithdrawalDB(t, "esports_payout_task_idem")
	defer db.Close()
	ctx := t.Context()
	seedBatchWithdrawal(t, db, 9741, "pb-task-idem", 4000, 7, true)

	batch, _, err := createPayoutBatch(ctx, db, payoutBatchAcceptAll(), 9, currentPayoutWeek(), nil, time.Now())
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	queries := 0
	paid := &payoutBatchFakeProvider{query: func(_ context.Context, orderNo string) (*payoutOrderResult, error) {
		queries++
		return &payoutOrderResult{OrderNo: orderNo, Status: yunzhanghuPayoutPaidStatus, PaidAt: time.Now().UTC()}, nil
	}}
	first := esportsSyncPayoutBatches(ctx, db, paid, time.Now(), io.Discard)
	if first.Converged != 1 || queries != 1 {
		t.Fatalf("首轮应收敛 1 批：%+v，查单 %d 次", first, queries)
	}
	settled := payoutBatchPayouts(t, db, batch.ID)[0]

	second := esportsSyncPayoutBatches(ctx, db, paid, time.Now(), io.Discard)
	if second != (payoutBatchSyncRun{}) {
		t.Fatalf("终态批次第二轮不应再被选中：%+v", second)
	}
	if queries != 1 {
		t.Fatalf("第二轮不应重复查单，got=%d 次", queries)
	}
	again := payoutBatchPayouts(t, db, batch.ID)[0]
	if again.Version != settled.Version || again.Status != esportswithdrawalpayout.StatusPaid {
		t.Fatalf("重复执行不得改写已落账明细：version %d→%d status=%s",
			settled.Version, again.Version, again.Status)
	}
}

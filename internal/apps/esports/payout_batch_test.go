package esports

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportspayoutbatch"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayerpayoutaccount"
	"go-admin/internal/ent/esportswithdrawal"
	"go-admin/internal/ent/esportswithdrawalpayout"
)

// ── 测试辅助 ──

// payoutBatchFakeProvider 记录提交 / 查单行为的假服务商。
type payoutBatchFakeProvider struct {
	submit func(ctx context.Context, req payoutBatchRequest) (*payoutBatchResult, error)
	query  func(ctx context.Context, orderNo string) (*payoutOrderResult, error)
}

func (f *payoutBatchFakeProvider) Name() string { return payoutProviderYunzhanghu }

func (f *payoutBatchFakeProvider) SubmitBatch(ctx context.Context,
	req payoutBatchRequest) (*payoutBatchResult, error) {
	if f.submit == nil {
		return &payoutBatchResult{}, nil
	}
	return f.submit(ctx, req)
}

func (f *payoutBatchFakeProvider) QueryOrder(ctx context.Context, orderNo string) (*payoutOrderResult, error) {
	if f.query == nil {
		return &payoutOrderResult{OrderNo: orderNo, Status: yunzhanghuPayoutPendingStatus}, nil
	}
	return f.query(ctx, orderNo)
}

func (f *payoutBatchFakeProvider) VerifyNotification(map[string]string, []byte) (*payoutNotification, error) {
	return nil, errInvalidInput
}

func (f *payoutBatchFakeProvider) NotifyAck(bool) (int, string) { return 200, "SUCCESS" }

// payoutBatchAcceptAll 全部受理的假服务商。
func payoutBatchAcceptAll() *payoutBatchFakeProvider {
	return &payoutBatchFakeProvider{submit: func(_ context.Context, req payoutBatchRequest) (*payoutBatchResult, error) {
		result := &payoutBatchResult{ProviderBatchNo: "PBN-" + req.BatchNo}
		for _, item := range req.Items {
			result.Accepted = append(result.Accepted, payoutAcceptedItem{OrderNo: item.OrderNo})
		}
		return result, nil
	}}
}

// payoutBatchRejectAt 第 rejectIndex 笔（从 0 开始）被服务商拒绝，其余受理。
func payoutBatchRejectAt(rejectIndex int) *payoutBatchFakeProvider {
	return &payoutBatchFakeProvider{submit: func(_ context.Context, req payoutBatchRequest) (*payoutBatchResult, error) {
		result := &payoutBatchResult{ProviderBatchNo: "PBN-" + req.BatchNo}
		for i, item := range req.Items {
			if i == rejectIndex {
				result.Rejected = append(result.Rejected, payoutRejectedItem{
					OrderNo: item.OrderNo, Code: "ACCT_INVALID", Msg: "收款账户非法",
				})
				continue
			}
			result.Accepted = append(result.Accepted, payoutAcceptedItem{OrderNo: item.OrderNo})
		}
		return result, nil
	}}
}

// currentPayoutWeek 返回当前 UTC 时间所在的 ISO 周（YYYY-Www）。
func currentPayoutWeek() string {
	year, week := time.Now().UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

// seedBatchWithdrawal 直接造一张「已审核通过」的提现单（reviewed_at=当前时间，落在本周）。
// withAccount=true 时先激活银行卡签约账户并把 payout_account_id 指向它。
func seedBatchWithdrawal(t *testing.T, db *ent.Client, userID int, key string, cents int64,
	reviewerID int, withAccount bool) *ent.EsportsWithdrawal {
	t.Helper()
	ctx := t.Context()
	player := seedPlayer(t, db, userID, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	if withAccount {
		if err := activatePlayerPayoutAccount(ctx, db, player.ID, "bank_card", "SIGN-"+key, "张三", "****1234"); err != nil {
			t.Fatalf("激活签约账户失败：%v", err)
		}
	}
	now := time.Now().UTC()
	item := db.EsportsWithdrawal.Create().
		SetWithdrawalNo("WD-" + key).
		SetPlayerID(player.ID).
		SetIdemKey("idem-" + key).
		SetAmountCents(cents).
		SetFeeCents(0).
		SetTaxCents(0).
		SetNetCents(cents).
		SetChannel(esportswithdrawal.ChannelBankCard).
		SetAccountRef("****1234").
		SetStatus(esportswithdrawal.StatusApproved).
		SetReviewedByAdminID(reviewerID).
		SetReviewedAt(now).
		SetCreatedAt(now).
		SaveX(ctx)
	if withAccount {
		account := db.EsportsPlayerPayoutAccount.Query().Where(
			esportsplayerpayoutaccount.PlayerIDEQ(player.ID),
			esportsplayerpayoutaccount.ProviderEQ(payoutProviderYunzhanghu),
		).OnlyX(ctx)
		item = db.EsportsWithdrawal.UpdateOneID(item.ID).SetPayoutAccountID(account.ID).SaveX(ctx)
	}
	return item
}

func payoutBatchPayouts(t *testing.T, db *ent.Client, batchID int) []*ent.EsportsWithdrawalPayout {
	t.Helper()
	return db.EsportsWithdrawalPayout.Query().Where(
		esportswithdrawalpayout.BatchIDEQ(batchID),
	).Order(ent.Asc(esportswithdrawalpayout.FieldID)).AllX(t.Context())
}

// ── 预览 ──

// 未签约 / 已停用 / 已有进行中打款执行单的单据跳过原因与金额汇总正确。
func TestPayoutBatchPreviewSkipReasons(t *testing.T) {
	db := openWithdrawalDB(t, "esports_payout_batch_preview")
	defer db.Close()
	ctx := t.Context()

	seedBatchWithdrawal(t, db, 9601, "pb-ready", 4000, 7, true)
	seedBatchWithdrawal(t, db, 9602, "pb-no-account", 3000, 7, false)
	inactive := seedBatchWithdrawal(t, db, 9603, "pb-inactive", 2000, 7, true)
	if err := unbindPlayerPayoutAccount(ctx, db, inactive.PlayerID, "bank_card"); err != nil {
		t.Fatalf("解约账户失败：%v", err)
	}
	busy := seedBatchWithdrawal(t, db, 9604, "pb-busy", 1000, 7, true)
	db.EsportsWithdrawalPayout.Create().
		SetPayoutNo("WP-BUSY-1").
		SetWithdrawalID(busy.ID).
		SetTransactionNo("TX-BUSY-1").
		SetChannel(esportswithdrawalpayout.ChannelBankCard).
		SetAmountCents(1000).
		SaveX(ctx)

	view, err := previewPayoutBatch(ctx, db, currentPayoutWeek())
	if err != nil {
		t.Fatalf("预览失败：%v", err)
	}
	if view.ReadyCount != 1 || view.ReadyTotalCents != 4000 || view.SkippedCount != 3 || view.SkippedTotalCents != 6000 {
		t.Fatalf("预览汇总不正确：%+v", view)
	}
	reasons := map[string]string{}
	for _, item := range view.Items {
		if item.PlayerName == "" {
			t.Fatalf("预览应补齐玩家名称：%+v", item)
		}
		reasons[item.WithdrawalNo] = item.SkipReason
	}
	want := map[string]string{
		"WD-pb-ready":      "",
		"WD-pb-no-account": payoutSkipNoAccount,
		"WD-pb-inactive":   payoutSkipAccountInactive,
		"WD-pb-busy":       payoutSkipActivePayout,
	}
	for no, reason := range want {
		if reasons[no] != reason {
			t.Fatalf("提现单 %s 跳过原因应为 %q，got %q", no, reason, reasons[no])
		}
	}
	if view.StartAt.Weekday() != time.Monday || view.EndAt.Sub(view.StartAt) != 7*24*time.Hour {
		t.Fatalf("周区间应为周一到下周一：%v ~ %v", view.StartAt, view.EndAt)
	}

	// 净额为 0 的跳过判定（schema 的 Positive 约束不允许落库 net_cents=0，这里直接校验判定函数）。
	if reason := payoutSkipReason(&ent.EsportsWithdrawal{NetCents: 0}, nil, false); reason != payoutSkipZeroNet {
		t.Fatalf("净额为 0 应跳过，got %q", reason)
	}
}

// ── 提交 ──

// 同一周连点两次只生成一个批次，第二次返回既有批次。
func TestPayoutBatchSubmitIdempotent(t *testing.T) {
	db := openWithdrawalDB(t, "esports_payout_batch_idem")
	defer db.Close()
	ctx := t.Context()
	week := currentPayoutWeek()
	seedBatchWithdrawal(t, db, 9611, "pb-idem-1", 4000, 7, true)
	seedBatchWithdrawal(t, db, 9612, "pb-idem-2", 3000, 7, true)

	first, existing, err := createPayoutBatch(ctx, db, payoutBatchAcceptAll(), 9, week, nil, time.Now())
	if err != nil {
		t.Fatalf("首次提交失败：%v", err)
	}
	if existing {
		t.Fatal("首次提交不应返回既有批次")
	}
	second, existing, err := createPayoutBatch(ctx, db, payoutBatchAcceptAll(), 9, week, nil, time.Now())
	if err != nil {
		t.Fatalf("重复提交失败：%v", err)
	}
	if !existing || second.ID != first.ID {
		t.Fatalf("重复提交应返回既有批次：existing=%v id=%d want=%d", existing, second.ID, first.ID)
	}
	if count := db.EsportsPayoutBatch.Query().CountX(ctx); count != 1 {
		t.Fatalf("同一周只应有一个批次，got=%d", count)
	}
	if first.Status != esportspayoutbatch.StatusSubmitted || first.TotalCount != 2 || first.TotalCents != 7000 ||
		first.ProviderBatchNo == nil || *first.ProviderBatchNo == "" ||
		first.SubmittedByAdminID == nil || *first.SubmittedByAdminID != 9 ||
		first.SubmittedAt == nil {
		t.Fatalf("批次提交结果不正确：%+v", first)
	}
	if payouts := payoutBatchPayouts(t, db, first.ID); len(payouts) != 2 {
		t.Fatalf("批次明细应为 2 笔，got=%d", len(payouts))
	}
}

// 指定单存在重复时整批拒绝，不落批次。
func TestPayoutBatchSubmitRejectsInconsistentSelection(t *testing.T) {
	db := openWithdrawalDB(t, "esports_payout_batch_inconsistent")
	defer db.Close()
	ctx := t.Context()
	item := seedBatchWithdrawal(t, db, 9621, "pb-dup", 4000, 7, true)

	_, _, err := createPayoutBatch(ctx, db, payoutBatchAcceptAll(), 9, currentPayoutWeek(),
		[]int{item.ID, item.ID}, time.Now())
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重复选单应整批拒绝并返回中文错误，got %v", err)
	}
	if count := db.EsportsPayoutBatch.Query().CountX(ctx); count != 0 {
		t.Fatalf("整批拒绝时不得落批次，got=%d", count)
	}
	if count := db.EsportsWithdrawalPayout.Query().CountX(ctx); count != 0 {
		t.Fatalf("整批拒绝时不得落打款明细，got=%d", count)
	}
}

// 部分被拒：批次 partial，失败明细 pay_failed，提现单保持 approved。
func TestPayoutBatchSubmitPartialFailure(t *testing.T) {
	db := openWithdrawalDB(t, "esports_payout_batch_partial")
	defer db.Close()
	ctx := t.Context()
	first := seedBatchWithdrawal(t, db, 9631, "pb-partial-1", 4000, 7, true)
	second := seedBatchWithdrawal(t, db, 9632, "pb-partial-2", 3000, 7, true)

	batch, _, err := createPayoutBatch(ctx, db, payoutBatchRejectAt(1), 9, currentPayoutWeek(), nil, time.Now())
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	if batch.Status != esportspayoutbatch.StatusPartial || !strings.Contains(batch.FailReason, "拒绝") {
		t.Fatalf("部分失败应为 partial，got %+v", batch)
	}
	payouts := payoutBatchPayouts(t, db, batch.ID)
	if len(payouts) != 2 {
		t.Fatalf("明细应为 2 笔，got=%d", len(payouts))
	}
	if payouts[0].Status != esportswithdrawalpayout.StatusPayRequested ||
		payouts[1].Status != esportswithdrawalpayout.StatusPayFailed || payouts[1].FailCode != "ACCT_INVALID" {
		t.Fatalf("明细状态不正确：%+v / %+v", payouts[0], payouts[1])
	}
	for _, id := range []int{first.ID, second.ID} {
		if reload := db.EsportsWithdrawal.GetX(ctx, id); reload.Status != esportswithdrawal.StatusApproved {
			t.Fatalf("拒付后提现单应保持 approved，got=%s", reload.Status)
		}
	}
}

// 服务商超时 / 结果未知：批次保持 submitting，明细保持 pay_requested（不得置失败）。
func TestPayoutBatchSubmitUnknownStaysSubmitting(t *testing.T) {
	db := openWithdrawalDB(t, "esports_payout_batch_unknown")
	defer db.Close()
	ctx := t.Context()
	seedBatchWithdrawal(t, db, 9641, "pb-unknown", 4000, 7, true)

	unknown := &payoutBatchFakeProvider{submit: func(context.Context, payoutBatchRequest) (*payoutBatchResult, error) {
		return nil, fmt.Errorf("%w：读取服务商响应超时", errPayoutProviderUnknown)
	}}
	batch, _, err := createPayoutBatch(ctx, db, unknown, 9, currentPayoutWeek(), nil, time.Now())
	if err != nil {
		t.Fatalf("结果未知不应返回业务错误：%v", err)
	}
	if batch.Status != esportspayoutbatch.StatusSubmitting || batch.FailReason != payoutBatchUnknownReason {
		t.Fatalf("结果未知批次应保持 submitting，got %+v", batch)
	}
	for _, payout := range payoutBatchPayouts(t, db, batch.ID) {
		if payout.Status != esportswithdrawalpayout.StatusPayRequested {
			t.Fatalf("结果未知时明细不得置失败，got=%s", payout.Status)
		}
	}
}

// ── 手动同步 ──

// 查单 paid → 打款单 paid、提现单 paid，批次 settled。
func TestPayoutBatchSyncSettlesPaid(t *testing.T) {
	db := openWithdrawalDB(t, "esports_payout_batch_sync")
	defer db.Close()
	ctx := t.Context()
	withdrawal := seedBatchWithdrawal(t, db, 9651, "pb-sync", 4000, 7, true)

	batch, _, err := createPayoutBatch(ctx, db, payoutBatchAcceptAll(), 9, currentPayoutWeek(), nil, time.Now())
	if err != nil {
		t.Fatalf("提交失败：%v", err)
	}
	paid := &payoutBatchFakeProvider{query: func(_ context.Context, orderNo string) (*payoutOrderResult, error) {
		return &payoutOrderResult{OrderNo: orderNo, Status: yunzhanghuPayoutPaidStatus, PaidAt: time.Now().UTC()}, nil
	}}
	updated, summary, err := syncPayoutBatch(ctx, db, paid, batch.ID, time.Now())
	if err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	if updated.Status != esportspayoutbatch.StatusSettled || updated.SettledAt == nil {
		t.Fatalf("全部到账后批次应为 settled，got %+v", updated)
	}
	if summary.ChangedCount != 1 || summary.PaidCount != 1 || summary.FailedCount != 0 || summary.PendingCount != 0 {
		t.Fatalf("同步摘要不正确：%+v", summary)
	}
	payouts := payoutBatchPayouts(t, db, batch.ID)
	if len(payouts) != 1 || payouts[0].Status != esportswithdrawalpayout.StatusPaid || payouts[0].PaidAt == nil {
		t.Fatalf("打款单未落账：%+v", payouts)
	}
	if reload := db.EsportsWithdrawal.GetX(ctx, withdrawal.ID); reload.Status != esportswithdrawal.StatusPaid ||
		reload.PaidAt == nil {
		t.Fatalf("提现单应同步置 paid：%+v", reload)
	}
}

// ── 失败重试 ──

// 只重试 pay_failed 明细：生成新批次、新单号，原失败明细保留审计。
func TestPayoutBatchRetryOnlyFailed(t *testing.T) {
	db := openWithdrawalDB(t, "esports_payout_batch_retry")
	defer db.Close()
	ctx := t.Context()
	seedBatchWithdrawal(t, db, 9661, "pb-retry-1", 4000, 7, true)
	seedBatchWithdrawal(t, db, 9662, "pb-retry-2", 3000, 7, true)

	original, _, err := createPayoutBatch(ctx, db, payoutBatchRejectAt(0), 9, currentPayoutWeek(), nil, time.Now())
	if err != nil {
		t.Fatalf("首次提交失败：%v", err)
	}
	originalPayouts := payoutBatchPayouts(t, db, original.ID)
	failed := originalPayouts[0]
	if failed.Status != esportswithdrawalpayout.StatusPayFailed || failed.ProviderOrderNo == nil {
		t.Fatalf("首笔应被拒付：%+v", failed)
	}

	retried, existing, err := retryPayoutBatch(ctx, db, payoutBatchAcceptAll(), 9, original.ID, time.Now())
	if err != nil {
		t.Fatalf("重试失败：%v", err)
	}
	if existing || retried.ID == original.ID || retried.BatchNo == original.BatchNo {
		t.Fatalf("重试应生成独立新批次：existing=%v id=%d", existing, retried.ID)
	}
	if retried.TotalCount != 1 || retried.TotalCents != 4000 || retried.Status != esportspayoutbatch.StatusSubmitted {
		t.Fatalf("重试批次应只含失败明细：%+v", retried)
	}
	retryPayouts := payoutBatchPayouts(t, db, retried.ID)
	if len(retryPayouts) != 1 || retryPayouts[0].ProviderOrderNo == nil ||
		*retryPayouts[0].ProviderOrderNo == *failed.ProviderOrderNo {
		t.Fatalf("重试明细应使用新服务商单号：%+v", retryPayouts)
	}
	// 原失败明细保留审计，不被复用或改写。
	kept := db.EsportsWithdrawalPayout.GetX(ctx, failed.ID)
	if kept.Status != esportswithdrawalpayout.StatusPayFailed || kept.BatchID == nil || *kept.BatchID != original.ID {
		t.Fatalf("原失败明细应保留在原批次：%+v", kept)
	}

	// 重复重试返回既有重试批次。
	again, existing, err := retryPayoutBatch(ctx, db, payoutBatchAcceptAll(), 9, original.ID, time.Now())
	if err != nil || !existing || again.ID != retried.ID {
		t.Fatalf("重复重试应返回既有重试批次：existing=%v err=%v", existing, err)
	}
}

// ── 职责分离 ──

// 提现单审核人不得同时是批量代发提交人。
func TestPayoutBatchRejectsSameReviewer(t *testing.T) {
	db := openWithdrawalDB(t, "esports_payout_batch_separation")
	defer db.Close()
	ctx := t.Context()
	seedBatchWithdrawal(t, db, 9671, "pb-separation", 4000, 7, true)

	_, _, err := createPayoutBatch(ctx, db, payoutBatchAcceptAll(), 7, currentPayoutWeek(), nil, time.Now())
	if !errors.Is(err, errForbidden) || !strings.Contains(err.Error(), "审核人") {
		t.Fatalf("审核人=提交人时应整批拒绝，got %v", err)
	}
	if count := db.EsportsPayoutBatch.Query().CountX(ctx); count != 0 {
		t.Fatalf("职责分离校验失败时不得落批次，got=%d", count)
	}
}

package esports

import (
	"context"
	"fmt"
	"io"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportspayoutbatch"
	"go-admin/internal/ent/esportswithdrawalpayout"
)

// 云账户代发批次定时查单补偿：服务商回调可能丢失，提交结果未知（submitting）或仍有
// pay_requested 明细（submitted / partial）的批次需要主动查单收敛，
// 否则批次会永久卡在中间状态、提现单也拿不到终态。
//
// ponytail: 单轮最多处理 payoutBatchSyncLimit 个批次，避免定时任务长时间占用数据库连接；
// 积压超限时按 id 升序在后续轮次继续处理，不需要额外排队表。

const (
	// payoutBatchSyncLimit 单轮最多处理的批次数（防止长事务与一轮跑太久）。
	payoutBatchSyncLimit = 50
	// payoutBatchSubmitGrace 提交结果未知的批次至少等待该时长再查单，避免与提交流程抢跑。
	payoutBatchSubmitGrace = 2 * time.Minute
)

// payoutBatchSyncRun 单轮查单补偿的汇总（供任务日志输出）。
type payoutBatchSyncRun struct {
	Scanned   int // 本轮选中的批次数
	Converged int // 成功查单并完成状态重算的批次数
	Skipped   int // 渠道非云账户等原因跳过的批次数
	Failed    int // 单批查单 / 落账失败的批次数
}

// esportsSyncPayoutBatches 定时查单补偿主体：逐批复用 syncPayoutBatch 的逐笔查单与落账逻辑，
// 与手动同步完全同口径。整体不返回错误：渠道未启用、读库失败、单批失败都只记录并继续，
// 避免一条坏数据让定时调度中断。
func esportsSyncPayoutBatches(ctx context.Context, db *ent.Client, provider payoutProvider,
	now time.Time, logger io.Writer) payoutBatchSyncRun {
	run := payoutBatchSyncRun{}
	// 当前渠道不是云账户（或适配器未启用）时整轮跳过，不报错。
	if provider == nil || provider.Name() != payoutProviderYunzhanghu {
		_, _ = fmt.Fprintln(logger, "当前打款渠道不是云账户，跳过代发批次查单")
		return run
	}
	due, err := listDuePayoutBatches(ctx, db, now)
	if err != nil {
		_, _ = fmt.Fprintf(logger, "读取待查单代发批次失败：%v\n", err)
		return run
	}
	run.Scanned = len(due)
	for _, batch := range due {
		if batch.Provider != payoutProviderYunzhanghu {
			run.Skipped++
			continue
		}
		updated, summary, syncErr := syncPayoutBatch(ctx, db, provider, batch.ID, now)
		if syncErr != nil {
			// 单批失败只记录，继续下一批。
			_, _ = fmt.Fprintf(logger, "代发批次 %s 查单失败：%v\n", batch.BatchNo, syncErr)
			run.Failed++
			continue
		}
		run.Converged++
		_, _ = fmt.Fprintf(logger, "代发批次 %s 查单：到账 %d、失败 %d、处理中 %d，状态 %s\n",
			batch.BatchNo, summary.PaidCount, summary.FailedCount, summary.PendingCount, updated.Status)
	}
	return run
}

// listDuePayoutBatches 选出本轮待查单的批次：
//   a) status=submitting 且创建已超过 payoutBatchSubmitGrace（提交时结果未知，等收敛）；
//   b) status=submitted / partial 且仍有 pay_requested 明细（重试前可能还有 pending）。
func listDuePayoutBatches(ctx context.Context, db *ent.Client, now time.Time) ([]*ent.EsportsPayoutBatch, error) {
	batches := make([]*ent.EsportsPayoutBatch, 0, payoutBatchSyncLimit)
	stale, err := db.EsportsPayoutBatch.Query().Where(
		esportspayoutbatch.StatusEQ(esportspayoutbatch.StatusSubmitting),
		esportspayoutbatch.CreatedAtLT(now.UTC().Add(-payoutBatchSubmitGrace)),
	).Order(ent.Asc(esportspayoutbatch.FieldID)).Limit(payoutBatchSyncLimit).All(ctx)
	if err != nil {
		return nil, err
	}
	batches = append(batches, stale...)
	remaining := payoutBatchSyncLimit - len(batches)
	if remaining <= 0 {
		return batches, nil
	}
	// 先由明细反查批次 id，避免反复扫描没有待收敛明细的终态批次。
	rows, err := db.EsportsWithdrawalPayout.Query().Where(
		esportswithdrawalpayout.StatusEQ(esportswithdrawalpayout.StatusPayRequested),
		esportswithdrawalpayout.BatchIDNotNil(),
	).Select(esportswithdrawalpayout.FieldBatchID).All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		if row.BatchID != nil {
			ids = append(ids, *row.BatchID)
		}
	}
	if len(ids) == 0 {
		return batches, nil
	}
	pending, err := db.EsportsPayoutBatch.Query().Where(
		esportspayoutbatch.IDIn(uniquePositiveIDs(ids)...),
		esportspayoutbatch.StatusIn(esportspayoutbatch.StatusSubmitted, esportspayoutbatch.StatusPartial),
	).Order(ent.Asc(esportspayoutbatch.FieldID)).Limit(remaining).All(ctx)
	if err != nil {
		return nil, err
	}
	return append(batches, pending...), nil
}

package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/esportspayoutbatch"
	"go-admin/internal/ent/esportsplayerpayoutaccount"
	"go-admin/internal/ent/esportswithdrawal"
	"go-admin/internal/ent/esportswithdrawalpayout"
	"go-admin/internal/global"
)

// 云账户批量代发编排：预览 → 一键提交 → 列表 / 明细 → 手动同步 → 失败重试。
//
// 口径说明：
//   - 周区间按 ISO 周（周一为一周开始）。提现单没有 approved_at 字段，这里以
//     reviewed_at（审核通过时间）落在 [周一 00:00, 下周一 00:00) 为口径；
//     历史数据 reviewed_at 为空的 approved 单不会进入本周批次（需人工补录审核时间）。
//   - 幂等键统一为 "yunzhanghu:<周>"：同一周同一服务商只有一个批次，重复提交返回既有批次；
//     重试批次使用 "yunzhanghu:<周>:retry:<原批次 id>"，独立编号与独立状态。
//   - 金额一律 int64 分，比较不经过浮点。

const (
	payoutBatchIdemPrefix = payoutProviderYunzhanghu + ":"
	// payoutBatchNoPrefix 批次号前缀，批次号本身带 UUID 保证唯一。
	payoutBatchNoPrefix = "PB"
	// payoutBatchUnknownReason 提交结果未知时写入批次失败原因，等待查单任务收敛。
	payoutBatchUnknownReason = "结果未知待查单"

	// 预览跳过原因（与前端展示文案一致）。
	payoutSkipNoAccount       = "未签约收款账户"
	payoutSkipAccountInactive = "签约账户已停用"
	payoutSkipZeroNet         = "净额为 0"
	payoutSkipActivePayout    = "已有进行中的打款执行单"
)

// payoutBatchPreviewItem 预览中的单笔提现。
type payoutBatchPreviewItem struct {
	WithdrawalNo    string `json:"withdrawal_no"`
	PlayerID        int    `json:"player_id"`
	PlayerName      string `json:"player_name"`
	NetCents        int64  `json:"net_cents"`
	AccountType     string `json:"account_type"`
	AccountNoMasked string `json:"account_no_masked"`
	Ready           bool   `json:"ready"`
	SkipReason      string `json:"skip_reason,omitempty"`
}

// payoutBatchPreview 一周的代发预览汇总。
type payoutBatchPreview struct {
	Week              string                   `json:"week"`
	StartAt           time.Time                `json:"start_at"`
	EndAt             time.Time                `json:"end_at"`
	Items             []payoutBatchPreviewItem `json:"items"`
	ReadyCount        int                      `json:"ready_count"`
	ReadyTotalCents   int64                    `json:"ready_total_cents"`
	SkippedCount      int                      `json:"skipped_count"`
	SkippedTotalCents int64                    `json:"skipped_total_cents"`
}

// payoutCandidate 可代发单（内部编排用）：携带提现单与签约账户快照。
type payoutCandidate struct {
	withdrawal *ent.EsportsWithdrawal
	account    *ent.EsportsPlayerPayoutAccount
	ready      bool
	skipReason string
}

// payoutBatchSyncSummary 手动同步的逐笔结果摘要。
type payoutBatchSyncSummary struct {
	ChangedCount int `json:"changed_count"`
	PaidCount    int `json:"paid_count"`
	FailedCount  int `json:"failed_count"`
	PendingCount int `json:"pending_count"`
}

// ── 周区间与可发判定 ──

// payoutWeekWindow 解析 YYYY-Www 为 ISO 周的 [周一, 下周一) 半开区间（UTC）。
// 非法格式 / 不存在的周（如 2026-W53）返回中文错误。
func payoutWeekWindow(raw string) (time.Time, time.Time, string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) != 8 || raw[4] != '-' || (raw[5] != 'W' && raw[5] != 'w') {
		return time.Time{}, time.Time{}, "", fmt.Errorf("%w：周格式应为 YYYY-Www，例如 2026-W43", errInvalidInput)
	}
	year, errYear := strconv.Atoi(raw[:4])
	week, errWeek := strconv.Atoi(raw[6:])
	if errYear != nil || errWeek != nil || year < 2000 || year > 2100 || week < 1 || week > 53 {
		return time.Time{}, time.Time{}, "", fmt.Errorf("%w：周格式应为 YYYY-Www，例如 2026-W43", errInvalidInput)
	}
	// ISO 8601：包含 1 月 4 日的那一周是第 1 周，该周的周一为起点。
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	offset := (int(jan4.Weekday()) + 6) % 7 // 周一=0
	start := jan4.AddDate(0, 0, -offset+(week-1)*7)
	if y, w := start.ISOWeek(); y != year || w != week {
		return time.Time{}, time.Time{}, "", fmt.Errorf("%w：%d 年没有第 %d 周（ISO 周）", errInvalidInput, year, week)
	}
	return start, start.AddDate(0, 0, 7), fmt.Sprintf("%04d-W%02d", year, week), nil
}

// payoutSkipReason 判定提现单是否可代发，返回空串表示可发。
// 非 active 的签约账户（pending / expired）统一归入「签约账户已停用」，避免出现无法代发的灰色状态。
func payoutSkipReason(withdrawal *ent.EsportsWithdrawal, account *ent.EsportsPlayerPayoutAccount,
	inProgress bool) string {
	if withdrawal.NetCents < 1 {
		return payoutSkipZeroNet
	}
	if withdrawal.PayoutAccountID == nil || account == nil {
		return payoutSkipNoAccount
	}
	if account.Status != esportsplayerpayoutaccount.StatusActive {
		return payoutSkipAccountInactive
	}
	if inProgress {
		return payoutSkipActivePayout
	}
	return ""
}

// buildPayoutCandidates 批量装载提现单的签约账户与进行中的打款执行单，逐单判定可发。
func buildPayoutCandidates(ctx context.Context, db *ent.Client,
	withdrawals []*ent.EsportsWithdrawal) ([]payoutCandidate, error) {
	accountIDs := make([]int, 0, len(withdrawals))
	withdrawalIDs := make([]int, 0, len(withdrawals))
	for _, item := range withdrawals {
		withdrawalIDs = append(withdrawalIDs, item.ID)
		if item.PayoutAccountID != nil {
			accountIDs = append(accountIDs, *item.PayoutAccountID)
		}
	}
	accounts := make(map[int]*ent.EsportsPlayerPayoutAccount, len(accountIDs))
	if ids := uniquePositiveIDs(accountIDs); len(ids) > 0 {
		rows, err := db.EsportsPlayerPayoutAccount.Query().Where(esportsplayerpayoutaccount.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			accounts[row.ID] = row
		}
	}
	inProgress := make(map[int]bool, len(withdrawalIDs))
	if ids := uniquePositiveIDs(withdrawalIDs); len(ids) > 0 {
		rows, err := db.EsportsWithdrawalPayout.Query().Where(
			esportswithdrawalpayout.WithdrawalIDIn(ids...),
			esportswithdrawalpayout.StatusEQ(esportswithdrawalpayout.StatusPayRequested),
		).Select(esportswithdrawalpayout.FieldWithdrawalID).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			inProgress[row.WithdrawalID] = true
		}
	}
	candidates := make([]payoutCandidate, 0, len(withdrawals))
	for _, item := range withdrawals {
		var account *ent.EsportsPlayerPayoutAccount
		if item.PayoutAccountID != nil {
			account = accounts[*item.PayoutAccountID]
		}
		reason := payoutSkipReason(item, account, inProgress[item.ID])
		candidates = append(candidates, payoutCandidate{
			withdrawal: item, account: account, ready: reason == "", skipReason: reason,
		})
	}
	return candidates, nil
}

// listPayoutWeekWithdrawals 取周内已审核通过的提现单（口径同文件头：reviewed_at）。
func listPayoutWeekWithdrawals(ctx context.Context, db *ent.Client, start, end time.Time,
	ids []int) ([]*ent.EsportsWithdrawal, error) {
	q := db.EsportsWithdrawal.Query().Where(
		esportswithdrawal.StatusEQ(esportswithdrawal.StatusApproved),
		esportswithdrawal.ReviewedAtGTE(start),
		esportswithdrawal.ReviewedAtLT(end),
	)
	if len(ids) > 0 {
		q.Where(esportswithdrawal.IDIn(ids...))
	}
	return q.Order(ent.Asc(esportswithdrawal.FieldID)).All(ctx)
}

// previewPayoutBatch 一周代发预览：逐单给出可发与跳过原因，并汇总金额。
func previewPayoutBatch(ctx context.Context, db *ent.Client, weekRaw string) (*payoutBatchPreview, error) {
	start, end, week, err := payoutWeekWindow(weekRaw)
	if err != nil {
		return nil, err
	}
	withdrawals, err := listPayoutWeekWithdrawals(ctx, db, start, end, nil)
	if err != nil {
		return nil, err
	}
	candidates, err := buildPayoutCandidates(ctx, db, withdrawals)
	if err != nil {
		return nil, err
	}
	playerIDs := make([]int, 0, len(withdrawals))
	for _, item := range withdrawals {
		playerIDs = append(playerIDs, item.PlayerID)
	}
	names, err := playerNamesOf(ctx, db, playerIDs)
	if err != nil {
		return nil, err
	}
	view := &payoutBatchPreview{
		Week: week, StartAt: start, EndAt: end,
		Items: make([]payoutBatchPreviewItem, 0, len(candidates)),
	}
	for _, candidate := range candidates {
		item := candidate.withdrawal
		line := payoutBatchPreviewItem{
			WithdrawalNo: item.WithdrawalNo, PlayerID: item.PlayerID,
			PlayerName: names[item.PlayerID], NetCents: item.NetCents,
			Ready: candidate.ready, SkipReason: candidate.skipReason,
		}
		if candidate.account != nil {
			line.AccountType = candidate.account.AccountType.String()
			line.AccountNoMasked = candidate.account.AccountNoMasked
		}
		if candidate.ready {
			view.ReadyCount++
			view.ReadyTotalCents += item.NetCents
		} else {
			view.SkippedCount++
			view.SkippedTotalCents += item.NetCents
		}
		view.Items = append(view.Items, line)
	}
	return view, nil
}

// ── 提交与重试 ──

// payoutBatchNo 生成批次号：PB + UUID，保证批次号与明细单号唯一。
func payoutBatchNo() string { return payoutBatchNoPrefix + uuid.NewString() }

// payoutBatchWeekOf 从批次幂等键还原周标识（重试批次键带 :retry: 后缀）。
func payoutBatchWeekOf(batch *ent.EsportsPayoutBatch) string {
	raw := strings.TrimPrefix(batch.IdemKey, payoutBatchIdemPrefix)
	if i := strings.Index(raw, ":retry:"); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

// createPayoutBatch 按周一键提交批量代发：
//   - 幂等：idem_key = yunzhanghu:<周>，重复提交直接返回既有批次（existing=true）；
//   - 事务内重新查可发单，不信任预览结果；任一笔不可发整批拒绝；
//   - 逐笔创建 pay_requested 打款执行单，提交服务商后按结果回写批次 / 明细状态。
func createPayoutBatch(ctx context.Context, db *ent.Client, provider payoutProvider, adminID int,
	weekRaw string, withdrawalIDs []int, now time.Time) (*ent.EsportsPayoutBatch, bool, error) {
	if adminID < 1 {
		return nil, false, errInvalidInput
	}
	start, end, week, err := payoutWeekWindow(weekRaw)
	if err != nil {
		return nil, false, err
	}
	ids := uniquePositiveIDs(withdrawalIDs)
	if len(ids) != len(withdrawalIDs) {
		return nil, false, fmt.Errorf("%w：提现单选择存在重复，请刷新预览后重试", errInvalidInput)
	}
	// 幂等优先：既有批次先返回，避免二次点击因「明细已在打款中」被误判为不可发。
	idemKey := payoutBatchIdemPrefix + week
	if existing, findErr := db.EsportsPayoutBatch.Query().
		Where(esportspayoutbatch.IdemKeyEQ(idemKey)).Only(ctx); findErr == nil {
		return existing, true, nil
	} else if !ent.IsNotFound(findErr) {
		return nil, false, findErr
	}
	withdrawals, err := listPayoutWeekWithdrawals(ctx, db, start, end, ids)
	if err != nil {
		return nil, false, err
	}
	if len(ids) > 0 && len(withdrawals) != len(ids) {
		return nil, false, fmt.Errorf("%w：所选提现单不属于该周或已不处于已审核状态", errInvalidInput)
	}
	candidates, err := buildPayoutCandidates(ctx, db, withdrawals)
	if err != nil {
		return nil, false, err
	}
	ready := make([]payoutCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ready {
			ready = append(ready, candidate)
			continue
		}
		// 指定单时整批拒绝；不指定单时只跳过不可发单（预览里已明确提示原因）。
		if len(ids) > 0 {
			return nil, false, fmt.Errorf("%w：提现单 %s 不可代发（%s）",
				errInvalidInput, candidate.withdrawal.WithdrawalNo, candidate.skipReason)
		}
	}
	if len(ready) == 0 {
		return nil, false, fmt.Errorf("%w：该周没有可代发的提现单", errInvalidInput)
	}
	return createAndSubmitPayoutBatch(ctx, db, provider, adminID, idemKey, ready, now)
}

// retryPayoutBatch 仅重试原批次内 pay_failed 明细：新建批次与明细（保留原失败记录做审计），
// 幂等键 yunzhanghu:<周>:retry:<原批次 id>，重复重试返回既有重试批次。
func retryPayoutBatch(ctx context.Context, db *ent.Client, provider payoutProvider, adminID, batchID int,
	now time.Time) (*ent.EsportsPayoutBatch, bool, error) {
	if adminID < 1 || batchID < 1 {
		return nil, false, errInvalidInput
	}
	original, err := db.EsportsPayoutBatch.Get(ctx, batchID)
	if err != nil {
		return nil, false, err
	}
	failed, err := db.EsportsWithdrawalPayout.Query().Where(
		esportswithdrawalpayout.BatchIDEQ(batchID),
		esportswithdrawalpayout.StatusEQ(esportswithdrawalpayout.StatusPayFailed),
	).Order(ent.Asc(esportswithdrawalpayout.FieldID)).All(ctx)
	if err != nil {
		return nil, false, err
	}
	if len(failed) == 0 {
		return nil, false, fmt.Errorf("%w：原批次没有可重试的失败明细", errInvalidInput)
	}
	week := payoutBatchWeekOf(original)
	if _, _, _, err = payoutWeekWindow(week); err != nil {
		return nil, false, fmt.Errorf("%w：原批次缺少有效的周标识，无法重试", errInvalidInput)
	}
	idemKey := payoutBatchIdemPrefix + week + ":retry:" + strconv.Itoa(batchID)
	existing, err := db.EsportsPayoutBatch.Query().Where(esportspayoutbatch.IdemKeyEQ(idemKey)).Only(ctx)
	if err == nil {
		return existing, true, nil
	}
	if !ent.IsNotFound(err) {
		return nil, false, err
	}
	withdrawalIDs := make([]int, 0, len(failed))
	failedCents := make(map[int]int64, len(failed))
	for _, payout := range failed {
		withdrawalIDs = append(withdrawalIDs, payout.WithdrawalID)
		failedCents[payout.WithdrawalID] = payout.AmountCents
	}
	withdrawals, err := db.EsportsWithdrawal.Query().
		Where(esportswithdrawal.IDIn(uniquePositiveIDs(withdrawalIDs)...)).
		Order(ent.Asc(esportswithdrawal.FieldID)).All(ctx)
	if err != nil {
		return nil, false, err
	}
	candidates, err := buildPayoutCandidates(ctx, db, withdrawals)
	if err != nil {
		return nil, false, err
	}
	for _, candidate := range candidates {
		if !candidate.ready {
			return nil, false, fmt.Errorf("%w：提现单 %s 已不可重试代发（%s）",
				errInvalidInput, candidate.withdrawal.WithdrawalNo, candidate.skipReason)
		}
		// 提现单净额被改动时不得按新金额重发，避免超出原失败明细的金额口径。
		if failedCents[candidate.withdrawal.ID] != candidate.withdrawal.NetCents {
			return nil, false, fmt.Errorf("%w：提现单 %s 的净额与失败明细金额不一致，请人工核对",
				errConflict, candidate.withdrawal.WithdrawalNo)
		}
	}
	return createAndSubmitPayoutBatch(ctx, db, provider, adminID, idemKey, candidates, now)
}

// createAndSubmitPayoutBatch 落库批次与明细并提交服务商，两条入口（首提 / 重试）共用。
func createAndSubmitPayoutBatch(ctx context.Context, db *ent.Client, provider payoutProvider, adminID int,
	idemKey string, candidates []payoutCandidate, now time.Time) (*ent.EsportsPayoutBatch, bool, error) {
	if provider == nil {
		return nil, false, fmt.Errorf("%w：云账户代发未启用", errPayoutProviderDisabled)
	}
	existing, err := db.EsportsPayoutBatch.Query().Where(esportspayoutbatch.IdemKeyEQ(idemKey)).Only(ctx)
	if err == nil {
		return existing, true, nil
	}
	if !ent.IsNotFound(err) {
		return nil, false, err
	}
	if len(candidates) == 0 {
		return nil, false, fmt.Errorf("%w：没有可代发的提现单", errInvalidInput)
	}
	// 资金职责分离与金额口径：提现单审核人不得是本次提交人；金额以库内净额为准。
	var totalCents int64
	for _, candidate := range candidates {
		item := candidate.withdrawal
		if item.ReviewedByAdminID != nil && *item.ReviewedByAdminID == adminID {
			return nil, false, fmt.Errorf("%w：提现单 %s 的审核人不得提交批量代发", errForbidden, item.WithdrawalNo)
		}
		totalCents += item.NetCents
	}
	batchNo := payoutBatchNo()
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	batch, err := tx.EsportsPayoutBatch.Create().
		SetBatchNo(batchNo).
		SetIdemKey(idemKey).
		SetProvider(payoutProviderYunzhanghu).
		SetStatus(esportspayoutbatch.StatusSubmitting).
		SetTotalCount(len(candidates)).
		SetTotalCents(totalCents).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			// 并发连点：另一请求已先插入同一幂等键，返回既有批次。
			if existing, findErr := db.EsportsPayoutBatch.Query().
				Where(esportspayoutbatch.IdemKeyEQ(idemKey)).Only(ctx); findErr == nil {
				return existing, true, nil
			}
		}
		return nil, false, err
	}
	items := make([]payoutBatchItem, 0, len(candidates))
	for i, candidate := range candidates {
		item := candidate.withdrawal
		// 明细单号 = 批次号 + 序号；批次号含 UUID，保证跨批次唯一。
		orderNo := fmt.Sprintf("%s-%d", batchNo, i+1)
		if _, err = tx.EsportsWithdrawalPayout.Create().
			SetPayoutNo(withdrawalPayoutNo()).
			SetWithdrawalID(item.ID).
			SetBatchID(batch.ID).
			SetProvider(payoutProviderYunzhanghu).
			SetProviderOrderNo(orderNo).
			SetChannel(esportswithdrawalpayout.Channel(item.Channel)).
			SetTransactionNo(orderNo).
			SetAmountCents(item.NetCents).
			SetStatus(esportswithdrawalpayout.StatusPayRequested).
			SetSubmittedByAdminID(adminID).
			Save(ctx); err != nil {
			return nil, false, err
		}
		items = append(items, payoutBatchItem{
			OrderNo: orderNo, SignNo: candidate.account.ProviderSignNo,
			AmountCents: item.NetCents, AccountType: candidate.account.AccountType.String(),
			AccountName: candidate.account.AccountName, Remark: item.WithdrawalNo,
		})
	}
	// 事务内复核：落库批次的笔数与金额必须等于明细合计，不等则整批回滚。
	created, err := tx.EsportsPayoutBatch.Get(ctx, batch.ID)
	if err != nil {
		return nil, false, err
	}
	if created.TotalCount != len(items) || created.TotalCents != totalCents {
		return nil, false, fmt.Errorf("%w：批次金额与提现单净额合计不一致，已整批拒绝", errConflict)
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	result, submitErr := provider.SubmitBatch(ctx, payoutBatchRequest{BatchNo: batchNo, Items: items})
	return settlePayoutBatchSubmit(ctx, db, batch, adminID, result, submitErr, now)
}

// settlePayoutBatchSubmit 按服务商提交结果回写批次与明细：
//   - 明确拒绝：整批 failed，全部明细 pay_failed；
//   - 结果未知：批次保持 submitting，明细保持 pay_requested（等查单任务收敛，绝不置失败）；
//   - 成功：按逐笔 rejected / unknown 计算 submitted / partial / failed。
func settlePayoutBatchSubmit(ctx context.Context, db *ent.Client, batch *ent.EsportsPayoutBatch,
	adminID int, result *payoutBatchResult, submitErr error, now time.Time) (*ent.EsportsPayoutBatch, bool, error) {
	if submitErr != nil && errors.Is(submitErr, errPayoutProviderRejected) {
		reason := truncateText(submitErr.Error(), yunzhanghuPayoutFailReasonMaxRune)
		if _, err := db.EsportsWithdrawalPayout.Update().Where(
			esportswithdrawalpayout.BatchIDEQ(batch.ID),
			esportswithdrawalpayout.StatusEQ(esportswithdrawalpayout.StatusPayRequested),
		).SetStatus(esportswithdrawalpayout.StatusPayFailed).
			SetProviderStatus(yunzhanghuPayoutFailedStatus).
			SetFailReason(reason).AddVersion(1).Save(ctx); err != nil {
			return nil, false, err
		}
		if _, err := db.EsportsPayoutBatch.UpdateOneID(batch.ID).
			SetStatus(esportspayoutbatch.StatusFailed).
			SetFailReason(reason).
			SetSubmittedByAdminID(adminID).
			SetSubmittedAt(now.UTC()).AddVersion(1).Save(ctx); err != nil {
			return nil, false, err
		}
		reloaded, err := db.EsportsPayoutBatch.Get(ctx, batch.ID)
		return reloaded, false, err
	}
	if submitErr != nil {
		// 超时 / 网络 / 验签失败：结果未知，不能判失败。
		if _, err := db.EsportsPayoutBatch.UpdateOneID(batch.ID).
			SetFailReason(payoutBatchUnknownReason).AddVersion(1).Save(ctx); err != nil {
			return nil, false, err
		}
		reloaded, err := db.EsportsPayoutBatch.Get(ctx, batch.ID)
		return reloaded, false, err
	}
	rejectedCount := 0
	if result != nil {
		for _, rejected := range result.Rejected {
			reason := strings.TrimSpace(rejected.Msg)
			if reason == "" {
				reason = "服务商拒绝受理"
			}
			if _, err := db.EsportsWithdrawalPayout.Update().Where(
				esportswithdrawalpayout.BatchIDEQ(batch.ID),
				esportswithdrawalpayout.ProviderOrderNoEQ(rejected.OrderNo),
				esportswithdrawalpayout.StatusEQ(esportswithdrawalpayout.StatusPayRequested),
			).SetStatus(esportswithdrawalpayout.StatusPayFailed).
				SetProviderStatus(yunzhanghuPayoutFailedStatus).
				SetFailCode(truncateText(rejected.Code, yunzhanghuPayoutFailCodeMaxLen)).
				SetFailReason(truncateText(reason, yunzhanghuPayoutFailReasonMaxRune)).
				AddVersion(1).Save(ctx); err != nil {
				return nil, false, err
			}
			rejectedCount++
		}
		for _, accepted := range result.Accepted {
			if _, err := db.EsportsWithdrawalPayout.Update().Where(
				esportswithdrawalpayout.BatchIDEQ(batch.ID),
				esportswithdrawalpayout.ProviderOrderNoEQ(accepted.OrderNo),
				esportswithdrawalpayout.StatusEQ(esportswithdrawalpayout.StatusPayRequested),
			).SetProviderStatus("accepted").AddVersion(1).Save(ctx); err != nil {
				return nil, false, err
			}
		}
	}
	update := db.EsportsPayoutBatch.UpdateOneID(batch.ID).
		SetSubmittedByAdminID(adminID).SetSubmittedAt(now.UTC())
	if result != nil && strings.TrimSpace(result.ProviderBatchNo) != "" {
		update.SetProviderBatchNo(strings.TrimSpace(result.ProviderBatchNo))
	}
	switch {
	case batch.TotalCount > 0 && rejectedCount >= batch.TotalCount:
		update.SetStatus(esportspayoutbatch.StatusFailed).
			SetFailReason("服务商拒绝了全部打款明细")
	case rejectedCount > 0:
		update.SetStatus(esportspayoutbatch.StatusPartial).
			SetFailReason(fmt.Sprintf("部分打款明细被拒绝（%d 笔）", rejectedCount))
	default:
		update.SetStatus(esportspayoutbatch.StatusSubmitted)
	}
	if _, err := update.AddVersion(1).Save(ctx); err != nil {
		return nil, false, err
	}
	reloaded, err := db.EsportsPayoutBatch.Get(ctx, batch.ID)
	return reloaded, false, err
}

// ── 手动同步 ──

// syncPayoutBatch 对批次内 pay_requested 明细逐笔查单：
// paid → 复用公共落账（payout paid + 提现单 paid）；failed → pay_failed + fail_code；
// pending / 查单结果未知 → 不动并继续下一笔。最后按明细状态重算批次状态。
func syncPayoutBatch(ctx context.Context, db *ent.Client, provider payoutProvider, batchID int,
	now time.Time) (*ent.EsportsPayoutBatch, payoutBatchSyncSummary, error) {
	summary := payoutBatchSyncSummary{}
	if provider == nil {
		return nil, summary, fmt.Errorf("%w：云账户代发未启用", errPayoutProviderDisabled)
	}
	batch, err := db.EsportsPayoutBatch.Get(ctx, batchID)
	if err != nil {
		return nil, summary, err
	}
	payouts, err := db.EsportsWithdrawalPayout.Query().Where(
		esportswithdrawalpayout.BatchIDEQ(batchID),
		esportswithdrawalpayout.StatusEQ(esportswithdrawalpayout.StatusPayRequested),
	).Order(ent.Asc(esportswithdrawalpayout.FieldID)).All(ctx)
	if err != nil {
		return nil, summary, err
	}
	for _, payout := range payouts {
		before := payout.Status
		if payout.ProviderOrderNo == nil || strings.TrimSpace(*payout.ProviderOrderNo) == "" {
			summary.PendingCount++
			continue
		}
		orderNo := strings.TrimSpace(*payout.ProviderOrderNo)
		result, queryErr := provider.QueryOrder(ctx, orderNo)
		if queryErr != nil || result == nil {
			// 查单本身结果未知（超时 / 网络 / 验签失败）：本笔不动，继续下一笔。
			summary.PendingCount++
			continue
		}
		notice := &payoutNotification{
			OrderNo: orderNo, Status: result.Status, FailCode: result.FailCode,
			Msg: result.FailMsg, PaidAt: result.PaidAt,
		}
		if settleErr := settleYunzhanghuPayoutNotification(ctx, db, notice); settleErr != nil {
			// 单笔落账失败（已被其它流程处理 / 冲突）不影响后续明细。
			if !ent.IsNotFound(settleErr) && !errors.Is(settleErr, errConflict) {
				return nil, summary, settleErr
			}
			summary.PendingCount++
			continue
		}
		after, err := db.EsportsWithdrawalPayout.Get(ctx, payout.ID)
		if err != nil {
			return nil, summary, err
		}
		if after.Status != before {
			summary.ChangedCount++
		}
		switch after.Status {
		case esportswithdrawalpayout.StatusPaid:
			summary.PaidCount++
		case esportswithdrawalpayout.StatusPayFailed:
			summary.FailedCount++
		default:
			summary.PendingCount++
		}
	}
	updated, err := recomputePayoutBatchStatus(ctx, db, batch, now)
	if err != nil {
		return nil, summary, err
	}
	return updated, summary, nil
}

// recomputePayoutBatchStatus 按批次内明细状态重算批次状态：
// 全部 paid → settled；有 paid 有失败 → partial；全部失败 → failed；
// 仍有 pay_requested → submitted（原为 submitting 且已有结果的保持 submitting，交给定时任务兜底）。
func recomputePayoutBatchStatus(ctx context.Context, db *ent.Client, batch *ent.EsportsPayoutBatch,
	now time.Time) (*ent.EsportsPayoutBatch, error) {
	rows, err := db.EsportsWithdrawalPayout.Query().Where(
		esportswithdrawalpayout.BatchIDEQ(batch.ID),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	var paid, failed, requested int
	for _, row := range rows {
		switch row.Status {
		case esportswithdrawalpayout.StatusPaid:
			paid++
		case esportswithdrawalpayout.StatusPayFailed:
			failed++
		default:
			requested++
		}
	}
	status := batch.Status
	switch {
	case requested > 0:
		// 原为 submitting 的批次保持 submitting，由定时查单任务收敛后再改终态。
		if batch.Status != esportspayoutbatch.StatusSubmitting {
			status = esportspayoutbatch.StatusSubmitted
		}
	case paid > 0 && failed == 0:
		status = esportspayoutbatch.StatusSettled
	case paid > 0:
		status = esportspayoutbatch.StatusPartial
	case failed > 0:
		status = esportspayoutbatch.StatusFailed
	default:
		status = esportspayoutbatch.StatusSubmitted
	}
	if status == batch.Status && !(status == esportspayoutbatch.StatusSettled && batch.SettledAt == nil) {
		reloaded, err := db.EsportsPayoutBatch.Get(ctx, batch.ID)
		return reloaded, err
	}
	update := db.EsportsPayoutBatch.UpdateOneID(batch.ID).SetStatus(status).AddVersion(1)
	if status == esportspayoutbatch.StatusSettled && batch.SettledAt == nil {
		update.SetSettledAt(now.UTC())
	}
	if _, err = update.Save(ctx); err != nil {
		return nil, err
	}
	return db.EsportsPayoutBatch.Get(ctx, batch.ID)
}

// ── 查询视图 ──

// payoutBatchView 批次列表视图：附带提交管理员展示名。
type payoutBatchView struct {
	*ent.EsportsPayoutBatch
	SubmittedBy string `json:"submitted_by"`
}

// payoutBatchItemView 批次明细视图：补齐提现单号、陪玩名称与到账时间。
type payoutBatchItemView struct {
	PayoutID        int        `json:"payout_id"`
	PayoutNo        string     `json:"payout_no"`
	WithdrawalID    int        `json:"withdrawal_id"`
	WithdrawalNo    string     `json:"withdrawal_no"`
	PlayerID        int        `json:"player_id"`
	PlayerName      string     `json:"player_name"`
	AmountCents     int64      `json:"amount_cents"`
	Status          string     `json:"status"`
	ProviderOrderNo string     `json:"provider_order_no"`
	ProviderStatus  string     `json:"provider_status"`
	FailCode        string     `json:"fail_code"`
	FailReason      string     `json:"fail_reason"`
	TransactionNo   string     `json:"transaction_no"`
	PaidAt          *time.Time `json:"paid_at"`
}

// adminNamesOf 批量解析管理员展示名（昵称优先，回落用户名）。
func adminNamesOf(ctx context.Context, db *ent.Client, ids []int) (map[int]string, error) {
	uniq := uniquePositiveIDs(ids)
	out := make(map[int]string, len(uniq))
	if len(uniq) == 0 {
		return out, nil
	}
	rows, err := db.Admin.Query().Where(admin.IDIn(uniq...)).
		Select(admin.FieldID, admin.FieldUsername, admin.FieldNickname).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = adminDisplayName(row)
	}
	return out, nil
}

// payoutBatchViewsOf 批次列表 / 详情共用：批量取提交人名称。
func payoutBatchViewsOf(ctx context.Context, db *ent.Client,
	batches []*ent.EsportsPayoutBatch) ([]payoutBatchView, error) {
	ids := make([]int, 0, len(batches))
	for _, batch := range batches {
		if batch.SubmittedByAdminID != nil {
			ids = append(ids, *batch.SubmittedByAdminID)
		}
	}
	names, err := adminNamesOf(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	views := make([]payoutBatchView, 0, len(batches))
	for _, batch := range batches {
		name := ""
		if batch.SubmittedByAdminID != nil {
			name = names[*batch.SubmittedByAdminID]
		}
		views = append(views, payoutBatchView{EsportsPayoutBatch: batch, SubmittedBy: name})
	}
	return views, nil
}

// ── HTTP handlers ──

// payoutBatchProvider 读取当前渠道并做就绪检查；人工打款 / 配置不完整统一按未启用处理。
func payoutBatchProvider(ctx context.Context) (payoutProvider, error) {
	provider, err := newPayoutProvider(ctx, global.ORM)
	if err != nil {
		if errors.Is(err, errPayoutProviderDisabled) {
			return nil, fmt.Errorf("%w：云账户代发未启用", errPayoutProviderDisabled)
		}
		return nil, err
	}
	if yunzhanghu, ok := provider.(*yunzhanghuProvider); ok {
		if readyErr := yunzhanghu.ensureReady(); readyErr != nil {
			return nil, fmt.Errorf("%w：云账户代发未启用或配置不完整", errPayoutProviderDisabled)
		}
	}
	return provider, nil
}

// payoutBatchProviderError 未启用统一 503，其余按业务错误映射。
func payoutBatchProviderError(c fiber.Ctx, err error) error {
	if errors.Is(err, errPayoutProviderDisabled) {
		return fail(c, fiber.StatusServiceUnavailable, "云账户代发未启用")
	}
	return businessError(c, err)
}

// adminPayoutBatchPreview GET /admin/payout-batches/preview?week=2026-W43
func adminPayoutBatchPreview(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	if _, err := payoutBatchProvider(c.Context()); err != nil {
		return payoutBatchProviderError(c, err)
	}
	view, err := previewPayoutBatch(c.Context(), global.ORM, c.Query("week"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// createPayoutBatchHandler POST /admin/payout-batches
func createPayoutBatchHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	provider, err := payoutBatchProvider(c.Context())
	if err != nil {
		return payoutBatchProviderError(c, err)
	}
	var in struct {
		Week          string `json:"week"`
		WithdrawalIDs []int  `json:"withdrawal_ids"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	batch, existing, err := createPayoutBatch(c.Context(), global.ORM, provider, adminID, in.Week, in.WithdrawalIDs, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{
		"id": batch.ID, "batch_no": batch.BatchNo, "status": batch.Status.String(), "existing": existing,
	})
}

// adminPayoutBatches GET /admin/payout-batches（支持 ?status= 过滤与 page / limit 分页）
func adminPayoutBatches(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	window, err := parseAdminListWindow(c)
	if err != nil {
		return businessError(c, err)
	}
	query := global.ORM.EsportsPayoutBatch.Query()
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query.Where(esportspayoutbatch.StatusEQ(esportspayoutbatch.Status(status)))
	}
	total, err := query.Clone().Count(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	batches, err := query.Order(ent.Desc(esportspayoutbatch.FieldID)).
		Limit(window.limit).Offset(window.offset).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	views, err := payoutBatchViewsOf(c.Context(), global.ORM, batches)
	if err != nil {
		return businessError(c, err)
	}
	return writeAdminList(c, window, total, views)
}

// adminPayoutBatchDetail GET /admin/payout-batches/:id（批次头 + 明细）
func adminPayoutBatchDetail(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "代发批次")
	if err != nil {
		return businessError(c, err)
	}
	batch, err := global.ORM.EsportsPayoutBatch.Get(c.Context(), id)
	if err != nil {
		return businessError(c, err)
	}
	payouts, err := global.ORM.EsportsWithdrawalPayout.Query().Where(
		esportswithdrawalpayout.BatchIDEQ(id),
	).Order(ent.Asc(esportswithdrawalpayout.FieldID)).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	withdrawalIDs := make([]int, 0, len(payouts))
	for _, payout := range payouts {
		withdrawalIDs = append(withdrawalIDs, payout.WithdrawalID)
	}
	withdrawals := make(map[int]*ent.EsportsWithdrawal, len(withdrawalIDs))
	playerIDs := make([]int, 0, len(withdrawalIDs))
	if ids := uniquePositiveIDs(withdrawalIDs); len(ids) > 0 {
		rows, queryErr := global.ORM.EsportsWithdrawal.Query().Where(esportswithdrawal.IDIn(ids...)).All(c.Context())
		if queryErr != nil {
			return businessError(c, queryErr)
		}
		for _, row := range rows {
			withdrawals[row.ID] = row
			playerIDs = append(playerIDs, row.PlayerID)
		}
	}
	names, err := playerNamesOf(c.Context(), global.ORM, playerIDs)
	if err != nil {
		return businessError(c, err)
	}
	items := make([]payoutBatchItemView, 0, len(payouts))
	for _, payout := range payouts {
		line := payoutBatchItemView{
			PayoutID: payout.ID, PayoutNo: payout.PayoutNo, WithdrawalID: payout.WithdrawalID,
			AmountCents: payout.AmountCents, Status: payout.Status.String(),
			ProviderStatus: payout.ProviderStatus, FailCode: payout.FailCode,
			FailReason: payout.FailReason, TransactionNo: payout.TransactionNo, PaidAt: payout.PaidAt,
		}
		if payout.ProviderOrderNo != nil {
			line.ProviderOrderNo = *payout.ProviderOrderNo
		}
		if withdrawal := withdrawals[payout.WithdrawalID]; withdrawal != nil {
			line.WithdrawalNo = withdrawal.WithdrawalNo
			line.PlayerID = withdrawal.PlayerID
			line.PlayerName = names[withdrawal.PlayerID]
		}
		items = append(items, line)
	}
	views, err := payoutBatchViewsOf(c.Context(), global.ORM, []*ent.EsportsPayoutBatch{batch})
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"batch": views[0], "items": items})
}

// syncPayoutBatchHandler POST /admin/payout-batches/:id/sync
func syncPayoutBatchHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	provider, err := payoutBatchProvider(c.Context())
	if err != nil {
		return payoutBatchProviderError(c, err)
	}
	id, err := parsePathID(c, "代发批次")
	if err != nil {
		return businessError(c, err)
	}
	batch, summary, err := syncPayoutBatch(c.Context(), global.ORM, provider, id, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{
		"id": batch.ID, "batch_no": batch.BatchNo, "status": batch.Status.String(),
		"changed_count": summary.ChangedCount, "paid_count": summary.PaidCount,
		"failed_count": summary.FailedCount, "pending_count": summary.PendingCount,
	})
}

// retryPayoutBatchHandler POST /admin/payout-batches/:id/retry
func retryPayoutBatchHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	provider, err := payoutBatchProvider(c.Context())
	if err != nil {
		return payoutBatchProviderError(c, err)
	}
	id, err := parsePathID(c, "代发批次")
	if err != nil {
		return businessError(c, err)
	}
	batch, existing, err := retryPayoutBatch(c.Context(), global.ORM, provider, adminID, id, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{
		"id": batch.ID, "batch_no": batch.BatchNo, "status": batch.Status.String(),
		"source_batch_id": id, "existing": existing,
	})
}

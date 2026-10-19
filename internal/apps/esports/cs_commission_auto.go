package esports

import (
	"context"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportssettlement"
)

// 客服提成自动结算单（§12.2）：把「人工按周期发起」升级为定时任务按**上一完整周期**自动生成结算单。
// 归属周期：明细以计提时间（`created_at`）落入周期窗口，与人工发起同口径（§12.6 周期边界）。
// 只处理上一完整周期，不触碰进行中的当前周期；生成的是**草稿**结算单，锁定与登记发放仍由财务操作。

// csCommissionPreviousPeriodWindow 返回上一个完整周期窗口 [start, end)。
// 复用 csCommissionPeriodWindow：当前周期起点再往前 1 秒必然落在上一周期内。
func csCommissionPreviousPeriodWindow(period string, now time.Time) (time.Time, time.Time) {
	currentStart, _ := csCommissionPeriodWindow(period, now)
	previousStart, _ := csCommissionPeriodWindow(period, currentStart.Add(-time.Second))
	return previousStart, currentStart
}

// generateDueCsSettlements 为各客服按「上一完整周期」自动生成提成结算单，返回生成数量。
// 只对「有生效提成规则、且该周期内存在待结算明细」的客服生成；同客服同周期已存在结算单时跳过（幂等）。
// 单个客服结算失败（例如并发已结算或 admin 记录缺失）不中断整体任务。
func generateDueCsSettlements(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	pending, err := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.StatusEQ(esportscscommissionentry.StatusPendingSettlement),
	).Select(esportscscommissionentry.FieldAdminID, esportscscommissionentry.FieldCreatedAt).All(ctx)
	if err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		return 0, nil
	}
	// 按客服归集待结算明细的计提时间，用于判断「上一周期是否有明细」。
	accruedAt := map[int][]time.Time{}
	for _, entry := range pending {
		accruedAt[entry.AdminID] = append(accruedAt[entry.AdminID], entry.CreatedAt)
	}
	created := 0
	for adminID, times := range accruedAt {
		if created >= limit {
			break
		}
		rule, err := resolveCsCommissionRuleIn(ctx, db.EsportsCsCommissionRule, adminID, now)
		if err != nil {
			return created, err
		}
		if rule == nil {
			continue
		}
		start, end := csCommissionPreviousPeriodWindow(string(rule.Period), now)
		found := false
		for _, at := range times {
			if !at.Before(start) && at.Before(end) {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		exists, err := db.EsportsSettlement.Query().Where(
			esportssettlement.OwnerTypeEQ(esportssettlement.OwnerTypeCs),
			esportssettlement.OwnerIDEQ(adminID),
			esportssettlement.PeriodStartEQ(start.UTC()),
			esportssettlement.PeriodEndEQ(end.UTC()),
			// P0-6：已作废（void）的同周期单不算「已生成」，否则作废后该周期永远无法自动补回；
			// 作废会把明细回退为待结算，因此这里放开后即可重新生成新草稿。
			esportssettlement.StatusNEQ(esportssettlement.StatusVoid),
		).Exist(ctx)
		if err != nil {
			return created, err
		}
		if exists {
			continue
		}
		if _, err = settleCsCommission(ctx, db, adminID, start, end, 0, now); err != nil {
			continue
		}
		created++
	}
	return created, nil
}

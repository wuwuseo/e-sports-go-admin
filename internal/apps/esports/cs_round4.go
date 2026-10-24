package esports

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportscustomerfollowup"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsordercollaborator"
	"go-admin/internal/ent/predicate"
	"go-admin/internal/global"
)

// 客服端第 4 轮补齐：订单可见性统一口径（剩余）、客户归属转移、
// 结单后复购跟进待办。

// ── 订单可见性统一谓词 ──

// visibleOrdersToAdmin 是订单的可见性谓词：负责客服 OR 被授权协作。
// 与订单列表 / 详情（handlers.go adminOrderScope、order_access.go loadOwnedOrder）同一口径，
// 避免出现「列表看得到、看板/待办统计看不到」的口径分叉。
func visibleOrdersToAdmin(adminID int) predicate.EsportsOrder {
	return esportsorder.Or(
		esportsorder.ResponsibleAdminIDEQ(adminID),
		esportsorder.HasCollaboratorsWith(esportsordercollaborator.AdminIDEQ(adminID)),
	)
}

// ── 客户归属转移 ──

// transferCustomerOwner 转交客户归属：改 owner_admin_id 并在同一事务追加一条交接留痕
// （操作人 + 时间 + 说明）。历史跟进记录不变：跟进表只增不改，交接本身就是新的一条追加记录。
//
// 权限：仅当前归属客服或超管可转交（沿用 loadOwnedCustomer 的档案边界）。
func transferCustomerOwner(ctx context.Context, db *ent.Client, operatorID int, isSuper bool,
	customerID, targetAdminID int, reason string, now time.Time) (*ent.EsportsCustomer, error) {
	reason = strings.TrimSpace(reason)
	if customerID < 1 || targetAdminID < 1 || operatorID < 1 || len([]rune(reason)) > 200 {
		return nil, errInvalidInput
	}
	customer, err := loadOwnedCustomer(ctx, db, operatorID, isSuper, customerID)
	if err != nil {
		return nil, err
	}
	if customer.OwnerAdminID != nil && *customer.OwnerAdminID == targetAdminID {
		return nil, fmt.Errorf("%w：目标客服已是该客户当前归属人", errInvalidInput)
	}
	if _, err = db.Admin.Get(ctx, targetAdminID); ent.IsNotFound(err) {
		return nil, fmt.Errorf("%w：目标客服不存在", errInvalidInput)
	}
	if err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// 并发保护：条件更新当前归属，避免两个客服同时转交给不同人时后写覆盖先写的留痕。
	update := tx.EsportsCustomer.Update().Where(esportscustomer.IDEQ(customerID))
	if customer.OwnerAdminID == nil {
		update.Where(esportscustomer.OwnerAdminIDIsNil())
	} else {
		update.Where(esportscustomer.OwnerAdminIDEQ(*customer.OwnerAdminID))
	}
	changed, err := update.SetOwnerAdminID(targetAdminID).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, fmt.Errorf("%w：客户归属已被他人变更，请刷新后重试", errConflict)
	}
	fromID := 0
	if customer.OwnerAdminID != nil {
		fromID = *customer.OwnerAdminID
	}
	ids := []int{operatorID, targetAdminID}
	if fromID > 0 {
		ids = append(ids, fromID)
	}
	names := adminNames(ctx, tx.Client(), ids)
	fromLabel := "无归属"
	if fromID > 0 && names[fromID] != "" {
		fromLabel = names[fromID]
	}
	toLabel := strconv.Itoa(targetAdminID)
	if names[targetAdminID] != "" {
		toLabel = names[targetAdminID]
	}
	operatorLabel := strconv.Itoa(operatorID)
	if names[operatorID] != "" {
		operatorLabel = names[operatorID]
	}
	summary := fmt.Sprintf("%s客户归属由 %s 转交给 %s（操作人 %s，时间 %s）",
		customerTransferPrefix, fromLabel, toLabel, operatorLabel, now.In(cstZone).Format("2006-01-02 15:04"))
	if reason != "" {
		summary += "：" + reason
	}
	if len([]rune(summary)) > 500 {
		summary = string([]rune(summary)[:500])
	}
	if _, err = tx.EsportsCustomerFollowUp.Create().SetCustomerID(customerID).SetAdminID(operatorID).
		SetChannel(esportscustomerfollowup.ChannelOther).SetSummary(summary).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCustomer.Get(ctx, customerID)
}

// customerTransferPrefix 是归属交接留痕记录的摘要前缀，便于归档检索与人工区分。
const customerTransferPrefix = "【归属转移】"

// ── 结单后复购跟进 ──

const (
	// repurchaseFollowUpDaysKey 是结单后生成复购回访待办的天数；0 = 关闭。
	repurchaseFollowUpDaysKey = "app.esports.repurchase_follow_up_days"
	// defaultRepurchaseFollowUpDays 是代码内真实回落默认值（缺失 / 非法回落，0 才有「关闭」语义）。
	defaultRepurchaseFollowUpDays = 7
	// repurchaseFollowUpPrefix 是自动生成的复购跟进记录摘要前缀（跟进表无类型字段，
	// 只能以摘要前缀作为标记；同时避免与人工跟进混计）。
	repurchaseFollowUpPrefix = "【复购跟进】"
	// repurchaseTodoScanCap 是工作台复购待办的单轮扫描上限（限流，避免看板接口扫全表）。
	repurchaseTodoScanCap = 200
)

// repurchaseOrderToken 生成复购待办的订单溯源标记（写进摘要，用于幂等去重）。
func repurchaseOrderToken(orderID int) string {
	return "order:" + strconv.Itoa(orderID)
}

// resolveRepurchaseFollowUpDays 读取结单后复购回访周期：0 = 关闭；缺失 / 非法回落 7。
func resolveRepurchaseFollowUpDays(ctx context.Context, db *ent.Client) (int, bool) {
	raw := strings.TrimSpace(assessmentConfigString(ctx, db, repurchaseFollowUpDaysKey, ""))
	if raw == "" {
		return defaultRepurchaseFollowUpDays, true
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 0 {
		return defaultRepurchaseFollowUpDays, true
	}
	return days, days > 0
}

// ensureRepurchaseFollowUp 为一条已结单订单生成客户维度复购回访待办（幂等）。
// 返回是否新建。归属客服优先取订单负责客服，其次客户归属客服；两者皆无则不落待办（无法归口）。
func ensureRepurchaseFollowUp(ctx context.Context, db *ent.Client, order *ent.EsportsOrder, days int, now time.Time) (bool, error) {
	token := repurchaseOrderToken(order.ID)
	exists, err := db.EsportsCustomerFollowUp.Query().Where(
		esportscustomerfollowup.SummaryContains("[" + token + "]"),
	).Exist(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	customer, err := db.EsportsCustomer.Get(ctx, order.CustomerID)
	if ent.IsNotFound(err) {
		// 订单带 customer_id 但客户实体缺失（历史/补录数据、测试夹具）时**不能报错**：
		// 复购待办是辅助动作，没有客户实体就无处归口，应按 no-op 处理。
		// 这一点很重要——本函数被结单主链路（finalizeCloseOrderIn）调用，报错会让**结单整体失败**。
		return false, nil
	}
	if err != nil {
		return false, err
	}
	adminID := 0
	if order.ResponsibleAdminID != nil {
		adminID = *order.ResponsibleAdminID
	} else if customer.OwnerAdminID != nil {
		adminID = *customer.OwnerAdminID
	}
	if adminID < 1 {
		return false, nil
	}
	// 到期时间取「结单时间 + N 天」；由任务补建（已过期）时按当前时刻立即到期。
	due := now.UTC()
	if order.ClosedAt != nil {
		scheduled := order.ClosedAt.UTC().AddDate(0, 0, days)
		if scheduled.After(now.UTC()) {
			due = scheduled
		}
	}
	summary := fmt.Sprintf("%s[%s] 订单 %s 已完成 %d 天，请回访客户促成复购",
		repurchaseFollowUpPrefix, token, order.OrderNo, days)
	if _, err = db.EsportsCustomerFollowUp.Create().SetCustomerID(order.CustomerID).SetAdminID(adminID).
		SetChannel(esportscustomerfollowup.ChannelOther).SetSummary(summary).
		SetNextFollowAt(due).Save(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// createRepurchaseFollowUpForOrder 是「结单后」钩子：结单事务完成后调用即可按 N 天生成复购待办。
//
// 建议主控在 settlement.go 结单落库成功后调用（函数名与调用点写进报告，worker 不改 settlement.go）：
//
//	if _, err := createRepurchaseFollowUpForOrder(ctx, tx.Client(), order.ID, now); err != nil { return err }
//
// 未结单 / 未配置 / 无归属客服时为明确的 no-op（返回 nil, nil），不阻断结单主链路。
func createRepurchaseFollowUpForOrder(ctx context.Context, db *ent.Client, orderID int, now time.Time) (*ent.EsportsCustomerFollowUp, error) {
	if orderID < 1 {
		return nil, errInvalidInput
	}
	days, enabled := resolveRepurchaseFollowUpDays(ctx, db)
	if !enabled {
		return nil, nil
	}
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted {
		return nil, nil
	}
	created, err := ensureRepurchaseFollowUp(ctx, db, order, days, now)
	if err != nil || !created {
		return nil, err
	}
	return db.EsportsCustomerFollowUp.Query().Where(
		esportscustomerfollowup.SummaryContains("[" + repurchaseOrderToken(orderID) + "]"),
	).Order(ent.Desc(esportscustomerfollowup.FieldID)).First(ctx)
}

// scheduleRepurchaseFollowUps 补齐「已结单满 N 天但尚未生成复购待办」的订单。
// 结单钩子未接线时由本任务兜底，保证复购跟进一定有落点。
func scheduleRepurchaseFollowUps(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 500 {
		return 0, errInvalidInput
	}
	days, enabled := resolveRepurchaseFollowUpDays(ctx, db)
	if !enabled {
		return 0, nil
	}
	orders, err := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusCompleted),
		esportsorder.ClosedAtNotNil(),
		esportsorder.ClosedAtLTE(now.UTC().AddDate(0, 0, -days)),
	).Order(ent.Asc(esportsorder.FieldClosedAt)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, order := range orders {
		ok, err := ensureRepurchaseFollowUp(ctx, db, order, days, now)
		if err != nil {
			return created, err
		}
		if ok {
			created++
		}
	}
	return created, nil
}

// scheduleRepurchaseFollowUpsTask 定时任务入口：需由主控注册为 esports_schedule_repurchase_follow_ups
// （建议 CronJob `0 15 10 * * *`，每日一次）。
func scheduleRepurchaseFollowUpsTask(ctx context.Context, logger io.Writer) error {
	count, err := scheduleRepurchaseFollowUps(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "生成复购跟进待办：%d 条\n", count)
	}
	return err
}

// countRepurchaseTodosDue 统计客服当前「复购跟进」待办数：
// 仅统计摘要带复购前缀、已到期，且是该客户最新一条跟进记录的复购待办——
// 客服补一条普通跟进即视为「已回访」，待办自动消失（跟进表只增不改，无法原地改状态）。
func countRepurchaseTodosDue(ctx context.Context, db *ent.Client, adminID int, isSuper bool, now time.Time) (int, error) {
	q := db.EsportsCustomerFollowUp.Query().Where(
		esportscustomerfollowup.SummaryHasPrefix(repurchaseFollowUpPrefix),
		esportscustomerfollowup.NextFollowAtNotNil(),
		esportscustomerfollowup.NextFollowAtLTE(now.UTC()),
	)
	if !isSuper {
		q.Where(esportscustomerfollowup.AdminIDEQ(adminID))
	}
	rows, err := q.Order(ent.Desc(esportscustomerfollowup.FieldID)).Limit(repurchaseTodoScanCap).All(ctx)
	if err != nil {
		return 0, err
	}
	seen := make(map[int]struct{}, len(rows))
	count := 0
	for _, row := range rows {
		if _, ok := seen[row.CustomerID]; ok {
			continue
		}
		seen[row.CustomerID] = struct{}{}
		newer, err := db.EsportsCustomerFollowUp.Query().Where(
			esportscustomerfollowup.CustomerIDEQ(row.CustomerID),
			esportscustomerfollowup.IDGT(row.ID),
		).Exist(ctx)
		if err != nil {
			return 0, err
		}
		if !newer {
			count++
		}
	}
	return count, nil
}

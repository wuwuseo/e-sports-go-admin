// Package esports is the source feature set for the single-studio order system.
package esports

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	cmfapp "github.com/wuwuseo/cmf/app"

	"go-admin/internal/global"
	"go-admin/internal/tasks"
)

// Manual inspection can opt in on a development host only. The incomplete P0
// workflow remains unavailable by default and in production.
var featureEnabled = experimentalEnabled(os.Getenv)

func experimentalEnabled(getenv func(string) string) bool {
	return getenv("ESPORTS_EXPERIMENTAL") == "1" &&
		strings.EqualFold(getenv("CMF_APP_ENV"), "development")
}

func expireOrdersTask(ctx context.Context, logger io.Writer) error {
	count, err := expireDueOrders(ctx, global.ORM, time.Now(), 100)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理待付款超时订单：%d\n", count)
	}
	return err
}

func expireDispatchInvitesTask(ctx context.Context, logger io.Writer) error {
	count, err := expireDispatchInvites(ctx, global.ORM, time.Now(), 100)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理过期派单邀请：%d\n", count)
	}
	return err
}

func expireWalletBonusTask(ctx context.Context, logger io.Writer) error {
	count, err := expireWalletBonus(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理到期赠送权益：%d\n", count)
	}
	return err
}

// publishScheduledPagesTask 激活到点的定时发布页面版本（§16.4）。
func publishScheduledPagesTask(ctx context.Context, logger io.Writer) error {
	count, err := publishScheduledPages(ctx, global.ORM, time.Now(), 100)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理定时发布页面：%d\n", count)
	}
	return err
}

// expireDispatchPriorityNoticeTask 把超过优先通知时长的订单释放到抢单大厅（§5.4 业务口径）。
func expireDispatchPriorityNoticeTask(ctx context.Context, logger io.Writer) error {
	count, err := expireDispatchPriorityNotices(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理优先通知超时释放：%d\n", count)
	}
	return err
}

// remindDispatchLadderTask 按可配置时间阶梯对超时未接单的邀请逐级催单（§5.7）。
func remindDispatchLadderTask(ctx context.Context, logger io.Writer) error {
	count, err := runRemindLadder(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理阶梯催单：%d\n", count)
	}
	return err
}

// checkServiceOvertimeTask 服务时长超过配置阈值时提醒客服确认是否加钟（§5.7）。
func checkServiceOvertimeTask(ctx context.Context, logger io.Writer) error {
	count, err := checkServiceOvertime(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理服务超时提醒：%d\n", count)
	}
	return err
}

// rebuildDispatchPriorityTask 周期重算优先派单权名单（§5.4）。
func rebuildDispatchPriorityTask(ctx context.Context, logger io.Writer) error {
	count, err := rebuildDispatchPriority(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理优先派单权名单：%d\n", count)
	}
	return err
}

// expireStoredValueCardsTask 把已过有效期的储值卡标记为已过期（§4.4 到期）。
func expireStoredValueCardsTask(ctx context.Context, logger io.Writer) error {
	count, err := expireStoredValueCards(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理到期储值卡：%d\n", count)
	}
	return err
}

// generateCsSettlementsTask 为各客服按上一完整周期自动生成提成结算单（§12.2）。
func generateCsSettlementsTask(ctx context.Context, logger io.Writer) error {
	count, err := generateDueCsSettlements(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "自动生成客服提成结算单：%d\n", count)
	}
	return err
}

// checkAfterSaleSLATask 对超出当前阶段时限的售后单写升级提醒（§13.5）。
func checkAfterSaleSLATask(ctx context.Context, logger io.Writer) error {
	count, err := checkAfterSaleSLABreaches(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理售后超时升级：%d\n", count)
	}
	return err
}

// checkCloseApprovalTimeoutTask 处理结单审批节点超时（§3.4）：允许自动通过的节点自动推进，其余升级提醒。
func checkCloseApprovalTimeoutTask(ctx context.Context, logger io.Writer) error {
	count, err := checkCloseApprovalTimeouts(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理结单审批超时：%d\n", count)
	}
	return err
}

// applyMemberDowngradeTask 执行等级保护期已到期的客户降级（§10.3）。
func applyMemberDowngradeTask(ctx context.Context, logger io.Writer) error {
	count, err := applyMemberDowngrades(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理会员等级保护期降级：%d\n", count)
	}
	return err
}

func init() {
	if featureEnabled {
		tasks.Register("esports_expire_orders", expireOrdersTask)
		tasks.Register("esports_expire_dispatch_invites", expireDispatchInvitesTask)
		tasks.Register("esports_expire_wallet_bonus", expireWalletBonusTask)
		tasks.Register("esports_apply_member_downgrade", applyMemberDowngradeTask)
		tasks.Register("esports_publish_scheduled_pages", publishScheduledPagesTask)
		tasks.Register("esports_expire_stored_value_cards", expireStoredValueCardsTask)
		tasks.Register("esports_rebuild_dispatch_priority", rebuildDispatchPriorityTask)
		tasks.Register("esports_expire_dispatch_priority_notice", expireDispatchPriorityNoticeTask)
		tasks.Register("esports_remind_dispatch_ladder", remindDispatchLadderTask)
		tasks.Register("esports_check_service_overtime", checkServiceOvertimeTask)
		tasks.Register("esports_generate_cs_settlements", generateCsSettlementsTask)
		tasks.Register("esports_check_aftersale_sla", checkAfterSaleSLATask)
		tasks.Register("esports_check_close_approval_timeout", checkCloseApprovalTimeoutTask)
	}
	cmfapp.Register(cmfapp.App{
		Code: "esports", Name: "陪玩报单", Version: "0.1.0", Enabled: featureEnabled,
		Config: []cmfapp.ConfigItem{
			{Key: "payment_deadline_minutes", Name: "订单支付时限（分钟）", Value: "0", Type: "number", Group: "esports", Remark: "业务负责人确认前为 0，禁止创建可支付订单"},
		},
		Routes: []cmfapp.Route{
			{Method: "GET", Path: "/skus", Auth: cmfapp.AuthPublic, Handler: publicSkus},
			{Method: "GET", Path: "/user/orders/:id/quote", Auth: cmfapp.AuthUser, Handler: userOrderQuote},
			{Method: "POST", Path: "/user/orders/:id/confirm", Auth: cmfapp.AuthUser, Handler: userConfirmQuote},
			{Method: "GET", Path: "/admin/skus", Auth: cmfapp.AuthAdmin, Handler: adminSkus},
			{Method: "POST", Path: "/admin/skus", Auth: cmfapp.AuthAdmin, Handler: createSku},
			{Method: "PUT", Path: "/admin/skus/:id", Auth: cmfapp.AuthAdmin, Handler: updateSkuHandler},
			{Method: "GET", Path: "/admin/categories", Auth: cmfapp.AuthAdmin, Handler: adminCategories},
			{Method: "POST", Path: "/admin/categories", Auth: cmfapp.AuthAdmin, Handler: createCategory},
			{Method: "PUT", Path: "/admin/categories/:id", Auth: cmfapp.AuthAdmin, Handler: updateCategoryHandler},
			{Method: "DELETE", Path: "/admin/categories/:id", Auth: cmfapp.AuthAdmin, Handler: deleteCategoryHandler},
			{Method: "GET", Path: "/admin/customers", Auth: cmfapp.AuthAdmin, Handler: adminCustomers},
			{Method: "POST", Path: "/admin/customers", Auth: cmfapp.AuthAdmin, Handler: createCustomer},
			{Method: "POST", Path: "/admin/customers/:id/age-checks", Auth: cmfapp.AuthAdmin, Handler: createAgeCheck},
			{Method: "GET", Path: "/admin/age-checks", Auth: cmfapp.AuthAdmin, Handler: pendingAgeChecks},
			{Method: "POST", Path: "/admin/age-checks/:id/review", Auth: cmfapp.AuthAdmin, Handler: reviewAgeCheckRequest},
			{Method: "GET", Path: "/admin/orders", Auth: cmfapp.AuthAdmin, Handler: adminOrders},
			{Method: "GET", Path: "/admin/orders/:id", Auth: cmfapp.AuthAdmin, Handler: adminOrder},
			{Method: "POST", Path: "/admin/orders", Auth: cmfapp.AuthAdmin, Handler: createOrder},
			{Method: "PUT", Path: "/admin/orders/:id/quote", Auth: cmfapp.AuthAdmin, Handler: repriceOrderHandler},
			{Method: "POST", Path: "/admin/orders/:id/cancel", Auth: cmfapp.AuthAdmin, Handler: cancelOrderHandler},
			{Method: "PUT", Path: "/admin/orders/:id/appointment", Auth: cmfapp.AuthAdmin, Handler: rescheduleOrderHandler},
			// 存单：把已付款订单标记为冻结，「恢复」清除标记（原地保留、不动履约状态与名额）。
			{Method: "POST", Path: "/admin/orders/:id/deposit", Auth: cmfapp.AuthAdmin, Handler: depositOrderHandler},
			{Method: "POST", Path: "/admin/orders/:id/restore", Auth: cmfapp.AuthAdmin, Handler: restoreOrderHandler},
			{Method: "POST", Path: "/admin/orders/:id/pay-by-balance", Auth: cmfapp.AuthAdmin, Handler: payOrderWithBalanceHandler},
			{Method: "GET", Path: "/admin/wallets", Auth: cmfapp.AuthAdmin, Handler: adminWallets},
			{Method: "GET", Path: "/admin/wallets/:id/transactions", Auth: cmfapp.AuthAdmin, Handler: adminWalletTransactions},
			{Method: "POST", Path: "/admin/wallets/:id/recharge", Auth: cmfapp.AuthAdmin, Handler: walletMutationHandler(false)},
			{Method: "POST", Path: "/admin/wallets/:id/adjust", Auth: cmfapp.AuthAdmin, Handler: walletMutationHandler(true)},
			// 批次分摊明细（§4.3 / §11.3）：查「每笔消费动用了哪些批次」。
			{Method: "GET", Path: "/admin/ledger-allocations", Auth: cmfapp.AuthAdmin, Handler: adminLedgerAllocations},
			// 充值活动：活动配置与按活动自动匹配赠送的充值。
			{Method: "GET", Path: "/admin/campaigns", Auth: cmfapp.AuthAdmin, Handler: adminCampaigns},
			{Method: "POST", Path: "/admin/campaigns", Auth: cmfapp.AuthAdmin, Handler: createCampaignHandler},
			{Method: "PUT", Path: "/admin/campaigns/:id", Auth: cmfapp.AuthAdmin, Handler: updateCampaignHandler},
			{Method: "POST", Path: "/admin/campaigns/:id/status", Auth: cmfapp.AuthAdmin, Handler: setCampaignStatusHandler},
			{Method: "POST", Path: "/admin/wallets/:id/recharge-campaigns", Auth: cmfapp.AuthAdmin, Handler: rechargeWalletByCampaignHandler},
			// 会员等级：等级配置、客户会员详情与手动调级。
			{Method: "GET", Path: "/admin/member-levels", Auth: cmfapp.AuthAdmin, Handler: adminMemberLevels},
			{Method: "POST", Path: "/admin/member-levels", Auth: cmfapp.AuthAdmin, Handler: createMemberLevelHandler},
			{Method: "PUT", Path: "/admin/member-levels/:id", Auth: cmfapp.AuthAdmin, Handler: updateMemberLevelHandler},
			{Method: "POST", Path: "/admin/member-levels/:id/status", Auth: cmfapp.AuthAdmin, Handler: setMemberLevelStatusHandler},
			{Method: "GET", Path: "/admin/customers/:id/member", Auth: cmfapp.AuthAdmin, Handler: adminCustomerMember},
			{Method: "POST", Path: "/admin/customers/:id/member/level", Auth: cmfapp.AuthAdmin, Handler: setCustomerMemberLevelHandler},
			// 礼物模块（§9）：礼物目录、送礼、退款、复核与榜单。
			{Method: "GET", Path: "/admin/gifts", Auth: cmfapp.AuthAdmin, Handler: adminGifts},
			{Method: "POST", Path: "/admin/gifts", Auth: cmfapp.AuthAdmin, Handler: createGiftHandler},
			{Method: "PUT", Path: "/admin/gifts/:id", Auth: cmfapp.AuthAdmin, Handler: updateGiftHandler},
			{Method: "POST", Path: "/admin/gifts/:id/status", Auth: cmfapp.AuthAdmin, Handler: setGiftStatusHandler},
			{Method: "GET", Path: "/admin/gift-orders", Auth: cmfapp.AuthAdmin, Handler: adminGiftOrders},
			{Method: "POST", Path: "/admin/gift-orders", Auth: cmfapp.AuthAdmin, Handler: sendGiftHandler},
			{Method: "POST", Path: "/admin/gift-orders/:id/refund", Auth: cmfapp.AuthAdmin, Handler: refundGiftHandler},
			{Method: "POST", Path: "/admin/gift-orders/:id/review", Auth: cmfapp.AuthAdmin, Handler: reviewGiftHandler},
			{Method: "GET", Path: "/admin/gift-leaderboard", Auth: cmfapp.AuthAdmin, Handler: giftLeaderboardHandler},
			{Method: "GET", Path: "/admin/dispatch/board", Auth: cmfapp.AuthAdmin, Handler: dispatchBoardHandler},
			// 优先派单权（§5.4）与派单候选排序（§5.5 首版）。
			{Method: "GET", Path: "/admin/dispatch/priority", Auth: cmfapp.AuthAdmin, Handler: adminDispatchPriority},
			{Method: "POST", Path: "/admin/dispatch/priority", Auth: cmfapp.AuthAdmin, Handler: grantDispatchPriorityHandler},
			{Method: "POST", Path: "/admin/dispatch/priority/:id/revoke", Auth: cmfapp.AuthAdmin, Handler: revokeDispatchPriorityHandler},
			{Method: "GET", Path: "/admin/orders/:id/dispatch-candidates", Auth: cmfapp.AuthAdmin, Handler: adminDispatchCandidates},
			{Method: "GET", Path: "/admin/orders/:id/priority-notices", Auth: cmfapp.AuthAdmin, Handler: adminOrderPriorityNotices},
			{Method: "POST", Path: "/admin/orders/:id/release-priority", Auth: cmfapp.AuthAdmin, Handler: releaseOrderPriorityHandler},
			// 普通待派单订单发布到抢单大厅（§5.2 / §5.6）：没有优先名单的游戏分区由此进入大厅。
			{Method: "POST", Path: "/admin/orders/:id/publish-grab", Auth: cmfapp.AuthAdmin, Handler: publishOrderToGrabHallHandler},
			// 陪玩转单（§5.7）：接单后无法服务由陪玩发起，须客服确认才释放席位。
			{Method: "GET", Path: "/admin/seat-transfers", Auth: cmfapp.AuthAdmin, Handler: adminSeatTransfers},
			{Method: "POST", Path: "/admin/seat-transfers/:id/decision", Auth: cmfapp.AuthAdmin, Handler: decideSeatTransferHandler},
			{Method: "GET", Path: "/admin/orders/:id/seats", Auth: cmfapp.AuthAdmin, Handler: orderSeatsHandler},
			{Method: "POST", Path: "/admin/orders/:id/seats/:seatIndex/assign", Auth: cmfapp.AuthAdmin, Handler: assignSeatHandler},
			{Method: "POST", Path: "/admin/orders/:id/seats/:seatIndex/revoke", Auth: cmfapp.AuthAdmin, Handler: revokeSeatHandler},
			{Method: "POST", Path: "/admin/orders/:id/claim", Auth: cmfapp.AuthAdmin, Handler: claimOrderHandler},
			{Method: "POST", Path: "/admin/orders/:id/transfer", Auth: cmfapp.AuthAdmin, Handler: transferOrderHandler},
			{Method: "POST", Path: "/admin/orders/:id/remind", Auth: cmfapp.AuthAdmin, Handler: remindOrderHandler},
			// 加钟（§3.3 / §6.2 / §12.5）：服务中订单追加金额与工时，由陪玩确认后累加并生成加钟分成。
			{Method: "GET", Path: "/admin/orders/:id/addons", Auth: cmfapp.AuthAdmin, Handler: listOrderAddonsHandler},
			{Method: "POST", Path: "/admin/orders/:id/addons", Auth: cmfapp.AuthAdmin, Handler: createOrderAddonHandler},
			{Method: "POST", Path: "/admin/order-addons/:id/cancel", Auth: cmfapp.AuthAdmin, Handler: cancelOrderAddonHandler},
			// 加钟收款（§3.3）：对已确认的加钟单用客户余额核销追加金额。
			{Method: "POST", Path: "/admin/order-addons/:id/pay-by-balance", Auth: cmfapp.AuthAdmin, Handler: payOrderAddonByBalanceHandler},
			// 结单（客服手动结单）与售后 / 退款模块。
			{Method: "POST", Path: "/admin/orders/:id/close", Auth: cmfapp.AuthAdmin, Handler: closeOrderHandler},
			// 结单复核（§3.4）：金额达复核阈值的订单进入待复核，由非负责客服的管理员通过 / 驳回。
			{Method: "POST", Path: "/admin/orders/:id/close-approve", Auth: cmfapp.AuthAdmin, Handler: approveCloseReviewHandler},
			{Method: "POST", Path: "/admin/orders/:id/close-reject", Auth: cmfapp.AuthAdmin, Handler: rejectCloseReviewHandler},
			// 结单表单（§3.4）：返回订单适用的表单模板与字段定义，供结单弹窗渲染。
			{Method: "GET", Path: "/admin/orders/:id/close-form", Auth: cmfapp.AuthAdmin, Handler: orderCloseFormHandler},
			// 结单审批进度（§3.4）：当前待审批节点，供复核弹窗展示。
			{Method: "GET", Path: "/admin/orders/:id/close-approval", Auth: cmfapp.AuthAdmin, Handler: orderCloseApprovalHandler},
			{Method: "GET", Path: "/admin/after-sales", Auth: cmfapp.AuthAdmin, Handler: adminAfterSales},
			{Method: "GET", Path: "/admin/after-sales/:id", Auth: cmfapp.AuthAdmin, Handler: adminAfterSaleDetail},
			{Method: "POST", Path: "/admin/after-sales", Auth: cmfapp.AuthAdmin, Handler: createAfterSaleHandler},
			{Method: "POST", Path: "/admin/after-sales/:id/accept", Auth: cmfapp.AuthAdmin, Handler: acceptAfterSaleHandler},
			{Method: "POST", Path: "/admin/after-sales/:id/plan", Auth: cmfapp.AuthAdmin, Handler: planAfterSaleHandler},
			// 待客户确认（§13.3）：客服可代客户确认方案（客户线下已同意），随后进入待审批。
			{Method: "POST", Path: "/admin/after-sales/:id/confirm", Auth: cmfapp.AuthAdmin, Handler: confirmAfterSalePlanHandler},
			{Method: "POST", Path: "/admin/after-sales/:id/approve", Auth: cmfapp.AuthAdmin, Handler: approveAfterSaleHandler},
			{Method: "POST", Path: "/admin/after-sales/:id/reject", Auth: cmfapp.AuthAdmin, Handler: rejectAfterSaleHandler},
			{Method: "POST", Path: "/admin/after-sales/:id/execute", Auth: cmfapp.AuthAdmin, Handler: executeAfterSaleHandler},
			{Method: "POST", Path: "/admin/after-sales/:id/withdraw", Auth: cmfapp.AuthAdmin, Handler: withdrawAfterSaleHandler},
			// 补履约任务（§13.6）：售后方案「补钟 / 换人」执行后产生，由客服开始服务并验收。
			{Method: "GET", Path: "/admin/fulfillment-makeups", Auth: cmfapp.AuthAdmin, Handler: listFulfillmentMakeupsHandler},
			{Method: "POST", Path: "/admin/fulfillment-makeups/:id/start", Auth: cmfapp.AuthAdmin, Handler: startFulfillmentMakeupHandler},
			{Method: "POST", Path: "/admin/fulfillment-makeups/:id/complete", Auth: cmfapp.AuthAdmin, Handler: completeFulfillmentMakeupHandler},
			// 分成与结算：分成规则、分成明细、结算单。
			{Method: "GET", Path: "/admin/commission-rules", Auth: cmfapp.AuthAdmin, Handler: adminCommissionRules},
			{Method: "POST", Path: "/admin/commission-rules", Auth: cmfapp.AuthAdmin, Handler: createCommissionRuleHandler},
			{Method: "POST", Path: "/admin/commission-rules/:id/activate", Auth: cmfapp.AuthAdmin, Handler: activateCommissionRuleHandler},
			{Method: "POST", Path: "/admin/commission-rules/:id/rollback", Auth: cmfapp.AuthAdmin, Handler: rollbackCommissionRuleHandler},
			{Method: "GET", Path: "/admin/commission-entries", Auth: cmfapp.AuthAdmin, Handler: adminCommissionEntries},
			// 结单规则（§3.4 首版）：可配置结单条件与金额复核阈值，挂在「结算」菜单下。
			{Method: "GET", Path: "/admin/close-rules", Auth: cmfapp.AuthAdmin, Handler: listCloseRulesHandler},
			{Method: "POST", Path: "/admin/close-rules", Auth: cmfapp.AuthAdmin, Handler: createCloseRuleHandler},
			{Method: "PUT", Path: "/admin/close-rules/:id", Auth: cmfapp.AuthAdmin, Handler: updateCloseRuleHandler},
			{Method: "POST", Path: "/admin/close-rules/:id/activate", Auth: cmfapp.AuthAdmin, Handler: activateCloseRuleHandler},
			{Method: "POST", Path: "/admin/close-rules/:id/rollback", Auth: cmfapp.AuthAdmin, Handler: rollbackCloseRuleHandler},
			// 结单表单设计器（§3.4）：字段库与模板（按 全局 / 游戏分区 / SKU + 金额区间绑定）。
			{Method: "GET", Path: "/admin/close-form-fields", Auth: cmfapp.AuthAdmin, Handler: listCloseFormFieldsHandler},
			{Method: "POST", Path: "/admin/close-form-fields", Auth: cmfapp.AuthAdmin, Handler: createCloseFormFieldHandler},
			{Method: "PUT", Path: "/admin/close-form-fields/:id", Auth: cmfapp.AuthAdmin, Handler: updateCloseFormFieldHandler},
			{Method: "POST", Path: "/admin/close-form-fields/:id/status", Auth: cmfapp.AuthAdmin, Handler: setCloseFormFieldStatusHandler},
			{Method: "GET", Path: "/admin/close-form-templates", Auth: cmfapp.AuthAdmin, Handler: listCloseFormTemplatesHandler},
			{Method: "POST", Path: "/admin/close-form-templates", Auth: cmfapp.AuthAdmin, Handler: createCloseFormTemplateHandler},
			{Method: "PUT", Path: "/admin/close-form-templates/:id", Auth: cmfapp.AuthAdmin, Handler: updateCloseFormTemplateHandler},
			{Method: "POST", Path: "/admin/close-form-templates/:id/activate", Auth: cmfapp.AuthAdmin, Handler: activateCloseFormTemplateHandler},
			{Method: "POST", Path: "/admin/close-form-templates/:id/rollback", Auth: cmfapp.AuthAdmin, Handler: rollbackCloseFormTemplateHandler},
			// 结单提交规则（§3.4）：单人 / 多人 / 自定义（代提交、提交比例、未提交处理）。
			{Method: "GET", Path: "/admin/close-submit-rules", Auth: cmfapp.AuthAdmin, Handler: listCloseSubmitRulesHandler},
			{Method: "POST", Path: "/admin/close-submit-rules", Auth: cmfapp.AuthAdmin, Handler: createCloseSubmitRuleHandler},
			{Method: "PUT", Path: "/admin/close-submit-rules/:id", Auth: cmfapp.AuthAdmin, Handler: updateCloseSubmitRuleHandler},
			{Method: "POST", Path: "/admin/close-submit-rules/:id/activate", Auth: cmfapp.AuthAdmin, Handler: activateCloseSubmitRuleHandler},
			{Method: "POST", Path: "/admin/close-submit-rules/:id/rollback", Auth: cmfapp.AuthAdmin, Handler: rollbackCloseSubmitRuleHandler},
			// 结单审批流（§3.4）：节点串行编排、审批角色与超时策略。
			{Method: "GET", Path: "/admin/close-approval-flows", Auth: cmfapp.AuthAdmin, Handler: listCloseApprovalFlowsHandler},
			{Method: "POST", Path: "/admin/close-approval-flows", Auth: cmfapp.AuthAdmin, Handler: createCloseApprovalFlowHandler},
			{Method: "PUT", Path: "/admin/close-approval-flows/:id", Auth: cmfapp.AuthAdmin, Handler: updateCloseApprovalFlowHandler},
			{Method: "POST", Path: "/admin/close-approval-flows/:id/activate", Auth: cmfapp.AuthAdmin, Handler: activateCloseApprovalFlowHandler},
			{Method: "POST", Path: "/admin/close-approval-flows/:id/rollback", Auth: cmfapp.AuthAdmin, Handler: rollbackCloseApprovalFlowHandler},
			// 陪玩追偿明细（§13.4）：售后赔付由陪玩承担时另建的扣回单，须独立审批后执行扣回。
			{Method: "GET", Path: "/admin/player-recoveries", Auth: cmfapp.AuthAdmin, Handler: listPlayerRecoveriesHandler},
			{Method: "POST", Path: "/admin/player-recoveries/:id/approve", Auth: cmfapp.AuthAdmin, Handler: approvePlayerRecoveryHandler},
			{Method: "POST", Path: "/admin/player-recoveries/:id/reject", Auth: cmfapp.AuthAdmin, Handler: rejectPlayerRecoveryHandler},
			{Method: "POST", Path: "/admin/player-recoveries/:id/execute", Auth: cmfapp.AuthAdmin, Handler: executePlayerRecoveryHandler},
			{Method: "GET", Path: "/admin/settlements", Auth: cmfapp.AuthAdmin, Handler: adminSettlements},
			{Method: "POST", Path: "/admin/settlements", Auth: cmfapp.AuthAdmin, Handler: createSettlementHandler},
			{Method: "POST", Path: "/admin/settlements/:id/lock", Auth: cmfapp.AuthAdmin, Handler: lockSettlementHandler},
			{Method: "GET", Path: "/admin/settlements/:id", Auth: cmfapp.AuthAdmin, Handler: adminSettlementDetail},
			// 客服提成（§12.2）：规则配置、明细与概览、独立结算单与发放登记，挂在既有「结算」菜单下。
			{Method: "GET", Path: "/admin/cs-commission-rules", Auth: cmfapp.AuthAdmin, Handler: adminCsCommissionRules},
			{Method: "POST", Path: "/admin/cs-commission-rules", Auth: cmfapp.AuthAdmin, Handler: createCsCommissionRuleHandler},
			{Method: "POST", Path: "/admin/cs-commission-rules/:id/activate", Auth: cmfapp.AuthAdmin, Handler: activateCsCommissionRuleHandler},
			{Method: "POST", Path: "/admin/cs-commission-rules/:id/rollback", Auth: cmfapp.AuthAdmin, Handler: rollbackCsCommissionRuleHandler},
			{Method: "GET", Path: "/admin/cs-commission-entries", Auth: cmfapp.AuthAdmin, Handler: adminCsCommissionEntries},
			{Method: "GET", Path: "/admin/cs-commission/summary", Auth: cmfapp.AuthAdmin, Handler: adminCsCommissionSummary},
			{Method: "POST", Path: "/admin/cs-settlements", Auth: cmfapp.AuthAdmin, Handler: createCsSettlementHandler},
			{Method: "POST", Path: "/admin/settlements/:id/pay", Auth: cmfapp.AuthAdmin, Handler: markSettlementPaidHandler},
			// 提现（§6.3 / §12.6）：财务审核与打款登记，挂在既有「结算」菜单下。
			{Method: "GET", Path: "/admin/withdrawals", Auth: cmfapp.AuthAdmin, Handler: adminWithdrawals},
			{Method: "GET", Path: "/admin/withdrawals/:id", Auth: cmfapp.AuthAdmin, Handler: adminWithdrawalDetail},
			{Method: "POST", Path: "/admin/withdrawals/:id/review", Auth: cmfapp.AuthAdmin, Handler: reviewWithdrawalHandler},
			{Method: "POST", Path: "/admin/withdrawals/:id/pay", Auth: cmfapp.AuthAdmin, Handler: payWithdrawalHandler},
			// 陪玩模块管理端：档案 CRUD 与上下线。
			{Method: "GET", Path: "/admin/players", Auth: cmfapp.AuthAdmin, Handler: adminPlayers},
			{Method: "POST", Path: "/admin/players", Auth: cmfapp.AuthAdmin, Handler: createPlayer},
			{Method: "PUT", Path: "/admin/players/:id", Auth: cmfapp.AuthAdmin, Handler: updatePlayerHandler},
			// 陪玩端自助：供 uni-app 客户端调用，不挂管理端菜单。
			{Method: "GET", Path: "/user/player/dashboard", Auth: cmfapp.AuthUser, Handler: playerDashboardHandler},
			{Method: "POST", Path: "/user/player/presence", Auth: cmfapp.AuthUser, Handler: playerPresenceHandler},
			{Method: "GET", Path: "/user/player/invites", Auth: cmfapp.AuthUser, Handler: playerInvitesHandler},
			{Method: "POST", Path: "/user/player/invites/:id/accept", Auth: cmfapp.AuthUser, Handler: playerAcceptInviteHandler},
			{Method: "POST", Path: "/user/player/invites/:id/reject", Auth: cmfapp.AuthUser, Handler: playerRejectInviteHandler},
			{Method: "GET", Path: "/user/player/grab-hall", Auth: cmfapp.AuthUser, Handler: playerGrabHallHandler},
			{Method: "POST", Path: "/user/player/orders/:id/grab", Auth: cmfapp.AuthUser, Handler: playerGrabHandler},
			{Method: "GET", Path: "/user/player/orders", Auth: cmfapp.AuthUser, Handler: playerOrdersHandler},
			{Method: "POST", Path: "/user/player/orders/:id/service/start", Auth: cmfapp.AuthUser, Handler: playerStartServiceHandler},
			{Method: "POST", Path: "/user/player/orders/:id/service/finish", Auth: cmfapp.AuthUser, Handler: playerFinishServiceHandler},
			// 陪玩端加钟通知（§6.2）：接收加钟通知并确认 / 拒绝。
			{Method: "GET", Path: "/user/player/addons", Auth: cmfapp.AuthUser, Handler: playerPendingAddonsHandler},
			{Method: "POST", Path: "/user/player/addons/:id/confirm", Auth: cmfapp.AuthUser, Handler: playerConfirmAddonHandler},
			{Method: "POST", Path: "/user/player/addons/:id/reject", Auth: cmfapp.AuthUser, Handler: playerRejectAddonHandler},
			// 陪玩端分成：本人明细与概览。
			{Method: "GET", Path: "/user/player/commission", Auth: cmfapp.AuthUser, Handler: playerCommissionEntries},
			{Method: "GET", Path: "/user/player/commission/summary", Auth: cmfapp.AuthUser, Handler: playerCommissionSummaryHandler},
			// 陪玩端提现（§6.3）：申请、我的提现单与提现概览。
			{Method: "POST", Path: "/user/player/withdrawals", Auth: cmfapp.AuthUser, Handler: playerApplyWithdrawal},
			{Method: "GET", Path: "/user/player/withdrawals", Auth: cmfapp.AuthUser, Handler: playerWithdrawals},
			{Method: "GET", Path: "/user/player/withdrawals/summary", Auth: cmfapp.AuthUser, Handler: playerWithdrawalSummaryHandler},
			// 陪玩端优先派单权通知（§5.4 业务口径）：窗口内查看与拒绝；接受复用 orders/{id}/grab。
			{Method: "GET", Path: "/user/player/dispatch-notices", Auth: cmfapp.AuthUser, Handler: playerDispatchNotices},
			{Method: "POST", Path: "/user/player/dispatch-notices/:id/decline", Auth: cmfapp.AuthUser, Handler: declineDispatchNoticeHandler},
			// 陪玩端转单（§5.7）：接单后无法服务时发起，客服确认前可撤回。
			{Method: "POST", Path: "/user/player/orders/:id/transfer", Auth: cmfapp.AuthUser, Handler: playerRequestTransfer},
			{Method: "GET", Path: "/user/player/transfer-requests", Auth: cmfapp.AuthUser, Handler: playerTransferRequests},
			{Method: "POST", Path: "/user/player/transfer-requests/:id/cancel", Auth: cmfapp.AuthUser, Handler: playerCancelTransfer},
			// 考官端（§7）：权重配置、考核任务、打分定级、申诉复核、评价与综合评分。
			{Method: "GET", Path: "/admin/assessment-rules", Auth: cmfapp.AuthAdmin, Handler: adminAssessmentRules},
			{Method: "POST", Path: "/admin/assessment-rules", Auth: cmfapp.AuthAdmin, Handler: setAssessmentRulesHandler},
			{Method: "GET", Path: "/admin/assessments", Auth: cmfapp.AuthAdmin, Handler: adminAssessments},
			{Method: "POST", Path: "/admin/assessments", Auth: cmfapp.AuthAdmin, Handler: createAssessmentHandler},
			{Method: "GET", Path: "/admin/assessments/:id", Auth: cmfapp.AuthAdmin, Handler: adminAssessmentDetail},
			{Method: "POST", Path: "/admin/assessments/:id/assign", Auth: cmfapp.AuthAdmin, Handler: assignAssessmentHandler},
			{Method: "POST", Path: "/admin/assessments/:id/score", Auth: cmfapp.AuthAdmin, Handler: scoreAssessmentHandler},
			{Method: "POST", Path: "/admin/assessments/:id/appeal/review", Auth: cmfapp.AuthAdmin, Handler: reviewAppealHandler},
			{Method: "GET", Path: "/admin/player-reviews", Auth: cmfapp.AuthAdmin, Handler: adminPlayerReviews},
			{Method: "POST", Path: "/admin/player-reviews", Auth: cmfapp.AuthAdmin, Handler: recordReviewHandler},
			{Method: "POST", Path: "/admin/player-reviews/:id/status", Auth: cmfapp.AuthAdmin, Handler: setReviewStatusHandler},
			{Method: "GET", Path: "/admin/players/:id/rating", Auth: cmfapp.AuthAdmin, Handler: adminPlayerRating},
			// 陪玩端自助：我的考核与申诉（§7.4）。
			{Method: "GET", Path: "/user/player/assessments", Auth: cmfapp.AuthUser, Handler: playerAssessmentsHandler},
			{Method: "POST", Path: "/user/player/assessments/:id/appeal", Auth: cmfapp.AuthUser, Handler: playerAppealHandler},
			// 客户自助：已完成订单的服务评价（§7.5）。
			{Method: "POST", Path: "/user/orders/:id/reviews", Auth: cmfapp.AuthUser, Handler: userSubmitReviewHandler},
			// 陪玩端自助：我的追偿明细（§13.4）。
			{Method: "GET", Path: "/user/player/recoveries", Auth: cmfapp.AuthUser, Handler: playerRecoveriesHandler},
			// 客户自助：售后方（§13.3）确认 / 拒绝待确认方案。
			{Method: "POST", Path: "/user/after-sales/:id/confirm", Auth: cmfapp.AuthUser, Handler: userConfirmAfterSaleHandler},
			{Method: "POST", Path: "/user/after-sales/:id/decline", Auth: cmfapp.AuthUser, Handler: userDeclineAfterSaleHandler},
			// 经营统计报表（§17）：五类只读聚合报表，按 :type 分派，支持多维筛选与下钻明细。
			{Method: "GET", Path: "/admin/reports/:type", Auth: cmfapp.AuthAdmin, Handler: adminReports},
			// 页面装修（§16）：客户自助端首页装修草稿、发布版本、回滚与素材库；/page 供客户端读已发布配置。
			{Method: "GET", Path: "/admin/pages", Auth: cmfapp.AuthAdmin, Handler: adminPageDocuments},
			{Method: "GET", Path: "/admin/pages/:channel", Auth: cmfapp.AuthAdmin, Handler: adminPageDocument},
			{Method: "PUT", Path: "/admin/pages/:channel/draft", Auth: cmfapp.AuthAdmin, Handler: savePageDraftHandler},
			{Method: "POST", Path: "/admin/pages/:channel/publish", Auth: cmfapp.AuthAdmin, Handler: publishPageHandler},
			{Method: "POST", Path: "/admin/pages/:channel/rollback", Auth: cmfapp.AuthAdmin, Handler: rollbackPageHandler},
			{Method: "GET", Path: "/admin/pages/:channel/versions", Auth: cmfapp.AuthAdmin, Handler: adminPageVersions},
			{Method: "GET", Path: "/admin/page-assets", Auth: cmfapp.AuthAdmin, Handler: adminPageAssets},
			{Method: "POST", Path: "/admin/page-assets", Auth: cmfapp.AuthAdmin, Handler: createPageAssetHandler},
			{Method: "DELETE", Path: "/admin/page-assets/:id", Auth: cmfapp.AuthAdmin, Handler: deletePageAssetHandler},
			{Method: "GET", Path: "/page", Auth: cmfapp.AuthPublic, Handler: publicPage},
			// 客服端（§8）：客户档案与跟进、企微侧边栏自动建档、话术库与工作台聚合。
			// PC 工作台 / 移动客服端 / 企微侧边栏三个入口共用同一套接口。
			{Method: "GET", Path: "/admin/customers/:id/profile", Auth: cmfapp.AuthAdmin, Handler: adminCustomerProfile},
			{Method: "PUT", Path: "/admin/customers/:id/profile", Auth: cmfapp.AuthAdmin, Handler: updateCustomerProfileHandler},
			{Method: "GET", Path: "/admin/customers/:id/follow-ups", Auth: cmfapp.AuthAdmin, Handler: adminCustomerFollowUps},
			{Method: "POST", Path: "/admin/customers/:id/follow-ups", Auth: cmfapp.AuthAdmin, Handler: createCustomerFollowUpHandler},
			{Method: "POST", Path: "/admin/customers/wecom-intake", Auth: cmfapp.AuthAdmin, Handler: wecomIntakeHandler},
			{Method: "GET", Path: "/admin/scripts", Auth: cmfapp.AuthAdmin, Handler: adminScripts},
			{Method: "POST", Path: "/admin/scripts", Auth: cmfapp.AuthAdmin, Handler: createScriptHandler},
			{Method: "PUT", Path: "/admin/scripts/:id", Auth: cmfapp.AuthAdmin, Handler: updateScriptHandler},
			{Method: "POST", Path: "/admin/scripts/:id/status", Auth: cmfapp.AuthAdmin, Handler: setScriptStatusHandler},
			{Method: "GET", Path: "/admin/cs/workbench", Auth: cmfapp.AuthAdmin, Handler: csWorkbenchHandler},
			// 储值卡（§4.4 / §11.3）：开卡与余额查询、退卡试算与财务审核、订单用储值卡核销。
			{Method: "GET", Path: "/admin/stored-value-cards", Auth: cmfapp.AuthAdmin, Handler: adminStoredValueCards},
			{Method: "POST", Path: "/admin/stored-value-cards", Auth: cmfapp.AuthAdmin, Handler: createStoredValueCardHandler},
			{Method: "GET", Path: "/admin/stored-value-cards/:id", Auth: cmfapp.AuthAdmin, Handler: adminStoredValueCardDetail},
			{Method: "GET", Path: "/admin/stored-value-cards/:id/refund-quote", Auth: cmfapp.AuthAdmin, Handler: storedValueCardRefundQuoteHandler},
			{Method: "POST", Path: "/admin/stored-value-cards/:id/refund-request", Auth: cmfapp.AuthAdmin, Handler: requestStoredValueRefundHandler},
			{Method: "POST", Path: "/admin/stored-value-cards/:id/refund-decision", Auth: cmfapp.AuthAdmin, Handler: decideStoredValueRefundHandler},
			{Method: "POST", Path: "/admin/orders/:id/pay-by-stored-value-card", Auth: cmfapp.AuthAdmin, Handler: payOrderByStoredValueCardHandler},
			// 储值卡套餐（§4.4）：档位维护，开卡时择一。
			{Method: "GET", Path: "/admin/stored-value-packages", Auth: cmfapp.AuthAdmin, Handler: listStoredValuePackagesHandler},
			{Method: "POST", Path: "/admin/stored-value-packages", Auth: cmfapp.AuthAdmin, Handler: createStoredValuePackageHandler},
			{Method: "PUT", Path: "/admin/stored-value-packages/:id", Auth: cmfapp.AuthAdmin, Handler: updateStoredValuePackageHandler},
			{Method: "POST", Path: "/admin/stored-value-packages/:id/status", Auth: cmfapp.AuthAdmin, Handler: setStoredValuePackageStatusHandler},
		},
		Permissions: []cmfapp.Permission{
			// 商品模块：一个模块一个菜单。
			{Type: cmfapp.PermMenu, Name: "商品", Code: "esports:product:menu", MenuPath: "/esports/products", Component: "esports/products/index", Icon: "icon-apps", SortOrder: 40},
			{Type: cmfapp.PermAPI, Name: "查看商品", Code: "esports:skus:list", Path: "/api/v1/esports/admin/skus", Method: "GET", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "创建商品", Code: "esports:skus:create", Path: "/api/v1/esports/admin/skus", Method: "POST", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑商品", Code: "esports:skus:update", Path: "/api/v1/esports/admin/skus/{id}", Method: "PUT", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "查看产品分类", Code: "esports:categories:list", Path: "/api/v1/esports/admin/categories", Method: "GET", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "创建产品分类", Code: "esports:categories:create", Path: "/api/v1/esports/admin/categories", Method: "POST", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑产品分类", Code: "esports:categories:update", Path: "/api/v1/esports/admin/categories/{id}", Method: "PUT", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "删除产品分类", Code: "esports:categories:delete", Path: "/api/v1/esports/admin/categories/{id}", Method: "DELETE", Parent: "esports:product:menu"},
			{Type: cmfapp.PermButton, Name: "新增商品按钮", Code: "esports:skus:add", Parent: "esports:product:menu"},
			{Type: cmfapp.PermButton, Name: "编辑商品按钮", Code: "esports:skus:edit", Parent: "esports:product:menu"},
			{Type: cmfapp.PermButton, Name: "管理产品分类按钮", Code: "esports:categories:manage", Parent: "esports:product:menu"},

			// 订单模块。
			{Type: cmfapp.PermMenu, Name: "订单", Code: "esports:order:menu", MenuPath: "/esports/orders", Component: "esports/orders/index", Icon: "icon-list", SortOrder: 41},
			{Type: cmfapp.PermAPI, Name: "查看订单", Code: "esports:orders:list", Path: "/api/v1/esports/admin/orders", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "查看订单详情", Code: "esports:orders:get", Path: "/api/v1/esports/admin/orders/{id}", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "订单改价", Code: "esports:orders:reprice", Path: "/api/v1/esports/admin/orders/{id}/quote", Method: "PUT", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "订单取消", Code: "esports:orders:cancel", Path: "/api/v1/esports/admin/orders/{id}/cancel", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "订单改期", Code: "esports:orders:reschedule", Path: "/api/v1/esports/admin/orders/{id}/appointment", Method: "PUT", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "余额核销订单", Code: "esports:orders:pay_balance", Path: "/api/v1/esports/admin/orders/{id}/pay-by-balance", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "结单", Code: "esports:orders:close", Path: "/api/v1/esports/admin/orders/{id}/close", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "复核通过结单", Code: "esports:orders:close_approve", Path: "/api/v1/esports/admin/orders/{id}/close-approve", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "复核驳回结单", Code: "esports:orders:close_reject", Path: "/api/v1/esports/admin/orders/{id}/close-reject", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "查看订单结单表单", Code: "esports:orders:close_form", Path: "/api/v1/esports/admin/orders/{id}/close-form", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "订单存单", Code: "esports:orders:deposit", Path: "/api/v1/esports/admin/orders/{id}/deposit", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "恢复存单", Code: "esports:orders:restore", Path: "/api/v1/esports/admin/orders/{id}/restore", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "查看订单加钟", Code: "esports:orders:addons", Path: "/api/v1/esports/admin/orders/{id}/addons", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "加钟余额收款", Code: "esports:orders:addon_pay", Path: "/api/v1/esports/admin/order-addons/{id}/pay-by-balance", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "发起加钟", Code: "esports:orders:addon_request", Path: "/api/v1/esports/admin/orders/{id}/addons", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "取消加钟", Code: "esports:orders:addon_cancel", Path: "/api/v1/esports/admin/order-addons/{id}/cancel", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermButton, Name: "订单操作按钮", Code: "esports:orders:operate", Parent: "esports:order:menu"},

			// 钱包模块：客户余额与陪玩猫粮账户、只增流水。
			{Type: cmfapp.PermMenu, Name: "钱包", Code: "esports:wallet:menu", MenuPath: "/esports/wallets", Component: "esports/wallets/index", Icon: "icon-wallet", SortOrder: 43},
			{Type: cmfapp.PermAPI, Name: "查看钱包账户", Code: "esports:wallets:list", Path: "/api/v1/esports/admin/wallets", Method: "GET", Parent: "esports:wallet:menu"},
			{Type: cmfapp.PermAPI, Name: "查看钱包流水", Code: "esports:wallets:transactions", Path: "/api/v1/esports/admin/wallets/{id}/transactions", Method: "GET", Parent: "esports:wallet:menu"},
			{Type: cmfapp.PermAPI, Name: "钱包充值", Code: "esports:wallets:recharge", Path: "/api/v1/esports/admin/wallets/{id}/recharge", Method: "POST", Parent: "esports:wallet:menu"},
			{Type: cmfapp.PermAPI, Name: "钱包调整", Code: "esports:wallets:adjust", Path: "/api/v1/esports/admin/wallets/{id}/adjust", Method: "POST", Parent: "esports:wallet:menu"},
			{Type: cmfapp.PermAPI, Name: "查看批次分摊明细", Code: "esports:wallets:ledger_allocations", Path: "/api/v1/esports/admin/ledger-allocations", Method: "GET", Parent: "esports:wallet:menu"},
			{Type: cmfapp.PermButton, Name: "钱包操作按钮", Code: "esports:wallets:manage", Parent: "esports:wallet:menu"},

			// 报单模块。
			{Type: cmfapp.PermMenu, Name: "报单", Code: "esports:report:menu", MenuPath: "/esports/reports", Component: "esports/reports/index", Icon: "icon-edit", SortOrder: 42},
			{Type: cmfapp.PermAPI, Name: "创建订单", Code: "esports:orders:create", Path: "/api/v1/esports/admin/orders", Method: "POST", Parent: "esports:report:menu"},
			{Type: cmfapp.PermAPI, Name: "查看客户", Code: "esports:customers:list", Path: "/api/v1/esports/admin/customers", Method: "GET", Parent: "esports:report:menu"},
			{Type: cmfapp.PermAPI, Name: "创建客户", Code: "esports:customers:create", Path: "/api/v1/esports/admin/customers", Method: "POST", Parent: "esports:report:menu"},
			{Type: cmfapp.PermAPI, Name: "提交年龄核验", Code: "esports:age_checks:create", Path: "/api/v1/esports/admin/customers/{id}/age-checks", Method: "POST", Parent: "esports:report:menu"},
			{Type: cmfapp.PermAPI, Name: "查看待复核年龄资格", Code: "esports:age_checks:list", Path: "/api/v1/esports/admin/age-checks", Method: "GET", Parent: "esports:report:menu"},
			{Type: cmfapp.PermAPI, Name: "复核年龄资格", Code: "esports:age_checks:review", Path: "/api/v1/esports/admin/age-checks/{id}/review", Method: "POST", Parent: "esports:report:menu"},
			{Type: cmfapp.PermButton, Name: "客服报单按钮", Code: "esports:orders:add", Parent: "esports:report:menu"},
			{Type: cmfapp.PermButton, Name: "创建客户按钮", Code: "esports:customers:add", Parent: "esports:report:menu"},
			{Type: cmfapp.PermButton, Name: "提交年龄核验按钮", Code: "esports:age_checks:add", Parent: "esports:report:menu"},
			{Type: cmfapp.PermButton, Name: "复核年龄资格按钮", Code: "esports:age_checks:approve", Parent: "esports:report:menu"},

			// 排单模块：管理端/客服工作台（派单、认领、转单、催单、看板、席位）。
			{Type: cmfapp.PermMenu, Name: "排单", Code: "esports:dispatch:menu", MenuPath: "/esports/dispatch", Component: "esports/dispatch/index", Icon: "icon-share-alt", SortOrder: 44},
			{Type: cmfapp.PermAPI, Name: "查看排单看板", Code: "esports:dispatch:board", Path: "/api/v1/esports/admin/dispatch/board", Method: "GET", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "查看优先派单权名单", Code: "esports:dispatch:priority_list", Path: "/api/v1/esports/admin/dispatch/priority", Method: "GET", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "授予优先派单权", Code: "esports:dispatch:priority_grant", Path: "/api/v1/esports/admin/dispatch/priority", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "移出优先派单权名单", Code: "esports:dispatch:priority_revoke", Path: "/api/v1/esports/admin/dispatch/priority/{id}/revoke", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "查看派单候选", Code: "esports:dispatch:candidates", Path: "/api/v1/esports/admin/orders/{id}/dispatch-candidates", Method: "GET", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "查看订单优先通知", Code: "esports:dispatch:priority_notices", Path: "/api/v1/esports/admin/orders/{id}/priority-notices", Method: "GET", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "提前释放优先通知", Code: "esports:dispatch:priority_release", Path: "/api/v1/esports/admin/orders/{id}/release-priority", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "发布到抢单大厅", Code: "esports:dispatch:publish_grab", Path: "/api/v1/esports/admin/orders/{id}/publish-grab", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermButton, Name: "优先派单权按钮", Code: "esports:dispatch:priority_manage", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "查看转单申请", Code: "esports:dispatch:transfers", Path: "/api/v1/esports/admin/seat-transfers", Method: "GET", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "确认转单申请", Code: "esports:dispatch:transfer_decide", Path: "/api/v1/esports/admin/seat-transfers/{id}/decision", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermButton, Name: "转单确认按钮", Code: "esports:dispatch:transfer_manage", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "查看订单席位", Code: "esports:dispatch:seats", Path: "/api/v1/esports/admin/orders/{id}/seats", Method: "GET", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "指派席位", Code: "esports:dispatch:assign", Path: "/api/v1/esports/admin/orders/{id}/seats/{seat_index}/assign", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "撤回席位", Code: "esports:dispatch:revoke", Path: "/api/v1/esports/admin/orders/{id}/seats/{seat_index}/revoke", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "认领订单", Code: "esports:dispatch:claim", Path: "/api/v1/esports/admin/orders/{id}/claim", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "转单", Code: "esports:dispatch:transfer", Path: "/api/v1/esports/admin/orders/{id}/transfer", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "催单", Code: "esports:dispatch:remind", Path: "/api/v1/esports/admin/orders/{id}/remind", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermButton, Name: "排单操作按钮", Code: "esports:dispatch:operate", Parent: "esports:dispatch:menu"},

			// 陪玩模块：管理端陪玩档案；陪玩端自助接口属独立客户端，不挂管理菜单。
			{Type: cmfapp.PermMenu, Name: "陪玩", Code: "esports:player:menu", MenuPath: "/esports/players", Component: "esports/players/index", Icon: "icon-user", SortOrder: 45},
			{Type: cmfapp.PermAPI, Name: "查看陪玩", Code: "esports:players:list", Path: "/api/v1/esports/admin/players", Method: "GET", Parent: "esports:player:menu"},
			{Type: cmfapp.PermAPI, Name: "创建陪玩", Code: "esports:players:create", Path: "/api/v1/esports/admin/players", Method: "POST", Parent: "esports:player:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑陪玩", Code: "esports:players:update", Path: "/api/v1/esports/admin/players/{id}", Method: "PUT", Parent: "esports:player:menu"},
			{Type: cmfapp.PermButton, Name: "陪玩管理按钮", Code: "esports:players:manage", Parent: "esports:player:menu"},

			// 售后模块：售后单受理 / 方案 / 审批 / 执行 / 退款。
			{Type: cmfapp.PermMenu, Name: "售后", Code: "esports:aftersale:menu", MenuPath: "/esports/aftersales", Component: "esports/aftersales/index", Icon: "icon-archive", SortOrder: 46},
			{Type: cmfapp.PermAPI, Name: "查看售后单", Code: "esports:aftersales:list", Path: "/api/v1/esports/admin/after-sales", Method: "GET", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "查看售后详情", Code: "esports:aftersales:detail", Path: "/api/v1/esports/admin/after-sales/{id}", Method: "GET", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "创建售后单", Code: "esports:aftersales:create", Path: "/api/v1/esports/admin/after-sales", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "受理售后", Code: "esports:aftersales:accept", Path: "/api/v1/esports/admin/after-sales/{id}/accept", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "提交售后方案", Code: "esports:aftersales:plan", Path: "/api/v1/esports/admin/after-sales/{id}/plan", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "代客户确认售后方案", Code: "esports:aftersales:confirm", Path: "/api/v1/esports/admin/after-sales/{id}/confirm", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "审批售后", Code: "esports:aftersales:approve", Path: "/api/v1/esports/admin/after-sales/{id}/approve", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "驳回售后", Code: "esports:aftersales:reject", Path: "/api/v1/esports/admin/after-sales/{id}/reject", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "执行售后", Code: "esports:aftersales:execute", Path: "/api/v1/esports/admin/after-sales/{id}/execute", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "撤回售后", Code: "esports:aftersales:withdraw", Path: "/api/v1/esports/admin/after-sales/{id}/withdraw", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "查看补履约任务", Code: "esports:aftersales:makeups", Path: "/api/v1/esports/admin/fulfillment-makeups", Method: "GET", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "开始补履约服务", Code: "esports:aftersales:makeup_start", Path: "/api/v1/esports/admin/fulfillment-makeups/{id}/start", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "验收补履约", Code: "esports:aftersales:makeup_complete", Path: "/api/v1/esports/admin/fulfillment-makeups/{id}/complete", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermButton, Name: "售后操作按钮", Code: "esports:aftersales:operate", Parent: "esports:aftersale:menu"},

			// 结算模块：分成规则 / 分成明细 / 结算单。
			{Type: cmfapp.PermMenu, Name: "结算", Code: "esports:settlement:menu", MenuPath: "/esports/settlements", Component: "esports/settlements/index", Icon: "icon-safe", SortOrder: 47},
			{Type: cmfapp.PermAPI, Name: "查看结单规则", Code: "esports:settlements:close_rules", Path: "/api/v1/esports/admin/close-rules", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "创建结单规则", Code: "esports:settlements:close_rule_create", Path: "/api/v1/esports/admin/close-rules", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑结单规则", Code: "esports:settlements:close_rule_update", Path: "/api/v1/esports/admin/close-rules/{id}", Method: "PUT", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "生效结单规则", Code: "esports:settlements:close_rule_activate", Path: "/api/v1/esports/admin/close-rules/{id}/activate", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "回滚结单规则", Code: "esports:settlements:close_rule_rollback", Path: "/api/v1/esports/admin/close-rules/{id}/rollback", Method: "POST", Parent: "esports:settlement:menu"},
			// 结单表单设计器（§3.4）：字段库与模板配置，独立菜单。
			{Type: cmfapp.PermMenu, Name: "结单表单", Code: "esports:close_form:menu", MenuPath: "/esports/close-forms", Component: "esports/close-forms/index", Icon: "icon-file", SortOrder: 48},
			{Type: cmfapp.PermAPI, Name: "查看结单表单字段", Code: "esports:close_forms:fields", Path: "/api/v1/esports/admin/close-form-fields", Method: "GET", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "创建结单表单字段", Code: "esports:close_forms:field_create", Path: "/api/v1/esports/admin/close-form-fields", Method: "POST", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑结单表单字段", Code: "esports:close_forms:field_update", Path: "/api/v1/esports/admin/close-form-fields/{id}", Method: "PUT", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "启停结单表单字段", Code: "esports:close_forms:field_status", Path: "/api/v1/esports/admin/close-form-fields/{id}/status", Method: "POST", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "查看结单表单模板", Code: "esports:close_forms:templates", Path: "/api/v1/esports/admin/close-form-templates", Method: "GET", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "创建结单表单模板", Code: "esports:close_forms:template_create", Path: "/api/v1/esports/admin/close-form-templates", Method: "POST", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑结单表单模板", Code: "esports:close_forms:template_update", Path: "/api/v1/esports/admin/close-form-templates/{id}", Method: "PUT", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "生效结单表单模板", Code: "esports:close_forms:template_activate", Path: "/api/v1/esports/admin/close-form-templates/{id}/activate", Method: "POST", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "回滚结单表单模板", Code: "esports:close_forms:template_rollback", Path: "/api/v1/esports/admin/close-form-templates/{id}/rollback", Method: "POST", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "查看结单提交规则", Code: "esports:close_forms:submit_rules", Path: "/api/v1/esports/admin/close-submit-rules", Method: "GET", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "创建结单提交规则", Code: "esports:close_forms:submit_rule_create", Path: "/api/v1/esports/admin/close-submit-rules", Method: "POST", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑结单提交规则", Code: "esports:close_forms:submit_rule_update", Path: "/api/v1/esports/admin/close-submit-rules/{id}", Method: "PUT", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "生效结单提交规则", Code: "esports:close_forms:submit_rule_activate", Path: "/api/v1/esports/admin/close-submit-rules/{id}/activate", Method: "POST", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "回滚结单提交规则", Code: "esports:close_forms:submit_rule_rollback", Path: "/api/v1/esports/admin/close-submit-rules/{id}/rollback", Method: "POST", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "查看结单审批流", Code: "esports:close_forms:approval_flows", Path: "/api/v1/esports/admin/close-approval-flows", Method: "GET", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "创建结单审批流", Code: "esports:close_forms:approval_flow_create", Path: "/api/v1/esports/admin/close-approval-flows", Method: "POST", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑结单审批流", Code: "esports:close_forms:approval_flow_update", Path: "/api/v1/esports/admin/close-approval-flows/{id}", Method: "PUT", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "生效结单审批流", Code: "esports:close_forms:approval_flow_activate", Path: "/api/v1/esports/admin/close-approval-flows/{id}/activate", Method: "POST", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "回滚结单审批流", Code: "esports:close_forms:approval_flow_rollback", Path: "/api/v1/esports/admin/close-approval-flows/{id}/rollback", Method: "POST", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermButton, Name: "结单表单操作按钮", Code: "esports:close_forms:manage", Parent: "esports:close_form:menu"},
			{Type: cmfapp.PermAPI, Name: "查看分成规则", Code: "esports:settlements:rules", Path: "/api/v1/esports/admin/commission-rules", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "创建分成规则", Code: "esports:settlements:rule_create", Path: "/api/v1/esports/admin/commission-rules", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "生效分成规则", Code: "esports:settlements:rule_activate", Path: "/api/v1/esports/admin/commission-rules/{id}/activate", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "回滚分成规则", Code: "esports:settlements:rule_rollback", Path: "/api/v1/esports/admin/commission-rules/{id}/rollback", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看分成明细", Code: "esports:settlements:entries", Path: "/api/v1/esports/admin/commission-entries", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看结算单", Code: "esports:settlements:list", Path: "/api/v1/esports/admin/settlements", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "生成结算单", Code: "esports:settlements:create", Path: "/api/v1/esports/admin/settlements", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "锁定结算单", Code: "esports:settlements:lock", Path: "/api/v1/esports/admin/settlements/{id}/lock", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看结算单详情", Code: "esports:settlements:detail", Path: "/api/v1/esports/admin/settlements/{id}", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看陪玩追偿", Code: "esports:settlements:recoveries", Path: "/api/v1/esports/admin/player-recoveries", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "审批陪玩追偿", Code: "esports:settlements:recovery_approve", Path: "/api/v1/esports/admin/player-recoveries/{id}/approve", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "驳回陪玩追偿", Code: "esports:settlements:recovery_reject", Path: "/api/v1/esports/admin/player-recoveries/{id}/reject", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "执行陪玩追偿", Code: "esports:settlements:recovery_execute", Path: "/api/v1/esports/admin/player-recoveries/{id}/execute", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermButton, Name: "结算操作按钮", Code: "esports:settlements:manage", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看客服提成规则", Code: "esports:settlements:cs_rules", Path: "/api/v1/esports/admin/cs-commission-rules", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "创建客服提成规则", Code: "esports:settlements:cs_rule_create", Path: "/api/v1/esports/admin/cs-commission-rules", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "生效客服提成规则", Code: "esports:settlements:cs_rule_activate", Path: "/api/v1/esports/admin/cs-commission-rules/{id}/activate", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "回滚客服提成规则", Code: "esports:settlements:cs_rule_rollback", Path: "/api/v1/esports/admin/cs-commission-rules/{id}/rollback", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看客服提成明细", Code: "esports:settlements:cs_entries", Path: "/api/v1/esports/admin/cs-commission-entries", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看客服提成概览", Code: "esports:settlements:cs_summary", Path: "/api/v1/esports/admin/cs-commission/summary", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "生成客服提成结算单", Code: "esports:settlements:cs_settle", Path: "/api/v1/esports/admin/cs-settlements", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "登记结算发放", Code: "esports:settlements:pay", Path: "/api/v1/esports/admin/settlements/{id}/pay", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermButton, Name: "客服提成规则按钮", Code: "esports:settlements:cs_rule_manage", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看提现单", Code: "esports:settlements:withdrawals", Path: "/api/v1/esports/admin/withdrawals", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看提现单详情", Code: "esports:settlements:withdrawal_detail", Path: "/api/v1/esports/admin/withdrawals/{id}", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "审核提现", Code: "esports:settlements:withdrawal_review", Path: "/api/v1/esports/admin/withdrawals/{id}/review", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "登记提现打款", Code: "esports:settlements:withdrawal_pay", Path: "/api/v1/esports/admin/withdrawals/{id}/pay", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermButton, Name: "提现审核按钮", Code: "esports:settlements:withdraw_manage", Parent: "esports:settlement:menu"},

			// 充值活动模块：活动配置与按活动自动匹配赠送的充值（§4.5）。
			{Type: cmfapp.PermMenu, Name: "充值活动", Code: "esports:campaign:menu", MenuPath: "/esports/campaigns", Component: "esports/campaigns/index", Icon: "icon-gift", SortOrder: 48},
			{Type: cmfapp.PermAPI, Name: "查看充值活动", Code: "esports:campaigns:list", Path: "/api/v1/esports/admin/campaigns", Method: "GET", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermAPI, Name: "创建充值活动", Code: "esports:campaigns:create", Path: "/api/v1/esports/admin/campaigns", Method: "POST", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑充值活动", Code: "esports:campaigns:update", Path: "/api/v1/esports/admin/campaigns/{id}", Method: "PUT", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermAPI, Name: "切换活动状态", Code: "esports:campaigns:status", Path: "/api/v1/esports/admin/campaigns/{id}/status", Method: "POST", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermAPI, Name: "按活动充值", Code: "esports:campaigns:recharge", Path: "/api/v1/esports/admin/wallets/{id}/recharge-campaigns", Method: "POST", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermButton, Name: "充值活动管理按钮", Code: "esports:campaigns:manage", Parent: "esports:campaign:menu"},

			// 会员等级模块：等级配置与客户会员等级（§10）。
			{Type: cmfapp.PermMenu, Name: "会员等级", Code: "esports:member:menu", MenuPath: "/esports/members", Component: "esports/members/index", Icon: "icon-star", SortOrder: 49},
			{Type: cmfapp.PermAPI, Name: "查看会员等级", Code: "esports:members:levels", Path: "/api/v1/esports/admin/member-levels", Method: "GET", Parent: "esports:member:menu"},
			{Type: cmfapp.PermAPI, Name: "创建会员等级", Code: "esports:members:level_create", Path: "/api/v1/esports/admin/member-levels", Method: "POST", Parent: "esports:member:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑会员等级", Code: "esports:members:level_update", Path: "/api/v1/esports/admin/member-levels/{id}", Method: "PUT", Parent: "esports:member:menu"},
			{Type: cmfapp.PermAPI, Name: "启停会员等级", Code: "esports:members:level_status", Path: "/api/v1/esports/admin/member-levels/{id}/status", Method: "POST", Parent: "esports:member:menu"},
			{Type: cmfapp.PermAPI, Name: "查看客户会员", Code: "esports:members:detail", Path: "/api/v1/esports/admin/customers/{id}/member", Method: "GET", Parent: "esports:member:menu"},
			{Type: cmfapp.PermAPI, Name: "手动调级", Code: "esports:members:set_level", Path: "/api/v1/esports/admin/customers/{id}/member/level", Method: "POST", Parent: "esports:member:menu"},
			{Type: cmfapp.PermButton, Name: "会员等级管理按钮", Code: "esports:members:manage", Parent: "esports:member:menu"},

			// 礼物模块：礼物目录、送礼单据、退款复核与榜单（§9）。
			{Type: cmfapp.PermMenu, Name: "礼物", Code: "esports:gift:menu", MenuPath: "/esports/gifts", Component: "esports/gifts/index", Icon: "icon-heart", SortOrder: 50},
			{Type: cmfapp.PermAPI, Name: "查看礼物目录", Code: "esports:gifts:list", Path: "/api/v1/esports/admin/gifts", Method: "GET", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "创建礼物", Code: "esports:gifts:create", Path: "/api/v1/esports/admin/gifts", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑礼物", Code: "esports:gifts:update", Path: "/api/v1/esports/admin/gifts/{id}", Method: "PUT", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "上下架礼物", Code: "esports:gifts:status", Path: "/api/v1/esports/admin/gifts/{id}/status", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "查看礼物单据", Code: "esports:gifts:orders", Path: "/api/v1/esports/admin/gift-orders", Method: "GET", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "送礼", Code: "esports:gifts:send", Path: "/api/v1/esports/admin/gift-orders", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "礼物退款", Code: "esports:gifts:refund", Path: "/api/v1/esports/admin/gift-orders/{id}/refund", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "复核礼物", Code: "esports:gifts:review", Path: "/api/v1/esports/admin/gift-orders/{id}/review", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "查看礼物榜单", Code: "esports:gifts:board", Path: "/api/v1/esports/admin/gift-leaderboard", Method: "GET", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermButton, Name: "礼物管理按钮", Code: "esports:gifts:manage", Parent: "esports:gift:menu"},

			// 考官端模块：考核权重配置、任务打分定级、申诉复核、客户评价与综合评分（§7）。
			{Type: cmfapp.PermMenu, Name: "考核", Code: "esports:assessment:menu", MenuPath: "/esports/assessments", Component: "esports/assessments/index", Icon: "icon-check-circle", SortOrder: 51},
			{Type: cmfapp.PermAPI, Name: "查看考核权重", Code: "esports:assessments:rules_list", Path: "/api/v1/esports/admin/assessment-rules", Method: "GET", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermAPI, Name: "配置考核权重", Code: "esports:assessments:rules_set", Path: "/api/v1/esports/admin/assessment-rules", Method: "POST", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermAPI, Name: "查看考核任务", Code: "esports:assessments:list", Path: "/api/v1/esports/admin/assessments", Method: "GET", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermAPI, Name: "创建考核任务", Code: "esports:assessments:create", Path: "/api/v1/esports/admin/assessments", Method: "POST", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermAPI, Name: "查看考核详情", Code: "esports:assessments:detail", Path: "/api/v1/esports/admin/assessments/{id}", Method: "GET", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermAPI, Name: "分配考官", Code: "esports:assessments:assign", Path: "/api/v1/esports/admin/assessments/{id}/assign", Method: "POST", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermAPI, Name: "考核打分", Code: "esports:assessments:score", Path: "/api/v1/esports/admin/assessments/{id}/score", Method: "POST", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermAPI, Name: "复核申诉", Code: "esports:assessments:appeal_review", Path: "/api/v1/esports/admin/assessments/{id}/appeal/review", Method: "POST", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermAPI, Name: "查看客户评价", Code: "esports:assessments:reviews_list", Path: "/api/v1/esports/admin/player-reviews", Method: "GET", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermAPI, Name: "代录评价", Code: "esports:assessments:reviews_record", Path: "/api/v1/esports/admin/player-reviews", Method: "POST", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermAPI, Name: "复核评价状态", Code: "esports:assessments:reviews_status", Path: "/api/v1/esports/admin/player-reviews/{id}/status", Method: "POST", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermAPI, Name: "查看综合评分", Code: "esports:assessments:rating", Path: "/api/v1/esports/admin/players/{id}/rating", Method: "GET", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermButton, Name: "考核管理按钮", Code: "esports:assessments:manage", Parent: "esports:assessment:menu"},

			// 经营统计报表模块（§17）：老板经营总览、客服业绩、派单效率、售后质量、财务资金与结算。
			{Type: cmfapp.PermMenu, Name: "报表", Code: "esports:stats:menu", MenuPath: "/esports/stats", Component: "esports/stats/index", Icon: "icon-bar-chart", SortOrder: 52},
			{Type: cmfapp.PermAPI, Name: "查看经营报表", Code: "esports:stats:view", Path: "/api/v1/esports/admin/reports/{type}", Method: "GET", Parent: "esports:stats:menu"},
			{Type: cmfapp.PermButton, Name: "查看财务资金报表", Code: "esports:stats:finance", Parent: "esports:stats:menu"},
			{Type: cmfapp.PermButton, Name: "报表操作按钮", Code: "esports:stats:manage", Parent: "esports:stats:menu"},

			// 页面装修模块（§16）：uni-app 客户自助端首页装修草稿、发布版本、回滚与素材库。
			{Type: cmfapp.PermMenu, Name: "页面装修", Code: "esports:page:menu", MenuPath: "/esports/pages", Component: "esports/pages/index", Icon: "icon-desktop", SortOrder: 53},
			{Type: cmfapp.PermAPI, Name: "查看装修文档", Code: "esports:pages:list", Path: "/api/v1/esports/admin/pages", Method: "GET", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "保存草稿", Code: "esports:pages:draft_save", Path: "/api/v1/esports/admin/pages/{channel}/draft", Method: "PUT", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "发布页面", Code: "esports:pages:publish", Path: "/api/v1/esports/admin/pages/{channel}/publish", Method: "POST", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "回滚页面", Code: "esports:pages:rollback", Path: "/api/v1/esports/admin/pages/{channel}/rollback", Method: "POST", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "查看发布记录", Code: "esports:pages:versions", Path: "/api/v1/esports/admin/pages/{channel}/versions", Method: "GET", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "查看素材库", Code: "esports:pages:assets", Path: "/api/v1/esports/admin/page-assets", Method: "GET", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "新增素材", Code: "esports:pages:asset_create", Path: "/api/v1/esports/admin/page-assets", Method: "POST", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "删除素材", Code: "esports:pages:asset_delete", Path: "/api/v1/esports/admin/page-assets/{id}", Method: "DELETE", Parent: "esports:page:menu"},
			{Type: cmfapp.PermButton, Name: "装修操作按钮", Code: "esports:pages:manage", Parent: "esports:page:menu"},

			// 客服端模块（§8）：客户档案与跟进、企微自动建档、话术库、客服工作台聚合。
			{Type: cmfapp.PermMenu, Name: "客服工作台", Code: "esports:cs:menu", MenuPath: "/esports/cs", Component: "esports/cs/index", Icon: "icon-customer-service", SortOrder: 54},
			{Type: cmfapp.PermAPI, Name: "查看客服工作台", Code: "esports:cs:workbench", Path: "/api/v1/esports/admin/cs/workbench", Method: "GET", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "查看客户档案汇总", Code: "esports:customers:profile", Path: "/api/v1/esports/admin/customers/{id}/profile", Method: "GET", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑客户档案", Code: "esports:customers:profile_update", Path: "/api/v1/esports/admin/customers/{id}/profile", Method: "PUT", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "查看跟进记录", Code: "esports:customers:followups", Path: "/api/v1/esports/admin/customers/{id}/follow-ups", Method: "GET", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "新增跟进记录", Code: "esports:customers:followup_create", Path: "/api/v1/esports/admin/customers/{id}/follow-ups", Method: "POST", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "企微自动建档", Code: "esports:customers:wecom_intake", Path: "/api/v1/esports/admin/customers/wecom-intake", Method: "POST", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "查看话术库", Code: "esports:scripts:list", Path: "/api/v1/esports/admin/scripts", Method: "GET", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "新增话术", Code: "esports:scripts:create", Path: "/api/v1/esports/admin/scripts", Method: "POST", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑话术", Code: "esports:scripts:update", Path: "/api/v1/esports/admin/scripts/{id}", Method: "PUT", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "启停话术", Code: "esports:scripts:status", Path: "/api/v1/esports/admin/scripts/{id}/status", Method: "POST", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermButton, Name: "客服工作台操作按钮", Code: "esports:cs:manage", Parent: "esports:cs:menu"},

			// 储值卡模块（§4.4）：预付账户开卡、余额与流水、退卡审核。
			{Type: cmfapp.PermMenu, Name: "储值卡", Code: "esports:stored_value:menu", MenuPath: "/esports/stored-value", Component: "esports/stored-value/index", Icon: "icon-safe", SortOrder: 55},
			{Type: cmfapp.PermAPI, Name: "查看储值卡", Code: "esports:stored_value:list", Path: "/api/v1/esports/admin/stored-value-cards", Method: "GET", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "开卡", Code: "esports:stored_value:create", Path: "/api/v1/esports/admin/stored-value-cards", Method: "POST", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "查看储值卡详情", Code: "esports:stored_value:detail", Path: "/api/v1/esports/admin/stored-value-cards/{id}", Method: "GET", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "退卡试算", Code: "esports:stored_value:refund_quote", Path: "/api/v1/esports/admin/stored-value-cards/{id}/refund-quote", Method: "GET", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "申请退卡", Code: "esports:stored_value:refund_request", Path: "/api/v1/esports/admin/stored-value-cards/{id}/refund-request", Method: "POST", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "审核退卡", Code: "esports:stored_value:refund_decision", Path: "/api/v1/esports/admin/stored-value-cards/{id}/refund-decision", Method: "POST", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "储值卡核销订单", Code: "esports:stored_value:pay_order", Path: "/api/v1/esports/admin/orders/{id}/pay-by-stored-value-card", Method: "POST", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermButton, Name: "储值卡操作按钮", Code: "esports:stored_value:manage", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermButton, Name: "储值卡退卡审核按钮", Code: "esports:stored_value:refund_approve", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "查看储值卡套餐", Code: "esports:stored_value:package_list", Path: "/api/v1/esports/admin/stored-value-packages", Method: "GET", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "新建套餐", Code: "esports:stored_value:package_create", Path: "/api/v1/esports/admin/stored-value-packages", Method: "POST", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑套餐", Code: "esports:stored_value:package_update", Path: "/api/v1/esports/admin/stored-value-packages/{id}", Method: "PUT", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "上下架套餐", Code: "esports:stored_value:package_status", Path: "/api/v1/esports/admin/stored-value-packages/{id}/status", Method: "POST", Parent: "esports:stored_value:menu"},
		},
	})
}

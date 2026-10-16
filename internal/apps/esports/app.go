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

// activateScheduledCommissionRulesTask 将到点的分成和客服提成版本转为生效状态。
func activateScheduledCommissionRulesTask(ctx context.Context, logger io.Writer) error {
	now := time.Now()
	playerCount, err := activateDueCommissionRules(ctx, global.ORM, now, 200)
	if err != nil {
		return err
	}
	csCount, err := activateDueCsCommissionRules(ctx, global.ORM, now, 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "生效陪玩分成规则：%d；客服提成规则：%d\n", playerCount, csCount)
	}
	return err
}

// generatePlayerSettlementsTask 按各陪玩生效周期（§12.6 逐级覆盖）自动生成上一完整周期的分成结算单。
func generatePlayerSettlementsTask(ctx context.Context, logger io.Writer) error {
	count, err := generateDuePlayerSettlements(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "自动生成陪玩分成结算单：%d\n", count)
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

// flagHighRiskCustomersTask 按风控阈值把高频退款客户标记为「疑似滥用复核」（§13.7，不自动处罚）。
func flagHighRiskCustomersTask(ctx context.Context, logger io.Writer) error {
	count, err := flagHighRiskCustomers(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理高频退款客户风控标记：%d\n", count)
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

// autoOfflinePlayersTask 心跳超时自动离线（§6.4）：在途订单转保护异常并提醒，其余置离线。
func autoOfflinePlayersTask(ctx context.Context, logger io.Writer) error {
	count, err := autoOfflineStalePlayers(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理心跳超时离线：%d\n", count)
	}
	return err
}

// autoPauseDisconnectedPlayersTask 断线超阈值自动暂停（§3.3）：服务中且心跳超阈值的服务段自动暂停并写系统事件。
func autoPauseDisconnectedPlayersTask(ctx context.Context, logger io.Writer) error {
	count, err := autoPauseDisconnectedPlayers(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理断线自动暂停：%d\n", count)
	}
	return err
}

// expireRescheduleProposalsTask 过期改期提案（§3.3）：超时的进行中提案转 expired，订单不变。
func expireRescheduleProposalsTask(ctx context.Context, logger io.Writer) error {
	count, err := expireRescheduleProposals(ctx, global.ORM, time.Now(), 100)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理过期改期提案：%d\n", count)
	}
	return err
}

// applyDueSkuSuspensionsTask 到点的定时停服申请生效（§15.7）：下架 SKU 并生成逐单处置清单。
func applyDueSkuSuspensionsTask(ctx context.Context, logger io.Writer) error {
	count, err := applyDueSkuSuspensions(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理到点停服申请：%d\n", count)
	}
	return err
}

// expireCustomerStoredValueIntentsTask 过期自助购卡支付意图（§4.4）：未核验且已超时的购卡意图转 expired。
func expireCustomerStoredValueIntentsTask(ctx context.Context, logger io.Writer) error {
	count, err := expireCustomerStoredValueIntents(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理过期自助购卡意图：%d\n", count)
	}
	return err
}

// expireRechargeIntentsTask 过期充值支付意图（§4.5）：未核验且已超时的充值意图转 expired。
func expireRechargeIntentsTask(ctx context.Context, logger io.Writer) error {
	count, err := expireRechargeIntents(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理过期充值意图：%d\n", count)
	}
	return err
}

// reconcileLedgersTask 全量核验账本（§11.6）：逐账户核对余额不为负、逐批次核对分摊不超额与引用完整，
// 异常写外盒事件供告警与人工复核。
func reconcileLedgersTask(ctx context.Context, logger io.Writer) error {
	now := time.Now()
	result, err := reconcileLedgers(ctx, global.ORM, ledgerReconcileScanCap)
	if err != nil {
		return err
	}
	if err = publishLedgerReconcileAnomalies(ctx, global.ORM, result, now); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(logger, "核验账本：账户 %d、分摊 %d、批次 %d，异常 %d%s\n",
		result.ScannedAccounts, result.ScannedAllocations, result.ScannedBatches,
		len(result.Anomalies), ledgerTruncationNote(result))
	return nil
}

// ledgerTruncationNote 说明本轮是否未扫完，便于运维判断是否需要手动补跑。
func ledgerTruncationNote(result ledgerReconcileResult) string {
	note := ""
	if result.ScanTruncated {
		note += "；已达单轮扫描上限，下轮继续"
	}
	if result.AnomalyOverflow {
		note += "；异常清单已截断"
	}
	return note
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
		tasks.Register("esports_generate_player_settlements", generatePlayerSettlementsTask)
		tasks.Register("esports_activate_scheduled_commission_rules", activateScheduledCommissionRulesTask)
		tasks.Register("esports_check_aftersale_sla", checkAfterSaleSLATask)
		tasks.Register("esports_check_close_approval_timeout", checkCloseApprovalTimeoutTask)
		tasks.Register("esports_flag_high_risk_customers", flagHighRiskCustomersTask)
		tasks.Register("esports_reconcile_ledgers", reconcileLedgersTask)
		tasks.Register("esports_auto_offline_players", autoOfflinePlayersTask)
		tasks.Register("esports_auto_pause_disconnected_players", autoPauseDisconnectedPlayersTask)
		tasks.Register("esports_expire_reschedule_proposals", expireRescheduleProposalsTask)
		tasks.Register("esports_apply_due_sku_suspensions", applyDueSkuSuspensionsTask)
		tasks.Register("esports_expire_customer_stored_value_intents", expireCustomerStoredValueIntentsTask)
		tasks.Register("esports_expire_recharge_intents", expireRechargeIntentsTask)
	}
	cmfapp.Register(cmfapp.App{
		Code: "esports", Name: "陪玩报单", Version: "0.1.0", Enabled: featureEnabled,
		Config: []cmfapp.ConfigItem{
			{Key: "payment_deadline_minutes", Name: "订单支付时限（分钟）", Value: "0", Type: "number", Group: "esports", Remark: "业务负责人确认前为 0，禁止创建可支付订单"},
			{Key: "duplicate_order_window_minutes", Name: "重复报单提醒时间窗（分钟）", Value: "0", Type: "number", Group: "esports", Remark: "0 关闭；窗口内同客户同 SKU 相似订单需显式确认后才可下单"},
			{Key: "customer_debt_limit_cents", Name: "客户欠款拦截阈值（分）", Value: "0", Type: "number", Group: "esports", Remark: "0 关闭；客户欠款超过该值时拒绝新报单"},
			{Key: "service_disconnect_pause_minutes", Name: "断线自动暂停阈值（分钟）", Value: "0", Type: "number", Group: "esports", Remark: "0 关闭；服务中陪玩心跳超时该时长后自动暂停服务段"},
			{Key: "reschedule_proposal_hours", Name: "改期提案确认时限（小时）", Value: "24", Type: "number", Group: "esports", Remark: "已付款订单改期提案的确认时限，且不晚于目标开始时间"},
			{Key: "stored_value_payment_window_minutes", Name: "自助购卡支付时限（分钟）", Value: "1440", Type: "number", Group: "esports", Remark: "客户自助购卡支付意图的有效时长，超时由任务过期"},
			{Key: "recharge_payment_window_minutes", Name: "充值支付时限（分钟）", Value: "1440", Type: "number", Group: "esports", Remark: "客户自助充值支付意图的有效时长，超时由任务过期"},
		},
		Routes: []cmfapp.Route{
			{Method: "GET", Path: "/skus", Auth: cmfapp.AuthPublic, Handler: publicSkus},
			{Method: "GET", Path: "/user/orders/:id/quote", Auth: cmfapp.AuthUser, Handler: userOrderQuote},
			{Method: "POST", Path: "/user/orders/:id/confirm", Auth: cmfapp.AuthUser, Handler: userConfirmQuote},
			{Method: "GET", Path: "/admin/skus", Auth: cmfapp.AuthAdmin, Handler: adminSkus},
			{Method: "POST", Path: "/admin/skus", Auth: cmfapp.AuthAdmin, Handler: createSku},
			{Method: "PUT", Path: "/admin/skus/:id", Auth: cmfapp.AuthAdmin, Handler: updateSkuHandler},
			// SKU 停服（§15.7）：申请 → 异人审批 → 受影响订单逐单处置；既有直接上下架接口不变。
			{Method: "POST", Path: "/admin/skus/:id/suspensions", Auth: cmfapp.AuthAdmin, Handler: requestSkuSuspensionHandler},
			{Method: "GET", Path: "/admin/sku-suspensions", Auth: cmfapp.AuthAdmin, Handler: adminSkuSuspensions},
			{Method: "GET", Path: "/admin/sku-suspensions/:id", Auth: cmfapp.AuthAdmin, Handler: skuSuspensionDetailHandler},
			{Method: "POST", Path: "/admin/sku-suspensions/:id/decision", Auth: cmfapp.AuthAdmin, Handler: reviewSkuSuspensionHandler},
			{Method: "POST", Path: "/admin/sku-suspensions/:id/cancel", Auth: cmfapp.AuthAdmin, Handler: cancelSkuSuspensionHandler},
			{Method: "POST", Path: "/admin/sku-suspensions/:id/orders/:orderId/disposal", Auth: cmfapp.AuthAdmin, Handler: disposeSkuSuspensionItemHandler},
			{Method: "GET", Path: "/admin/categories", Auth: cmfapp.AuthAdmin, Handler: adminCategories},
			{Method: "POST", Path: "/admin/categories", Auth: cmfapp.AuthAdmin, Handler: createCategory},
			{Method: "PUT", Path: "/admin/categories/:id", Auth: cmfapp.AuthAdmin, Handler: updateCategoryHandler},
			{Method: "DELETE", Path: "/admin/categories/:id", Auth: cmfapp.AuthAdmin, Handler: deleteCategoryHandler},
			// 商品父实体 / 规格组 / 逐席角色（§15.1 / §15.5）：商品挂多 SKU，规格组约束多人规格与每席角色。
			{Method: "GET", Path: "/admin/products", Auth: cmfapp.AuthAdmin, Handler: adminProducts},
			{Method: "GET", Path: "/admin/products/:id", Auth: cmfapp.AuthAdmin, Handler: adminProduct},
			{Method: "POST", Path: "/admin/products", Auth: cmfapp.AuthAdmin, Handler: createProductHandler},
			{Method: "PUT", Path: "/admin/products/:id", Auth: cmfapp.AuthAdmin, Handler: updateProductHandler},
			{Method: "GET", Path: "/admin/spec-groups", Auth: cmfapp.AuthAdmin, Handler: adminSpecGroups},
			{Method: "POST", Path: "/admin/spec-groups", Auth: cmfapp.AuthAdmin, Handler: createSpecGroupHandler},
			{Method: "PUT", Path: "/admin/spec-groups/:id", Auth: cmfapp.AuthAdmin, Handler: updateSpecGroupHandler},
			{Method: "GET", Path: "/admin/orders/:id/seat-roles", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(orderSeatRolesHandler)},
			{Method: "GET", Path: "/admin/customers", Auth: cmfapp.AuthAdmin, Handler: adminCustomers},
			{Method: "POST", Path: "/admin/customers", Auth: cmfapp.AuthAdmin, Handler: createCustomer},
			{Method: "POST", Path: "/admin/customers/:id/age-checks", Auth: cmfapp.AuthAdmin, Handler: createAgeCheck},
			{Method: "GET", Path: "/admin/age-checks", Auth: cmfapp.AuthAdmin, Handler: pendingAgeChecks},
			{Method: "POST", Path: "/admin/age-checks/:id/review", Auth: cmfapp.AuthAdmin, Handler: reviewAgeCheckRequest},
			{Method: "GET", Path: "/admin/orders", Auth: cmfapp.AuthAdmin, Handler: adminOrders},
			{Method: "GET", Path: "/admin/orders/:id", Auth: cmfapp.AuthAdmin, Handler: adminOrder},
			{Method: "POST", Path: "/admin/orders", Auth: cmfapp.AuthAdmin, Handler: createOrder},
			// 历史订单补录（§3.3 / §4.1）：入口固定 admin_backfill，默认不计提业绩，须带 Idempotency-Key。
			{Method: "POST", Path: "/admin/orders/backfill", Auth: cmfapp.AuthAdmin, Handler: backfillOrderHandler},
			// 已核销收款的渠道撤销 / 拒付风险事件（§3.2）：登记后由非登记人处置资金异常。
			{Method: "GET", Path: "/admin/payment-reversals", Auth: cmfapp.AuthAdmin, Handler: listPaymentReversalsHandler},
			{Method: "POST", Path: "/admin/payment-reversals", Auth: cmfapp.AuthAdmin, Handler: registerPaymentReversalHandler},
			{Method: "POST", Path: "/admin/payment-reversals/:id/decision", Auth: cmfapp.AuthAdmin, Handler: decidePaymentReversalHandler},
			{Method: "POST", Path: "/admin/orders/:id/quote-preview", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(previewRepriceHandler)},
			{Method: "POST", Path: "/admin/orders/:id/price-adjustments", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(requestPriceAdjustmentHandler)},
			{Method: "PUT", Path: "/admin/orders/:id/quote", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(repriceOrderHandler)},
			{Method: "GET", Path: "/admin/price-adjustments", Auth: cmfapp.AuthAdmin, Handler: adminPriceAdjustments},
			{Method: "POST", Path: "/admin/price-adjustments/:id/decision", Auth: cmfapp.AuthAdmin, Handler: decidePriceAdjustmentHandler},
			{Method: "POST", Path: "/admin/orders/:id/cancel", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(cancelOrderHandler)},
			// 取消试算（§3.3 公示取消政策）：给出免费窗口、手续费与应退金额。
			{Method: "GET", Path: "/admin/orders/:id/cancel-preview", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(previewCancelOrderHandler)},
			// 限时改期提案（§3.3）：已付款订单改期走提案，客户与已接单成员逐人确认后原子生效。
			{Method: "POST", Path: "/admin/orders/:id/reschedule-proposals", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(proposeRescheduleHandler)},
			{Method: "GET", Path: "/admin/orders/:id/reschedule-proposals", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(listOrderRescheduleProposalsHandler)},
			{Method: "POST", Path: "/admin/reschedule-proposals/:id/withdraw", Auth: cmfapp.AuthAdmin, Handler: ownedRescheduleProposalHandler(withdrawRescheduleProposalHandler)},
			{Method: "PUT", Path: "/admin/orders/:id/appointment", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(rescheduleOrderHandler)},
			// 预约档期确认（§3.2 / §5.5）：request → confirmed，含资源核验；未确认档期不允许派单。
			{Method: "POST", Path: "/admin/orders/:id/appointment/confirm", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(confirmOrderAppointmentHandler)},
			// 客服代陪玩暂停 / 恢复服务段（§3.3）：理由必填，事件以管理员身份留痕。
			{Method: "POST", Path: "/admin/orders/:id/players/:playerId/service/pause", Auth: cmfapp.AuthAdmin, Handler: adminPausePlayerServiceHandler},
			{Method: "POST", Path: "/admin/orders/:id/players/:playerId/service/resume", Auth: cmfapp.AuthAdmin, Handler: adminResumePlayerServiceHandler},
			// 服务段时长争议更正（§3.3）：留痕更正有效 / 暂停秒数并登记停工量与责任方。
			{Method: "POST", Path: "/admin/orders/:id/players/:playerId/service/timing-correction", Auth: cmfapp.AuthAdmin, Handler: adminCorrectServiceTimingHandler},
			{Method: "PUT", Path: "/admin/orders/:id/channel-cost", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(setOrderChannelCostHandler)},
			// 单笔手动指定分成规则（§12.3 最高一级）：仅未锁定成员规则前可设，rule_id=0 清除。
			{Method: "PUT", Path: "/admin/orders/:id/commission-rule", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(setOrderManualCommissionRuleHandler)},
			// 存单：把已付款订单标记为冻结，「恢复」清除标记（原地保留、不动履约状态与名额）。
			// 收款账户与收款方式启停（§4.3）：账户须为工作室主体并经财务审核，客服不能直接启用。
			{Method: "GET", Path: "/admin/payment-accounts", Auth: cmfapp.AuthAdmin, Handler: adminPaymentAccounts},
			{Method: "POST", Path: "/admin/payment-accounts", Auth: cmfapp.AuthAdmin, Handler: createPaymentAccountHandler},
			{Method: "POST", Path: "/admin/payment-accounts/:id/review", Auth: cmfapp.AuthAdmin, Handler: reviewPaymentAccountHandler},
			{Method: "POST", Path: "/admin/payment-accounts/:id/disable", Auth: cmfapp.AuthAdmin, Handler: disablePaymentAccountHandler},
			{Method: "GET", Path: "/admin/payment-channels", Auth: cmfapp.AuthAdmin, Handler: adminPaymentChannels},
			{Method: "POST", Path: "/admin/payment-channels/:channel/status", Auth: cmfapp.AuthAdmin, Handler: setPaymentChannelStatusHandler},
			// 待核验收款（§4.2 / §4.3）：客服提交企微 / 商户码 / 对公到账凭证，财务核验后核销并放行派单。
			{Method: "GET", Path: "/admin/offline-payment-proofs", Auth: cmfapp.AuthAdmin, Handler: adminOfflinePaymentProofs},
			{Method: "POST", Path: "/admin/offline-payment-proofs", Auth: cmfapp.AuthAdmin, Handler: submitOfflinePaymentProofHandler},
			{Method: "POST", Path: "/admin/offline-payment-proofs/:id/verify", Auth: cmfapp.AuthAdmin, Handler: verifyOfflinePaymentProofHandler},
			{Method: "POST", Path: "/admin/offline-payment-proofs/:id/reject", Auth: cmfapp.AuthAdmin, Handler: rejectOfflinePaymentProofHandler},
			{Method: "POST", Path: "/admin/orders/:id/deposit", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(depositOrderHandler)},
			// 结单条件逐项状态（§3.4）：结单按钮据此置灰，与结单拦截口径一致。
			{Method: "GET", Path: "/admin/orders/:id/close-conditions", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(orderCloseConditions)},
			{Method: "POST", Path: "/admin/orders/:id/restore", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(restoreOrderHandler)},
			{Method: "POST", Path: "/admin/orders/:id/pay-by-balance", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(payOrderWithBalanceHandler)},
			{Method: "GET", Path: "/admin/wallets", Auth: cmfapp.AuthAdmin, Handler: adminWallets},
			{Method: "GET", Path: "/admin/wallets/:id/transactions", Auth: cmfapp.AuthAdmin, Handler: adminWalletTransactions},
			{Method: "POST", Path: "/admin/wallets/:id/recharge", Auth: cmfapp.AuthAdmin, Handler: walletMutationHandler(false)},
			{Method: "POST", Path: "/admin/wallets/:id/adjust", Auth: cmfapp.AuthAdmin, Handler: walletMutationHandler(true)},
			// 余额账户按充值批次的退款试算（§4.5）：纯只读，逐批还原可退本金与作废赠送。
			{Method: "GET", Path: "/admin/wallets/:id/refund-quote", Auth: cmfapp.AuthAdmin, Handler: adminWalletRefundQuote},
			// 批次分摊明细（§4.3 / §11.3）：查「每笔消费动用了哪些批次」。
			{Method: "GET", Path: "/admin/ledger-allocations", Auth: cmfapp.AuthAdmin, Handler: adminLedgerAllocations},
			// 充值活动：活动配置与按活动自动匹配赠送的充值。
			{Method: "GET", Path: "/admin/campaigns", Auth: cmfapp.AuthAdmin, Handler: adminCampaigns},
			{Method: "POST", Path: "/admin/campaigns", Auth: cmfapp.AuthAdmin, Handler: createCampaignHandler},
			{Method: "PUT", Path: "/admin/campaigns/:id", Auth: cmfapp.AuthAdmin, Handler: updateCampaignHandler},
			{Method: "POST", Path: "/admin/campaigns/:id/status", Auth: cmfapp.AuthAdmin, Handler: setCampaignStatusHandler},
			{Method: "POST", Path: "/admin/wallets/:id/recharge-campaigns", Auth: cmfapp.AuthAdmin, Handler: rechargeWalletByCampaignHandler},
			// 充值独立支付意图（§4.5）：财务核验客户充值到账并按意图内锁定快照入账。
			{Method: "POST", Path: "/admin/recharge-intents/:intentNo/verify", Auth: cmfapp.AuthAdmin, Handler: adminVerifyRechargeIntentHandler},
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
			{Method: "GET", Path: "/admin/gift-intents", Auth: cmfapp.AuthAdmin, Handler: adminGiftIntents},
			{Method: "POST", Path: "/admin/gift-intents", Auth: cmfapp.AuthAdmin, Handler: createGiftIntentHandler},
			{Method: "POST", Path: "/admin/gift-orders", Auth: cmfapp.AuthAdmin, Handler: sendGiftHandler},
			{Method: "POST", Path: "/admin/gift-orders/:id/refund", Auth: cmfapp.AuthAdmin, Handler: refundGiftHandler},
			{Method: "POST", Path: "/admin/gift-orders/:id/review", Auth: cmfapp.AuthAdmin, Handler: reviewGiftHandler},
			{Method: "GET", Path: "/admin/gift-leaderboard", Auth: cmfapp.AuthAdmin, Handler: giftLeaderboardHandler},
			// 礼物争议（§9.5）：客户申请 → 客服审核 → 执行退款（支持部分退款）。
			{Method: "GET", Path: "/admin/gift-disputes", Auth: cmfapp.AuthAdmin, Handler: adminGiftDisputes},
			{Method: "POST", Path: "/admin/gift-disputes", Auth: cmfapp.AuthAdmin, Handler: createGiftDisputeByAdminHandler},
			{Method: "POST", Path: "/admin/gift-disputes/:id/review", Auth: cmfapp.AuthAdmin, Handler: reviewGiftDisputeHandler},
			{Method: "POST", Path: "/admin/gift-disputes/:id/execute", Auth: cmfapp.AuthAdmin, Handler: executeGiftDisputeHandler},
			{Method: "GET", Path: "/user/gift-orders", Auth: cmfapp.AuthUser, Handler: userGiftOrdersHandler},
			{Method: "GET", Path: "/user/gift-disputes", Auth: cmfapp.AuthUser, Handler: userGiftDisputes},
			{Method: "POST", Path: "/user/gift-disputes", Auth: cmfapp.AuthUser, Handler: userCreateGiftDisputeHandler},
			{Method: "POST", Path: "/user/gift-disputes/:id/withdraw", Auth: cmfapp.AuthUser, Handler: userWithdrawGiftDisputeHandler},
			{Method: "GET", Path: "/admin/dispatch/board", Auth: cmfapp.AuthAdmin, Handler: dispatchBoardHandler},
			// 优先派单权（§5.4）与派单候选排序（§5.5 首版）。
			{Method: "GET", Path: "/admin/dispatch/priority", Auth: cmfapp.AuthAdmin, Handler: adminDispatchPriority},
			{Method: "POST", Path: "/admin/dispatch/priority", Auth: cmfapp.AuthAdmin, Handler: grantDispatchPriorityHandler},
			{Method: "POST", Path: "/admin/dispatch/priority/:id/revoke", Auth: cmfapp.AuthAdmin, Handler: revokeDispatchPriorityHandler},
			{Method: "GET", Path: "/admin/orders/:id/dispatch-candidates", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(adminDispatchCandidates)},
			{Method: "GET", Path: "/admin/orders/:id/priority-notices", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(adminOrderPriorityNotices)},
			{Method: "POST", Path: "/admin/orders/:id/release-priority", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(releaseOrderPriorityHandler)},
			// 普通待派单订单发布到抢单大厅（§5.2 / §5.6）：没有优先名单的游戏分区由此进入大厅。
			{Method: "POST", Path: "/admin/orders/:id/publish-grab", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(publishOrderToGrabHallHandler)},
			// 陪玩转单（§5.7）：接单后无法服务由陪玩发起，须客服确认才释放席位。
			{Method: "GET", Path: "/admin/seat-transfers", Auth: cmfapp.AuthAdmin, Handler: adminSeatTransfers},
			{Method: "POST", Path: "/admin/seat-transfers/:id/decision", Auth: cmfapp.AuthAdmin, Handler: decideSeatTransferHandler},
			{Method: "GET", Path: "/admin/orders/:id/seats", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(orderSeatsHandler)},
			{Method: "POST", Path: "/admin/orders/:id/seats/:seatIndex/assign", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(assignSeatHandler)},
			{Method: "POST", Path: "/admin/orders/:id/seats/:seatIndex/revoke", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(revokeSeatHandler)},
			// 固排成队（§5.3）：主陪玩 + 固排一次成队。
			{Method: "GET", Path: "/admin/orders/:id/teammate-candidates", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(adminTeammateCandidates)},
			{Method: "POST", Path: "/admin/orders/:id/team-dispatch", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(teamDispatchHandler)},
			{Method: "POST", Path: "/admin/orders/:id/claim", Auth: cmfapp.AuthAdmin, Handler: claimOrderHandler},
			{Method: "POST", Path: "/admin/orders/:id/transfer", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(transferOrderHandler)},
			{Method: "POST", Path: "/admin/orders/:id/remind", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(remindOrderHandler)},
			// 加钟（§3.3 / §6.2 / §12.5）：服务中订单追加金额与工时，由陪玩确认后累加并生成加钟分成。
			{Method: "GET", Path: "/admin/orders/:id/addons", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(listOrderAddonsHandler)},
			{Method: "POST", Path: "/admin/orders/:id/addons", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(createOrderAddonHandler)},
			{Method: "POST", Path: "/admin/order-addons/:id/cancel", Auth: cmfapp.AuthAdmin, Handler: ownedAddonHandler(cancelOrderAddonHandler)},
			// 加钟收款（§3.3）：对已确认的加钟单用客户余额核销追加金额。
			{Method: "POST", Path: "/admin/order-addons/:id/pay-by-balance", Auth: cmfapp.AuthAdmin, Handler: ownedAddonHandler(payOrderAddonByBalanceHandler)},
			// 结单（客服手动结单）与售后 / 退款模块。
			{Method: "POST", Path: "/admin/orders/:id/close", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(closeOrderHandler)},
			// 结单复核（§3.4）：金额达复核阈值的订单进入待复核，由非负责客服的管理员通过 / 驳回。
			{Method: "POST", Path: "/admin/orders/:id/close-approve", Auth: cmfapp.AuthAdmin, Handler: approveCloseReviewHandler},
			{Method: "POST", Path: "/admin/orders/:id/close-reject", Auth: cmfapp.AuthAdmin, Handler: rejectCloseReviewHandler},
			// 结单表单（§3.4）：返回订单适用的表单模板与字段定义，供结单弹窗渲染。
			{Method: "GET", Path: "/admin/orders/:id/close-form", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(orderCloseFormHandler)},
			// 结单审批进度（§3.4）：当前待审批节点，供复核弹窗展示。
			{Method: "GET", Path: "/admin/orders/:id/close-approval", Auth: cmfapp.AuthAdmin, Handler: orderCloseApprovalHandler},
			{Method: "GET", Path: "/admin/after-sales", Auth: cmfapp.AuthAdmin, Handler: adminAfterSales},
			{Method: "GET", Path: "/admin/after-sales/:id", Auth: cmfapp.AuthAdmin, Handler: ownedAfterSaleHandler(adminAfterSaleDetail)},
			{Method: "POST", Path: "/admin/after-sales", Auth: cmfapp.AuthAdmin, Handler: createAfterSaleHandler},
			{Method: "POST", Path: "/admin/after-sales/:id/accept", Auth: cmfapp.AuthAdmin, Handler: ownedAfterSaleHandler(acceptAfterSaleHandler)},
			{Method: "POST", Path: "/admin/after-sales/:id/plan", Auth: cmfapp.AuthAdmin, Handler: ownedAfterSaleHandler(planAfterSaleHandler)},
			// 待客户确认（§13.3）：客服可代客户确认方案（客户线下已同意），随后进入待审批。
			{Method: "POST", Path: "/admin/after-sales/:id/approve", Auth: cmfapp.AuthAdmin, Handler: approveAfterSaleHandler},
			{Method: "POST", Path: "/admin/after-sales/:id/reject", Auth: cmfapp.AuthAdmin, Handler: rejectAfterSaleHandler},
			{Method: "POST", Path: "/admin/after-sales/:id/execute", Auth: cmfapp.AuthAdmin, Handler: executeAfterSaleHandler},
			{Method: "POST", Path: "/admin/after-sales/:id/withdraw", Auth: cmfapp.AuthAdmin, Handler: ownedAfterSaleHandler(withdrawAfterSaleHandler)},
			// 多动作售后方案（§13.3 / §13.6）：动作项列表与逐项执行 / 失败重试。
			{Method: "GET", Path: "/admin/after-sales/:id/actions", Auth: cmfapp.AuthAdmin, Handler: ownedAfterSaleHandler(listAfterSaleActionsHandler)},
			{Method: "POST", Path: "/admin/after-sales/:id/actions/:seq/execute", Auth: cmfapp.AuthAdmin, Handler: ownedAfterSaleHandler(executeAfterSaleActionHandler)},
			// 按充值批次生成外部退款执行明细（§4.5）。
			{Method: "POST", Path: "/admin/after-sales/:id/refund-execute", Auth: cmfapp.AuthAdmin, Handler: ownedAfterSaleHandler(adminRechargeRefundExecuteHandler)},
			// 财务登记外部打款后置执行明细为 executed（§4.5）。
			{Method: "POST", Path: "/admin/recharge-refund-executions/:id/execute", Auth: cmfapp.AuthAdmin, Handler: markRechargeRefundExecutionExecutedHandler},
			// 补履约任务（§13.6）：售后方案「补钟 / 换人」执行后产生，由客服开始服务并验收。
			{Method: "GET", Path: "/admin/fulfillment-makeups", Auth: cmfapp.AuthAdmin, Handler: listFulfillmentMakeupsHandler},
			{Method: "POST", Path: "/admin/fulfillment-makeups/:id/start", Auth: cmfapp.AuthAdmin, Handler: startFulfillmentMakeupHandler},
			{Method: "POST", Path: "/admin/fulfillment-makeups/:id/complete", Auth: cmfapp.AuthAdmin, Handler: completeFulfillmentMakeupHandler},
			// 终态售后新证据复核（§13.6）：已驳回 / 已完成的售后可申请复核，由异人审查维持或发回重处理。
			{Method: "GET", Path: "/admin/after-sales/:id/reviews", Auth: cmfapp.AuthAdmin, Handler: ownedAfterSaleHandler(listAfterSaleReviewsHandler)},
			{Method: "POST", Path: "/admin/after-sales/:id/reviews", Auth: cmfapp.AuthAdmin, Handler: ownedAfterSaleHandler(requestAfterSaleReviewHandler)},
			{Method: "POST", Path: "/admin/after-sale-reviews/:id/decision", Auth: cmfapp.AuthAdmin, Handler: ownedAfterSaleReviewHandler(decideAfterSaleReviewHandler)},
			{Method: "POST", Path: "/admin/after-sale-reviews/:id/withdraw", Auth: cmfapp.AuthAdmin, Handler: ownedAfterSaleReviewHandler(withdrawAfterSaleReviewHandler)},
			// 陪玩责任信用（§13.5）：售后责任方判定为陪玩后自动记载，复核决定是否暂停接单。
			{Method: "GET", Path: "/admin/player-credit-events", Auth: cmfapp.AuthAdmin, Handler: listPlayerCreditEventsHandler},
			{Method: "POST", Path: "/admin/player-credit-events/:id/review", Auth: cmfapp.AuthAdmin, Handler: reviewPlayerCreditEventHandler},
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
			// 客户风控（§13.7）：风险名单与人工标记 / 解除。
			{Method: "GET", Path: "/admin/risk-customers", Auth: cmfapp.AuthAdmin, Handler: listRiskCustomersHandler},
			{Method: "POST", Path: "/admin/customers/:id/risk", Auth: cmfapp.AuthAdmin, Handler: markCustomerRiskHandler},
			{Method: "GET", Path: "/admin/settlements", Auth: cmfapp.AuthAdmin, Handler: adminSettlements},
			{Method: "POST", Path: "/admin/settlements", Auth: cmfapp.AuthAdmin, Handler: createSettlementHandler},
			{Method: "POST", Path: "/admin/settlements/:id/lock", Auth: cmfapp.AuthAdmin, Handler: lockSettlementHandler},
			{Method: "GET", Path: "/admin/settlements/:id", Auth: cmfapp.AuthAdmin, Handler: adminSettlementDetail},
			// 结算周期（§12.6）：日/周/月/自定义间隔，按「全局 → 游戏 → 等级 → 陪玩」逐级覆盖。
			{Method: "GET", Path: "/admin/settlement-cycles", Auth: cmfapp.AuthAdmin, Handler: adminSettlementCycles},
			{Method: "POST", Path: "/admin/settlement-cycles", Auth: cmfapp.AuthAdmin, Handler: createSettlementCycleHandler},
			{Method: "PUT", Path: "/admin/settlement-cycles/:id", Auth: cmfapp.AuthAdmin, Handler: updateSettlementCycleHandler},
			{Method: "POST", Path: "/admin/settlement-cycles/:id/status", Auth: cmfapp.AuthAdmin, Handler: setSettlementCycleStatusHandler},
			// 争议冻结（§12.6）：受理售后自动冻结，另提供人工冻结 / 解冻订单待结算分成明细的入口。
			{Method: "POST", Path: "/admin/orders/:id/freeze-commission", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(freezeOrderCommissionHandler)},
			{Method: "POST", Path: "/admin/orders/:id/unfreeze-commission", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(unfreezeOrderCommissionHandler)},
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
			// 提现打款执行状态机与外部渠道退款（§6.3 / §12.6 人工核验分支）。
			{Method: "POST", Path: "/admin/withdrawals/:id/payout-request", Auth: cmfapp.AuthAdmin, Handler: requestWithdrawalPayoutHandler},
			{Method: "GET", Path: "/admin/withdrawals/:id/payouts", Auth: cmfapp.AuthAdmin, Handler: listWithdrawalPayoutsHandler},
			{Method: "POST", Path: "/admin/withdrawal-payouts/:id/paid", Auth: cmfapp.AuthAdmin, Handler: markWithdrawalPayoutPaidHandler},
			{Method: "POST", Path: "/admin/withdrawal-payouts/:id/failed", Auth: cmfapp.AuthAdmin, Handler: markWithdrawalPayoutFailedHandler},
			{Method: "POST", Path: "/admin/external-refunds", Auth: cmfapp.AuthAdmin, Handler: registerExternalRefundHandler},
			{Method: "GET", Path: "/admin/external-refunds", Auth: cmfapp.AuthAdmin, Handler: listExternalRefundsHandler},
			{Method: "POST", Path: "/admin/external-refunds/:id/execute", Auth: cmfapp.AuthAdmin, Handler: executeExternalRefundHandler},
			// 陪玩模块管理端：档案 CRUD 与上下线。
			{Method: "GET", Path: "/admin/players", Auth: cmfapp.AuthAdmin, Handler: adminPlayers},
			{Method: "POST", Path: "/admin/players", Auth: cmfapp.AuthAdmin, Handler: createPlayer},
			{Method: "PUT", Path: "/admin/players/:id", Auth: cmfapp.AuthAdmin, Handler: updatePlayerHandler},
			// 陪玩端自助：供 uni-app 客户端调用，不挂管理端菜单。
			{Method: "GET", Path: "/user/player/dashboard", Auth: cmfapp.AuthUser, Handler: playerDashboardHandler},
			{Method: "GET", Path: "/user/player/profile", Auth: cmfapp.AuthUser, Handler: playerProfileHandler},
			{Method: "POST", Path: "/user/player/presence", Auth: cmfapp.AuthUser, Handler: playerPresenceHandler},
			// 陪玩端保活（§6.4）：刷新心跳时戳，超时由 esports_auto_offline_players 任务自动离线。
			{Method: "POST", Path: "/user/player/heartbeat", Auth: cmfapp.AuthUser, Handler: playerHeartbeatHandler},
			{Method: "GET", Path: "/user/player/invites", Auth: cmfapp.AuthUser, Handler: playerInvitesHandler},
			{Method: "POST", Path: "/user/player/invites/:id/accept", Auth: cmfapp.AuthUser, Handler: playerAcceptInviteHandler},
			{Method: "POST", Path: "/user/player/invites/:id/reject", Auth: cmfapp.AuthUser, Handler: playerRejectInviteHandler},
			{Method: "GET", Path: "/user/player/grab-hall", Auth: cmfapp.AuthUser, Handler: playerGrabHallHandler},
			{Method: "POST", Path: "/user/player/orders/:id/grab", Auth: cmfapp.AuthUser, Handler: playerGrabHandler},
			{Method: "GET", Path: "/user/player/orders", Auth: cmfapp.AuthUser, Handler: playerOrdersHandler},
			{Method: "GET", Path: "/user/player/service-segments", Auth: cmfapp.AuthUser, Handler: playerServiceTimingsHandler},
			{Method: "POST", Path: "/user/player/orders/:id/service/start", Auth: cmfapp.AuthUser, Handler: playerStartServiceHandler},
			{Method: "POST", Path: "/user/player/orders/:id/service/pause", Auth: cmfapp.AuthUser, Handler: playerPauseServiceHandler},
			{Method: "POST", Path: "/user/player/orders/:id/service/resume", Auth: cmfapp.AuthUser, Handler: playerResumeServiceHandler},
			{Method: "POST", Path: "/user/player/orders/:id/service/finish", Auth: cmfapp.AuthUser, Handler: playerFinishServiceHandler},
			// 陪玩端服务售后申诉（§13.2）：就本人服务的订单发起售后，客服受理后走统一售后状态机。
			{Method: "POST", Path: "/user/player/order-after-sales", Auth: cmfapp.AuthUser, Handler: playerCreateOrderAfterSaleHandler},
			{Method: "GET", Path: "/user/player/order-after-sales", Auth: cmfapp.AuthUser, Handler: playerOrderAfterSalesHandler},
			// 陪玩端主动邀请固排（§5.3）：主陪玩就本人已接单的订单邀请固排占剩余席位。
			{Method: "GET", Path: "/user/player/orders/:id/teammate-candidates", Auth: cmfapp.AuthUser, Handler: playerTeammateCandidatesHandler},
			{Method: "POST", Path: "/user/player/orders/:id/team-invites", Auth: cmfapp.AuthUser, Handler: playerInviteTeammatesHandler},
			// 陪玩端本人信用记载（§13.5）。
			{Method: "GET", Path: "/user/player/credit-events", Auth: cmfapp.AuthUser, Handler: playerCreditEventsHandler},
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
			// 陪玩端自助：固排（§5.3 / §6.1）——列表、发起、确认 / 拒绝、解除、选人搜索。
			{Method: "GET", Path: "/user/player/teammates", Auth: cmfapp.AuthUser, Handler: playerTeammatesHandler},
			{Method: "POST", Path: "/user/player/teammates", Auth: cmfapp.AuthUser, Handler: playerRequestTeammateHandler},
			{Method: "POST", Path: "/user/player/teammates/:id/dissolve", Auth: cmfapp.AuthUser, Handler: playerDissolveTeammateHandler},
			{Method: "GET", Path: "/user/player/teammate-requests", Auth: cmfapp.AuthUser, Handler: playerTeammateRequestsHandler},
			{Method: "POST", Path: "/user/player/teammate-requests/:id/accept", Auth: cmfapp.AuthUser, Handler: playerAcceptTeammateHandler},
			{Method: "POST", Path: "/user/player/teammate-requests/:id/decline", Auth: cmfapp.AuthUser, Handler: playerDeclineTeammateHandler},
			{Method: "GET", Path: "/user/player/players", Auth: cmfapp.AuthUser, Handler: playerSearchPlayersHandler},
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
			// 客户自助（§13.2 / §10）：下单、我的订单 / 售后、余额与资料，均按绑定客户做数据隔离。
			{Method: "POST", Path: "/user/orders", Auth: cmfapp.AuthUser, Handler: userPlaceOrderHandler},
			{Method: "GET", Path: "/user/orders", Auth: cmfapp.AuthUser, Handler: userOrdersHandler},
			{Method: "GET", Path: "/user/orders/:id", Auth: cmfapp.AuthUser, Handler: userOrderDetailHandler},
			{Method: "POST", Path: "/user/orders/:id/cancel", Auth: cmfapp.AuthUser, Handler: userCancelUnpaidOrderHandler},
			{Method: "GET", Path: "/user/orders/:id/members", Auth: cmfapp.AuthUser, Handler: userOrderMembersHandler},
			{Method: "GET", Path: "/user/aftersales", Auth: cmfapp.AuthUser, Handler: userAfterSalesHandler},
			{Method: "POST", Path: "/user/aftersales", Auth: cmfapp.AuthUser, Handler: userCreateAfterSaleHandler},
			{Method: "GET", Path: "/user/wallets", Auth: cmfapp.AuthUser, Handler: userWalletHandler},
			{Method: "GET", Path: "/user/profile", Auth: cmfapp.AuthUser, Handler: userProfileHandler},
			// 客户自助储值卡（§4.4）：在售套餐、自助购卡（合同确认 + 支付意图）、我的储值卡与购卡申请详情。
			{Method: "GET", Path: "/user/stored-value-packages", Auth: cmfapp.AuthUser, Handler: userStoredValuePackagesHandler},
			{Method: "POST", Path: "/user/stored-value-cards", Auth: cmfapp.AuthUser, Handler: userPurchaseStoredValueCardHandler},
			{Method: "GET", Path: "/user/stored-value-cards", Auth: cmfapp.AuthUser, Handler: userStoredValueCardsHandler},
			{Method: "GET", Path: "/user/stored-value-cards/:id", Auth: cmfapp.AuthUser, Handler: userStoredValueCardDetailHandler},
			{Method: "GET", Path: "/user/stored-value-cards/:id/purchase", Auth: cmfapp.AuthUser, Handler: userStoredValuePurchaseDetailHandler},
			// 客户自助充值意图（§4.5）：锁定活动快照 + 支付意图 + 付款指引。
			{Method: "POST", Path: "/user/recharge-intents", Auth: cmfapp.AuthUser, Handler: userCreateRechargeIntentHandler},
			// 客户 / 陪玩端确认 / 拒绝改期提案（§3.3）。
			{Method: "POST", Path: "/user/reschedule-proposals/:id/confirm", Auth: cmfapp.AuthUser, Handler: customerConfirmRescheduleProposalHandler},
			{Method: "POST", Path: "/user/player/reschedule-proposals/:id/confirm", Auth: cmfapp.AuthUser, Handler: playerConfirmRescheduleProposalHandler},
			{Method: "GET", Path: "/user/gift-intents", Auth: cmfapp.AuthUser, Handler: userGiftIntents},
			{Method: "POST", Path: "/user/gift-intents/:id/confirm", Auth: cmfapp.AuthUser, Handler: userConfirmGiftIntent},
			// 陪玩端自助：我的追偿明细（§13.4）。
			{Method: "GET", Path: "/user/player/recoveries", Auth: cmfapp.AuthUser, Handler: playerRecoveriesHandler},
			// 客户自助：售后方（§13.3）确认 / 拒绝待确认方案。
			{Method: "POST", Path: "/user/after-sales/:id/confirm", Auth: cmfapp.AuthUser, Handler: userConfirmAfterSaleHandler},
			{Method: "POST", Path: "/user/after-sales/:id/decline", Auth: cmfapp.AuthUser, Handler: userDeclineAfterSaleHandler},
			// 客户售后凭证附件清单（§13.2 只读确认）。
			{Method: "GET", Path: "/user/after-sales/:id/evidence", Auth: cmfapp.AuthUser, Handler: userAfterSaleEvidenceHandler},
			// 经营统计报表（§17）：五类只读聚合报表，按 :type 分派，支持多维筛选与下钻明细。
			{Method: "GET", Path: "/admin/reports/:type", Auth: cmfapp.AuthAdmin, Handler: adminReports},
			// 报表导出与审计（§17）：CSV 导出 + 只增审计记录。
			{Method: "GET", Path: "/admin/reports/:type/export", Auth: cmfapp.AuthAdmin, Handler: adminReportExport},
			{Method: "GET", Path: "/admin/reports/:type/exports", Auth: cmfapp.AuthAdmin, Handler: adminReportTypeExports},
			{Method: "GET", Path: "/admin/report-exports", Auth: cmfapp.AuthAdmin, Handler: adminReportExports},
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
			{Method: "POST", Path: "/admin/stored-value-cards/:id/payment-verification", Auth: cmfapp.AuthAdmin, Handler: verifyStoredValueCardPaymentHandler},
			// 客户自助购卡到账核验（§4.4）：复用既有核验边界，核验后同步支付意图并写激活外盒。
			{Method: "POST", Path: "/admin/stored-value-cards/:id/verify-self-purchase", Auth: cmfapp.AuthAdmin, Handler: adminVerifyCustomerStoredValuePurchaseHandler},
			{Method: "GET", Path: "/admin/stored-value-cards/:id", Auth: cmfapp.AuthAdmin, Handler: adminStoredValueCardDetail},
			{Method: "GET", Path: "/admin/stored-value-cards/:id/refund-quote", Auth: cmfapp.AuthAdmin, Handler: storedValueCardRefundQuoteHandler},
			// 退卡批次维度试算（§4.5）：与退卡试算同口径，额外按预付款批次展开逐批还原明细。
			{Method: "GET", Path: "/admin/stored-value-cards/:id/refund-batch-quote", Auth: cmfapp.AuthAdmin, Handler: adminStoredValueCardRefundBatchQuote},
			{Method: "POST", Path: "/admin/stored-value-cards/:id/refund-request", Auth: cmfapp.AuthAdmin, Handler: requestStoredValueRefundHandler},
			{Method: "POST", Path: "/admin/stored-value-cards/:id/refund-decision", Auth: cmfapp.AuthAdmin, Handler: decideStoredValueRefundHandler},
			{Method: "POST", Path: "/admin/orders/:id/pay-by-stored-value-card", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(payOrderByStoredValueCardHandler)},
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
			// SKU 停服（§15.7）：申请 / 列表 / 详情 / 异人审批 / 撤回 / 逐单处置。
			{Type: cmfapp.PermAPI, Name: "发起停服申请", Code: "esports:skus:suspension_request", Path: "/api/v1/esports/admin/skus/{id}/suspensions", Method: "POST", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "查看停服申请", Code: "esports:skus:suspension_list", Path: "/api/v1/esports/admin/sku-suspensions", Method: "GET", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "停服申请详情", Code: "esports:skus:suspension_detail", Path: "/api/v1/esports/admin/sku-suspensions/{id}", Method: "GET", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "审批停服申请", Code: "esports:skus:suspension_decision", Path: "/api/v1/esports/admin/sku-suspensions/{id}/decision", Method: "POST", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "撤回停服申请", Code: "esports:skus:suspension_cancel", Path: "/api/v1/esports/admin/sku-suspensions/{id}/cancel", Method: "POST", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "停服逐单处置", Code: "esports:skus:suspension_disposal", Path: "/api/v1/esports/admin/sku-suspensions/{id}/orders/{orderId}/disposal", Method: "POST", Parent: "esports:product:menu"},
			// 商品父实体 / 规格组（§15.1 / §15.5）。
			{Type: cmfapp.PermAPI, Name: "查看商品", Code: "esports:products:list", Path: "/api/v1/esports/admin/products", Method: "GET", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "查看商品详情", Code: "esports:products:detail", Path: "/api/v1/esports/admin/products/{id}", Method: "GET", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "创建商品", Code: "esports:products:create", Path: "/api/v1/esports/admin/products", Method: "POST", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑商品", Code: "esports:products:update", Path: "/api/v1/esports/admin/products/{id}", Method: "PUT", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "查看规格组", Code: "esports:spec_groups:list", Path: "/api/v1/esports/admin/spec-groups", Method: "GET", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "创建规格组", Code: "esports:spec_groups:create", Path: "/api/v1/esports/admin/spec-groups", Method: "POST", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑规格组", Code: "esports:spec_groups:update", Path: "/api/v1/esports/admin/spec-groups/{id}", Method: "PUT", Parent: "esports:product:menu"},
			{Type: cmfapp.PermButton, Name: "新增商品按钮", Code: "esports:skus:add", Parent: "esports:product:menu"},
			{Type: cmfapp.PermButton, Name: "编辑商品按钮", Code: "esports:skus:edit", Parent: "esports:product:menu"},
			{Type: cmfapp.PermButton, Name: "管理产品分类按钮", Code: "esports:categories:manage", Parent: "esports:product:menu"},

			// 订单模块。
			{Type: cmfapp.PermMenu, Name: "订单", Code: "esports:order:menu", MenuPath: "/esports/orders", Component: "esports/orders/index", Icon: "icon-list", SortOrder: 41},
			{Type: cmfapp.PermAPI, Name: "查看订单", Code: "esports:orders:list", Path: "/api/v1/esports/admin/orders", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "查看订单详情", Code: "esports:orders:get", Path: "/api/v1/esports/admin/orders/{id}", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "订单改价", Code: "esports:orders:reprice", Path: "/api/v1/esports/admin/orders/{id}/quote", Method: "PUT", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "改价预览", Code: "esports:orders:quote_preview", Path: "/api/v1/esports/admin/orders/{id}/quote-preview", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "发起超限改价审批", Code: "esports:orders:price_request", Path: "/api/v1/esports/admin/orders/{id}/price-adjustments", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "查看改价审批", Code: "esports:orders:price_approvals", Path: "/api/v1/esports/admin/price-adjustments", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "复核超限改价", Code: "esports:orders:price_decision", Path: "/api/v1/esports/admin/price-adjustments/{id}/decision", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermButton, Name: "复核超限改价按钮", Code: "esports:orders:price_approve", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "订单取消", Code: "esports:orders:cancel", Path: "/api/v1/esports/admin/orders/{id}/cancel", Method: "POST", Parent: "esports:order:menu"},
			// 历史订单补录（§3.3 / §4.1）：默认不计提业绩，须单独授权。
			{Type: cmfapp.PermAPI, Name: "历史订单补录", Code: "esports:orders:backfill", Path: "/api/v1/esports/admin/orders/backfill", Method: "POST", Parent: "esports:order:menu"},
			// 已核销收款的渠道撤销 / 拒付（§3.2）。
			{Type: cmfapp.PermAPI, Name: "查看渠道撤销拒付", Code: "esports:payments:reversal_list", Path: "/api/v1/esports/admin/payment-reversals", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "登记渠道撤销拒付", Code: "esports:payments:reversal_register", Path: "/api/v1/esports/admin/payment-reversals", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "处置渠道撤销拒付", Code: "esports:payments:reversal_decide", Path: "/api/v1/esports/admin/payment-reversals/{id}/decision", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "取消试算", Code: "esports:orders:cancel_preview", Path: "/api/v1/esports/admin/orders/{id}/cancel-preview", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "订单改期", Code: "esports:orders:reschedule", Path: "/api/v1/esports/admin/orders/{id}/appointment", Method: "PUT", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "登记渠道成本", Code: "esports:orders:channel-cost", Path: "/api/v1/esports/admin/orders/{id}/channel-cost", Method: "PUT", Parent: "esports:order:menu"},
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
			// 服务段代操作与争议更正（§3.3）：客服代陪玩暂停 / 恢复与留痕更正。
			{Type: cmfapp.PermAPI, Name: "代操作服务暂停", Code: "esports:orders:service_pause_admin", Path: "/api/v1/esports/admin/orders/{id}/players/{playerId}/service/pause", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "代操作服务恢复", Code: "esports:orders:service_resume_admin", Path: "/api/v1/esports/admin/orders/{id}/players/{playerId}/service/resume", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "服务时长更正", Code: "esports:orders:service_timing_correction", Path: "/api/v1/esports/admin/orders/{id}/players/{playerId}/service/timing-correction", Method: "POST", Parent: "esports:order:menu"},
			// 限时改期提案（§3.3）：发起 / 查看 / 撤回。
			{Type: cmfapp.PermAPI, Name: "发起改期提案", Code: "esports:orders:reschedule_propose", Path: "/api/v1/esports/admin/orders/{id}/reschedule-proposals", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "查看改期提案", Code: "esports:orders:reschedule_list", Path: "/api/v1/esports/admin/orders/{id}/reschedule-proposals", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "撤回改期提案", Code: "esports:orders:reschedule_withdraw", Path: "/api/v1/esports/admin/reschedule-proposals/{id}/withdraw", Method: "POST", Parent: "esports:order:menu"},
			// 订单逐席角色快照（§15.1 / §15.5）。
			{Type: cmfapp.PermAPI, Name: "查看订单席位角色", Code: "esports:orders:seat_roles", Path: "/api/v1/esports/admin/orders/{id}/seat-roles", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermButton, Name: "订单操作按钮", Code: "esports:orders:operate", Parent: "esports:order:menu"},

			// 钱包模块：客户余额与陪玩猫粮账户、只增流水。
			{Type: cmfapp.PermMenu, Name: "钱包", Code: "esports:wallet:menu", MenuPath: "/esports/wallets", Component: "esports/wallets/index", Icon: "icon-wallet", SortOrder: 43},
			{Type: cmfapp.PermAPI, Name: "查看钱包账户", Code: "esports:wallets:list", Path: "/api/v1/esports/admin/wallets", Method: "GET", Parent: "esports:wallet:menu"},
			{Type: cmfapp.PermAPI, Name: "查看钱包流水", Code: "esports:wallets:transactions", Path: "/api/v1/esports/admin/wallets/{id}/transactions", Method: "GET", Parent: "esports:wallet:menu"},
			{Type: cmfapp.PermAPI, Name: "钱包充值", Code: "esports:wallets:recharge", Path: "/api/v1/esports/admin/wallets/{id}/recharge", Method: "POST", Parent: "esports:wallet:menu"},
			{Type: cmfapp.PermAPI, Name: "钱包调整", Code: "esports:wallets:adjust", Path: "/api/v1/esports/admin/wallets/{id}/adjust", Method: "POST", Parent: "esports:wallet:menu"},
			// 余额账户按充值批次的退款试算（§4.5）：纯只读，支持审计。
			{Type: cmfapp.PermAPI, Name: "余额批次退款试算", Code: "esports:wallets:refund_quote", Path: "/api/v1/esports/admin/wallets/{id}/refund-quote", Method: "GET", Parent: "esports:wallet:menu"},
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
			{Type: cmfapp.PermAPI, Name: "审批售后", Code: "esports:aftersales:approve", Path: "/api/v1/esports/admin/after-sales/{id}/approve", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "驳回售后", Code: "esports:aftersales:reject", Path: "/api/v1/esports/admin/after-sales/{id}/reject", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "执行售后", Code: "esports:aftersales:execute", Path: "/api/v1/esports/admin/after-sales/{id}/execute", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "撤回售后", Code: "esports:aftersales:withdraw", Path: "/api/v1/esports/admin/after-sales/{id}/withdraw", Method: "POST", Parent: "esports:aftersale:menu"},
			// 多动作售后方案（§13.3 / §13.6）：动作项列表与逐项执行。
			{Type: cmfapp.PermAPI, Name: "查看售后动作项", Code: "esports:aftersales:actions_list", Path: "/api/v1/esports/admin/after-sales/{id}/actions", Method: "GET", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "执行售后动作项", Code: "esports:aftersales:action_execute", Path: "/api/v1/esports/admin/after-sales/{id}/actions/{seq}/execute", Method: "POST", Parent: "esports:aftersale:menu"},
			// 按充值批次退款执行（§4.5）。
			{Type: cmfapp.PermAPI, Name: "批次退款执行", Code: "esports:aftersales:refund_execute", Path: "/api/v1/esports/admin/after-sales/{id}/refund-execute", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "批次退款打款确认", Code: "esports:settlements:recharge_refund_execute", Path: "/api/v1/esports/admin/recharge-refund-executions/{id}/execute", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看补履约任务", Code: "esports:aftersales:makeups", Path: "/api/v1/esports/admin/fulfillment-makeups", Method: "GET", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "开始补履约服务", Code: "esports:aftersales:makeup_start", Path: "/api/v1/esports/admin/fulfillment-makeups/{id}/start", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "验收补履约", Code: "esports:aftersales:makeup_complete", Path: "/api/v1/esports/admin/fulfillment-makeups/{id}/complete", Method: "POST", Parent: "esports:aftersale:menu"},
			// 终态售后新证据复核（§13.6）。
			{Type: cmfapp.PermAPI, Name: "查看售后复核记录", Code: "esports:aftersales:review_list", Path: "/api/v1/esports/admin/after-sales/{id}/reviews", Method: "GET", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "申请售后复核", Code: "esports:aftersales:review_request", Path: "/api/v1/esports/admin/after-sales/{id}/reviews", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "审查售后复核", Code: "esports:aftersales:review_decide", Path: "/api/v1/esports/admin/after-sale-reviews/{id}/decision", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "撤回售后复核", Code: "esports:aftersales:review_withdraw", Path: "/api/v1/esports/admin/after-sale-reviews/{id}/withdraw", Method: "POST", Parent: "esports:aftersale:menu"},
			// 陪玩责任信用（§13.5）：挂在陪玩菜单下。
			{Type: cmfapp.PermAPI, Name: "查看陪玩信用记载", Code: "esports:players:credit_list", Path: "/api/v1/esports/admin/player-credit-events", Method: "GET", Parent: "esports:player:menu"},
			{Type: cmfapp.PermAPI, Name: "复核陪玩信用", Code: "esports:players:credit_review", Path: "/api/v1/esports/admin/player-credit-events/{id}/review", Method: "POST", Parent: "esports:player:menu"},
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
			{Type: cmfapp.PermAPI, Name: "查看结算周期", Code: "esports:settlements:cycles", Path: "/api/v1/esports/admin/settlement-cycles", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "创建结算周期", Code: "esports:settlements:cycle_create", Path: "/api/v1/esports/admin/settlement-cycles", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑结算周期", Code: "esports:settlements:cycle_update", Path: "/api/v1/esports/admin/settlement-cycles/{id}", Method: "PUT", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "启停结算周期", Code: "esports:settlements:cycle_status", Path: "/api/v1/esports/admin/settlement-cycles/{id}/status", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "冻结订单分成", Code: "esports:settlements:freeze", Path: "/api/v1/esports/admin/orders/{id}/freeze-commission", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "解冻订单分成", Code: "esports:settlements:unfreeze", Path: "/api/v1/esports/admin/orders/{id}/unfreeze-commission", Method: "POST", Parent: "esports:settlement:menu"},
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
			// 提现打款执行状态机与外部渠道退款（§6.3 / §12.6 人工核验分支）。
			{Type: cmfapp.PermAPI, Name: "提交提现打款", Code: "esports:settlements:payout_request", Path: "/api/v1/esports/admin/withdrawals/{id}/payout-request", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看提现打款记录", Code: "esports:settlements:payout_list", Path: "/api/v1/esports/admin/withdrawals/{id}/payouts", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "标记打款成功", Code: "esports:settlements:payout_paid", Path: "/api/v1/esports/admin/withdrawal-payouts/{id}/paid", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "标记打款失败", Code: "esports:settlements:payout_failed", Path: "/api/v1/esports/admin/withdrawal-payouts/{id}/failed", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "登记外部退款", Code: "esports:settlements:external_refund_register", Path: "/api/v1/esports/admin/external-refunds", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看外部退款", Code: "esports:settlements:external_refund_list", Path: "/api/v1/esports/admin/external-refunds", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "执行外部退款", Code: "esports:settlements:external_refund_execute", Path: "/api/v1/esports/admin/external-refunds/{id}/execute", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermButton, Name: "提现审核按钮", Code: "esports:settlements:withdraw_manage", Parent: "esports:settlement:menu"},

			// 充值活动模块：活动配置与按活动自动匹配赠送的充值（§4.5）。
			{Type: cmfapp.PermMenu, Name: "充值活动", Code: "esports:campaign:menu", MenuPath: "/esports/campaigns", Component: "esports/campaigns/index", Icon: "icon-gift", SortOrder: 48},
			{Type: cmfapp.PermAPI, Name: "查看充值活动", Code: "esports:campaigns:list", Path: "/api/v1/esports/admin/campaigns", Method: "GET", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermAPI, Name: "创建充值活动", Code: "esports:campaigns:create", Path: "/api/v1/esports/admin/campaigns", Method: "POST", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑充值活动", Code: "esports:campaigns:update", Path: "/api/v1/esports/admin/campaigns/{id}", Method: "PUT", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermAPI, Name: "切换活动状态", Code: "esports:campaigns:status", Path: "/api/v1/esports/admin/campaigns/{id}/status", Method: "POST", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermAPI, Name: "按活动充值", Code: "esports:campaigns:recharge", Path: "/api/v1/esports/admin/wallets/{id}/recharge-campaigns", Method: "POST", Parent: "esports:campaign:menu"},
			// 充值独立支付意图核验（§4.5）。
			{Type: cmfapp.PermAPI, Name: "核验充值到账", Code: "esports:campaigns:recharge_verify", Path: "/api/v1/esports/admin/recharge-intents/{intentNo}/verify", Method: "POST", Parent: "esports:campaign:menu"},
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
			{Type: cmfapp.PermAPI, Name: "查看待确认礼物", Code: "esports:gifts:intents", Path: "/api/v1/esports/admin/gift-intents", Method: "GET", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "发起礼物确认", Code: "esports:gifts:intent_create", Path: "/api/v1/esports/admin/gift-intents", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "送礼", Code: "esports:gifts:send", Path: "/api/v1/esports/admin/gift-orders", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "礼物退款", Code: "esports:gifts:refund", Path: "/api/v1/esports/admin/gift-orders/{id}/refund", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "复核礼物", Code: "esports:gifts:review", Path: "/api/v1/esports/admin/gift-orders/{id}/review", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "查看礼物榜单", Code: "esports:gifts:board", Path: "/api/v1/esports/admin/gift-leaderboard", Method: "GET", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermButton, Name: "礼物管理按钮", Code: "esports:gifts:manage", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "查看礼物争议", Code: "esports:gifts:disputes", Path: "/api/v1/esports/admin/gift-disputes", Method: "GET", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "代建礼物争议", Code: "esports:gifts:dispute_create", Path: "/api/v1/esports/admin/gift-disputes", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "审核礼物争议", Code: "esports:gifts:dispute_review", Path: "/api/v1/esports/admin/gift-disputes/{id}/review", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "执行礼物争议退款", Code: "esports:gifts:dispute_execute", Path: "/api/v1/esports/admin/gift-disputes/{id}/execute", Method: "POST", Parent: "esports:gift:menu"},

			// 收款管理（§4.3）：收款账户财务审核、收款方式启停、外部到账核销与到账凭证。
			{Type: cmfapp.PermMenu, Name: "收款管理", Code: "esports:payment:menu", MenuPath: "/esports/payments", Component: "esports/payments/index", Icon: "icon-safe", SortOrder: 52},
			{Type: cmfapp.PermAPI, Name: "查看收款账户", Code: "esports:payments:accounts", Path: "/api/v1/esports/admin/payment-accounts", Method: "GET", Parent: "esports:payment:menu"},
			{Type: cmfapp.PermAPI, Name: "提交收款账户", Code: "esports:payments:account_create", Path: "/api/v1/esports/admin/payment-accounts", Method: "POST", Parent: "esports:payment:menu"},
			{Type: cmfapp.PermAPI, Name: "审核收款账户", Code: "esports:payments:account_review", Path: "/api/v1/esports/admin/payment-accounts/{id}/review", Method: "POST", Parent: "esports:payment:menu"},
			{Type: cmfapp.PermAPI, Name: "停用收款账户", Code: "esports:payments:account_disable", Path: "/api/v1/esports/admin/payment-accounts/{id}/disable", Method: "POST", Parent: "esports:payment:menu"},
			{Type: cmfapp.PermAPI, Name: "查看收款方式启停", Code: "esports:payments:channels", Path: "/api/v1/esports/admin/payment-channels", Method: "GET", Parent: "esports:payment:menu"},
			{Type: cmfapp.PermAPI, Name: "启停收款方式", Code: "esports:payments:channel_status", Path: "/api/v1/esports/admin/payment-channels/{channel}/status", Method: "POST", Parent: "esports:payment:menu"},
			{Type: cmfapp.PermAPI, Name: "查看待核验收款", Code: "esports:payments:proofs", Path: "/api/v1/esports/admin/offline-payment-proofs", Method: "GET", Parent: "esports:payment:menu"},
			{Type: cmfapp.PermAPI, Name: "提交到账凭证", Code: "esports:payments:proof_submit", Path: "/api/v1/esports/admin/offline-payment-proofs", Method: "POST", Parent: "esports:payment:menu"},
			{Type: cmfapp.PermAPI, Name: "核销外部到账", Code: "esports:payments:proof_verify", Path: "/api/v1/esports/admin/offline-payment-proofs/{id}/verify", Method: "POST", Parent: "esports:payment:menu"},
			{Type: cmfapp.PermAPI, Name: "驳回到账凭证", Code: "esports:payments:proof_reject", Path: "/api/v1/esports/admin/offline-payment-proofs/{id}/reject", Method: "POST", Parent: "esports:payment:menu"},
			{Type: cmfapp.PermButton, Name: "收款管理按钮", Code: "esports:payments:manage", Parent: "esports:payment:menu"},

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
			// 报表导出与审计（§17）。
			{Type: cmfapp.PermAPI, Name: "导出报表", Code: "esports:stats:export", Path: "/api/v1/esports/admin/reports/{type}/export", Method: "GET", Parent: "esports:stats:menu"},
			{Type: cmfapp.PermAPI, Name: "查看报表导出历史", Code: "esports:stats:export_history", Path: "/api/v1/esports/admin/report-exports", Method: "GET", Parent: "esports:stats:menu"},
			{Type: cmfapp.PermAPI, Name: "按类型查看导出历史", Code: "esports:stats:export_type_history", Path: "/api/v1/esports/admin/reports/{type}/exports", Method: "GET", Parent: "esports:stats:menu"},
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
			// 风险客户（§13.7）：风险名单与人工标记 / 解除。
			{Type: cmfapp.PermMenu, Name: "风险客户", Code: "esports:risk:menu", MenuPath: "/esports/risk-customers", Component: "esports/risk-customers/index", Icon: "icon-exclamation-circle", SortOrder: 49},
			{Type: cmfapp.PermAPI, Name: "查看风险客户", Code: "esports:cs:risk_customers", Path: "/api/v1/esports/admin/risk-customers", Method: "GET", Parent: "esports:risk:menu"},
			{Type: cmfapp.PermAPI, Name: "标记客户风险", Code: "esports:cs:risk_mark", Path: "/api/v1/esports/admin/customers/{id}/risk", Method: "POST", Parent: "esports:risk:menu"},
			{Type: cmfapp.PermButton, Name: "风险客户操作按钮", Code: "esports:risk:manage", Parent: "esports:risk:menu"},
			{Type: cmfapp.PermButton, Name: "客服工作台操作按钮", Code: "esports:cs:manage", Parent: "esports:cs:menu"},

			// 储值卡模块（§4.4）：预付账户开卡、余额与流水、退卡审核。
			{Type: cmfapp.PermMenu, Name: "储值卡", Code: "esports:stored_value:menu", MenuPath: "/esports/stored-value", Component: "esports/stored-value/index", Icon: "icon-safe", SortOrder: 55},
			{Type: cmfapp.PermAPI, Name: "查看储值卡", Code: "esports:stored_value:list", Path: "/api/v1/esports/admin/stored-value-cards", Method: "GET", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "开卡", Code: "esports:stored_value:create", Path: "/api/v1/esports/admin/stored-value-cards", Method: "POST", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "核验开卡到账", Code: "esports:stored_value:payment_verification", Path: "/api/v1/esports/admin/stored-value-cards/{id}/payment-verification", Method: "POST", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermButton, Name: "核验开卡到账按钮", Code: "esports:stored_value:payment_verify", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "查看储值卡详情", Code: "esports:stored_value:detail", Path: "/api/v1/esports/admin/stored-value-cards/{id}", Method: "GET", Parent: "esports:stored_value:menu"},
			{Type: cmfapp.PermAPI, Name: "退卡试算", Code: "esports:stored_value:refund_quote", Path: "/api/v1/esports/admin/stored-value-cards/{id}/refund-quote", Method: "GET", Parent: "esports:stored_value:menu"},
			// 退卡批次维度试算（§4.5）：按预付款批次展开逐批还原明细。
			{Type: cmfapp.PermAPI, Name: "退卡批次试算", Code: "esports:stored_value:refund_batch_quote", Path: "/api/v1/esports/admin/stored-value-cards/{id}/refund-batch-quote", Method: "GET", Parent: "esports:stored_value:menu"},
			// 客户自助购卡到账核验（§4.4）。
			{Type: cmfapp.PermAPI, Name: "自助购卡核验", Code: "esports:stored_value:verify_self_purchase", Path: "/api/v1/esports/admin/stored-value-cards/{id}/verify-self-purchase", Method: "POST", Parent: "esports:stored_value:menu"},
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

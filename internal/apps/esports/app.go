// Package esports is the source feature set for the single-studio order system.
package esports

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	cmfapp "github.com/wuwuseo/cmf/app"

	"go-admin/internal/global"
	"go-admin/internal/tasks"
)

// Manual inspection can opt in via the ESPORTS_EXPERIMENTAL flag. The incomplete P0
// workflow remains unavailable by default; production rollout requires the flag plus a
// non-zero payment deadline (see paymentDeadlineMinutes).
var featureEnabled = experimentalEnabled(os.Getenv)

func experimentalEnabled(getenv func(string) string) bool {
	// 上线配置：不再强制 development 环境，生产由 ESPORTS_EXPERIMENTAL=1 显式开启即可。
	return getenv("ESPORTS_EXPERIMENTAL") == "1"
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

// autoTransferBacklogOrdersTask 客服离线 / 订单积压自动转公共池（§5.1-05）。
// 两个触发条件是**独立**的（离线超 cs_backlog_auto_transfer_minutes **或** 待派单数超
// cs_backlog_order_threshold），两键同时为 0 时整体跳过、不做任何改动。
func autoTransferBacklogOrdersTask(ctx context.Context, logger io.Writer) error {
	count, err := autoTransferBacklogOrders(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "客服离线/积压自动转公共池：%d 单\n", count)
	}
	return err
}

// scanDispatchEscalationsTask 派单三类逐级升级（§5.9-06）+ 多席缺员自动补位（§5.3-06）。
// 补位在该函数内部先执行，因此**不需要**为 §5.3-06 再注册第二个任务。
func scanDispatchEscalationsTask(ctx context.Context, logger io.Writer) error {
	count, err := runDispatchEscalations(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "派单升级与多席补位：%d\n", count)
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

// deliverOutboxTask 周期性投递业务（非通知类）外盒事件（§外盒）：认领 pending/processing 过期
// 事件并投递到本地可验证的业务事件投递器（站内消息）。
func deliverOutboxTask(ctx context.Context, logger io.Writer) error {
	count, err := deliverOutboxBatch(ctx, global.ORM, &messageEventPublisher{db: global.ORM}, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "投递业务外盒事件：%d\n", count)
	}
	return err
}

// deliverNotificationsTask 周期性投递通知类外盒事件（§通知）：认领通知事件并按通道投递到
// 本地可验证的通知投递器（站内消息），失败退避重试、达上限置 dead 计入客服失败待办。
func deliverNotificationsTask(ctx context.Context, logger io.Writer) error {
	count, err := deliverNotificationOutboxBatch(ctx, global.ORM, &messageNotificationSender{db: global.ORM}, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "投递通知外盒事件：%d\n", count)
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
		tasks.Register("esports_expire_recharge_intents", expireRechargeIntentsTask)
		tasks.Register("esports_deliver_outbox", deliverOutboxTask)
		tasks.Register("esports_deliver_notifications", deliverNotificationsTask)
		tasks.Register("esports_auto_trigger_upgrade_assessments", autoTriggerUpgradeAssessmentsTask)
		tasks.Register("esports_auto_transfer_backlog_orders", autoTransferBacklogOrdersTask)
		tasks.Register("esports_scan_dispatch_escalations", scanDispatchEscalationsTask)
		// 第 4 轮第 2 波：售后规则扫描（§13.2-05）、结单时限（§2.1-09）、档期确认过期（§3.2-04/§5.5-06）、
		// 抽查与等级复核（§7.1-03/§7.3-02）、刷评识别（§7.5-05）、复购跟进兜底（§14-01）。
		// 注册与 cron_seeds.go 的种子**必须成对**，否则一致性测试会红。
		tasks.Register("esports_scan_aftersale_rules", scanAfterSaleRulesTask)
		tasks.Register("esports_scan_close_deadline", scanCloseDeadlineTask)
		tasks.Register("esports_expire_appointment_confirmations", scanAppointmentExpiryTask)
		tasks.Register("esports_generate_spot_checks", generateSpotCheckAssessmentsTask)
		tasks.Register("esports_run_player_level_review", runPlayerLevelReviewTask)
		tasks.Register("esports_detect_review_brushing", detectReviewBrushingTask)
		tasks.Register("esports_schedule_repurchase_follow_ups", scheduleRepurchaseFollowUpsTask)
		// 会员覆盖到期回落（§10.3-03）与会员优先预约意向过期消解（§10.4）。
		// 两者均已有「读取时惰性判定」兜底（member.go settleMemberLevelOverride / cs.go csWorkbenchTodos），
		// 任务只是把回落 / 消解提前到固定时点；注册与 cron_seeds.go 的种子必须成对。
		tasks.Register("esports_expire_member_level_overrides", applyMemberOverrideExpiryTask)
		tasks.Register("esports_expire_priority_booking_intents", expirePriorityBookingIntentsTask)
		// 外部退款待执行超时督办（审核报告 §8.2-12）：只写督办事件，不自动执行、不自动取消；
		// 任务函数由 external_refund.go 提供（expirePendingExternalRefundTask），注册名与 cron 种子必须成对。
		tasks.Register("esports_expire_pending_external_refunds", expirePendingExternalRefundTask)
	}
	cmfapp.Register(cmfapp.App{
		Code: "esports", Name: "陪玩报单", Version: "0.1.0", Enabled: featureEnabled,
		// 系统配置页的业务配置分组（sys_config_group 核心字典的功能集例外，cmf v1.4.0 起允许）。
		Dicts: []cmfapp.DictItem{
			{Type: "sys_config_group", Label: "业务配置", Value: "esports", Sort: 3},
		},
		Config: []cmfapp.ConfigItem{
			{Key: "payment_deadline_minutes", Name: "订单支付时限（分钟）", Value: "30", Type: "number", Group: "esports", Remark: "待付款订单的支付时限，超时由任务过期释放名额；须为 1~1440 的正整数"},
			{Key: "duplicate_order_window_minutes", Name: "重复报单提醒时间窗（分钟）", Value: "0", Type: "number", Group: "esports", Remark: "0 关闭；窗口内同客户同 SKU 相似订单需显式确认后才可下单"},
			{Key: "customer_debt_limit_cents", Name: "客户欠款拦截阈值（分）", Value: "0", Type: "number", Group: "esports", Remark: "0 关闭；客户欠款超过该值时拒绝新报单"},
			{Key: "service_disconnect_pause_minutes", Name: "断线自动暂停阈值（分钟）", Value: "0", Type: "number", Group: "esports", Remark: "0 关闭；服务中陪玩心跳超时该时长后自动暂停服务段"},
			{Key: "reschedule_proposal_hours", Name: "改期提案确认时限（小时）", Value: "24", Type: "number", Group: "esports", Remark: "已付款订单改期提案的确认时限，且不晚于目标开始时间"},
			{Key: "recharge_payment_window_minutes", Name: "充值支付时限（分钟）", Value: "1440", Type: "number", Group: "esports", Remark: "客户自助充值支付意图的有效时长，超时由任务过期"},
			{Key: "page_allowed_domains", Name: "首页外部跳转与图片资源已审核 HTTPS 域名白名单", Value: "", Type: "string", Group: "esports", Remark: "逗号分隔的已审核 HTTPS 域名；既用于外部跳转链接（仅允许这些域名的外链），也用于发布前外部图片资源（含轮播 images）的探测白名单；为空表示不放行任何外部域名（内部跳转用站内相对路径，外部图片不探测）"},
			{Key: "finance_report_role_names", Name: "财务报表授权角色", Value: "", Type: "string", Group: "esports", Remark: "逗号分隔的角色名，命中者可查看财务资金报表；为空仅超管可看"},
			{Key: "wecom_sidebar_auth_mode", Name: "企微侧边栏接入模式", Value: "account", Type: "string", Group: "esports", Remark: "js_sdk=启用企微 JS-SDK 自动识别当前客户；account=关闭 JS-SDK，客服账号登录后手动识别"},
			{Key: "wecom_corp_id", Name: "企微企业主体 corpId", Value: "", Type: "string", Group: "esports", Remark: "企业微信 JS-SDK 签名所需 corpId；留空表示未配置，侧边栏降级为手动建档"},
			{Key: "wecom_agent_id", Name: "企微自建应用 agentId", Value: "", Type: "string", Group: "esports", Remark: "企业微信自建应用 agentId；留空表示未配置"},
			{Key: "wecom_secret", Name: "企微自建应用密钥", Value: "", Type: "string", Group: "esports", Remark: "企业微信自建应用 secret，用于换取 access_token / jsapi_ticket；留空表示未配置"},
			{Key: "wecom_jsapi_ticket", Name: "企微 JS-SDK 票据", Value: "", Type: "string", Group: "esports", Remark: "企微 JS-SDK jsapi_ticket；可手动预置或由后台自动拉取；留空表示未配置"},
			{Key: "wecom_sidebar_footer_tip", Name: "企微侧边栏底部提示", Value: "", Type: "string", Group: "esports", Remark: "企微侧边栏 H5「底部提示」区块文案（§8.4-11）；留空表示不展示该区块"},
			{Key: "page_icon_allowlist", Name: "页面图标短标识允许清单", Value: "icon-apps,icon-archive,icon-bar-chart,icon-check-circle,icon-cs,icon-customer-service,icon-desktop,icon-edit,icon-exclamation-circle,icon-file,icon-gift,icon-heart,icon-list,icon-safe,icon-share-alt,icon-star,icon-user,icon-wallet", Type: "string", Group: "esports", Remark: "页面装修图标短标识白名单（§16.3-04），逗号分隔；在代码基准清单上做**加法**（可扩展、不可收窄）；https 图片地址与站内相对路径不受此限"},
			{Key: "notification_channels", Name: "通知通道开关", Value: "", Type: "string", Group: "esports", Remark: "逗号分隔启用的通知通道（wecom_app/voice/push），如 \"wecom_app,push\"；为空表示不启用任何通道，通知事件将进入失败待办"},
			{Key: "notification_push_webhook", Name: "通知推送网关", Value: "", Type: "string", Group: "esports", Remark: "JSON：{\"url\":\"https://...\",\"secret\":\"...\",\"timeout_seconds\":5}；配置后按收件人推送并带 X-Esports-Signature 签名；留空表示不推送（站内信仍投递）"},
			{Key: "payment_gateways", Name: "在线收款渠道配置", Value: "", Type: "string", Group: "esports", Remark: "JSON（按渠道编码索引）：{\"wechat_work\":{\"kind\":\"http_hmac\",\"base_url\":\"https://...\",\"app_id\":\"...\",\"app_secret\":\"...\",\"notify_url\":\"...\",\"enabled\":true}}；未配置的渠道发起收款返回 503"},
			{Key: "wallet_display", Name: "钱包展示配置", Value: "", Type: "string", Group: "esports", Remark: "JSON（按账户类型索引）：{\"customer_balance\":{\"display_name\":\"账户余额\",\"unit_name\":\"元\",\"unit_cents\":100},\"player_catfood\":{\"display_name\":\"猫粮\",\"unit_name\":\"猫粮\",\"unit_cents\":100}}；留空用内置默认"},
			// —— 商品编码自动生成（新建商品时按「前缀-全局流水」生成，唯一索引兜底）——
			{Key: "product_code_prefix", Name: "商品编码前缀", Value: "SKU", Type: "string", Group: "esports", Remark: "新建商品自动生成编码的前缀，最终形如 SKU-000001；留空回落 SKU"},
			{Key: "product_code_serial_width", Name: "商品编码流水位数", Value: "6", Type: "number", Group: "esports", Remark: "流水号补零位数，1~12；≤0 或非法回落 6"},
			// —— 技能等级（§5.5 / §7.3）已改为专用表 EsportsSkillLevel 驱动 ——
			// 在「技能等级」页（运营管理 → 会员等级下方）编辑；启动迁移把旧的
			// app.esports.skill_levels 系统配置一次性搬入该表（见 MigrateSkillLevelsFromConfig）。
			// —— §13.5 售后四段时限（小时，0 = 该阶段不限时；超时只写升级外盒，不自动流转状态）——
			{Key: "aftersale_accept_hours", Name: "售后受理时限（小时）", Value: "2", Type: "number", Group: "esports", Remark: "0 表示该阶段不限时；非法值（<0 或 >2160）回落 2"},
			{Key: "aftersale_plan_hours", Name: "售后方案时限（小时）", Value: "24", Type: "number", Group: "esports", Remark: "0 不限时；非法值回落 24"},
			{Key: "aftersale_customer_confirm_hours", Name: "售后客户确认时限（小时）", Value: "48", Type: "number", Group: "esports", Remark: "0 不限时；非法值回落 48"},
			{Key: "aftersale_execute_hours", Name: "售后执行时限（小时）", Value: "24", Type: "number", Group: "esports", Remark: "0 不限时；非法值回落 24"},
			// —— 订单取消、档期与预付款扣减 ——
			{Key: "appointment_confirm_hours", Name: "档期确认时限（小时）", Value: "24", Type: "number", Group: "esports", Remark: "预约单确认档期的时限，合法 1~720；为 0 或非法均回落 24（无「0 关闭」语义）"},
			{Key: "cancel_free_hours", Name: "取消免费窗口（小时）", Value: "0", Type: "number", Group: "esports", Remark: "0 表示无免费窗口（任何取消都按手续费计）；非法值（<0 或 >720）直接拒绝取消操作，不回落"},
			{Key: "cancel_fee_bp", Name: "取消手续费（基点）", Value: "0", Type: "number", Group: "esports", Remark: "万分比；0 表示不收取消费；非法值（<0 或 >10000）直接拒绝取消操作，不回落"},
			{Key: "customer_aftersale_window_days", Name: "客户自助售后申请时限（天）", Value: "30", Type: "number", Group: "esports", Remark: "0 表示不限时；非法值（<0 或 >365）直接报错，不回落；不影响客服代建售后单"},
			{Key: "prepay_deduction_order", Name: "预付款扣减顺序", Value: "bonus_first", Type: "string", Group: "esports", Remark: "bonus_first=先赠送后本金（默认）/ principal_first=先本金后赠送 / proportional=按剩余比例分摊；留空或非法回落 bonus_first"},
			// —— 排单与派单 ——
			{Key: "dispatch_invite_ttl_minutes", Name: "派单邀请有效期（分钟）", Value: "10", Type: "number", Group: "esports", Remark: "邀请到期未回应即回收改派；合法 1~1440，为 0 或非法回落 10（无「0 关闭」语义）"},
			{Key: "dispatch_priority_quota", Name: "优先派单名额上限（人）", Value: "3", Type: "number", Group: "esports", Remark: "每个游戏每期的优先名单名额；<1 或非法回落 3；候选不足时宁缺毋滥，不填满名额"},
			{Key: "dispatch_priority_auto", Name: "优先权达标制阈值", Value: "{\"rating_min\":4.0,\"completed_min\":1}", Type: "string", Group: "esports", Remark: "JSON：{\"rating_min\":4.0,\"completed_min\":1}；rating_min 须 0<x≤5，completed_min≥0（0 表示不卡完成单数）；整体或单字段非法回落默认"},
			{Key: "dispatch_priority_notice_minutes", Name: "优先通知独占时长（分钟）", Value: "3", Type: "number", Group: "esports", Remark: "名单内陪玩的独占抢单窗口；合法 1~1440，为 0 或非法回落 3；超时释放到抢单大厅"},
			{Key: "dispatch_ranking_weights", Name: "派单排序权重", Value: "{\"rating\":40,\"history\":30,\"preference\":60,\"zone\":30,\"balance\":20,\"history_cap\":10,\"balance_cap\":10,\"distance\":0,\"timezone\":0}", Type: "string", Group: "esports", Remark: "JSON 权重，某项为 0 表示该分量不参与排序；history_cap / balance_cap 须 ≥1；zone 为历史字段，优先派单权不参与排序（§5.2-04），该值已不影响结果；distance / timezone 为 §5.5-03 新增的加分项，**默认 0 = 不参与排序，以保持既有排序行为不变**"},
			{Key: "remind_ladder_minutes", Name: "催单时间阶梯（分钟）", Value: "5,15,30", Type: "string", Group: "esports", Remark: "逗号分隔的升序档位（每档 1~1440），如 5,15,30；非法档位被跳过；留空或全非法回落 5,15,30"},
			{Key: "service_overtime_minutes", Name: "服务超时提醒阈值（分钟）", Value: "0", Type: "number", Group: "esports", Remark: "0 关闭提醒；服务段有效时长超过该值即提醒责任客服确认是否加钟；>1440 或非法回落 0（关闭）"},
			// —— 陪玩在线、服务时长与考核评价 ——
			{Key: "player_heartbeat_timeout_minutes", Name: "陪玩心跳超时（分钟）", Value: "10", Type: "number", Group: "esports", Remark: "心跳超时后由任务判定在线状态；合法 1~1440，<1 或非法回落 10（无「0 关闭」语义）"},
			{Key: "service_duration_mismatch_minutes", Name: "服务时长差异提醒阈值（分钟）", Value: "15", Type: "number", Group: "esports", Remark: "<1 或非法回落 15；实际阈值取 max(该值, 上报时长×20%)；超阈值提醒客服核实"},
			{Key: "service_duration_severe_minutes", Name: "严重时长差异阈值（分钟）", Value: "30", Type: "number", Group: "esports", Remark: "<1 或非法回落 30；超阈值在同一事务内自动建售后单（channel=system）"},
			{Key: "assessment_pass_score", Name: "考核通过分数线", Value: "60", Type: "number", Group: "esports", Remark: "百分制；≤0 或非法回落 60，不能用 0 表达关闭"},
			{Key: "review_min_samples", Name: "评分样本充足下限（条）", Value: "5", Type: "number", Group: "esports", Remark: "低于该样本数标记样本不足；≤0 或非法回落 5（无「0 关闭」语义）"},
			{Key: "review_rule_version", Name: "评价规则版本号", Value: "v1", Type: "string", Group: "esports", Remark: "如 v1 / v2；留空或全空白回落 v1；用于评价计算口径留痕"},
			// —— 升级考核申请门槛（§7.1-02 / §7.2-01）——
			{Key: "upgrade_apply_min_service_units", Name: "升级申请·最低累计服务量", Value: "30", Type: "number", Group: "esports", Remark: "已完成服务段的 reported_units 求和（进行中不计）；≤0 或非法回落 30（无「0 关闭」语义）"},
			{Key: "upgrade_apply_min_rating_score", Name: "升级申请·最低综合评分", Value: "80", Type: "number", Group: "esports", Remark: "百分制，1~5 星按每星 20 分折算；评价样本不足时不按低分处理；≤0 或非法回落 80"},
			{Key: "upgrade_apply_min_tenure_days", Name: "升级申请·最低在岗天数", Value: "7", Type: "number", Group: "esports", Remark: "自陪玩档案创建时间起算；≤0 或非法回落 7（无「0 关闭」语义）"},
			{Key: "upgrade_apply_cooldown_days", Name: "升级申请冷却期（天）", Value: "30", Type: "number", Group: "esports", Remark: "距上次升级考核单创建时间；冷却期内重复申请被拒；≤0 或非法回落 30"},
			// —— 礼物与风控 ——
			{Key: "gift_high_value_threshold_cents", Name: "大额礼物确认阈值（分）", Value: "100000", Type: "number", Group: "esports", Remark: "≤0 或非法回落 100000（1000 元）；达到阈值须显式 confirmed=true 才可送出"},
			{Key: "risk_window_days", Name: "风控退款观察窗口（天）", Value: "30", Type: "number", Group: "esports", Remark: "<0 或非法回落 30；为 0 时窗口即「现在」，历史退款不再计入"},
			{Key: "risk_refund_count", Name: "风控退款次数阈值（次）", Value: "3", Type: "number", Group: "esports", Remark: "0 表示不按次数判；与 risk_refund_cents 同时 ≤0 时风控任务整体跳过"},
			{Key: "risk_refund_cents", Name: "风控退款金额阈值（分）", Value: "50000", Type: "number", Group: "esports", Remark: "0 表示不按金额判（与非法值回落 50000 语义不同）；与 risk_refund_count 同时 ≤0 时任务跳过"},
			// —— 会员等级 ——
			{Key: "member_downgrade_guard_days", Name: "会员降级保护期（天）", Value: "30", Type: "number", Group: "esports", Remark: "<1 或 >3650 或非法回落 30；保护期届满由任务执行降级并留事件"},
			// —— 结算周期与提现 ——
			{Key: "settlement_period", Name: "结算周期", Value: "weekly", Type: "string", Group: "esports", Remark: "daily / weekly / monthly；被结算周期表（陪玩 / 等级 / 游戏 / 全局）命中时不生效；留空或非法回落 weekly"},
			{Key: "withdrawal_min_cents", Name: "最低提现金额（分）", Value: "0", Type: "number", Group: "esports", Remark: "0 不限门槛；非法值或 >1 亿分回落 0"},
			{Key: "withdrawal_min_interval_hours", Name: "提现最小间隔（小时）", Value: "0", Type: "number", Group: "esports", Remark: "0 不限间隔；非法值或 >720 回落 0；间隔按最近一次申请（含已驳回）计算"},
			{Key: "withdrawal_daily_limit", Name: "每日提现次数上限（次）", Value: "0", Type: "number", Group: "esports", Remark: "0 不限次数；非法值或 >100 回落 0；按 UTC+8 自然日统计，含已驳回申请"},
			{Key: "withdrawal_fee_bp", Name: "提现手续费（基点）", Value: "0", Type: "number", Group: "esports", Remark: "万分比；0 不收费；仅在未配置合法阶梯时生效；非法值或 >10000 回落 0"},
			{Key: "withdrawal_tax_bp", Name: "提现代扣税费（基点）", Value: "0", Type: "number", Group: "esports", Remark: "万分比；0 不收税；仅在未配置合法阶梯时生效；非法值或 >10000 回落 0"},
			{Key: "withdrawal_fee_tiers", Name: "提现手续费阶梯", Value: "", Type: "string", Group: "esports", Remark: "JSON 数组，如 [{\"min_cents\":0,\"bp\":100},{\"min_cents\":50000,\"bp\":50}]，取「金额≥min_cents」的最高档；留空或任一项非法视为未配置，回落 withdrawal_fee_bp"},
			{Key: "withdrawal_tax_tiers", Name: "提现代扣税费阶梯", Value: "", Type: "string", Group: "esports", Remark: "JSON 数组，格式同 withdrawal_fee_tiers；留空或任一项非法视为未配置，回落 withdrawal_tax_bp"},
			// —— 账本核验游标（内部运行时状态：程序自写自读，运维改成非 0 会永久跳过低位 ID 的核验）——
			{Key: "ledger_reconcile_cursor_wallet_account", Name: "账本核验游标（钱包账户）", Value: "0", Type: "number", Group: "esports", Remark: "内部状态：已扫过的最大账户 ID，缺失读作 0（从头扫）；由 esports_reconcile_ledgers 任务写回，运维不要手改"},
			{Key: "ledger_reconcile_cursor_allocation", Name: "账本核验游标（分摊明细）", Value: "0", Type: "number", Group: "esports", Remark: "内部状态：已扫过的最大分摊明细 ID（单轮上限 5000 条，截断时写回续扫）；由账本核验任务写回，运维不要手改"},
			{Key: "upgrade_auto_trigger_cursor", Name: "升级考核自动触发游标", Value: "0", Type: "number", Group: "esports", Remark: "内部状态：已扫过的最大陪玩 ID；0/缺失/非法/负数均读作 0（从头扫）；由 esports_auto_trigger_upgrade_assessments 写回，运维不要手改"},
			// —— 第 4 轮补充声明：以下 18 键由 TestEsportsConfigKeysAreDeclared 实测出「已读未声明」，
			//    默认值一律取自**读取点的真实代码回落**（不是按键名猜），声明前已逐键 grep 复核。 ——
			// 售后 / 风控
			{Key: "aftersale_refund_review_threshold_cents", Name: "退款财务复核门槛（分）", Value: "0", Type: "number", Group: "esports", Remark: "0/负数/非法/未配置=不启用金额分流（照常放行）；>0 且退款额达门槛须超管或配置的财务角色复核，否则 403"},
			{Key: "aftersale_refund_finance_role_names", Name: "大额退款财务复核角色", Value: "", Type: "string", Group: "esports", Remark: "逗号分隔角色名；留空=未配置，此时大额退款仅超管可复核（明确降级，不静默放行）；只配本键不配门槛键则不生效"},
			{Key: "external_refund_require_aftersale", Name: "外部退款强制关联售后单", Value: "0", Type: "number", Group: "esports", Remark: "仅整数 1 启用；未配置或其他值（含 true）一律读作关闭——此时仍可登记，但强制写 payout.offbook_suspected 私下赔付留痕"},
			{Key: "risk_restrict_refund_count", Name: "风控限制档次数阈值（次）", Value: "6", Type: "number", Group: "esports", Remark: "缺失/负数/非法回落 6；显式 0=不按次数判；与金额键同时 ≤0 时限制档关闭（仅落 watch，不拦自助下单）"},
			{Key: "risk_restrict_refund_cents", Name: "风控限制档金额阈值（分）", Value: "200000", Type: "number", Group: "esports", Remark: "缺失/负数/非法回落 200000（2000 元）；显式 0=不按金额判该档；与次数键同时 ≤0 时自动限制整体关闭"},
			// 订单报单校验
			{Key: "price_deviation_threshold_bp", Name: "订单价偏离提醒阈值（基点）", Value: "3000", Type: "number", Group: "esports", Remark: "基点，3000=30%；缺失/<0/>10000/非法回落 3000；显式 0=关闭偏离判定；偏离度超阈值才提醒"},
			{Key: "price_deviation_reason_required", Name: "偏离强制填写原因", Value: "true", Type: "string", Group: "esports", Remark: "true/1/yes/on=强制（缺失或非法也回落强制）；仅显式 false/0/no/off 关停；仅在超偏离阈值时生效"},
			{Key: "order_discount_limit_bps", Name: "单笔会员折扣上限（基点）", Value: "0", Type: "number", Group: "esports", Remark: "0/≤0/>10000/非法/未配置=不限制；>0 时折扣基点超上限即拒绝下单"},
			{Key: "customer_debt_remind_cents", Name: "客户欠款提醒阈值（分）", Value: "0", Type: "number", Group: "esports", Remark: "0=只要有欠款就提醒（与 customer_debt_limit_cents 的 0=关闭拦截语义相反）；负数回落 0；只提醒不拦截"},
			{Key: "close_form_require_service_result", Name: "结单强制服务结果", Value: "false", Type: "string", Group: "esports", Remark: "默认 false：内置结单模板 builtin_default 恒出现在模板列表，但只有置 true 才启用并强制采集「服务结果」——默认关闭是为不改存量结单口径"},
			// 收款账户
			{Key: "corporate_transfer_threshold_cents", Name: "大额对公转账门槛（分）", Value: "0", Type: "number", Group: "esports", Remark: "0/负数/非法/未配置=不限制渠道；>0 时金额≥门槛且渠道≠corporate_transfer 即拒绝"},
			{Key: "payment_subject_whitelist", Name: "收款主体白名单", Value: "", Type: "string", Group: "esports", Remark: "逗号分隔主体名；留空或全空白=未配置（提交与启用收款账户一律拒绝，须先登记白名单，避免任意主体名经财务审核启用）；非空时仅名单内主体名通过校验"},
			// 客服离线与积压自动转池（对应定时任务尚未注册，属预留）
			{Key: "cs_offline_timeout_minutes", Name: "客服离线判定时长（分钟）", Value: "15", Type: "number", Group: "esports", Remark: "无「0 关闭」语义：0/<1/>1440/非法/未配置一律回落 15；口径=最近活跃时间 + 该时长；看板在线状态与自动转池共用此口径"},
			{Key: "cs_backlog_auto_transfer_minutes", Name: "积压自动转池时长（分钟）", Value: "0", Type: "number", Group: "esports", Remark: "0=关闭「滞留超时」这个触发条件；与单量阈值同时为 0 时任务整体跳过；由 esports_auto_transfer_backlog_orders 执行"},
			{Key: "cs_backlog_order_threshold", Name: "积压自动转池单量阈值（单）", Value: "0", Type: "number", Group: "esports", Remark: "0=不按单量判；>0 时该客服待派单数**严格大于**阈值即转池——与「离线超时」是两个**独立**触发器，满足任一即转（不要求同时离线）；由 esports_auto_transfer_backlog_orders 执行"},
			// 派单升级
			{Key: "dispatch_no_taker_minutes", Name: "无人接单升级阈值（分钟）", Value: "60", Type: "number", Group: "esports", Remark: "无「0 关闭」语义：0/<1/>1440/非法/未配置一律回落 60"},
			{Key: "dispatch_start_escalate_minutes", Name: "临近开始升级提前量（分钟）", Value: "60", Type: "number", Group: "esports", Remark: "无「0 关闭」语义：0/<1/>1440/非法/未配置一律回落 60"},
			{Key: "dispatch_refill_minutes", Name: "多席缺员自动补位阈值（分钟）", Value: "10", Type: "number", Group: "esports", Remark: "多席订单出现空缺席位后，超过该时长仍未补齐即触发自动补位；<1/>1440/非法回落 10（无「0 关闭」语义）"},
			// 会员
			{Key: "member_override_guard_days", Name: "手动调级覆盖期（天）", Value: "90", Type: "number", Group: "esports", Remark: "无「0 关闭」语义：0/非法（<1 或 >3650）/未配置一律回落 90；覆盖期内手动等级作为保底来源"},
			// 提现渠道开关（真实键带渠道后缀；裸前缀 withdrawal_channel 已在 config_declaration_test.go 豁免正则误报）
			// 未配置/空/非 disabled 一律视为**开通**（与代码回落一致，保持既有行为）；要停用某渠道必须显式写 disabled。
			{Key: "withdrawal_channel.bank_card", Name: "提现方式·银行卡", Value: "enabled", Type: "string", Group: "esports", Remark: "enabled 或空=开通；仅显式 disabled 关闭；未开通渠道发起提现申请被拒（中文提示）"},
			{Key: "withdrawal_channel.wechat", Name: "提现方式·微信", Value: "enabled", Type: "string", Group: "esports", Remark: "enabled 或空=开通；仅显式 disabled 关闭；未开通渠道发起提现申请被拒（中文提示）"},
			{Key: "withdrawal_channel.alipay", Name: "提现方式·支付宝", Value: "enabled", Type: "string", Group: "esports", Remark: "enabled 或空=开通；仅显式 disabled 关闭；未开通渠道发起提现申请被拒（中文提示）"},
			// —— 第 4 轮第 2 波补充声明：以下 16 键由守卫测试实测出「已读未声明」，
			//    默认值一律取自**读取点的真实代码回落**（已逐键 grep 复核），不按键名猜。 ——
			{Key: "aftersale_rule_auto_create_enabled", Name: "售后规则自动建单总开关", Value: "1", Type: "string", Group: "esports", Remark: "默认开启；**仅当值恰为字符串 \"0\" 才关闭**（只留痕+待办、不建单）。\"false\"/\"off\"/空值都会被读成开启，别按 bool 直觉填"},
			{Key: "appointment_expire_action", Name: "档期过期处置方式", Value: "both", Type: "string", Group: "esports", Remark: "refund=超时单只允许全额退还 / reschedule=只允许改期 / both=两者都允许（默认）；未配置或非枚举值一律回落 both，无「关闭」语义"},
			{Key: "close_deadline_hours", Name: "结单时限（小时）", Value: "0", Type: "number", Group: "esports", Remark: "0=关闭该超时待办（缺失/空/非法/<1/>1440 也一律回落 0）；>0 时「待结单 / 待结单复核」超该小时数即进客服与管理员待办"},
			{Key: "gift_payment_window_minutes", Name: "礼物收款时限（分钟）", Value: "1440", Type: "number", Group: "esports", Remark: "1440=24 小时（缺失/空/非法/0/负数/>10080 一律回落 1440）；**0 不是关闭而是非法**，无法用 0 关闭礼物收款"},
			{Key: "spot_check_cycle_days", Name: "随机抽查最小间隔（天）", Value: "7", Type: "number", Group: "esports", Remark: "≥1；缺失/空/非法/≤0 一律回落 7（**无「0 关闭」语义**）；口径=同一陪玩两次抽查之间的最小间隔"},
			{Key: "spot_check_ratio_bp", Name: "随机抽查抽取比例（万分比）", Value: "1000", Type: "number", Group: "esports", Remark: "1000=10%（缺失/空/非法/0/负数/>10000 一律回落 1000）；10000=全抽；**0 无法关闭抽查**（会被当非法回落 10%）；逐人独立按概率判定，非精确配额"},
			{Key: "level_review_cycle_days", Name: "等级复核周期（天）", Value: "30", Type: "number", Group: "esports", Remark: "≥1；缺失/空/非法/≤0 一律回落 30（**无「0 关闭」语义**）；按判定时刻向前切窗"},
			{Key: "level_review_fail_periods", Name: "等级复核连续不达标期数", Value: "3", Type: "number", Group: "esports", Remark: "≥1；缺失/空/非法/≤0 一律回落 3（无「0 关闭」语义）；连续不达标期数达到该值才降一档"},
			{Key: "review_appeal_window_days", Name: "评价申诉时限（天）", Value: "30", Type: "number", Group: "esports", Remark: "缺失/空/非数字回落 30；显式 0=不限时；**注意负数也被按 0 处理（=不限时）**，与「非法一律回落 30」并不一致"},
			{Key: "review_brush_rule", Name: "异常刷评识别规则", Value: "", Type: "string", Group: "esports", Remark: "JSON：{\"window_hours\":24,\"max_reviews_per_customer\":3,\"identical_comment\":true}；留空=**关闭**自动识别（显式降级）；JSON 非法则明确报错（不静默关闭）；window_hours / max_reviews 非正回落 24 / 3"},
			{Key: "sku_cost_view_roles", Name: "SKU 成本可见角色", Value: "", Type: "string", Group: "esports", Remark: "逗号分隔角色名；留空=不额外授权（**仅超管可看成本，超管恒放行**）；非空时命中任一活跃角色即可读；未授权角色成本既不可读也不可写"},
			// 密钥由 apps.Provision 为每个安装实例随机生成，不在源码中存固定值。
			{Key: "page_preview_secret", Name: "页面预览签名密钥", Value: "", Type: "string", Group: "esports", Remark: "**全局单份 HMAC-SHA256 密钥**（不是每设备一份）；首次播种时随机生成 32 字节密钥，启动时也会补齐历史空值；运行中留空则预览码功能不可用，生成接口明确失败「页面预览签名密钥未配置」；换密钥会让已发出的预览码立刻失效（预览码 TTL 30 分钟）"},
			{Key: "player_expected_price_limits_cents", Name: "期望报价允许区间（分）", Value: "0,0", Type: "string", Group: "esports", Remark: "格式 \"min,max\"（两个非负整数分，逗号分隔）；\"0,0\"=两侧都不限（也是缺失/空/字段数≠2/非数字/负数/min>max 的回落）；**键名虽含 _cents 但实际是字符串，后台数字框写不进去**"},
			{Key: "repurchase_follow_up_days", Name: "结单后复购跟进天数", Value: "7", Type: "number", Group: "esports", Remark: "缺失/空/非数字/负数回落 7；**显式 0 = 关闭**（不生成复购回访待办）；>0 时结单后该天数生成客户维度复购跟进待办"},
		},
		Routes: []cmfapp.Route{
			{Method: "GET", Path: "/products", Auth: cmfapp.AuthPublic, Handler: publicSkus},
			// 技能等级（§5.5）：公开读供管理端表单与 H5 文案；配置本体在「技能等级」页编辑（下方 admin 路由）。
			{Method: "GET", Path: "/skill-levels", Auth: cmfapp.AuthPublic, Handler: publicSkillLevels},
			// 在线支付渠道验签回调（§4.3）：原始 body + 签名头验签后核销；
			// 渠道编码优先取路径参数，兼容仅带 X-Pay-Channel 头的旧回调地址。
			{Method: "POST", Path: "/payment-notify", Auth: cmfapp.AuthPublic, Handler: paymentGatewayNotifyHandler},
			{Method: "POST", Path: "/payment-notify/:channel", Auth: cmfapp.AuthPublic, Handler: paymentGatewayNotifyHandler},
			// 客户端交易入口（§9 / §6 / §4.5）：礼物目录、陪玩推荐、已生效充值赠送活动，均为顶层公开路径。
			{Method: "GET", Path: "/gifts", Auth: cmfapp.AuthPublic, Handler: publicGifts},
			{Method: "GET", Path: "/players", Auth: cmfapp.AuthPublic, Handler: publicPlayers},
			{Method: "GET", Path: "/campaigns", Auth: cmfapp.AuthPublic, Handler: publicCampaigns},
			{Method: "GET", Path: "/user/orders/:id/quote", Auth: cmfapp.AuthUser, Handler: userOrderQuote},
			{Method: "POST", Path: "/user/orders/:id/confirm", Auth: cmfapp.AuthUser, Handler: userConfirmQuote},
			{Method: "GET", Path: "/admin/products", Auth: cmfapp.AuthAdmin, Handler: adminSkus},
			{Method: "POST", Path: "/admin/products", Auth: cmfapp.AuthAdmin, Handler: createSku},
			{Method: "PUT", Path: "/admin/products/:id", Auth: cmfapp.AuthAdmin, Handler: updateSkuHandler},
			// SKU 停服（§15.7）：申请 → 异人审批 → 受影响订单逐单处置；既有直接上下架接口不变。
			{Method: "POST", Path: "/admin/products/:id/suspensions", Auth: cmfapp.AuthAdmin, Handler: requestSkuSuspensionHandler},
			{Method: "GET", Path: "/admin/product-suspensions", Auth: cmfapp.AuthAdmin, Handler: adminSkuSuspensions},
			{Method: "GET", Path: "/admin/product-suspensions/:id", Auth: cmfapp.AuthAdmin, Handler: skuSuspensionDetailHandler},
			{Method: "POST", Path: "/admin/product-suspensions/:id/decision", Auth: cmfapp.AuthAdmin, Handler: reviewSkuSuspensionHandler},
			{Method: "POST", Path: "/admin/product-suspensions/:id/cancel", Auth: cmfapp.AuthAdmin, Handler: cancelSkuSuspensionHandler},
			{Method: "POST", Path: "/admin/product-suspensions/:id/orders/:orderId/disposal", Auth: cmfapp.AuthAdmin, Handler: disposeSkuSuspensionItemHandler},
			{Method: "GET", Path: "/admin/categories", Auth: cmfapp.AuthAdmin, Handler: adminCategories},
			{Method: "POST", Path: "/admin/categories", Auth: cmfapp.AuthAdmin, Handler: createCategory},
			{Method: "PUT", Path: "/admin/categories/:id", Auth: cmfapp.AuthAdmin, Handler: updateCategoryHandler},
			{Method: "DELETE", Path: "/admin/categories/:id", Auth: cmfapp.AuthAdmin, Handler: deleteCategoryHandler},
			// 商品父实体 / 规格组 / 逐席角色（§15.1 / §15.5）：商品挂多 SKU，规格组约束多人规格与每席角色。
			{Method: "GET", Path: "/admin/customers", Auth: cmfapp.AuthAdmin, Handler: adminCustomers},
			{Method: "POST", Path: "/admin/customers", Auth: cmfapp.AuthAdmin, Handler: createCustomer},
			{Method: "POST", Path: "/admin/customers/:id/age-checks", Auth: cmfapp.AuthAdmin, Handler: createAgeCheck},
			{Method: "GET", Path: "/admin/age-checks", Auth: cmfapp.AuthAdmin, Handler: pendingAgeChecks},
			{Method: "POST", Path: "/admin/age-checks/:id/review", Auth: cmfapp.AuthAdmin, Handler: reviewAgeCheckRequest},
			{Method: "GET", Path: "/admin/orders", Auth: cmfapp.AuthAdmin, Handler: adminOrders},
			{Method: "GET", Path: "/admin/orders/:id", Auth: cmfapp.AuthAdmin, Handler: adminOrder},
			// 授权协作订单（§8 客服看「负责或获授权协作」的订单）：协作只放开只读可见性，
			// 不转移负责关系；授予 / 撤销仅限超管或该订单负责人。
			{Method: "GET", Path: "/admin/orders/:id/collaborators", Auth: cmfapp.AuthAdmin, Handler: listOrderCollaboratorsHandler},
			{Method: "POST", Path: "/admin/orders/:id/collaborators", Auth: cmfapp.AuthAdmin, Handler: addOrderCollaboratorHandler},
			{Method: "DELETE", Path: "/admin/orders/:id/collaborators/:adminId", Auth: cmfapp.AuthAdmin, Handler: removeOrderCollaboratorHandler},
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
			{Method: "GET", Path: "/admin/orders/:id/cancel-preview", Auth: cmfapp.AuthAdmin, Handler: readableOrderHandler(previewCancelOrderHandler)},
			// 限时改期提案（§3.3）：已付款订单改期走提案，客户与已接单成员逐人确认后原子生效。
			{Method: "POST", Path: "/admin/orders/:id/reschedule-proposals", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(proposeRescheduleHandler)},
			{Method: "GET", Path: "/admin/orders/:id/reschedule-proposals", Auth: cmfapp.AuthAdmin, Handler: readableOrderHandler(listOrderRescheduleProposalsHandler)},
			{Method: "POST", Path: "/admin/reschedule-proposals/:id/withdraw", Auth: cmfapp.AuthAdmin, Handler: ownedRescheduleProposalHandler(withdrawRescheduleProposalHandler)},
			{Method: "PUT", Path: "/admin/orders/:id/appointment", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(rescheduleOrderHandler)},
			// 预约档期确认（§3.2 / §5.5）：request → confirmed，含资源核验；未确认档期不允许派单。
			{Method: "POST", Path: "/admin/orders/:id/appointment/confirm", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(confirmOrderAppointmentHandler)},
			// 客服代陪玩暂停 / 恢复服务段（§3.3）：理由必填，事件以管理员身份留痕。
			{Method: "POST", Path: "/admin/orders/:id/players/:playerId/service/pause", Auth: cmfapp.AuthAdmin, Handler: adminPausePlayerServiceHandler},
			{Method: "POST", Path: "/admin/orders/:id/players/:playerId/service/resume", Auth: cmfapp.AuthAdmin, Handler: adminResumePlayerServiceHandler},
			// 服务段时长争议更正（§3.3）：留痕更正有效 / 暂停秒数并登记停工量与责任方。
			{Method: "POST", Path: "/admin/orders/:id/players/:playerId/service/timing-correction", Auth: cmfapp.AuthAdmin, Handler: adminCorrectServiceTimingHandler},
			// 终止服务段（§3.3-06）：客服侧提前结束某陪玩的服务段，登记已履约量 / 未履约量 / 责任方。
			{Method: "POST", Path: "/admin/orders/:id/players/:playerId/service/terminate", Auth: cmfapp.AuthAdmin, Handler: adminTerminatePlayerServiceHandler},
			{Method: "PUT", Path: "/admin/orders/:id/channel-cost", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(setOrderChannelCostHandler)},
			// 单笔手动指定分成规则（§12.3 最高一级）：仅未锁定成员规则前可设，rule_id=0 清除。
			{Method: "PUT", Path: "/admin/orders/:id/commission-rule", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(setOrderManualCommissionRuleHandler)},
			// 存单：把已付款订单标记为冻结，「恢复」清除标记（原地保留、不动履约状态与名额）。
			// 收款账户与收款方式启停（§4.3）：账户须为工作室主体并经财务审核，客服不能直接启用。
			// 上线自检（§18）：逐项回报功能开关 / 支付时限 / 收款通道 / 推送 / 企微是否就绪，只读且不回显密钥。
			{Method: "GET", Path: "/admin/readiness", Auth: cmfapp.AuthAdmin, Handler: adminReadinessHandler},
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
			{Method: "GET", Path: "/admin/orders/:id/close-conditions", Auth: cmfapp.AuthAdmin, Handler: readableOrderHandler(orderCloseConditions)},
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
			// 技能等级管理（§5.5 / §7.3）：配置表 EsportsSkillLevel 的管理端读写（整体保存）。
			{Method: "GET", Path: "/admin/skill-levels", Auth: cmfapp.AuthAdmin, Handler: adminSkillLevels},
			{Method: "PUT", Path: "/admin/skill-levels", Auth: cmfapp.AuthAdmin, Handler: updateSkillLevelsHandler},
			{Method: "GET", Path: "/admin/member-levels", Auth: cmfapp.AuthAdmin, Handler: adminMemberLevels},
			{Method: "POST", Path: "/admin/member-levels", Auth: cmfapp.AuthAdmin, Handler: createMemberLevelHandler},
			{Method: "PUT", Path: "/admin/member-levels/:id", Auth: cmfapp.AuthAdmin, Handler: updateMemberLevelHandler},
			{Method: "POST", Path: "/admin/member-levels/:id/status", Auth: cmfapp.AuthAdmin, Handler: setMemberLevelStatusHandler},
			// 删除会员等级（审核报告 §8.1-8）：仅「已停用且未被任何客户引用」的等级可删，
			// 启用中 / 仍被引用一律 409 中文提示，引导改用停用（可逆口径）。
			{Method: "DELETE", Path: "/admin/member-levels/:id", Auth: cmfapp.AuthAdmin, Handler: deleteMemberLevelHandler},
			{Method: "GET", Path: "/admin/customers/:id/member", Auth: cmfapp.AuthAdmin, Handler: adminCustomerMember},
			{Method: "POST", Path: "/admin/customers/:id/member/level", Auth: cmfapp.AuthAdmin, Handler: setCustomerMemberLevelHandler},
			// 会员优先预约（§10.4）：客服端待处理预约意向列表，按会员等级置顶；处理意向使其从待办消解。
			{Method: "GET", Path: "/admin/priority-bookings", Auth: cmfapp.AuthAdmin, Handler: adminPriorityBookingsHandler},
			{Method: "POST", Path: "/admin/priority-bookings/:id/process", Auth: cmfapp.AuthAdmin, Handler: adminResolvePriorityBookingHandler},
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
			{Method: "GET", Path: "/admin/orders/:id/dispatch-candidates", Auth: cmfapp.AuthAdmin, Handler: readableOrderHandler(adminDispatchCandidates)},
			{Method: "GET", Path: "/admin/orders/:id/priority-notices", Auth: cmfapp.AuthAdmin, Handler: readableOrderHandler(adminOrderPriorityNotices)},
			{Method: "POST", Path: "/admin/orders/:id/release-priority", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(releaseOrderPriorityHandler)},
			// 普通待派单订单发布到抢单大厅（§5.2 / §5.6）：没有优先名单的游戏分区由此进入大厅。
			{Method: "POST", Path: "/admin/orders/:id/publish-grab", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(publishOrderToGrabHallHandler)},
			// 陪玩转单（§5.7）：接单后无法服务由陪玩发起，须客服确认才释放席位。
			{Method: "GET", Path: "/admin/seat-transfers", Auth: cmfapp.AuthAdmin, Handler: adminSeatTransfers},
			{Method: "POST", Path: "/admin/seat-transfers/:id/decision", Auth: cmfapp.AuthAdmin, Handler: decideSeatTransferHandler},
			{Method: "GET", Path: "/admin/orders/:id/seats", Auth: cmfapp.AuthAdmin, Handler: readableOrderHandler(orderSeatsHandler)},
			{Method: "POST", Path: "/admin/orders/:id/seats/:seatIndex/assign", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(assignSeatHandler)},
			{Method: "POST", Path: "/admin/orders/:id/seats/:seatIndex/revoke", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(revokeSeatHandler)},
			// 固排成队（§5.3）：主陪玩 + 固排一次成队。
			{Method: "GET", Path: "/admin/orders/:id/teammate-candidates", Auth: cmfapp.AuthAdmin, Handler: readableOrderHandler(adminTeammateCandidates)},
			{Method: "POST", Path: "/admin/orders/:id/team-dispatch", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(teamDispatchHandler)},
			{Method: "POST", Path: "/admin/orders/:id/claim", Auth: cmfapp.AuthAdmin, Handler: claimOrderHandler},
			{Method: "POST", Path: "/admin/orders/:id/release-pool", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(releaseOrderToPublicPoolHandler)},
			{Method: "POST", Path: "/admin/orders/:id/transfer", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(transferOrderHandler)},
			{Method: "POST", Path: "/admin/orders/:id/remind", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(remindOrderHandler)},
			// 加钟（§3.3 / §6.2 / §12.5）：服务中订单追加金额与工时，由陪玩确认后累加并生成加钟分成。
			{Method: "GET", Path: "/admin/orders/:id/addons", Auth: cmfapp.AuthAdmin, Handler: readableOrderHandler(listOrderAddonsHandler)},
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
			{Method: "GET", Path: "/admin/orders/:id/close-form", Auth: cmfapp.AuthAdmin, Handler: readableOrderHandler(orderCloseFormHandler)},
			// 结单审批进度（§3.4）：当前待审批节点，供复核弹窗展示。
			{Method: "GET", Path: "/admin/orders/:id/close-approval", Auth: cmfapp.AuthAdmin, Handler: readableOrderHandler(orderCloseApprovalHandler)},
			// —— 第 4 轮第 2 波新增路由 ——
			// ⚠️ `/admin/after-sales/rules` 与 `/admin/after-sales/rule-hits` 必须**先于**紧邻其下的
			// `/admin/after-sales/:id` 注册：Fiber 按注册顺序匹配，`:id` 会把字面量段 `rules` 吃掉
			// （实测 `:id` 就在下一行）。改动此顺序会静默让规则 CRUD 变成「查详情」。
			{Method: "GET", Path: "/admin/after-sales/rules", Auth: cmfapp.AuthAdmin, Handler: adminAfterSaleRulesHandler},
			{Method: "POST", Path: "/admin/after-sales/rules", Auth: cmfapp.AuthAdmin, Handler: adminCreateAfterSaleRuleHandler},
			{Method: "GET", Path: "/admin/after-sales/rules/:id", Auth: cmfapp.AuthAdmin, Handler: adminAfterSaleRuleDetailHandler},
			{Method: "PUT", Path: "/admin/after-sales/rules/:id", Auth: cmfapp.AuthAdmin, Handler: adminUpdateAfterSaleRuleHandler},
			{Method: "DELETE", Path: "/admin/after-sales/rules/:id", Auth: cmfapp.AuthAdmin, Handler: adminDeleteAfterSaleRuleHandler},
			{Method: "GET", Path: "/admin/after-sales/rule-hits", Auth: cmfapp.AuthAdmin, Handler: adminAfterSaleRuleHitsHandler},
			{Method: "GET", Path: "/admin/after-sales/:id/messages", Auth: cmfapp.AuthAdmin, Handler: adminAfterSaleMessagesHandler},
			{Method: "POST", Path: "/admin/after-sales/:id/messages", Auth: cmfapp.AuthAdmin, Handler: adminPostAfterSaleMessageHandler},
			{Method: "POST", Path: "/admin/after-sales/:id/evidence-request", Auth: cmfapp.AuthAdmin, Handler: adminRequestAfterSaleEvidenceHandler},
			// 收款发起（§4.3-02）、档期过期处置（§5.5-06）、期望报价复核（§6.1-12）
			// P0-12：收款发起与档期处置都会动钱（后者含全额退款），必须与其它订单写路由一样
			// 走 ownedOrderHandler 的「负责客服 / 超管」写入边界，不能只靠 PermissionMiddleware。
			{Method: "POST", Path: "/admin/orders/:id/payment/start", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(adminStartOrderPaymentHandler)},
			{Method: "POST", Path: "/admin/orders/:id/appointment/disposal", Auth: cmfapp.AuthAdmin, Handler: ownedOrderHandler(disposeExpiredAppointmentHandler)},
			{Method: "POST", Path: "/admin/players/:id/expected-price/review", Auth: cmfapp.AuthAdmin, Handler: adminReviewExpectedPriceHandler},
			// 客服在线状态写入口与列表（§1.1-08）：离线/积压自动转池已由 §5.1-05 交付，此处只补原子写入口。
			{Method: "POST", Path: "/admin/cs/presence", Auth: cmfapp.AuthAdmin, Handler: adminSetCsPresenceHandler},
			{Method: "GET", Path: "/admin/cs/presences", Auth: cmfapp.AuthAdmin, Handler: adminListCsPresencesHandler},
			// 客户归属转移（§8.3-02）：追加交接留痕，历史跟进不变。
			{Method: "POST", Path: "/admin/customers/:id/owner-transfer", Auth: cmfapp.AuthAdmin, Handler: transferCustomerOwnerHandler},
			// 页面预览码与底部导航（§16.4-02 / §16.3-12）；素材引用反查（§16.2-06）
			{Method: "POST", Path: "/admin/pages/:channel/preview-code", Auth: cmfapp.AuthAdmin, Handler: issuePagePreviewCodeHandler},
			{Method: "PUT", Path: "/admin/pages/:channel/tabbar", Auth: cmfapp.AuthAdmin, Handler: savePageTabbarHandler},
			{Method: "GET", Path: "/admin/pages/:channel/tabbar", Auth: cmfapp.AuthAdmin, Handler: adminPageTabbar},
			{Method: "GET", Path: "/admin/page-assets/:id/usages", Auth: cmfapp.AuthAdmin, Handler: pageAssetUsagesHandler},
			// 评价申诉（§7.5-05）：管理端列表与复核
			{Method: "GET", Path: "/admin/player-review-appeals", Auth: cmfapp.AuthAdmin, Handler: adminReviewAppeals},
			{Method: "POST", Path: "/admin/player-review-appeals/:id/review", Auth: cmfapp.AuthAdmin, Handler: reviewReviewAppealHandler},
			// 客户 / 陪玩端自助（AuthUser 按仓库约定不需要 PermAPI）
			{Method: "GET", Path: "/user/after-sales/:id/messages", Auth: cmfapp.AuthUser, Handler: userAfterSaleMessagesHandler},
			{Method: "POST", Path: "/user/after-sales/:id/messages", Auth: cmfapp.AuthUser, Handler: userPostAfterSaleMessageHandler},
			{Method: "GET", Path: "/user/after-sales/:id/progress", Auth: cmfapp.AuthUser, Handler: userAfterSaleProgressHandler},
			{Method: "GET", Path: "/user/after-sales/:id/evidence-status", Auth: cmfapp.AuthUser, Handler: userAfterSaleEvidenceStatusHandler},
			{Method: "GET", Path: "/user/player/priority-status", Auth: cmfapp.AuthUser, Handler: playerPriorityStatusHandler},
			{Method: "POST", Path: "/user/player/expected-price", Auth: cmfapp.AuthUser, Handler: playerSubmitExpectedPriceHandler},
			{Method: "POST", Path: "/user/reviews/:id/appeal", Auth: cmfapp.AuthUser, Handler: userReviewAppealHandler},
			// 客户自助「我的评价」列表（§7.5）：给出 review_id 与 appeal_status，申诉 UI 才能自行定位评价。
			{Method: "GET", Path: "/user/reviews", Auth: cmfapp.AuthUser, Handler: userReviewsHandler},
			// 公开：页面预览码兑换（§16.4-02）
			{Method: "GET", Path: "/page/preview/:code", Auth: cmfapp.AuthPublic, Handler: publicPagePreview},
			// —— 礼物档位与收款（§9.1-02 / §9.2-01）、账本核验异常（§11.6-01）、陪玩可用时段（§10.4-04）、收礼记录（§6.1-14）——
			{Method: "GET", Path: "/admin/gifts/:id/tiers", Auth: cmfapp.AuthAdmin, Handler: giftTierListHandler},
			{Method: "POST", Path: "/admin/gifts/:id/tiers", Auth: cmfapp.AuthAdmin, Handler: createGiftTierHandler},
			{Method: "PUT", Path: "/admin/gift-tiers/:id", Auth: cmfapp.AuthAdmin, Handler: updateGiftTierHandler},
			{Method: "DELETE", Path: "/admin/gift-tiers/:id", Auth: cmfapp.AuthAdmin, Handler: deleteGiftTierHandler},
			{Method: "POST", Path: "/admin/gift-intents/:id/payment/start", Auth: cmfapp.AuthAdmin, Handler: startGiftPaymentHandler},
			{Method: "POST", Path: "/admin/gift-intents/:id/payment/verify", Auth: cmfapp.AuthAdmin, Handler: verifyGiftPaymentHandler},
			{Method: "GET", Path: "/admin/ledger-reconcile-anomalies", Auth: cmfapp.AuthAdmin, Handler: adminLedgerReconcileAnomalies},
			{Method: "GET", Path: "/admin/players/:id/availability", Auth: cmfapp.AuthAdmin, Handler: adminPlayerAvailabilityHandler},
			{Method: "GET", Path: "/user/player/players/:id/availability", Auth: cmfapp.AuthUser, Handler: userPlayerAvailabilityHandler},
			{Method: "GET", Path: "/user/player/gift-records", Auth: cmfapp.AuthUser, Handler: userPlayerGiftRecordsHandler},
			// 公开：礼物收款回调（§9.2-01）：原始字节 + `X-Pay-Signature` 验签，渠道取 `?channel=` 或 `X-Pay-Channel`。
			{Method: "POST", Path: "/public/gift-payment-notify", Auth: cmfapp.AuthPublic, Handler: giftPaymentNotifyHandler},
			// —— 售后单（§13.1）：列表 / 详情 / 建单 / 受理 / 方案 / 审批 / 执行 / 撤回 ——
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
			// 结算单列表支持 ?status= 过滤（P0-8）：默认不含作废单，status=void 才返回作废单。
			{Method: "GET", Path: "/admin/settlements", Auth: cmfapp.AuthAdmin, Handler: adminSettlementsFiltered},
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
			// 客服提成明细冻结 / 解冻（审核报告 §8.2-14）：财务 / 客服可手工止血（冻结不计入可结算），
			// 解冻记录操作人，冻结需填原因。
			{Method: "POST", Path: "/admin/cs-commission-entries/:id/freeze", Auth: cmfapp.AuthAdmin, Handler: freezeCsCommissionEntryHandler},
			{Method: "POST", Path: "/admin/cs-commission-entries/:id/unfreeze", Auth: cmfapp.AuthAdmin, Handler: unfreezeCsCommissionEntryHandler},
			{Method: "GET", Path: "/admin/cs-commission/summary", Auth: cmfapp.AuthAdmin, Handler: adminCsCommissionSummary},
			{Method: "POST", Path: "/admin/cs-settlements", Auth: cmfapp.AuthAdmin, Handler: createCsSettlementHandler},
			{Method: "POST", Path: "/admin/settlements/:id/pay", Auth: cmfapp.AuthAdmin, Handler: markSettlementPaidHandler},
			// 结算单作废 / 重开（P0-6）：草稿或锁定未发放的客服提成结算单可作废（明细回退待结算），
			// 作废后可按同一客服 + 同一周期重开重新生成草稿；作废人 / 发放人不得等于登记人（锁定人）。
			{Method: "POST", Path: "/admin/cs-settlements/:id/void", Auth: cmfapp.AuthAdmin, Handler: voidCsSettlementHandler},
			{Method: "POST", Path: "/admin/cs-settlements/reopen", Auth: cmfapp.AuthAdmin, Handler: reopenCsSettlementHandler},
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
			{Method: "GET", Path: "/admin/players/:id/identity", Auth: cmfapp.AuthAdmin, Handler: adminPlayerIdentityHandler},
			{Method: "POST", Path: "/admin/players/:id/identity/review", Auth: cmfapp.AuthAdmin, Handler: adminReviewPlayerIdentityHandler},
			// 陪玩端自助：供 uni-app 客户端调用，不挂管理端菜单。
			{Method: "GET", Path: "/user/player/dashboard", Auth: cmfapp.AuthUser, Handler: playerDashboardHandler},
			{Method: "GET", Path: "/user/player/profile", Auth: cmfapp.AuthUser, Handler: playerProfileHandler},
			// 陪玩自助维护非资格类资料（§6.1 入驻）：昵称 / 手机号 / 擅长游戏；
			// 资格、等级、在线状态等字段一律不接受客户端写入。
			{Method: "PUT", Path: "/user/player/profile", Auth: cmfapp.AuthUser, Handler: playerUpdateProfileHandler},
			// 陪玩站内消息（通知闭环）：读取 player:<id> 地址下的通用消息并回执已读。
			{Method: "GET", Path: "/user/player/messages", Auth: cmfapp.AuthUser, Handler: playerMessagesHandler},
			{Method: "POST", Path: "/user/player/messages/read", Auth: cmfapp.AuthUser, Handler: playerMessagesMarkReadHandler},
			{Method: "POST", Path: "/user/player/identity", Auth: cmfapp.AuthUser, Handler: playerSubmitIdentityHandler},
			{Method: "GET", Path: "/user/player/identity", Auth: cmfapp.AuthUser, Handler: playerIdentityStatusHandler},
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
			// 陪玩自助申请升级考核（§7.1-02 / §7.2-01）：自助形态走 AuthUser，无需 PermAPI。
			{Method: "POST", Path: "/user/player/assessments/upgrade-apply", Auth: cmfapp.AuthUser, Handler: playerApplyUpgradeAssessmentHandler},
			// 客户自助：已完成订单的服务评价（§7.5）。
			{Method: "POST", Path: "/user/orders/:id/reviews", Auth: cmfapp.AuthUser, Handler: userSubmitReviewHandler},
			// 客户自助（§13.2 / §10）：下单、我的订单 / 售后、余额与资料，均按绑定客户做数据隔离。
			{Method: "POST", Path: "/user/orders", Auth: cmfapp.AuthUser, Handler: userPlaceOrderHandler},
			{Method: "GET", Path: "/user/orders", Auth: cmfapp.AuthUser, Handler: userOrdersHandler},
			{Method: "GET", Path: "/user/orders/:id", Auth: cmfapp.AuthUser, Handler: userOrderDetailHandler},
			{Method: "POST", Path: "/user/orders/:id/cancel", Auth: cmfapp.AuthUser, Handler: userCancelUnpaidOrderHandler},
			{Method: "GET", Path: "/user/orders/:id/members", Auth: cmfapp.AuthUser, Handler: userOrderMembersHandler},
			// 客户自助收款闭环（§4.3）：读收款方式与账户、提交付款凭证、查询核验进度。
			{Method: "GET", Path: "/user/payment-methods", Auth: cmfapp.AuthUser, Handler: userPaymentMethodsHandler},
			{Method: "POST", Path: "/user/orders/:id/payment-proof", Auth: cmfapp.AuthUser, Handler: userSubmitPaymentProofHandler},
			{Method: "GET", Path: "/user/orders/:id/payment-proofs", Auth: cmfapp.AuthUser, Handler: userPaymentProofsHandler},
			// 在线收款（§4.3）：已开通渠道发起收款，返回渠道收银台地址；核销走公开回调 payment-notify。
			{Method: "POST", Path: "/user/orders/:id/payment/start", Auth: cmfapp.AuthUser, Handler: userStartOrderPaymentHandler},
			// 客户自助站内付款（§4.3）：余额足额核销，复用管理端同一核销边界并核对订单归属。
			{Method: "POST", Path: "/user/orders/:id/pay-by-balance", Auth: cmfapp.AuthUser, Handler: userPayOrderByBalanceHandler},
			{Method: "GET", Path: "/user/aftersales", Auth: cmfapp.AuthUser, Handler: userAfterSalesHandler},
			{Method: "POST", Path: "/user/aftersales", Auth: cmfapp.AuthUser, Handler: userCreateAfterSaleHandler},
			{Method: "GET", Path: "/user/wallets", Auth: cmfapp.AuthUser, Handler: userWalletHandler},
			// 钱包展示口径（§11.4）：两端读取服务端配置的名称 / 单位 / 换算倍率，避免客户端硬编码。
			{Method: "GET", Path: "/user/wallets/display", Auth: cmfapp.AuthUser, Handler: userWalletDisplayHandler},
			{Method: "GET", Path: "/user/profile", Auth: cmfapp.AuthUser, Handler: userProfileHandler},
			// 客户站内消息（通知闭环）：读取 customer:<id> 地址下的通用消息并回执已读，
			// 与管理员消息中心口径一致，避免站内信只能写不能读。
			{Method: "GET", Path: "/user/messages", Auth: cmfapp.AuthUser, Handler: userMessagesHandler},
			{Method: "POST", Path: "/user/messages/read", Auth: cmfapp.AuthUser, Handler: userMessagesMarkReadHandler},
			// 会员优先预约（§10.4）：客户自助提交预约意向（陪玩 + 期望时段 + 备注）。
			{Method: "POST", Path: "/user/priority-bookings", Auth: cmfapp.AuthUser, Handler: userCreatePriorityBookingHandler},
			// 客户自助充值意图（§4.5）：锁定活动快照 + 支付意图 + 付款指引。
			{Method: "POST", Path: "/user/recharge-intents", Auth: cmfapp.AuthUser, Handler: userCreateRechargeIntentHandler},
			// 客户 / 陪玩端确认 / 拒绝改期提案（§3.3）。
			{Method: "POST", Path: "/user/reschedule-proposals/:id/confirm", Auth: cmfapp.AuthUser, Handler: customerConfirmRescheduleProposalHandler},
			{Method: "POST", Path: "/user/player/reschedule-proposals/:id/confirm", Auth: cmfapp.AuthUser, Handler: playerConfirmRescheduleProposalHandler},
			// 客户 / 陪玩端读取本人可见的改期提案（§3.3）：待确认优先，含逐人确认进度摘要。
			{Method: "GET", Path: "/user/reschedule-proposals", Auth: cmfapp.AuthUser, Handler: customerRescheduleProposalsHandler},
			{Method: "GET", Path: "/user/player/reschedule-proposals", Auth: cmfapp.AuthUser, Handler: playerRescheduleProposalsHandler},
			{Method: "GET", Path: "/user/gift-intents", Auth: cmfapp.AuthUser, Handler: userGiftIntents},
			{Method: "POST", Path: "/user/gift-intents", Auth: cmfapp.AuthUser, Handler: userCreateGiftIntentHandler},
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
			{Method: "POST", Path: "/admin/pages/:channel/publish-review/:versionId/approve", Auth: cmfapp.AuthAdmin, Handler: approvePagePublishHandler},
			{Method: "POST", Path: "/admin/pages/:channel/publish-review/:versionId/reject", Auth: cmfapp.AuthAdmin, Handler: rejectPagePublishHandler},
			{Method: "GET", Path: "/admin/pages/:channel/versions", Auth: cmfapp.AuthAdmin, Handler: adminPageVersions},
			{Method: "GET", Path: "/admin/page-assets", Auth: cmfapp.AuthAdmin, Handler: adminPageAssets},
			{Method: "POST", Path: "/admin/page-assets", Auth: cmfapp.AuthAdmin, Handler: createPageAssetHandler},
			{Method: "DELETE", Path: "/admin/page-assets/:id", Auth: cmfapp.AuthAdmin, Handler: deletePageAssetHandler},
			{Method: "GET", Path: "/page", Auth: cmfapp.AuthPublic, Handler: publicPage},
			{Method: "GET", Path: "/wecom/sidebar-config", Auth: cmfapp.AuthPublic, Handler: publicWecomSidebarConfig},
			// 企微 JS-SDK 签名与侧边栏收款方式（§8.4）：签名供前端 wx.config；收款方式复用客户自助端视图。
			{Method: "POST", Path: "/admin/wecom/js-config", Auth: cmfapp.AuthAdmin, Handler: wecomJsConfigHandler},
			{Method: "GET", Path: "/admin/wecom/payment-methods", Auth: cmfapp.AuthAdmin, Handler: adminWecomPaymentMethodsHandler},
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
			// —— 审核报告收尾批次：4 处「后端缺接口」补齐（均为只读或状态推进的窄接口）——
			// 代客确认（§13.3）：pending_customer_confirm → pending_approval，只写代确认人，
			// 不等于审批；审批仍是 /approve 且审批人不得为该单处理人。
			{Method: "POST", Path: "/admin/after-sales/:id/confirm", Auth: cmfapp.AuthAdmin, Handler: ownedAfterSaleHandler(adminConfirmAfterSaleHandler)},
			// 档期超时处置可用动作（§5.5-06 只读）：前端据此渲染「全额退还 / 改期」按钮。
			{Method: "GET", Path: "/admin/orders/:id/appointment/disposal-options", Auth: cmfapp.AuthAdmin, Handler: readableOrderHandler(appointmentDisposalOptionsHandler)},
			// 服务段计时读取（§3.3 只读）：给「时长更正」弹窗预填有效时长与可填上界。
			{Method: "GET", Path: "/admin/orders/:id/players/:playerId/service/timing", Auth: cmfapp.AuthAdmin, Handler: readableOrderHandler(adminServiceTimingHandler)},
			// 充值退款执行批次列表（§4.5 只读）：?status= pending / executed、?limit=。
			{Method: "GET", Path: "/admin/recharge-refund-executions", Auth: cmfapp.AuthAdmin, Handler: listRechargeRefundExecutionsHandler},
		},
		Permissions: []cmfapp.Permission{
			// 商品模块：一个模块一个菜单。
			{Type: cmfapp.PermMenu, Name: "商品", Code: "esports:product:menu", MenuPath: "/esports/products", Parent: "esports:operations:menu", Component: "esports/products/index", Icon: "icon-apps", SortOrder: 40},
			{Type: cmfapp.PermAPI, Name: "查看商品", Code: "esports:products:list", Path: "/api/v1/esports/admin/products", Method: "GET", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "创建商品", Code: "esports:products:create", Path: "/api/v1/esports/admin/products", Method: "POST", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑商品", Code: "esports:products:update", Path: "/api/v1/esports/admin/products/{id}", Method: "PUT", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "查看产品分类", Code: "esports:categories:list", Path: "/api/v1/esports/admin/categories", Method: "GET", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "创建产品分类", Code: "esports:categories:create", Path: "/api/v1/esports/admin/categories", Method: "POST", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑产品分类", Code: "esports:categories:update", Path: "/api/v1/esports/admin/categories/{id}", Method: "PUT", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "删除产品分类", Code: "esports:categories:delete", Path: "/api/v1/esports/admin/categories/{id}", Method: "DELETE", Parent: "esports:product:menu"},
			// SKU 停服（§15.7）：申请 / 列表 / 详情 / 异人审批 / 撤回 / 逐单处置。
			{Type: cmfapp.PermAPI, Name: "发起停服申请", Code: "esports:products:suspension_request", Path: "/api/v1/esports/admin/products/{id}/suspensions", Method: "POST", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "查看停服申请", Code: "esports:products:suspension_list", Path: "/api/v1/esports/admin/product-suspensions", Method: "GET", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "停服申请详情", Code: "esports:products:suspension_detail", Path: "/api/v1/esports/admin/product-suspensions/{id}", Method: "GET", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "审批停服申请", Code: "esports:products:suspension_decision", Path: "/api/v1/esports/admin/product-suspensions/{id}/decision", Method: "POST", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "撤回停服申请", Code: "esports:products:suspension_cancel", Path: "/api/v1/esports/admin/product-suspensions/{id}/cancel", Method: "POST", Parent: "esports:product:menu"},
			{Type: cmfapp.PermAPI, Name: "停服逐单处置", Code: "esports:products:suspension_disposal", Path: "/api/v1/esports/admin/product-suspensions/{id}/orders/{orderId}/disposal", Method: "POST", Parent: "esports:product:menu"},
			{Type: cmfapp.PermButton, Name: "新增商品按钮", Code: "esports:products:add", Parent: "esports:product:menu"},
			{Type: cmfapp.PermButton, Name: "编辑商品按钮", Code: "esports:products:edit", Parent: "esports:product:menu"},
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
			{Type: cmfapp.PermAPI, Name: "确认预约档期", Code: "esports:orders:appointment_confirm", Path: "/api/v1/esports/admin/orders/{id}/appointment/confirm", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "登记渠道成本", Code: "esports:orders:channel-cost", Path: "/api/v1/esports/admin/orders/{id}/channel-cost", Method: "PUT", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "余额核销订单", Code: "esports:orders:pay_balance", Path: "/api/v1/esports/admin/orders/{id}/pay-by-balance", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "结单", Code: "esports:orders:close", Path: "/api/v1/esports/admin/orders/{id}/close", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "复核通过结单", Code: "esports:orders:close_approve", Path: "/api/v1/esports/admin/orders/{id}/close-approve", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "复核驳回结单", Code: "esports:orders:close_reject", Path: "/api/v1/esports/admin/orders/{id}/close-reject", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "查看订单结单表单", Code: "esports:orders:close_form", Path: "/api/v1/esports/admin/orders/{id}/close-form", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "查看结单条件", Code: "esports:orders:close_conditions", Path: "/api/v1/esports/admin/orders/{id}/close-conditions", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "查看结单审批进度", Code: "esports:orders:close_approval", Path: "/api/v1/esports/admin/orders/{id}/close-approval", Method: "GET", Parent: "esports:order:menu"},
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
			{Type: cmfapp.PermAPI, Name: "终止服务段", Code: "esports:orders:service_terminate", Path: "/api/v1/esports/admin/orders/{id}/players/{playerId}/service/terminate", Method: "POST", Parent: "esports:order:menu"},
			// 限时改期提案（§3.3）：发起 / 查看 / 撤回。
			{Type: cmfapp.PermAPI, Name: "发起改期提案", Code: "esports:orders:reschedule_propose", Path: "/api/v1/esports/admin/orders/{id}/reschedule-proposals", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "查看改期提案", Code: "esports:orders:reschedule_list", Path: "/api/v1/esports/admin/orders/{id}/reschedule-proposals", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "撤回改期提案", Code: "esports:orders:reschedule_withdraw", Path: "/api/v1/esports/admin/reschedule-proposals/{id}/withdraw", Method: "POST", Parent: "esports:order:menu"},
			// 订单逐席角色快照（§15.1 / §15.5）。
			// 授权协作订单（§8）：协作仅放开只读可见，授予 / 撤销由超管或订单负责人执行。
			{Type: cmfapp.PermAPI, Name: "查看订单协作客服", Code: "esports:orders:collaborators", Path: "/api/v1/esports/admin/orders/{id}/collaborators", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "授予订单协作", Code: "esports:orders:collaborator_add", Path: "/api/v1/esports/admin/orders/{id}/collaborators", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "撤销订单协作", Code: "esports:orders:collaborator_remove", Path: "/api/v1/esports/admin/orders/{id}/collaborators/{adminId}", Method: "DELETE", Parent: "esports:order:menu"},
			{Type: cmfapp.PermButton, Name: "订单操作按钮", Code: "esports:orders:operate", Parent: "esports:order:menu"},

			// 钱包模块：客户余额与陪玩猫粮账户、只增流水。
			{Type: cmfapp.PermMenu, Name: "钱包", Code: "esports:wallet:menu", MenuPath: "/esports/wallets", Parent: "esports:operations:menu", Component: "esports/wallets/index", Icon: "icon-wallet", SortOrder: 43},
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
			{Type: cmfapp.PermAPI, Name: "查看固排候选", Code: "esports:dispatch:teammate_candidates", Path: "/api/v1/esports/admin/orders/{id}/teammate-candidates", Method: "GET", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "固排一次成队", Code: "esports:dispatch:team_dispatch", Path: "/api/v1/esports/admin/orders/{id}/team-dispatch", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "查看订单优先通知", Code: "esports:dispatch:priority_notices", Path: "/api/v1/esports/admin/orders/{id}/priority-notices", Method: "GET", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "提前释放优先通知", Code: "esports:dispatch:priority_release", Path: "/api/v1/esports/admin/orders/{id}/release-priority", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "发布到抢单大厅", Code: "esports:dispatch:publish_grab", Path: "/api/v1/esports/admin/orders/{id}/publish-grab", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermButton, Name: "优先派单权按钮", Code: "esports:dispatch:priority_manage", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "查看转单申请", Code: "esports:dispatch:transfers", Path: "/api/v1/esports/admin/seat-transfers", Method: "GET", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "确认转单申请", Code: "esports:dispatch:transfer_decide", Path: "/api/v1/esports/admin/seat-transfers/{id}/decision", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermButton, Name: "转单确认按钮", Code: "esports:dispatch:transfer_manage", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "查看订单席位", Code: "esports:dispatch:seats", Path: "/api/v1/esports/admin/orders/{id}/seats", Method: "GET", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "指派席位", Code: "esports:dispatch:assign", Path: "/api/v1/esports/admin/orders/{id}/seats/{seatIndex}/assign", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "撤回席位", Code: "esports:dispatch:revoke", Path: "/api/v1/esports/admin/orders/{id}/seats/{seatIndex}/revoke", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "认领订单", Code: "esports:dispatch:claim", Path: "/api/v1/esports/admin/orders/{id}/claim", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "释放到公共池", Code: "esports:dispatch:release_pool", Path: "/api/v1/esports/admin/orders/{id}/release-pool", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "转单", Code: "esports:dispatch:transfer", Path: "/api/v1/esports/admin/orders/{id}/transfer", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermAPI, Name: "催单", Code: "esports:dispatch:remind", Path: "/api/v1/esports/admin/orders/{id}/remind", Method: "POST", Parent: "esports:dispatch:menu"},
			{Type: cmfapp.PermButton, Name: "排单操作按钮", Code: "esports:dispatch:operate", Parent: "esports:dispatch:menu"},

			// 陪玩模块：管理端陪玩档案；陪玩端自助接口属独立客户端，不挂管理菜单。
			{Type: cmfapp.PermMenu, Name: "陪玩", Code: "esports:player:menu", MenuPath: "/esports/players", Parent: "esports:operations:menu", Component: "esports/players/index", Icon: "icon-user", SortOrder: 45},
			{Type: cmfapp.PermAPI, Name: "查看陪玩", Code: "esports:players:list", Path: "/api/v1/esports/admin/players", Method: "GET", Parent: "esports:player:menu"},
			{Type: cmfapp.PermAPI, Name: "创建陪玩", Code: "esports:players:create", Path: "/api/v1/esports/admin/players", Method: "POST", Parent: "esports:player:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑陪玩", Code: "esports:players:update", Path: "/api/v1/esports/admin/players/{id}", Method: "PUT", Parent: "esports:player:menu"},
			{Type: cmfapp.PermAPI, Name: "查看陪玩实名认证", Code: "esports:players:identity_view", Path: "/api/v1/esports/admin/players/{id}/identity", Method: "GET", Parent: "esports:player:menu"},
			{Type: cmfapp.PermAPI, Name: "审核陪玩实名认证", Code: "esports:players:identity_review", Path: "/api/v1/esports/admin/players/{id}/identity/review", Method: "POST", Parent: "esports:player:menu"},
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
			// —— 第 4 轮第 2 波新增 PermAPI：与「第 4 轮第 2 波新增路由」**一一对应**（方法 + 完整路径），
			//    多一条少一条都会让 TestEsportsAppDeclarationsValidate 变红。AuthUser/AuthPublic 路由按仓库约定不需要 PermAPI。——
			{Type: cmfapp.PermAPI, Name: "查看售后规则", Code: "esports:aftersale:rules_list", Path: "/api/v1/esports/admin/after-sales/rules", Method: "GET", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "创建售后规则", Code: "esports:aftersale:rules_create", Path: "/api/v1/esports/admin/after-sales/rules", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "查看售后规则详情", Code: "esports:aftersale:rules_detail", Path: "/api/v1/esports/admin/after-sales/rules/{id}", Method: "GET", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑售后规则", Code: "esports:aftersale:rules_update", Path: "/api/v1/esports/admin/after-sales/rules/{id}", Method: "PUT", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "删除售后规则", Code: "esports:aftersale:rules_delete", Path: "/api/v1/esports/admin/after-sales/rules/{id}", Method: "DELETE", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "查看售后规则命中留痕", Code: "esports:aftersale:rules_hits", Path: "/api/v1/esports/admin/after-sales/rule-hits", Method: "GET", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "查看售后沟通记录", Code: "esports:aftersales:messages_list", Path: "/api/v1/esports/admin/after-sales/{id}/messages", Method: "GET", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "回复售后沟通记录", Code: "esports:aftersales:messages_create", Path: "/api/v1/esports/admin/after-sales/{id}/messages", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "提示客户补证", Code: "esports:aftersales:evidence_request", Path: "/api/v1/esports/admin/after-sales/{id}/evidence-request", Method: "POST", Parent: "esports:aftersale:menu"},
			{Type: cmfapp.PermAPI, Name: "客服为订单发起收款", Code: "esports:payments:order_start", Path: "/api/v1/esports/admin/orders/{id}/payment/start", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "处置过期档期", Code: "esports:orders:appointment_dispose", Path: "/api/v1/esports/admin/orders/{id}/appointment/disposal", Method: "POST", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "复核陪玩期望报价", Code: "esports:players:expected_price_review", Path: "/api/v1/esports/admin/players/{id}/expected-price/review", Method: "POST", Parent: "esports:player:menu"},
			{Type: cmfapp.PermAPI, Name: "写入客服在线状态", Code: "esports:cs:presence_write", Path: "/api/v1/esports/admin/cs/presence", Method: "POST", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "查看客服在线状态", Code: "esports:cs:presence_list", Path: "/api/v1/esports/admin/cs/presences", Method: "GET", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "转移客户归属", Code: "esports:customers:transfer_owner", Path: "/api/v1/esports/admin/customers/{id}/owner-transfer", Method: "POST", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "生成页面预览码", Code: "esports:pages:preview_code", Path: "/api/v1/esports/admin/pages/{channel}/preview-code", Method: "POST", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "保存底部导航", Code: "esports:pages:tabbar_update", Path: "/api/v1/esports/admin/pages/{channel}/tabbar", Method: "PUT", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "查看底部导航", Code: "esports:pages:tabbar_detail", Path: "/api/v1/esports/admin/pages/{channel}/tabbar", Method: "GET", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "查看素材引用位置", Code: "esports:pages:asset_usages", Path: "/api/v1/esports/admin/page-assets/{id}/usages", Method: "GET", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "查看评价申诉", Code: "esports:assessments:review_appeals_list", Path: "/api/v1/esports/admin/player-review-appeals", Method: "GET", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermAPI, Name: "复核评价申诉", Code: "esports:assessments:review_appeal_decide", Path: "/api/v1/esports/admin/player-review-appeals/{id}/review", Method: "POST", Parent: "esports:assessment:menu"},
			{Type: cmfapp.PermAPI, Name: "查看礼物档位", Code: "esports:gifts:tiers", Path: "/api/v1/esports/admin/gifts/{id}/tiers", Method: "GET", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "创建礼物档位", Code: "esports:gifts:tier_create", Path: "/api/v1/esports/admin/gifts/{id}/tiers", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑礼物档位", Code: "esports:gifts:tier_update", Path: "/api/v1/esports/admin/gift-tiers/{id}", Method: "PUT", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "删除礼物档位", Code: "esports:gifts:tier_delete", Path: "/api/v1/esports/admin/gift-tiers/{id}", Method: "DELETE", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "发起礼物收款", Code: "esports:gifts:payment_start", Path: "/api/v1/esports/admin/gift-intents/{id}/payment/start", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "核验礼物收款", Code: "esports:gifts:payment_verify", Path: "/api/v1/esports/admin/gift-intents/{id}/payment/verify", Method: "POST", Parent: "esports:gift:menu"},
			{Type: cmfapp.PermAPI, Name: "查看账本核验异常", Code: "esports:wallets:reconcile_anomalies", Path: "/api/v1/esports/admin/ledger-reconcile-anomalies", Method: "GET", Parent: "esports:wallet:menu"},
			{Type: cmfapp.PermAPI, Name: "查看陪玩可用时段", Code: "esports:members:player_availability", Path: "/api/v1/esports/admin/players/{id}/availability", Method: "GET", Parent: "esports:member:menu"},

			// 结算模块：分成规则 / 分成明细 / 结算单。
			{Type: cmfapp.PermMenu, Name: "结算", Code: "esports:settlement:menu", MenuPath: "/esports/settlements", Component: "esports/settlements/index", Icon: "icon-safe", SortOrder: 47},
			{Type: cmfapp.PermAPI, Name: "查看结单规则", Code: "esports:settlements:close_rules", Path: "/api/v1/esports/admin/close-rules", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "创建结单规则", Code: "esports:settlements:close_rule_create", Path: "/api/v1/esports/admin/close-rules", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑结单规则", Code: "esports:settlements:close_rule_update", Path: "/api/v1/esports/admin/close-rules/{id}", Method: "PUT", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "生效结单规则", Code: "esports:settlements:close_rule_activate", Path: "/api/v1/esports/admin/close-rules/{id}/activate", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "回滚结单规则", Code: "esports:settlements:close_rule_rollback", Path: "/api/v1/esports/admin/close-rules/{id}/rollback", Method: "POST", Parent: "esports:settlement:menu"},
			// 结单表单设计器（§3.4）：字段库与模板配置，独立菜单。
			{Type: cmfapp.PermMenu, Name: "结单表单", Code: "esports:close_form:menu", MenuPath: "/esports/close-forms", Parent: "esports:operations:menu", Component: "esports/close-forms/index", Icon: "icon-file", SortOrder: 48},
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
			{Type: cmfapp.PermAPI, Name: "手动指定订单分成规则", Code: "esports:settlements:manual_rule", Path: "/api/v1/esports/admin/orders/{id}/commission-rule", Method: "PUT", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看客服提成规则", Code: "esports:settlements:cs_rules", Path: "/api/v1/esports/admin/cs-commission-rules", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "创建客服提成规则", Code: "esports:settlements:cs_rule_create", Path: "/api/v1/esports/admin/cs-commission-rules", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "生效客服提成规则", Code: "esports:settlements:cs_rule_activate", Path: "/api/v1/esports/admin/cs-commission-rules/{id}/activate", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "回滚客服提成规则", Code: "esports:settlements:cs_rule_rollback", Path: "/api/v1/esports/admin/cs-commission-rules/{id}/rollback", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看客服提成明细", Code: "esports:settlements:cs_entries", Path: "/api/v1/esports/admin/cs-commission-entries", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "冻结客服提成明细", Code: "esports:settlements:cs_entry_freeze", Path: "/api/v1/esports/admin/cs-commission-entries/{id}/freeze", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "解冻客服提成明细", Code: "esports:settlements:cs_entry_unfreeze", Path: "/api/v1/esports/admin/cs-commission-entries/{id}/unfreeze", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "查看客服提成概览", Code: "esports:settlements:cs_summary", Path: "/api/v1/esports/admin/cs-commission/summary", Method: "GET", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "生成客服提成结算单", Code: "esports:settlements:cs_settle", Path: "/api/v1/esports/admin/cs-settlements", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "登记结算发放", Code: "esports:settlements:pay", Path: "/api/v1/esports/admin/settlements/{id}/pay", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "作废客服提成结算单", Code: "esports:settlements:cs_void", Path: "/api/v1/esports/admin/cs-settlements/{id}/void", Method: "POST", Parent: "esports:settlement:menu"},
			{Type: cmfapp.PermAPI, Name: "重开客服提成结算单", Code: "esports:settlements:cs_reopen", Path: "/api/v1/esports/admin/cs-settlements/reopen", Method: "POST", Parent: "esports:settlement:menu"},
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
			{Type: cmfapp.PermMenu, Name: "充值活动", Code: "esports:campaign:menu", MenuPath: "/esports/campaigns", Parent: "esports:operations:menu", Component: "esports/campaigns/index", Icon: "icon-gift", SortOrder: 48},
			{Type: cmfapp.PermAPI, Name: "查看充值活动", Code: "esports:campaigns:list", Path: "/api/v1/esports/admin/campaigns", Method: "GET", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermAPI, Name: "创建充值活动", Code: "esports:campaigns:create", Path: "/api/v1/esports/admin/campaigns", Method: "POST", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑充值活动", Code: "esports:campaigns:update", Path: "/api/v1/esports/admin/campaigns/{id}", Method: "PUT", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermAPI, Name: "切换活动状态", Code: "esports:campaigns:status", Path: "/api/v1/esports/admin/campaigns/{id}/status", Method: "POST", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermAPI, Name: "按活动充值", Code: "esports:campaigns:recharge", Path: "/api/v1/esports/admin/wallets/{id}/recharge-campaigns", Method: "POST", Parent: "esports:campaign:menu"},
			// 充值独立支付意图核验（§4.5）。
			{Type: cmfapp.PermAPI, Name: "核验充值到账", Code: "esports:campaigns:recharge_verify", Path: "/api/v1/esports/admin/recharge-intents/{intentNo}/verify", Method: "POST", Parent: "esports:campaign:menu"},
			{Type: cmfapp.PermButton, Name: "充值活动管理按钮", Code: "esports:campaigns:manage", Parent: "esports:campaign:menu"},

			// 会员等级模块：等级配置与客户会员等级（§10）。
			{Type: cmfapp.PermMenu, Name: "会员等级", Code: "esports:member:menu", MenuPath: "/esports/members", Parent: "esports:operations:menu", Component: "esports/members/index", Icon: "icon-star", SortOrder: 49},
			{Type: cmfapp.PermAPI, Name: "查看会员等级", Code: "esports:members:levels", Path: "/api/v1/esports/admin/member-levels", Method: "GET", Parent: "esports:member:menu"},
			{Type: cmfapp.PermAPI, Name: "创建会员等级", Code: "esports:members:level_create", Path: "/api/v1/esports/admin/member-levels", Method: "POST", Parent: "esports:member:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑会员等级", Code: "esports:members:level_update", Path: "/api/v1/esports/admin/member-levels/{id}", Method: "PUT", Parent: "esports:member:menu"},
			{Type: cmfapp.PermAPI, Name: "启停会员等级", Code: "esports:members:level_status", Path: "/api/v1/esports/admin/member-levels/{id}/status", Method: "POST", Parent: "esports:member:menu"},
			{Type: cmfapp.PermAPI, Name: "删除会员等级", Code: "esports:members:level_delete", Path: "/api/v1/esports/admin/member-levels/{id}", Method: "DELETE", Parent: "esports:member:menu"},
			{Type: cmfapp.PermAPI, Name: "查看客户会员", Code: "esports:members:detail", Path: "/api/v1/esports/admin/customers/{id}/member", Method: "GET", Parent: "esports:member:menu"},
			{Type: cmfapp.PermAPI, Name: "手动调级", Code: "esports:members:set_level", Path: "/api/v1/esports/admin/customers/{id}/member/level", Method: "POST", Parent: "esports:member:menu"},
			{Type: cmfapp.PermAPI, Name: "查看会员优先预约", Code: "esports:members:priority_bookings", Path: "/api/v1/esports/admin/priority-bookings", Method: "GET", Parent: "esports:member:menu"},
			{Type: cmfapp.PermAPI, Name: "处理会员优先预约", Code: "esports:members:priority_booking_process", Path: "/api/v1/esports/admin/priority-bookings/{id}/process", Method: "POST", Parent: "esports:member:menu"},
			{Type: cmfapp.PermButton, Name: "会员等级管理按钮", Code: "esports:members:manage", Parent: "esports:member:menu"},

			// 技能等级（陪玩等级）模块：与会员等级同级的独立菜单，等级档位配置（§5.5 / §7.3）。
			{Type: cmfapp.PermMenu, Name: "技能等级", Code: "esports:skill_level:menu", MenuPath: "/esports/skill-levels", Parent: "esports:operations:menu", Component: "esports/skill-levels/index", Icon: "icon-trophy", SortOrder: 50},
			{Type: cmfapp.PermAPI, Name: "查看技能等级", Code: "esports:skill_levels:list", Path: "/api/v1/esports/admin/skill-levels", Method: "GET", Parent: "esports:skill_level:menu"},
			{Type: cmfapp.PermAPI, Name: "保存技能等级", Code: "esports:skill_levels:save", Path: "/api/v1/esports/admin/skill-levels", Method: "PUT", Parent: "esports:skill_level:menu"},
			{Type: cmfapp.PermButton, Name: "技能等级管理按钮", Code: "esports:skill_levels:manage", Parent: "esports:skill_level:menu"},

			// 礼物模块：礼物目录、送礼单据、退款复核与榜单（§9）。
			{Type: cmfapp.PermMenu, Name: "礼物", Code: "esports:gift:menu", MenuPath: "/esports/gifts", Parent: "esports:operations:menu", Component: "esports/gifts/index", Icon: "icon-heart", SortOrder: 51},
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
			{Type: cmfapp.PermMenu, Name: "收款管理", Code: "esports:payment:menu", MenuPath: "/esports/payments", Parent: "esports:operations:menu", Component: "esports/payments/index", Icon: "icon-safe", SortOrder: 52},
			{Type: cmfapp.PermAPI, Name: "查看收款账户", Code: "esports:payments:accounts", Path: "/api/v1/esports/admin/payment-accounts", Method: "GET", Parent: "esports:payment:menu"},
			{Type: cmfapp.PermAPI, Name: "上线自检", Code: "esports:payments:readiness", Path: "/api/v1/esports/admin/readiness", Method: "GET", Parent: "esports:payment:menu"},
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

			// 运营管理目录与页面装修模块（§16 / §16.2-01）：页面装修直接承载
			// 装修编辑器组件，作为运营管理下的一条菜单（不再设「客户首页」子菜单）。
			{Type: cmfapp.PermMenu, Name: "运营管理", Code: "esports:operations:menu", MenuPath: "/esports/operations", Icon: "icon-apps", SortOrder: 39},
			{Type: cmfapp.PermMenu, Name: "页面装修", Code: "esports:page:menu", MenuPath: "/esports/pages/home", Parent: "esports:operations:menu", Component: "esports/pages/index", Icon: "icon-desktop", SortOrder: 53},
			{Type: cmfapp.PermAPI, Name: "查看装修文档", Code: "esports:pages:list", Path: "/api/v1/esports/admin/pages", Method: "GET", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "查看装修详情", Code: "esports:pages:detail", Path: "/api/v1/esports/admin/pages/{channel}", Method: "GET", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "保存草稿", Code: "esports:pages:draft_save", Path: "/api/v1/esports/admin/pages/{channel}/draft", Method: "PUT", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "提交发布申请", Code: "esports:pages:publish", Path: "/api/v1/esports/admin/pages/{channel}/publish", Method: "POST", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "提交回滚申请", Code: "esports:pages:rollback", Path: "/api/v1/esports/admin/pages/{channel}/rollback", Method: "POST", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "通过发布审核", Code: "esports:pages:publish_approve", Path: "/api/v1/esports/admin/pages/{channel}/publish-review/{versionId}/approve", Method: "POST", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "驳回发布审核", Code: "esports:pages:publish_reject", Path: "/api/v1/esports/admin/pages/{channel}/publish-review/{versionId}/reject", Method: "POST", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "查看发布记录", Code: "esports:pages:versions", Path: "/api/v1/esports/admin/pages/{channel}/versions", Method: "GET", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "查看素材库", Code: "esports:pages:assets", Path: "/api/v1/esports/admin/page-assets", Method: "GET", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "新增素材", Code: "esports:pages:asset_create", Path: "/api/v1/esports/admin/page-assets", Method: "POST", Parent: "esports:page:menu"},
			{Type: cmfapp.PermAPI, Name: "删除素材", Code: "esports:pages:asset_delete", Path: "/api/v1/esports/admin/page-assets/{id}", Method: "DELETE", Parent: "esports:page:menu"},
			{Type: cmfapp.PermButton, Name: "装修操作按钮", Code: "esports:pages:manage", Parent: "esports:page:menu"},
			{Type: cmfapp.PermButton, Name: "提交发布按钮", Code: "esports:pages:publish_submit", Parent: "esports:page:menu"},
			{Type: cmfapp.PermButton, Name: "审核发布按钮", Code: "esports:pages:publish_review", Parent: "esports:page:menu"},
			{Type: cmfapp.PermButton, Name: "回滚按钮", Code: "esports:pages:rollback_btn", Parent: "esports:page:menu"},

			// 客服端模块（§8）：客户档案与跟进、企微自动建档、话术库、客服工作台聚合。
			{Type: cmfapp.PermMenu, Name: "客服工作台", Code: "esports:cs:menu", MenuPath: "/esports/cs", Component: "esports/cs/index", Icon: "icon-customer-service", SortOrder: 54},
			{Type: cmfapp.PermAPI, Name: "查看客服工作台", Code: "esports:cs:workbench", Path: "/api/v1/esports/admin/cs/workbench", Method: "GET", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "查看客户档案汇总", Code: "esports:customers:profile", Path: "/api/v1/esports/admin/customers/{id}/profile", Method: "GET", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑客户档案", Code: "esports:customers:profile_update", Path: "/api/v1/esports/admin/customers/{id}/profile", Method: "PUT", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "查看跟进记录", Code: "esports:customers:followups", Path: "/api/v1/esports/admin/customers/{id}/follow-ups", Method: "GET", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "新增跟进记录", Code: "esports:customers:followup_create", Path: "/api/v1/esports/admin/customers/{id}/follow-ups", Method: "POST", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "企微自动建档", Code: "esports:customers:wecom_intake", Path: "/api/v1/esports/admin/customers/wecom-intake", Method: "POST", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "企微 JS-SDK 签名", Code: "esports:wecom:js_config", Path: "/api/v1/esports/admin/wecom/js-config", Method: "POST", Parent: "esports:cs:menu"},
			{Type: cmfapp.PermAPI, Name: "企微侧边栏收款方式", Code: "esports:wecom:payment_methods", Path: "/api/v1/esports/admin/wecom/payment-methods", Method: "GET", Parent: "esports:cs:menu"},
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

			// —— 审核报告收尾批次新增 PermAPI：与「审核报告收尾批次」的路由**一一对应**（方法 + 完整路径一致），
			//    多一条少一条都会让 internal/apps 的 TestEsportsAppDeclarationsValidate 变红。 ——
			{Type: cmfapp.PermAPI, Name: "代客确认售后方案", Code: "esports:aftersales:confirm", Path: "/api/v1/esports/admin/after-sales/{id}/confirm", Method: "POST", Parent: "esports:aftersale:menu"},
			// 档期处置可用动作：与「处置过期档期」同一业务动作的只读前置，仍挂在订单菜单下；
			// 权限码必须与 appointment_dispose 区分（ValidateApps 要求权限码全局唯一）。
			{Type: cmfapp.PermAPI, Name: "查看档期处置可用动作", Code: "esports:orders:appointment_disposal_options", Path: "/api/v1/esports/admin/orders/{id}/appointment/disposal-options", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "查看服务段计时", Code: "esports:orders:service_timing", Path: "/api/v1/esports/admin/orders/{id}/players/{playerId}/service/timing", Method: "GET", Parent: "esports:order:menu"},
			{Type: cmfapp.PermAPI, Name: "查看退款执行批次", Code: "esports:settlements:recharge_refund_executions", Path: "/api/v1/esports/admin/recharge-refund-executions", Method: "GET", Parent: "esports:settlement:menu"},
		},
	})
}

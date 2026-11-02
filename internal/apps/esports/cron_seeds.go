package esports

import "go-admin/internal/apps/apphook"

// CronSeed 定时任务种子（结构见 apphook.CronSeed）。本功能集的任务依赖种子才会被
// 调度——订单超时释放、邀请过期回收、结算生成、保护期降级、账本核验、外盒与通知
// 投递等都依赖定时任务才会生效（审计 D12）。
type CronSeed = apphook.CronSeed

// CronSeeds 返回本功能集的定时任务种子。任务函数均设计为幂等收敛，重复执行安全；
// 功能集未开启（ESPORTS_EXPERIMENTAL != 1）时不会执行 Provision，也就不会播种。
// 种子清单与 app.go 中 tasks.Register 的注册必须成对，否则一致性测试会红。
func CronSeeds() []CronSeed {
	return []CronSeed{
		{Name: "esports_expire_orders", Expression: "0 * * * * *", Description: "待付款订单超时取消并释放名额"},
		{Name: "esports_expire_dispatch_invites", Expression: "0 * * * * *", Description: "派单邀请到期撤销并释放占位"},
		{Name: "esports_expire_wallet_bonus", Expression: "0 0 * * * *", Description: "充值赠送权益到期作废"},
		{Name: "esports_apply_member_downgrade", Expression: "0 10 0 * * *", Description: "会员等级保护期到期降级"},
		{Name: "esports_publish_scheduled_pages", Expression: "0 * * * * *", Description: "客户首页装修到点发布"},
		{Name: "esports_rebuild_dispatch_priority", Expression: "0 0 1 * * *", Description: "优先派单权名单周期重算与预生成"},
		{Name: "esports_expire_dispatch_priority_notice", Expression: "0 * * * * *", Description: "优先通知窗口超时释放到抢单大厅"},
		{Name: "esports_remind_dispatch_ladder", Expression: "0 * * * * *", Description: "超时未接单逐级催单"},
		{Name: "esports_check_service_overtime", Expression: "0 */5 * * * *", Description: "服务超时提醒客服确认加钟"},
		{Name: "esports_generate_cs_settlements", Expression: "0 30 2 * * *", Description: "按周期生成客服提成结算单"},
		{Name: "esports_generate_player_settlements", Expression: "0 35 2 * * *", Description: "按周期生成陪玩分成结算单"},
		{Name: "esports_activate_scheduled_commission_rules", Expression: "0 */5 * * * *", Description: "分成/提成规则到点生效"},
		{Name: "esports_check_aftersale_sla", Expression: "0 */5 * * * *", Description: "售后时限超时升级"},
		{Name: "esports_check_close_approval_timeout", Expression: "0 */5 * * * *", Description: "结单审批节点超时策略"},
		{Name: "esports_flag_high_risk_customers", Expression: "0 0 3 * * *", Description: "高频退款客户风险标记"},
		{Name: "esports_reconcile_ledgers", Expression: "0 0 4 * * *", Description: "钱包/批次账本全量核验"},
		{Name: "esports_auto_offline_players", Expression: "0 * * * * *", Description: "心跳超时陪玩自动离线"},
		{Name: "esports_auto_pause_disconnected_players", Expression: "0 * * * * *", Description: "断线超阈值自动暂停服务段"},
		{Name: "esports_expire_reschedule_proposals", Expression: "0 * * * * *", Description: "改期提案到期失效"},
		{Name: "esports_apply_due_sku_suspensions", Expression: "0 * * * * *", Description: "到点的 SKU 停服申请生效"},
		{Name: "esports_expire_recharge_intents", Expression: "0 */5 * * * *", Description: "充值支付意图过期"},
		{Name: "esports_compensate_checkout_payments", Expression: "0 */5 * * * *", Description: "聚合收款查单补偿与超时关闭"},
		{Name: "esports_sync_payout_batches", Expression: "0 */5 * * * *", Description: "云账户代发批次查单补偿与状态收敛"},
		{Name: "esports_deliver_outbox", Expression: "0 * * * * *", Description: "事务外盒事件投递与失败重试"},
		{Name: "esports_deliver_notifications", Expression: "0 * * * * *", Description: "关键通知定向投递与失败待办"},
		{Name: "esports_auto_trigger_upgrade_assessments", Expression: "0 25 * * * *", Description: "达标陪玩自动生成升级考核单"},
		{Name: "esports_auto_transfer_backlog_orders", Expression: "0 * * * * *", Description: "客服离线/订单积压自动转公共池"},
		{Name: "esports_scan_dispatch_escalations", Expression: "0 * * * * *", Description: "派单三类逐级升级与多席缺员自动补位"},
		{Name: "esports_scan_aftersale_rules", Expression: "0 * * * * *", Description: "按规则扫描结单后金额/时长异常并建 channel=system 售后单"},
		{Name: "esports_scan_close_deadline", Expression: "0 */10 * * * *", Description: "结单时限超时进客服与管理员待办"},
		{Name: "esports_expire_appointment_confirmations", Expression: "0 * * * * *", Description: "档期确认过期置待处置并提供退还/改期"},
		{Name: "esports_generate_spot_checks", Expression: "0 0 * * * *", Description: "按周期与抽取比例生成随机抽查任务"},
		{Name: "esports_run_player_level_review", Expression: "0 30 3 * * *", Description: "陪玩等级定期复核，连续不达标降一档"},
		{Name: "esports_detect_review_brushing", Expression: "0 */5 * * * *", Description: "异常刷评自动识别与留痕"},
		{Name: "esports_schedule_repurchase_follow_ups", Expression: "0 15 10 * * *", Description: "结单后复购跟进待办兜底"},
		{Name: "esports_expire_member_level_overrides", Expression: "0 25 0 * * *", Description: "会员手动调级覆盖到期回落成长值口径"},
		{Name: "esports_expire_priority_booking_intents", Expression: "0 5 * * * *", Description: "会员优先预约意向过期消解，待办不再堆积"},
		{Name: "esports_expire_pending_external_refunds", Expression: "0 15 * * * *", Description: "外部退款登记后长期未执行超期督办，只提醒不代办"},
		{Name: "esports_expire_realname_pending", Expression: "0 */5 * * * *", Description: "统一实名待审超时置失效并把流水置放弃"},
		{Name: "esports_auto_confirm_customer_grabs", Expression: "0 * * * * *", Description: "客户超时未确认抢单陪玩自动确认"},
	}
}

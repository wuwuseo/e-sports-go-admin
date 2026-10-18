package esports

// CronSeed 描述功能集自带的一个定时任务种子：新库首次 Provision 时按 name 幂等建 CronJob，
// 避免「任务函数已注册、却从未被调度」——订单超时释放、邀请过期回收、结算生成、
// 保护期降级、账本核验、外盒与通知投递等都依赖定时任务才会生效（审计 D12）。
type CronSeed struct {
	// Name 必须与 app.go 中 tasks.Register 注册的名字完全一致（CronJob.command 即任务名）。
	Name string
	// Expression 为 6 段 cron 表达式（含秒），与 internal/cron 的 WithSeconds() 解析器一致。
	Expression string
	// Description 供管理端定时任务列表展示。
	Description string
}

// CronSeeds 返回本功能集的定时任务种子。任务函数均设计为幂等收敛，重复执行安全；
// 功能集未开启（ESPORTS_EXPERIMENTAL != 1）时不会执行 Provision，也就不会播种。
func CronSeeds() []CronSeed {
	return []CronSeed{
		{"esports_expire_orders", "0 * * * * *", "待付款订单超时取消并释放名额（§4.2）"},
		{"esports_expire_dispatch_invites", "0 * * * * *", "派单邀请到期撤销并释放占位（§5.9）"},
		{"esports_expire_wallet_bonus", "0 0 * * * *", "充值赠送权益到期作废（§4.5）"},
		{"esports_apply_member_downgrade", "0 10 0 * * *", "会员等级保护期到期降级（§10.3）"},
		{"esports_publish_scheduled_pages", "0 * * * * *", "客户首页装修到点发布（§16.4）"},
		{"esports_expire_stored_value_cards", "0 5 * * * *", "储值卡到期作废赠送权益（§4.4）"},
		{"esports_rebuild_dispatch_priority", "0 0 1 * * *", "优先派单权名单周期重算与预生成（§5.4）"},
		{"esports_expire_dispatch_priority_notice", "0 * * * * *", "优先通知窗口超时释放到抢单大厅（§5.4）"},
		{"esports_remind_dispatch_ladder", "0 * * * * *", "超时未接单逐级催单（§5.7）"},
		{"esports_check_service_overtime", "0 */5 * * * *", "服务超时提醒客服确认加钟（§5.7）"},
		{"esports_generate_cs_settlements", "0 30 2 * * *", "按周期生成客服提成结算单（§12.2）"},
		{"esports_generate_player_settlements", "0 35 2 * * *", "按周期生成陪玩分成结算单（§12.6）"},
		{"esports_activate_scheduled_commission_rules", "0 */5 * * * *", "分成/提成规则到点生效（§12.4）"},
		{"esports_check_aftersale_sla", "0 */5 * * * *", "售后时限超时升级（§13.5）"},
		{"esports_check_close_approval_timeout", "0 */5 * * * *", "结单审批节点超时策略（§3.4）"},
		{"esports_flag_high_risk_customers", "0 0 3 * * *", "高频退款客户风险标记（§13.7）"},
		{"esports_reconcile_ledgers", "0 0 4 * * *", "钱包/批次账本全量核验（§11.6）"},
		{"esports_auto_offline_players", "0 * * * * *", "心跳超时陪玩自动离线（§6.4）"},
		{"esports_auto_pause_disconnected_players", "0 * * * * *", "断线超阈值自动暂停服务段（§3.3）"},
		{"esports_expire_reschedule_proposals", "0 * * * * *", "改期提案到期失效（§3.3）"},
		{"esports_apply_due_sku_suspensions", "0 * * * * *", "到点的 SKU 停服申请生效（§15.7）"},
		{"esports_expire_customer_stored_value_intents", "0 */5 * * * *", "自助购卡支付意图过期（§4.4）"},
		{"esports_expire_recharge_intents", "0 */5 * * * *", "充值支付意图过期（§4.5）"},
		{"esports_deliver_outbox", "0 * * * * *", "事务外盒事件投递与失败重试（§2.1）"},
		{"esports_deliver_notifications", "0 * * * * *", "关键通知定向投递与失败待办（§2.1）"},
	}
}

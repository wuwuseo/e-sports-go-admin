package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"go-admin/internal/apps/esports/schematier"
)

type EsportsOrder struct{ ent.Schema }

func (EsportsOrder) Fields() []ent.Field {
	return []ent.Field{
		field.String("order_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"order_no"`),
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		// 常规报单必须绑定已上架 SKU；管理员补录无 SKU 的历史单允许 sku_id=0 不绑定 SKU。
		field.Int("sku_id").StorageKey("product_id").StructTag(`json:"product_id"`),
		// 下单时的商品 / 规格名快照：报单时从 SKU 与规格组写入，详情与导出改读快照，
		// 之后改 SKU 标题或规格名不追溯改写历史单；历史单为空串，读取方回落实时 SKU。
		field.String("sku_code").StorageKey("product_code").MaxLen(80).Default("").StructTag(`json:"product_code"`),
		field.String("sku_name").StorageKey("product_name").MaxLen(160).Default("").StructTag(`json:"product_name"`),
		field.String("spec_name").MaxLen(120).Default("").StructTag(`json:"spec_name"`),
		field.Int("created_by_admin_id").Optional().Nillable().StructTag(`json:"created_by_admin_id"`),
		field.Int("responsible_admin_id").Optional().Nillable().StructTag(`json:"responsible_admin_id"`),
		// 客服提成在足额核销时锁定业绩归属与规则；locked_at 非空、rule_id 空表示当时无规则。
		field.Int("cs_commission_admin_id").Optional().Nillable().StructTag(`json:"cs_commission_admin_id"`),
		field.Int("cs_commission_rule_id").Optional().Nillable().StructTag(`json:"cs_commission_rule_id"`),
		field.Time("cs_commission_rule_locked_at").Optional().Nillable().StructTag(`json:"cs_commission_rule_locked_at"`),
		// 五级优先级的最高一级「单笔手动指定」：派单/接单前由管理员为这一单指定分成规则。
		// 只在成员锁定席位规则前生效；已锁定成员的分成快照不因事后指定而追溯。
		field.Int("manual_commission_rule_id").Optional().Nillable().StructTag(`json:"manual_commission_rule_id"`),
		field.Int("manual_commission_rule_set_by_admin_id").Optional().Nillable().StructTag(`json:"manual_commission_rule_set_by_admin_id"`),
		field.Time("manual_commission_rule_set_at").Optional().Nillable().StructTag(`json:"manual_commission_rule_set_at"`),
		// 下单时锁定结单表单版本；locked_at 非空、template_id 空表示下单当时无适用模板。
		field.Int("close_form_template_id").Optional().Nillable().StructTag(`json:"close_form_template_id"`),
		field.Time("close_form_template_locked_at").Optional().Nillable().StructTag(`json:"close_form_template_locked_at"`),
		// 下单时的客户来源快照，用于经营报表按渠道来源筛选；客户后来改档不影响历史订单。
		field.Enum("channel_source").Values("manual", "wecom", "self", "import").Default("manual").StructTag(`json:"channel_source"`),
		// 报单入口快照：PC 客服或客户自助；移动客服和企微侧边栏启用后可扩展入口。
		field.Enum("entry_point").Values("cs_pc", "cs_mobile", "wecom_sidebar", "customer_self", "admin_backfill").Default("cs_pc").StructTag(`json:"entry_point"`),
		field.Int64("quantity").Positive().StructTag(`json:"quantity"`),
		field.Int64("unit_price_cents").Positive().StructTag(`json:"unit_price_cents"`),
		field.Int64("min_unit_price_cents").Default(0).StructTag(`json:"min_unit_price_cents"`),
		field.Int("adjustment_approval_bp").Default(2000).StructTag(`json:"adjustment_approval_bp"`),
		field.Int64("discount_cents").Default(0).StructTag(`json:"discount_cents"`),
		field.Int64("manual_adjust_cents").Default(0).StructTag(`json:"manual_adjust_cents"`),
		// 会员等级与消费折扣基点快照：下单与改价时按当时生效的等级规则写入，
		// 事后改折扣规则不追溯改写；历史单等级为空串、基点 10000 表示按不打折口径展示。
		field.String("member_level_code").MaxLen(32).Default("").StructTag(`json:"member_level_code"`),
		field.Int("member_discount_bps").Default(10000).StructTag(`json:"member_discount_bps"`),
		field.Int64("payable_cents").Positive().StructTag(`json:"payable_cents"`),
		field.Int64("promised_service_units").Positive().StructTag(`json:"promised_service_units"`),
		// 每席承诺服务量：多人单下单时从 SKU（其次规格组）
		// 快照，业务侧须校验 promised_service_units = per_seat_service_units × required_seats；
		// 0 表示未配置（历史单 / 单人单），读取方按「未提供每席量」处理。
		field.Int64("per_seat_service_units").Default(0).StructTag(`json:"per_seat_service_units"`),
		// 服务时长快照（分钟，）：下单时从 SKU 快照，用于预约时段重叠与容量判定；0 表示未配置。
		field.Int("service_duration_minutes").Default(0).StructTag(`json:"service_duration_minutes"`),
		// 加钟累计：由「已确认」的加钟单累加，只增不改；payable_cents 保持原报价快照。
		field.Int64("addon_cents").Default(0).StructTag(`json:"addon_cents"`),
		field.Int64("addon_units").Default(0).StructTag(`json:"addon_units"`),
		field.Int("required_seats").Positive().StructTag(`json:"required_seats"`),
		// 所需技能等级快照：报单时从 SKU 快照，用于接单硬性校验。
		// 取值为系统配置 app.esports.skill_levels 的等级编码；历史单保留快照时的编码。
		field.String("required_level").MaxLen(32).Default("novice").StructTag(`json:"required_level"`),
		// 逐席角色快照：报单时从 SKU 关联规格组快照每席角色/可替换条件，多人规格报价据此展示席位约束；
		// 历史单缺该字段为空（不追溯）。
		field.JSON("seat_roles", []schematier.SeatSpec{}).Optional().StructTag(`json:"seat_roles"`),
		field.Int("quote_version").Default(1).StructTag(`json:"quote_version"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Enum("fulfillment_status").Values("pending_payment", "pending_dispatch", "pending_acceptance", "grabbing", "accepted", "in_service", "pending_close", "pending_close_review", "completed", "cancelled").Default("pending_payment").StructTag(`json:"fulfillment_status"`),
		field.Enum("payment_status").Values("unpaid", "partial", "paid", "exception").Default("unpaid").StructTag(`json:"payment_status"`),
		// 付款方式标记：核销成功时写入，仅用于查询与结单一致性校验。
		// stored_value_card 为已废弃值（2026-10-20 储值卡功能移除），保留仅为历史订单可读，禁止新写入。
		field.Enum("payment_channel").Values("none", "customer_balance", "stored_value_card",
			"wechat_work", "merchant_qr", "corporate_transfer").Default("none").StructTag(`json:"payment_channel"`),
		// 优先派单权「独占优先通知」窗口（业务口径）：进入待派单时若该游戏分区存在生效名单，
		// 则只有名单内陪玩可见并可接单；名单内全部拒绝或窗口超时后释放到抢单大厅（转 grabbing）。
		// none=未开窗（直接进大厅） / pending=优先通知中 / released=已释放到大厅 / accepted=名单内陪玩已接单。
		field.Enum("priority_notice_status").Values("none", "pending", "released", "accepted").Default("none").
			StructTag(`json:"priority_notice_status"`),
		field.Time("priority_notice_until").Optional().Nillable().StructTag(`json:"priority_notice_until"`),
		// 名单匹配用的游戏分区快照（订单建立后商品分类再变更不影响已开的窗口）。
		field.String("priority_game").MaxLen(64).Default("").StructTag(`json:"priority_game"`),
		// 释放到大厅的原因：超时 / 名单内全部拒绝 / 客服提前释放。
		field.String("priority_release_reason").MaxLen(255).Default("").StructTag(`json:"priority_release_reason"`),
		// 催单优先级：POST /admin/orders/:id/remind 写入的最新档位（1~3），0=未催单；
		// 看板按该字段排序与展示（此前只落订单事件 details，无法检索/排序）。
		field.Int("remind_priority_level").Default(0).StructTag(`json:"remind_priority_level"`),
		field.String("remind_note").MaxLen(255).Default("").StructTag(`json:"remind_note"`),
		field.Time("reminded_at").Optional().Nillable().StructTag(`json:"reminded_at"`),
		// 时间线埋点（各自只写一次，永不改写）：首次进入待派单池、首次派出邀请、
		// 首次有效接单、开工时间；配合 priority_notice_until / priority_released_at
		// 可区分「名单内响应慢」与「无人可派」。各类等待时长由报表按这些时间戳派生。
		field.Time("first_pooled_at").Optional().Nillable().StructTag(`json:"first_pooled_at"`),
		field.Time("first_invited_at").Optional().Nillable().StructTag(`json:"first_invited_at"`),
		field.Time("first_accepted_at").Optional().Nillable().StructTag(`json:"first_accepted_at"`),
		field.Time("service_started_at").Optional().Nillable().StructTag(`json:"service_started_at"`),
		// 「可以开工」时刻：全部席位就绪后业务侧写入一次，并与 order.ready_to_start
		// 订单事件同时落库；「进入待派单池」沿用 first_pooled_at 与 order.entered_pending_dispatch 事件。
		field.Time("ready_to_start_at").Optional().Nillable().StructTag(`json:"ready_to_start_at"`),
		field.Time("priority_released_at").Optional().Nillable().StructTag(`json:"priority_released_at"`),
		// 客户指定的陪玩：有值时订单直接走指派流程，**不进优先通知窗口**；
		// 并在派单候选中置顶标记 Preferred。报单时写入，须校验陪玩资质。
		field.Int("preferred_player_id").Optional().Nillable().StructTag(`json:"preferred_player_id"`),
		// 多陪玩快照（多人单）：报单时指定的全部陪玩（第一个与 preferred_player_id 一致）。
		// 指定后订单直接由客服指派、不进优先通知窗口；派单时按此列表逐席位人工指派。
		field.JSON("preferred_player_ids", []int{}).Optional().StructTag(`json:"preferred_player_ids"`),
		field.Enum("refund_status").Values("none", "pending_approval", "executing", "partial", "full", "failed").Default("none").StructTag(`json:"refund_status"`),
		// 累计已退金额（分），退款执行成功后累加。
		field.Int64("refunded_cents").Default(0).StructTag(`json:"refunded_cents"`),
		// 结单时尚未退款的服务应付额。结单后退款按此基数冲回当时实际计提的服务分成/客服提成；
		// nil 表示历史订单未留快照，退款冲回沿用原应付额。
		field.Int64("close_net_service_cents").Optional().Nillable().StructTag(`json:"close_net_service_cents"`),
		// 已预占但尚未执行的退款额度（分），售后审批通过时预占、执行时扣减。
		field.Int64("refund_reserved_cents").Default(0).StructTag(`json:"refund_reserved_cents"`),
		// 已知渠道成本（分，）：由客服在订单上登记的渠道费用，结单计提客服提成时从可分基数扣除，
		// 参与「工作室剩余 = 服务应付 − 陪玩分成 − 客服提成 − 渠道成本」的反算。默认 0。
		field.Int64("channel_cost_cents").Default(0).StructTag(`json:"channel_cost_cents"`),
		// appointment_status 末尾追加 pending_confirm：付费预约「已付款待确认档期」的独立展示态；
		// 档期确认时限到期由任务置为该态（写 appointment_suspended_at）并暂停派单、提供全额退款/改期。既有取值保留。
		field.Enum("appointment_status").Values("instant", "request", "confirmed", "pending_confirm").Default("instant").StructTag(`json:"appointment_status"`),
		field.Time("requested_start_at").Optional().Nillable().StructTag(`json:"requested_start_at"`),
		field.Time("appointment_confirmation_deadline").Optional().Nillable().StructTag(`json:"appointment_confirmation_deadline"`),
		// 档期确认到期自动暂停派单的时刻：仅由到期处置任务写入，空表示未被自动暂停。
		field.Time("appointment_suspended_at").Optional().Nillable().StructTag(`json:"appointment_suspended_at"`),
		field.Time("payment_deadline").StructTag(`json:"payment_deadline"`),
		field.Time("paid_at").Optional().Nillable().StructTag(`json:"paid_at"`),
		// 结单（客服手动结单）时间，仅结单后非空。
		field.Time("closed_at").Optional().Nillable().StructTag(`json:"closed_at"`),
		// 存单只是订单上的一个冻结标记：冻结不改变履约状态，也不释放 SKU 名额占用。
		field.Enum("deposit_status").Values("none", "frozen").Default("none").StructTag(`json:"deposit_status"`),
		// 存单时间与操作管理员，仅 deposit_status=frozen 时非空。
		field.Time("deposited_at").Optional().Nillable().StructTag(`json:"deposited_at"`),
		field.Int("deposited_by_admin_id").Optional().Nillable().StructTag(`json:"deposited_by_admin_id"`),
		// 管理员历史订单补录：补录单固定 entry_point=admin_backfill，
		// 必须记录补录原因、补录人与补录时刻。陪玩分成与成长值默认正常计提；
		// backfill_count_commission 重新定义为「是否计入客服提成」，默认 false（客服提成默认不计入）。
		field.String("backfill_reason").MaxLen(255).Default("").StructTag(`json:"backfill_reason"`),
		field.Bool("backfill_count_commission").Default(false).StructTag(`json:"backfill_count_commission"`),
		field.Int("backfilled_by_admin_id").Optional().Nillable().StructTag(`json:"backfilled_by_admin_id"`),
		field.Time("backfilled_at").Optional().Nillable().StructTag(`json:"backfilled_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

// Edges 仅供授权协作查询：order.collaborators 对应协作文档，读写边界仍以 responsible_admin_id 为准。
func (EsportsOrder) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("collaborators", EsportsOrderCollaborator.Type),
	}
}

func (EsportsOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_no").Unique(),
		index.Fields("customer_id", "created_at"),
		index.Fields("responsible_admin_id", "fulfillment_status"),
		index.Fields("channel_source", "entry_point", "created_at"),
	}
}

type EsportsPaymentIntent struct{ ent.Schema }

func (EsportsPaymentIntent) Fields() []ent.Field {
	return []ent.Field{
		field.String("intent_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"intent_no"`),
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.Int("confirmation_id").Positive().StructTag(`json:"confirmation_id"`),
		field.Int("quote_version").Positive().StructTag(`json:"quote_version"`),
		field.Int64("amount_cents").Positive().StructTag(`json:"amount_cents"`),
		field.String("channel").NotEmpty().MaxLen(48).StructTag(`json:"channel"`),
		field.Enum("status").Values("pending", "invalidated", "paid", "expired").Default("pending").StructTag(`json:"status"`),
		field.Time("expires_at").StructTag(`json:"expires_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsPaymentIntent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("intent_no").Unique(),
		index.Fields("order_id", "quote_version"),
	}
}

type EsportsPaymentReceipt struct{ ent.Schema }

func (EsportsPaymentReceipt) Fields() []ent.Field {
	return []ent.Field{
		field.Int("intent_id").Positive().StructTag(`json:"intent_id"`),
		field.String("channel").NotEmpty().MaxLen(48).StructTag(`json:"channel"`),
		field.String("transaction_no").NotEmpty().MaxLen(128).StructTag(`json:"transaction_no"`),
		field.Int64("actual_cents").Positive().StructTag(`json:"actual_cents"`),
		field.Int64("applied_cents").Default(0).StructTag(`json:"applied_cents"`),
		field.Int64("exception_cents").Default(0).StructTag(`json:"exception_cents"`),
		field.Enum("status").Values("verified", "exception").Default("exception").StructTag(`json:"status"`),
		field.Int("verified_by_admin_id").Optional().Nillable().StructTag(`json:"verified_by_admin_id"`),
		field.Time("confirmed_at").StructTag(`json:"confirmed_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsPaymentReceipt) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel", "transaction_no").Unique(),
		index.Fields("intent_id"),
	}
}

type EsportsOrderEvent struct{ ent.Schema }

func (EsportsOrderEvent) Fields() []ent.Field {
	return []ent.Field{
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.Int("order_version").Positive().StructTag(`json:"order_version"`),
		field.String("event_type").NotEmpty().MaxLen(80).StructTag(`json:"event_type"`),
		field.String("actor_type").NotEmpty().MaxLen(40).StructTag(`json:"actor_type"`),
		field.String("actor_id").MaxLen(80).StructTag(`json:"actor_id"`),
		field.JSON("details", map[string]interface{}{}).Optional().StructTag(`json:"details"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsOrderEvent) Indexes() []ent.Index {
	return []ent.Index{index.Fields("order_id", "order_version").Unique()}
}

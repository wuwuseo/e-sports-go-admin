package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type EsportsOrder struct{ ent.Schema }

func (EsportsOrder) Fields() []ent.Field {
	return []ent.Field{
		field.String("order_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"order_no"`),
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		field.Int("sku_id").Positive().StructTag(`json:"sku_id"`),
		field.Int("created_by_admin_id").Optional().Nillable().StructTag(`json:"created_by_admin_id"`),
		field.Int("responsible_admin_id").Optional().Nillable().StructTag(`json:"responsible_admin_id"`),
		field.Int64("quantity").Positive().StructTag(`json:"quantity"`),
		field.Int64("unit_price_cents").Positive().StructTag(`json:"unit_price_cents"`),
		field.Int64("discount_cents").Default(0).StructTag(`json:"discount_cents"`),
		field.Int64("manual_adjust_cents").Default(0).StructTag(`json:"manual_adjust_cents"`),
		field.Int64("payable_cents").Positive().StructTag(`json:"payable_cents"`),
		field.Int64("promised_service_units").Positive().StructTag(`json:"promised_service_units"`),
	// 加钟累计（§3.3 / §12.5）：由「已确认」的加钟单累加，只增不改；payable_cents 保持原报价快照。
	field.Int64("addon_cents").Default(0).StructTag(`json:"addon_cents"`),
	field.Int64("addon_units").Default(0).StructTag(`json:"addon_units"`),
		field.Int("required_seats").Positive().StructTag(`json:"required_seats"`),
		field.Int("quote_version").Default(1).StructTag(`json:"quote_version"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Enum("fulfillment_status").Values("pending_payment", "pending_dispatch", "pending_acceptance", "grabbing", "accepted", "in_service", "pending_close", "pending_close_review", "completed", "cancelled").Default("pending_payment").StructTag(`json:"fulfillment_status"`),
		field.Enum("payment_status").Values("unpaid", "partial", "paid", "exception").Default("unpaid").StructTag(`json:"payment_status"`),
		// 付款方式标记（§3.1：储值卡不作为订单类型，用订单上的独立字段标记付款方式）。
		// 核销成功时写入，仅用于查询与结单一致性校验。
		field.Enum("payment_channel").Values("none", "customer_balance", "stored_value_card",
			"wechat_work", "merchant_qr", "corporate_transfer").Default("none").StructTag(`json:"payment_channel"`),
		// 关联储值卡（§4.6：储值卡标记、关联储值卡与核销流水不一致时禁止结算）。
		field.Int("stored_value_card_id").Optional().Nillable().StructTag(`json:"stored_value_card_id"`),
		// 优先派单权「独占优先通知」窗口（§5.4 业务口径）：进入待派单时若该游戏分区存在生效名单，
		// 则只有名单内陪玩可见并可接单；名单内全部拒绝或窗口超时后释放到抢单大厅（转 grabbing）。
		// none=未开窗（直接进大厅） / pending=优先通知中 / released=已释放到大厅 / accepted=名单内陪玩已接单。
		field.Enum("priority_notice_status").Values("none", "pending", "released", "accepted").Default("none").
			StructTag(`json:"priority_notice_status"`),
		field.Time("priority_notice_until").Optional().Nillable().StructTag(`json:"priority_notice_until"`),
		// 名单匹配用的游戏分区快照（订单建立后商品分类再变更不影响已开的窗口）。
		field.String("priority_game").MaxLen(64).Default("").StructTag(`json:"priority_game"`),
		// 释放到大厅的原因：超时 / 名单内全部拒绝 / 客服提前释放。
		field.String("priority_release_reason").MaxLen(255).Default("").StructTag(`json:"priority_release_reason"`),
		// §5.9 时间线埋点（各自只写一次，永不改写）：首次进入待派单池、首次派出邀请、
		// 首次有效接单、开工时间；配合 priority_notice_until / priority_released_at
		// 可区分「名单内响应慢」与「无人可派」。各类等待时长由报表按这些时间戳派生。
		field.Time("first_pooled_at").Optional().Nillable().StructTag(`json:"first_pooled_at"`),
		field.Time("first_invited_at").Optional().Nillable().StructTag(`json:"first_invited_at"`),
		field.Time("first_accepted_at").Optional().Nillable().StructTag(`json:"first_accepted_at"`),
		field.Time("service_started_at").Optional().Nillable().StructTag(`json:"service_started_at"`),
		field.Time("priority_released_at").Optional().Nillable().StructTag(`json:"priority_released_at"`),
		// 客户指定的陪玩（§5.4）：有值时订单直接走指派流程，**不进优先通知窗口**；
		// 并在派单候选中置顶标记 Preferred。报单时写入，须校验陪玩资质。
		field.Int("preferred_player_id").Optional().Nillable().StructTag(`json:"preferred_player_id"`),
		field.Enum("refund_status").Values("none", "pending_approval", "executing", "partial", "full", "failed").Default("none").StructTag(`json:"refund_status"`),
		// 累计已退金额（分），退款执行成功后累加。
		field.Int64("refunded_cents").Default(0).StructTag(`json:"refunded_cents"`),
		// 已预占但尚未执行的退款额度（分），售后审批通过时预占、执行时扣减。
		field.Int64("refund_reserved_cents").Default(0).StructTag(`json:"refund_reserved_cents"`),
		field.Enum("appointment_status").Values("instant", "request", "confirmed").Default("instant").StructTag(`json:"appointment_status"`),
		field.Time("requested_start_at").Optional().Nillable().StructTag(`json:"requested_start_at"`),
		field.Time("appointment_confirmation_deadline").Optional().Nillable().StructTag(`json:"appointment_confirmation_deadline"`),
		field.Time("payment_deadline").StructTag(`json:"payment_deadline"`),
		field.Time("paid_at").Optional().Nillable().StructTag(`json:"paid_at"`),
		// 结单（客服手动结单）时间，仅结单后非空。
		field.Time("closed_at").Optional().Nillable().StructTag(`json:"closed_at"`),
		// 存单只是订单上的一个冻结标记：冻结不改变履约状态，也不释放 SKU 名额占用。
		field.Enum("deposit_status").Values("none", "frozen").Default("none").StructTag(`json:"deposit_status"`),
		// 存单时间与操作管理员，仅 deposit_status=frozen 时非空。
		field.Time("deposited_at").Optional().Nillable().StructTag(`json:"deposited_at"`),
		field.Int("deposited_by_admin_id").Optional().Nillable().StructTag(`json:"deposited_by_admin_id"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_no").Unique(),
		index.Fields("customer_id", "created_at"),
		index.Fields("responsible_admin_id", "fulfillment_status"),
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

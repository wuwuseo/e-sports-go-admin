package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsCheckout 是「订单 / 充值 / 礼物」三类业务单的本站聚合收款入口：
// 一个业务单至多一条 checkout（business_type + business_id 唯一），入口码 entry_token 随机短期生成，
// 客户端扫码打开本站扫码页，再按微信 / 支付宝环境向服务商创建支付尝试。
//
// 金额与业务时限在创建时快照：checkout 只做入口与状态承载，真正的入账仍由
// applyVerifiedPayment（订单）/ settleRechargeIntent（充值）/ 礼物收款核销链路完成，
// 只有服务商验签回调或服务端查单确认成功才把 checkout 置 paid。
type EsportsCheckout struct{ ent.Schema }

func (EsportsCheckout) Fields() []ent.Field {
	return []ent.Field{
		// 业务类型：订单 / 充值 / 礼物。
		field.Enum("business_type").Values("order", "recharge", "gift").StructTag(`json:"business_type"`),
		// 业务单主键（订单 ID / 充值意图 ID / 礼物确认单 ID）。
		field.Int("business_id").Positive().StructTag(`json:"business_id"`),
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		// 应收金额快照（分）：创建时从业务单读取，后续业务单改价不允许继续用旧快照收款。
		field.Int64("amount_cents").Positive().StructTag(`json:"amount_cents"`),
		// 业务时限快照：订单支付时限 / 充值与礼物收款时限；超过即不能再创建新的支付尝试。
		field.Time("business_expires_at").StructTag(`json:"business_expires_at"`),
		// 收款模式：aggregate=服务商聚合动态支付 / manual=固定商家码 + 截图人工核验。
		// 两者在同一业务单上互斥，互斥判定由服务层与支付尝试的 active_dedup_key 共同保证。
		field.Enum("channel_mode").Values("aggregate", "manual").StructTag(`json:"channel_mode"`),
		// 入口状态：open 待支付 / paid 已确认到账 / expired 业务时限已过 / cancelled 业务已取消。
		field.Enum("status").Values("open", "paid", "expired", "cancelled").Default("open").StructTag(`json:"status"`),
		// 入口令牌：crypto/rand 随机十六进制，仅用于扫码页定位，不承载业务身份。
		field.String("entry_token").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"entry_token"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCheckout) Indexes() []ent.Index {
	return []ent.Index{
		// 一个业务单只允许一个聚合收款入口，重复创建按幂等返回既有入口。
		index.Fields("business_type", "business_id").Unique(),
		index.Fields("entry_token").Unique(),
		index.Fields("status"),
	}
}

// EsportsCheckoutAttempt 是一次向具体服务商发起的动态支付尝试。
// 「一个业务单同一时刻只允许一条有效支付尝试」由 active_dedup_key 唯一索引兜底：
// 尝试创建时写入 checkout:<checkout_id>，完成（paid / failed / expired）后清空为 NULL；
// MySQL / SQLite 的唯一索引都允许多个 NULL，因此历史尝试与其它 checkout 互不冲突
// （与 EsportsAssessment.active_dedup_key 同一模式，不用部分索引以兼容 MySQL 5.7）。
//
// 已受理（pending）的尝试不允许改投另一家服务商：服务商与商户单号在创建时固定；
// 只有在服务商受理前（创建失败）才允许按 priority 切换到下一家。
type EsportsCheckoutAttempt struct{ ent.Schema }

func (EsportsCheckoutAttempt) Fields() []ent.Field {
	return []ent.Field{
		field.Int("checkout_id").Positive().StructTag(`json:"checkout_id"`),
		// 服务商：收钱吧 / 汇付天下。
		field.Enum("provider").Values("shouqianba", "huifu").StructTag(`json:"provider"`),
		// 实际支付渠道：微信 / 支付宝（由扫码页按客户端环境选择）。
		field.Enum("pay_channel").Values("wechat", "alipay").StructTag(`json:"pay_channel"`),
		// 本站商户单号：对服务商下发的唯一订单号，超时重试必须换新号。
		field.String("merchant_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"merchant_no"`),
		// 服务商交易号：回调 / 查单确认后回填，和服务商组成唯一键，防止跨尝试复用同一笔交易。
		field.String("provider_trade_no").Optional().Nillable().MaxLen(128).StructTag(`json:"provider_trade_no,omitempty"`),
		// creating=已落库待服务商受理 / pending=服务商已受理 / paid=确认到账 /
		// failed=受理失败或异常到账转人工 / expired=超时未支付。
		field.Enum("status").Values("creating", "pending", "paid", "failed", "expired").
			Default("creating").StructTag(`json:"status"`),
		// 服务商返回的收银台地址（WAP / 跳转支付），可能为空。
		field.String("pay_url").MaxLen(1024).Default("").StructTag(`json:"pay_url"`),
		// 二维码内容（正扫模式），可能为空；客户端据此自行渲染二维码，避免服务端存图片。
		field.String("qr_payload").MaxLen(2048).Default("").StructTag(`json:"qr_payload"`),
		field.Time("expires_at").StructTag(`json:"expires_at"`),
		// 有效尝试唯一键：有效期内为 checkout:<id>，终态清空为 NULL（可空唯一）。
		field.String("active_dedup_key").Optional().Nillable().MaxLen(80).StructTag(`json:"active_dedup_key,omitempty"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCheckoutAttempt) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("merchant_no").Unique(),
		index.Fields("checkout_id", "status"),
		// 服务商交易号唯一；未回填为 NULL，不参与冲突（MySQL / SQLite 唯一索引均允许多个 NULL）。
		index.Fields("provider", "provider_trade_no").Unique(),
		index.Fields("active_dedup_key").Unique(),
	}
}

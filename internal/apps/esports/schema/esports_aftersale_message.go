package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsAfterSaleMessage 是售后单内的沟通记录（§13.3-04，只增不改）：
// 客服与客户在同一张售后单里来回回复，客户侧只能看到 visibility=all 的消息；
// 附件以引用地址列表存放，与既有 evidence_ref 口径一致。
//
// 建议表名：esports_after_sale_messages。
type EsportsAfterSaleMessage struct{ ent.Schema }

func (EsportsAfterSaleMessage) Hooks() []ent.Hook { return appendOnlyHooks() }

func (EsportsAfterSaleMessage) Fields() []ent.Field {
	return []ent.Field{
		field.Int("after_sale_id").Positive().StructTag(`json:"after_sale_id"`),
		// 发送方：客户 / 客服 / 陪玩 / 系统。
		field.Enum("author_type").Values("customer", "admin", "player", "system").StructTag(`json:"author_type"`),
		// 发送方 ID（客户 ID / 管理员 ID / 陪玩 ID）；system 为 0。
		field.Int("author_id").Default(0).StructTag(`json:"author_id"`),
		// 发送方显示名快照，避免后续改名影响历史沟通记录。
		field.String("author_name").MaxLen(120).Default("").StructTag(`json:"author_name"`),
		field.String("content").NotEmpty().MaxLen(1000).StructTag(`json:"content"`),
		// 附件引用地址（截图 / 凭证），与 evidence_ref 同口径。
		field.JSON("attachments", []string{}).Optional().StructTag(`json:"attachments"`),
		// 可见性：all 客户侧可见 / internal 仅客服侧可见（内部备注）。
		field.Enum("visibility").Values("all", "internal").Default("all").StructTag(`json:"visibility"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsAfterSaleMessage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("after_sale_id", "created_at"),
		index.Fields("author_type", "author_id"),
	}
}

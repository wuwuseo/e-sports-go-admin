package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsPriceAdjustment binds an independent approval to one exact order quote.
type EsportsPriceAdjustment struct{ ent.Schema }

func (EsportsPriceAdjustment) Fields() []ent.Field {
	return []ent.Field{
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.Int("order_version").Positive().StructTag(`json:"order_version"`),
		field.Int64("quantity").Positive().StructTag(`json:"quantity"`),
		field.Int64("manual_adjust_cents").StructTag(`json:"manual_adjust_cents"`),
		field.Int64("payable_cents").Positive().StructTag(`json:"payable_cents"`),
		field.Int("requested_by_admin_id").Positive().StructTag(`json:"requested_by_admin_id"`),
		field.String("request_key").NotEmpty().MaxLen(128).StructTag(`json:"request_key"`),
		field.String("reason").NotEmpty().MaxLen(255).StructTag(`json:"reason"`),
		field.Enum("status").Values("pending", "approved", "rejected", "consumed").Default("pending").StructTag(`json:"status"`),
		field.Int("reviewed_by_admin_id").Positive().Optional().Nillable().StructTag(`json:"reviewed_by_admin_id"`),
		field.String("review_note").MaxLen(255).Default("").StructTag(`json:"review_note"`),
		field.Time("reviewed_at").Optional().Nillable().StructTag(`json:"reviewed_at"`),
		field.Time("consumed_at").Optional().Nillable().StructTag(`json:"consumed_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsPriceAdjustment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("requested_by_admin_id", "request_key").Unique(),
		index.Fields("order_id", "order_version"),
		index.Fields("status", "created_at"),
	}
}

package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsConfirmation binds a verified customer action to one immutable quote.
type EsportsConfirmation struct{ ent.Schema }

func (EsportsConfirmation) Fields() []ent.Field {
	return []ent.Field{
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		field.String("subject_type").NotEmpty().MaxLen(48).StructTag(`json:"subject_type"`),
		field.Int("subject_id").Positive().StructTag(`json:"subject_id"`),
		field.Int("subject_version").Positive().StructTag(`json:"subject_version"`),
		field.Int("sku_id").Positive().StructTag(`json:"sku_id"`),
		field.Int64("quantity").Positive().StructTag(`json:"quantity"`),
		field.Int64("amount_cents").Positive().StructTag(`json:"amount_cents"`),
		field.String("channel").NotEmpty().MaxLen(48).StructTag(`json:"channel"`),
		field.String("evidence_ref").NotEmpty().MaxLen(255).StructTag(`json:"evidence_ref"`),
		field.Int("recorded_by_admin_id").Optional().Nillable().StructTag(`json:"recorded_by_admin_id"`),
		field.Int("reviewed_by_admin_id").Optional().Nillable().StructTag(`json:"reviewed_by_admin_id"`),
		field.Enum("status").Values("pending", "verified", "revoked").Default("pending").StructTag(`json:"status"`),
		field.Time("expires_at").StructTag(`json:"expires_at"`),
		field.Time("verified_at").Optional().Nillable().StructTag(`json:"verified_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsConfirmation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("subject_type", "subject_id", "subject_version", "customer_id", "channel").Unique(),
		index.Fields("customer_id", "created_at"),
	}
}

package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsAgeCheck keeps the evidence and independent review behind a customer's
// purchasing eligibility. Only an approved check may change age_status.
type EsportsAgeCheck struct{ ent.Schema }

func (EsportsAgeCheck) Fields() []ent.Field {
	return []ent.Field{
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		field.Int("requested_by_admin_id").Positive().StructTag(`json:"requested_by_admin_id"`),
		field.Enum("claimed_age_status").Values("adult", "minor").StructTag(`json:"claimed_age_status"`),
		field.Enum("verification_method").Values("staff_document_review").StructTag(`json:"verification_method"`),
		field.Int("evidence_attachment_id").Positive().StructTag(`json:"evidence_attachment_id"`),
		field.String("evidence_sha256").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"evidence_sha256"`),
		field.Enum("status").Values("pending", "approved", "rejected").Default("pending").StructTag(`json:"status"`),
		field.String("pending_key").Optional().Nillable().MaxLen(80).StructTag(`json:"-"`),
		field.Int("reviewed_by_admin_id").Optional().Nillable().StructTag(`json:"reviewed_by_admin_id"`),
		field.String("review_note").MaxLen(500).Default("").StructTag(`json:"review_note"`),
		field.Time("reviewed_at").Optional().Nillable().StructTag(`json:"reviewed_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsAgeCheck) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("pending_key").Unique(),
		index.Fields("customer_id", "created_at"),
		index.Fields("status", "created_at"),
	}
}

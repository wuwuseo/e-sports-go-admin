package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsGiftIntent records the exact gift terms a customer must approve before
// a staff member can debit their balance. Confirmation is consumed with payment.
type EsportsGiftIntent struct{ ent.Schema }

func (EsportsGiftIntent) Fields() []ent.Field {
	return []ent.Field{
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		field.Int("gift_id").Positive().StructTag(`json:"gift_id"`),
		field.String("gift_name").NotEmpty().MaxLen(120).StructTag(`json:"gift_name"`),
		field.Int("gift_version").Positive().StructTag(`json:"gift_version"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		field.String("player_name").NotEmpty().MaxLen(120).StructTag(`json:"player_name"`),
		field.Int("order_id").Optional().Nillable().StructTag(`json:"order_id"`),
		field.Int("quantity").Positive().StructTag(`json:"quantity"`),
		field.Int64("unit_price_cents").Positive().StructTag(`json:"unit_price_cents"`),
		field.Int64("amount_cents").Positive().StructTag(`json:"amount_cents"`),
		field.String("payment_source").Default("customer_balance_principal").StructTag(`json:"payment_source"`),
		field.Int("stored_value_card_id").Positive().Optional().Nillable().StructTag(`json:"stored_value_card_id"`),
		field.String("stored_value_card_no").MaxLen(64).Default("").StructTag(`json:"stored_value_card_no"`),
		field.Int("created_by_admin_id").Positive().StructTag(`json:"created_by_admin_id"`),
		field.String("client_key").NotEmpty().MaxLen(128).StructTag(`json:"client_key"`),
		field.Enum("status").Values("pending", "confirmed", "consumed", "cancelled").Default("pending").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.String("confirmation_ref").MaxLen(255).Default("").StructTag(`json:"confirmation_ref"`),
		field.Int("gift_order_id").Positive().Optional().Nillable().StructTag(`json:"gift_order_id"`),
		field.Time("expires_at").StructTag(`json:"expires_at"`),
		field.Time("confirmed_at").Optional().Nillable().StructTag(`json:"confirmed_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsGiftIntent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("created_by_admin_id", "client_key").Unique(),
		index.Fields("customer_id", "created_at"),
		index.Fields("status", "expires_at"),
	}
}

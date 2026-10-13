package schema

import (
	"context"
	"errors"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsLedgerEntry is append-only; corrections are new entries that cite the original source.
type EsportsLedgerEntry struct{ ent.Schema }

func (EsportsLedgerEntry) Hooks() []ent.Hook { return appendOnlyHooks() }

func (EsportsLedgerEntry) Fields() []ent.Field {
	return []ent.Field{
		field.String("account_type").NotEmpty().MaxLen(48).StructTag(`json:"account_type"`),
		field.String("account_id").NotEmpty().MaxLen(80).StructTag(`json:"account_id"`),
		field.String("source_type").NotEmpty().MaxLen(48).StructTag(`json:"source_type"`),
		field.String("source_id").NotEmpty().MaxLen(80).StructTag(`json:"source_id"`),
		field.String("entry_role").NotEmpty().MaxLen(48).StructTag(`json:"entry_role"`),
		field.Int64("delta_cents").StructTag(`json:"delta_cents"`),
		field.String("fund_batch").MaxLen(80).Default("").StructTag(`json:"fund_batch"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsLedgerEntry) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("source_type", "source_id", "entry_role").Unique(),
		index.Fields("account_type", "account_id", "created_at"),
	}
}

type EsportsIdempotency struct{ ent.Schema }

func (EsportsIdempotency) Hooks() []ent.Hook { return appendOnlyHooks() }

func (EsportsIdempotency) Fields() []ent.Field {
	return []ent.Field{
		field.String("operation").NotEmpty().MaxLen(80).StructTag(`json:"operation"`),
		field.String("principal").NotEmpty().MaxLen(100).StructTag(`json:"principal"`),
		field.String("request_key").NotEmpty().MaxLen(128).StructTag(`json:"request_key"`),
		field.String("request_hash").NotEmpty().MaxLen(64).StructTag(`json:"request_hash"`),
		field.String("result_ref").NotEmpty().MaxLen(100).StructTag(`json:"result_ref"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsIdempotency) Indexes() []ent.Index {
	return []ent.Index{index.Fields("operation", "principal", "request_key").Unique()}
}

type EsportsOutbox struct{ ent.Schema }

func (EsportsOutbox) Fields() []ent.Field {
	return []ent.Field{
		field.String("event_key").NotEmpty().MaxLen(120).StructTag(`json:"event_key"`),
		field.String("event_type").NotEmpty().MaxLen(80).StructTag(`json:"event_type"`),
		field.String("aggregate_ref").NotEmpty().MaxLen(100).StructTag(`json:"aggregate_ref"`),
		field.JSON("payload", map[string]interface{}{}).StructTag(`json:"payload"`),
		field.Enum("status").Values("pending", "processing", "delivered", "dead").Default("pending").StructTag(`json:"status"`),
		field.Int("attempts").Default(0).StructTag(`json:"attempts"`),
		field.Time("next_attempt_at").Default(time.Now).StructTag(`json:"next_attempt_at"`),
		field.Time("lease_until").Optional().Nillable().StructTag(`json:"lease_until"`),
		field.Time("delivered_at").Optional().Nillable().StructTag(`json:"delivered_at"`),
		field.String("last_error").MaxLen(500).Default("").StructTag(`json:"last_error"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsOutbox) Indexes() []ent.Index {
	return []ent.Index{index.Fields("event_key").Unique(), index.Fields("status", "next_attempt_at")}
}

func appendOnlyHooks() []ent.Hook {
	return []ent.Hook{func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if mutation.Op()&(ent.OpUpdate|ent.OpUpdateOne|ent.OpDelete|ent.OpDeleteOne) != 0 {
				return nil, errors.New("该业务记录只允许追加；更正必须新增冲正记录")
			}
			return next.Mutate(ctx, mutation)
		})
	}}
}

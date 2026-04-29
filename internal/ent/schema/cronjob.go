package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// CronJob holds the schema definition for the CronJob entity.
type CronJob struct {
	ent.Schema
}

// Fields of the CronJob.
func (CronJob) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("name").
			Unique().
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"name"`).
			Comment("任务名称"),
		field.String("description").
			Optional().
			MaxLen(500).
			StructTag(`json:"description,omitempty"`).
			Comment("描述"),
		field.Enum("type").
			Values("shell", "curl", "task").
			StructTag(`json:"type"`).
			Comment("任务类型"),
		field.String("expression").
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"expression"`).
			Comment("cron表达式"),
		field.String("command").
			MaxLen(2048).
			NotEmpty().
			StructTag(`json:"command"`).
			Comment("命令/配置/task名称"),
		field.Bool("enabled").
			Default(true).
			StructTag(`json:"enabled"`).
			Comment("是否启用"),
		field.Time("last_run_at").
			Optional().
			Nillable().
			StructTag(`json:"last_run_at,omitempty"`).
			Comment("上次执行时间"),
		field.Time("next_run_at").
			Optional().
			Nillable().
			StructTag(`json:"next_run_at,omitempty"`).
			Comment("下次执行时间"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			StructTag(`json:"created_at"`).
			Comment("创建时间"),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			StructTag(`json:"updated_at"`).
			Comment("更新时间"),
	}
}

// Indexes of the CronJob.
func (CronJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("enabled"),
		index.Fields("type"),
	}
}

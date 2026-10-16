package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"go-admin/internal/apps/esports/schematier"
)

// SeatSpec 定义在 schematier 包以避免与主 schema 包同名的生成代码导入冲突；此处以别名保留类型名。
type SeatSpec = schematier.SeatSpec

// EsportsSpecGroup 是多人规格的规格组（§15.5）：记录每个席位的角色、可替换条件与规格选项。
// 多人规格 SKU 通过 spec_group_id 引用它；报单时把 seats 快照进订单的 seat_roles。
// 席位数量即 len(seats)，须与引用它的 SKU 的 required_seats 一致。
type EsportsSpecGroup struct{ ent.Schema }

func (EsportsSpecGroup) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").NotEmpty().MaxLen(48).Immutable().StructTag(`json:"code"`),
		field.String("name").NotEmpty().MaxLen(120).StructTag(`json:"name"`),
		field.JSON("seats", []SeatSpec{}).StructTag(`json:"seats"`),
		field.Enum("status").Values("enabled", "disabled").Default("enabled").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsSpecGroup) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("code").Unique(),
		index.Fields("status"),
	}
}

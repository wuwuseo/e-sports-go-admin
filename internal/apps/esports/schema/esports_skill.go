package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"time"
)

// EsportsSkillLevel 是技能等级（陪玩等级，）配置。等级按 sort_order 升序
// 即从低到高（下标小 = 低档），顺序由管理端整体保存时重排；商品门槛 / 订单快照 /
// 陪玩档案只存 code 字符串，所有比较按保存当时的表序数进行，删档后的历史编码按
// fail-closed 口径处理（低于一切有效档，接不了任何已知门槛的单，见 checkPlayerGameLevel）。
type EsportsSkillLevel struct{ ent.Schema }

func (EsportsSkillLevel) Fields() []ent.Field {
	return []ent.Field{
		// 编码落库到商品门槛 / 订单快照 / 陪玩档案：小写字母开头的字母数字下划线（≤32），
		// 格式校验在业务层 saveSkillLevels；保存后允许改名不改码，改码等于换档。
		field.String("code").NotEmpty().MaxLen(32).StructTag(`json:"code"`),
		// 显示名仅作展示（≤32 字）。
		field.String("name").NotEmpty().MaxLen(32).StructTag(`json:"name"`),
		// 等级顺序：升序即低到高，管理端用上移 / 下移调整后整体保存。
		field.Int("sort_order").Default(0).StructTag(`json:"sort_order"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsSkillLevel) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("code").Unique(),
		index.Fields("sort_order"),
	}
}

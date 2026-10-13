package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsPageDocument 是某渠道首页装修的当前工作副本（§16）。一个渠道只有一行：
// draft_config 为运营正在编辑的结构化配置，published_version_no 指向当前生效的不可变快照。
// draft_version 作乐观锁，避免多人同时保存草稿互相覆盖。
// ponytail: 首版仅支持 uni-app 客户自助端单渠道 uni_customer_home；渠道字段保留以便后续扩展。
type EsportsPageDocument struct{ ent.Schema }

func (EsportsPageDocument) Fields() []ent.Field {
	return []ent.Field{
		field.String("channel").NotEmpty().MaxLen(48).StructTag(`json:"channel"`),
		// 结构化页面配置（组件数组），只决定展示位置、样式与筛选规则，不含资金数据。
		field.JSON("draft_config", map[string]interface{}{}).StructTag(`json:"draft_config"`),
		field.Int("draft_version").Default(1).StructTag(`json:"draft_version"`),
		field.Int("published_version_no").Default(0).StructTag(`json:"published_version_no"`),
		// draft 有未发布草稿 / pending 已排定时发布 / published 草稿已发布且无待发布。
		field.Enum("status").Values("draft", "pending", "published").Default("draft").StructTag(`json:"status"`),
		field.Int("updated_by").Default(0).StructTag(`json:"updated_by"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsPageDocument) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel").Unique(),
	}
}

// EsportsPageVersion 是首页装修的不可变发布快照（§16.4）。发布 / 回滚均生成新版本，
// 不原地修改旧记录；同一渠道同一时刻只有一条 status=published，其余历史版本转 history。
// 定时发布先落 status=scheduled，由系统任务到点激活。
type EsportsPageVersion struct{ ent.Schema }

func (EsportsPageVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int("document_id").StructTag(`json:"document_id"`),
		field.String("channel").NotEmpty().MaxLen(48).StructTag(`json:"channel"`),
		field.Int("version_no").StructTag(`json:"version_no"`),
		field.JSON("config", map[string]interface{}{}).StructTag(`json:"config"`),
		// scheduled 定时待发布 / published 当前生效 / history 历史。
		field.Enum("status").Values("scheduled", "published", "history").Default("scheduled").StructTag(`json:"status"`),
		field.Enum("publish_mode").Values("immediate", "scheduled").Default("immediate").StructTag(`json:"publish_mode"`),
		field.Time("scheduled_at").Optional().Nillable().StructTag(`json:"scheduled_at"`),
		field.Time("published_at").Optional().Nillable().StructTag(`json:"published_at"`),
		field.Int("published_by").Default(0).StructTag(`json:"published_by"`),
		// 回滚来源版本号，非回滚为 0。
		field.Int("rollback_from_version_no").Default(0).StructTag(`json:"rollback_from_version_no"`),
		field.String("note").Default("").MaxLen(255).StructTag(`json:"note"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsPageVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("document_id", "version_no").Unique(),
		index.Fields("status", "scheduled_at"),
	}
}

// EsportsPageAsset 是装修素材库条目（§16.2）。只登记受控资源地址与尺寸，
// 不做字节存储与裁剪。
// ponytail: 上传 / 裁剪 / 外部域名白名单强校验未实现，仅登记名称、地址与尺寸。
type EsportsPageAsset struct{ ent.Schema }

func (EsportsPageAsset) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(160).StructTag(`json:"name"`),
		field.String("url").NotEmpty().MaxLen(512).StructTag(`json:"url"`),
		field.String("mime").Default("").MaxLen(80).StructTag(`json:"mime"`),
		field.Int64("size_bytes").Default(0).StructTag(`json:"size_bytes"`),
		field.Int("width").Default(0).StructTag(`json:"width"`),
		field.Int("height").Default(0).StructTag(`json:"height"`),
		field.Int("created_by").Default(0).StructTag(`json:"created_by"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

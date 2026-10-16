package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsReportExport 是 §17 报表导出的只增审计表：记录谁在什么时间、以什么筛选条件
// 导出了哪份报表，以及导出结果的行数与文件引用 / 摘要。审计独立于管理端操作日志，
// 不因操作日志被清理而丢失留痕；更正一律追加新记录，不允许修改或删除历史行。
type EsportsReportExport struct{ ent.Schema }

func (EsportsReportExport) Hooks() []ent.Hook { return appendOnlyHooks() }

func (EsportsReportExport) Fields() []ent.Field {
	return []ent.Field{
		// 报表类型：owner_overview / cs_performance / dispatch_efficiency / aftersale_quality / finance。
		field.String("report_type").NotEmpty().MaxLen(48).Immutable().StructTag(`json:"report_type"`),
		// 导出格式，当前仅 csv。
		field.String("format").NotEmpty().MaxLen(16).Default("csv").Immutable().StructTag(`json:"format"`),
		// 导出人（管理端账号）。
		field.Int("admin_id").Positive().Immutable().StructTag(`json:"admin_id"`),
		// 导出人展示名快照（昵称优先，回落用户名），避免账号删除后无法定位。
		field.String("admin_name").MaxLen(120).Default("").Immutable().StructTag(`json:"admin_name"`),
		// 筛选参数快照（含时间区间、各维度筛选与收敛后的数据边界）。
		field.JSON("filter_snapshot", map[string]any{}).Optional().StructTag(`json:"filter_snapshot"`),
		// 导出结果的数据行数。
		field.Int("row_count").Default(0).Immutable().StructTag(`json:"row_count"`),
		// 结果文件引用：导出文件名的确定性引用。
		field.String("file_name").NotEmpty().MaxLen(255).Immutable().StructTag(`json:"file_name"`),
		// 结果摘要：导出文件内容的 SHA-256 十六进制摘要，供完整性校验。
		field.String("checksum").MaxLen(128).Default("").Immutable().StructTag(`json:"checksum"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsReportExport) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("report_type", "created_at"),
		index.Fields("admin_id", "created_at"),
	}
}

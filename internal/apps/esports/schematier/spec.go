package schematier

// SeatSpec 是多人规格中单个席位的约束定义（§15.5）。
// 规格组用它定义每个席位的角色与可替换条件；订单报单时把同一结构快照进 seat_roles，
// 供多人规格报价展示席位约束。定义在 schematier 包以沿用既有的 JSON 字段自定义类型约定。
type SeatSpec struct {
	// SeatIndex 席位序号，从 0 开始，对应 required_seats 展开后的第几席。
	SeatIndex int `json:"seat_index"`
	// Role 该席位需要的陪玩角色（如 坦克 / 输出 / 辅助 / 教练）。
	Role string `json:"role"`
	// Substitutable 该席位是否允许替换（可替换条件）。
	Substitutable bool `json:"substitutable"`
	// PromisedUnits 该席位承诺的服务量（§15.5-01 / §5.3-05）：多人规格固定每席承诺量，
	// 报单时随 seat_roles 快照进订单；0 表示该席位未单独配置，回落到规格组 / SKU 的每席量。
	// 新增字段对存量 JSON 快照向后兼容（旧数据反序列化得 0，不回填）。
	PromisedUnits int64 `json:"promised_units"`
	// Spec 该席位的规格选项 / 要求描述（如「星耀以上」「女性陪玩」）。
	Spec string `json:"spec"`
}

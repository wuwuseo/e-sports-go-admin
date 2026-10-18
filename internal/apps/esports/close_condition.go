package esports

import (
	"context"
	"fmt"
	"math"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsorderaddon"
	"go-admin/internal/ent/esportsservicesegment"

	"go-admin/internal/apps/esports/schematier"
)

// 结单条件自定义（§3.4）：字段 + 运算符 + 值，支持 AND / OR / NOT 嵌套。
// 结单执行（closeOrder）与「结单按钮是否可点」共用同一套求值（checkCloseConditions），
// 保证条件不满足时既拦截结单、也能在前端把按钮置灰。
// 规则未配置条件树时，把结构化条件项（凭证 ≥N、时长为零、无未处理加钟 / 售后）折叠为等价条件树，
// 两条路径共用同一求值实现，行为与历史默认口径一致。

const (
	closeCondMaxDepth = 5
	closeCondMaxNodes = 50
	closeCondOpAnd    = "and"
	closeCondOpOr     = "or"
	closeCondOpNot    = "not"
)

// closeConditionFieldSpec 条件字段元信息（供校验与前端枚举共用）。
type closeConditionFieldSpec struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Kind：count 计数 / seconds 秒 / units 服务量 / bool 布尔（1 表示成立）。
	Kind string `json:"kind"`
}

var closeConditionFieldSpecs = []closeConditionFieldSpec{
	{Key: "evidence_count", Label: "服务凭证张数", Kind: "count"},
	{Key: "service_seconds", Label: "系统服务时长（秒）", Kind: "seconds"},
	{Key: "service_units", Label: "成员上报服务量", Kind: "units"},
	{Key: "promised_service_units", Label: "承诺服务量", Kind: "units"},
	{Key: "addon_units", Label: "加钟量", Kind: "units"},
	{Key: "pending_addon_count", Label: "未处理加钟数（待确认/未收款）", Kind: "count"},
	{Key: "open_aftersale_count", Label: "进行中售后数", Kind: "count"},
	{Key: "stored_value_consistent", Label: "储值卡核销一致", Kind: "bool"},
}

var closeConditionOperators = []string{">=", ">", "<=", "<", "==", "!="}

func closeConditionFieldSpecOf(key string) (closeConditionFieldSpec, bool) {
	for _, spec := range closeConditionFieldSpecs {
		if spec.Key == key {
			return spec, true
		}
	}
	return closeConditionFieldSpec{}, false
}

func validCloseConditionOperator(operator string) bool {
	for _, value := range closeConditionOperators {
		if value == operator {
			return true
		}
	}
	return false
}

// isCloseConditionTreeConfigured 判断条件树是否已配置（零值表示未配置）。
func isCloseConditionTreeConfigured(node schematier.CloseConditionNode) bool {
	return strings.TrimSpace(node.Op) != "" || strings.TrimSpace(node.Field) != ""
}

// normalizeCloseConditionTree 归一化并校验条件树：限制深度与节点数、not 仅允许单子节点、
// 叶子必须是合法字段 + 运算符 + 有限数值。未配置返回 nil, nil。
func normalizeCloseConditionTree(node schematier.CloseConditionNode) (*schematier.CloseConditionNode, error) {
	if !isCloseConditionTreeConfigured(node) {
		return nil, nil
	}
	normalized, count, err := normalizeCloseConditionNode(node, 1)
	if err != nil {
		return nil, err
	}
	if count > closeCondMaxNodes {
		return nil, errInvalidInput
	}
	return &normalized, nil
}

func normalizeCloseConditionNode(node schematier.CloseConditionNode, depth int) (schematier.CloseConditionNode, int, error) {
	if depth > closeCondMaxDepth {
		return node, 0, errInvalidInput
	}
	op := strings.ToLower(strings.TrimSpace(node.Op))
	field := strings.TrimSpace(node.Field)
	operator := strings.TrimSpace(node.Operator)
	// 组合节点：op + children。
	if field == "" && operator == "" && node.Value == nil {
		if op != closeCondOpAnd && op != closeCondOpOr && op != closeCondOpNot {
			return node, 0, errInvalidInput
		}
		if len(node.Children) == 0 {
			return node, 0, errInvalidInput
		}
		if op == closeCondOpNot && len(node.Children) != 1 {
			return node, 0, errInvalidInput
		}
		children := make([]schematier.CloseConditionNode, 0, len(node.Children))
		total := 1
		for _, child := range node.Children {
			normalized, count, err := normalizeCloseConditionNode(child, depth+1)
			if err != nil {
				return node, 0, err
			}
			total += count
			children = append(children, normalized)
		}
		return schematier.CloseConditionNode{Op: op, Children: children}, total, nil
	}
	// 叶子节点：field + operator + value。
	if op != "" || len(node.Children) > 0 {
		return node, 0, errInvalidInput
	}
	if _, ok := closeConditionFieldSpecOf(field); !ok || !validCloseConditionOperator(operator) ||
		node.Value == nil || math.IsNaN(*node.Value) || math.IsInf(*node.Value, 0) {
		return node, 0, errInvalidInput
	}
	value := *node.Value
	return schematier.CloseConditionNode{Field: field, Operator: operator, Value: &value}, 1, nil
}

// closeConditionFacts 结单条件求值所需的订单事实快照。
type closeConditionFacts struct {
	EvidenceCount        int64
	ServiceSeconds       int64
	ServiceUnits         int64
	PromisedServiceUnits int64
	AddonUnits           int64
	PendingAddonCount    int64
	OpenAfterSaleCount   int64
	StoredValueOK        bool
}

// closeConditionFactsOf 汇总订单事实：服务段凭证 / 时长 / 服务量、未处理加钟、进行中售后与储值卡一致性。
func closeConditionFactsOf(ctx context.Context, db *ent.Client, order *ent.EsportsOrder) (closeConditionFacts, error) {
	facts := closeConditionFacts{
		PromisedServiceUnits: order.PromisedServiceUnits,
		AddonUnits:           order.AddonUnits,
	}
	segments, err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(order.ID),
	).All(ctx)
	if err != nil {
		return facts, err
	}
	for _, segment := range segments {
		if strings.TrimSpace(segment.EvidenceRef) != "" {
			facts.EvidenceCount++
		}
		facts.ServiceSeconds += segment.SystemSeconds
		facts.ServiceUnits += segment.ReportedUnits
	}
	// 未处理加钟：待陪玩确认，或已确认但尚未收款（有应收金额）。已确认未收款的加钟
	// 不能随结单被当作已结算，须先收款或撤销。
	pendingAddons, err := db.EsportsOrderAddon.Query().Where(
		esportsorderaddon.OrderIDEQ(order.ID),
		esportsorderaddon.Or(
			esportsorderaddon.StatusEQ(esportsorderaddon.StatusPending),
			esportsorderaddon.And(
				esportsorderaddon.StatusEQ(esportsorderaddon.StatusConfirmed),
				esportsorderaddon.PaymentStatusEQ(esportsorderaddon.PaymentStatusUnpaid),
				esportsorderaddon.AddedCentsGT(0),
			),
		),
	).Count(ctx)
	if err != nil {
		return facts, err
	}
	facts.PendingAddonCount = int64(pendingAddons)
	openAfterSales, err := db.EsportsAfterSale.Query().Where(
		esportsaftersale.SourceTypeEQ("order"),
		esportsaftersale.SourceIDEQ(order.ID),
		esportsaftersale.StatusIn(activeAfterSaleStatuses()...),
	).Count(ctx)
	if err != nil {
		return facts, err
	}
	facts.OpenAfterSaleCount = int64(openAfterSales)
	facts.StoredValueOK = storedValueOrderConsistent(ctx, db, order) == nil
	return facts, nil
}

// closeConditionActual 取事实字段的当前值（布尔成立为 1，否则为 0）。
func closeConditionActual(facts closeConditionFacts, field string) float64 {
	switch field {
	case "evidence_count":
		return float64(facts.EvidenceCount)
	case "service_seconds":
		return float64(facts.ServiceSeconds)
	case "service_units":
		return float64(facts.ServiceUnits)
	case "promised_service_units":
		return float64(facts.PromisedServiceUnits)
	case "addon_units":
		return float64(facts.AddonUnits)
	case "pending_addon_count":
		return float64(facts.PendingAddonCount)
	case "open_aftersale_count":
		return float64(facts.OpenAfterSaleCount)
	case "stored_value_consistent":
		if facts.StoredValueOK {
			return 1
		}
		return 0
	}
	return 0
}

func compareCloseCondition(actual float64, operator string, expected float64) bool {
	switch operator {
	case ">=":
		return actual >= expected
	case ">":
		return actual > expected
	case "<=":
		return actual <= expected
	case "<":
		return actual < expected
	case "==":
		return actual == expected
	case "!=":
		return actual != expected
	}
	return false
}

// closeConditionItem 单个条件项的求值结果（供前端逐项展示与结单失败原因拼装）。
type closeConditionItem struct {
	Field     string  `json:"field"`
	Label     string  `json:"label"`
	Operator  string  `json:"operator"`
	Expected  float64 `json:"expected"`
	Actual    float64 `json:"actual"`
	Satisfied bool    `json:"satisfied"`
	Reason    string  `json:"reason"`
}

func newCloseConditionItem(field, operator string, expected, actual float64) closeConditionItem {
	spec, _ := closeConditionFieldSpecOf(field)
	item := closeConditionItem{
		Field: field, Label: spec.Label, Operator: operator,
		Expected: expected, Actual: actual,
		Satisfied: compareCloseCondition(actual, operator, expected),
	}
	if !item.Satisfied {
		item.Reason = fmt.Sprintf("%s 需 %s %g，当前 %g", spec.Label, operator, expected, actual)
	}
	return item
}

// evaluateCloseConditionTree 递归求值条件树，并收集全部叶子项结果（便于展示未满足原因）。
func evaluateCloseConditionTree(node schematier.CloseConditionNode, facts closeConditionFacts) (bool, []closeConditionItem) {
	if strings.TrimSpace(node.Field) != "" && node.Value != nil {
		item := newCloseConditionItem(node.Field, node.Operator,
			*node.Value, closeConditionActual(facts, node.Field))
		return item.Satisfied, []closeConditionItem{item}
	}
	op := strings.ToLower(strings.TrimSpace(node.Op))
	switch op {
	case closeCondOpNot:
		ok, items := evaluateCloseConditionTree(node.Children[0], facts)
		if len(items) == 1 {
			items[0].Satisfied = !items[0].Satisfied
			items[0].Reason = ""
			if !items[0].Satisfied {
				items[0].Reason = fmt.Sprintf("%s 不应满足 %s %g，当前 %g",
					items[0].Label, items[0].Operator, items[0].Expected, items[0].Actual)
			}
		}
		return !ok, items
	case closeCondOpOr:
		satisfied := false
		items := make([]closeConditionItem, 0, len(node.Children))
		for _, child := range node.Children {
			ok, childItems := evaluateCloseConditionTree(child, facts)
			items = append(items, childItems...)
			if ok {
				satisfied = true
			}
		}
		return satisfied, items
	default: // and（含未指定 op 的多子节点兜底）
		satisfied := true
		items := make([]closeConditionItem, 0, len(node.Children))
		for _, child := range node.Children {
			ok, childItems := evaluateCloseConditionTree(child, facts)
			items = append(items, childItems...)
			if !ok {
				satisfied = false
			}
		}
		return satisfied, items
	}
}

func closeConditionLeaf(field, operator string, expected float64) schematier.CloseConditionNode {
	value := expected
	return schematier.CloseConditionNode{Field: field, Operator: operator, Value: &value}
}

// structuredCloseConditionTree 把结构化条件项折叠为等价条件树（§3.4 默认项），
// 使「未配置条件树」与「配置条件树」走同一套求值逻辑。无条件项时返回 nil（视为无条件可结单）。
func structuredCloseConditionTree(conds closeConditions) *schematier.CloseConditionNode {
	var children []schematier.CloseConditionNode
	if conds.MinEvidence >= 1 {
		children = append(children, closeConditionLeaf("evidence_count", ">=", float64(conds.MinEvidence)))
	}
	if conds.RequireDuration {
		children = append(children, schematier.CloseConditionNode{
			Op: closeCondOpOr,
			Children: []schematier.CloseConditionNode{
				closeConditionLeaf("service_seconds", ">", 0),
				closeConditionLeaf("service_units", ">", 0),
			},
		})
	}
	if conds.RequireNoPendingAddon {
		children = append(children, closeConditionLeaf("pending_addon_count", "==", 0))
	}
	if conds.RequireNoOpenAftersale {
		children = append(children, closeConditionLeaf("open_aftersale_count", "==", 0))
	}
	if len(children) == 0 {
		return nil
	}
	return &schematier.CloseConditionNode{Op: closeCondOpAnd, Children: children}
}

// closeConditionStatus 结单条件求值结果：供结单执行拦截与前端结单按钮置灰共用。
type closeConditionStatus struct {
	Satisfied            bool                      `json:"satisfied"`
	TreeConfigured       bool                      `json:"tree_configured"`
	RuleCode             string                    `json:"rule_code"`
	RuleVersion          int                       `json:"rule_version"`
	ReviewThresholdCents int64                     `json:"review_threshold_cents"`
	Items                []closeConditionItem      `json:"items"`
	Reasons              []string                  `json:"reasons"`
	Fields               []closeConditionFieldSpec `json:"fields"`
	Operators            []string                  `json:"operators"`
}

// orderCloseConditionStatus 读取订单当前结单条件状态（供结单按钮可用性与逐项提示）。
func orderCloseConditionStatus(ctx context.Context, db *ent.Client, orderID int) (closeConditionStatus, error) {
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return closeConditionStatus{}, err
	}
	rule, err := resolveCloseRule(ctx, db, order)
	if err != nil {
		return closeConditionStatus{}, err
	}
	return checkCloseConditions(ctx, db, order, rule)
}

// checkCloseConditions 按订单生效的结单规则求值结单条件（§3.4）。
// 规则配置了条件树时用条件树；否则折叠结构化条件项。储值卡核销一致性为硬性条件，仍在结单链路单独校验。
func checkCloseConditions(ctx context.Context, db *ent.Client, order *ent.EsportsOrder,
	rule *ent.EsportsCloseRule) (closeConditionStatus, error) {
	conds := effectiveCloseConditions(rule)
	status := closeConditionStatus{
		Satisfied: true, ReviewThresholdCents: conds.ReviewThresholdCents,
		Fields: closeConditionFieldSpecs, Operators: closeConditionOperators,
	}
	tree := structuredCloseConditionTree(conds)
	if rule != nil {
		status.RuleCode = rule.Code
		status.RuleVersion = rule.Version
		if isCloseConditionTreeConfigured(rule.ConditionTree) {
			normalized, err := normalizeCloseConditionTree(rule.ConditionTree)
			if err != nil {
				return status, err
			}
			tree = normalized
			status.TreeConfigured = true
		}
	}
	if tree == nil {
		return status, nil
	}
	facts, err := closeConditionFactsOf(ctx, db, order)
	if err != nil {
		return status, err
	}
	satisfied, items := evaluateCloseConditionTree(*tree, facts)
	status.Satisfied = satisfied
	status.Items = items
	for _, item := range items {
		if !item.Satisfied {
			status.Reasons = append(status.Reasons, item.Reason)
		}
	}
	return status, nil
}

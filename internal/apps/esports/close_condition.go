package esports

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsorderaddon"
	"go-admin/internal/ent/esportsservicesegment"

	"go-admin/internal/apps/esports/schematier"
)

// 结单条件自定义：字段 + 运算符 + 值，支持 AND / OR / NOT 嵌套。
// 结单执行（closeOrder）与「结单按钮是否可点」共用同一套求值（checkCloseConditions），
// 保证条件不满足时既拦截结单、也能在前端把按钮置灰。
// 结构化条件项（凭证 ≥N、服务时长 / 服务量不为零、无未处理加钟 / 售后）折叠为
// structuredCloseConditionTree，它是**不可被自定义条件树替换的默认底线**：
// 规则配置了 condition_tree 时，默认树与自定义树按 AND 合并求值（默认树为 nil 时只用自定义树，
// 自定义树未配置时只用默认树），管理员无法用自定义条件树整条删掉默认底线。
// 「服务凭证张数」（evidence_count）按订单全部服务段 evidence_ref 中的图片附件张数统计：
// JSON 数组形态按附件 ID 去重后计数，非空的历史文本引用整段按 1 张计。

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
}

// closeConditionFactsOf 汇总订单事实：服务段凭证 / 时长 / 服务量、未处理加钟与进行中售后。
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
	// 凭证张数按图片计：JSON 数组形态按附件 ID 去重后计数，历史文本引用整段按 1 张计。
	facts.EvidenceCount = closeConditionEvidenceCount(segments)
	for _, segment := range segments {
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
	return facts, nil
}

// closeConditionEvidenceCount 统计订单全部服务段的「服务凭证张数」（evidence_count）：
// 与 serviceEvidenceAttachmentIDs 同一解析口径，JSON 数组形态（如 [11,12]）按其中的附件 ID
// 去重后计张数（同一段传两张图算 2 张，跨段重复的 ID 只算 1 张）；
// 非空但不是合法 JSON 数组的历史文本引用（如 shot-1 / backfill:xxx）整段按 1 张计，
// 保证存量单不会因新口径突然变成 0；空引用（含空 JSON 数组）不计。
func closeConditionEvidenceCount(segments []*ent.EsportsServiceSegment) int64 {
	seen := map[int]bool{}
	var textCount int64
	for _, segment := range segments {
		ref := strings.TrimSpace(segment.EvidenceRef)
		if ref == "" {
			continue
		}
		if strings.HasPrefix(ref, "[") {
			var ids []int
			if err := json.Unmarshal([]byte(ref), &ids); err == nil {
				for _, id := range ids {
					if id > 0 {
						seen[id] = true
					}
				}
				continue
			}
		}
		textCount++
	}
	return int64(len(seen)) + textCount
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

// structuredCloseConditionTree 把结构化条件项折叠为等价条件树（默认项），
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

// mergeCloseConditionTrees 把默认底线树与自定义条件树按 AND 合并：
// 任一侧为 nil 时返回另一侧（默认树无条件项 / 规则未配置条件树，行为与历史一致），
// 两棵都在时包一个 and 节点，求值仍会递归收集两侧全部叶子，条件状态逐项明细不丢。
func mergeCloseConditionTrees(defaultTree, customTree *schematier.CloseConditionNode) *schematier.CloseConditionNode {
	if defaultTree == nil {
		return customTree
	}
	if customTree == nil {
		return defaultTree
	}
	return &schematier.CloseConditionNode{
		Op:       closeCondOpAnd,
		Children: []schematier.CloseConditionNode{*defaultTree, *customTree},
	}
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
	// ：结单前必须逐张查看的服务段凭证附件 ID（陪玩上传的图片）。
	// 结单接口须原样回传覆盖全部 ID 的 viewed_evidence_attachment_ids，缺一即参数错误。
	EvidenceAttachmentIDs []int `json:"evidence_attachment_ids"`
}

// serviceEvidenceAttachmentIDs 汇总订单全部服务段 evidence_ref 中的凭证附件 ID，
// 升序去重返回。仅解析 JSON 数组形态（陪玩上传的图片附件 ID）；历史文本型凭证（如 oss://...）
// 解析不出附件，不纳入「已查看」强制口径，避免存量单被新规则追溯拦截。
func serviceEvidenceAttachmentIDs(ctx context.Context, db *ent.Client, orderID int) ([]int, error) {
	if orderID < 1 {
		return nil, errInvalidInput
	}
	segments, err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
	).Select(esportsservicesegment.FieldEvidenceRef).All(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[int]bool{}
	for _, segment := range segments {
		ref := strings.TrimSpace(segment.EvidenceRef)
		if ref == "" || !strings.HasPrefix(ref, "[") {
			continue
		}
		var ids []int
		if err := json.Unmarshal([]byte(ref), &ids); err != nil {
			continue
		}
		for _, id := range ids {
			if id > 0 {
				seen[id] = true
			}
		}
	}
	if len(seen) == 0 {
		return []int{}, nil
	}
	out := make([]int, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Ints(out)
	return out, nil
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

// checkCloseConditions 按订单生效的结单规则求值结单条件。
// 默认结构化条件树（structuredCloseConditionTree）是硬性底线，永远参与求值；
// 规则配置了 condition_tree 时与其按 AND 合并（TreeConfigured 仍表示「规则是否配置了条件树」），
// 因此自定义条件树只能加严、不能删掉「有凭证」「服务量不为零」这类默认底线。
// 合并后逐项 Items / Reasons 仍列出全部叶子结果，供前端逐项展示与结单失败文案使用。
func checkCloseConditions(ctx context.Context, db *ent.Client, order *ent.EsportsOrder,
	rule *ent.EsportsCloseRule) (closeConditionStatus, error) {
	conds := effectiveCloseConditions(rule)
	status := closeConditionStatus{
		Satisfied: true, ReviewThresholdCents: conds.ReviewThresholdCents,
		Fields: closeConditionFieldSpecs, Operators: closeConditionOperators,
	}
	// 默认底线树：由生效规则的结构化条件项折叠而来，无任何默认项时为 nil。
	tree := structuredCloseConditionTree(conds)
	// ：无论条件树是否配置，都要给出「结单前必须查看」的服务段凭证附件 ID。
	evidenceIDs, err := serviceEvidenceAttachmentIDs(ctx, db, order.ID)
	if err != nil {
		return status, err
	}
	status.EvidenceAttachmentIDs = evidenceIDs
	if rule != nil {
		status.RuleCode = rule.Code
		status.RuleVersion = rule.Version
		if isCloseConditionTreeConfigured(rule.ConditionTree) {
			normalized, err := normalizeCloseConditionTree(rule.ConditionTree)
			if err != nil {
				return status, err
			}
			// 默认底线不可被替换：与自定义树 AND 合并后一起求值。
			tree = mergeCloseConditionTrees(tree, normalized)
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

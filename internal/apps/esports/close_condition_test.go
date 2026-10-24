package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportscloserule"
	"go-admin/internal/ent/esportsservicesegment"

	"go-admin/internal/apps/esports/schematier"
)

func num(value float64) *float64 { return &value }

func closeLeaf(field, operator string, value float64) schematier.CloseConditionNode {
	return schematier.CloseConditionNode{Field: field, Operator: operator, Value: num(value)}
}

// 条件树校验：字段 / 运算符 / 值 / 组合结构 / not 子节点数 / 深度受限，非法结构一律拒绝。
func TestCloseConditionTreeValidation(t *testing.T) {
	valid := schematier.CloseConditionNode{Op: "and", Children: []schematier.CloseConditionNode{
		closeLeaf("evidence_count", ">=", 1),
		{Op: "or", Children: []schematier.CloseConditionNode{
			closeLeaf("service_seconds", ">", 0),
			closeLeaf("service_units", ">", 0),
		}},
	}}
	if normalized, err := normalizeCloseConditionTree(valid); err != nil || normalized == nil {
		t.Fatalf("合法条件树应通过：%v", err)
	}
	// 未配置（零值）视为无条件树。
	if normalized, err := normalizeCloseConditionTree(schematier.CloseConditionNode{}); err != nil || normalized != nil {
		t.Fatalf("空条件树应视为未配置：%v %+v", err, normalized)
	}
	// 超过最大深度的 and 链。
	deep := closeLeaf("evidence_count", ">=", 1)
	for i := 0; i < closeCondMaxDepth+1; i++ {
		deep = schematier.CloseConditionNode{Op: "and", Children: []schematier.CloseConditionNode{deep}}
	}
	cases := []struct {
		name string
		node schematier.CloseConditionNode
	}{
		{"未知字段", closeLeaf("unknown_field", ">=", 1)},
		{"未知运算符", closeLeaf("evidence_count", "like", 1)},
		{"缺值", schematier.CloseConditionNode{Field: "evidence_count", Operator: ">="}},
		{"空组合", schematier.CloseConditionNode{Op: "and"}},
		{"未知组合", schematier.CloseConditionNode{Op: "xor", Children: []schematier.CloseConditionNode{closeLeaf("evidence_count", ">=", 1)}}},
		{"not 多子节点", schematier.CloseConditionNode{Op: "not", Children: []schematier.CloseConditionNode{
			closeLeaf("evidence_count", ">=", 1), closeLeaf("service_seconds", ">", 0),
		}}},
		{"叶子混组合", schematier.CloseConditionNode{Op: "and", Field: "evidence_count",
			Operator: ">=", Value: num(1), Children: []schematier.CloseConditionNode{closeLeaf("service_seconds", ">", 0)}}},
		{"超出深度", deep},
	}
	for _, item := range cases {
		if _, err := normalizeCloseConditionTree(item.node); !errors.Is(err, errInvalidInput) {
			t.Fatalf("%s 应被拒绝，err=%v", item.name, err)
		}
	}
	// 创建规则时同样拦截非法条件树。
	db := openCloseRuleDB(t, "esports_close_condition_validate")
	defer db.Close()
	bad := closeLeaf("unknown_field", ">=", 1)
	if _, err := createCloseRule(t.Context(), db, closeRuleInput{
		Code: "BAD-TREE", Name: "非法条件", Scope: "global", MinEvidence: 1, ConditionTree: &bad,
	}, 11); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法条件树应在创建时被拒，err=%v", err)
	}
}

// AND/OR 嵌套条件树：凭证 ≥1 且（系统时长 ≥600 秒 或 上报服务量 ≥2）；
// 该规则同时配置了默认结构化条件项，求值时自定义树与默认底线按 AND 合并（不能被替换）；
// 条件未满足时结单被拦截、条件状态接口同步给出逐项明细（供结单按钮置灰）。
func TestCloseConditionTreeAndOrEvaluation(t *testing.T) {
	db := openCloseRuleDB(t, "esports_close_condition_tree")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	tree := schematier.CloseConditionNode{Op: "and", Children: []schematier.CloseConditionNode{
		closeLeaf("evidence_count", ">=", 1),
		{Op: "or", Children: []schematier.CloseConditionNode{
			closeLeaf("service_seconds", ">=", 600),
			closeLeaf("service_units", ">=", 2),
		}},
	}}
	rule, err := createCloseRule(ctx, db, closeRuleInput{
		Code: "TREE-AND-OR", Name: "通用 AND/OR 条件", Scope: "global", MinEvidence: 1,
		RequireDuration: true, RequireNoPendingAddon: true, RequireNoOpenAftersale: true,
		ConditionTree: &tree,
	}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if rule.ConditionTree.Op != "and" || len(rule.ConditionTree.Children) != 2 {
		t.Fatalf("条件树未落库：%+v", rule.ConditionTree)
	}
	if _, err = activateCloseRule(ctx, db, rule.ID, now); err != nil {
		t.Fatal(err)
	}

	// 系统时长 600 秒 → OR 分支成立。
	okOrder := seedSimpleCloseableOrder(t, db, "close-tree-ok", 1000, 11)
	if _, err = closeOrder(ctx, db, okOrder.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("条件满足应可结单：%v", err)
	}
	// 时长 100 秒、上报 1 单位 → OR 分支不成立。
	badOrder := seedSimpleCloseableOrder(t, db, "close-tree-bad", 1000, 11)
	db.EsportsServiceSegment.Update().Where(esportsservicesegment.OrderIDEQ(badOrder.ID)).
		SetSystemSeconds(100).SetReportedUnits(1).SaveX(ctx)
	_, err = closeOrder(ctx, db, badOrder.ID, 11, closeOrderInput{Note: "结单"}, now)
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "成员上报服务量") {
		t.Fatalf("条件未满足应拦截并提示字段：%v", err)
	}
	status, err := orderCloseConditionStatus(ctx, db, badOrder.ID)
	if err != nil || status.Satisfied || !status.TreeConfigured || status.RuleCode != "TREE-AND-OR" {
		t.Fatalf("条件状态不正确：%+v err=%v", status, err)
	}
	// 默认底线树（凭证 ≥1、时长 >0、无未处理加钟、无进行中售后，共 5 个叶子）与自定义树
	// （3 个叶子）按 AND 合并，条件状态应逐项列出合并后的全部 8 个叶子。
	if len(status.Items) != 8 || len(status.Reasons) == 0 {
		t.Fatalf("条件状态应逐项列出合并后的叶子结果：%+v", status.Items)
	}
	// 补足服务量到 2 → 满足，可结单。
	db.EsportsServiceSegment.Update().Where(esportsservicesegment.OrderIDEQ(badOrder.ID)).
		SetReportedUnits(2).SaveX(ctx)
	if _, err = closeOrder(ctx, db, badOrder.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("补足服务量后应可结单：%v", err)
	}
}

// NOT 组合条件：要求「无进行中售后」为真，可用 not 表达「不存在未处理加钟」的否定形态。
func TestCloseConditionTreeNotOperator(t *testing.T) {
	db := openCloseRuleDB(t, "esports_close_condition_not")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	tree := schematier.CloseConditionNode{Op: "and", Children: []schematier.CloseConditionNode{
		closeLeaf("evidence_count", ">=", 1),
		// not(pending_addon_count > 0) 等价于 pending_addon_count == 0。
		{Op: "not", Children: []schematier.CloseConditionNode{closeLeaf("pending_addon_count", ">", 0)}},
	}}
	rule, err := createCloseRule(ctx, db, closeRuleInput{
		Code: "TREE-NOT", Name: "否定条件", Scope: "global", MinEvidence: 1,
		RequireDuration: false, RequireNoPendingAddon: false, RequireNoOpenAftersale: false,
		ConditionTree: &tree,
	}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCloseRule(ctx, db, rule.ID, now); err != nil {
		t.Fatal(err)
	}
	order := seedSimpleCloseableOrder(t, db, "close-tree-not", 1000, 11)
	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("无待处理加钟应可结单：%v", err)
	}
}

// SKU「结单要求」：SKU 直接引用结单规则 code，优先于全局规则；清空后回落全局。
func TestSkuCloseRuleReferenceWins(t *testing.T) {
	db := openCloseRuleDB(t, "esports_close_condition_sku")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	loose, err := createCloseRule(ctx, db, closeRuleInput{
		Code: "GLOBAL-LOOSE", Name: "全局宽松", Scope: "global", MinEvidence: 1,
	}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCloseRule(ctx, db, loose.ID, now); err != nil {
		t.Fatal(err)
	}
	strict, err := createCloseRule(ctx, db, closeRuleInput{
		Code: "SKU-STRICT", Name: "SKU 严格", Scope: "global", MinEvidence: 2,
	}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCloseRule(ctx, db, strict.ID, now); err != nil {
		t.Fatal(err)
	}
	// 引用校验：必须指向生效中的规则 code。
	if err := validateSkuCloseRuleCode(ctx, db, "NO-SUCH-RULE"); !errors.Is(err, errInvalidInput) {
		t.Fatalf("引用不存在的结单规则应被拒，err=%v", err)
	}
	if err := validateSkuCloseRuleCode(ctx, db, "SKU-STRICT"); err != nil {
		t.Fatalf("引用生效规则应通过：%v", err)
	}

	sku := db.EsportsSku.Create().SetCode("SKU-TREE").SetTitle("条件测试商品").
		SetServiceType("play").SetUnitPriceCents(1000).SetServiceUnits(1).
		SetServiceUnitName("小时").SetCloseRuleCode("SKU-STRICT").SaveX(ctx)
	order := seedSimpleCloseableOrder(t, db, "close-sku-ref", 1000, 11)
	db.EsportsOrder.UpdateOneID(order.ID).SetSkuID(sku.ID).SaveX(ctx)
	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("SKU 结单要求（凭证 ≥2）应优先于全局规则，err=%v", err)
	}
	// 清空 SKU 结单要求 → 回落全局宽松规则（凭证 ≥1）→ 可结单。
	db.EsportsSku.UpdateOneID(sku.ID).SetCloseRuleCode("").SaveX(ctx)
	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("回落全局规则后应可结单：%v", err)
	}
	// 规则 code 在生效规则集合内可见（供前端下拉选择）。
	rules := db.EsportsCloseRule.Query().Where(
		esportscloserule.StatusEQ(esportscloserule.StatusActive),
	).AllX(ctx)
	if len(rules) != 2 {
		t.Fatalf("生效规则数量异常：%d", len(rules))
	}
}

// 结构化条件项与条件树共用求值：规则仅配结构化项时的逐项状态可读、口径与结单一致。
func TestStructuredConditionsExposeStatus(t *testing.T) {
	db := openCloseRuleDB(t, "esports_close_condition_structured")
	defer db.Close()
	ctx := t.Context()
	order := seedSimpleCloseableOrder(t, db, "close-structured", 1000, 11)
	// 清掉服务段凭证 → 默认条件（凭证 ≥1）不满足。
	db.EsportsServiceSegment.Update().Where(esportsservicesegment.OrderIDEQ(order.ID)).
		SetEvidenceRef("").SaveX(ctx)
	status, err := orderCloseConditionStatus(ctx, db, order.ID)
	if err != nil || status.Satisfied || status.TreeConfigured {
		t.Fatalf("结构化默认条件应判定为不满足：%+v err=%v", status, err)
	}
	// 默认条件展开为：凭证 ≥1、时长（>0 秒 或 >0 服务量）、无未处理加钟、无进行中售后，共 5 个叶子。
	if len(status.Items) != 5 {
		t.Fatalf("默认条件应展开为 5 个叶子项：%+v", status.Items)
	}
	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, time.Now()); !errors.Is(err, errInvalidInput) {
		t.Fatalf("结构化条件不满足应拦截结单，err=%v", err)
	}
}

// 事实字段覆盖：承诺服务量、加钟量、进行中售后数均可作为条件字段。
// 凭证张数口径：seedSimpleCloseableOrder 的 evidence_ref 是历史文本引用（shot-1），按 1 张计。
func TestCloseConditionFactsAndExtraFields(t *testing.T) {
	db := openCloseRuleDB(t, "esports_close_condition_facts")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	order := seedSimpleCloseableOrder(t, db, "close-facts", 1000, 11)
	facts, err := closeConditionFactsOf(ctx, db, order)
	if err != nil {
		t.Fatal(err)
	}
	if facts.EvidenceCount != 1 || facts.ServiceSeconds != 600 || facts.ServiceUnits != 1 ||
		facts.PromisedServiceUnits != 1 || facts.PendingAddonCount != 0 ||
		facts.OpenAfterSaleCount != 0 || facts.AddonUnits != 0 {
		t.Fatalf("事实快照不正确：%+v", facts)
	}
	tree := schematier.CloseConditionNode{Op: "and", Children: []schematier.CloseConditionNode{
		closeLeaf("promised_service_units", ">=", 1),
		closeLeaf("service_units", ">=", 1),
		closeLeaf("open_aftersale_count", "==", 0),
	}}
	satisfied, items := evaluateCloseConditionTree(tree, facts)
	if !satisfied || len(items) != 3 {
		t.Fatalf("多字段条件求值失败：%v %+v", satisfied, items)
	}
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("无条件树时默认条件仍应可结单：%v", err)
	}
}

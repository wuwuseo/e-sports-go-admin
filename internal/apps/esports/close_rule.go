package esports

import (
	"context"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscloserule"

	"go-admin/internal/apps/esports/schematier"
)

// derefCloseConditionNode 把可选条件树指针转为值（nil 表示未配置，只按结构化条件项求值）。
func derefCloseConditionNode(node *schematier.CloseConditionNode) schematier.CloseConditionNode {
	if node == nil {
		return schematier.CloseConditionNode{}
	}
	return *node
}

// 结单规则（§3.4 首版）：把「服务凭证不少于 N 张、服务时长不为零、无未处理加钟 / 售后」等
// 结单条件做成可配置规则，并按 全局 / 游戏分区 / SKU 绑定到订单；金额达阈值时须由非负责客服的
// 管理员复核结单（高额单管理员复核的最小闭环）。
// 规则不可原地修改：改规则＝新增一条递增 version 的新行，旧行转 history（§12.4 同款版本管理）。

// closeRuleInput 结单规则的可编辑字段（code 创建后不可改）。
type closeRuleInput struct {
	Code                   string `json:"code"`
	Name                   string `json:"name"`
	Scope                  string `json:"scope"`
	ScopeKey               string `json:"scope_key"`
	MinEvidence            int    `json:"min_evidence"`
	RequireDuration        bool   `json:"require_duration"`
	RequireNoPendingAddon  bool   `json:"require_no_pending_addon"`
	RequireNoOpenAftersale bool   `json:"require_no_open_aftersale"`
	ReviewThresholdCents   int64  `json:"review_threshold_cents"`
	// ConditionTree 是通用 AND/OR 结单条件树（§3.4 结单条件自定义）。留空表示只用结构化条件项；
	// 配置后与结构化条件项**按 AND 合并**（结构化条件是硬性底线，不能被条件树删掉）。
	ConditionTree *schematier.CloseConditionNode `json:"condition_tree"`
	Note          string                         `json:"note"`
}

func validCloseRuleScope(scope string) bool {
	return scope == "global" || scope == "game" || scope == "sku"
}

func validateCloseRuleInput(in closeRuleInput) error {
	if strings.TrimSpace(in.Name) == "" || len([]rune(in.Name)) > 64 ||
		strings.TrimSpace(in.Code) == "" || len(in.Code) > 48 ||
		!validCloseRuleScope(in.Scope) || len([]rune(strings.TrimSpace(in.ScopeKey))) > 64 ||
		in.MinEvidence < 1 || in.ReviewThresholdCents < 0 || len([]rune(strings.TrimSpace(in.Note))) > 255 {
		return errInvalidInput
	}
	if in.Scope != "global" && strings.TrimSpace(in.ScopeKey) == "" {
		return errInvalidInput
	}
	return nil
}

// createCloseRule 新建结单规则（draft）。同 code 已存在则版本 = 现有最大 version + 1。
func createCloseRule(ctx context.Context, db *ent.Client, in closeRuleInput, adminID int) (*ent.EsportsCloseRule, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	in.ScopeKey = strings.TrimSpace(in.ScopeKey)
	in.Note = strings.TrimSpace(in.Note)
	if err := validateCloseRuleInput(in); err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	version := 1
	latest, err := tx.EsportsCloseRule.Query().Where(
		esportscloserule.CodeEQ(in.Code),
	).Order(ent.Desc(esportscloserule.FieldVersion)).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if latest != nil {
		version = latest.Version + 1
	}
	tree, err := normalizeCloseConditionTree(derefCloseConditionNode(in.ConditionTree))
	if err != nil {
		return nil, err
	}
	create := tx.EsportsCloseRule.Create().SetCode(in.Code).SetName(in.Name).
		SetScope(esportscloserule.Scope(in.Scope)).SetScopeKey(in.ScopeKey).
		SetMinEvidence(in.MinEvidence).SetRequireDuration(in.RequireDuration).
		SetRequireNoPendingAddon(in.RequireNoPendingAddon).
		SetRequireNoOpenAftersale(in.RequireNoOpenAftersale).
		SetReviewThresholdCents(in.ReviewThresholdCents).
		SetVersion(version).SetCreatedByAdminID(adminID).SetNote(in.Note)
	if tree != nil {
		create.SetConditionTree(*tree)
	}
	item, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCloseRule.Get(ctx, item.ID)
}

// updateCloseRule 编辑草稿（乐观锁，须为 draft）。code 不可改。
func updateCloseRule(ctx context.Context, db *ent.Client, id, expectedVersion int, in closeRuleInput) (*ent.EsportsCloseRule, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.ScopeKey = strings.TrimSpace(in.ScopeKey)
	in.Note = strings.TrimSpace(in.Note)
	if err := validateCloseRuleInput(in); err != nil {
		return nil, err
	}
	tree, err := normalizeCloseConditionTree(derefCloseConditionNode(in.ConditionTree))
	if err != nil {
		return nil, err
	}
	update := db.EsportsCloseRule.Update().Where(
		esportscloserule.IDEQ(id),
		esportscloserule.VersionEQ(expectedVersion),
		esportscloserule.StatusEQ(esportscloserule.StatusDraft),
	).SetName(in.Name).SetScope(esportscloserule.Scope(in.Scope)).SetScopeKey(in.ScopeKey).
		SetMinEvidence(in.MinEvidence).SetRequireDuration(in.RequireDuration).
		SetRequireNoPendingAddon(in.RequireNoPendingAddon).
		SetRequireNoOpenAftersale(in.RequireNoOpenAftersale).
		SetReviewThresholdCents(in.ReviewThresholdCents).SetNote(in.Note)
	// 条件树置空即只保留结构化条件项；配置后与结构化条件项按 AND 合并（底线不可删）。
	if tree == nil {
		update.SetConditionTree(schematier.CloseConditionNode{})
	} else {
		update.SetConditionTree(*tree)
	}
	changed, err := update.AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsCloseRule.Get(ctx, id)
}

// activateCloseRule 生效：同 code 现有 active 转 history，本行（须 draft）转 active。
func activateCloseRule(ctx context.Context, db *ent.Client, id int, now time.Time) (*ent.EsportsCloseRule, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	target, err := tx.EsportsCloseRule.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if target.Status != esportscloserule.StatusDraft {
		return nil, errConflict
	}
	if _, err = tx.EsportsCloseRule.Update().Where(
		esportscloserule.CodeEQ(target.Code),
		esportscloserule.StatusEQ(esportscloserule.StatusActive),
	).SetStatus(esportscloserule.StatusHistory).Save(ctx); err != nil {
		return nil, err
	}
	changed, err := tx.EsportsCloseRule.Update().Where(
		esportscloserule.IDEQ(id),
		esportscloserule.StatusEQ(esportscloserule.StatusDraft),
	).SetStatus(esportscloserule.StatusActive).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCloseRule.Get(ctx, id)
}

// rollbackCloseRule 回滚到指定版本：以该行的 code 与配置为模板新建 max version+1 的直接 active 行，
// 并把原 active 转 history（原历史行不可变，§12.4）。
func rollbackCloseRule(ctx context.Context, db *ent.Client, id, adminID int) (*ent.EsportsCloseRule, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	source, err := tx.EsportsCloseRule.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	latest, err := tx.EsportsCloseRule.Query().Where(
		esportscloserule.CodeEQ(source.Code),
	).Order(ent.Desc(esportscloserule.FieldVersion)).First(ctx)
	if err != nil {
		return nil, err
	}
	created, err := tx.EsportsCloseRule.Create().SetCode(source.Code).SetName(source.Name).
		SetScope(source.Scope).SetScopeKey(source.ScopeKey).
		SetMinEvidence(source.MinEvidence).SetRequireDuration(source.RequireDuration).
		SetRequireNoPendingAddon(source.RequireNoPendingAddon).
		SetRequireNoOpenAftersale(source.RequireNoOpenAftersale).
		SetReviewThresholdCents(source.ReviewThresholdCents).
		SetVersion(latest.Version + 1).SetStatus(esportscloserule.StatusActive).
		SetCreatedByAdminID(adminID).SetNote(source.Note).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsCloseRule.Update().Where(
		esportscloserule.CodeEQ(source.Code),
		esportscloserule.StatusEQ(esportscloserule.StatusActive),
		esportscloserule.IDNEQ(created.ID),
	).SetStatus(esportscloserule.StatusHistory).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCloseRule.Get(ctx, created.ID)
}

// validateSkuCloseRuleCode 校验 SKU 的「结单要求」引用（§15.3）：
// 留空表示按游戏 / 全局规则回落；非空时必须存在生效中的同 code 结单规则。
func validateSkuCloseRuleCode(ctx context.Context, db *ent.Client, code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil
	}
	if len(code) > 48 {
		return errInvalidInput
	}
	exists, err := db.EsportsCloseRule.Query().Where(
		esportscloserule.CodeEQ(code),
		esportscloserule.StatusEQ(esportscloserule.StatusActive),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return errInvalidInput
	}
	return nil
}

// listCloseRules 列出结单规则（按 ID 倒序，最多 limit 条）。
func listCloseRules(ctx context.Context, db *ent.Client, limit int) ([]*ent.EsportsCloseRule, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	return db.EsportsCloseRule.Query().Order(ent.Desc(esportscloserule.FieldID)).Limit(limit).All(ctx)
}

// resolveCloseRule 解析订单适用的生效结单规则：sku > game > global，同层取 version 最大；
// 无命中返回 nil, nil（调用方回落内置默认条件）。
func resolveCloseRule(ctx context.Context, db *ent.Client, order *ent.EsportsOrder) (*ent.EsportsCloseRule, error) {
	active, err := db.EsportsCloseRule.Query().Where(
		esportscloserule.StatusEQ(esportscloserule.StatusActive),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}
	pick := func(scope esportscloserule.Scope, key string) *ent.EsportsCloseRule {
		var best *ent.EsportsCloseRule
		for _, rule := range active {
			if rule.Scope != scope || rule.ScopeKey != key {
				continue
			}
			if best == nil || rule.Version > best.Version {
				best = rule
			}
		}
		return best
	}
	sku, err := db.EsportsSku.Get(ctx, order.SkuID)
	if err == nil {
		// §15.3：SKU「结单要求」直接引用结单规则 code，优先级高于按 scope 匹配。
		if code := strings.TrimSpace(sku.CloseRuleCode); code != "" {
			var best *ent.EsportsCloseRule
			for _, rule := range active {
				if rule.Code != code {
					continue
				}
				if best == nil || rule.Version > best.Version {
					best = rule
				}
			}
			if best != nil {
				return best, nil
			}
		}
		if rule := pick(esportscloserule.ScopeSku, sku.Code); rule != nil {
			return rule, nil
		}
		if sku.CategoryID != nil {
			if category, err := db.EsportsProductCategory.Get(ctx, *sku.CategoryID); err == nil {
				if game := strings.TrimSpace(category.Game); game != "" {
					if rule := pick(esportscloserule.ScopeGame, game); rule != nil {
						return rule, nil
					}
				}
			}
		}
	}
	return pick(esportscloserule.ScopeGlobal, ""), nil
}

// closeConditions 是结单条件的有效值（规则缺失时回落内置默认）。
type closeConditions struct {
	MinEvidence            int
	RequireDuration        bool
	RequireNoPendingAddon  bool
	RequireNoOpenAftersale bool
	ReviewThresholdCents   int64
}

// effectiveCloseConditions 取规则条件；规则缺失时回落 §3.4 内置默认（凭证 ≥1、时长不为零、无未处理加钟 / 售后）。
func effectiveCloseConditions(rule *ent.EsportsCloseRule) closeConditions {
	if rule == nil {
		return closeConditions{
			MinEvidence:            1,
			RequireDuration:        true,
			RequireNoPendingAddon:  true,
			RequireNoOpenAftersale: true,
		}
	}
	return closeConditions{
		MinEvidence:            rule.MinEvidence,
		RequireDuration:        rule.RequireDuration,
		RequireNoPendingAddon:  rule.RequireNoPendingAddon,
		RequireNoOpenAftersale: rule.RequireNoOpenAftersale,
		ReviewThresholdCents:   rule.ReviewThresholdCents,
	}
}

// hasOpenAfterSale 判断订单是否存在进行中（非终态）的售后单（§3.4 结单条件默认项）。
func hasOpenAfterSale(ctx context.Context, db *ent.Client, orderID int) (bool, error) {
	return db.EsportsAfterSale.Query().Where(
		esportsaftersale.SourceTypeEQ("order"),
		esportsaftersale.SourceIDEQ(orderID),
		esportsaftersale.StatusIn(activeAfterSaleStatuses()...),
	).Exist(ctx)
}

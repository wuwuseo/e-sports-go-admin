package esports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscloseformfield"
	"go-admin/internal/ent/esportscloseformsnapshot"
	"go-admin/internal/ent/esportscloseformtemplate"
	"go-admin/internal/ent/esportsservicesegment"
)

// 结单表单设计器（§3.4 首版）：字段库 + 模板（按 全局 / 游戏分区 / SKU + 金额区间绑定）
// + 下单时锁定模板版本，结单时按该模板校验必填字段并留痕快照。
// ponytail: 拖拽画布与任意坐标、字段级联与条件显示、字段库版本历史、
// 按订单类型（即时 / 预约）绑定尚未实现。旧模板未存字段定义快照时仍回退实时字段库。

// closeFormFieldTypes 是工程实现的字段类型全集（§3.4 自定义字段类型）。
var closeFormFieldTypes = map[string]bool{
	"text": true, "number": true, "money": true, "select": true, "multiselect": true,
	"datetime": true, "image": true, "switch": true, "reference": true,
}

// validCloseFormScope 与结单规则同款适用范围。
func validCloseFormScope(scope string) bool {
	return scope == "global" || scope == "game" || scope == "sku"
}

// validCloseFormFieldKey 字段 key 形如小写字母开头、仅含小写字母 / 数字 / 下划线。
func validCloseFormFieldKey(key string) bool {
	if len(key) < 2 || len(key) > 48 || key[0] < 'a' || key[0] > 'z' {
		return false
	}
	for i := 1; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}

// ── 字段库（原地编辑，不做版本化） ──

type closeFormFieldInput struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	FieldType string   `json:"field_type"`
	Required  bool     `json:"required"`
	Options   []string `json:"options"`
	MaxLength int      `json:"max_length"`
	SortOrder int      `json:"sort_order"`
	Note      string   `json:"note"`
}

// normalizeCloseFormFieldInput 去空去重选项并校验字段定义。
func normalizeCloseFormFieldInput(in closeFormFieldInput) (closeFormFieldInput, error) {
	in.Key = strings.TrimSpace(in.Key)
	in.Label = strings.TrimSpace(in.Label)
	in.Note = strings.TrimSpace(in.Note)
	if !validCloseFormFieldKey(in.Key) || in.Label == "" || len([]rune(in.Label)) > 64 ||
		!closeFormFieldTypes[in.FieldType] || in.MaxLength < 0 || in.MaxLength > 2000 ||
		in.SortOrder < -1000 || in.SortOrder > 1000 || len([]rune(in.Note)) > 255 {
		return in, errInvalidInput
	}
	seen := map[string]bool{}
	cleaned := make([]string, 0, len(in.Options))
	for _, option := range in.Options {
		option = strings.TrimSpace(option)
		if option == "" || len([]rune(option)) > 64 || seen[option] {
			continue
		}
		seen[option] = true
		cleaned = append(cleaned, option)
	}
	switch in.FieldType {
	case "select", "multiselect":
		if len(cleaned) == 0 {
			return in, fmt.Errorf("%w：单选 / 多选字段须至少配置一个选项", errInvalidInput)
		}
		in.Options = cleaned
	case "text":
		in.Options = nil
	default:
		in.Options = nil
		if in.FieldType != "text" && in.MaxLength > 0 {
			return in, fmt.Errorf("%w：仅文本字段支持最大长度", errInvalidInput)
		}
	}
	return in, nil
}

// createCloseFormField 新建字段库字段；key 唯一。
func createCloseFormField(ctx context.Context, db *ent.Client, in closeFormFieldInput) (*ent.EsportsCloseFormField, error) {
	in, err := normalizeCloseFormFieldInput(in)
	if err != nil {
		return nil, err
	}
	item, err := db.EsportsCloseFormField.Create().SetKey(in.Key).SetLabel(in.Label).
		SetFieldType(esportscloseformfield.FieldType(in.FieldType)).SetRequired(in.Required).
		SetOptions(in.Options).SetMaxLength(in.MaxLength).SetSortOrder(in.SortOrder).
		SetNote(in.Note).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	return item, nil
}

// updateCloseFormField 原地编辑字段（乐观锁）；key 不可改。
func updateCloseFormField(ctx context.Context, db *ent.Client, id, expectedVersion int, in closeFormFieldInput) (*ent.EsportsCloseFormField, error) {
	in, err := normalizeCloseFormFieldInput(in)
	if err != nil {
		return nil, err
	}
	if id < 1 {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsCloseFormField.Update().Where(
		esportscloseformfield.IDEQ(id),
		esportscloseformfield.VersionEQ(expectedVersion),
	).SetLabel(in.Label).SetFieldType(esportscloseformfield.FieldType(in.FieldType)).
		SetRequired(in.Required).SetOptions(in.Options).SetMaxLength(in.MaxLength).
		SetSortOrder(in.SortOrder).SetNote(in.Note).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsCloseFormField.Get(ctx, id)
}

// setCloseFormFieldEnabled 启用 / 停用字段（停用后新模板不可再引用，已生效模板不受影响）。
func setCloseFormFieldEnabled(ctx context.Context, db *ent.Client, id int, enabled bool) (*ent.EsportsCloseFormField, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsCloseFormField.Update().Where(
		esportscloseformfield.IDEQ(id),
	).SetEnabled(enabled).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errInvalidInput
	}
	return db.EsportsCloseFormField.Get(ctx, id)
}

// listCloseFormFields 列出字段库（按排序值、ID 升序）。
func listCloseFormFields(ctx context.Context, db *ent.Client, limit int) ([]*ent.EsportsCloseFormField, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	return db.EsportsCloseFormField.Query().
		Order(ent.Asc(esportscloseformfield.FieldSortOrder), ent.Asc(esportscloseformfield.FieldID)).
		Limit(limit).All(ctx)
}

// ── 模板（版本化：新增 / 生效 / 回滚） ──

type closeFormTemplateInput struct {
	Code     string                         `json:"code"`
	Name     string                         `json:"name"`
	Scope    string                         `json:"scope"`
	ScopeKey string                         `json:"scope_key"`
	MinCents int64                          `json:"min_cents"`
	MaxCents int64                          `json:"max_cents"`
	Fields   []schema.CloseFormFieldBinding `json:"fields"`
	Note     string                         `json:"note"`
}

// normalizeCloseFormTemplateInput 校验模板定义，并确认引用字段存在且已启用。
func normalizeCloseFormTemplateInput(ctx context.Context, db *ent.Client, in closeFormTemplateInput) (closeFormTemplateInput, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	in.ScopeKey = strings.TrimSpace(in.ScopeKey)
	in.Note = strings.TrimSpace(in.Note)
	if in.Code == "" || len(in.Code) > 48 || in.Name == "" || len([]rune(in.Name)) > 64 ||
		!validCloseFormScope(in.Scope) || len([]rune(in.ScopeKey)) > 64 ||
		in.MinCents < 0 || in.MaxCents < 0 || (in.MaxCents > 0 && in.MaxCents <= in.MinCents) ||
		len([]rune(in.Note)) > 255 {
		return in, errInvalidInput
	}
	if in.Scope != "global" && in.ScopeKey == "" {
		return in, fmt.Errorf("%w：按游戏分区 / SKU 绑定时须填写范围标识", errInvalidInput)
	}
	if len(in.Fields) == 0 {
		return in, fmt.Errorf("%w：模板须至少绑定一个字段", errInvalidInput)
	}
	keys := make([]string, 0, len(in.Fields))
	seen := map[string]bool{}
	cleaned := make([]schema.CloseFormFieldBinding, 0, len(in.Fields))
	for _, binding := range in.Fields {
		key := strings.TrimSpace(binding.FieldKey)
		if !validCloseFormFieldKey(key) || seen[key] {
			return in, fmt.Errorf("%w：模板字段 key 非法或重复", errInvalidInput)
		}
		seen[key] = true
		keys = append(keys, key)
		cleaned = append(cleaned, schema.CloseFormFieldBinding{
			FieldKey: key, Required: binding.Required, Sort: binding.Sort,
		})
	}
	available, err := db.EsportsCloseFormField.Query().Where(
		esportscloseformfield.KeyIn(keys...),
		esportscloseformfield.EnabledEQ(true),
	).Select(esportscloseformfield.FieldKey).All(ctx)
	if err != nil {
		return in, err
	}
	if len(available) != len(keys) {
		return in, fmt.Errorf("%w：模板引用了不存在或已停用的字段", errInvalidInput)
	}
	in.Fields = cleaned
	return in, nil
}

// createCloseFormTemplate 新建模板（draft）。同 code 已存在则版本 = 现有最大 version + 1。
func createCloseFormTemplate(ctx context.Context, db *ent.Client, in closeFormTemplateInput, adminID int) (*ent.EsportsCloseFormTemplate, error) {
	in, err := normalizeCloseFormTemplateInput(ctx, db, in)
	if err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	version := 1
	latest, err := tx.EsportsCloseFormTemplate.Query().Where(
		esportscloseformtemplate.CodeEQ(in.Code),
	).Order(ent.Desc(esportscloseformtemplate.FieldVersion)).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if latest != nil {
		version = latest.Version + 1
	}
	item, err := tx.EsportsCloseFormTemplate.Create().SetCode(in.Code).SetName(in.Name).
		SetScope(esportscloseformtemplate.Scope(in.Scope)).SetScopeKey(in.ScopeKey).
		SetMinCents(in.MinCents).SetMaxCents(in.MaxCents).SetFields(in.Fields).
		SetVersion(version).SetCreatedByAdminID(adminID).SetNote(in.Note).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCloseFormTemplate.Get(ctx, item.ID)
}

// updateCloseFormTemplate 编辑草稿（乐观锁，须为 draft）；code 不可改。
func updateCloseFormTemplate(ctx context.Context, db *ent.Client, id, expectedVersion int, in closeFormTemplateInput) (*ent.EsportsCloseFormTemplate, error) {
	in, err := normalizeCloseFormTemplateInput(ctx, db, in)
	if err != nil {
		return nil, err
	}
	changed, err := db.EsportsCloseFormTemplate.Update().Where(
		esportscloseformtemplate.IDEQ(id),
		esportscloseformtemplate.VersionEQ(expectedVersion),
		esportscloseformtemplate.StatusEQ(esportscloseformtemplate.StatusDraft),
	).SetName(in.Name).SetScope(esportscloseformtemplate.Scope(in.Scope)).SetScopeKey(in.ScopeKey).
		SetMinCents(in.MinCents).SetMaxCents(in.MaxCents).SetFields(in.Fields).
		SetNote(in.Note).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsCloseFormTemplate.Get(ctx, id)
}

// activateCloseFormTemplate 生效：同 code 现有 active 转 history，本行（须 draft）转 active。
func activateCloseFormTemplate(ctx context.Context, db *ent.Client, id int) (*ent.EsportsCloseFormTemplate, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	target, err := tx.EsportsCloseFormTemplate.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if target.Status != esportscloseformtemplate.StatusDraft {
		return nil, errConflict
	}
	definitions, err := snapshotCloseFormFields(ctx, tx.Client(), target.Fields)
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsCloseFormTemplate.Update().Where(
		esportscloseformtemplate.CodeEQ(target.Code),
		esportscloseformtemplate.StatusEQ(esportscloseformtemplate.StatusActive),
	).SetStatus(esportscloseformtemplate.StatusHistory).Save(ctx); err != nil {
		return nil, err
	}
	changed, err := tx.EsportsCloseFormTemplate.Update().Where(
		esportscloseformtemplate.IDEQ(id),
		esportscloseformtemplate.StatusEQ(esportscloseformtemplate.StatusDraft),
	).SetStatus(esportscloseformtemplate.StatusActive).
		SetFieldDefinitions(definitions).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCloseFormTemplate.Get(ctx, id)
}

// rollbackCloseFormTemplate 回滚到指定版本：以该行配置为模板新建 max version+1 的直接 active 行，
// 并把原 active 转 history（原历史行不可变，§12.4）。
func rollbackCloseFormTemplate(ctx context.Context, db *ent.Client, id, adminID int) (*ent.EsportsCloseFormTemplate, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	source, err := tx.EsportsCloseFormTemplate.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	latest, err := tx.EsportsCloseFormTemplate.Query().Where(
		esportscloseformtemplate.CodeEQ(source.Code),
	).Order(ent.Desc(esportscloseformtemplate.FieldVersion)).First(ctx)
	if err != nil {
		return nil, err
	}
	created, err := tx.EsportsCloseFormTemplate.Create().SetCode(source.Code).SetName(source.Name).
		SetScope(source.Scope).SetScopeKey(source.ScopeKey).
		SetMinCents(source.MinCents).SetMaxCents(source.MaxCents).SetFields(source.Fields).
		SetFieldDefinitions(source.FieldDefinitions).
		SetVersion(latest.Version + 1).SetStatus(esportscloseformtemplate.StatusActive).
		SetCreatedByAdminID(adminID).SetNote(source.Note).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsCloseFormTemplate.Update().Where(
		esportscloseformtemplate.CodeEQ(source.Code),
		esportscloseformtemplate.StatusEQ(esportscloseformtemplate.StatusActive),
		esportscloseformtemplate.IDNEQ(created.ID),
	).SetStatus(esportscloseformtemplate.StatusHistory).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCloseFormTemplate.Get(ctx, created.ID)
}

// listCloseFormTemplates 列出模板（按 ID 倒序）。
func listCloseFormTemplates(ctx context.Context, db *ent.Client, limit int) ([]*ent.EsportsCloseFormTemplate, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	return db.EsportsCloseFormTemplate.Query().
		Order(ent.Desc(esportscloseformtemplate.FieldID)).Limit(limit).All(ctx)
}

// closeFormTemplateMatchesAmount 金额区间命中：min ≤ 应付，且 max 为 0（不限）或 应付 < max。
func closeFormTemplateMatchesAmount(t *ent.EsportsCloseFormTemplate, payableCents int64) bool {
	if payableCents < t.MinCents {
		return false
	}
	return t.MaxCents == 0 || payableCents < t.MaxCents
}

// resolveCloseFormTemplate 解析订单适用的生效模板：sku > game > global，同层取命中金额区间且 version 最大；
// 无命中返回 nil, nil（结单表单非必填）。
func resolveCloseFormTemplate(ctx context.Context, db *ent.Client, order *ent.EsportsOrder) (*ent.EsportsCloseFormTemplate, error) {
	if order.CloseFormTemplateLockedAt != nil {
		if order.CloseFormTemplateID == nil {
			return nil, nil
		}
		return db.EsportsCloseFormTemplate.Get(ctx, *order.CloseFormTemplateID)
	}
	active, err := db.EsportsCloseFormTemplate.Query().Where(
		esportscloseformtemplate.StatusEQ(esportscloseformtemplate.StatusActive),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}
	pick := func(scope esportscloseformtemplate.Scope, key string) *ent.EsportsCloseFormTemplate {
		var best *ent.EsportsCloseFormTemplate
		for _, item := range active {
			if item.Scope != scope || item.ScopeKey != key ||
				!closeFormTemplateMatchesAmount(item, order.PayableCents) {
				continue
			}
			if best == nil || item.Version > best.Version {
				best = item
			}
		}
		return best
	}
	sku, err := db.EsportsSku.Get(ctx, order.SkuID)
	if err == nil {
		if item := pick(esportscloseformtemplate.ScopeSku, sku.Code); item != nil {
			return item, nil
		}
		if sku.CategoryID != nil {
			if category, err := db.EsportsProductCategory.Get(ctx, *sku.CategoryID); err == nil {
				if game := strings.TrimSpace(category.Game); game != "" {
					if item := pick(esportscloseformtemplate.ScopeGame, game); item != nil {
						return item, nil
					}
				}
			}
		}
	}
	return pick(esportscloseformtemplate.ScopeGlobal, ""), nil
}

// ── 字段值校验与快照 ──

// snapshotCloseFormFields 在模板生效时固定字段类型、选项与必填设置。
func snapshotCloseFormFields(ctx context.Context, db *ent.Client, bindings []schema.CloseFormFieldBinding) ([]schema.CloseFormFieldDefinition, error) {
	ordered, _, err := liveCloseFormFieldValues(ctx, db, bindings)
	if err != nil {
		return nil, err
	}
	if len(ordered) != len(bindings) {
		return nil, fmt.Errorf("%w：模板引用字段不存在", errInvalidInput)
	}
	snapshot := make([]schema.CloseFormFieldDefinition, 0, len(ordered))
	for _, def := range ordered {
		if !def.Enabled {
			return nil, fmt.Errorf("%w：模板引用字段已停用", errInvalidInput)
		}
		snapshot = append(snapshot, schema.CloseFormFieldDefinition{
			ID: def.ID, Key: def.Key, Label: def.Label, FieldType: def.FieldType.String(),
			Required: def.Required, Options: append([]string(nil), def.Options...),
			MaxLength: def.MaxLength, SortOrder: def.SortOrder, Version: def.Version,
		})
	}
	return snapshot, nil
}

// closeFormFieldValues 优先读取模板生效时的字段定义；旧模板无快照时兼容实时字段库。
func closeFormFieldValues(ctx context.Context, db *ent.Client, template *ent.EsportsCloseFormTemplate) ([]*ent.EsportsCloseFormField, map[string]*ent.EsportsCloseFormField, error) {
	if len(template.FieldDefinitions) == 0 {
		return liveCloseFormFieldValues(ctx, db, template.Fields)
	}
	byKey := make(map[string]*ent.EsportsCloseFormField, len(template.FieldDefinitions))
	for _, snap := range template.FieldDefinitions {
		byKey[snap.Key] = &ent.EsportsCloseFormField{
			ID: snap.ID, Key: snap.Key, Label: snap.Label,
			FieldType: esportscloseformfield.FieldType(snap.FieldType), Required: snap.Required,
			Options: append([]string(nil), snap.Options...), MaxLength: snap.MaxLength,
			SortOrder: snap.SortOrder, Version: snap.Version, Enabled: true,
		}
	}
	ordered := make([]*ent.EsportsCloseFormField, 0, len(template.Fields))
	for _, binding := range template.Fields {
		if def := byKey[binding.FieldKey]; def != nil {
			ordered = append(ordered, def)
		}
	}
	return ordered, byKey, nil
}

func liveCloseFormFieldValues(ctx context.Context, db *ent.Client, bindings []schema.CloseFormFieldBinding) ([]*ent.EsportsCloseFormField, map[string]*ent.EsportsCloseFormField, error) {
	keys := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		keys = append(keys, binding.FieldKey)
	}
	if len(keys) == 0 {
		return nil, map[string]*ent.EsportsCloseFormField{}, nil
	}
	defs, err := db.EsportsCloseFormField.Query().Where(esportscloseformfield.KeyIn(keys...)).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	byKey := make(map[string]*ent.EsportsCloseFormField, len(defs))
	for _, def := range defs {
		byKey[def.Key] = def
	}
	ordered := make([]*ent.EsportsCloseFormField, 0, len(bindings))
	for _, binding := range bindings {
		if def := byKey[binding.FieldKey]; def != nil {
			ordered = append(ordered, def)
		}
	}
	return ordered, byKey, nil
}

// isEmptyCloseFormValue 判断值是否为空（开关的 false 视为有效值）。
func isEmptyCloseFormValue(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	case []any:
		return len(v) == 0
	case []string:
		return len(v) == 0
	default:
		return false
	}
}

// normalizeCloseFormValue 按字段类型校验并归一化单个值；返回归一化后的值。
func normalizeCloseFormValue(def *ent.EsportsCloseFormField, value any) (any, error) {
	fail := func() (any, error) {
		return nil, fmt.Errorf("%w：字段「%s」取值不合法", errInvalidInput, def.Label)
	}
	switch def.FieldType {
	case esportscloseformfield.FieldTypeText:
		text, ok := value.(string)
		if !ok {
			return fail()
		}
		text = strings.TrimSpace(text)
		if def.MaxLength > 0 && len([]rune(text)) > def.MaxLength {
			return nil, fmt.Errorf("%w：字段「%s」超出最大长度 %d", errInvalidInput, def.Label, def.MaxLength)
		}
		return text, nil
	case esportscloseformfield.FieldTypeNumber:
		switch value.(type) {
		case float64, int, int64:
			return value, nil
		default:
			return fail()
		}
	case esportscloseformfield.FieldTypeSwitch:
		if _, ok := value.(bool); !ok {
			return fail()
		}
		return value, nil
	case esportscloseformfield.FieldTypeSelect:
		text, ok := value.(string)
		if !ok {
			return fail()
		}
		if !containsString(def.Options, text) {
			return fail()
		}
		return text, nil
	case esportscloseformfield.FieldTypeMultiselect:
		items, ok := value.([]any)
		if !ok {
			if strs, okStr := value.([]string); okStr {
				for _, item := range strs {
					if !containsString(def.Options, item) {
						return fail()
					}
				}
				return strs, nil
			}
			return fail()
		}
		cleaned := make([]string, 0, len(items))
		for _, item := range items {
			text, ok := item.(string)
			if !ok || !containsString(def.Options, text) {
				return fail()
			}
			cleaned = append(cleaned, text)
		}
		return cleaned, nil
	case esportscloseformfield.FieldTypeDatetime:
		text, ok := value.(string)
		if !ok {
			return fail()
		}
		if _, err := time.Parse(time.RFC3339, text); err != nil {
			return fail()
		}
		return text, nil
	case esportscloseformfield.FieldTypeImage:
		text, ok := value.(string)
		if !ok || len([]rune(text)) > 512 {
			return fail()
		}
		return text, nil
	case esportscloseformfield.FieldTypeReference:
		switch value.(type) {
		case string, float64, int, int64:
			return value, nil
		default:
			return fail()
		}
	default:
		return fail()
	}
}

// containsString 判断字符串是否在列表中。
func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

// applyCloseForm 结单时按生效模板校验字段值并写快照（§3.4）。无适用模板时不校验、不写快照。
func applyCloseForm(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, adminID int, values map[string]any, note string, now time.Time) error {
	template, err := resolveCloseFormTemplate(ctx, tx.Client(), order)
	if err != nil {
		return err
	}
	if template == nil || len(template.Fields) == 0 {
		return nil
	}
	_, byKey, err := closeFormFieldValues(ctx, tx.Client(), template)
	if err != nil {
		return err
	}
	recorded := map[string]any{}
	for _, binding := range template.Fields {
		def := byKey[binding.FieldKey]
		if def == nil {
			return fmt.Errorf("%w：结单表单字段「%s」已下线，无法结单", errInvalidInput, binding.FieldKey)
		}
		// 金额只读字段由系统按订单应付金额自动填写，不取提交值。
		if def.FieldType == esportscloseformfield.FieldTypeMoney {
			recorded[def.Key] = order.PayableCents
			continue
		}
		value, ok := values[def.Key]
		required := binding.Required || def.Required
		if !ok || isEmptyCloseFormValue(value) {
			if required {
				return fmt.Errorf("%w：请填写结单表单必填项「%s」", errInvalidInput, def.Label)
			}
			recorded[def.Key] = nil
			continue
		}
		normalized, err := normalizeCloseFormValue(def, value)
		if err != nil {
			return err
		}
		recorded[def.Key] = normalized
	}
	if _, err = tx.EsportsCloseFormSnapshot.Create().SetOrderID(order.ID).
		SetTemplateID(template.ID).SetTemplateCode(template.Code).SetTemplateVersion(template.Version).
		SetValues(recorded).SetNote(note).SetSubmittedByAdminID(adminID).Save(ctx); err != nil {
		return err
	}
	return nil
}

// orderCloseFormView 是订单结单配置视图：适用表单模板与字段、最近一次快照、提交规则摘要与未上报成员。
type orderCloseFormView struct {
	Template       *ent.EsportsCloseFormTemplate `json:"template"`
	Fields         []*ent.EsportsCloseFormField  `json:"fields"`
	LatestSnapshot *ent.EsportsCloseFormSnapshot `json:"latest_snapshot"`
	SubmitRule     map[string]interface{}        `json:"submit_rule"`
	Unsubmitted    []unsubmittedMember           `json:"unsubmitted"`
}

// orderCloseForm 返回订单结单表单所需的模板与字段定义（供管理端结单弹窗渲染）。
func orderCloseForm(ctx context.Context, db *ent.Client, orderID int) (*orderCloseFormView, error) {
	if orderID < 1 {
		return nil, errInvalidInput
	}
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	view := &orderCloseFormView{Fields: []*ent.EsportsCloseFormField{}}
	template, err := resolveCloseFormTemplate(ctx, db, order)
	if err != nil {
		return nil, err
	}
	total, err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
	).Count(ctx)
	if err != nil {
		return nil, err
	}
	submitRule, err := resolveCloseSubmitRule(ctx, db, order)
	if err != nil {
		return nil, err
	}
	view.SubmitRule = closeSubmitRuleSummary(submitRule, total)
	if view.Unsubmitted, err = unsubmittedMembers(ctx, db, orderID); err != nil {
		return nil, err
	}
	if template != nil {
		view.Template = template
		ordered, _, err := closeFormFieldValues(ctx, db, template)
		if err != nil {
			return nil, err
		}
		if ordered != nil {
			view.Fields = ordered
		}
	}
	snapshot, err := db.EsportsCloseFormSnapshot.Query().Where(
		esportscloseformsnapshot.OrderIDEQ(orderID),
	).Order(ent.Desc(esportscloseformsnapshot.FieldID)).First(ctx)
	if err == nil {
		view.LatestSnapshot = snapshot
	} else if !ent.IsNotFound(err) {
		return nil, err
	}
	return view, nil
}

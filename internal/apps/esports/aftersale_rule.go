package esports

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersalerule"
	"go-admin/internal/ent/esportsaftersalerulehit"
	"go-admin/internal/ent/esportsgiftorder"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

// 售后自动建单规则：把「结单后异常」做成可配置的「条件 + 动作」规则，
// 由 esports_scan_aftersale_rules 任务按规则扫描并建 channel=system 的售后单，
// 每次命中写一条 EsportsAfterSaleRuleHit 只增留痕（dedup_key 唯一兜底重复建单）。
//
// 与既有硬编码分支的分工（两者并存，互不替代）：
//   - player.go 的 finishPlayerService 在「服务段结束」的同一事务里按
//     service_duration_severe_minutes 直接建 duration_short 单：即时、不依赖任务调度；
//   - 本规则引擎在「结单后 / 服务段结束 / 礼物支付完成」由任务按规则表判定，
//     覆盖人员在后台自配的金额异常、更多问题类型与适用游戏 / SKU 范围。
//
// 两者对同一（来源单据 + 问题类型）建单时由 active_dedup_key 唯一索引兜底：
// 后到的调用返回 errConflict，命中留痕回填既有单据 ID，不产生第二张进行中售后单。
const (
	// afterSaleRuleAutoCreateEnabledKey 是自动建单总开关：仅字符串 "0" 关闭（只留痕 + 待办，不建单），
	// 其余（含未配置、空值、非法值）一律按开启处理——单条规则是否建单仍由规则的
	// auto_create 与 channel 决定。
	afterSaleRuleAutoCreateEnabledKey = "app.esports.aftersale_rule_auto_create_enabled"
	// afterSaleRuleChangedEventType 规则配置变更留痕事件（不匹配通知前缀，只做审计记录）。
	afterSaleRuleChangedEventType = "aftersale_rule.changed"
	// afterSaleRuleScanPageSize / afterSaleRuleScanMaxPages 限定单次任务对单条规则的扫描量，
	// 游标始终向前推进（按 ID 升序），已命中来源被跳过，因此多轮调度可逐步吃完历史积压。
	afterSaleRuleScanPageSize = 100
	afterSaleRuleScanMaxPages = 5
)

// afterSaleRuleInput 是规则写入入参（创建 / 更新共用；更新时 Version 为乐观锁版本号）。
type afterSaleRuleInput struct {
	Code           string `json:"code"`
	Name           string `json:"name"`
	SourceType     string `json:"source_type"`
	TriggerEvent   string `json:"trigger_event"`
	Metric         string `json:"metric"`
	ThresholdValue int64  `json:"threshold_value"`
	WindowMinutes  int    `json:"window_minutes"`
	ScopeGame      string `json:"scope_game"`
	ScopeSkuID     *int   `json:"scope_product_id"`
	AfterSaleKind  string `json:"after_sale_kind"`
	Channel        string `json:"channel"`
	FaultParty     string `json:"fault_party"`
	// AutoCreate 留空时按 true（schema 默认）处理：命中即建单。
	AutoCreate *bool  `json:"auto_create"`
	Priority   int    `json:"priority"`
	Note       string `json:"note"`
	Status     string `json:"status"`
	Version    int    `json:"version"`
}

// normalizeAfterSaleRuleInput 归一化并校验规则入参；非法组合一律明确报错，不静默回落。
func normalizeAfterSaleRuleInput(in afterSaleRuleInput) (afterSaleRuleInput, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	in.SourceType = strings.TrimSpace(in.SourceType)
	in.TriggerEvent = strings.TrimSpace(in.TriggerEvent)
	in.Metric = strings.TrimSpace(in.Metric)
	in.ScopeGame = strings.TrimSpace(in.ScopeGame)
	in.AfterSaleKind = strings.TrimSpace(in.AfterSaleKind)
	in.Channel = strings.TrimSpace(in.Channel)
	in.FaultParty = strings.TrimSpace(in.FaultParty)
	in.Note = strings.TrimSpace(in.Note)
	in.Status = strings.TrimSpace(in.Status)
	if in.SourceType == "" {
		in.SourceType = string(esportsaftersalerule.SourceTypeOrder)
	}
	if in.TriggerEvent == "" {
		in.TriggerEvent = string(esportsaftersalerule.TriggerEventOrderClosed)
	}
	if in.Metric == "" {
		in.Metric = string(esportsaftersalerule.MetricNone)
	}
	if in.AfterSaleKind == "" {
		in.AfterSaleKind = string(esportsaftersalerule.AfterSaleKindOther)
	}
	if in.Channel == "" {
		in.Channel = string(esportsaftersalerule.ChannelSystem)
	}
	if in.FaultParty == "" {
		in.FaultParty = string(esportsaftersalerule.FaultPartyUndetermined)
	}
	if in.Status == "" {
		in.Status = string(esportsaftersalerule.StatusDraft)
	}
	if !validAfterSaleRuleCode(in.Code) {
		return in, fmt.Errorf("%w：规则编码必填，只允许小写字母、数字与下划线，且不超过 48 字符", errInvalidInput)
	}
	if in.Name == "" || len([]rune(in.Name)) > 120 {
		return in, fmt.Errorf("%w：规则名称必填且不超过 120 字", errInvalidInput)
	}
	if len([]rune(in.Note)) > 255 {
		return in, fmt.Errorf("%w：规则备注不超过 255 字", errInvalidInput)
	}
	if !validAfterSaleSourceType(in.SourceType) {
		return in, fmt.Errorf("%w：规则来源类型不受支持", errInvalidInput)
	}
	if !validAfterSaleRuleTrigger(in.SourceType, in.TriggerEvent) {
		return in, fmt.Errorf("%w：该来源不支持所选触发时机（仅支持结单后 / 服务段结束 / 礼物支付完成）", errInvalidInput)
	}
	if !validAfterSaleRuleMetric(in.SourceType, in.Metric) {
		return in, fmt.Errorf("%w：该来源不支持所选判定口径", errInvalidInput)
	}
	if !validAfterSaleKindForSource(in.SourceType, in.AfterSaleKind) {
		return in, fmt.Errorf("%w：命中后生成的售后问题类型与来源不匹配", errInvalidInput)
	}
	if !validFaultParty(in.FaultParty) {
		return in, fmt.Errorf("%w：默认责任方取值非法", errInvalidInput)
	}
	switch in.Channel {
	case string(esportsaftersalerule.ChannelSystem), string(esportsaftersalerule.ChannelAdmin):
	default:
		return in, fmt.Errorf("%w：建单通道只能是 system（自动建单）或 admin（只推待办）", errInvalidInput)
	}
	switch in.Status {
	case string(esportsaftersalerule.StatusDraft), string(esportsaftersalerule.StatusActive),
		string(esportsaftersalerule.StatusDisabled):
	default:
		return in, fmt.Errorf("%w：规则状态只能是 draft / active / disabled", errInvalidInput)
	}
	if in.WindowMinutes < 0 || in.WindowMinutes > 60*24*365 {
		return in, fmt.Errorf("%w：观察窗口须为 0（不限时）到 525600 分钟之间", errInvalidInput)
	}
	if in.Priority < 0 || in.Priority > 1000 {
		return in, fmt.Errorf("%w：优先级须为 0–1000", errInvalidInput)
	}
	if in.ScopeSkuID != nil && *in.ScopeSkuID < 1 {
		return in, fmt.Errorf("%w：适用 SKU 取值非法", errInvalidInput)
	}
	if err := validateAfterSaleRuleThreshold(in.Metric, in.ThresholdValue); err != nil {
		return in, err
	}
	return in, nil
}

// validAfterSaleRuleCode 规则编码：小写字母 / 数字 / 下划线，1–48 字符（入库唯一）。
func validAfterSaleRuleCode(code string) bool {
	if code == "" || len(code) > 48 {
		return false
	}
	for _, r := range code {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// validAfterSaleRuleTrigger 校验「来源类型 ↔ 触发时机」组合。
// manual（仅人工触发）本轮未提供人工触发入口，明确报错而不是留下永不生效的配置。
func validAfterSaleRuleTrigger(sourceType, trigger string) bool {
	switch esportsaftersalerule.SourceType(sourceType) {
	case esportsaftersalerule.SourceTypeOrder:
		return trigger == string(esportsaftersalerule.TriggerEventOrderClosed) ||
			trigger == string(esportsaftersalerule.TriggerEventServiceSegmentFinished)
	case esportsaftersalerule.SourceTypeGift:
		return trigger == string(esportsaftersalerule.TriggerEventGiftPaid)
	}
	return false
}

// validAfterSaleRuleMetric 校验「来源类型 ↔ 判定口径」组合：礼物来源没有服务时长与 SKU 维度。
func validAfterSaleRuleMetric(sourceType, metric string) bool {
	switch esportsaftersalerule.SourceType(sourceType) {
	case esportsaftersalerule.SourceTypeOrder:
		switch esportsaftersalerule.Metric(metric) {
		case esportsaftersalerule.MetricAmountDeviationBp, esportsaftersalerule.MetricAmountOverCents,
			esportsaftersalerule.MetricDurationShortageMinutes, esportsaftersalerule.MetricDurationShortageBp,
			esportsaftersalerule.MetricNone:
			return true
		}
		return false
	case esportsaftersalerule.SourceTypeGift:
		switch esportsaftersalerule.Metric(metric) {
		case esportsaftersalerule.MetricAmountDeviationBp, esportsaftersalerule.MetricAmountOverCents,
			esportsaftersalerule.MetricNone:
			return true
		}
		return false
	}
	return false
}

// validateAfterSaleRuleThreshold 阈值单位随口径变化：基点 1–10000 / 分与分钟 ≥1 / 不判定口径必须为 0。
func validateAfterSaleRuleThreshold(metric string, threshold int64) error {
	switch esportsaftersalerule.Metric(metric) {
	case esportsaftersalerule.MetricNone:
		if threshold != 0 {
			return fmt.Errorf("%w：判定口径为 none 时阈值必须为 0", errInvalidInput)
		}
		return nil
	case esportsaftersalerule.MetricAmountDeviationBp, esportsaftersalerule.MetricDurationShortageBp:
		if threshold < 1 || threshold > 10000 {
			return fmt.Errorf("%w：基点阈值须为 1–10000", errInvalidInput)
		}
		return nil
	default:
		if threshold < 1 {
			return fmt.Errorf("%w：该口径的阈值须大于 0", errInvalidInput)
		}
		return nil
	}
}

// afterSaleRuleAutoCreateEnabled 读取自动建单总开关：仅 "0" 关闭，其余（含未配置）按开启处理。
func afterSaleRuleAutoCreateEnabled(ctx context.Context, db *ent.Client) bool {
	item, err := db.SystemConfig.Query().Where(
		systemconfig.KeyEQ(afterSaleRuleAutoCreateEnabledKey),
	).Only(ctx)
	if err != nil {
		return true
	}
	return strings.TrimSpace(item.Value) != "0"
}

// listAfterSaleRules 列出规则，status 为空返回全部；按优先级倒序、ID 升序。
func listAfterSaleRules(ctx context.Context, db *ent.Client, status string, limit int) ([]*ent.EsportsAfterSaleRule, error) {
	status = strings.TrimSpace(status)
	switch status {
	case "", string(esportsaftersalerule.StatusDraft), string(esportsaftersalerule.StatusActive),
		string(esportsaftersalerule.StatusDisabled):
	default:
		return nil, fmt.Errorf("%w：规则状态只能是 draft / active / disabled", errInvalidInput)
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsAfterSaleRule.Query()
	if status != "" {
		q = q.Where(esportsaftersalerule.StatusEQ(esportsaftersalerule.Status(status)))
	}
	return q.Order(ent.Desc(esportsaftersalerule.FieldPriority), ent.Asc(esportsaftersalerule.FieldID)).
		Limit(limit).All(ctx)
}

// getAfterSaleRule 读取单条规则。
func getAfterSaleRule(ctx context.Context, db *ent.Client, id int) (*ent.EsportsAfterSaleRule, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	return db.EsportsAfterSaleRule.Get(ctx, id)
}

// createAfterSaleRule 新建规则（同一事务内写配置变更留痕）。
func createAfterSaleRule(ctx context.Context, db *ent.Client, adminID int, in afterSaleRuleInput, now time.Time) (*ent.EsportsAfterSaleRule, error) {
	in, err := normalizeAfterSaleRuleInput(in)
	if err != nil {
		return nil, err
	}
	if adminID < 1 {
		return nil, errForbidden
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	create := tx.EsportsAfterSaleRule.Create().SetCode(in.Code).SetName(in.Name).
		SetSourceType(esportsaftersalerule.SourceType(in.SourceType)).
		SetTriggerEvent(esportsaftersalerule.TriggerEvent(in.TriggerEvent)).
		SetMetric(esportsaftersalerule.Metric(in.Metric)).
		SetThresholdValue(in.ThresholdValue).SetWindowMinutes(in.WindowMinutes).
		SetScopeGame(in.ScopeGame).SetAfterSaleKind(esportsaftersalerule.AfterSaleKind(in.AfterSaleKind)).
		SetChannel(esportsaftersalerule.Channel(in.Channel)).
		SetFaultParty(esportsaftersalerule.FaultParty(in.FaultParty)).
		SetAutoCreate(afterSaleRuleAutoCreateValue(in)).
		SetPriority(in.Priority).SetNote(in.Note).SetStatus(esportsaftersalerule.Status(in.Status))
	if in.ScopeSkuID != nil {
		create.SetScopeSkuID(*in.ScopeSkuID)
	}
	item, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, fmt.Errorf("%w：规则编码 %s 已存在", errConflict, in.Code)
		}
		return nil, err
	}
	if err = recordAfterSaleRuleChange(ctx, tx, adminID, "created", nil, item, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSaleRule.Get(ctx, item.ID)
}

// afterSaleRuleAutoCreateValue 命中是否建单：未显式给出时按 schema 默认 true。
func afterSaleRuleAutoCreateValue(in afterSaleRuleInput) bool {
	if in.AutoCreate == nil {
		return true
	}
	return *in.AutoCreate
}

// updateAfterSaleRule 原地编辑规则：按 version 乐观锁更新，编码不可修改（Immutable）。
// 未带版本号时按当前版本更新，仍受唯一约束保护。
func updateAfterSaleRule(ctx context.Context, db *ent.Client, id, adminID int, in afterSaleRuleInput, now time.Time) (*ent.EsportsAfterSaleRule, error) {
	in, err := normalizeAfterSaleRuleInput(in)
	if err != nil {
		return nil, err
	}
	if id < 1 || adminID < 1 {
		return nil, errForbidden
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := tx.EsportsAfterSaleRule.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Code != "" && in.Code != current.Code {
		return nil, fmt.Errorf("%w：规则编码创建后不可修改", errInvalidInput)
	}
	version := in.Version
	if version < 1 {
		version = current.Version
	}
	update := tx.EsportsAfterSaleRule.Update().Where(
		esportsaftersalerule.IDEQ(id),
		esportsaftersalerule.VersionEQ(version),
	).SetName(in.Name).
		SetSourceType(esportsaftersalerule.SourceType(in.SourceType)).
		SetTriggerEvent(esportsaftersalerule.TriggerEvent(in.TriggerEvent)).
		SetMetric(esportsaftersalerule.Metric(in.Metric)).
		SetThresholdValue(in.ThresholdValue).SetWindowMinutes(in.WindowMinutes).
		SetScopeGame(in.ScopeGame).SetAfterSaleKind(esportsaftersalerule.AfterSaleKind(in.AfterSaleKind)).
		SetChannel(esportsaftersalerule.Channel(in.Channel)).
		SetFaultParty(esportsaftersalerule.FaultParty(in.FaultParty)).
		SetAutoCreate(afterSaleRuleAutoCreateValue(in)).
		SetPriority(in.Priority).SetNote(in.Note).SetStatus(esportsaftersalerule.Status(in.Status)).
		SetVersion(version + 1)
	if in.ScopeSkuID != nil {
		update.SetScopeSkuID(*in.ScopeSkuID)
	} else {
		update.ClearScopeSkuID()
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, fmt.Errorf("%w：规则已被他人修改，请刷新后重试", errConflict)
	}
	updated, err := tx.EsportsAfterSaleRule.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = recordAfterSaleRuleChange(ctx, tx, adminID, "updated", current, updated, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSaleRule.Get(ctx, id)
}

// deleteAfterSaleRule 删除规则：已产生命中留痕的规则不可删除（否则命中记录无法回溯到规则），
// 明确提示改为停用；未命中的规则可删除并留配置变更留痕。
func deleteAfterSaleRule(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) error {
	if id < 1 || adminID < 1 {
		return errForbidden
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := tx.EsportsAfterSaleRule.Get(ctx, id)
	if err != nil {
		return err
	}
	hits, err := tx.EsportsAfterSaleRuleHit.Query().Where(
		esportsaftersalerulehit.RuleIDEQ(id),
	).Count(ctx)
	if err != nil {
		return err
	}
	if hits > 0 {
		return fmt.Errorf("%w：该规则已产生 %d 条命中留痕，不可删除，请改为停用（status=disabled）", errConflict, hits)
	}
	if err = tx.EsportsAfterSaleRule.DeleteOneID(id).Exec(ctx); err != nil {
		return err
	}
	if err = recordAfterSaleRuleChange(ctx, tx, adminID, "deleted", current, nil, now); err != nil {
		return err
	}
	return tx.Commit()
}

// listAfterSaleRuleHits 列出命中留痕；ruleID / sourceType 为 0 / 空表示不过滤；
// pendingOnly=true 只返回未自动建单的待办命中（after_sale_id=0）。
func listAfterSaleRuleHits(ctx context.Context, db *ent.Client, ruleID int, sourceType string, pendingOnly bool, limit int) ([]*ent.EsportsAfterSaleRuleHit, error) {
	sourceType = strings.TrimSpace(sourceType)
	if sourceType != "" && !validAfterSaleSourceType(sourceType) {
		return nil, fmt.Errorf("%w：来源类型不受支持", errInvalidInput)
	}
	if ruleID < 0 {
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsAfterSaleRuleHit.Query()
	if ruleID > 0 {
		q = q.Where(esportsaftersalerulehit.RuleIDEQ(ruleID))
	}
	if sourceType != "" {
		q = q.Where(esportsaftersalerulehit.SourceTypeEQ(sourceType))
	}
	if pendingOnly {
		q = q.Where(esportsaftersalerulehit.AfterSaleIDEQ(0))
	}
	return q.Order(ent.Desc(esportsaftersalerulehit.FieldID)).Limit(limit).All(ctx)
}

// recordAfterSaleRuleChange 写规则配置变更留痕（审计用，不触发通知）。
func recordAfterSaleRuleChange(ctx context.Context, tx *ent.Tx, adminID int, action string,
	before, after *ent.EsportsAfterSaleRule, now time.Time) error {
	rule := after
	if rule == nil {
		rule = before
	}
	if rule == nil {
		return errInvalidInput
	}
	payload := map[string]interface{}{
		"action": action, "admin_id": adminID, "rule_id": rule.ID, "rule_code": rule.Code,
		"changed_at": now.UTC().Format(time.RFC3339),
	}
	if before != nil {
		payload["before"] = afterSaleRuleSnapshot(before)
	}
	if after != nil {
		payload["after"] = afterSaleRuleSnapshot(after)
	}
	if _, err := tx.EsportsOutbox.Create().
		SetEventKey(afterSaleRuleChangedEventType + ":" + rule.Code + ":" + uuid.NewString()).
		SetEventType(afterSaleRuleChangedEventType).SetAggregateRef(rule.Code).
		SetCreatedAt(now.UTC()).
		SetPayload(payload).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil
		}
		return err
	}
	return nil
}

// afterSaleRuleSnapshot 规则可变字段快照，用于留痕改前 / 改后。
func afterSaleRuleSnapshot(rule *ent.EsportsAfterSaleRule) map[string]interface{} {
	snapshot := map[string]interface{}{
		"name": rule.Name, "source_type": rule.SourceType.String(),
		"trigger_event": rule.TriggerEvent.String(), "metric": rule.Metric.String(),
		"threshold_value": rule.ThresholdValue, "window_minutes": rule.WindowMinutes,
		"scope_game": rule.ScopeGame, "after_sale_kind": rule.AfterSaleKind.String(),
		"channel": rule.Channel.String(), "fault_party": rule.FaultParty.String(),
		"auto_create": rule.AutoCreate, "priority": rule.Priority,
		"note": rule.Note, "status": rule.Status.String(), "version": rule.Version,
	}
	if rule.ScopeSkuID != nil {
		snapshot["scope_product_id"] = *rule.ScopeSkuID
	}
	return snapshot
}

// afterSaleRuleOrderFacts 订单来源规则判定所需的只读事实快照。
type afterSaleRuleOrderFacts struct {
	Order *ent.EsportsOrder
	// ExpectedCents 与下单 / 改价同一口径的应付金额：数量×单价 − 会员折扣 + 人工调整。
	ExpectedCents int64
	// PromisedMinutes 订单承诺服务分钟数（每席承诺时长 × 席位数）。
	PromisedMinutes int64
	// ServedMinutes 系统计时汇总的服务分钟数（仅已结束服务段）。
	ServedMinutes int64
}

// loadAfterSaleRuleOrderFacts 读取订单判定事实；不满足规则适用范围（游戏 / SKU）时返回 (nil, nil)，
// 表示该来源不在规则作用域内，不判定也不留痕。
func loadAfterSaleRuleOrderFacts(ctx context.Context, client *ent.Client, rule *ent.EsportsAfterSaleRule, orderID int) (*afterSaleRuleOrderFacts, error) {
	order, err := client.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if rule.ScopeSkuID != nil && order.SkuID != *rule.ScopeSkuID {
		return nil, nil
	}
	if rule.ScopeGame != "" {
		// 游戏维度来自 SKU 所属商品分类（SKU 本身不落游戏字段）：分类缺失视为无法核对，不判定。
		sku, err := client.EsportsSku.Get(ctx, order.SkuID)
		if ent.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if sku.CategoryID == nil {
			return nil, nil
		}
		category, err := client.EsportsProductCategory.Get(ctx, *sku.CategoryID)
		if ent.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if category.Game != rule.ScopeGame {
			return nil, nil
		}
	}
	seats := int64(order.RequiredSeats)
	if seats < 1 {
		seats = 1
	}
	facts := &afterSaleRuleOrderFacts{
		Order:           order,
		ExpectedCents:   order.Quantity*order.UnitPriceCents - order.DiscountCents + order.ManualAdjustCents,
		PromisedMinutes: int64(order.ServiceDurationMinutes) * seats,
	}
	segments, err := client.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusFinished),
	).Select(esportsservicesegment.FieldSystemSeconds).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, segment := range segments {
		facts.ServedMinutes += segment.SystemSeconds / 60
	}
	return facts, nil
}

// afterSaleRuleMetricValue 计算订单来源的度量值；ok=false 表示该来源缺少判定所需数据（不命中、不误报）。
func afterSaleRuleMetricValue(rule *ent.EsportsAfterSaleRule, facts *afterSaleRuleOrderFacts) (int64, bool) {
	if facts == nil || facts.Order == nil {
		return 0, false
	}
	switch rule.Metric {
	case esportsaftersalerule.MetricAmountOverCents:
		return facts.Order.PayableCents, true
	case esportsaftersalerule.MetricAmountDeviationBp:
		return deviationBp(facts.Order.PayableCents, facts.ExpectedCents)
	case esportsaftersalerule.MetricDurationShortageMinutes:
		if facts.PromisedMinutes <= 0 {
			return 0, false
		}
		shortage := facts.PromisedMinutes - facts.ServedMinutes
		if shortage < 0 {
			shortage = 0
		}
		return shortage, true
	case esportsaftersalerule.MetricDurationShortageBp:
		if facts.PromisedMinutes <= 0 {
			return 0, false
		}
		shortage := facts.PromisedMinutes - facts.ServedMinutes
		if shortage <= 0 {
			return 0, true
		}
		return shortage * 10000 / facts.PromisedMinutes, true
	}
	return 0, false
}

// deviationBp 计算实际值相对期望值的偏离基点；期望值 ≤0 时不可判定。
func deviationBp(actual, expected int64) (int64, bool) {
	if expected <= 0 {
		return 0, false
	}
	diff := actual - expected
	if diff < 0 {
		diff = -diff
	}
	return diff * 10000 / expected, true
}

// afterSaleRuleGiftMetricValue 计算礼物来源的度量值；礼物没有时长与 SKU 维度，配了该范围即不判定。
func afterSaleRuleGiftMetricValue(ctx context.Context, client *ent.Client, rule *ent.EsportsAfterSaleRule, giftOrderID int) (int64, bool, error) {
	if rule.ScopeSkuID != nil || rule.ScopeGame != "" {
		return 0, false, nil
	}
	gift, err := client.EsportsGiftOrder.Get(ctx, giftOrderID)
	if err != nil {
		if ent.IsNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	switch rule.Metric {
	case esportsaftersalerule.MetricAmountOverCents:
		return gift.AmountCents, true, nil
	case esportsaftersalerule.MetricAmountDeviationBp:
		value, ok := deviationBp(gift.AmountCents, int64(gift.Quantity)*gift.UnitPriceCents)
		return value, ok, nil
	}
	return 0, false, nil
}

// afterSaleRuleMatched 命中判定：度量值严格大于阈值即命中；metric=none 永不命中。
func afterSaleRuleMatched(rule *ent.EsportsAfterSaleRule, value int64) bool {
	if rule.Metric == esportsaftersalerule.MetricNone {
		return false
	}
	return value > rule.ThresholdValue
}

// afterSaleRuleHitDedupKey 命中幂等键：<规则编码>:<来源类型>:<来源单据 ID>，唯一索引兜底重复命中。
func afterSaleRuleHitDedupKey(ruleCode, sourceType string, sourceID int) string {
	return ruleCode + ":" + sourceType + ":" + strconv.Itoa(sourceID)
}

// scanAfterSaleRules 按 active 规则扫描结单后 / 服务段结束 / 礼物支付完成的异常并建单。
// limit 是单次最多判定的来源单据数（默认 200，上限 1000），返回命中条数。
func scanAfterSaleRules(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if db == nil {
		return 0, errInvalidInput
	}
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	rules, err := db.EsportsAfterSaleRule.Query().Where(
		esportsaftersalerule.StatusEQ(esportsaftersalerule.StatusActive),
		esportsaftersalerule.TriggerEventIn(
			esportsaftersalerule.TriggerEventOrderClosed,
			esportsaftersalerule.TriggerEventServiceSegmentFinished,
			esportsaftersalerule.TriggerEventGiftPaid,
		),
	).Order(ent.Desc(esportsaftersalerule.FieldPriority), ent.Asc(esportsaftersalerule.FieldID)).All(ctx)
	if err != nil {
		return 0, err
	}
	if len(rules) == 0 {
		return 0, nil
	}
	autoCreateEnabled := afterSaleRuleAutoCreateEnabled(ctx, db)
	hits, processed := 0, 0
	for _, rule := range rules {
		ruleHits, ruleProcessed, err := runAfterSaleRule(ctx, db, rule, now, limit-processed, autoCreateEnabled)
		hits += ruleHits
		processed += ruleProcessed
		if err != nil {
			return hits, err
		}
		if processed >= limit {
			break
		}
	}
	return hits, nil
}

// runAfterSaleRule 处理单条规则：按 ID 升序分页扫描候选来源，已命中来源直接跳过，
// 游标始终前进，因此多轮调度能逐步覆盖历史积压而不重复处理。
func runAfterSaleRule(ctx context.Context, db *ent.Client, rule *ent.EsportsAfterSaleRule,
	now time.Time, budget int, autoCreateEnabled bool) (int, int, error) {
	if rule == nil || budget <= 0 {
		return 0, 0, nil
	}
	hits, processed := 0, 0
	cursor := 0
	for page := 0; page < afterSaleRuleScanMaxPages; page++ {
		sourceIDs, next, err := afterSaleRuleCandidateSources(ctx, db, rule, now, cursor, afterSaleRuleScanPageSize)
		if err != nil {
			return hits, processed, err
		}
		if len(sourceIDs) == 0 {
			return hits, processed, nil
		}
		for _, sourceID := range sourceIDs {
			created, err := evaluateAfterSaleRuleSource(ctx, db, rule, sourceID, now, autoCreateEnabled)
			if err != nil {
				return hits, processed, err
			}
			processed++
			if created {
				hits++
			}
			if processed >= budget {
				return hits, processed, nil
			}
		}
		cursor = next
		if len(sourceIDs) < afterSaleRuleScanPageSize {
			return hits, processed, nil
		}
	}
	return hits, processed, nil
}

// afterSaleRuleCandidateSources 返回该规则一页候选来源单据 ID 与下一页游标（按各自 ID 升序）。
func afterSaleRuleCandidateSources(ctx context.Context, db *ent.Client, rule *ent.EsportsAfterSaleRule,
	now time.Time, cursor, pageSize int) ([]int, int, error) {
	windowStart := now.UTC().Add(-time.Duration(rule.WindowMinutes) * time.Minute)
	switch rule.TriggerEvent {
	case esportsaftersalerule.TriggerEventOrderClosed:
		q := db.EsportsOrder.Query().Where(
			esportsorder.IDGT(cursor),
			esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusCompleted),
		)
		if rule.WindowMinutes > 0 {
			q = q.Where(esportsorder.ClosedAtNotNil(), esportsorder.ClosedAtGTE(windowStart))
		}
		rows, err := q.Order(ent.Asc(esportsorder.FieldID)).Limit(pageSize).
			Select(esportsorder.FieldID).All(ctx)
		if err != nil {
			return nil, cursor, err
		}
		ids := make([]int, 0, len(rows))
		for _, order := range rows {
			ids = append(ids, order.ID)
			cursor = order.ID
		}
		return ids, cursor, nil
	case esportsaftersalerule.TriggerEventServiceSegmentFinished:
		q := db.EsportsServiceSegment.Query().Where(
			esportsservicesegment.IDGT(cursor),
			esportsservicesegment.StatusEQ(esportsservicesegment.StatusFinished),
		)
		if rule.WindowMinutes > 0 {
			q = q.Where(esportsservicesegment.EndedAtNotNil(), esportsservicesegment.EndedAtGTE(windowStart))
		}
		rows, err := q.Order(ent.Asc(esportsservicesegment.FieldID)).Limit(pageSize).
			Select(esportsservicesegment.FieldID, esportsservicesegment.FieldOrderID).All(ctx)
		if err != nil {
			return nil, cursor, err
		}
		seen := make(map[int]struct{}, len(rows))
		ids := make([]int, 0, len(rows))
		next := cursor
		for _, segment := range rows {
			next = segment.ID
			if _, dup := seen[segment.OrderID]; dup {
				continue
			}
			seen[segment.OrderID] = struct{}{}
			ids = append(ids, segment.OrderID)
		}
		return ids, next, nil
	case esportsaftersalerule.TriggerEventGiftPaid:
		q := db.EsportsGiftOrder.Query().Where(
			esportsgiftorder.IDGT(cursor),
			esportsgiftorder.StatusEQ(esportsgiftorder.StatusPaid),
		)
		if rule.WindowMinutes > 0 {
			q = q.Where(esportsgiftorder.CreatedAtGTE(windowStart))
		}
		rows, err := q.Order(ent.Asc(esportsgiftorder.FieldID)).Limit(pageSize).
			Select(esportsgiftorder.FieldID).All(ctx)
		if err != nil {
			return nil, cursor, err
		}
		ids := make([]int, 0, len(rows))
		next := cursor
		for _, gift := range rows {
			ids = append(ids, gift.ID)
			next = gift.ID
		}
		return ids, next, nil
	}
	return nil, cursor, nil
}

// evaluateAfterSaleRuleSource 对单个来源判定：已命中直接跳过；不满足范围 / 不可计算 → 不判定；
// 命中则写留痕并按规则动作建单。
func evaluateAfterSaleRuleSource(ctx context.Context, db *ent.Client, rule *ent.EsportsAfterSaleRule,
	sourceID int, now time.Time, autoCreateEnabled bool) (bool, error) {
	dedupKey := afterSaleRuleHitDedupKey(rule.Code, rule.SourceType.String(), sourceID)
	exists, err := db.EsportsAfterSaleRuleHit.Query().Where(
		esportsaftersalerulehit.DedupKeyEQ(dedupKey),
	).Exist(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	var value int64
	var ok bool
	switch rule.SourceType {
	case esportsaftersalerule.SourceTypeOrder:
		facts, err := loadAfterSaleRuleOrderFacts(ctx, db, rule, sourceID)
		if err != nil {
			if ent.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		value, ok = afterSaleRuleMetricValue(rule, facts)
	case esportsaftersalerule.SourceTypeGift:
		value, ok, err = afterSaleRuleGiftMetricValue(ctx, db, rule, sourceID)
		if err != nil {
			return false, err
		}
	default:
		return false, nil
	}
	if !ok || !afterSaleRuleMatched(rule, value) {
		return false, nil
	}
	return recordAfterSaleRuleHit(ctx, db, rule, sourceID, value, now, autoCreateEnabled)
}

// recordAfterSaleRuleHit 在同一事务内写命中留痕并按规则动作建单：
//   - channel=system 且 auto_create 且总开关开启 → 建 channel=system 的售后单并把 ID 回填留痕；
//   - 否则（channel=admin / auto_create=false / 总开关关闭）只留痕，after_sale_id=0 即为客服待办；
//   - 已存在同来源同问题进行中售后单时返回 errConflict，留痕回填既有单据 ID，不重复建单。
func recordAfterSaleRuleHit(ctx context.Context, db *ent.Client, rule *ent.EsportsAfterSaleRule,
	sourceID int, value int64, now time.Time, autoCreateEnabled bool) (bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	dedupKey := afterSaleRuleHitDedupKey(rule.Code, rule.SourceType.String(), sourceID)
	exists, err := tx.EsportsAfterSaleRuleHit.Query().Where(
		esportsaftersalerulehit.DedupKeyEQ(dedupKey),
	).Exist(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	create := tx.EsportsAfterSaleRuleHit.Create().SetRuleID(rule.ID).SetRuleCode(rule.Code).
		SetSourceType(rule.SourceType.String()).SetSourceID(sourceID).
		SetMetricValue(value).SetThresholdValue(rule.ThresholdValue).SetDedupKey(dedupKey)
	afterSaleID := 0
	autoCreate := rule.AutoCreate && autoCreateEnabled &&
		rule.Channel == esportsaftersalerule.ChannelSystem &&
		rule.Metric != esportsaftersalerule.MetricNone
	if autoCreate {
		item, err := createAfterSaleTx(ctx, tx, afterSaleInput{
			SourceType:   rule.SourceType.String(),
			SourceID:     sourceID,
			Kind:         rule.AfterSaleKind.String(),
			Channel:      "system",
			Demand:       afterSaleRuleDemand(rule, value),
			ExpectedPlan: truncateRunes("按规则「"+rule.Name+"」核实后处置："+rule.Note, 255),
		}, afterSaleCreateOptions{
			ActorType: "system", ActorID: "aftersale_rule:" + rule.Code,
			Details: map[string]interface{}{
				"auto_created": true, "rule_id": rule.ID, "rule_code": rule.Code,
				"metric": rule.Metric.String(), "metric_value": value,
				"threshold_value": rule.ThresholdValue,
			},
		})
		switch {
		case err == nil:
			afterSaleID = item.ID
			if rule.FaultParty != esportsaftersalerule.FaultPartyUndetermined {
				if _, err = tx.EsportsAfterSale.UpdateOneID(item.ID).
					SetFaultParty(esportsaftersale.FaultParty(rule.FaultParty.String())).Save(ctx); err != nil {
					return false, err
				}
			}
		case errors.Is(err, errConflict):
			// 同来源同问题已有进行中售后单（含 player.go 的硬编码建单分支）：留痕指向既有单据。
			existing, qerr := tx.EsportsAfterSale.Query().Where(
				esportsaftersale.SourceTypeEQ(esportsaftersale.SourceType(rule.SourceType.String())),
				esportsaftersale.SourceIDEQ(sourceID),
				esportsaftersale.KindEQ(esportsaftersale.Kind(rule.AfterSaleKind.String())),
				esportsaftersale.StatusIn(activeAfterSaleStatuses()...),
			).Order(ent.Desc(esportsaftersale.FieldID)).First(ctx)
			if qerr != nil && !ent.IsNotFound(qerr) {
				return false, qerr
			}
			if existing == nil {
				// 冲突并非来自「已有进行中售后单」（如订单版本并发冲突）：**不写留痕**并回滚，
				// 让下一轮调度重试，避免把未建单的来源永久标成已处理。
				return false, nil
			}
			afterSaleID = existing.ID
		default:
			return false, err
		}
	}
	if _, err = create.SetAfterSaleID(afterSaleID).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			// 并发下另一轮调度已写入同一条命中留痕：不重复建单。
			return false, nil
		}
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// afterSaleRuleDemand 命中建单的诉求文案（含实测值与阈值，便于客服核实）。
func afterSaleRuleDemand(rule *ent.EsportsAfterSaleRule, value int64) string {
	return truncateRunes(fmt.Sprintf("系统规则「%s」命中%s：实测 %s，阈值 %s，请核实并处置",
		rule.Name, afterSaleRuleMetricLabel(rule.Metric),
		afterSaleRuleMetricText(rule.Metric, value),
		afterSaleRuleMetricText(rule.Metric, rule.ThresholdValue)), 500)
}

// afterSaleRuleMetricLabel 判定口径的中文名。
func afterSaleRuleMetricLabel(metric esportsaftersalerule.Metric) string {
	switch metric {
	case esportsaftersalerule.MetricAmountDeviationBp:
		return "订单金额偏离"
	case esportsaftersalerule.MetricAmountOverCents:
		return "订单金额超阈值"
	case esportsaftersalerule.MetricDurationShortageMinutes:
		return "服务时长短缺"
	case esportsaftersalerule.MetricDurationShortageBp:
		return "服务时长短缺比例"
	}
	return "人工配置条件"
}

// afterSaleRuleMetricText 按口径把整数度量值格式化为中文（基点换算百分比用整数运算，不用浮点）。
func afterSaleRuleMetricText(metric esportsaftersalerule.Metric, value int64) string {
	switch metric {
	case esportsaftersalerule.MetricAmountDeviationBp, esportsaftersalerule.MetricDurationShortageBp:
		return fmt.Sprintf("%d 基点（%d.%02d%%）", value, value/100, value%100)
	case esportsaftersalerule.MetricAmountOverCents:
		return fmt.Sprintf("%d 分", value)
	case esportsaftersalerule.MetricDurationShortageMinutes:
		return fmt.Sprintf("%d 分钟", value)
	}
	return strconv.FormatInt(value, 10)
}

// scanAfterSaleRulesTask 是 `esports_scan_aftersale_rules` 的调度包装（CronJob 表达式
// `0 * * * * *`；注册与 cron_seeds 种子由主控补齐）。
func scanAfterSaleRulesTask(ctx context.Context, logger io.Writer) error {
	count, err := scanAfterSaleRules(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "售后规则命中建单：%d\n", count)
	}
	return err
}

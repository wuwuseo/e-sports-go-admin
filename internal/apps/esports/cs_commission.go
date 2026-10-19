package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportscscommissionrule"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/ent/esportswallettx"
)

// csCommissionRuleInput 客服提成规则可编辑字段（§12.2）。rule_no 创建后不可变。
type csCommissionRuleInput struct {
	RuleNo       string                    `json:"rule_no"`
	Scope        string                    `json:"scope"`
	ScopeKey     string                    `json:"scope_key"`
	BaseMode     string                    `json:"base_mode"`
	CalcMode     string                    `json:"calc_mode"`
	IncludeGifts bool                      `json:"include_gifts"`
	ValueBp      int                       `json:"value_bp"`
	ValueCents   int64                     `json:"value_cents"`
	Tiers        []schema.CsCommissionTier `json:"tiers"`
	Period       string                    `json:"period"`
	Note         string                    `json:"note"`
	EffectiveAt  *time.Time                `json:"effective_at"`
}

// validateCsCommissionRule 校验客服提成规则配置。任一不满足返回 errInvalidInput。
func validateCsCommissionRule(in csCommissionRuleInput) error {
	if len([]rune(strings.TrimSpace(in.RuleNo))) > 48 || len(strings.TrimSpace(in.ScopeKey)) > 64 ||
		len([]rune(strings.TrimSpace(in.Note))) > 255 {
		return errInvalidInput
	}
	switch in.Scope {
	case "global":
	case "admin":
		// scope=admin 时 scope_key 为目标客服的管理员 ID。
		if id, err := strconv.Atoi(strings.TrimSpace(in.ScopeKey)); err != nil || id < 1 {
			return errInvalidInput
		}
	default:
		return errInvalidInput
	}
	switch in.BaseMode {
	case "order_amount", "order_contribution", "order_count":
	default:
		return errInvalidInput
	}
	switch in.Period {
	case "daily", "weekly", "monthly":
	default:
		return errInvalidInput
	}
	switch in.CalcMode {
	case "percent":
		// 单量基数无法按比例计价（1 分的万分比恒为 0），直接拒绝以免产出恒零提成。
		if in.BaseMode == "order_count" {
			return errInvalidInput
		}
		prev := int64(0)
		for i, tier := range in.Tiers {
			if tier.ThresholdBaseCents <= 0 || tier.ValueBp <= 0 || tier.ValueBp > 10000 {
				return errInvalidInput
			}
			if i > 0 && tier.ThresholdBaseCents <= prev {
				return errInvalidInput
			}
			prev = tier.ThresholdBaseCents
		}
		// 有阶梯时 value_bp 作为兜底；没阶梯时必须给出 value_bp。
		if len(in.Tiers) == 0 && (in.ValueBp <= 0 || in.ValueBp > 10000) {
			return errInvalidInput
		}
		if len(in.Tiers) > 0 && in.ValueBp < 0 {
			return errInvalidInput
		}
	case "fixed_per_order":
		if in.ValueCents <= 0 {
			return errInvalidInput
		}
	default:
		return errInvalidInput
	}
	return nil
}

// createCsCommissionRule 新建客服提成规则（一律 draft）。同一 rule_no 已存在则版本递增。
func createCsCommissionRule(ctx context.Context, db *ent.Client, in csCommissionRuleInput, adminID int, now time.Time) (*ent.EsportsCsCommissionRule, error) {
	in.RuleNo = strings.TrimSpace(in.RuleNo)
	in.ScopeKey = strings.TrimSpace(in.ScopeKey)
	in.Note = strings.TrimSpace(in.Note)
	if err := validateCsCommissionRule(in); err != nil {
		return nil, err
	}
	tiers := in.Tiers
	if tiers == nil {
		tiers = []schema.CsCommissionTier{}
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ruleNo := in.RuleNo
	version := 1
	if ruleNo != "" {
		latest, err := tx.EsportsCsCommissionRule.Query().Where(
			esportscscommissionrule.RuleNoEQ(ruleNo),
		).Order(ent.Desc(esportscscommissionrule.FieldVersion)).First(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, err
		}
		if latest != nil {
			version = latest.Version + 1
		}
	} else {
		ruleNo = "CC" + uuid.NewString()
	}
	create := tx.EsportsCsCommissionRule.Create().SetRuleNo(ruleNo).SetVersion(version).
		SetStatus(esportscscommissionrule.StatusDraft).
		SetScope(esportscscommissionrule.Scope(in.Scope)).SetScopeKey(in.ScopeKey).
		SetBaseMode(esportscscommissionrule.BaseMode(in.BaseMode)).
		SetCalcMode(esportscscommissionrule.CalcMode(in.CalcMode)).
		SetIncludeGifts(in.IncludeGifts).
		SetValueBp(in.ValueBp).SetValueCents(in.ValueCents).SetTiers(tiers).
		SetPeriod(esportscscommissionrule.Period(in.Period)).
		SetNote(in.Note).SetCreatedByAdminID(adminID)
	if in.EffectiveAt != nil {
		create.SetEffectiveAt(in.EffectiveAt.UTC())
	}
	rule, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCsCommissionRule.Get(ctx, rule.ID)
}

// activateCsCommissionRule 生效指定规则：未来时间进入 scheduled，到点后可解析并由任务转 active。
func activateCsCommissionRule(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsCsCommissionRule, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	target, err := tx.EsportsCsCommissionRule.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if target.Status != esportscscommissionrule.StatusDraft {
		return nil, errConflict
	}
	if target.EffectiveAt != nil && target.EffectiveAt.After(now.UTC()) {
		if _, err = tx.EsportsCsCommissionRule.Update().Where(
			esportscscommissionrule.RuleNoEQ(target.RuleNo),
			esportscscommissionrule.StatusEQ(esportscscommissionrule.StatusScheduled),
		).SetStatus(esportscscommissionrule.StatusHistory).Save(ctx); err != nil {
			return nil, err
		}
		changed, err := tx.EsportsCsCommissionRule.Update().Where(
			esportscscommissionrule.IDEQ(id), esportscscommissionrule.StatusEQ(esportscscommissionrule.StatusDraft),
		).SetStatus(esportscscommissionrule.StatusScheduled).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return db.EsportsCsCommissionRule.Get(ctx, id)
	}
	if _, err = tx.EsportsCsCommissionRule.Update().Where(
		esportscscommissionrule.RuleNoEQ(target.RuleNo),
		esportscscommissionrule.StatusIn(esportscscommissionrule.StatusActive, esportscscommissionrule.StatusScheduled),
	).SetStatus(esportscscommissionrule.StatusHistory).Save(ctx); err != nil {
		return nil, err
	}
	changed, err := tx.EsportsCsCommissionRule.Update().Where(
		esportscscommissionrule.IDEQ(id),
		esportscscommissionrule.StatusEQ(esportscscommissionrule.StatusDraft),
	).SetStatus(esportscscommissionrule.StatusActive).SetEffectiveAt(now.UTC()).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCsCommissionRule.Get(ctx, id)
}

// rollbackCsCommissionRule 回滚到指定版本：以该行为模板新建 max version+1 的直接 active 行，
// 并把原 active 转 history。原历史行不可变（§12.4）。
func rollbackCsCommissionRule(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsCsCommissionRule, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	source, err := tx.EsportsCsCommissionRule.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	latest, err := tx.EsportsCsCommissionRule.Query().Where(
		esportscscommissionrule.RuleNoEQ(source.RuleNo),
	).Order(ent.Desc(esportscscommissionrule.FieldVersion)).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	next := 1
	if latest != nil {
		next = latest.Version + 1
	}
	rule, err := tx.EsportsCsCommissionRule.Create().SetRuleNo(source.RuleNo).SetVersion(next).
		SetStatus(esportscscommissionrule.StatusActive).
		SetScope(source.Scope).SetScopeKey(source.ScopeKey).
		SetBaseMode(source.BaseMode).SetCalcMode(source.CalcMode).
		SetIncludeGifts(source.IncludeGifts).
		SetValueBp(source.ValueBp).SetValueCents(source.ValueCents).
		SetTiers(source.Tiers).SetPeriod(source.Period).
		SetNote(source.Note).SetEffectiveAt(now.UTC()).
		SetCreatedByAdminID(adminID).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	// 同编号当前生效版本转 history（回滚源可能本身不是 active）。
	if _, err = tx.EsportsCsCommissionRule.Update().Where(
		esportscscommissionrule.RuleNoEQ(source.RuleNo),
		esportscscommissionrule.StatusIn(esportscscommissionrule.StatusActive, esportscscommissionrule.StatusScheduled),
		esportscscommissionrule.IDNEQ(rule.ID),
	).SetStatus(esportscscommissionrule.StatusHistory).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCsCommissionRule.Get(ctx, rule.ID)
}

// listCsCommissionRules 列出客服提成规则，可按 rule_no 过滤，按 ID 倒序。
func listCsCommissionRules(ctx context.Context, db *ent.Client, ruleNo string, limit int) ([]*ent.EsportsCsCommissionRule, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsCsCommissionRule.Query()
	if ruleNo = strings.TrimSpace(ruleNo); ruleNo != "" {
		q.Where(esportscscommissionrule.RuleNoEQ(ruleNo))
	}
	return q.Order(ent.Desc(esportscscommissionrule.FieldID)).Limit(limit).All(ctx)
}

// resolveCsCommissionRuleIn 解析某客服适用的生效提成规则：admin 层优先于 global，同层取 version 最大。
// 无规则返回 nil, nil。
func resolveCsCommissionRuleIn(ctx context.Context, rules *ent.EsportsCsCommissionRuleClient, adminID int, at time.Time) (*ent.EsportsCsCommissionRule, error) {
	active, err := rules.Query().Where(
		esportscscommissionrule.StatusIn(esportscscommissionrule.StatusActive, esportscscommissionrule.StatusScheduled),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}
	pick := func(scope esportscscommissionrule.Scope, key string) *ent.EsportsCsCommissionRule {
		var best *ent.EsportsCsCommissionRule
		for _, rule := range active {
			if rule.Scope != scope || rule.ScopeKey != key {
				continue
			}
			// 时间过滤只约束 scheduled 版本：active 表示已生效，立即激活写入的
			// effective_at（激活时刻）不作为解析门槛。
			if rule.Status == esportscscommissionrule.StatusScheduled &&
				(rule.EffectiveAt == nil || rule.EffectiveAt.After(at.UTC())) {
				continue
			}
			if best == nil || rule.Version > best.Version {
				best = rule
			}
		}
		return best
	}
	if rule := pick(esportscscommissionrule.ScopeAdmin, strconv.Itoa(adminID)); rule != nil {
		return rule, nil
	}
	return pick(esportscscommissionrule.ScopeGlobal, ""), nil
}

// lockCsCommissionOnPayment 把付款时点的业绩客服及提成规则写到同一笔订单状态更新。
func lockCsCommissionOnPayment(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder,
	update *ent.EsportsOrderUpdate, now time.Time) error {
	update.SetCsCommissionRuleLockedAt(now.UTC())
	if order.ResponsibleAdminID == nil {
		return nil
	}
	adminID := *order.ResponsibleAdminID
	rule, err := resolveCsCommissionRuleIn(ctx, tx.EsportsCsCommissionRule, adminID, now)
	if err != nil {
		return err
	}
	update.SetCsCommissionAdminID(adminID)
	if rule != nil {
		update.SetCsCommissionRuleID(rule.ID)
	}
	return nil
}

// csCommissionBase 计算提成基数（分）。order_amount=扣除结单前退款后的报单金额；
// order_contribution=再扣除陪玩分成后的可分余额（§12.1）；order_count=单量固定为 1。
func csCommissionBase(netPayable, contribution int64, baseMode string) int64 {
	switch baseMode {
	case "order_contribution":
		return contribution
	case "order_count":
		return 1
	default:
		return netPayable
	}
}

// matchCsCommissionTier 按提成周期累计净基数匹配阶梯：取 threshold <= cumulative 且最高的一档。
// 阶梯阈值在建规则时已校验严格递增，故最后一个命中档即最高档。无命中返回 (0, false)。
func matchCsCommissionTier(tiers []schema.CsCommissionTier, cumulativeCents int64) (int, bool) {
	best := -1
	for i, tier := range tiers {
		if tier.ThresholdBaseCents <= cumulativeCents {
			best = i
		}
	}
	if best < 0 {
		return 0, false
	}
	return tiers[best].ValueBp, true
}

// csCommissionPeriodWindow 按提成周期返回 [start, end)（UTC 自然周期，与结算周期同口径）。
func csCommissionPeriodWindow(period string, now time.Time) (time.Time, time.Time) {
	utc := now.UTC()
	day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case "daily":
		return day, day.AddDate(0, 0, 1)
	case "weekly":
		offset := (int(day.Weekday()) + 6) % 7
		start := day.AddDate(0, 0, -offset)
		return start, start.AddDate(0, 0, 7)
	default:
		start := time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	}
}

// orderCsCommissionPeriodBase 汇总该客服在周期窗口内已计提的净基数（含负数冲回），用于阶梯分档。
// 在 Go 侧按绝对时刻过滤，避免不同驱动对带时区时间字面量的比较差异。
func orderCsCommissionPeriodBase(ctx context.Context, tx *ent.Tx, adminID int, start, end time.Time) (int64, error) {
	rows, err := tx.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.AdminIDEQ(adminID),
	).Select(esportscscommissionentry.FieldBaseCents, esportscscommissionentry.FieldCreatedAt).All(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, row := range rows {
		if row.CreatedAt.Before(start) || !row.CreatedAt.Before(end) {
			continue
		}
		total += row.BaseCents
	}
	return total, nil
}

// accrueOrderCsCommission 结单时计提客服提成（§12.2），必须在 closeOrder 同一事务内、陪玩分成之后调用。
// 提成只在订单完成后生成，未开工取消单不会走到结单；结单前已发生的退款从最终计提基数扣除。
// 上限：陪玩分成 + 渠道成本 + 客服提成不得超过可分基数（§12.1）；并反算工作室剩余快照。
// 幂等：该订单已有 service 提成明细则直接返回。
func accrueOrderCsCommission(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, now time.Time) error {
	exists, err := tx.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(order.ID),
		esportscscommissionentry.KindEQ(esportscscommissionentry.KindService),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	var adminID int
	var rule *ent.EsportsCsCommissionRule
	if order.CsCommissionRuleLockedAt != nil {
		if order.CsCommissionAdminID == nil || order.CsCommissionRuleID == nil {
			return nil
		}
		adminID = *order.CsCommissionAdminID
		var err error
		rule, err = tx.EsportsCsCommissionRule.Get(ctx, *order.CsCommissionRuleID)
		if err != nil {
			return err
		}
	} else {
		// 历史订单没有支付时点快照时，按现有归属与规则兼容计提。
		if order.ResponsibleAdminID == nil {
			return nil
		}
		adminID = *order.ResponsibleAdminID
		var err error
		rule, err = resolveCsCommissionRuleIn(ctx, tx.EsportsCsCommissionRule, adminID, now)
		if err != nil {
			return err
		}
	}
	if rule == nil {
		return nil
	}
	// 结单前履约后退款从最终计提基数扣除；加钟款属追加营收，按业务口径并入可分基数（§12.1-04）。
	netPayable := order.PayableCents + order.AddonCents - order.RefundedCents
	if netPayable <= 0 {
		return nil
	}
	playerTotal, err := sumOrderCommissionCents(ctx, tx, order.ID)
	if err != nil {
		return err
	}
	// 可分余额 = 扣除结单前退款、陪玩分成与渠道成本后的金额，提成不得超过它（§12.1）。
	channelCost := order.ChannelCostCents
	remaining := netPayable - playerTotal - channelCost
	if remaining < 0 {
		remaining = 0
	}
	base := csCommissionBase(netPayable, remaining, rule.BaseMode.String())
	if rule.CalcMode != esportscscommissionrule.CalcModeFixedPerOrder && base <= 0 {
		return nil
	}
	note := ""
	var amount int64
	matchedTierBp := 0
	switch rule.CalcMode {
	case esportscscommissionrule.CalcModeFixedPerOrder:
		amount = rule.ValueCents
	default:
		start, end := csCommissionPeriodWindow(rule.Period.String(), now)
		priorBase, err := orderCsCommissionPeriodBase(ctx, tx, adminID, start, end)
		if err != nil {
			return err
		}
		bp := rule.ValueBp
		if len(rule.Tiers) > 0 {
			if tierBp, ok := matchCsCommissionTier(rule.Tiers, priorBase+base); ok {
				bp = tierBp
				matchedTierBp = tierBp
			}
		}
		amount = base * int64(bp) / 10000
	}
	// 上限：客服提成不得超过扣除陪玩分成后的可分余额。
	if amount > remaining {
		amount = remaining
		note = "已按可分基数上限截断"
	}
	if amount <= 0 {
		return nil
	}
	// 工作室剩余 = 可分余额 − 客服提成（§12.1 反算）。百分比提成的舍入尾差落入工作室剩余，等价于尾差单列。
	studioRemainder := remaining - amount
	if studioRemainder < 0 {
		studioRemainder = 0
	}
	if _, err = tx.EsportsCsCommissionEntry.Create().
		SetIdemKey("cs_commission:service:" + order.OrderNo + ":" + strconv.Itoa(adminID)).
		SetOrderID(order.ID).SetAdminID(adminID).
		SetKind(esportscscommissionentry.KindService).
		SetRuleID(rule.ID).SetRuleNo(rule.RuleNo).SetRuleVersion(rule.Version).
		SetCalcMode(esportscscommissionentry.CalcMode(rule.CalcMode.String())).
		SetBaseMode(esportscscommissionentry.BaseMode(rule.BaseMode.String())).
		SetBaseCents(base).SetMatchedTierBp(matchedTierBp).SetAmountCents(amount).
		SetChannelCostCents(channelCost).SetStudioRemainderCents(studioRemainder).
		SetStatus(esportscscommissionentry.StatusPendingSettlement).SetNote(note).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errConflict
		}
		return err
	}
	return nil
}

// accrueGiftCsCommission 只使用送礼支付时快照的启用规则；疑似交易在复核通过前不计提。
func accrueGiftCsCommission(ctx context.Context, tx *ent.Tx, gift *ent.EsportsGiftOrder, now time.Time) error {
	if gift.CsCommissionRuleID == nil || gift.ResponsibleAdminID == nil {
		return nil
	}
	exists, err := tx.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.GiftOrderIDEQ(gift.ID),
		esportscscommissionentry.KindEQ(esportscscommissionentry.KindGift),
	).Exist(ctx)
	if err != nil || exists {
		return err
	}
	rule, err := tx.EsportsCsCommissionRule.Get(ctx, *gift.CsCommissionRuleID)
	if err != nil {
		return err
	}
	if !rule.IncludeGifts {
		return nil
	}
	remaining := gift.AmountCents - gift.IncomeCents
	if remaining <= 0 {
		return nil
	}
	base := csCommissionBase(gift.AmountCents, remaining, rule.BaseMode.String())
	var amount int64
	matchedTierBp := 0
	if rule.CalcMode == esportscscommissionrule.CalcModeFixedPerOrder {
		amount = rule.ValueCents
	} else {
		start, end := csCommissionPeriodWindow(rule.Period.String(), now)
		priorBase, err := orderCsCommissionPeriodBase(ctx, tx, *gift.ResponsibleAdminID, start, end)
		if err != nil {
			return err
		}
		bp := rule.ValueBp
		if len(rule.Tiers) > 0 {
			if tierBp, ok := matchCsCommissionTier(rule.Tiers, priorBase+base); ok {
				bp = tierBp
				matchedTierBp = tierBp
			}
		}
		amount = base * int64(bp) / 10000
	}
	note := "礼物收入计提"
	if amount > remaining {
		amount = remaining
		note = "礼物收入计提；已按可分余额上限截断"
	}
	if amount <= 0 {
		return nil
	}
	_, err = tx.EsportsCsCommissionEntry.Create().
		SetIdemKey("cs_commission:gift:" + gift.GiftNo + ":" + strconv.Itoa(*gift.ResponsibleAdminID)).
		SetGiftOrderID(gift.ID).SetAdminID(*gift.ResponsibleAdminID).
		SetKind(esportscscommissionentry.KindGift).
		SetRuleID(rule.ID).SetRuleNo(rule.RuleNo).SetRuleVersion(rule.Version).
		SetCalcMode(esportscscommissionentry.CalcMode(rule.CalcMode.String())).
		SetBaseMode(esportscscommissionentry.BaseMode(rule.BaseMode.String())).
		SetBaseCents(base).SetMatchedTierBp(matchedTierBp).SetAmountCents(amount).
		SetStudioRemainderCents(remaining - amount).
		SetStatus(esportscscommissionentry.StatusPendingSettlement).SetNote(note).Save(ctx)
	if ent.IsConstraintError(err) {
		return errConflict
	}
	return err
}

// reverseGiftCsCommission 按退款比例冲回礼物客服提成（§9.5 / §12.2）：amount 为本次退款额，
// 累计冲回不超过已计提提成；多次部分退款的幂等键按累计退款额区分，首次全额退款沿用历史键。
func reverseGiftCsCommission(ctx context.Context, tx *ent.Tx, accrued *ent.EsportsCsCommissionEntry,
	gift *ent.EsportsGiftOrder, amount int64) error {
	if amount <= 0 || gift.AmountCents <= 0 || accrued.AmountCents <= 0 {
		return nil
	}
	prior, err := tx.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.IdemKeyHasPrefix("cs_commission:gift_reversal:" + gift.GiftNo + ":" +
			strconv.Itoa(accrued.AdminID)),
	).Select(esportscscommissionentry.FieldAmountCents).All(ctx)
	if err != nil {
		return err
	}
	var reversed int64
	for _, entry := range prior {
		reversed -= entry.AmountCents
	}
	remaining := accrued.AmountCents - reversed
	target := accrued.AmountCents * amount / gift.AmountCents
	if target > remaining {
		target = remaining
	}
	if target <= 0 {
		return nil
	}
	idem := "cs_commission:gift_reversal:" + gift.GiftNo + ":" + strconv.Itoa(accrued.AdminID)
	if gift.RefundCents > 0 {
		idem += ":" + strconv.FormatInt(gift.RefundCents+amount, 10)
	}
	baseReversal := -(accrued.BaseCents * amount / gift.AmountCents)
	if baseReversal > 0 {
		baseReversal = 0
	}
	create := tx.EsportsCsCommissionEntry.Create().
		SetIdemKey(idem).
		SetGiftOrderID(gift.ID).SetAdminID(accrued.AdminID).
		SetKind(esportscscommissionentry.KindGiftReversal).
		SetRuleNo(accrued.RuleNo).SetRuleVersion(accrued.RuleVersion).
		SetCalcMode(accrued.CalcMode).SetBaseMode(accrued.BaseMode).
		SetBaseCents(baseReversal).SetMatchedTierBp(accrued.MatchedTierBp).
		SetAmountCents(-target).
		SetStatus(esportscscommissionentry.StatusPendingSettlement).
		SetNote("礼物退款冲回客服提成")
	if accrued.RuleID != nil {
		create.SetRuleID(*accrued.RuleID)
	}
	_, err = create.Save(ctx)
	if ent.IsConstraintError(err) {
		return errConflict
	}
	return err
}

// reverseOrderCsCommission 退款冲回客服提成（§12.2）：结单后部分退款按比例冲回，全额退款冲至零。
// 必须在 executeAfterSaleOnce 的同一事务内、退款额度落账之后调用；幂等靠 idem_key 唯一索引兜底。
// 客服提成每订单只有一条 service 明细，无跨明细尾差；其舍入尾差已在计提时落入工作室剩余（§12.1）。
func reverseOrderCsCommission(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, item *ent.EsportsAfterSale, now time.Time) error {
	rev := refundReversalContext{
		RefundCents: item.RefundCents,
		RefKey:      item.AfterSaleNo,
		AfterSaleID: &item.ID,
		PlayerID:    item.PlayerID,
	}
	return reverseOrderCsCommissionFor(ctx, tx, order, rev, now)
}

// reverseOrderCsCommissionFor 客服提成冲回实现（售后 / 外部退款共用）。
func reverseOrderCsCommissionFor(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder,
	rev refundReversalContext, now time.Time) error {
	if rev.RefundCents <= 0 || order.PayableCents <= 0 {
		return nil
	}
	service, err := tx.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(order.ID),
		esportscscommissionentry.KindEQ(esportscscommissionentry.KindService),
	).First(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	prior, err := tx.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(order.ID),
		esportscscommissionentry.KindEQ(esportscscommissionentry.KindReversal),
	).Select(esportscscommissionentry.FieldAmountCents).All(ctx)
	if err != nil {
		return err
	}
	var reversed int64
	for _, entry := range prior {
		reversed -= entry.AmountCents
	}
	remaining := service.AmountCents - reversed
	if remaining <= 0 {
		return nil
	}
	var reversal int64
	closeBase := commissionCloseBase(order)
	if closeBase <= 0 {
		return errConflict // 已计提明细不应对应零元结单基数。
	}
	if order.RefundedCents >= order.PayableCents {
		// 全额退款冲至零。
		reversal = -remaining
	} else {
		reversal = -(service.AmountCents * rev.RefundCents / closeBase)
		if -reversal > remaining {
			reversal = -remaining
		}
	}
	if reversal == 0 {
		return nil
	}
	baseReversal := -(service.BaseCents * rev.RefundCents / closeBase)
	if baseReversal > 0 {
		baseReversal = 0
	}
	note := "售后退款冲回"
	if rev.Note != "" {
		note = rev.Note + "客服提成"
	}
	create := tx.EsportsCsCommissionEntry.Create().
		SetIdemKey("cs_commission:reversal:" + rev.RefKey + ":" + strconv.Itoa(service.AdminID)).
		SetOrderID(order.ID).SetAdminID(service.AdminID).
		SetKind(esportscscommissionentry.KindReversal).
		SetRuleNo(service.RuleNo).SetRuleVersion(service.RuleVersion).
		SetCalcMode(service.CalcMode).SetBaseMode(service.BaseMode).
		SetBaseCents(baseReversal).SetMatchedTierBp(service.MatchedTierBp).
		SetAmountCents(reversal).SetStatus(esportscscommissionentry.StatusPendingSettlement).
		SetNote(note)
	if rev.AfterSaleID != nil {
		create.SetAfterSaleID(*rev.AfterSaleID)
	}
	if service.RuleID != nil {
		create.SetRuleID(*service.RuleID)
	}
	if _, err := create.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errConflict
		}
		return err
	}
	return nil
}

// settleCsCommission 结算某客服一个周期内的全部待结算提成明细，生成独立客服提成结算单（§12.2）。
// 与陪玩结算不同：**不写猫粮钱包**，发放由财务打款并留外部凭证。
// P0-6：这里生成的是**草稿**，明细只「挂单」（写 settlement_id）而**不置 settled**；明细转为
// settled 发生在登记发放成功时（markSettlementPaid，锁定之后的发放环节）。这样草稿未被锁定也不会
// 造成提成永久漏发——明细仍是「待结算」，可作废（void）后重开重新汇总。
// operatorID / now 预留给审计留痕（当前调用方含定时任务，传 0 / 由 created_at 兜底）。
func settleCsCommission(ctx context.Context, db *ent.Client, adminID int,
	periodStart, periodEnd time.Time, operatorID int, now time.Time) (*ent.EsportsSettlement, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	settlement, err := createCsSettlementDraft(ctx, tx, adminID, periodStart, periodEnd)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsSettlement.Get(ctx, settlement.ID)
}

// createCsSettlementDraft 在事务内生成客服提成草稿结算单，并把该周期的待结算明细挂到这张单上。
// 挂单只写 settlement_id、不改明细状态（P0-6）；settlement_id 非空同时充当「已纳入结算」标记，
// 使同一明细不会被第二张结算单重复汇总（防重复发钱），作废时再解绑回退。
func createCsSettlementDraft(ctx context.Context, tx *ent.Tx, adminID int,
	periodStart, periodEnd time.Time) (*ent.EsportsSettlement, error) {
	if adminID < 1 || !periodStart.Before(periodEnd) {
		return nil, errInvalidInput
	}
	if _, err := tx.Admin.Get(ctx, adminID); err != nil {
		return nil, err
	}
	pending, err := tx.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.AdminIDEQ(adminID),
		esportscscommissionentry.StatusEQ(esportscscommissionentry.StatusPendingSettlement),
		// 已挂到其它结算单（草稿或已结算）的明细不再重复汇总。
		esportscscommissionentry.SettlementIDIsNil(),
		// P0-8：被争议冻结的明细不得进入结算单，解冻后才恢复可结算。
		esportscscommissionentry.FrozenAtIsNil(),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	// 在 Go 侧按绝对时刻过滤 [periodStart, periodEnd)，与陪玩结算同口径。
	// ponytail: 逐条内存过滤为 O(n)，单客服待结算明细到万级时改为数据库范围查询。
	entries := make([]*ent.EsportsCsCommissionEntry, 0, len(pending))
	for _, entry := range pending {
		if entry.CreatedAt.Before(periodStart) || !entry.CreatedAt.Before(periodEnd) {
			continue
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w：该周期没有待结算提成明细", errInvalidInput)
	}
	var gross, adjustment int64
	ids := make([]int, 0, len(entries))
	for _, entry := range entries {
		if entry.AmountCents >= 0 {
			gross += entry.AmountCents
		} else {
			adjustment += entry.AmountCents
		}
		ids = append(ids, entry.ID)
	}
	settlement, err := tx.EsportsSettlement.Create().SetSettlementNo("ST" + uuid.NewString()).
		SetOwnerType(esportssettlement.OwnerTypeCs).SetOwnerID(adminID).
		SetPeriodStart(periodStart.UTC()).SetPeriodEnd(periodEnd.UTC()).
		SetGrossCents(gross).SetAdjustmentCents(adjustment).SetNetCents(gross + adjustment).
		SetEntryCount(len(entries)).SetStatus(esportssettlement.StatusDraft).Save(ctx)
	if err != nil {
		return nil, err
	}
	changed, err := tx.EsportsCsCommissionEntry.Update().Where(
		esportscscommissionentry.IDIn(ids...),
		esportscscommissionentry.StatusEQ(esportscscommissionentry.StatusPendingSettlement),
		esportscscommissionentry.SettlementIDIsNil(),
	).SetSettlementID(settlement.ID).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != len(ids) {
		return nil, errConflict
	}
	return settlement, nil
}

// markSettlementPaid 登记发放：locked → paid，并留外部打款凭证（§12.2 发放记录关联外部凭证）。
// 事务、读取与提交在此；全部资金校验与状态迁移见 payLockedCsSettlement。
func markSettlementPaid(ctx context.Context, db *ent.Client, id, adminID int, payoutRef string, now time.Time) (*ent.EsportsSettlement, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	settlement, err := tx.EsportsSettlement.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = payLockedCsSettlement(ctx, tx, settlement, adminID, payoutRef, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsSettlement.Get(ctx, id)
}

// payLockedCsSettlement 在事务内完成客服提成结算单登记发放的全部校验与落库。
// P0-7 约束：
//  1. 只有 owner_type=cs 的客服提成结算单走外部打款；陪玩结算单在锁定时已把金额写入猫粮钱包，
//     再登记外部发放就是重复发钱，必须拒绝并给中文提示；
//  2. 该结算单若已存在钱包/猫粮入账流水（历史或异常数据），同样拒绝外部发放；
//  3. 发放人（执行）不得与登记人（锁定人）相同——资金类不得自审；
//  4. 状态与版本条件更新（status=locked AND version=读取值 AND paid_at 为空 AND payout_ref 为空）：
//     P0-8 有了 version 后，并发下只有一个操作能成功，影响行数不为 1 即 errConflict。
//
// 发放成功时把该结算单绑定的明细由 pending_settlement 置 settled（冻结明细不置），
// 并校验 settled 条数 == entry_count，不一致整单回滚。
func payLockedCsSettlement(ctx context.Context, tx *ent.Tx, settlement *ent.EsportsSettlement,
	adminID int, payoutRef string, now time.Time) error {
	payoutRef = strings.TrimSpace(payoutRef)
	if adminID < 1 || payoutRef == "" || len(payoutRef) > 128 {
		return fmt.Errorf("%w：发放须填写外部打款凭证号", errInvalidInput)
	}
	if settlement.OwnerType != esportssettlement.OwnerTypeCs {
		return fmt.Errorf("%w：该结算单的领取主体是陪玩，金额已进入猫粮账户，不能登记外部发放", errInvalidInput)
	}
	if settlement.Status == esportssettlement.StatusVoid {
		return fmt.Errorf("%w：结算单已作废，不能登记发放", errConflict)
	}
	if settlement.Status != esportssettlement.StatusLocked ||
		settlement.PaidAt != nil || settlement.PayoutRef != "" {
		return errConflict
	}
	if csSettlementRegistrar(settlement) == adminID {
		return fmt.Errorf("%w：发放人不得与登记人相同，资金发放须由他人执行", errInvalidInput)
	}
	// 该单号已入账到钱包（猫粮等）时禁止再登记外部发放，避免一笔钱发两次。
	credited, err := tx.EsportsWalletTx.Query().Where(
		esportswallettx.SourceTypeEQ("settlement"),
		esportswallettx.SourceIDEQ(settlement.SettlementNo),
	).Count(ctx)
	if err != nil {
		return err
	}
	if credited > 0 {
		return fmt.Errorf("%w：该结算单资金已入账到钱包账户，不能重复登记外部发放", errInvalidInput)
	}
	changed, err := tx.EsportsSettlement.Update().Where(
		esportssettlement.IDEQ(settlement.ID),
		esportssettlement.StatusEQ(esportssettlement.StatusLocked),
		esportssettlement.VersionEQ(settlement.Version),
		esportssettlement.PaidAtIsNil(),
		esportssettlement.PayoutRefEQ(""),
	).SetStatus(esportssettlement.StatusPaid).SetPayoutRef(payoutRef).
		SetPaidByAdminID(adminID).SetPaidAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	if _, err = tx.EsportsCsCommissionEntry.Update().Where(
		esportscscommissionentry.SettlementIDEQ(settlement.ID),
		esportscscommissionentry.StatusEQ(esportscscommissionentry.StatusPendingSettlement),
		// 冻结明细不得置 settled（正常流程下冻结与挂单互斥，这里是兜底）。
		esportscscommissionentry.FrozenAtIsNil(),
	).SetStatus(esportscscommissionentry.StatusSettled).AddVersion(1).Save(ctx); err != nil {
		return err
	}
	settled, err := tx.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.SettlementIDEQ(settlement.ID),
		esportscscommissionentry.StatusEQ(esportscscommissionentry.StatusSettled),
	).Count(ctx)
	if err != nil {
		return err
	}
	if settled != settlement.EntryCount {
		return errConflict
	}
	return nil
}

// csSettlementRegistrar 返回结算单的登记人（锁定即确认登记）管理员 ID；未锁定时为 0。
// 发放与作废都据此做「执行人不得等于登记人」的职责分离校验。
func csSettlementRegistrar(settlement *ent.EsportsSettlement) int {
	if settlement.LockedByAdminID == nil {
		return 0
	}
	return *settlement.LockedByAdminID
}

// csSettlementVoidResult 是作废结算单的结果快照：含被回退的明细数与操作留痕，供调用方回显与对账。
type csSettlementVoidResult struct {
	SettlementID  int       `json:"settlement_id"`
	SettlementNo  string    `json:"settlement_no"`
	OwnerType     string    `json:"owner_type"`
	OwnerID       int       `json:"owner_id"`
	PeriodStart   time.Time `json:"period_start"`
	PeriodEnd     time.Time `json:"period_end"`
	NetCents      int64     `json:"net_cents"`
	EntryCount    int       `json:"entry_count"`
	ReleasedCount int       `json:"released_count"`
	OperatorID    int       `json:"operator_id"`
	Reason        string    `json:"reason"`
	VoidedAt      time.Time `json:"voided_at"`
	// Version 为作废后的结算单版本（乐观锁），便于调用方核对并发状态。
	Version int `json:"version"`
}

// csSettlementReason 规整作废 / 重开原因：去空白、必填、限长，避免写坏备注与事件载荷。
func csSettlementReason(reason, action string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", fmt.Errorf("%w：%s须填写原因", errInvalidInput, action)
	}
	return truncateRunes(reason, 120), nil
}

// csVoidEntryNote 生成作废回退备注：保留原备注并追加操作人、原因与原单号，超长按字符截断。
func csVoidEntryNote(old, settlementNo string, operatorID int, reason string, now time.Time) string {
	extra := fmt.Sprintf("作废回退：原结算单 %s，%s 管理员 #%d 作废，原因：%s",
		settlementNo, now.UTC().Format(time.RFC3339), operatorID, reason)
	if strings.TrimSpace(old) == "" {
		return truncateRunes(extra, 255)
	}
	return truncateRunes(old+"；"+extra, 255)
}

// voidCsCommissionSettlement 作废客服提成结算单（P0-6 / P0-8 软作废）：把该单绑定的明细从 settled /
// 挂单状态安全回退到 pending_settlement，并把结算单转为 void 终态使其不可再发放。
//   - 仅 owner_type=cs 且状态为 draft / locked 可作废；已登记发放（paid）直接拒绝，须走财务冲正；
//   - 作废人不得等于登记人（锁定人），与发放同一套职责分离口径；
//   - 每条明细都按「ID + 原状态 + 仍绑定本单」条件更新并校验影响行数，重复调用不会重复回退；
//   - 结算单按「id + version + 状态」条件更新并置 voided_by_admin_id / voided_at / void_reason，
//     版本不一致（并发操作）返回 errConflict；
//   - 操作人、原因、原单金额快照同时写入外盒事件 settlement.voided，并追加到明细备注，形成审计痕迹。
func voidCsCommissionSettlement(ctx context.Context, db *ent.Client, id, operatorID int, reason string, now time.Time) (*csSettlementVoidResult, error) {
	if id < 1 || operatorID < 1 {
		return nil, errInvalidInput
	}
	reason, err := csSettlementReason(reason, "作废")
	if err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	settlement, err := tx.EsportsSettlement.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if settlement.OwnerType != esportssettlement.OwnerTypeCs {
		return nil, fmt.Errorf("%w：仅客服提成结算单支持作废", errInvalidInput)
	}
	if settlement.Status == esportssettlement.StatusVoid {
		return nil, fmt.Errorf("%w：结算单已作废，请勿重复操作", errConflict)
	}
	if settlement.Status == esportssettlement.StatusPaid ||
		settlement.PaidAt != nil || settlement.PayoutRef != "" {
		return nil, fmt.Errorf("%w：结算单已登记发放，不能作废，请走财务冲正", errConflict)
	}
	if csSettlementRegistrar(settlement) == operatorID {
		return nil, fmt.Errorf("%w：作废人不得与登记人相同，作废须由他人执行", errInvalidInput)
	}
	entries, err := tx.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.SettlementIDEQ(id),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(entries) != settlement.EntryCount {
		return nil, errConflict
	}
	released := 0
	for _, entry := range entries {
		if entry.Status == esportscscommissionentry.StatusVoid {
			// 已撤销的明细无需回退，也不计入回退数。
			continue
		}
		changed, err := tx.EsportsCsCommissionEntry.Update().Where(
			esportscscommissionentry.IDEQ(entry.ID),
			esportscscommissionentry.StatusEQ(entry.Status),
			esportscscommissionentry.SettlementIDEQ(id),
		).SetStatus(esportscscommissionentry.StatusPendingSettlement).
			ClearSettlementID().
			SetNote(csVoidEntryNote(entry.Note, settlement.SettlementNo, operatorID, reason, now)).
			AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
		released++
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("settlement.voided:" + settlement.SettlementNo).
		SetEventType("settlement.voided").
		SetAggregateRef(settlement.SettlementNo).
		SetPayload(map[string]interface{}{
			"settlement_id":    settlement.ID,
			"settlement_no":    settlement.SettlementNo,
			"owner_type":       string(settlement.OwnerType),
			"owner_id":         settlement.OwnerID,
			"status_before":    string(settlement.Status),
			"period_start":     settlement.PeriodStart.UTC().Format(time.RFC3339),
			"period_end":       settlement.PeriodEnd.UTC().Format(time.RFC3339),
			"gross_cents":      settlement.GrossCents,
			"adjustment_cents": settlement.AdjustmentCents,
			"net_cents":        settlement.NetCents,
			"entry_count":      settlement.EntryCount,
			"released_count":   released,
			"operator_id":      operatorID,
			"reason":           reason,
			"voided_at":        now.UTC().Format(time.RFC3339),
		}).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	changed, err := tx.EsportsSettlement.Update().Where(
		esportssettlement.IDEQ(id),
		esportssettlement.VersionEQ(settlement.Version),
		esportssettlement.StatusIn(esportssettlement.StatusDraft, esportssettlement.StatusLocked),
		esportssettlement.PaidAtIsNil(),
		esportssettlement.PayoutRefEQ(""),
	).SetStatus(esportssettlement.StatusVoid).
		SetVoidedByAdminID(operatorID).SetVoidedAt(now.UTC()).SetVoidReason(reason).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &csSettlementVoidResult{
		SettlementID:  settlement.ID,
		SettlementNo:  settlement.SettlementNo,
		OwnerType:     string(settlement.OwnerType),
		OwnerID:       settlement.OwnerID,
		PeriodStart:   settlement.PeriodStart.UTC(),
		PeriodEnd:     settlement.PeriodEnd.UTC(),
		NetCents:      settlement.NetCents,
		EntryCount:    settlement.EntryCount,
		ReleasedCount: released,
		OperatorID:    operatorID,
		Reason:        reason,
		VoidedAt:      now.UTC(),
		Version:       settlement.Version + 1,
	}, nil
}

// reopenCsCommissionSettlement 重开客服提成结算单：作废后按同一客服、同一周期重新生成草稿单，
// 保证提成不被漏发。重开只出**草稿**，仍需锁定与登记发放（且发放人不得等于登记人），
// 因此「作废 + 重开」不会产生重复发钱；重开动作写外盒事件 settlement.reopened 留痕。
func reopenCsCommissionSettlement(ctx context.Context, db *ent.Client, adminID int,
	periodStart, periodEnd time.Time, operatorID int, reason string, now time.Time) (*ent.EsportsSettlement, error) {
	if adminID < 1 || operatorID < 1 || !periodStart.Before(periodEnd) {
		return nil, errInvalidInput
	}
	reason, err := csSettlementReason(reason, "重开")
	if err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	settlement, err := createCsSettlementDraft(ctx, tx, adminID, periodStart, periodEnd)
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("settlement.reopened:" + settlement.SettlementNo).
		SetEventType("settlement.reopened").
		SetAggregateRef(settlement.SettlementNo).
		SetPayload(map[string]interface{}{
			"settlement_id": settlement.ID,
			"settlement_no": settlement.SettlementNo,
			"owner_type":    string(settlement.OwnerType),
			"owner_id":      settlement.OwnerID,
			"period_start":  settlement.PeriodStart.UTC().Format(time.RFC3339),
			"period_end":    settlement.PeriodEnd.UTC().Format(time.RFC3339),
			"net_cents":     settlement.NetCents,
			"entry_count":   settlement.EntryCount,
			"operator_id":   operatorID,
			"reason":        reason,
			"reopened_at":   now.UTC().Format(time.RFC3339),
		}).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsSettlement.Get(ctx, settlement.ID)
}

// freezeCsCommissionEntry 冻结一条客服提成明细（P0-8 争议冻结）。
// 只允许冻结「待结算且未挂到任何结算单」的明细：已结算或已纳入结算单的明细冻结会造成口径冲突，
// 直接拒绝并给中文提示。冻结与状态正交（仍为 pending_settlement），解冻后恢复可结算。
func freezeCsCommissionEntry(ctx context.Context, db *ent.Client, entryID, adminID int, reason string, now time.Time) (*ent.EsportsCsCommissionEntry, error) {
	reason, err := csSettlementReason(reason, "冻结")
	if err != nil {
		return nil, err
	}
	if entryID < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsCsCommissionEntry.Update().Where(
		esportscscommissionentry.IDEQ(entryID),
		esportscscommissionentry.StatusEQ(esportscscommissionentry.StatusPendingSettlement),
		esportscscommissionentry.SettlementIDIsNil(),
		esportscscommissionentry.FrozenAtIsNil(),
	).SetFrozenAt(now.UTC()).SetFrozenReason(reason).
		SetFrozenByAdminID(adminID).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		if _, err = db.EsportsCsCommissionEntry.Get(ctx, entryID); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w：该提成明细已结算、已纳入结算单或已冻结，不能冻结", errConflict)
	}
	return db.EsportsCsCommissionEntry.Get(ctx, entryID)
}

// unfreezeCsCommissionEntry 解冻一条客服提成明细（争议终结后恢复可结算）。
func unfreezeCsCommissionEntry(ctx context.Context, db *ent.Client, entryID int) (*ent.EsportsCsCommissionEntry, error) {
	if entryID < 1 {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsCsCommissionEntry.Update().Where(
		esportscscommissionentry.IDEQ(entryID),
		esportscscommissionentry.FrozenAtNotNil(),
	).ClearFrozenAt().SetFrozenReason("").ClearFrozenByAdminID().AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, fmt.Errorf("%w：该提成明细未冻结或不存在", errConflict)
	}
	return db.EsportsCsCommissionEntry.Get(ctx, entryID)
}

// freezeOrderCsCommissionEntries 冻结某订单全部待结算的客服提成明细，返回冻结条数（争议受理时调用）。
func freezeOrderCsCommissionEntries(ctx context.Context, db *ent.Client, orderID, adminID int,
	reason string, now time.Time) (int, error) {
	reason, err := csSettlementReason(reason, "冻结")
	if err != nil {
		return 0, err
	}
	if orderID < 1 || adminID < 1 {
		return 0, errInvalidInput
	}
	return db.EsportsCsCommissionEntry.Update().Where(
		esportscscommissionentry.OrderIDEQ(orderID),
		esportscscommissionentry.StatusEQ(esportscscommissionentry.StatusPendingSettlement),
		esportscscommissionentry.SettlementIDIsNil(),
		esportscscommissionentry.FrozenAtIsNil(),
	).SetFrozenAt(now.UTC()).SetFrozenReason(reason).
		SetFrozenByAdminID(adminID).AddVersion(1).Save(ctx)
}

// unfreezeOrderCsCommissionEntries 解冻某订单全部已冻结的客服提成明细，返回解冻条数。
func unfreezeOrderCsCommissionEntries(ctx context.Context, db *ent.Client, orderID int) (int, error) {
	if orderID < 1 {
		return 0, errInvalidInput
	}
	return db.EsportsCsCommissionEntry.Update().Where(
		esportscscommissionentry.OrderIDEQ(orderID),
		esportscscommissionentry.FrozenAtNotNil(),
	).ClearFrozenAt().SetFrozenReason("").ClearFrozenByAdminID().AddVersion(1).Save(ctx)
}

// listCsCommissionEntries 列出客服提成明细，按需过滤，按 ID 倒序。
func listCsCommissionEntries(ctx context.Context, db *ent.Client, adminID, orderID int, status string, limit int) ([]*ent.EsportsCsCommissionEntry, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	q := db.EsportsCsCommissionEntry.Query()
	if adminID > 0 {
		q.Where(esportscscommissionentry.AdminIDEQ(adminID))
	}
	if orderID > 0 {
		q.Where(esportscscommissionentry.OrderIDEQ(orderID))
	}
	if status = strings.TrimSpace(status); status != "" {
		switch status {
		case "pending_settlement", "settled", "void":
			q.Where(esportscscommissionentry.StatusEQ(esportscscommissionentry.Status(status)))
		default:
			return nil, errInvalidInput
		}
	}
	return q.Order(ent.Desc(esportscscommissionentry.FieldID)).Limit(limit).All(ctx)
}

// csCommissionView 客服提成概览：待结算净额、冻结金额、已结算合计、本月累计与下次结算时间。
type csCommissionView struct {
	PendingSettlementCents int64     `json:"pending_settlement_cents"`
	FrozenCents            int64     `json:"frozen_cents"`
	SettledCents           int64     `json:"settled_cents"`
	MonthlyCents           int64     `json:"monthly_cents"`
	MonthStart             time.Time `json:"month_start"`
	MonthEnd               time.Time `json:"month_end"`
	NextSettlementAt       time.Time `json:"next_settlement_at"`
}

// csCommissionSummary 客服提成概览（§12.7 当月累计）。
// P0-8：被冻结的待结算明细单独计入 frozen_cents，不混进待结算净额（冻结不可结算）。
func csCommissionSummary(ctx context.Context, db *ent.Client, adminID int, now time.Time) (*csCommissionView, error) {
	if adminID < 1 {
		return nil, errInvalidInput
	}
	if _, err := db.Admin.Get(ctx, adminID); err != nil {
		return nil, err
	}
	rows, err := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.AdminIDEQ(adminID),
		esportscscommissionentry.StatusIn(
			esportscscommissionentry.StatusPendingSettlement, esportscscommissionentry.StatusSettled),
	).Select(esportscscommissionentry.FieldAmountCents, esportscscommissionentry.FieldStatus,
		esportscscommissionentry.FieldCreatedAt, esportscscommissionentry.FieldFrozenAt).All(ctx)
	if err != nil {
		return nil, err
	}
	monthStart, monthEnd := csCommissionPeriodWindow("monthly", now)
	var view csCommissionView
	view.MonthStart, view.MonthEnd = monthStart, monthEnd
	view.NextSettlementAt = nextSettlementAt(settlementPeriod(ctx, db), now)
	for _, row := range rows {
		if row.Status == esportscscommissionentry.StatusPendingSettlement {
			if row.FrozenAt != nil {
				view.FrozenCents += row.AmountCents
			} else {
				view.PendingSettlementCents += row.AmountCents
			}
		} else {
			view.SettledCents += row.AmountCents
		}
		if !row.CreatedAt.Before(monthStart) && row.CreatedAt.Before(monthEnd) {
			view.MonthlyCents += row.AmountCents
		}
	}
	return &view, nil
}

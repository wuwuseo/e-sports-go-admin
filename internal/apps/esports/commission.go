package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscommissionrule"
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsgiftorder"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderaddon"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/systemconfig"
)

// 结算周期配置键，取值 daily/weekly/monthly，缺失或非法时回落 weekly。
const settlementPeriodKey = "app.esports.settlement_period"

type ruleInput struct {
	RuleNo      string     `json:"rule_no"`
	Scope       string     `json:"scope"`
	ScopeKey    string     `json:"scope_key"`
	CalcMode    string     `json:"calc_mode"`
	ValueBp     int        `json:"value_bp"`
	ValueCents  int64      `json:"value_cents"`
	UnitBasis   string     `json:"unit_basis"`
	Note        string     `json:"note"`
	EffectiveAt *time.Time `json:"effective_at"`
}

// commissionMember 订单实际服务成员（陪玩 ID + 席位 + 权重秒数）。
type commissionMember struct {
	PlayerID      int
	SeatIndex     int
	SystemSeconds int64
	LockedRuleID  *int
	RuleLocked    bool
}

func validCommissionScope(scope string) bool {
	switch scope {
	case "global", "game", "level", "player":
		return true
	}
	return false
}

func validCommissionCalcMode(mode string) bool {
	switch mode {
	case "percent", "fixed_per_order", "fixed_per_unit":
		return true
	}
	return false
}

func validCommissionUnitBasis(basis string) bool {
	return basis == "sku_quantity" || basis == "promised_units"
}

// loadOrderMembers 取订单实际服务成员：优先服务段（按陪玩去重并汇总秒数），无服务段时
// 退回 accepted 且已分配陪玩的席位。两者皆空返回空切片。
func loadOrderMembers(ctx context.Context,
	segments *ent.EsportsServiceSegmentClient,
	seats *ent.EsportsDispatchSeatClient,
	orderID int,
) ([]commissionMember, error) {
	segs, err := segments.Query().Where(esportsservicesegment.OrderIDEQ(orderID)).
		Order(ent.Asc(esportsservicesegment.FieldSeatIndex), ent.Asc(esportsservicesegment.FieldPlayerID)).All(ctx)
	if err != nil {
		return nil, err
	}
	members := make([]commissionMember, 0, len(segs))
	index := map[int]int{}
	for _, seg := range segs {
		if idx, ok := index[seg.PlayerID]; ok {
			members[idx].SystemSeconds += seg.SystemSeconds
			continue
		}
		index[seg.PlayerID] = len(members)
		members = append(members, commissionMember{
			PlayerID: seg.PlayerID, SeatIndex: seg.SeatIndex, SystemSeconds: seg.SystemSeconds,
			LockedRuleID: seg.CommissionRuleID, RuleLocked: seg.CommissionRuleLockedAt != nil,
		})
	}
	if len(members) > 0 {
		return members, nil
	}
	accepted, err := seats.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
		esportsdispatchseat.PlayerIDNotNil(),
	).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, seat := range accepted {
		if seat.PlayerID == nil {
			continue
		}
		members = append(members, commissionMember{
			PlayerID: *seat.PlayerID, SeatIndex: seat.SeatIndex,
			LockedRuleID: seat.CommissionRuleID, RuleLocked: seat.CommissionRuleLockedAt != nil,
		})
	}
	return members, nil
}

// resolveCommissionRuleForPlayerIn 按单个陪玩解析当前规则，避免全单取首位成员的个体约定。
// §12.3 五级优先级：单笔手动指定 > 陪玩个体约定(player) > 陪玩等级(level) > 游戏或服务类型(game) > 全局默认。
// 无规则返回 nil；接单时把结果（包括无规则）锁定到席位，后续规则变更不影响该成员。
// scheduled 版本到达预设时间即适用，不依赖定时任务是否已补写状态。
func resolveCommissionRuleForPlayerIn(ctx context.Context,
	rules *ent.EsportsCommissionRuleClient,
	skus *ent.EsportsSkuClient,
	categories *ent.EsportsProductCategoryClient,
	order *ent.EsportsOrder, playerID int, playerLevel string, at time.Time,
) (*ent.EsportsCommissionRule, error) {
	// 时间过滤只约束 scheduled 版本：active 表示已生效，立即激活写入的
	// effective_at（激活时刻）不作为解析门槛。
	usable := func(rule *ent.EsportsCommissionRule) bool {
		if rule.Status != esportscommissionrule.StatusScheduled {
			return true
		}
		return rule.EffectiveAt != nil && !rule.EffectiveAt.After(at.UTC())
	}
	active, err := rules.Query().Where(
		esportscommissionrule.StatusIn(esportscommissionrule.StatusActive, esportscommissionrule.StatusScheduled),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	// 最高一级「单笔手动指定」：只认当前仍处于 active/scheduled 的版本，
	// 被新版转 history 后自然回落下一级，避免引用已废弃版本。
	if order != nil && order.ManualCommissionRuleID != nil {
		for _, rule := range active {
			if rule.ID == *order.ManualCommissionRuleID && usable(rule) {
				return rule, nil
			}
		}
	}
	if len(active) == 0 {
		return nil, nil
	}
	pick := func(scope esportscommissionrule.Scope, key string) *ent.EsportsCommissionRule {
		var best *ent.EsportsCommissionRule
		for _, rule := range active {
			if rule.Scope != scope || rule.ScopeKey != key || !usable(rule) {
				continue
			}
			if best == nil || rule.Version > best.Version {
				best = rule
			}
		}
		return best
	}
	if rule := pick(esportscommissionrule.ScopePlayer, strconv.Itoa(playerID)); rule != nil {
		return rule, nil
	}
	// 「陪玩等级」这一级让 §7.3 的等级与分成联动自动生效，无需为每人单独配规则。
	if level := strings.TrimSpace(playerLevel); level != "" {
		if rule := pick(esportscommissionrule.ScopeLevel, level); rule != nil {
			return rule, nil
		}
	}
	// 取 SKU 所属分类的 game 作为 game 层键；分类为空则跳过该层。
	if order != nil {
		if sku, err := skus.Get(ctx, order.SkuID); err == nil && sku.CategoryID != nil {
			if category, err := categories.Get(ctx, *sku.CategoryID); err == nil {
				if game := strings.TrimSpace(category.Game); game != "" {
					if rule := pick(esportscommissionrule.ScopeGame, game); rule != nil {
						return rule, nil
					}
				}
			}
		}
	}
	return pick(esportscommissionrule.ScopeGlobal, ""), nil
}

func commissionRuleForMember(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, member commissionMember, now time.Time) (*ent.EsportsCommissionRule, error) {
	if member.RuleLocked {
		if member.LockedRuleID == nil {
			return nil, nil
		}
		return tx.EsportsCommissionRule.Get(ctx, *member.LockedRuleID)
	}
	// 旧数据没有接单快照时保留兼容回退；新订单必须走锁定分支。
	// 回退解析要读当时的陪玩等级；档案缺失按无等级处理（跳过 level 级）。
	playerLevel := ""
	if player, pErr := tx.EsportsPlayer.Get(ctx, member.PlayerID); pErr == nil {
		playerLevel = player.Level
	}
	return resolveCommissionRuleForPlayerIn(ctx, tx.EsportsCommissionRule,
		tx.EsportsSku, tx.EsportsProductCategory, order, member.PlayerID, playerLevel, now)
}

// createCommissionRule 新建分成规则（一律 draft）。同一 rule_no 已存在则版本 = 现有最大 version + 1，
// 否则 version = 1；rule_no 为空时自动生成。
func createCommissionRule(ctx context.Context, db *ent.Client, in ruleInput, adminID int, now time.Time) (*ent.EsportsCommissionRule, error) {
	in.RuleNo = strings.TrimSpace(in.RuleNo)
	in.Scope = strings.TrimSpace(in.Scope)
	in.ScopeKey = strings.TrimSpace(in.ScopeKey)
	in.CalcMode = strings.TrimSpace(in.CalcMode)
	in.UnitBasis = strings.TrimSpace(in.UnitBasis)
	in.Note = strings.TrimSpace(in.Note)
	if len([]rune(in.RuleNo)) > 48 || len(in.ScopeKey) > 64 || len([]rune(in.Note)) > 255 ||
		!validCommissionScope(in.Scope) || !validCommissionCalcMode(in.CalcMode) ||
		!validCommissionUnitBasis(in.UnitBasis) {
		return nil, errInvalidInput
	}
	if in.Scope != "global" && in.ScopeKey == "" {
		return nil, errInvalidInput
	}
	// scope=level 的 scope_key 必须是当前等级配置中的编码，否则这条规则永远解析不到（§12.3）。
	if in.Scope == "level" && !validPlayerLevel(skillLevels(ctx, db), in.ScopeKey) {
		return nil, errInvalidInput
	}
	switch in.CalcMode {
	case "percent":
		if in.ValueBp <= 0 || in.ValueBp > 10000 {
			return nil, errInvalidInput
		}
	case "fixed_per_order", "fixed_per_unit":
		if in.ValueCents <= 0 {
			return nil, errInvalidInput
		}
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ruleNo := in.RuleNo
	version := 1
	if ruleNo != "" {
		latest, err := tx.EsportsCommissionRule.Query().Where(
			esportscommissionrule.RuleNoEQ(ruleNo),
		).Order(ent.Desc(esportscommissionrule.FieldVersion)).First(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, err
		}
		if latest != nil {
			version = latest.Version + 1
		}
	} else {
		ruleNo = "CR" + uuid.NewString()
	}
	create := tx.EsportsCommissionRule.Create().SetRuleNo(ruleNo).SetVersion(version).
		SetStatus(esportscommissionrule.StatusDraft).
		SetScope(esportscommissionrule.Scope(in.Scope)).SetScopeKey(in.ScopeKey).
		SetCalcMode(esportscommissionrule.CalcMode(in.CalcMode)).SetValueBp(in.ValueBp).
		SetValueCents(in.ValueCents).SetUnitBasis(esportscommissionrule.UnitBasis(in.UnitBasis)).
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
	return db.EsportsCommissionRule.Get(ctx, rule.ID)
}

// activateCommissionRule 生效指定规则：未来时间进入 scheduled，到点后可解析并由任务转 active。
func activateCommissionRule(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsCommissionRule, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	target, err := tx.EsportsCommissionRule.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if target.Status != esportscommissionrule.StatusDraft {
		return nil, errConflict
	}
	if target.EffectiveAt != nil && target.EffectiveAt.After(now.UTC()) {
		if _, err = tx.EsportsCommissionRule.Update().Where(
			esportscommissionrule.RuleNoEQ(target.RuleNo),
			esportscommissionrule.StatusEQ(esportscommissionrule.StatusScheduled),
		).SetStatus(esportscommissionrule.StatusHistory).Save(ctx); err != nil {
			return nil, err
		}
		changed, err := tx.EsportsCommissionRule.Update().Where(
			esportscommissionrule.IDEQ(id), esportscommissionrule.StatusEQ(esportscommissionrule.StatusDraft),
		).SetStatus(esportscommissionrule.StatusScheduled).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return db.EsportsCommissionRule.Get(ctx, id)
	}
	if _, err = tx.EsportsCommissionRule.Update().Where(
		esportscommissionrule.RuleNoEQ(target.RuleNo),
		esportscommissionrule.StatusIn(esportscommissionrule.StatusActive, esportscommissionrule.StatusScheduled),
	).SetStatus(esportscommissionrule.StatusHistory).Save(ctx); err != nil {
		return nil, err
	}
	changed, err := tx.EsportsCommissionRule.Update().Where(
		esportscommissionrule.IDEQ(id),
		esportscommissionrule.StatusEQ(esportscommissionrule.StatusDraft),
	).SetStatus(esportscommissionrule.StatusActive).SetEffectiveAt(now.UTC()).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCommissionRule.Get(ctx, id)
}

// rollbackCommissionRule 回滚到指定版本：以该行的 rule_no 与配置为模板新建 max version+1 的
// 直接 active 行，并把原 active 转 history。原历史行不可变（§12.4）。
func rollbackCommissionRule(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsCommissionRule, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	source, err := tx.EsportsCommissionRule.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	latest, err := tx.EsportsCommissionRule.Query().Where(
		esportscommissionrule.RuleNoEQ(source.RuleNo),
	).Order(ent.Desc(esportscommissionrule.FieldVersion)).First(ctx)
	if err != nil {
		return nil, err
	}
	created, err := tx.EsportsCommissionRule.Create().SetRuleNo(source.RuleNo).
		SetVersion(latest.Version + 1).SetStatus(esportscommissionrule.StatusActive).
		SetScope(source.Scope).SetScopeKey(source.ScopeKey).SetCalcMode(source.CalcMode).
		SetValueBp(source.ValueBp).SetValueCents(source.ValueCents).SetUnitBasis(source.UnitBasis).
		SetNote(source.Note).SetCreatedByAdminID(adminID).SetEffectiveAt(now.UTC()).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsCommissionRule.Update().Where(
		esportscommissionrule.RuleNoEQ(source.RuleNo),
		esportscommissionrule.StatusIn(esportscommissionrule.StatusActive, esportscommissionrule.StatusScheduled),
		esportscommissionrule.IDNEQ(created.ID),
	).SetStatus(esportscommissionrule.StatusHistory).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCommissionRule.Get(ctx, created.ID)
}

// listCommissionRules 列出分成规则，ruleNo 非空时按编号过滤，按 ID 倒序。
func listCommissionRules(ctx context.Context, db *ent.Client, ruleNo string, limit int) ([]*ent.EsportsCommissionRule, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsCommissionRule.Query()
	if ruleNo = strings.TrimSpace(ruleNo); ruleNo != "" {
		q.Where(esportscommissionrule.RuleNoEQ(ruleNo))
	}
	return q.Order(ent.Desc(esportscommissionrule.FieldID)).Limit(limit).All(ctx)
}

// tailReversalNote 是「舍入尾差单列」冲回明细的备注标记（§12.1-05）：
// 多条原明细按比例截断产生的尾差不再并入最后一条冲回明细，而是单列一条 kind=reversal 明细，
// 既保证冲回合计等于精确值，也让尾差可被单独查询与对账。
const tailReversalNote = "舍入尾差单列"

// splitServiceUnits 把「折后明细金额」按最小服务单位拆分（§15.5-04）：
// 每单位金额 = 折后金额 ÷ 承诺服务量（整数分向下取整），舍入尾差固定记在最后一单位，
// 逐单位金额合计恒等于折后金额。（§18.2 裁决：优先「尾差记在最后一单位」的规则，
// 而不是台账举例 [34,33,33]。）
func splitServiceUnits(amount int64, units int64) []int64 {
	if units < 1 {
		return nil
	}
	parts := make([]int64, units)
	share := amount / units
	var allocated int64
	for i := range parts {
		parts[i] = share
		allocated += share
	}
	parts[len(parts)-1] += amount - allocated
	return parts
}

// unitReversalTargets 按最小服务单位分摊「某类明细（服务 / 加钟）的退款冲回目标」（§15.5-04）：
// 先按「折后明细金额 ÷ 承诺服务量」求逐单位金额，再逐单位按退款比例（退款额 ÷ 统一可退基数）
// 求逐单位冲回额；两步的舍入尾差都并入最后一单位（§18.2 裁决）。
// 返回值合计恒等于 amount×refundCents÷refundBase，即该类冲回目标本身——
// 单位级分摊因此真正参与退款金额除法，而不仅仅是展示。
func unitReversalTargets(amount, units, refundCents, refundBase int64) []int64 {
	if amount <= 0 || refundCents <= 0 || refundBase <= 0 {
		return nil
	}
	if units < 1 {
		units = 1
	}
	unitAmounts := splitServiceUnits(amount, units)
	parts := make([]int64, len(unitAmounts))
	var allocated int64
	for i, unitAmount := range unitAmounts {
		parts[i] = unitAmount * refundCents / refundBase
		allocated += parts[i]
	}
	if target := amount * refundCents / refundBase; target != allocated {
		parts[len(parts)-1] += target - allocated
	}
	return parts
}

// sumUnitAmounts 汇总逐单位金额（冲回目标由单位级分摊求和得出）。
func sumUnitAmounts(parts []int64) int64 {
	var total int64
	for _, part := range parts {
		total += part
	}
	return total
}

// addonWalletSourceType 是加钟余额收款流水的来源类型（见 addon.go payOrderAddonByBalance）。
const addonWalletSourceType = "order_addon"

// addonReceivedCents 订单「加钟已收款」合计（分，§12.5-02）：由只增钱包流水
// （source_type=order_addon、entry_type=consume，source_id=本单加钟单号）求和派生，
// 沿用本仓「余额一律由流水求和派生」的口径，不新增冗余余额字段。
// 加钟单尚未收款（无收款流水）时返回 0，即不计入可退额度。
func addonReceivedCents(ctx context.Context, client *ent.Client, orderID int) (int64, error) {
	rows, err := orderAddonConsumeRows(ctx, client, orderID)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, row := range rows {
		total += -(row.PrincipalDeltaCents + row.BonusDeltaCents)
	}
	if total < 0 {
		total = 0
	}
	return total, nil
}

// orderAddonConsumeRows 订单全部加钟余额收款流水（只增），按写入顺序返回；
// 供「加钟已收款派生」与「加钟款原路退回构成」共用同一取数口径。
func orderAddonConsumeRows(ctx context.Context, client *ent.Client, orderID int) ([]*ent.EsportsWalletTx, error) {
	addons, err := client.EsportsOrderAddon.Query().Where(
		esportsorderaddon.OrderIDEQ(orderID),
	).Select(esportsorderaddon.FieldAddonNo).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(addons) == 0 {
		return nil, nil
	}
	nos := make([]string, 0, len(addons))
	for _, addon := range addons {
		nos = append(nos, addon.AddonNo)
	}
	return client.EsportsWalletTx.Query().Where(
		esportswallettx.SourceTypeEQ(addonWalletSourceType),
		esportswallettx.SourceIDIn(nos...),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeConsume),
	).Order(ent.Asc(esportswallettx.FieldID)).All(ctx)
}

// refundableCommissionBase 是退款冲回的统一「可退基数」（§12.1-05 / §12.5-02）：
// 服务与加钟两类冲回必须按同一基数计算比例，否则同一次退款两类冲回比例不一致（加钟侧被少冲）。
// 基数 = 结单净服务额快照（缺失时回落订单应付额）+ 加钟已收款：与可退上界（服务应付 + 加钟已收）
// 同源，保证「把可退额度全部退完」时服务 / 加钟冲回都恰好回到 100% 原计提（D34 不变量）。
func refundableCommissionBase(ctx context.Context, client *ent.Client, order *ent.EsportsOrder) (int64, error) {
	if order == nil {
		return 0, nil
	}
	addonReceived, err := addonReceivedCents(ctx, client, order.ID)
	if err != nil {
		return 0, err
	}
	base := commissionCloseBase(order)
	if base <= 0 {
		base = order.PayableCents
	}
	return base + addonReceived, nil
}

// commissionPool 按规则算出订单总分成池（成员间再分摊，避免多陪玩订单超额）。
func commissionPool(order *ent.EsportsOrder, rule *ent.EsportsCommissionRule) int64 {
	switch rule.CalcMode {
	case esportscommissionrule.CalcModePercent:
		return order.PayableCents * int64(rule.ValueBp) / 10000
	case esportscommissionrule.CalcModeFixedPerOrder:
		return rule.ValueCents
	case esportscommissionrule.CalcModeFixedPerUnit:
		units := order.Quantity
		if rule.UnitBasis == esportscommissionrule.UnitBasisPromisedUnits {
			units = order.PromisedServiceUnits
		}
		return rule.ValueCents * units
	}
	return 0
}

// splitPool 把总池按成员 system_seconds 权重分摊；权重合计为 0 时按人头均分。
// 整数除法尾差归最后一名成员，保证分摊额合计恰好等于 pool。
func splitPool(pool int64, members []commissionMember) []int64 {
	amounts := make([]int64, len(members))
	if len(members) == 0 {
		return amounts
	}
	var total int64
	for _, member := range members {
		total += member.SystemSeconds
	}
	var allocated int64
	if total <= 0 {
		share := pool / int64(len(members))
		for i := range amounts {
			amounts[i] = share
			allocated += share
		}
	} else {
		for i, member := range members {
			share := pool * member.SystemSeconds / total
			amounts[i] = share
			allocated += share
		}
	}
	amounts[len(members)-1] += pool - allocated
	return amounts
}

// sumOrderCommissionCents 订单累计非作废分成明细净额（含加钟明细与冲回负数），
// 供计算剩余可分基数（§12.1-04）。
func sumOrderCommissionCents(ctx context.Context, tx *ent.Tx, orderID int) (int64, error) {
	entries, err := tx.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(orderID),
		esportscommissionentry.StatusNEQ(esportscommissionentry.StatusVoid),
	).Select(esportscommissionentry.FieldAmountCents).All(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, entry := range entries {
		total += entry.AmountCents
	}
	return total, nil
}

// accrueOrderCommission 结单时按成员各自接单快照计提；服务明细与订单状态同事务提交。
// 订单净服务应付按服务秒数分给各成员，各成员规则作用于其份额，尾差归该规则组最后一人。
// 无接单快照的历史服务段回退到当前规则；新单不会因结单前改规则而改变约定。
func accrueOrderCommission(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, now time.Time) error {
	exists, err := tx.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindService),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	members, err := loadOrderMembers(ctx, tx.EsportsServiceSegment, tx.EsportsDispatchSeat, order.ID)
	if err != nil {
		return err
	}
	if len(members) == 0 {
		return nil
	}
	if order.PayableCents <= 0 {
		return nil
	}
	netPayable := order.PayableCents - order.RefundedCents
	if netPayable <= 0 {
		return nil
	}
	// 可分基数（§12.1-04）：服务应付 + 加钟追加金额 − 已退款。加钟分成先于结单计提，
	// 须从服务侧上限中一并扣除，保证「陪玩分成 + 客服提成 + 渠道成本 ≤ 可分基数」。
	accrued, err := sumOrderCommissionCents(ctx, tx, order.ID)
	if err != nil {
		return err
	}
	available := order.PayableCents + order.AddonCents - order.RefundedCents - order.ChannelCostCents - accrued
	if available <= 0 {
		return nil
	}
	bases := splitPool(netPayable, members)
	rules := make([]*ent.EsportsCommissionRule, len(members))
	amounts := make([]int64, len(members))
	groups := map[int][]int{}
	for i, member := range members {
		rule, err := commissionRuleForMember(ctx, tx, order, member, now)
		if err != nil {
			return err
		}
		if rule == nil {
			continue
		}
		rules[i] = rule
		groups[rule.ID] = append(groups[rule.ID], i)
	}
	for _, indexes := range groups {
		rule := rules[indexes[0]]
		pool := commissionPool(order, rule)
		var groupBase, allocated int64
		for _, i := range indexes {
			groupBase += bases[i]
			amounts[i] = pool * bases[i] / order.PayableCents
			allocated += amounts[i]
		}
		groupTarget := pool * groupBase / order.PayableCents
		amounts[indexes[len(indexes)-1]] += groupTarget - allocated
	}
	var total int64
	for _, amount := range amounts {
		total += amount
	}
	note := ""
	if total > available {
		note = "已按可分基数上限截断"
		var allocated int64
		last := -1
		for i, amount := range amounts {
			if amount <= 0 {
				continue
			}
			last = i
			amounts[i] = available * amount / total
			allocated += amounts[i]
		}
		if last >= 0 {
			amounts[last] += available - allocated
		}
	}
	for i, member := range members {
		rule := rules[i]
		if rule == nil || amounts[i] <= 0 {
			continue
		}
		if _, err = tx.EsportsCommissionEntry.Create().
			SetIdemKey("commission:service:" + order.OrderNo + ":" + strconv.Itoa(member.PlayerID)).
			SetOrderID(order.ID).SetPlayerID(member.PlayerID).
			SetKind(esportscommissionentry.KindService).SetSeatIndex(member.SeatIndex).
			SetRuleID(rule.ID).SetRuleNo(rule.RuleNo).SetRuleVersion(rule.Version).
			SetCalcMode(esportscommissionentry.CalcMode(rule.CalcMode.String())).
			SetBaseCents(bases[i]).SetAmountCents(amounts[i]).
			SetStatus(esportscommissionentry.StatusPendingSettlement).SetNote(note).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return errConflict
			}
			return err
		}
	}
	return nil
}

// addonReversalNote 加钟冲回明细的备注标记，用于区分服务/加钟口径的既有冲回累计。
const addonReversalNote = "加钟退款分摊"

// refundReversalContext 退款冲回的共用上下文：售后执行与外部退款执行共用同一套冲回逻辑，
// 差异只在退款金额、幂等键（售后单号 / 退款单号）、关联售后 ID、争议陪玩与操作人。
// AfterSaleID / PlayerID 为 nil 时不写关联字段（外部退款未指定争议陪玩）。
type refundReversalContext struct {
	RefundCents  int64
	RefKey       string
	AfterSaleID  *int
	PlayerID     *int
	ActorAdminID int
	// Note 非空时用于标注外部退款冲回，便于在明细里区分售后冲回与外部渠道退款冲回。
	Note string
}

// reverseOrderCommission 退款按比例冲回分成，必须在 executeAfterSaleOnce 的同一事务内、
// 退款额度落账之后调用。新增 kind=reversal 的负数明细，使退款金额落在退款当期（§12.6）；
// 幂等靠 idem_key 唯一索引兜底，重复执行不重复冲回。
func reverseOrderCommission(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, item *ent.EsportsAfterSale, now time.Time) error {
	rev := refundReversalContext{
		RefundCents: item.RefundCents,
		RefKey:      item.AfterSaleNo,
		AfterSaleID: &item.ID,
		PlayerID:    item.PlayerID,
	}
	return reverseOrderCommissionFor(ctx, tx, order, rev, now)
}

// reverseOrderCommissionFor 冲回实现（售后 / 外部退款共用）。
// 服务与加钟两类明细都按同一「可退基数」计算冲回目标（§12.1-05 / §12.5-02），
// 舍入尾差不再并入最后一条冲回明细，而是单列为独立的冲回行（§12.1-05）。
// 售后单指定争议陪玩时优先冲回该成员的明细：先冲满其本人分成（不超过该类冲回目标），
// 剩余目标再按比例分摊其他成员；未指定或该成员无明细时维持整体比例冲回。
func reverseOrderCommissionFor(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder,
	rev refundReversalContext, now time.Time) error {
	if rev.RefundCents <= 0 || order.PayableCents <= 0 {
		return nil
	}
	entries, err := tx.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		// §12.5：退款同时冲回原单服务分成与加钟分成（加钟退款分摊）。
		esportscommissionentry.KindIn(
			esportscommissionentry.KindService, esportscommissionentry.KindAddon),
		// 已撤销（void）的加钟明细不再参与退款冲回，避免二次冲回。
		esportscommissionentry.StatusNEQ(esportscommissionentry.StatusVoid),
	).All(ctx)
	if err != nil {
		return err
	}
	refundBase, err := refundableCommissionBase(ctx, tx.Client(), order)
	if err != nil {
		return err
	}
	// 先读该订单全部既有冲回明细，按成员区分服务/加钟口径累计，用于限制争议成员
	// 在多次部分退款下的累计冲回不超过其本人原分成。
	// 加钟备注可能带外部退款前缀，用包含匹配保持历史数据兼容。
	priorRevs, err := tx.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal),
	).All(ctx)
	if err != nil {
		return err
	}
	priorService, priorAddon := map[int]int64{}, map[int]int64{}
	var priorServiceTotal, priorAddonTotal int64
	for _, prior := range priorRevs {
		if strings.Contains(prior.Note, addonReversalNote) {
			priorAddon[prior.PlayerID] += prior.AmountCents
			priorAddonTotal += prior.AmountCents
		} else {
			priorService[prior.PlayerID] += prior.AmountCents
			priorServiceTotal += prior.AmountCents
		}
	}
	// reversalTail 是舍入尾差单列出的一条独立冲回明细：index 指向它归属的原明细，
	// 由该明细决定陪玩 / 席位 / 规则快照，amount 为正数尾差。
	type reversalTail struct {
		index  int
		addon  bool
		amount int64
	}
	// allocate 计算一组明细各自应冲回的正数金额（分），并把舍入尾差单列返回。
	allocate := func(idxs []int, target int64, prior map[int]int64, addon bool) ([]int64, []reversalTail) {
		amounts := make([]int64, len(entries))
		var tails []reversalTail
		if len(idxs) == 0 || target <= 0 {
			return amounts, tails
		}
		spread := func(sub []int, amount int64) {
			if len(sub) == 0 || amount <= 0 {
				return
			}
			var subTotal int64
			for _, i := range sub {
				subTotal += entries[i].AmountCents
			}
			if subTotal <= 0 {
				return
			}
			var allocated int64
			for _, i := range sub {
				amounts[i] = amount * entries[i].AmountCents / subTotal
				allocated += amounts[i]
			}
			// 尾差单列（§12.1-05）：不再并入最后一条明细，而是独立冲回行，便于单独对账。
			if tail := amount - allocated; tail != 0 {
				tails = append(tails, reversalTail{index: sub[len(sub)-1], addon: addon, amount: tail})
			}
		}
		var preferred, others []int
		if rev.PlayerID != nil {
			for _, i := range idxs {
				if entries[i].PlayerID == *rev.PlayerID {
					preferred = append(preferred, i)
				} else {
					others = append(others, i)
				}
			}
		}
		if len(preferred) == 0 {
			spread(idxs, target)
			return amounts, tails
		}
		var preferredTotal int64
		for _, i := range preferred {
			preferredTotal += entries[i].AmountCents
		}
		if rev.PlayerID != nil {
			// 扣除该成员本类口径已被冲回的部分，剩余可冲回额度为负时按 0 处理。
			remaining := preferredTotal + prior[*rev.PlayerID]
			if remaining < 0 {
				remaining = 0
			}
			preferredTotal = remaining
		}
		// 争议成员先冲满本人剩余分成（不超过该类冲回目标），剩余目标再分摊其他成员。
		cap := min(target, preferredTotal)
		spread(preferred, cap)
		spread(others, target-cap)
		return amounts, tails
	}
	serviceIdx, addonIdx := []int{}, []int{}
	var totalService, totalAddon int64
	for i, entry := range entries {
		if entry.Kind == esportscommissionentry.KindService {
			serviceIdx = append(serviceIdx, i)
			totalService += entry.AmountCents
		} else {
			addonIdx = append(addonIdx, i)
			totalAddon += entry.AmountCents
		}
	}
	var amounts []int64
	var tails []reversalTail
	// §15.5-04：两类冲回目标先按最小服务单位分摊求和（逐单位金额 × 退款额 ÷ 统一可退基数，
	// 尾差并入最后一单位），再用该目标在条目级按比例冲回；单位级与条目级共用同一可退基数。
	// D34（§12.1-05）：同一类别累计冲回不得超过原计提——加钟收款后统一基数扩容时，
	// 早期已冲满的类别不得因分母变大而被再次冲回，故目标按「原计提 − 既有冲回」封顶。
	serviceUnits := order.PromisedServiceUnits
	addonUnits := order.AddonUnits
	if refundBase > 0 && totalService > 0 {
		serviceTarget := sumUnitAmounts(unitReversalTargets(totalService, serviceUnits, rev.RefundCents, refundBase))
		serviceTarget = min(serviceTarget, max(totalService+priorServiceTotal, 0))
		amounts, tails = allocate(serviceIdx, serviceTarget, priorService, false)
	}
	if refundBase > 0 && totalAddon > 0 {
		addonTarget := sumUnitAmounts(unitReversalTargets(totalAddon, addonUnits, rev.RefundCents, refundBase))
		addonTarget = min(addonTarget, max(totalAddon+priorAddonTotal, 0))
		addonAmounts, addonTails := allocate(addonIdx, addonTarget, priorAddon, true)
		if amounts == nil {
			amounts = addonAmounts
		} else {
			for i, amount := range addonAmounts {
				if amount != 0 {
					amounts[i] = amount
				}
			}
		}
		tails = append(tails, addonTails...)
	}
	if amounts == nil {
		return nil
	}
	// createReversal 写一条冲回明细：base 统一为可退基数，规则快照沿用原明细。
	createReversal := func(entry *ent.EsportsCommissionEntry, idem, note string, amount int64) error {
		create := tx.EsportsCommissionEntry.Create().
			SetIdemKey(idem).
			SetOrderID(order.ID).SetPlayerID(entry.PlayerID).
			SetKind(esportscommissionentry.KindReversal).SetSeatIndex(entry.SeatIndex).
			SetBaseCents(refundBase).SetAmountCents(-amount).
			SetRuleNo(entry.RuleNo).SetRuleVersion(entry.RuleVersion).
			SetCalcMode(esportscommissionentry.CalcMode(entry.CalcMode.String())).
			SetStatus(esportscommissionentry.StatusPendingSettlement)
		if rev.AfterSaleID != nil {
			create.SetAfterSaleID(*rev.AfterSaleID)
		}
		if note != "" {
			create.SetNote(note)
		}
		if entry.RuleID != nil {
			create.SetRuleID(*entry.RuleID)
		}
		if _, err := create.Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return errConflict
			}
			return err
		}
		return nil
	}
	for i, entry := range entries {
		if amounts[i] <= 0 {
			continue
		}
		// 加钟明细可能同一陪玩多条，幂等键改用明细 ID 以免互相覆盖。
		idem := "commission:reversal:" + rev.RefKey + ":" + strconv.Itoa(entry.PlayerID)
		note := rev.Note
		if entry.Kind == esportscommissionentry.KindAddon {
			idem = "commission:reversal:addon:" + rev.RefKey + ":" + strconv.Itoa(entry.ID)
			note = addonReversalNote
			if rev.Note != "" {
				note = addonReversalNote + "·" + rev.Note
			}
		}
		if err := createReversal(entry, idem, note, amounts[i]); err != nil {
			return err
		}
	}
	for _, tail := range tails {
		entry := entries[tail.index]
		// 尾差行必须与明细行可区分：备注带尾差标记，幂等键独立，便于财务逐笔核对。
		idem := "commission:reversal:tail:" + rev.RefKey + ":" + strconv.Itoa(entry.ID)
		note := tailReversalNote
		if tail.addon {
			idem = "commission:reversal:tail:addon:" + rev.RefKey + ":" + strconv.Itoa(entry.ID)
			note = addonReversalNote + "·" + tailReversalNote
		}
		if err := createReversal(entry, idem, note, tail.amount); err != nil {
			return err
		}
	}
	return nil
}

func commissionCloseBase(order *ent.EsportsOrder) int64 {
	if order.CloseNetServiceCents != nil {
		return *order.CloseNetServiceCents
	}
	return order.PayableCents
}

// settlePlayerCommission 结算某陪玩一个周期内全部待结算明细，汇总正向/负向并写入猫粮流水。
// net 为负表示冲回，不阻断结算（§12.6 退款记在退款当期）。
func settlePlayerCommission(ctx context.Context, db *ent.Client, playerID int,
	periodStart, periodEnd time.Time, adminID int, now time.Time) (*ent.EsportsSettlement, error) {
	if playerID < 1 || !periodStart.Before(periodEnd) {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.EsportsPlayer.Get(ctx, playerID); err != nil {
		return nil, err
	}
	pending, err := tx.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.PlayerIDEQ(playerID),
		esportscommissionentry.StatusEQ(esportscommissionentry.StatusPendingSettlement),
		esportscommissionentry.SettlementIDIsNil(),
		// §12.6：被争议冻结的明细不参与当期结算，解冻后才可结算。
		esportscommissionentry.FrozenAtIsNil(),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	// 在 Go 侧按绝对时刻过滤 [periodStart, periodEnd)，避免不同数据库驱动对带时区时间的
	// 字面量比较差异（sqlite 按文本比较、MySQL 按 UTC）。
	// ponytail: 逐条内存过滤为 O(n)，单陪玩待结算明细到万级时改为数据库范围查询（需统一时区存储）。
	entries := make([]*ent.EsportsCommissionEntry, 0, len(pending))
	for _, entry := range pending {
		if entry.CreatedAt.Before(periodStart) || !entry.CreatedAt.Before(periodEnd) {
			continue
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w：该周期没有待结算明细", errInvalidInput)
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
	net := gross + adjustment
	settlement, err := tx.EsportsSettlement.Create().SetSettlementNo("ST" + uuid.NewString()).
		SetOwnerType(esportssettlement.OwnerTypePlayer).SetOwnerID(playerID).
		SetPeriodStart(periodStart.UTC()).SetPeriodEnd(periodEnd.UTC()).
		SetGrossCents(gross).SetAdjustmentCents(adjustment).SetNetCents(net).
		SetEntryCount(len(entries)).SetStatus(esportssettlement.StatusDraft).Save(ctx)
	if err != nil {
		return nil, err
	}
	changed, err := tx.EsportsCommissionEntry.Update().Where(
		esportscommissionentry.IDIn(ids...),
		esportscommissionentry.StatusEQ(esportscommissionentry.StatusPendingSettlement),
		esportscommissionentry.SettlementIDIsNil(),
	).SetSettlementID(settlement.ID).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != len(ids) {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsSettlement.Get(ctx, settlement.ID)
}

// lockSettlement 锁定结算单：draft → locked。
func lockSettlement(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsSettlement, error) {
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
	if settlement.Status != esportssettlement.StatusDraft {
		return nil, errConflict
	}
	// 旧版在创建草稿时已入账。识别旧草稿并只锁定状态，避免升级后再次入账。
	var pendingPlayerEntries bool
	if settlement.OwnerType == esportssettlement.OwnerTypePlayer {
		entries, err := tx.EsportsCommissionEntry.Query().Where(
			esportscommissionentry.SettlementIDEQ(id),
		).All(ctx)
		if err != nil {
			return nil, err
		}
		if len(entries) != settlement.EntryCount || len(entries) == 0 {
			return nil, errConflict
		}
		var pendingCount, settledCount int
		var total int64
		for _, entry := range entries {
			if entry.PlayerID != settlement.OwnerID {
				return nil, errConflict
			}
			total += entry.AmountCents
			switch entry.Status {
			case esportscommissionentry.StatusPendingSettlement:
				pendingCount++
			case esportscommissionentry.StatusSettled:
				settledCount++
			default:
				return nil, errConflict
			}
		}
		if total != settlement.NetCents || (pendingCount > 0 && settledCount > 0) {
			return nil, errConflict
		}
		credited, err := tx.EsportsWalletTx.Query().Where(
			esportswallettx.AccountTypeEQ(esportswallettx.AccountTypePlayerCatfood),
			esportswallettx.OwnerIDEQ(settlement.OwnerID),
			esportswallettx.SourceTypeEQ("settlement"),
			esportswallettx.SourceIDEQ(settlement.SettlementNo),
		).Count(ctx)
		if err != nil {
			return nil, err
		}
		pendingPlayerEntries = pendingCount == len(entries)
		expectedLegacyCredits := 0
		if settlement.NetCents != 0 {
			expectedLegacyCredits = 1
		}
		if (pendingPlayerEntries && credited != 0) ||
			(!pendingPlayerEntries && credited != expectedLegacyCredits) {
			return nil, errConflict
		}
	}
	changed, err := tx.EsportsSettlement.Update().Where(
		esportssettlement.IDEQ(id),
		esportssettlement.StatusEQ(esportssettlement.StatusDraft),
	).SetStatus(esportssettlement.StatusLocked).SetLockedByAdminID(adminID).
		SetLockedAt(now.UTC()).
		// P0-8 乐观锁：锁定也抬版本，与客服提成侧的发放 / 作废版本守卫保持同一口径。
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if pendingPlayerEntries {
		changed, err = tx.EsportsCommissionEntry.Update().Where(
			esportscommissionentry.SettlementIDEQ(id),
			esportscommissionentry.StatusEQ(esportscommissionentry.StatusPendingSettlement),
		).SetStatus(esportscommissionentry.StatusSettled).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != settlement.EntryCount {
			return nil, errConflict
		}
		if settlement.NetCents != 0 {
			if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
				SetAccountType(esportswallettx.AccountTypePlayerCatfood).SetOwnerID(settlement.OwnerID).
				SetEntryType(esportswallettx.EntryTypeRecharge).
				SetPrincipalDeltaCents(settlement.NetCents).SetSourceType("settlement").
				SetSourceID(settlement.SettlementNo).SetOperatorID(adminID).
				SetNote("分成结算").Save(ctx); err != nil {
				if ent.IsConstraintError(err) {
					return nil, errConflict
				}
				return nil, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsSettlement.Get(ctx, id)
}

func getSettlement(ctx context.Context, db *ent.Client, id int) (*ent.EsportsSettlement, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	return db.EsportsSettlement.Get(ctx, id)
}

// settlementDetailView 是结算单详情读模型（§12.6-06）：主表 + 本期可下钻明细，
// 明细带 rule_no / rule_version，使规则版本可以从结算单追溯。
//
// Entries 用 any 承载两种读模型：
//   - owner_type=player → commissionEntryView（陪玩分成明细，含规则数值 / 扣减项 / 到手净额）；
//   - owner_type=cs     → csSettlementEntryView（客服提成明细，金额 / 类型 / 来源单号 / 时间）。
//
// 两者 JSON 字段名对齐（id、kind、amount_cents、base_cents、status、created_at、net_cents…），
// 前端「结算单详情」表格可同一套列渲染；主体差异字段（player_id / admin_id / order_no）按主体给出。
type settlementDetailView struct {
	Settlement *ent.EsportsSettlement `json:"settlement"`
	Entries    []any                  `json:"entries"`
	EntryCount int                    `json:"entry_count"`
	// DetailTruncated 为真表示本期明细超过返回上限，避免「静默漏数」。
	DetailTruncated bool `json:"detail_truncated"`
}

// csSettlementEntryView 是客服提成明细的结算单详情读模型（§12.2 / §12.6-06）：
// 与陪玩侧字段名对齐（金额、类型、来源单号、时间 + 规则快照 + 状态），
// 主体侧差异用 admin_id（业绩归属客服）替代 player_id。
type csSettlementEntryView struct {
	ID          int    `json:"id"`
	OrderID     *int   `json:"order_id"`
	OrderNo     string `json:"order_no"`
	GiftOrderID *int   `json:"gift_order_id"`
	AdminID     int    `json:"admin_id"`
	Kind        string `json:"kind"`
	CalcMode    string `json:"calc_mode"`
	BaseMode    string `json:"base_mode"`
	RuleNo      string `json:"rule_no"`
	RuleVersion int    `json:"rule_version"`
	BaseCents   int64  `json:"base_cents"`
	AmountCents int64  `json:"amount_cents"`
	// NetCents 与陪玩侧同名：客服提成明细没有逐单扣减项，净额即本明细金额。
	NetCents     int64     `json:"net_cents"`
	Status       string    `json:"status"`
	SettlementID *int      `json:"settlement_id"`
	Note         string    `json:"note"`
	CreatedAt    time.Time `json:"created_at"`
}

// csSettlementEntryViews 把客服提成明细补成结算单详情读模型：批量解析来源单号
// （服务 / 冲回明细看订单号，礼物明细看礼物单号），避免逐条查询。
func csSettlementEntryViews(ctx context.Context, db *ent.Client,
	rows []*ent.EsportsCsCommissionEntry) ([]any, error) {
	views := make([]any, 0, len(rows))
	if len(rows) == 0 {
		return views, nil
	}
	orderIDs := make([]int, 0, len(rows))
	giftIDs := make([]int, 0, len(rows))
	seenOrder, seenGift := map[int]bool{}, map[int]bool{}
	for _, row := range rows {
		if row.OrderID != nil && !seenOrder[*row.OrderID] {
			seenOrder[*row.OrderID] = true
			orderIDs = append(orderIDs, *row.OrderID)
		}
		if row.GiftOrderID != nil && !seenGift[*row.GiftOrderID] {
			seenGift[*row.GiftOrderID] = true
			giftIDs = append(giftIDs, *row.GiftOrderID)
		}
	}
	orderNos := map[int]string{}
	if len(orderIDs) > 0 {
		orders, err := db.EsportsOrder.Query().Where(esportsorder.IDIn(orderIDs...)).
			Select(esportsorder.FieldID, esportsorder.FieldOrderNo).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, order := range orders {
			orderNos[order.ID] = order.OrderNo
		}
	}
	giftNos := map[int]string{}
	if len(giftIDs) > 0 {
		gifts, err := db.EsportsGiftOrder.Query().Where(esportsgiftorder.IDIn(giftIDs...)).
			Select(esportsgiftorder.FieldID, esportsgiftorder.FieldGiftNo).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, gift := range gifts {
			giftNos[gift.ID] = gift.GiftNo
		}
	}
	for _, row := range rows {
		orderNo := ""
		if row.OrderID != nil {
			orderNo = orderNos[*row.OrderID]
		}
		if orderNo == "" && row.GiftOrderID != nil {
			orderNo = giftNos[*row.GiftOrderID]
		}
		views = append(views, csSettlementEntryView{
			ID: row.ID, OrderID: row.OrderID, OrderNo: orderNo,
			GiftOrderID: row.GiftOrderID, AdminID: row.AdminID,
			Kind: row.Kind.String(), CalcMode: row.CalcMode.String(), BaseMode: row.BaseMode.String(),
			RuleNo: row.RuleNo, RuleVersion: row.RuleVersion,
			BaseCents: row.BaseCents, AmountCents: row.AmountCents, NetCents: row.AmountCents,
			Status: row.Status.String(), SettlementID: row.SettlementID,
			Note: row.Note, CreatedAt: row.CreatedAt.UTC(),
		})
	}
	return views, nil
}

// settlementEntriesByOwner 按 owner_type 分流查询结算单明细：陪玩结算单查 EsportsCommissionEntry，
// 客服提成结算单查 EsportsCsCommissionEntry。
// 修复前一律只查陪玩明细，导致客服提成结算单详情 entries 恒为空（前端只能看汇总、无法下钻核对）。
func settlementEntriesByOwner(ctx context.Context, db *ent.Client,
	settlement *ent.EsportsSettlement, limit int) ([]any, bool, error) {
	if settlement.OwnerType == esportssettlement.OwnerTypeCs {
		rows, err := db.EsportsCsCommissionEntry.Query().Where(
			esportscscommissionentry.SettlementIDEQ(settlement.ID),
		).Order(ent.Asc(esportscscommissionentry.FieldID)).Limit(limit + 1).All(ctx)
		if err != nil {
			return nil, false, err
		}
		truncated := len(rows) > limit
		if truncated {
			rows = rows[:limit]
		}
		views, err := csSettlementEntryViews(ctx, db, rows)
		return views, truncated, err
	}
	rows, err := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.SettlementIDEQ(settlement.ID),
	).Order(ent.Asc(esportscommissionentry.FieldID)).Limit(limit + 1).All(ctx)
	if err != nil {
		return nil, false, err
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	views, err := commissionEntryViews(ctx, db, rows)
	if err != nil {
		return nil, false, err
	}
	out := make([]any, 0, len(views))
	for _, view := range views {
		out = append(out, view)
	}
	return out, truncated, nil
}

// getSettlementDetail 读结算单详情并内联本期全部待结算 / 已结算明细（按明细 ID 升序）。
// 明细按 owner_type 分流：陪玩分成结算单 → 分成明细；客服提成结算单 → 客服提成明细。
func getSettlementDetail(ctx context.Context, db *ent.Client, id, limit int) (*settlementDetailView, error) {
	settlement, err := getSettlement(ctx, db, id)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 2000 {
		limit = 200
	}
	views, truncated, err := settlementEntriesByOwner(ctx, db, settlement, limit)
	if err != nil {
		return nil, err
	}
	return &settlementDetailView{
		Settlement: settlement, Entries: views,
		EntryCount: len(views), DetailTruncated: truncated,
	}, nil
}

// listSettlements 列出结算单，playerID 非空时按陪玩过滤，按 ID 倒序。
// status 为可选状态过滤（P0-8）：
//   - 不传：默认只返回**未作废**的结算单（作废单不干扰日常发放视图）；
//   - draft / locked / paid：只看该状态；
//   - void：只看已作废单（审计与重开追溯需要能查到）；
//   - all：不过滤状态，返回全部。
func listSettlements(ctx context.Context, db *ent.Client, playerID, limit int, status ...string) ([]*ent.EsportsSettlement, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsSettlement.Query()
	if playerID > 0 {
		q.Where(esportssettlement.OwnerIDEQ(playerID))
	}
	filter := ""
	if len(status) > 0 {
		filter = strings.TrimSpace(status[0])
	}
	switch filter {
	case "":
		q.Where(esportssettlement.StatusNEQ(esportssettlement.StatusVoid))
	case "all":
		// 显式要求全部状态，不加过滤。
	case "draft", "locked", "paid", "void":
		q.Where(esportssettlement.StatusEQ(esportssettlement.Status(filter)))
	default:
		return nil, errInvalidInput
	}
	return q.Order(ent.Desc(esportssettlement.FieldID)).Limit(limit).All(ctx)
}

// listCommissionEntries 列出分成明细，按需过滤，按 ID 倒序。
func listCommissionEntries(ctx context.Context, db *ent.Client, playerID, orderID int, status string, limit int) ([]*ent.EsportsCommissionEntry, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	q := db.EsportsCommissionEntry.Query()
	if playerID > 0 {
		q.Where(esportscommissionentry.PlayerIDEQ(playerID))
	}
	if orderID > 0 {
		q.Where(esportscommissionentry.OrderIDEQ(orderID))
	}
	if status = strings.TrimSpace(status); status != "" {
		switch status {
		case "pending_settlement", "settled", "void":
			q.Where(esportscommissionentry.StatusEQ(esportscommissionentry.Status(status)))
		default:
			return nil, errInvalidInput
		}
	}
	return q.Order(ent.Desc(esportscommissionentry.FieldID)).Limit(limit).All(ctx)
}

// commissionEntryView 是分成明细的读模型（§12.7-01）：在明细原始字段（分成方式、
// 规则版本、基数、金额）之上补出规则数值、该明细被冲回的扣减项与到手净额，
// 以及按最小服务单位拆分的留痕（§15.5-04）。
type commissionEntryView struct {
	*ent.EsportsCommissionEntry
	// RuleValueBp / RuleValueCents 是该明细规则快照的数值：percent 看 bp，固定额看分。
	RuleValueBp    int   `json:"rule_value_bp"`
	RuleValueCents int64 `json:"rule_value_cents"`
	// DeductedCents 是同一订单同一陪玩在该明细口径上累计的冲回金额（负数，含尾差单列行）。
	DeductedCents int64 `json:"deducted_cents"`
	// NetCents 是到手净额：本明细金额 + 扣减项（明细行扣减后净额；冲回行即其本身金额）。
	NetCents int64 `json:"net_cents"`
	// ServiceUnitAmounts 是逐最小服务单位留痕（§15.5-04，承诺量 > 1 时给出）：
	// 分成明细为「每单位折后金额」，退款冲回明细为「每单位冲回额」，尾差记在最后一单位。
	ServiceUnitAmounts []int64 `json:"service_unit_amounts,omitempty"`
}

// listCommissionEntryViews 列出分成明细读模型（管理员 / 陪玩端共用同一口径）。
func listCommissionEntryViews(ctx context.Context, db *ent.Client, playerID, orderID int, status string, limit int) ([]commissionEntryView, error) {
	entries, err := listCommissionEntries(ctx, db, playerID, orderID, status, limit)
	if err != nil {
		return nil, err
	}
	return commissionEntryViews(ctx, db, entries)
}

// commissionEntryViews 把一批明细补成读模型：批量取规则数值与订单承诺服务量，
// 并把冲回明细按（订单 + 陪玩 + 口径）归集为对应正向明细的扣减项。
func commissionEntryViews(ctx context.Context, db *ent.Client, entries []*ent.EsportsCommissionEntry) ([]commissionEntryView, error) {
	views := make([]commissionEntryView, 0, len(entries))
	if len(entries) == 0 {
		return views, nil
	}
	ruleIDs := make([]int, 0, len(entries))
	orderIDs := make([]int, 0, len(entries))
	seenRule, seenOrder := map[int]bool{}, map[int]bool{}
	// 冲回归集：服务/加钟口径按（订单 + 陪玩），礼物按陪玩（礼物订单与订单 ID 空间不通用）。
	serviceReversed, addonReversed := map[[2]int]int64{}, map[[2]int]int64{}
	giftReversed := map[int]int64{}
	for _, entry := range entries {
		orderID := 0
		if entry.OrderID != nil {
			orderID = *entry.OrderID
			if !seenOrder[orderID] {
				seenOrder[orderID] = true
				orderIDs = append(orderIDs, orderID)
			}
		}
		if entry.RuleID != nil && !seenRule[*entry.RuleID] {
			seenRule[*entry.RuleID] = true
			ruleIDs = append(ruleIDs, *entry.RuleID)
		}
		switch entry.Kind {
		case esportscommissionentry.KindReversal:
			// 加钟冲回（含尾差单列行）备注带加钟标记，其余计入服务口径。
			if strings.Contains(entry.Note, addonReversalNote) {
				addonReversed[[2]int{orderID, entry.PlayerID}] += entry.AmountCents
			} else {
				serviceReversed[[2]int{orderID, entry.PlayerID}] += entry.AmountCents
			}
		case esportscommissionentry.KindGiftReversal:
			giftReversed[entry.PlayerID] += entry.AmountCents
		}
	}
	rules := map[int]*ent.EsportsCommissionRule{}
	if len(ruleIDs) > 0 {
		items, err := db.EsportsCommissionRule.Query().
			Where(esportscommissionrule.IDIn(ruleIDs...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, rule := range items {
			rules[rule.ID] = rule
		}
	}
	// 单位级分摊的分母按口径取：服务明细用承诺服务量，加钟明细用加钟服务量（§15.5-04）。
	type orderUnits struct{ service, addon int64 }
	unitsByOrder := map[int]orderUnits{}
	if len(orderIDs) > 0 {
		items, err := db.EsportsOrder.Query().Where(esportsorder.IDIn(orderIDs...)).
			Select(esportsorder.FieldID, esportsorder.FieldPromisedServiceUnits,
				esportsorder.FieldAddonUnits).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, order := range items {
			unitsByOrder[order.ID] = orderUnits{
				service: order.PromisedServiceUnits, addon: order.AddonUnits,
			}
		}
	}
	for _, entry := range entries {
		view := commissionEntryView{EsportsCommissionEntry: entry, NetCents: entry.AmountCents}
		if entry.RuleID != nil {
			if rule := rules[*entry.RuleID]; rule != nil {
				view.RuleValueBp = rule.ValueBp
				view.RuleValueCents = rule.ValueCents
			}
		}
		orderID := 0
		if entry.OrderID != nil {
			orderID = *entry.OrderID
		}
		switch entry.Kind {
		case esportscommissionentry.KindService:
			view.DeductedCents = serviceReversed[[2]int{orderID, entry.PlayerID}]
		case esportscommissionentry.KindAddon:
			view.DeductedCents = addonReversed[[2]int{orderID, entry.PlayerID}]
		case esportscommissionentry.KindGift:
			view.DeductedCents = giftReversed[entry.PlayerID]
		}
		view.NetCents = entry.AmountCents + view.DeductedCents
		// §15.5-04：逐最小服务单位留痕，尾差记在最后一单位（§18.2 裁决）。
		// 分成明细给「每单位折后金额」（折后明细金额 ÷ 承诺服务量），退款冲回明细给「每单位冲回额」；
		// 尾差单列行不再重复拆分，礼物类不按订单服务量拆分。
		units := unitsByOrder[orderID].service
		amount := entry.BaseCents
		switch entry.Kind {
		case esportscommissionentry.KindAddon:
			units = unitsByOrder[orderID].addon
		case esportscommissionentry.KindReversal:
			if strings.Contains(entry.Note, tailReversalNote) {
				amount = 0
			} else {
				amount = -entry.AmountCents
				if strings.Contains(entry.Note, addonReversalNote) {
					units = unitsByOrder[orderID].addon
				}
			}
		case esportscommissionentry.KindGift, esportscommissionentry.KindGiftReversal:
			amount = 0
		}
		if units > 1 && amount > 0 {
			view.ServiceUnitAmounts = splitServiceUnits(amount, units)
		}
		views = append(views, view)
	}
	return views, nil
}

type playerCommissionView struct {
	PendingSettlementCents int64 `json:"pending_settlement_cents"`
	// FrozenCents 被争议冻结的待结算金额（§12.6）：仍属待结算但不参与当期结算。
	FrozenCents      int64     `json:"frozen_cents"`
	SettledCents     int64     `json:"settled_cents"`
	NextSettlementAt time.Time `json:"next_settlement_at"`
	// 生效周期与命中层级，便于陪玩端解释「下次结算时间」的来源。
	SettlementPeriod string `json:"settlement_period"`
	SettlementScope  string `json:"settlement_scope"`
}

// settlementPeriod 读系统配置得到结算周期；缺失或非法回落 weekly。
func settlementPeriod(ctx context.Context, db *ent.Client) string {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(settlementPeriodKey)).Only(ctx)
	if err != nil {
		return "weekly"
	}
	switch strings.TrimSpace(item.Value) {
	case "daily", "weekly", "monthly":
		return strings.TrimSpace(item.Value)
	}
	return "weekly"
}

// nextSettlementAt 按周期（UTC 自然周期）推算下一个周期起点：daily 次日 0 点、
// weekly 下周一 0 点、monthly 次月 1 日 0 点。
func nextSettlementAt(period string, now time.Time) time.Time {
	utc := now.UTC()
	day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case "daily":
		return day.AddDate(0, 0, 1)
	case "monthly":
		return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	default:
		offset := (int(day.Weekday()) + 6) % 7 // 距本周一的天数
		return day.AddDate(0, 0, -offset).AddDate(0, 0, 7)
	}
}

// playerCommissionSummary 陪玩端分成概览：待结算净额、争议冻结额、已结算合计、下次结算时间。
// 结算周期按 §12.6 逐级覆盖解析（陪玩 → 等级 → 游戏 → 全局 → 系统配置）。
func playerCommissionSummary(ctx context.Context, db *ent.Client, playerID int, now time.Time) (*playerCommissionView, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	player, err := db.EsportsPlayer.Get(ctx, playerID)
	if err != nil {
		return nil, err
	}
	rows, err := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.PlayerIDEQ(playerID),
		esportscommissionentry.StatusIn(
			esportscommissionentry.StatusPendingSettlement, esportscommissionentry.StatusSettled),
	).Select(esportscommissionentry.FieldAmountCents, esportscommissionentry.FieldStatus,
		esportscommissionentry.FieldFrozenAt).All(ctx)
	if err != nil {
		return nil, err
	}
	var pending, frozen, settled int64
	for _, row := range rows {
		switch row.Status {
		case esportscommissionentry.StatusPendingSettlement:
			if row.FrozenAt != nil {
				frozen += row.AmountCents
				continue
			}
			pending += row.AmountCents
		case esportscommissionentry.StatusSettled:
			settled += row.AmountCents
		}
	}
	cycle, err := resolveSettlementCycle(ctx, db, player)
	if err != nil {
		return nil, err
	}
	_, _, next := settlementCycleWindow(cycle, now)
	return &playerCommissionView{
		PendingSettlementCents: pending,
		FrozenCents:            frozen,
		SettledCents:           settled,
		NextSettlementAt:       next,
		SettlementPeriod:       cycle.Period,
		SettlementScope:        cycle.Scope,
	}, nil
}

// freezeOrderCommissionForDispute 冻结订单的待结算分成明细（§12.6 争议冻结）。
// playerID 非空时只冻结该成员；已结算 / 已冻结的明细不受影响，返回本次冻结条数。
func freezeOrderCommissionForDispute(ctx context.Context, db *ent.Client, orderID int,
	playerID *int, reason string, adminID int, now time.Time) (int, error) {
	return freezeCommissionForOrder(ctx, db.EsportsCommissionEntry, orderID, playerID, reason, adminID, now)
}

// unfreezeOrderCommission 解冻订单的待结算分成明细（争议终结后恢复可结算）。
func unfreezeOrderCommission(ctx context.Context, db *ent.Client, orderID int) (int, error) {
	return unfreezeCommissionForOrder(ctx, db.EsportsCommissionEntry, orderID)
}

// freezeCommissionForOrder 事务内外通用的冻结实现（tx 与 client 共用同一 client 类型）。
func freezeCommissionForOrder(ctx context.Context, entries *ent.EsportsCommissionEntryClient,
	orderID int, playerID *int, reason string, adminID int, now time.Time) (int, error) {
	if orderID < 1 {
		return 0, errInvalidInput
	}
	if len([]rune(reason)) > 255 {
		return 0, errInvalidInput
	}
	q := entries.Update().Where(
		esportscommissionentry.OrderIDEQ(orderID),
		esportscommissionentry.StatusEQ(esportscommissionentry.StatusPendingSettlement),
		esportscommissionentry.FrozenAtIsNil(),
		// 已纳入结算单（草稿或已锁）的明细不再冻结，避免冻结与结算单口径冲突。
		esportscommissionentry.SettlementIDIsNil(),
	)
	if playerID != nil && *playerID > 0 {
		q = q.Where(esportscommissionentry.PlayerIDEQ(*playerID))
	}
	changed, err := q.SetFrozenAt(now.UTC()).SetFrozenReason(reason).
		SetFrozenByAdminID(adminID).AddVersion(1).Save(ctx)
	if err != nil {
		return 0, err
	}
	return changed, nil
}

// unfreezeCommissionForOrder 事务内外通用的解冻实现。
func unfreezeCommissionForOrder(ctx context.Context, entries *ent.EsportsCommissionEntryClient,
	orderID int) (int, error) {
	if orderID < 1 {
		return 0, errInvalidInput
	}
	changed, err := entries.Update().Where(
		esportscommissionentry.OrderIDEQ(orderID),
		esportscommissionentry.StatusEQ(esportscommissionentry.StatusPendingSettlement),
		esportscommissionentry.FrozenAtNotNil(),
	).ClearFrozenAt().SetFrozenReason("").ClearFrozenByAdminID().AddVersion(1).Save(ctx)
	if err != nil {
		return 0, err
	}
	return changed, nil
}

// setOrderManualCommissionRule 设置或清除订单的「单笔手动指定」分成规则（§12.3 五级优先级最高一级）。
// 只对尚未锁定规则的席位生效：成员在接单 / 抢单时点已把规则快照写入席位（§12.4），
// 事后改写会破坏「关键时点锁定」语义，因此订单已存在锁定席位时直接拒绝，由派单流程重新指派。
// ruleID=0 表示清除，恢复按个体 / 等级 / 游戏 / 全局逐级回落。
func setOrderManualCommissionRule(ctx context.Context, db *ent.Client, orderID, ruleID, adminID int, now time.Time) (*ent.EsportsOrder, error) {
	if orderID < 1 || ruleID < 0 || adminID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	// 存单冻结期间禁止改写订单分成规则：规则快照由派单时点锁定，冻结单不应再产生资金口径变化。
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.FulfillmentStatus == esportsorder.FulfillmentStatusCompleted ||
		order.FulfillmentStatus == esportsorder.FulfillmentStatusCancelled {
		return nil, errConflict
	}
	locked, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.CommissionRuleLockedAtNotNil(),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if locked {
		return nil, errConflict
	}
	update := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
	).AddVersion(1)
	eventType := "order.commission_rule_manual_cleared"
	if ruleID == 0 {
		update.ClearManualCommissionRuleID().ClearManualCommissionRuleSetByAdminID().ClearManualCommissionRuleSetAt()
	} else {
		rule, rErr := tx.EsportsCommissionRule.Get(ctx, ruleID)
		if rErr != nil {
			if ent.IsNotFound(rErr) {
				return nil, errInvalidInput
			}
			return nil, rErr
		}
		// 只能指定到当前仍生效的版本；草稿 / 历史版本写进订单会变成永不命中的死配置。
		if rule.Status != esportscommissionrule.StatusActive && rule.Status != esportscommissionrule.StatusScheduled {
			return nil, errInvalidInput
		}
		update.SetManualCommissionRuleID(rule.ID).SetManualCommissionRuleSetByAdminID(adminID).
			SetManualCommissionRuleSetAt(now.UTC())
		eventType = "order.commission_rule_manual_set"
	}
	changed, err := update.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(order.Version + 1).SetEventType(eventType).
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"manual_commission_rule_id": ruleID,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

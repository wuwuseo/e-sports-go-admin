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
	"go-admin/internal/ent/esportsdispatchseat"
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
}

func validCommissionScope(scope string) bool {
	switch scope {
	case "global", "game", "player":
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
	segs, err := segments.Query().Where(esportsservicesegment.OrderIDEQ(orderID)).All(ctx)
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
		members = append(members, commissionMember{PlayerID: *seat.PlayerID, SeatIndex: seat.SeatIndex})
	}
	return members, nil
}

// resolveCommissionRule 解析订单适用的生效分成规则，按具体度 player > game > global 择一，
// 同层多条取 version 最大。找不到返回 nil, nil（表示无规则，明细不生成）。
func resolveCommissionRule(ctx context.Context, db *ent.Client, order *ent.EsportsOrder) (*ent.EsportsCommissionRule, error) {
	return resolveCommissionRuleIn(ctx, db.EsportsCommissionRule, db.EsportsServiceSegment,
		db.EsportsDispatchSeat, db.EsportsSku, db.EsportsProductCategory, order)
}

// resolveCommissionRuleIn 与 resolveCommissionRule 同逻辑，供事务内（tx）复用。
func resolveCommissionRuleIn(ctx context.Context,
	rules *ent.EsportsCommissionRuleClient,
	segments *ent.EsportsServiceSegmentClient,
	seats *ent.EsportsDispatchSeatClient,
	skus *ent.EsportsSkuClient,
	categories *ent.EsportsProductCategoryClient,
	order *ent.EsportsOrder,
) (*ent.EsportsCommissionRule, error) {
	active, err := rules.Query().Where(
		esportscommissionrule.StatusEQ(esportscommissionrule.StatusActive),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}
	pick := func(scope esportscommissionrule.Scope, key string) *ent.EsportsCommissionRule {
		var best *ent.EsportsCommissionRule
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
	members, err := loadOrderMembers(ctx, segments, seats, order.ID)
	if err != nil {
		return nil, err
	}
	for _, member := range members {
		if rule := pick(esportscommissionrule.ScopePlayer, strconv.Itoa(member.PlayerID)); rule != nil {
			return rule, nil
		}
	}
	// 取 SKU 所属分类的 game 作为 game 层键；分类为空则跳过该层。
	if sku, err := skus.Get(ctx, order.SkuID); err == nil && sku.CategoryID != nil {
		if category, err := categories.Get(ctx, *sku.CategoryID); err == nil {
			if game := strings.TrimSpace(category.Game); game != "" {
				if rule := pick(esportscommissionrule.ScopeGame, game); rule != nil {
					return rule, nil
				}
			}
		}
	}
	return pick(esportscommissionrule.ScopeGlobal, ""), nil
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

// activateCommissionRule 生效指定规则：同 rule_no 现有 active 转 history，本行（须 draft）转 active。
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
	if _, err = tx.EsportsCommissionRule.Update().Where(
		esportscommissionrule.RuleNoEQ(target.RuleNo),
		esportscommissionrule.StatusEQ(esportscommissionrule.StatusActive),
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
		esportscommissionrule.StatusEQ(esportscommissionrule.StatusActive),
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

// accrueOrderCommission 结单时分成分成，必须在 closeOrder 的同一事务内调用。
// 幂等：该订单已有 service 明细则直接返回，不重复生成。
// ponytail: 客服提成（§12.2）、渠道成本、工作室剩余反算、礼物收入分成、加钟单独明细均未实现；
// 分成主体只有陪玩，且规则在结单时统一快照（§12.4 的「成员接受/抢单时逐成员锁定」未做）。
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
	rule, err := resolveCommissionRuleIn(ctx, tx.EsportsCommissionRule, tx.EsportsServiceSegment,
		tx.EsportsDispatchSeat, tx.EsportsSku, tx.EsportsProductCategory, order)
	if err != nil {
		return err
	}
	if rule == nil {
		return nil
	}
	pool := commissionPool(order, rule)
	note := ""
	// 上限：分成池不得超过可分基数（订单应付），超出则截断并注明。
	if pool > order.PayableCents {
		pool = order.PayableCents
		note = "已按可分基数上限截断"
	}
	if pool <= 0 {
		return nil
	}
	amounts := splitPool(pool, members)
	for i, member := range members {
		if _, err = tx.EsportsCommissionEntry.Create().
			SetIdemKey("commission:service:" + order.OrderNo + ":" + strconv.Itoa(member.PlayerID)).
			SetOrderID(order.ID).SetPlayerID(member.PlayerID).
			SetKind(esportscommissionentry.KindService).SetSeatIndex(member.SeatIndex).
			SetRuleID(rule.ID).SetRuleNo(rule.RuleNo).SetRuleVersion(rule.Version).
			SetCalcMode(esportscommissionentry.CalcMode(rule.CalcMode.String())).
			SetBaseCents(order.PayableCents).SetAmountCents(amounts[i]).
			SetStatus(esportscommissionentry.StatusPendingSettlement).SetNote(note).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return errConflict
			}
			return err
		}
	}
	return nil
}

// reverseOrderCommission 退款按比例冲回分成，必须在 executeAfterSaleOnce 的同一事务内、
// 退款额度落账之后调用。新增 kind=reversal 的负数明细，使退款金额落在退款当期（§12.6）；
// 幂等靠 idem_key 唯一索引兜底，重复执行不重复冲回。
// ponytail: 舍入尾差未单列（§12.1 要求尾差单列）；「优先按对应服务明细和成员冲回」未做，
// 统一按订单整体退款比例冲回。
func reverseOrderCommission(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, item *ent.EsportsAfterSale, now time.Time) error {
	if item.RefundCents <= 0 || order.PayableCents <= 0 {
		return nil
	}
	entries, err := tx.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		// §12.5：退款同时冲回原单服务分成与加钟分成（加钟退款分摊）。
		esportscommissionentry.KindIn(
			esportscommissionentry.KindService, esportscommissionentry.KindAddon),
	).All(ctx)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		reversal := -(entry.AmountCents * item.RefundCents / order.PayableCents)
		if reversal == 0 {
			continue
		}
		// 加钟明细可能同一陪玩多条，幂等键改用明细 ID 以免互相覆盖。
		idem := "commission:reversal:" + item.AfterSaleNo + ":" + strconv.Itoa(entry.PlayerID)
		if entry.Kind == esportscommissionentry.KindAddon {
			idem = "commission:reversal:addon:" + item.AfterSaleNo + ":" + strconv.Itoa(entry.ID)
		}
		create := tx.EsportsCommissionEntry.Create().
			SetIdemKey(idem).
			SetOrderID(order.ID).SetPlayerID(entry.PlayerID).
			SetKind(esportscommissionentry.KindReversal).SetSeatIndex(entry.SeatIndex).
			SetAfterSaleID(item.ID).SetBaseCents(order.PayableCents).SetAmountCents(reversal).
			SetRuleNo(entry.RuleNo).SetRuleVersion(entry.RuleVersion).
			SetCalcMode(esportscommissionentry.CalcMode(entry.CalcMode.String())).
			SetStatus(esportscommissionentry.StatusPendingSettlement)
		if entry.Kind == esportscommissionentry.KindAddon {
			create.SetNote("加钟退款分摊")
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
	}
	return nil
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
	if net != 0 {
		if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
			SetAccountType(esportswallettx.AccountTypePlayerCatfood).SetOwnerID(playerID).
			SetEntryType(esportswallettx.EntryTypeRecharge).
			SetPrincipalDeltaCents(net).SetSourceType("settlement").
			SetSourceID(settlement.SettlementNo).SetOperatorID(adminID).
			SetNote("分成结算").Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
	}
	changed, err := tx.EsportsCommissionEntry.Update().Where(
		esportscommissionentry.IDIn(ids...),
		esportscommissionentry.StatusEQ(esportscommissionentry.StatusPendingSettlement),
	).SetStatus(esportscommissionentry.StatusSettled).SetSettlementID(settlement.ID).
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
	changed, err := db.EsportsSettlement.Update().Where(
		esportssettlement.IDEQ(id),
		esportssettlement.StatusEQ(esportssettlement.StatusDraft),
	).SetStatus(esportssettlement.StatusLocked).SetLockedByAdminID(adminID).
		SetLockedAt(now.UTC()).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsSettlement.Get(ctx, id)
}

func getSettlement(ctx context.Context, db *ent.Client, id int) (*ent.EsportsSettlement, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	return db.EsportsSettlement.Get(ctx, id)
}

// listSettlements 列出结算单，playerID 非空时按陪玩过滤，按 ID 倒序。
func listSettlements(ctx context.Context, db *ent.Client, playerID, limit int) ([]*ent.EsportsSettlement, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsSettlement.Query()
	if playerID > 0 {
		q.Where(esportssettlement.OwnerIDEQ(playerID))
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

type playerCommissionView struct {
	PendingSettlementCents int64     `json:"pending_settlement_cents"`
	SettledCents           int64     `json:"settled_cents"`
	NextSettlementAt       time.Time `json:"next_settlement_at"`
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

// playerCommissionSummary 陪玩端分成概览：待结算净额、已结算合计、下次结算时间。
// ponytail: 结算周期仅支持 daily/weekly/monthly 三种预设，§12.6 的「按游戏/等级/单个陪玩逐级覆盖」
// 与「自定义间隔」未实现；争议冻结未实现。
func playerCommissionSummary(ctx context.Context, db *ent.Client, playerID int, now time.Time) (*playerCommissionView, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	if _, err := db.EsportsPlayer.Get(ctx, playerID); err != nil {
		return nil, err
	}
	rows, err := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.PlayerIDEQ(playerID),
		esportscommissionentry.StatusIn(
			esportscommissionentry.StatusPendingSettlement, esportscommissionentry.StatusSettled),
	).Select(esportscommissionentry.FieldAmountCents, esportscommissionentry.FieldStatus).All(ctx)
	if err != nil {
		return nil, err
	}
	var pending, settled int64
	for _, row := range rows {
		switch row.Status {
		case esportscommissionentry.StatusPendingSettlement:
			pending += row.AmountCents
		case esportscommissionentry.StatusSettled:
			settled += row.AmountCents
		}
	}
	return &playerCommissionView{
		PendingSettlementCents: pending,
		SettledCents:           settled,
		NextSettlementAt:       nextSettlementAt(settlementPeriod(ctx, db), now),
	}, nil
}

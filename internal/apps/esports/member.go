package esports

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsmemberleveloverride"
	"go-admin/internal/ent/esportsmemberlevelrule"
	"go-admin/internal/ent/systemconfig"
)

// 会员等级（§10）相关常量。
const (
	// 等级保护期天数系统配置键；未配置时用默认值（见 memberDowngradeGuardDays）。
	memberDowngradeGuardKey = "app.esports.member_downgrade_guard_days"
	// 默认等级保护期：成长值跌破门槛后 30 天内不降级。
	defaultMemberDowngradeGuardDays = 30
	// 折扣基点满值：10000 = 不打折。
	memberDiscountBpsFull = 10000
)

var memberLevelCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// memberLevelInput 是等级配置的可编辑字段。code 创建后不可变。
type memberLevelInput struct {
	Code             string `json:"code"`
	Name             string `json:"name"`
	GrowthThreshold  int64  `json:"growth_threshold"`
	DiscountBps      int    `json:"discount_bps"`
	PriorityDispatch bool   `json:"priority_dispatch"`
	PrepayBonusBps   int    `json:"prepay_bonus_bps"`
	SortOrder        int    `json:"sort_order"`
}

// validateMemberLevelInput 校验等级配置；任一不满足返回 errInvalidInput，调用方不得写库。
func validateMemberLevelInput(in memberLevelInput) error {
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 32 || !memberLevelCodePattern.MatchString(strings.TrimSpace(in.Code)) {
		return errInvalidInput
	}
	if in.GrowthThreshold < 0 || in.DiscountBps < 0 || in.DiscountBps > memberDiscountBpsFull ||
		in.PrepayBonusBps < 0 || in.PrepayBonusBps > memberDiscountBpsFull {
		return errInvalidInput
	}
	return nil
}

// createMemberLevel 新建等级配置（默认 enabled）；code 冲突返回 errConflict。
func createMemberLevel(ctx context.Context, db *ent.Client, in memberLevelInput) (*ent.EsportsMemberLevelRule, error) {
	if err := validateMemberLevelInput(in); err != nil {
		return nil, err
	}
	item, err := db.EsportsMemberLevelRule.Create().
		SetCode(strings.TrimSpace(in.Code)).SetName(strings.TrimSpace(in.Name)).
		SetGrowthThreshold(in.GrowthThreshold).SetDiscountBps(in.DiscountBps).
		SetPriorityDispatch(in.PriorityDispatch).SetPrepayBonusBps(in.PrepayBonusBps).
		SetSortOrder(in.SortOrder).SetStatus(esportsmemberlevelrule.StatusEnabled).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	return item, nil
}

// updateMemberLevel 条件更新等级配置（version 乐观锁）；code 不可改。
// 改门槛不影响历史成长值流水，只影响下次重算（§10.5 以流水为准）。
func updateMemberLevel(ctx context.Context, db *ent.Client, id, expectedVersion int, in memberLevelInput) (*ent.EsportsMemberLevelRule, error) {
	if id < 1 || expectedVersion < 1 {
		return nil, errInvalidInput
	}
	if err := validateMemberLevelInput(in); err != nil {
		return nil, err
	}
	changed, err := db.EsportsMemberLevelRule.Update().Where(
		esportsmemberlevelrule.IDEQ(id),
		esportsmemberlevelrule.VersionEQ(expectedVersion),
	).SetName(strings.TrimSpace(in.Name)).
		SetGrowthThreshold(in.GrowthThreshold).SetDiscountBps(in.DiscountBps).
		SetPriorityDispatch(in.PriorityDispatch).SetPrepayBonusBps(in.PrepayBonusBps).
		SetSortOrder(in.SortOrder).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsMemberLevelRule.Get(ctx, id)
}

// setMemberLevelStatus 启停等级配置。
func setMemberLevelStatus(ctx context.Context, db *ent.Client, id, expectedVersion int, status string) (*ent.EsportsMemberLevelRule, error) {
	if id < 1 || expectedVersion < 1 {
		return nil, errInvalidInput
	}
	switch status {
	case "enabled", "disabled":
	default:
		return nil, errInvalidInput
	}
	changed, err := db.EsportsMemberLevelRule.Update().Where(
		esportsmemberlevelrule.IDEQ(id),
		esportsmemberlevelrule.VersionEQ(expectedVersion),
	).SetStatus(esportsmemberlevelrule.Status(status)).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsMemberLevelRule.Get(ctx, id)
}

// listMemberLevels 按门槛从低到高列出全部等级配置（含停用）。
func listMemberLevels(ctx context.Context, db *ent.Client, limit int) ([]*ent.EsportsMemberLevelRule, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	return db.EsportsMemberLevelRule.Query().
		Order(ent.Asc(esportsmemberlevelrule.FieldGrowthThreshold), ent.Asc(esportsmemberlevelrule.FieldSortOrder)).
		Limit(limit).All(ctx)
}

// memberRuleByCode 按 code 查等级配置（不论启停）；不存在返回 nil。
func memberRuleByCode(ctx context.Context, db *ent.Client, code string) (*ent.EsportsMemberLevelRule, error) {
	if strings.TrimSpace(code) == "" {
		return nil, nil
	}
	item, err := db.EsportsMemberLevelRule.Query().Where(esportsmemberlevelrule.CodeEQ(code)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return item, nil
}

// memberLevelForGrowth 取成长值达到的最高一档启用等级；无任何等级达标返回 nil。
func memberLevelForGrowth(ctx context.Context, db *ent.Client, growth int64) (*ent.EsportsMemberLevelRule, error) {
	item, err := db.EsportsMemberLevelRule.Query().Where(
		esportsmemberlevelrule.StatusEQ(esportsmemberlevelrule.StatusEnabled),
		esportsmemberlevelrule.GrowthThresholdLTE(growth),
	).Order(ent.Desc(esportsmemberlevelrule.FieldGrowthThreshold), ent.Desc(esportsmemberlevelrule.FieldSortOrder)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return item, nil
}

// memberDiscountBps 返回客户当前等级对应的折扣基点；未定级或等级已停用时返回 0 表示按原价。
func memberDiscountBps(ctx context.Context, db *ent.Client, levelCode string) (int, error) {
	rule, err := memberRuleByCode(ctx, db, levelCode)
	if err != nil || rule == nil {
		return 0, err
	}
	if rule.Status != esportsmemberlevelrule.StatusEnabled {
		return 0, nil
	}
	return rule.DiscountBps, nil
}

// memberDiscountCents 按折扣基点算折扣额（分）。折后应付 = 规则价 * bps / 10000，向下取整。
// 用 (q*bps + r*bps/10000) 分解求商，避免 subtotal*bps 整数溢出。
// 纯函数，便于测试。
func memberDiscountCents(subtotal int64, bps int) int64 {
	if subtotal <= 0 || bps <= 0 || bps >= memberDiscountBpsFull {
		return 0
	}
	quotient := subtotal/memberDiscountBpsFull*int64(bps) + subtotal%memberDiscountBpsFull*int64(bps)/memberDiscountBpsFull
	return subtotal - quotient
}

// customerGrowthTotal 客户累计成长值 = 成长值流水的求和（成长值不落库）。
func customerGrowthTotal(ctx context.Context, db *ent.Client, customerID int) (int64, error) {
	var rows []struct {
		Sum int64 `json:"sum"`
	}
	if err := db.EsportsGrowthLedger.Query().
		Where(esportsgrowthledger.CustomerIDEQ(customerID)).
		Aggregate(ent.Sum(esportsgrowthledger.FieldDelta)).Scan(ctx, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Sum, nil
}

// accrueOrderGrowth 结单时按订单实付金额计提成长值（§10.2）。同一订单只计提一次，
// 重复计提靠 (source_type, source_id) 唯一索引幂等；计提后重算客户等级。
func accrueOrderGrowth(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, now time.Time) error {
	if order.PayableCents <= 0 {
		return nil
	}
	_, err := tx.EsportsGrowthLedger.Create().
		SetCustomerID(order.CustomerID).SetDelta(order.PayableCents).
		SetSourceType(esportsgrowthledger.SourceTypeOrder).
		SetSourceID(order.OrderNo).SetNote("订单结单计提").Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil // 已计提过（重放），不再重算
		}
		return err
	}
	return recomputeCustomerLevel(ctx, tx.Client(), order.CustomerID, now)
}

// reverseOrderGrowth 退款执行后按退款金额冲回成长值（§10.2/§10.5）。
// 订单尚未计提成长值（未结单即退款）时跳过；幂等靠售后单号唯一索引。
func reverseOrderGrowth(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, item *ent.EsportsAfterSale, adminID int, now time.Time) error {
	if item.RefundCents <= 0 {
		return nil
	}
	accrued, err := tx.EsportsGrowthLedger.Query().Where(
		esportsgrowthledger.SourceTypeEQ(esportsgrowthledger.SourceTypeOrder),
		esportsgrowthledger.SourceIDEQ(order.OrderNo),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if !accrued {
		return nil
	}
	_, err = tx.EsportsGrowthLedger.Create().
		SetCustomerID(order.CustomerID).SetDelta(-item.RefundCents).
		SetSourceType(esportsgrowthledger.SourceTypeAfterSale).
		SetSourceID(item.AfterSaleNo).SetOperatorID(adminID).
		SetNote("售后退款冲回成长值").Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return errConflict
		}
		return err
	}
	return recomputeCustomerLevel(ctx, tx.Client(), order.CustomerID, now)
}

// recomputeCustomerLevel 按成长值流水重算客户等级（§10.3）：
//   - 未定级 → 直接定级；成长值跨过更高门槛 → 即时升级并写通知事件；
//   - 成长值跌破当前等级门槛 → 只记录应降等级与保护期到期时间，保护期满由定时任务降级；
//   - 成长值回升到当前等级 → 撤销待降级标记。
func recomputeCustomerLevel(ctx context.Context, db *ent.Client, customerID int, now time.Time) error {
	growth, err := customerGrowthTotal(ctx, db, customerID)
	if err != nil {
		return err
	}
	target, err := memberLevelForGrowth(ctx, db, growth)
	if err != nil {
		return err
	}
	if target == nil {
		return nil // 无可用等级配置：不动等级
	}
	customer, err := db.EsportsCustomer.Get(ctx, customerID)
	if err != nil {
		return err
	}
	current := customer.MemberLevelCode
	if current == "" {
		return applyMemberLevel(ctx, db, customer, target.Code, now, "member.level_assigned", growth)
	}
	if current == target.Code {
		if customer.MemberPendingLevelCode == "" {
			return nil
		}
		// 成长值回升到当前等级，撤销待降级。
		_, err = db.EsportsCustomer.Update().Where(
			esportscustomer.IDEQ(customerID), esportscustomer.VersionEQ(customer.Version),
		).SetMemberPendingLevelCode("").ClearMemberDowngradeAfter().AddVersion(1).Save(ctx)
		return err
	}
	currentRule, err := memberRuleByCode(ctx, db, current)
	if err != nil {
		return err
	}
	if currentRule == nil || target.GrowthThreshold > currentRule.GrowthThreshold {
		return applyMemberLevel(ctx, db, customer, target.Code, now, "member.level_upgraded", growth)
	}
	// 降级：挂起，等保护期到期由定时任务执行。
	guardDays, err := memberDowngradeGuardDays(ctx, db)
	if err != nil {
		return err
	}
	changed, err := db.EsportsCustomer.Update().Where(
		esportscustomer.IDEQ(customerID), esportscustomer.VersionEQ(customer.Version),
	).SetMemberPendingLevelCode(target.Code).
		SetMemberDowngradeAfter(now.UTC().AddDate(0, 0, guardDays)).
		AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	_, err = db.EsportsOutbox.Create().
		SetEventKey(fmt.Sprintf("member.level_downgrade_pending:%d:%s:%d", customerID, target.Code, now.Unix())).
		SetEventType("member.level_downgrade_pending").
		SetAggregateRef(fmt.Sprintf("customer:%d", customerID)).
		SetPayload(map[string]interface{}{
			"customer_id": customerID, "pending_level_code": target.Code,
			"current_level_code": current, "growth": growth,
		}).Save(ctx)
	if err != nil && !ent.IsConstraintError(err) {
		return err
	}
	return nil
}

// applyMemberLevel 把等级落为当前生效值（清挂起标记）并写等级变更事件（通知客户与对接客服）。
func applyMemberLevel(ctx context.Context, db *ent.Client, customer *ent.EsportsCustomer, levelCode string, now time.Time, eventType string, growth int64) error {
	changed, err := db.EsportsCustomer.Update().Where(
		esportscustomer.IDEQ(customer.ID), esportscustomer.VersionEQ(customer.Version),
	).SetMemberLevelCode(levelCode).SetMemberPendingLevelCode("").ClearMemberDowngradeAfter().
		SetMemberLevelChangedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	_, err = db.EsportsOutbox.Create().
		SetEventKey(fmt.Sprintf("%s:%d:%s:%d", eventType, customer.ID, levelCode, now.Unix())).
		SetEventType(eventType).SetAggregateRef(fmt.Sprintf("customer:%d", customer.ID)).
		SetPayload(map[string]interface{}{
			"customer_id": customer.ID, "level_code": levelCode,
			"previous_level_code": customer.MemberLevelCode, "growth": growth,
		}).Save(ctx)
	if err != nil && !ent.IsConstraintError(err) {
		return err
	}
	return nil
}

// memberDowngradeGuardDays 读等级保护期天数（§10.3）；未配置或非法时回落默认 30 天。
func memberDowngradeGuardDays(ctx context.Context, db *ent.Client) (int, error) {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(memberDowngradeGuardKey)).Only(ctx)
	if ent.IsNotFound(err) {
		return defaultMemberDowngradeGuardDays, nil
	}
	if err != nil {
		return 0, err
	}
	days, err := strconv.Atoi(item.Value)
	if err != nil || days < 1 || days > 3650 {
		return defaultMemberDowngradeGuardDays, nil
	}
	return days, nil
}

// applyMemberDowngrades 批量执行保护期已到期的待降级客户（§10.3），逐个条件更新，版本竞争跳过。
// ponytail: 单批全量扫描待降级客户，未做分页游标；待降级量级在会员体系下很小。
func applyMemberDowngrades(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errInvalidInput
	}
	rows, err := db.EsportsCustomer.Query().Where(
		esportscustomer.MemberPendingLevelCodeNEQ(""),
		esportscustomer.MemberDowngradeAfterLTE(now.UTC()),
	).Order(ent.Asc(esportscustomer.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, customer := range rows {
		changed, err := db.EsportsCustomer.Update().Where(
			esportscustomer.IDEQ(customer.ID),
			esportscustomer.VersionEQ(customer.Version),
			esportscustomer.MemberPendingLevelCodeEQ(customer.MemberPendingLevelCode),
		).SetMemberLevelCode(customer.MemberPendingLevelCode).
			SetMemberPendingLevelCode("").ClearMemberDowngradeAfter().
			SetMemberLevelChangedAt(now.UTC()).AddVersion(1).Save(ctx)
		if err != nil {
			return count, err
		}
		if changed == 1 {
			count++
		}
	}
	return count, nil
}

// setMemberLevelByAdmin 管理员手动调级（§10.3）：写只增覆盖记录（操作人/原因/时间）并立即改当前等级，
// 不修改成长值流水。
// ponytail: 手动调级不设永久锁定，后续成长值变动仍按门槛重算（更高即时升级，更低走保护期降级）。
func setMemberLevelByAdmin(ctx context.Context, db *ent.Client, customerID int, levelCode, reason string, operatorID int, now time.Time) (*ent.EsportsCustomer, error) {
	levelCode = strings.TrimSpace(levelCode)
	reason = strings.TrimSpace(reason)
	if customerID < 1 || operatorID < 1 || reason == "" || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	rule, err := memberRuleByCode(ctx, db, levelCode)
	if err != nil {
		return nil, err
	}
	if rule == nil || rule.Status != esportsmemberlevelrule.StatusEnabled {
		return nil, fmt.Errorf("%w：目标会员等级不存在或已停用", errInvalidInput)
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	customer, err := tx.EsportsCustomer.Get(ctx, customerID)
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsMemberLevelOverride.Create().
		SetCustomerID(customerID).SetLevelCode(levelCode).
		SetReason(reason).SetOperatorID(operatorID).Save(ctx); err != nil {
		return nil, err
	}
	changed, err := tx.EsportsCustomer.Update().Where(
		esportscustomer.IDEQ(customerID), esportscustomer.VersionEQ(customer.Version),
	).SetMemberLevelCode(levelCode).SetMemberPendingLevelCode("").ClearMemberDowngradeAfter().
		SetMemberLevelChangedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey(fmt.Sprintf("member.level_manual:%d:%s:%d", customerID, levelCode, now.Unix())).
		SetEventType("member.level_manual").SetAggregateRef(fmt.Sprintf("customer:%d", customerID)).
		SetPayload(map[string]interface{}{
			"customer_id": customerID, "level_code": levelCode, "reason": reason,
			"operator_id": operatorID, "previous_level_code": customer.MemberLevelCode,
		}).Save(ctx); err != nil && !ent.IsConstraintError(err) {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCustomer.Get(ctx, customerID)
}

// memberView 是客户会员详情：等级、成长值、待降级信息与覆盖记录。
type memberView struct {
	CustomerID       int                               `json:"customer_id"`
	LevelCode        string                            `json:"level_code"`
	LevelName        string                            `json:"level_name"`
	DiscountBps      int                               `json:"discount_bps"`
	GrowthTotal      int64                             `json:"growth_total"`
	PendingLevelCode string                            `json:"pending_level_code"`
	DowngradeAfter   *time.Time                        `json:"downgrade_after"`
	LevelChangedAt   *time.Time                        `json:"level_changed_at"`
	Ledger           []*ent.EsportsGrowthLedger        `json:"ledger"`
	Overrides        []*ent.EsportsMemberLevelOverride `json:"overrides"`
}

// buildMemberView 组装客户会员详情：等级与折扣来自配置表，成长值由流水求和。
func buildMemberView(ctx context.Context, db *ent.Client, customerID int, limit int) (*memberView, error) {
	if customerID < 1 {
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	customer, err := db.EsportsCustomer.Get(ctx, customerID)
	if err != nil {
		return nil, err
	}
	growth, err := customerGrowthTotal(ctx, db, customerID)
	if err != nil {
		return nil, err
	}
	view := &memberView{
		CustomerID: customer.ID, LevelCode: customer.MemberLevelCode,
		GrowthTotal: growth, PendingLevelCode: customer.MemberPendingLevelCode,
		DowngradeAfter: customer.MemberDowngradeAfter, LevelChangedAt: customer.MemberLevelChangedAt,
	}
	if rule, err := memberRuleByCode(ctx, db, customer.MemberLevelCode); err != nil {
		return nil, err
	} else if rule != nil {
		view.LevelName = rule.Name
		view.DiscountBps = rule.DiscountBps
	}
	view.Ledger, err = db.EsportsGrowthLedger.Query().
		Where(esportsgrowthledger.CustomerIDEQ(customerID)).
		Order(ent.Desc(esportsgrowthledger.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	view.Overrides, err = db.EsportsMemberLevelOverride.Query().
		Where(esportsmemberleveloverride.CustomerIDEQ(customerID)).
		Order(ent.Desc(esportsmemberleveloverride.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	return view, nil
}

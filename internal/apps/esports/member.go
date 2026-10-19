package esports

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsmemberleveloverride"
	"go-admin/internal/ent/esportsmemberlevelrule"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

// 会员等级（§10）相关常量。
const (
	// 等级保护期天数系统配置键；未配置时用默认值（见 memberDowngradeGuardDays）。
	memberDowngradeGuardKey = "app.esports.member_downgrade_guard_days"
	// 默认等级保护期：成长值跌破门槛后 30 天内不降级。
	defaultMemberDowngradeGuardDays = 30
	// 折扣基点满值：10000 = 不打折。
	memberDiscountBpsFull = 10000
	// 手动调级覆盖期天数系统配置键（§10.3-03 / §10.5-04）；未配置时用默认值。
	memberOverrideGuardKey = "app.esports.member_override_guard_days"
	// 默认手动调级覆盖期：90 天内手动调级等级不被成长值重算侵蚀。
	defaultMemberOverrideGuardDays = 90
	// 手动调级逐次指定覆盖期时的天数上下限（与系统配置允许范围一致）。
	minMemberOverrideDays = 1
	maxMemberOverrideDays = 3650
	// 手动调级等级变更事件类型：覆盖期是「那一次调级」的属性，随只增事件载荷快照落库，
	// 不引入新字段也不改写只增的覆盖记录（见 memberOverrideSnapshotExpiry）。
	memberManualLevelEventType = "member.level_manual"
	// 订单成长值按本金口径计提时的备注标记（§10.2-01）。成长值流水只增不改、schema 无支付来源字段，
	// 故用备注标记计提口径：冲回时据此判断该笔计提是否已剔除赠送权益，历史（全额口径）流水仍按原口径冲回。
	orderGrowthPrincipalNote = "订单结单计提（本金口径）"
)

var memberLevelCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// memberLevelInput 是等级配置的可编辑字段。code 创建后不可变。
// DedicatedCs / PriorityBooking 用指针区分「未传」与「显式 false」：
// 编辑接口未传时保持原值，避免前端旧表单把已有权益误清零。
type memberLevelInput struct {
	Code             string `json:"code"`
	Name             string `json:"name"`
	GrowthThreshold  int64  `json:"growth_threshold"`
	DiscountBps      int    `json:"discount_bps"`
	PriorityDispatch bool   `json:"priority_dispatch"`
	PrepayBonusBps   int    `json:"prepay_bonus_bps"`
	DedicatedCs      *bool  `json:"dedicated_cs"`
	PriorityBooking  *bool  `json:"priority_booking"`
	// 专属通道 / 线下活动优先（§10.1-02）：与 DedicatedCs / PriorityBooking 同一「未传不清零」口径。
	DedicatedChannel     *bool `json:"dedicated_channel"`
	OfflineEventPriority *bool `json:"offline_event_priority"`
	SortOrder            int   `json:"sort_order"`
}

// memberLevelFlag 把可选布尔开关解引用：未传按 false 处理（新建默认关闭）。
func memberLevelFlag(value *bool) bool {
	return value != nil && *value
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
		SetDedicatedCs(memberLevelFlag(in.DedicatedCs)).
		SetPriorityBooking(memberLevelFlag(in.PriorityBooking)).
		SetDedicatedChannel(memberLevelFlag(in.DedicatedChannel)).
		SetOfflineEventPriority(memberLevelFlag(in.OfflineEventPriority)).
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
// DedicatedCs / PriorityBooking 仅在请求显式携带时更新，未传保持库中原值。
// 改门槛不影响历史成长值流水，只影响下次重算（§10.5 以流水为准）。
func updateMemberLevel(ctx context.Context, db *ent.Client, id, expectedVersion int, in memberLevelInput) (*ent.EsportsMemberLevelRule, error) {
	if id < 1 || expectedVersion < 1 {
		return nil, errInvalidInput
	}
	if err := validateMemberLevelInput(in); err != nil {
		return nil, err
	}
	update := db.EsportsMemberLevelRule.Update().Where(
		esportsmemberlevelrule.IDEQ(id),
		esportsmemberlevelrule.VersionEQ(expectedVersion),
	).SetName(strings.TrimSpace(in.Name)).
		SetGrowthThreshold(in.GrowthThreshold).SetDiscountBps(in.DiscountBps).
		SetPriorityDispatch(in.PriorityDispatch).SetPrepayBonusBps(in.PrepayBonusBps).
		SetSortOrder(in.SortOrder)
	if in.DedicatedCs != nil {
		update.SetDedicatedCs(*in.DedicatedCs)
	}
	if in.PriorityBooking != nil {
		update.SetPriorityBooking(*in.PriorityBooking)
	}
	if in.DedicatedChannel != nil {
		update.SetDedicatedChannel(*in.DedicatedChannel)
	}
	if in.OfflineEventPriority != nil {
		update.SetOfflineEventPriority(*in.OfflineEventPriority)
	}
	changed, err := update.AddVersion(1).Save(ctx)
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

// deleteMemberLevel 删除会员等级配置（审核报告 §8.1-8）：只有「已停用且未被任何客户引用」的等级才允许删除。
// 启用中的等级直接拒绝（引导先停用）；仍有客户把该 code 作为当前等级 / 待生效等级时同样拒绝——
// 删掉会让客户等级与历史覆盖记录悬空，停用是可逆口径，删除只用于清理从未被使用的脏配置。
func deleteMemberLevel(ctx context.Context, db *ent.Client, id int) error {
	if id < 1 {
		return errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	item, err := tx.EsportsMemberLevelRule.Get(ctx, id)
	if err != nil {
		return err
	}
	if item.Status != esportsmemberlevelrule.StatusDisabled {
		return fmt.Errorf("%w：该会员等级启用中，不能删除，请先停用", errConflict)
	}
	referenced, err := tx.EsportsCustomer.Query().Where(
		esportscustomer.Or(
			esportscustomer.MemberLevelCodeEQ(item.Code),
			esportscustomer.MemberPendingLevelCodeEQ(item.Code),
		),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if referenced {
		return fmt.Errorf("%w：该会员等级仍被客户引用，不能删除，请改用停用", errConflict)
	}
	deleted, err := tx.EsportsMemberLevelRule.Delete().Where(
		esportsmemberlevelrule.IDEQ(id),
		esportsmemberlevelrule.StatusEQ(esportsmemberlevelrule.StatusDisabled),
	).Exec(ctx)
	if err != nil {
		return err
	}
	if deleted != 1 {
		return errConflict
	}
	return tx.Commit()
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

// customerMemberRule 返回客户当前等级对应的**启用**规则（§10.4 权益判定口径）；
// 未定级、规则不存在或已停用时返回 nil，表示不享受该等级任何权益。
func customerMemberRule(ctx context.Context, db *ent.Client, levelCode string) (*ent.EsportsMemberLevelRule, error) {
	rule, err := memberRuleByCode(ctx, db, levelCode)
	if err != nil || rule == nil {
		return nil, err
	}
	if rule.Status != esportsmemberlevelrule.StatusEnabled {
		return nil, nil
	}
	return rule, nil
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

// memberPrepayBonusCents 按会员等级的储值卡赠送比例（§10.4）算开卡赠送额（分）；
// 未定级、等级停用或比例为 0 时返回 0。整数向下取整，按 (q*bps + r*bps/10000) 分解避免溢出。
func memberPrepayBonusCents(ctx context.Context, db *ent.Client, levelCode string, principalCents int64) (int64, error) {
	rule, err := memberRuleByCode(ctx, db, levelCode)
	if err != nil || rule == nil {
		return 0, err
	}
	if rule.Status != esportsmemberlevelrule.StatusEnabled || rule.PrepayBonusBps <= 0 || principalCents <= 0 {
		return 0, nil
	}
	bps := int64(rule.PrepayBonusBps)
	return principalCents/memberDiscountBpsFull*bps + principalCents%memberDiscountBpsFull*bps/memberDiscountBpsFull, nil
}

// memberDedicatedChannel 返回客户当前等级是否开通「专属通道」权益（§10.1-02）。
// 未定级、等级不存在或已停用一律返回 false（不享受任何权益，与 customerMemberRule 同口径）。
func memberDedicatedChannel(ctx context.Context, db *ent.Client, levelCode string) (bool, error) {
	rule, err := customerMemberRule(ctx, db, levelCode)
	if err != nil || rule == nil {
		return false, err
	}
	return rule.DedicatedChannel, nil
}

// memberOfflineEventPriority 返回客户当前等级是否开通「线下活动优先」权益（§10.1-02）。
// 未定级、等级不存在或已停用一律返回 false。
func memberOfflineEventPriority(ctx context.Context, db *ent.Client, levelCode string) (bool, error) {
	rule, err := customerMemberRule(ctx, db, levelCode)
	if err != nil || rule == nil {
		return false, err
	}
	return rule.OfflineEventPriority, nil
}

// memberPrivilegeView 是客户当前生效的会员权益快照（§10.1-02 / §10.4）。
// Active=false 表示未定级 / 等级停用 / 规则缺失，此时不得在权益页宣称「已生效」。
type memberPrivilegeView struct {
	CustomerID           int    `json:"customer_id"`
	LevelCode            string `json:"level_code"`
	LevelName            string `json:"level_name"`
	Active               bool   `json:"active"`
	DiscountBps          int    `json:"discount_bps"`
	PrepayBonusBps       int    `json:"prepay_bonus_bps"`
	DedicatedCs          bool   `json:"dedicated_cs"`
	DedicatedChannel     bool   `json:"dedicated_channel"`
	OfflineEventPriority bool   `json:"offline_event_priority"`
	PriorityBooking      bool   `json:"priority_booking"`
	PriorityDispatch     bool   `json:"priority_dispatch"`
}

// memberPrivilegeFlags 一次性读取客户全部会员权益字段（P1-11：权益必须有真实消费点）。
// 口径与 customerMemberRule 一致：等级须存在且 enabled，否则返回 Active=false 的空权益。
//
// 已接入的消费点：客服工作台客户卡片 / 客户档案（cs.go customerProfileOverview）、
// 会员详情视图（memberView），以及优先预约提交（priority_booking.go 读 PriorityBooking）。
// 仍待外部文件接入（本文件不可改，见整改报告）：
//   - 下单 / 报价：service.go placeOrder / 报价路径除会员折扣外，应读 DedicatedChannel 写入订单派单优先级标记；
//   - 派单：dispatch.go prioritizeMemberOrders 现读 PriorityDispatch，可叠加 DedicatedChannel 作二级置顶。
func memberPrivilegeFlags(ctx context.Context, db *ent.Client, customerID int) (*memberPrivilegeView, error) {
	if customerID < 1 {
		return nil, errInvalidInput
	}
	customer, err := db.EsportsCustomer.Get(ctx, customerID)
	if err != nil {
		return nil, err
	}
	view := &memberPrivilegeView{CustomerID: customer.ID, LevelCode: customer.MemberLevelCode}
	rule, err := customerMemberRule(ctx, db, customer.MemberLevelCode)
	if err != nil {
		return nil, err
	}
	if rule == nil {
		return view, nil
	}
	view.LevelName = rule.Name
	view.Active = true
	view.DiscountBps = rule.DiscountBps
	view.PrepayBonusBps = rule.PrepayBonusBps
	view.DedicatedCs = rule.DedicatedCs
	view.DedicatedChannel = rule.DedicatedChannel
	view.OfflineEventPriority = rule.OfflineEventPriority
	view.PriorityBooking = rule.PriorityBooking
	view.PriorityDispatch = rule.PriorityDispatch
	return view, nil
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

// orderSettlementComposition 返回订单核销资金的本金 / 赠送构成（分，§10.2-01「核销明细本金口径」）：
//   - 有核销流水（余额 / 储值卡核销）时取流水的实际本金 / 赠送列，这正是「赠送权益支付部分」的凭据；
//   - 无核销流水（现金渠道、线下核销、历史补录单）时全额按本金计，与既有「按实付额计提」口径一致。
//
// 返回的 evidence 表示是否有核销流水可证。
func orderSettlementComposition(ctx context.Context, db *ent.Client, order *ent.EsportsOrder) (principal, bonus int64, evidence bool, err error) {
	rows, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.SourceTypeEQ("order"),
		esportswallettx.SourceIDEQ(order.OrderNo),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeConsume),
	).All(ctx)
	if err != nil {
		return 0, 0, false, err
	}
	for _, row := range rows {
		principal += -row.PrincipalDeltaCents
		bonus += -row.BonusDeltaCents
	}
	if principal <= 0 && bonus <= 0 {
		return order.PayableCents, 0, false, nil
	}
	return principal, bonus, true, nil
}

// principalShareOfRefund 按原核销构成算「累计已退 refundedCents 中的本金份额」（分）：
// 与退款落账（refundComposition）同一分摊口径——本金 × 累计已退 / 原核销总额，封顶本金。
func principalShareOfRefund(principal, bonus, refundedCents int64) int64 {
	composition := principal + bonus
	if composition <= 0 || refundedCents <= 0 {
		return 0
	}
	return min(principal*refundedCents/composition, principal)
}

// accrueOrderGrowth 结单时按**核销明细的本金**计提成长值（§10.2-01）：现金实付与已核销预付本金计入，
// 充值赠送权益支付的部分不计（赠送不直接增加成长值）；结单前已发生的退款按本金份额扣除，
// 避免「先退款、后结单」时按全额补计。同一订单只计提一次，
// 重复计提靠 (source_type, source_id) 唯一索引幂等；计提后重算客户等级。
// 流水同时落 payment_source（§10.2-03），便于事后追溯这笔成长值来自现金还是已核销预付本金。
func accrueOrderGrowth(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, now time.Time) error {
	principal, bonus, evidence, err := orderSettlementComposition(ctx, tx.Client(), order)
	if err != nil {
		return err
	}
	net := principal - principalShareOfRefund(principal, bonus, order.RefundedCents)
	if net <= 0 {
		return nil
	}
	source := esportsgrowthledger.PaymentSourceCash
	if evidence {
		switch {
		case principal <= 0:
			source = esportsgrowthledger.PaymentSourceBonusGrant
		case bonus <= 0:
			source = esportsgrowthledger.PaymentSourcePrepaidPrincipal
		default:
			source = esportsgrowthledger.PaymentSourceMixed
		}
	}
	_, err = tx.EsportsGrowthLedger.Create().
		SetCustomerID(order.CustomerID).SetDelta(net).
		SetSourceType(esportsgrowthledger.SourceTypeOrder).
		SetSourceID(order.OrderNo).SetPaymentSource(source).
		SetNote("订单结单计提").Save(ctx)
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
	rev := refundReversalContext{
		RefundCents:  item.RefundCents,
		RefKey:       item.AfterSaleNo,
		AfterSaleID:  &item.ID,
		PlayerID:     item.PlayerID,
		ActorAdminID: adminID,
	}
	return reverseOrderGrowthFor(ctx, tx, order, rev, now)
}

// reverseOrderGrowthFor 成长值冲回实现（售后 / 外部退款共用）。
// 只冲回「退款对应的本金部分」（§10.2-01「退款只冲回对应部分」）：订单按本金口径计提时，
// 按原核销构成算本次退还的本金份额；升级前的历史流水（payment_source = none，全额口径）
// 沿用「按退款额冲回」，两种口径都不超过该订单已计提的成长值。
// 成长值流水 source_type 枚举没有 external_refund 分支，外部退款沿用 after_sale 枚举、
// SourceID 写退款单号（ER...）；
// 该流水语义为「退款冲回」，ID 为退款单号（售后冲回则为售后单号）。
// (source_type, source_id) 唯一索引同时承担幂等职责。
func reverseOrderGrowthFor(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder,
	rev refundReversalContext, now time.Time) error {
	if rev.RefundCents <= 0 {
		return nil
	}
	orderRows, err := tx.EsportsGrowthLedger.Query().Where(
		esportsgrowthledger.SourceTypeEQ(esportsgrowthledger.SourceTypeOrder),
		esportsgrowthledger.SourceIDEQ(order.OrderNo),
	).All(ctx)
	if err != nil {
		return err
	}
	if len(orderRows) == 0 {
		return nil // 未计提（未结单即退款 / 全部用赠送权益支付）
	}
	reversal, err := growthReversalCents(ctx, tx, order, rev.RefundCents, orderRows)
	if err != nil {
		return err
	}
	if reversal <= 0 {
		return nil
	}
	note := "售后退款冲回成长值"
	if rev.Note != "" {
		note = rev.Note + "成长值"
	}
	create := tx.EsportsGrowthLedger.Create().
		SetCustomerID(order.CustomerID).SetDelta(-reversal).
		SetSourceType(esportsgrowthledger.SourceTypeAfterSale).
		SetSourceID(rev.RefKey).SetPaymentSource(growthReversalPaymentSource(orderRows)).
		SetNote(note)
	if rev.ActorAdminID > 0 {
		create.SetOperatorID(rev.ActorAdminID)
	}
	if _, err = create.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errConflict
		}
		return err
	}
	return recomputeCustomerLevel(ctx, tx.Client(), order.CustomerID, now)
}

// growthReversalPaymentSource 推断成长值冲回流水的支付来源（§10.2-03）：
// 冲回是「按原计提口径退回」，故沿用该订单计提流水的支付来源；历史全额口径流水（none）
// 无法自证来源，按「已核销预付本金」标注（既有的全额口径即按实付额计提）。
func growthReversalPaymentSource(orderRows []*ent.EsportsGrowthLedger) esportsgrowthledger.PaymentSource {
	for _, row := range orderRows {
		if row.PaymentSource != esportsgrowthledger.PaymentSourceNone {
			return row.PaymentSource
		}
	}
	return esportsgrowthledger.PaymentSourcePrepaidPrincipal
}

// growthReversalCents 计算本次退款应冲回的成长值（分）：
//   - 订单按本金口径计提（payment_source != none）时，按原核销构成算本次退还的本金份额
//     （与退款落账同一分摊口径），多次退款累计恰好等于已计提本金，退满即归零；
//   - 历史全额口径流水（payment_source = none）沿用「按退款额冲回」，仅以已计提额为上限。
//
// 两种口径都不超过该订单已计提的成长值，保证退款不会把成长值冲成负数。
func growthReversalCents(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder,
	refundCents int64, orderRows []*ent.EsportsGrowthLedger) (int64, error) {
	var accrued int64
	principalBased := false
	for _, row := range orderRows {
		accrued += row.Delta
		if row.PaymentSource != esportsgrowthledger.PaymentSourceNone {
			principalBased = true
		}
	}
	if accrued <= 0 {
		return 0, nil
	}
	reversal := refundCents
	if principalBased {
		principal, bonus, _, err := orderSettlementComposition(ctx, tx.Client(), order)
		if err != nil {
			return 0, err
		}
		if principal+bonus > 0 && order.PayableCents > 0 {
			refundedBefore := max(order.RefundedCents-refundCents, 0)
			if principalBack, _, err := refundComposition(principal, bonus, order.PayableCents,
				refundedBefore, refundCents); err == nil {
				reversal = principalBack
			} else {
				// 数据口径异常（应付额与核销构成不符）时退回保守口径，不让成长值阻断退款落账。
				reversal = min(refundCents, accrued)
			}
		}
	}
	return min(max(reversal, 0), accrued), nil
}

// recomputeCustomerLevel 按成长值流水重算客户等级（§10.3）：
//   - 未定级 → 直接定级；成长值跨过更高门槛 → 即时升级并写通知事件；
//   - 成长值跌破当前等级门槛 → 只记录应降等级与保护期到期时间，保护期满由定时任务降级；
//   - 成长值回升到当前等级 → 撤销待降级标记；
//   - 手动调级覆盖期内的等级**优先于**成长值口径（§10.3-03 / §10.5-04）：人工设定可升可降，
//     重算不得覆盖人工设定；覆盖期到期后自动失效（见 settleMemberLevelOverride / applyMemberOverrideExpiries），
//     等级回落为成长值口径结果。
func recomputeCustomerLevel(ctx context.Context, db *ent.Client, customerID int, now time.Time) error {
	growth, err := customerGrowthTotal(ctx, db, customerID)
	if err != nil {
		return err
	}
	target, err := memberLevelForGrowth(ctx, db, growth)
	if err != nil {
		return err
	}
	// 手动调级覆盖期内的等级**优先于**成长值口径（§10.3-03 / §10.5-04）。
	// P1-10：此前只在「覆盖等级门槛更高」时采用覆盖值（取最高），人工降级会被下一次重算顶回，
	// 表现为「只能升不能降」；现在覆盖期内人工设定即权威等级，可升也可降，覆盖到期才回落成长值口径。
	if overridden, err := activeMemberLevelForCustomer(ctx, db, customerID, now); err != nil {
		return err
	} else if overridden != nil {
		target = overridden
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
	downgradeAfter := now.UTC().AddDate(0, 0, guardDays)
	changed, err := db.EsportsCustomer.Update().Where(
		esportscustomer.IDEQ(customerID), esportscustomer.VersionEQ(customer.Version),
	).SetMemberPendingLevelCode(target.Code).
		SetMemberDowngradeAfter(downgradeAfter).
		AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	return writeMemberLevelEvent(ctx, db.EsportsOutbox.Create, "member.level_downgrade_pending", customerID, current, target.Code,
		growth, map[string]interface{}{"downgrade_after": downgradeAfter}, now)
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
	return writeMemberLevelEvent(ctx, db.EsportsOutbox.Create, eventType, customer.ID, customer.MemberLevelCode, levelCode, growth, nil, now)
}

// memberLevelEventKey 生成等级变更事件键（§10.5-03）：客户 + 等级 + 业务日期（UTC+8）。
// 键里不含秒，避免同一秒的两次变更被 EsportsOutbox.event_key 唯一索引吞掉；
// 同日同客户同等级的重复变更由 writeMemberLevelEvent 追加序号区分。
func memberLevelEventKey(eventType string, customerID int, levelCode string, at time.Time) string {
	return fmt.Sprintf("%s:%d:%s:%s", eventType, customerID, levelCode, at.In(cstZone).Format("20060102"))
}

// memberOutboxFactory 返回一个全新的外盒写入构造器，使等级事件既能写事务内（`tx.EsportsOutbox.Create`）
// 也能写事务外（`db.EsportsOutbox.Create`）；事件键冲突需重试，故传工厂而不是构造器本身。
type memberOutboxFactory func() *ent.EsportsOutboxCreate

// writeMemberLevelEvent 写等级变更事件（逐次留痕，§10.5-03）：等级每次变更都落一条只增事件，
// 载荷含变更前后等级、成长值与发生时刻，构成逐次变更记录。
// 事件键冲突时追加序号重试，保证同一天对同一客户的每次变更各有一条事件，不被唯一索引吞掉。
func writeMemberLevelEvent(ctx context.Context, newCreate memberOutboxFactory, eventType string, customerID int,
	previousCode, levelCode string, growth int64, extra map[string]interface{}, now time.Time) error {
	payload := map[string]interface{}{
		"customer_id": customerID, "level_code": levelCode,
		"previous_level_code": previousCode, "growth": growth,
		"occurred_at": now.UTC(),
	}
	for key, value := range extra {
		payload[key] = value
	}
	base := memberLevelEventKey(eventType, customerID, levelCode, now)
	for attempt := 1; attempt <= 8; attempt++ {
		key := base
		if attempt > 1 {
			key = fmt.Sprintf("%s#%d", base, attempt)
		}
		_, err := newCreate().
			SetEventKey(key).SetEventType(eventType).
			SetAggregateRef(fmt.Sprintf("customer:%d", customerID)).
			SetPayload(payload).Save(ctx)
		if err == nil {
			return nil
		}
		if !ent.IsConstraintError(err) {
			return err
		}
	}
	return errConflict
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

// memberOverrideGuardDays 读手动调级覆盖期天数（§10.5-04）；未配置或非法时回落默认 90 天。
func memberOverrideGuardDays(ctx context.Context, db *ent.Client) (int, error) {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(memberOverrideGuardKey)).Only(ctx)
	if ent.IsNotFound(err) {
		return defaultMemberOverrideGuardDays, nil
	}
	if err != nil {
		return 0, err
	}
	days, err := strconv.Atoi(item.Value)
	if err != nil || days < 1 || days > 3650 {
		return defaultMemberOverrideGuardDays, nil
	}
	return days, nil
}

// lastMemberLevelOverride 返回客户最近一次手动调级覆盖记录；从未调级返回 nil。
func lastMemberLevelOverride(ctx context.Context, db *ent.Client, customerID int) (*ent.EsportsMemberLevelOverride, error) {
	override, err := db.EsportsMemberLevelOverride.Query().
		Where(esportsmemberleveloverride.CustomerIDEQ(customerID)).
		Order(ent.Desc(esportsmemberleveloverride.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return override, nil
}

// memberOverrideCoverage 描述客户最近一次手动调级覆盖的生效窗口（§10.3-03 / §10.5-04）。
//   - Active：此刻是否仍在覆盖期内（覆盖期 = 覆盖记录创建时间 + 可配天数，默认 90 天）；
//   - Rule：覆盖期内且目标等级仍启用时的等级规则；覆盖期外或等级已停用/不存在时为 nil。
//
// 只读、不写库，可作为「读取时惰性判定」的单一事实来源。
type memberOverrideCoverage struct {
	Override  *ent.EsportsMemberLevelOverride
	Rule      *ent.EsportsMemberLevelRule
	ExpiresAt time.Time
	Active    bool
}

// memberOverrideWindow 是手动调级时**逐次指定**的覆盖期（§10.3-03 扩展）：
// 与系统配置 app.esports.member_override_guard_days 二选一，零值表示沿用系统配置。
type memberOverrideWindow struct {
	Days  int
	Until time.Time
}

// resolveMemberOverrideExpiry 把逐次指定的覆盖期解析为「覆盖到期时刻」。
//   - 两者都没给：返回零值，调用方沿用系统配置天数（既有口径不变）；
//   - 同时给出：errInvalidInput + 中文提示（不静默取其一）；
//   - 越界 / 非未来时刻：errInvalidInput + 中文提示。
//
// 到期时刻是覆盖窗口的唯一落库口径：覆盖记录本身仍只增不改，窗口随等级变更事件快照保存。
func resolveMemberOverrideExpiry(in memberOverrideWindow, now time.Time) (time.Time, error) {
	if in.Days == 0 && in.Until.IsZero() {
		return time.Time{}, nil
	}
	if in.Days != 0 && !in.Until.IsZero() {
		return time.Time{}, fmt.Errorf("%w：override_days 与 override_until 只能二选一", errInvalidInput)
	}
	if in.Days != 0 {
		if in.Days < minMemberOverrideDays || in.Days > maxMemberOverrideDays {
			return time.Time{}, fmt.Errorf("%w：override_days 须在 %d–%d 天之间",
				errInvalidInput, minMemberOverrideDays, maxMemberOverrideDays)
		}
		return now.UTC().AddDate(0, 0, in.Days), nil
	}
	until := in.Until.UTC()
	if !until.After(now.UTC()) {
		return time.Time{}, fmt.Errorf("%w：override_until 必须晚于当前时间", errInvalidInput)
	}
	if until.After(now.UTC().AddDate(0, 0, maxMemberOverrideDays)) {
		return time.Time{}, fmt.Errorf("%w：override_until 不得超过当前时间 %d 天",
			errInvalidInput, maxMemberOverrideDays)
	}
	return until, nil
}

// memberOverrideSnapshotExpiry 读取该客户最近一次手动调级事件里快照的覆盖到期时刻。
//
// 为什么要走事件：本轮不允许改 schema，而「覆盖期逐次可配」必须落库、且覆盖记录是只增的
// 审计记录（created_at 只能代表调级发生的真实时刻，不能用来夹带到期时间）。
// 等级变更事件本身就是这次调级的唯一留痕，窗口作为其属性随载荷保存最自然；
// 读取时以最新一次手动调级事件为准，未指定时返回零值，调用方回落系统配置天数。
func memberOverrideSnapshotExpiry(ctx context.Context, db *ent.Client, customerID int, levelCode string) (time.Time, error) {
	item, err := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ(memberManualLevelEventType),
		esportsoutbox.AggregateRefEQ(fmt.Sprintf("customer:%d", customerID)),
	).Order(ent.Desc(esportsoutbox.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	// 事件与最新覆盖记录必须是同一次调级，否则（历史数据 / 并发写入）不采用快照。
	if code, ok := item.Payload["level_code"].(string); ok && code != levelCode {
		return time.Time{}, nil
	}
	switch raw := item.Payload["override_until"].(type) {
	case string:
		until, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return time.Time{}, nil
		}
		return until.UTC(), nil
	case time.Time:
		return raw.UTC(), nil
	}
	return time.Time{}, nil
}

// memberOverrideCoverageFor 读取客户手动调级的覆盖状态；无覆盖记录时返回零值（Override 为 nil）。
// 覆盖到期时刻优先取该次调级事件里快照的 override_until（逐次指定），缺失时回落系统配置天数。
func memberOverrideCoverageFor(ctx context.Context, db *ent.Client, customerID int, now time.Time) (memberOverrideCoverage, error) {
	var coverage memberOverrideCoverage
	override, err := lastMemberLevelOverride(ctx, db, customerID)
	if err != nil || override == nil {
		return coverage, err
	}
	days, err := memberOverrideGuardDays(ctx, db)
	if err != nil {
		return coverage, err
	}
	coverage.Override = override
	coverage.ExpiresAt = override.CreatedAt.AddDate(0, 0, days)
	if snapshot, snapshotErr := memberOverrideSnapshotExpiry(ctx, db, customerID, override.LevelCode); snapshotErr != nil {
		return coverage, snapshotErr
	} else if !snapshot.IsZero() {
		coverage.ExpiresAt = snapshot
	}
	coverage.Active = now.UTC().Before(coverage.ExpiresAt)
	if !coverage.Active {
		return coverage, nil
	}
	rule, err := customerMemberRule(ctx, db, override.LevelCode)
	if err != nil {
		return coverage, err
	}
	coverage.Rule = rule
	return coverage, nil
}

// activeMemberLevelForCustomer 返回覆盖期内最近一次手动调级对应的**启用**等级规则（§10.3-03 / §10.5-04）。
// 超出覆盖期或等级已停用/不存在时返回 nil，表示不再作为等级来源，重算回到成长值口径。
func activeMemberLevelForCustomer(ctx context.Context, db *ent.Client, customerID int, now time.Time) (*ent.EsportsMemberLevelRule, error) {
	coverage, err := memberOverrideCoverageFor(ctx, db, customerID, now)
	if err != nil {
		return nil, err
	}
	return coverage.Rule, nil
}

// settleMemberLevelOverride 是覆盖到期的**读取时惰性判定**（P1-10 回落机制的一半）：
// 当客户最近一次手动调级的覆盖期已过、且当前等级仍停在这次人工设定的等级上时，
// 立刻按成长值口径重算回落（可能即时升级，或按保护期挂起降级）。
// 已由后续调级/重算改过等级、或仍在覆盖期内的情形不做任何写入。
func settleMemberLevelOverride(ctx context.Context, db *ent.Client, customerID int, now time.Time) error {
	coverage, err := memberOverrideCoverageFor(ctx, db, customerID, now)
	if err != nil || coverage.Override == nil || coverage.Active {
		return err
	}
	customer, err := db.EsportsCustomer.Get(ctx, customerID)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if customer.MemberLevelCode != coverage.Override.LevelCode {
		return nil // 覆盖到期后等级已被其它来源改写，无需回落
	}
	return recomputeCustomerLevel(ctx, db, customerID, now)
}

// applyMemberOverrideExpiries 批量执行覆盖期已到期的手动调级回落（P1-10 回落机制的另一半，
// 供定时任务调用）：逐客户条件重算，重复执行安全。
//
// 覆盖期可能被逐次指定（override_days / override_until），与系统配置天数不一致，
// 因此不能再用「created_at <= now - 配置天数」的粗口径筛人：改为取每个客户最近一条覆盖记录，
// 由 memberOverrideCoverageFor 判定是否真的到期（未到期不做任何写入）。
//
// ponytail: 会员手动调级量级很小，扫描上限取 limit*5 即可覆盖，不引入「是否已处理」的状态位。
func applyMemberOverrideExpiries(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errInvalidInput
	}
	rows, err := db.EsportsMemberLevelOverride.Query().
		Order(ent.Desc(esportsmemberleveloverride.FieldID)).
		Limit(limit * 5).All(ctx)
	if err != nil {
		return 0, err
	}
	seen := make(map[int]bool, len(rows))
	count := 0
	for _, row := range rows {
		if seen[row.CustomerID] {
			continue // 同一客户只按最新一条覆盖记录判断
		}
		seen[row.CustomerID] = true
		customer, err := db.EsportsCustomer.Get(ctx, row.CustomerID)
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			return count, err
		}
		if customer.MemberLevelCode != row.LevelCode {
			continue // 等级已被其它来源改写，无需回落
		}
		before := customer.MemberLevelCode
		if err = settleMemberLevelOverride(ctx, db, customer.ID, now); err != nil {
			return count, err
		}
		after, err := db.EsportsCustomer.Get(ctx, customer.ID)
		if err != nil {
			return count, err
		}
		if after.MemberLevelCode != before {
			count++
		}
	}
	return count, nil
}

// applyMemberOverrideExpiryTask 定时任务入口（P1-10）：把覆盖期已到期的手动调级回落为成长值口径。
//
// ⚠️ 需由 app.go 注册（本文件不持有注册权）：
//
//	tasks.Register("esports_expire_member_level_overrides", applyMemberOverrideExpiryTask)
//
// 并在 cron_seeds.go 增加 CronSeed（建议每日一次，例如 "0 20 0 * * *"）。
// 未注册时回落仍生效——读取会员详情（buildMemberView）与批量重算前都会做惰性判定。
func applyMemberOverrideExpiryTask(ctx context.Context, logger io.Writer) error {
	count, err := applyMemberOverrideExpiries(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理会员手动调级覆盖到期回落：%d\n", count)
	}
	return err
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
			// 降级执行同样逐次留事件（§10.5-03）：客户侧与对接客服都能看到何时被降级。
			if err = writeMemberLevelEvent(ctx, db.EsportsOutbox.Create, "member.level_downgraded", customer.ID,
				customer.MemberLevelCode, customer.MemberPendingLevelCode, 0,
				map[string]interface{}{"trigger": "guard_expired"}, now); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

// setMemberLevelByAdmin 管理员手动调级（§10.3）：写只增覆盖记录（操作人/原因/时间）并立即改当前等级，
// 不修改成长值流水。
// 覆盖期内该等级作为稳定的等级来源参与取最高（§10.5-04）：下一次成长值变动重算不会把它平掉，
// 覆盖期到期（默认 90 天，可配）后回到成长值口径，按保护期正常降级。
//
// windows 可选传入本次调级的覆盖期（override_days / override_until，二选一）：不传时沿用系统配置天数，
// 传入时以它为准并把到期时刻快照进等级变更事件（见 memberOverrideSnapshotExpiry）。
// 用可变参数是为了不打断既有调用点（本文件外还有多处按 7 个位置参数调用）。
func setMemberLevelByAdmin(ctx context.Context, db *ent.Client, customerID int, levelCode, reason string, operatorID int, now time.Time, windows ...memberOverrideWindow) (*ent.EsportsCustomer, error) {
	levelCode = strings.TrimSpace(levelCode)
	reason = strings.TrimSpace(reason)
	if customerID < 1 || operatorID < 1 || reason == "" || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	var window memberOverrideWindow
	if len(windows) > 0 {
		window = windows[0]
	}
	overrideUntil, err := resolveMemberOverrideExpiry(window, now)
	if err != nil {
		return nil, err
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
	override, err := tx.EsportsMemberLevelOverride.Create().
		SetCustomerID(customerID).SetLevelCode(levelCode).
		SetReason(reason).SetOperatorID(operatorID).Save(ctx)
	if err != nil {
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
	manualExtra := map[string]interface{}{"reason": reason, "operator_id": operatorID, "override_id": override.ID}
	if !overrideUntil.IsZero() {
		// 逐次指定的覆盖到期时刻：覆盖记录只增不改，窗口随事件载荷快照，供覆盖判定读取。
		manualExtra["override_until"] = overrideUntil.Format(time.RFC3339)
	}
	if err = writeMemberLevelEvent(ctx, tx.EsportsOutbox.Create, memberManualLevelEventType, customerID,
		customer.MemberLevelCode, levelCode, 0, manualExtra, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCustomer.Get(ctx, customerID)
}

// memberView 是客户会员详情：等级、成长值、待降级信息与覆盖记录。
type memberView struct {
	CustomerID       int        `json:"customer_id"`
	LevelCode        string     `json:"level_code"`
	LevelName        string     `json:"level_name"`
	DiscountBps      int        `json:"discount_bps"`
	GrowthTotal      int64      `json:"growth_total"`
	PendingLevelCode string     `json:"pending_level_code"`
	DowngradeAfter   *time.Time `json:"downgrade_after"`
	LevelChangedAt   *time.Time `json:"level_changed_at"`
	// 当前等级生效的权益快照（§10.1-02 / §10.4）：客服端与自助端据此渲染权益与入口。
	DedicatedCs          bool `json:"dedicated_cs"`
	PriorityBooking      bool `json:"priority_booking"`
	PriorityDispatch     bool `json:"priority_dispatch"`
	DedicatedChannel     bool `json:"dedicated_channel"`
	OfflineEventPriority bool `json:"offline_event_priority"`
	PrepayBonusBps       int  `json:"prepay_bonus_bps"`
	// 手动调级覆盖状态（P1-10）：覆盖期内 override_active=true，override_level_code 为人工设定等级，
	// override_expires_at 为覆盖到期时刻；到期后由惰性判定回落，override_active 变 false。
	OverrideLevelCode string                            `json:"override_level_code"`
	OverrideExpiresAt *time.Time                        `json:"override_expires_at"`
	OverrideActive    bool                              `json:"override_active"`
	Ledger            []*ent.EsportsGrowthLedger        `json:"ledger"`
	Overrides         []*ent.EsportsMemberLevelOverride `json:"overrides"`
}

// buildMemberView 组装客户会员详情：等级与折扣来自配置表，成长值由流水求和。
// 读取前先做覆盖到期的惰性判定（P1-10）：覆盖期已过的手动调级会在此刻回落，不依赖定时任务。
func buildMemberView(ctx context.Context, db *ent.Client, customerID int, limit int) (*memberView, error) {
	return memberViewAt(ctx, db, customerID, limit, time.Now())
}

// memberViewAt 是 buildMemberView 的可注入时钟版本，便于测试覆盖到期后的回落行为。
func memberViewAt(ctx context.Context, db *ent.Client, customerID int, limit int, now time.Time) (*memberView, error) {
	if customerID < 1 {
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	if err := settleMemberLevelOverride(ctx, db, customerID, now); err != nil {
		return nil, err
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
	if coverage, err := memberOverrideCoverageFor(ctx, db, customerID, now); err != nil {
		return nil, err
	} else if coverage.Override != nil {
		expiresAt := coverage.ExpiresAt
		view.OverrideLevelCode = coverage.Override.LevelCode
		view.OverrideExpiresAt = &expiresAt
		view.OverrideActive = coverage.Active
	}
	if rule, err := memberRuleByCode(ctx, db, customer.MemberLevelCode); err != nil {
		return nil, err
	} else if rule != nil {
		view.LevelName = rule.Name
		view.DiscountBps = rule.DiscountBps
	}
	// 权益快照按「启用中的等级规则」取（§10.4）：等级停用即不享受任何权益。
	if rule, err := customerMemberRule(ctx, db, customer.MemberLevelCode); err != nil {
		return nil, err
	} else if rule != nil {
		view.DedicatedCs = rule.DedicatedCs
		view.PriorityBooking = rule.PriorityBooking
		view.PriorityDispatch = rule.PriorityDispatch
		view.DedicatedChannel = rule.DedicatedChannel
		view.OfflineEventPriority = rule.OfflineEventPriority
		view.PrepayBonusBps = rule.PrepayBonusBps
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

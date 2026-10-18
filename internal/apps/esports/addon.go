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
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderaddon"
	"go-admin/internal/ent/esportswallettx"
)

// 加钟（§3.3 订单操作 / §6.2 陪玩端加钟确认 / §12.5 特殊场景）：
// 客服对服务中订单发起加钟（追加金额与工时），陪玩确认时按**确认时点锁定的分成规则**计算并保存
// 分成快照，把追加金额与追加服务量累加到订单（只增不改）；`kind=addon` 的加钟分成明细在
// **收款成功后才计提**（未收款不产生应付陪玩的负债，避免「确认即计提、未收款可结算」）。
// 加钟金额不改写原订单报价快照（`payable_cents` 保持原报价事实）。

// addonInput 加钟申请入参。
type addonInput struct {
	AddedCents int64  `json:"added_cents"`
	AddedUnits int64  `json:"added_units"`
	Note       string `json:"note"`
}

// addonNo 生成加钟单号，前缀 AD。
func addonNo() string { return "AD" + uuid.NewString() }

// validAddonStatus 校验加钟单状态。
func validAddonStatus(status string) bool {
	switch status {
	case "pending", "confirmed", "rejected", "cancelled":
		return true
	}
	return false
}

// addonCommissionCents 按规则计算本次加钟的陪玩分成（§12.5：按加钟确认时锁定的规则计算）。
// 基数取本次追加金额；fixed_per_unit 以追加服务量为单位数。
// ponytail: 加钟分成不受「分成之和 ≤ 可分基数」上限约束（加钟属追加服务，基数与原订单独立）。
func addonCommissionCents(rule *ent.EsportsCommissionRule, addedCents, addedUnits int64) int64 {
	if rule == nil {
		return 0
	}
	switch rule.CalcMode {
	case esportscommissionrule.CalcModePercent:
		return addedCents * int64(rule.ValueBp) / 10000
	case esportscommissionrule.CalcModeFixedPerOrder:
		return rule.ValueCents
	case esportscommissionrule.CalcModeFixedPerUnit:
		return rule.ValueCents * addedUnits
	default:
		return 0
	}
}

// requestOrderAddon 客服对服务中订单发起加钟：落一条 pending 加钟单等待陪玩确认。
// 约束：订单须为服务中；追加金额与追加服务量至少一项为正；同一订单同时只允许一条待确认加钟。
func requestOrderAddon(ctx context.Context, db *ent.Client, orderID, adminID int,
	in addonInput, now time.Time) (*ent.EsportsOrderAddon, error) {
	in.Note = strings.TrimSpace(in.Note)
	if orderID < 1 || adminID < 1 || len([]rune(in.Note)) > 255 ||
		in.AddedCents < 0 || in.AddedUnits < 0 || (in.AddedCents == 0 && in.AddedUnits == 0) {
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
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		return nil, fmt.Errorf("%w：仅服务中的订单可发起加钟", errInvalidInput)
	}
	pending, err := tx.EsportsOrderAddon.Query().Where(
		esportsorderaddon.OrderIDEQ(orderID),
		esportsorderaddon.StatusEQ(esportsorderaddon.StatusPending),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, fmt.Errorf("%w：该订单已有待确认的加钟，请等待陪玩确认", errConflict)
	}
	addon, err := tx.EsportsOrderAddon.Create().
		SetAddonNo(addonNo()).SetOrderID(orderID).
		SetAddedCents(in.AddedCents).SetAddedUnits(in.AddedUnits).
		SetRequestedByAdminID(adminID).SetNote(in.Note).Save(ctx)
	if err != nil {
		return nil, err
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, order.Version, "")
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.addon_requested").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"addon_no": addon.AddonNo, "added_cents": addon.AddedCents, "added_units": addon.AddedUnits,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("order.addon_requested:" + addon.AddonNo).
		SetEventType("order.addon_requested").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "addon_no": addon.AddonNo,
			"added_cents": addon.AddedCents, "added_units": addon.AddedUnits,
		}).Save(ctx); err != nil {
		if !ent.IsConstraintError(err) {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrderAddon.Get(ctx, addon.ID)
}

// listOrderAddons 某订单的加钟单列表（管理端）。
func listOrderAddons(ctx context.Context, db *ent.Client, orderID int) ([]*ent.EsportsOrderAddon, error) {
	if orderID < 1 {
		return nil, errInvalidInput
	}
	return db.EsportsOrderAddon.Query().Where(
		esportsorderaddon.OrderIDEQ(orderID),
	).Order(ent.Desc(esportsorderaddon.FieldID)).Limit(100).All(ctx)
}

// listPlayerPendingAddons 陪玩端「我的加钟通知」：本人在该订单上有 accepted 席位的待确认加钟。
func listPlayerPendingAddons(ctx context.Context, db *ent.Client, playerID int) ([]*ent.EsportsOrderAddon, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	seats, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
	).Select(esportsdispatchseat.FieldOrderID).All(ctx)
	if err != nil {
		return nil, err
	}
	orderIDs := make([]int, 0, len(seats))
	for _, seat := range seats {
		orderIDs = append(orderIDs, seat.OrderID)
	}
	if len(orderIDs) == 0 {
		return []*ent.EsportsOrderAddon{}, nil
	}
	return db.EsportsOrderAddon.Query().Where(
		esportsorderaddon.OrderIDIn(orderIDs...),
		esportsorderaddon.StatusEQ(esportsorderaddon.StatusPending),
	).Order(ent.Desc(esportsorderaddon.FieldID)).Limit(100).All(ctx)
}

// confirmOrderAddon 陪玩确认加钟（§6.2）：把追加金额与服务量累加到订单，并按确认时点锁定的
// 分成规则计算、保存本次加钟分成快照（rule_id / rule_no / rule_version / commission_cents）；
// **不在此刻计提分成**，`kind=addon` 明细等收款成功后再建（见 payOrderAddonByBalance）。
// 不改写原订单报价快照。
func confirmOrderAddon(ctx context.Context, db *ent.Client, playerID, addonID int, now time.Time) (*ent.EsportsOrderAddon, error) {
	if playerID < 1 || addonID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	addon, err := tx.EsportsOrderAddon.Get(ctx, addonID)
	if err != nil {
		return nil, err
	}
	if addon.Status != esportsorderaddon.StatusPending {
		return nil, errConflict
	}
	seatExists, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(addon.OrderID),
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !seatExists {
		return nil, errForbidden
	}
	order, err := tx.EsportsOrder.Get(ctx, addon.OrderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		return nil, errConflict
	}
	// 确认时点锁定分成规则并计算本次加钟分成（§12.5）。
	// 按确认时点的陪玩等级走 §12.3 的 level 级；档案缺失按无等级处理。
	addonPlayerLevel := ""
	if addonPlayer, pErr := tx.EsportsPlayer.Get(ctx, playerID); pErr == nil {
		addonPlayerLevel = addonPlayer.Level.String()
	}
	rule, err := resolveCommissionRuleForPlayerIn(ctx, tx.EsportsCommissionRule,
		tx.EsportsSku, tx.EsportsProductCategory, order, playerID, addonPlayerLevel, now)
	if err != nil {
		return nil, err
	}
	commission := addonCommissionCents(rule, addon.AddedCents, addon.AddedUnits)

	update := tx.EsportsOrderAddon.Update().Where(
		esportsorderaddon.IDEQ(addon.ID),
		esportsorderaddon.VersionEQ(addon.Version),
		esportsorderaddon.StatusEQ(esportsorderaddon.StatusPending),
	).SetStatus(esportsorderaddon.StatusConfirmed).
		SetConfirmedPlayerID(playerID).SetConfirmedAt(now.UTC()).
		SetCommissionCents(commission).AddVersion(1)
	if rule != nil {
		update = update.SetRuleID(rule.ID).SetRuleNo(rule.RuleNo).SetRuleVersion(rule.Version)
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	// 订单累加加钟金额与服务量（只增不改）。
	changed, err = tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusInService),
	).AddAddonCents(addon.AddedCents).AddAddonUnits(addon.AddedUnits).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	// 确认只保存分成快照，不计提分成；收款成功后再按本次快照建 `kind=addon` 明细。
	// 事件里同时记录规则快照（含 calc_mode），便于对账与追溯。
	details := map[string]interface{}{
		"addon_no": addon.AddonNo, "added_cents": addon.AddedCents,
		"added_units": addon.AddedUnits, "commission_cents": commission,
	}
	if rule != nil {
		details["rule_id"] = rule.ID
		details["rule_no"] = rule.RuleNo
		details["rule_version"] = rule.Version
		details["calc_mode"] = rule.CalcMode.String()
	}
	// 订单事件占用新版本号（唯一索引 (order_id, order_version)）。
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.addon_confirmed").
		SetActorType("player").SetActorID(strconv.Itoa(playerID)).
		SetDetails(details).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrderAddon.Get(ctx, addonID)
}

// rejectOrderAddon 陪玩拒绝加钟：仅本人在该订单上有 accepted 席位时可拒绝，须填原因。
func rejectOrderAddon(ctx context.Context, db *ent.Client, playerID, addonID int,
	reason string, now time.Time) (*ent.EsportsOrderAddon, error) {
	reason = strings.TrimSpace(reason)
	if playerID < 1 || addonID < 1 || reason == "" || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	addon, err := tx.EsportsOrderAddon.Get(ctx, addonID)
	if err != nil {
		return nil, err
	}
	if addon.Status != esportsorderaddon.StatusPending {
		return nil, errConflict
	}
	seatExists, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(addon.OrderID),
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !seatExists {
		return nil, errForbidden
	}
	changed, err := tx.EsportsOrderAddon.Update().Where(
		esportsorderaddon.IDEQ(addon.ID),
		esportsorderaddon.VersionEQ(addon.Version),
		esportsorderaddon.StatusEQ(esportsorderaddon.StatusPending),
	).SetStatus(esportsorderaddon.StatusRejected).
		SetRejectReason(reason).SetConfirmedPlayerID(playerID).
		SetConfirmedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	order, err := tx.EsportsOrder.Get(ctx, addon.OrderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.addon_rejected").
		SetActorType("player").SetActorID(strconv.Itoa(playerID)).
		SetDetails(map[string]interface{}{
			"addon_no": addon.AddonNo, "reason": reason,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrderAddon.Get(ctx, addonID)
}

// cancelOrderAddon 客服撤销加钟：待确认（陪玩未回应）与「已确认但未收款」均可撤销，
// **已收款的加钟不可撤销**。撤销已确认加钟时同事务回滚订单的加钟累加，并处理已计提的加钟分成：
// 仍待结算的置 void，已结算的新增等额负数冲回明细（历史流水只增不删）。并写 order.addon_cancelled 事件。
func cancelOrderAddon(ctx context.Context, db *ent.Client, addonID, adminID int,
	now time.Time) (*ent.EsportsOrderAddon, error) {
	if addonID < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	addon, err := tx.EsportsOrderAddon.Get(ctx, addonID)
	if err != nil {
		return nil, err
	}
	cancellable := addon.Status == esportsorderaddon.StatusPending ||
		(addon.Status == esportsorderaddon.StatusConfirmed &&
			addon.PaymentStatus == esportsorderaddon.PaymentStatusUnpaid)
	if !cancellable {
		if addon.Status == esportsorderaddon.StatusConfirmed &&
			addon.PaymentStatus == esportsorderaddon.PaymentStatusPaid {
			return nil, fmt.Errorf("%w：已收款的加钟不可撤销", errConflict)
		}
		return nil, errConflict
	}
	order, err := tx.EsportsOrder.Get(ctx, addon.OrderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	update := tx.EsportsOrderAddon.Update().Where(
		esportsorderaddon.IDEQ(addon.ID),
		esportsorderaddon.VersionEQ(addon.Version),
		esportsorderaddon.StatusEQ(addon.Status),
	)
	if addon.Status == esportsorderaddon.StatusConfirmed {
		update = update.Where(esportsorderaddon.PaymentStatusEQ(esportsorderaddon.PaymentStatusUnpaid))
	}
	changed, err := update.SetStatus(esportsorderaddon.StatusCancelled).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	details := map[string]interface{}{"addon_no": addon.AddonNo}
	orderVersion := order.Version
	if addon.Status == esportsorderaddon.StatusConfirmed {
		// 撤销已确认加钟：回滚确认时累加到订单的追加金额与服务量（只减本次确认的部分）。
		if addon.AddedCents > 0 || addon.AddedUnits > 0 {
			changed, err = tx.EsportsOrder.Update().Where(
				esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
				esportsorder.AddonCentsGTE(addon.AddedCents),
				esportsorder.AddonUnitsGTE(addon.AddedUnits),
			).AddAddonCents(-addon.AddedCents).AddAddonUnits(-addon.AddedUnits).
				AddVersion(1).Save(ctx)
			if err != nil {
				return nil, err
			}
			if changed != 1 {
				return nil, errConflict
			}
			orderVersion = order.Version + 1
		}
		if err = cancelAccruedAddonCommission(ctx, tx, addon); err != nil {
			return nil, err
		}
		details["added_cents"] = addon.AddedCents
		details["added_units"] = addon.AddedUnits
		details["commission_cents"] = addon.CommissionCents
	}
	newVersion, err := bumpOrder(ctx, tx, order.ID, orderVersion, "")
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.addon_cancelled").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(details).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrderAddon.Get(ctx, addonID)
}

// cancelAccruedAddonCommission 撤销加钟时处理已计提的加钟分成（历史数据可能仍带确认即计提的明细）：
// 待结算明细置 void；已结算明细新增等额负数冲回，保持流水只增不删。
func cancelAccruedAddonCommission(ctx context.Context, tx *ent.Tx, addon *ent.EsportsOrderAddon) error {
	if addon.ConfirmedPlayerID == nil || *addon.ConfirmedPlayerID < 1 {
		return nil
	}
	playerID := *addon.ConfirmedPlayerID
	idem := "commission:addon:" + addon.AddonNo + ":" + strconv.Itoa(playerID)
	entry, err := tx.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.IdemKeyEQ(idem),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if entry.Status == esportscommissionentry.StatusVoid {
		return nil
	}
	if entry.Status == esportscommissionentry.StatusPendingSettlement {
		changed, err := tx.EsportsCommissionEntry.Update().Where(
			esportscommissionentry.IDEQ(entry.ID),
			esportscommissionentry.VersionEQ(entry.Version),
			esportscommissionentry.StatusEQ(esportscommissionentry.StatusPendingSettlement),
		).SetStatus(esportscommissionentry.StatusVoid).AddVersion(1).Save(ctx)
		if err != nil {
			return err
		}
		if changed != 1 {
			return errConflict
		}
		return nil
	}
	// 已结算：新增负数冲回明细（不修改历史明细）。
	create := tx.EsportsCommissionEntry.Create().
		SetIdemKey("commission:reversal:addon_cancel:" + addon.AddonNo + ":" + strconv.Itoa(entry.ID)).
		SetOrderID(addon.OrderID).SetPlayerID(playerID).
		SetKind(esportscommissionentry.KindReversal).SetSeatIndex(entry.SeatIndex).
		SetBaseCents(entry.BaseCents).SetAmountCents(-entry.AmountCents).
		SetRuleNo(entry.RuleNo).SetRuleVersion(entry.RuleVersion).
		SetCalcMode(esportscommissionentry.CalcMode(entry.CalcMode.String())).
		SetStatus(esportscommissionentry.StatusPendingSettlement).
		SetNote(addonReversalNote + "·加钟撤销")
	if entry.RuleID != nil {
		create = create.SetRuleID(*entry.RuleID)
	}
	if _, err := create.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errConflict
		}
		return err
	}
	return nil
}

// hasPendingAddon 判断订单是否存在未处理（待陪玩确认）的加钟，供结单条件校验（§3.4）。
func hasPendingAddon(ctx context.Context, db *ent.Client, orderID int) (bool, error) {
	return db.EsportsOrderAddon.Query().Where(
		esportsorderaddon.OrderIDEQ(orderID),
		esportsorderaddon.StatusEQ(esportsorderaddon.StatusPending),
	).Exist(ctx)
}

// payOrderAddonByBalance 客服对**已确认**的加钟单用客户余额收单（§3.3 / §6.2 加钟收款）。
// 约束：加钟单须 confirmed 且未收款、追加金额 > 0、客户余额足额；扣减顺序按
// `app.esports.prepay_deduction_order`。同事务写余额消费流水、预付款落账与批次分摊，
// 收款成功后按加钟确认时保存的规则快照计提一次 `kind=addon` 分成（幂等键
// `commission:addon:<addonNo>:<playerID>`，历史已计提则跳过），并留 `order.addon_paid` 事件
// （幂等靠加钟单条件更新兜底：已收款再次调用返回冲突）。
// ponytail: 仅支持客户余额足额核销，储值卡核销、部分收款与加钟支付意图未接入。
func payOrderAddonByBalance(ctx context.Context, db *ent.Client, adminID, addonID int,
	now time.Time) (*ent.EsportsOrderAddon, error) {
	if adminID < 1 || addonID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	addon, err := tx.EsportsOrderAddon.Get(ctx, addonID)
	if err != nil {
		return nil, err
	}
	if addon.Status != esportsorderaddon.StatusConfirmed ||
		addon.PaymentStatus != esportsorderaddon.PaymentStatusUnpaid || addon.AddedCents <= 0 {
		return nil, fmt.Errorf("%w：仅「已确认且未收款」的加钟单可收款", errConflict)
	}
	order, err := tx.EsportsOrder.Get(ctx, addon.OrderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	// 账户级串行锁（§4.2 / §12.1）：与订单余额核销、售后退款共用同一把余额锁，
	// 否则「求和 → 写负数流水」窗口内并发收款可把余额扣成负数。
	unlock := lockMoneyAccount("balance:" + strconv.Itoa(order.CustomerID))
	defer unlock()
	customer, err := tx.EsportsCustomer.Get(ctx, order.CustomerID)
	if err != nil {
		return nil, err
	}
	balance, err := walletBalanceTx(ctx, tx, walletCustomerBalance, customer.ID)
	if err != nil {
		return nil, err
	}
	if balance.total() < addon.AddedCents {
		return nil, fmt.Errorf("%w：客户余额不足，加钟应收 %d 分", errInsufficientBalance, addon.AddedCents)
	}
	principalDeduct, bonusDeduct := splitPrepayDeduction(
		prepayDeductionOrder(ctx, tx.Client()), balance.PrincipalCents, balance.BonusCents, addon.AddedCents)
	walletTx, err := tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(customer.ID).
		SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-principalDeduct).SetBonusDeltaCents(-bonusDeduct).
		SetSourceType("order_addon").SetSourceID(addon.AddonNo).
		SetOperatorID(adminID).SetNote("加钟余额收款").Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsLedgerEntry.Create().SetAccountType("customer_order_prepayment").
		SetAccountID(strconv.Itoa(customer.ID)).SetSourceType("wallet_tx").
		SetSourceID(strconv.Itoa(walletTx.ID)).SetEntryRole("applied_order_prepayment").
		SetDeltaCents(addon.AddedCents).Save(ctx); err != nil {
		return nil, err
	}
	// 批次级逐笔扣减（§4.3 / §4.5）：记录本次加钟收款动用了哪些充值批次。
	if err = allocateLedgerConsumption(ctx, tx, walletCustomerBalance, strconv.Itoa(customer.ID),
		"order_addon", addon.AddonNo, addon.AddedCents); err != nil {
		return nil, err
	}
	changed, err := tx.EsportsOrderAddon.Update().Where(
		esportsorderaddon.IDEQ(addon.ID),
		esportsorderaddon.VersionEQ(addon.Version),
		esportsorderaddon.StatusEQ(esportsorderaddon.StatusConfirmed),
		esportsorderaddon.PaymentStatusEQ(esportsorderaddon.PaymentStatusUnpaid),
	).SetPaymentStatus(esportsorderaddon.PaymentStatusPaid).
		SetPaidCents(addon.AddedCents).SetPaidAt(now.UTC()).SetPaidByAdminID(adminID).
		SetPaymentRef(walletTx.TxNo).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	// 收款成功后才计提加钟分成：按确认时保存的规则快照建明细，幂等键兜底重复收款/重放。
	if err = accrueAddonCommissionOnPayment(ctx, tx, addon); err != nil {
		return nil, err
	}
	newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.addon_paid").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"addon_no": addon.AddonNo, "paid_cents": addon.AddedCents,
			"principal_deducted": principalDeduct, "bonus_deducted": bonusDeduct,
			"commission_cents": addon.CommissionCents,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrderAddon.Get(ctx, addonID)
}

// accrueAddonCommissionOnPayment 加钟收款成功后按确认时保存的规则快照计提 `kind=addon` 分成。
// 只在收款成功路径调用；幂等键 `commission:addon:<addonNo>:<playerID>` 唯一，
// 重复收款/重放或历史数据（旧版确认即计提）已有同一明细时跳过，不重复计提。
func accrueAddonCommissionOnPayment(ctx context.Context, tx *ent.Tx, addon *ent.EsportsOrderAddon) error {
	if addon.CommissionCents <= 0 || addon.ConfirmedPlayerID == nil || *addon.ConfirmedPlayerID < 1 {
		return nil
	}
	playerID := *addon.ConfirmedPlayerID
	idem := "commission:addon:" + addon.AddonNo + ":" + strconv.Itoa(playerID)
	exists, err := tx.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.IdemKeyEQ(idem),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	// 席位号用于分成明细归属；确认后席位状态可能已变化，缺失时按 0 兜底。
	seatIndex := 0
	seat, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(addon.OrderID),
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
	).Only(ctx)
	if err == nil {
		seatIndex = seat.SeatIndex
	} else if !ent.IsNotFound(err) {
		return err
	}
	// 规则行不可原地修改且旧版本保留，按 rule_id 读取到的 calc_mode 即确认时点快照。
	calcMode := esportscommissionentry.CalcModePercent
	if addon.RuleID != nil {
		rule, err := tx.EsportsCommissionRule.Get(ctx, *addon.RuleID)
		if err == nil {
			calcMode = esportscommissionentry.CalcMode(rule.CalcMode.String())
		} else if !ent.IsNotFound(err) {
			return err
		}
	}
	create := tx.EsportsCommissionEntry.Create().
		SetIdemKey(idem).SetOrderID(addon.OrderID).SetPlayerID(playerID).
		SetKind(esportscommissionentry.KindAddon).SetSeatIndex(seatIndex).
		SetCalcMode(calcMode).SetBaseCents(addon.AddedCents).
		SetAmountCents(addon.CommissionCents).
		SetStatus(esportscommissionentry.StatusPendingSettlement).
		SetNote("加钟分成（" + addon.AddonNo + "）")
	if addon.RuleID != nil {
		create = create.SetRuleID(*addon.RuleID)
	}
	if addon.RuleNo != "" {
		create = create.SetRuleNo(addon.RuleNo)
	}
	if addon.RuleVersion > 0 {
		create = create.SetRuleVersion(addon.RuleVersion)
	}
	if _, err := create.Save(ctx); err != nil {
		// 并发收款或历史明细已存在：唯一索引兜底，视为已计提。
		if ent.IsConstraintError(err) {
			return nil
		}
		return err
	}
	return nil
}

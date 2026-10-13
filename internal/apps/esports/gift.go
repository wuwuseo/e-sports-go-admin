package esports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsgift"
	"go-admin/internal/ent/esportsgiftorder"
	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsidempotency"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/systemconfig"
)

// 礼物模块（§9）相关常量。
const (
	// 大额打赏阈值（分）系统配置键；未配置时用默认值。
	giftHighValueThresholdKey = "app.esports.gift_high_value_threshold_cents"
	// 默认大额阈值 100000 分 = 1000 元。
	defaultGiftHighValueCents = 100000
	// 防刷榜窗口内同一「客户→陪玩」笔数达到该值即标记待复核（§9.4）。
	giftFlagPairLimit = 5
	// 防刷榜观察窗口小时数。
	giftFlagWindowHours = 24
)

// giftInput 是礼物目录的可编辑字段。code 创建后不可变。
type giftInput struct {
	Code            string `json:"code"`
	Name            string `json:"name"`
	Icon            string `json:"icon"`
	PriceCents      int64  `json:"price_cents"`
	Category        string `json:"category"`
	CommissionMode  string `json:"commission_mode"`
	CommissionBp    int    `json:"commission_bp"`
	CommissionCents int64  `json:"commission_cents"`
	HighValue       bool   `json:"high_value"`
	RequireAge      bool   `json:"require_age"`
	SortOrder       int    `json:"sort_order"`
}

// validateGiftInput 校验礼物目录配置；任一不满足返回 errInvalidInput。
func validateGiftInput(in giftInput) error {
	code := strings.TrimSpace(in.Code)
	name := strings.TrimSpace(in.Name)
	if code == "" || len(code) > 80 || name == "" || len([]rune(name)) > 120 ||
		len([]rune(in.Icon)) > 255 || len([]rune(in.Category)) > 80 || in.PriceCents <= 0 {
		return errInvalidInput
	}
	switch in.CommissionMode {
	case "percent":
		if in.CommissionBp < 0 || in.CommissionBp > 10000 {
			return errInvalidInput
		}
	case "fixed":
		if in.CommissionCents < 0 || in.CommissionCents > in.PriceCents {
			return errInvalidInput
		}
	default:
		return errInvalidInput
	}
	return nil
}

// createGift 新建礼物（默认 draft）；code 冲突返回 errConflict。
func createGift(ctx context.Context, db *ent.Client, in giftInput, status string) (*ent.EsportsGift, error) {
	if err := validateGiftInput(in); err != nil {
		return nil, err
	}
	if status != "draft" && status != "on_sale" {
		return nil, errInvalidInput
	}
	item, err := db.EsportsGift.Create().SetCode(strings.TrimSpace(in.Code)).
		SetName(strings.TrimSpace(in.Name)).SetIcon(strings.TrimSpace(in.Icon)).
		SetPriceCents(in.PriceCents).SetCategory(strings.TrimSpace(in.Category)).
		SetCommissionMode(esportsgift.CommissionMode(in.CommissionMode)).
		SetCommissionBp(in.CommissionBp).SetCommissionCents(in.CommissionCents).
		SetHighValue(in.HighValue).SetRequireAge(in.RequireAge).
		SetSortOrder(in.SortOrder).SetStatus(esportsgift.Status(status)).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	return item, nil
}

// updateGift 条件更新礼物（version 乐观锁）；code、状态不可通过本接口改。
func updateGift(ctx context.Context, db *ent.Client, id, expectedVersion int, in giftInput) (*ent.EsportsGift, error) {
	if id < 1 || expectedVersion < 1 {
		return nil, errInvalidInput
	}
	if err := validateGiftInput(in); err != nil {
		return nil, err
	}
	changed, err := db.EsportsGift.Update().Where(
		esportsgift.IDEQ(id), esportsgift.VersionEQ(expectedVersion),
	).SetName(strings.TrimSpace(in.Name)).SetIcon(strings.TrimSpace(in.Icon)).
		SetPriceCents(in.PriceCents).SetCategory(strings.TrimSpace(in.Category)).
		SetCommissionMode(esportsgift.CommissionMode(in.CommissionMode)).
		SetCommissionBp(in.CommissionBp).SetCommissionCents(in.CommissionCents).
		SetHighValue(in.HighValue).SetRequireAge(in.RequireAge).
		SetSortOrder(in.SortOrder).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsGift.Get(ctx, id)
}

// setGiftStatus 上下架礼物：draft/on_sale/off_sale 三态互转。
func setGiftStatus(ctx context.Context, db *ent.Client, id, expectedVersion int, status string) (*ent.EsportsGift, error) {
	if id < 1 || expectedVersion < 1 {
		return nil, errInvalidInput
	}
	switch status {
	case "draft", "on_sale", "off_sale":
	default:
		return nil, errInvalidInput
	}
	changed, err := db.EsportsGift.Update().Where(
		esportsgift.IDEQ(id), esportsgift.VersionEQ(expectedVersion),
	).SetStatus(esportsgift.Status(status)).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsGift.Get(ctx, id)
}

// listGifts 列出礼物目录，status 非空时按状态过滤，按排序值与 ID 升序。
func listGifts(ctx context.Context, db *ent.Client, status string, limit int) ([]*ent.EsportsGift, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsGift.Query()
	switch status = strings.TrimSpace(status); status {
	case "":
	case "draft", "on_sale", "off_sale":
		q.Where(esportsgift.StatusEQ(esportsgift.Status(status)))
	default:
		return nil, errInvalidInput
	}
	return q.Order(ent.Asc(esportsgift.FieldSortOrder), ent.Asc(esportsgift.FieldID)).Limit(limit).All(ctx)
}

// giftInput 的幂等哈希：同一请求内容重复提交返回同一单据。
func giftRequestHash(in giftSendInput) string {
	data, _ := json.Marshal(in)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// existingGift 按幂等记录取回已创建的礼物单据；不存在返回 nil, nil。
func existingGift(ctx context.Context, db *ent.Client, principal, key, hash string) (*ent.EsportsGiftOrder, error) {
	record, err := db.EsportsIdempotency.Query().Where(
		esportsidempotency.OperationEQ("send_gift"),
		esportsidempotency.PrincipalEQ(principal),
		esportsidempotency.RequestKeyEQ(key),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if record.RequestHash != hash {
		return nil, errConflict
	}
	return db.EsportsGiftOrder.Query().Where(esportsgiftorder.GiftNoEQ(record.ResultRef)).Only(ctx)
}

// giftSendInput 是送礼请求。收礼对象在支付前确认（player_id），支付后不得静默改人（§9.2）。
type giftSendInput struct {
	CustomerID int    `json:"customer_id"`
	GiftID     int    `json:"gift_id"`
	PlayerID   int    `json:"player_id"`
	Quantity   int    `json:"quantity"`
	OrderID    *int   `json:"order_id"`
	Confirmed  bool   `json:"confirmed"`
	ClientKey  string `json:"client_key"`
}

// giftIncome 按礼物自身分成配置算陪玩礼物收入（分）。percent 用金额 * bp / 10000（防溢出分解），
// fixed 取固定金额；结果夹在 [0, 金额] 之间。纯函数，便于测试。
func giftIncome(amount int64, mode string, bp int, cents int64) int64 {
	if amount <= 0 {
		return 0
	}
	var income int64
	if mode == "fixed" {
		income = cents
	} else {
		if bp <= 0 {
			return 0
		}
		income = amount/10000*int64(bp) + amount%10000*int64(bp)/10000
	}
	if income < 0 {
		return 0
	}
	if income > amount {
		return amount
	}
	return income
}

// giftCalcMode 把礼物分成口径映射到分成明细的 calc_mode。
func giftCalcMode(mode string) esportscommissionentry.CalcMode {
	if mode == "fixed" {
		return esportscommissionentry.CalcModeFixedPerOrder
	}
	return esportscommissionentry.CalcModePercent
}

// giftHighValueThresholdCents 读大额打赏阈值（§9.5）；未配置或非法时回落默认 1000 元。
func giftHighValueThresholdCents(ctx context.Context, db *ent.Client) (int64, error) {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(giftHighValueThresholdKey)).Only(ctx)
	if ent.IsNotFound(err) {
		return defaultGiftHighValueCents, nil
	}
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(strings.TrimSpace(item.Value), 10, 64)
	if err != nil || value <= 0 {
		return defaultGiftHighValueCents, nil
	}
	return value, nil
}

// giftPairFlagged 检测同一「客户→陪玩」在防刷窗口内是否已异常集中（§9.4 防刷榜）。
// ponytail: 只实现「异常集中」启发式；跨身份的「往返送礼」需要客户与陪玩身份映射，未实现。
func giftPairFlagged(ctx context.Context, db *ent.Client, customerID, playerID int, now time.Time) (bool, error) {
	since := now.UTC().Add(-time.Duration(giftFlagWindowHours) * time.Hour)
	count, err := db.EsportsGiftOrder.Query().Where(
		esportsgiftorder.CustomerIDEQ(customerID),
		esportsgiftorder.PlayerIDEQ(playerID),
		esportsgiftorder.CreatedAtGTE(since),
	).Count(ctx)
	if err != nil {
		return false, err
	}
	return count >= giftFlagPairLimit, nil
}

// sendGift 送礼：先查幂等记录，未命中则走 createGiftOnce；幂等冲突时回查已建单据。
func sendGift(ctx context.Context, db *ent.Client, adminID int, in giftSendInput, now time.Time) (*ent.EsportsGiftOrder, error) {
	order, err := sendGiftOnce(ctx, db, adminID, in, now)
	if err != nil && errors.Is(err, errConflict) {
		principal := fmt.Sprintf("admin:%d", adminID)
		if prior, lookupErr := existingGift(ctx, db, principal, in.ClientKey, giftRequestHash(in)); lookupErr == nil && prior != nil {
			return prior, nil
		}
	}
	return order, err
}

// sendGiftOnce 在同一事务内校验付款来源、原子扣减客户余额本金、生成礼物单据与礼物收入、
// 计提会员成长值，并写通知事件。只有足额核销后才生成收入与成长值（§9.2）。
func sendGiftOnce(ctx context.Context, db *ent.Client, adminID int, in giftSendInput, now time.Time) (*ent.EsportsGiftOrder, error) {
	key := strings.TrimSpace(in.ClientKey)
	if in.CustomerID < 1 || in.GiftID < 1 || in.PlayerID < 1 || in.Quantity < 1 || in.Quantity > 999 ||
		len(key) < 8 || len(key) > 128 {
		return nil, errInvalidInput
	}
	principal := fmt.Sprintf("admin:%d", adminID)
	hash := giftRequestHash(in)
	if previous, err := existingGift(ctx, db, principal, key, hash); err != nil || previous != nil {
		return previous, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	customer, err := tx.EsportsCustomer.Get(ctx, in.CustomerID)
	if err != nil {
		return nil, err
	}
	// 已验证身份：无登录绑定的客户不可送礼（§9.2 校验收付款来源）。
	if customer.UserID == nil {
		return nil, fmt.Errorf("%w：客户未绑定已验证身份，无法送礼", errInvalidInput)
	}
	player, err := tx.EsportsPlayer.Get(ctx, in.PlayerID)
	if err != nil {
		return nil, err
	}
	// 同一已验证身份不可给自己送礼（§9.4/§9.5）。
	if player.UserID == *customer.UserID {
		return nil, fmt.Errorf("%w：同一已验证身份不可给自己送礼", errInvalidInput)
	}
	// 未成年人与年龄限制：一律要求成年资格（§9.5）。
	if customer.AgeStatus != esportscustomer.AgeStatusAdult {
		return nil, errAgeUnverified
	}
	gift, err := tx.EsportsGift.Get(ctx, in.GiftID)
	if err != nil {
		return nil, err
	}
	if gift.Status != esportsgift.StatusOnSale {
		return nil, errConflict
	}
	if gift.PriceCents > math.MaxInt64/int64(in.Quantity) {
		return nil, errInvalidInput
	}
	amount := gift.PriceCents * int64(in.Quantity)
	// 大额 / 高额档二次确认（§9.5）。
	threshold, err := giftHighValueThresholdCents(ctx, tx.Client())
	if err != nil {
		return nil, err
	}
	if (gift.HighValue || amount >= threshold) && !in.Confirmed {
		return nil, fmt.Errorf("%w：大额打赏需二次确认后再提交", errInvalidInput)
	}
	// 关联订单仅追溯，校验归属（§9.2）。
	if in.OrderID != nil {
		order, err := tx.EsportsOrder.Get(ctx, *in.OrderID)
		if err != nil {
			return nil, err
		}
		if order.CustomerID != customer.ID {
			return nil, fmt.Errorf("%w：关联订单不属于该客户", errInvalidInput)
		}
	}
	// 付款来源：首版仅客户余额本金，拒绝赠送权益与外部直付（§9.5）。
	balance, err := walletBalanceTx(ctx, tx, walletCustomerBalance, customer.ID)
	if err != nil {
		return nil, err
	}
	if balance.PrincipalCents < amount {
		return nil, fmt.Errorf("%w：本金余额不足，请充值或更换支付方式", errInsufficientBalance)
	}
	flagged, err := giftPairFlagged(ctx, tx.Client(), customer.ID, player.ID, now)
	if err != nil {
		return nil, err
	}
	reviewStatus := esportsgiftorder.ReviewStatusNormal
	if flagged {
		reviewStatus = esportsgiftorder.ReviewStatusFlagged
	}
	giftNo := uuid.NewString()
	income := giftIncome(amount, gift.CommissionMode.String(), gift.CommissionBp, gift.CommissionCents)
	create := tx.EsportsGiftOrder.Create().SetGiftNo(giftNo).SetGiftID(gift.ID).
		SetCustomerID(customer.ID).SetPlayerID(player.ID).SetPlayerName(player.DisplayName).
		SetQuantity(in.Quantity).SetUnitPriceCents(gift.PriceCents).SetAmountCents(amount).
		SetPrincipalCents(amount).SetBonusCents(0).
		SetCommissionMode(esportsgiftorder.CommissionMode(gift.CommissionMode.String())).
		SetCommissionBp(gift.CommissionBp).SetCommissionCents(gift.CommissionCents).
		SetIncomeCents(income).SetReviewStatus(reviewStatus).
		SetStatus(esportsgiftorder.StatusPaid)
	if in.OrderID != nil {
		create.SetOrderID(*in.OrderID)
	}
	order, err := create.Save(ctx)
	if err != nil {
		return nil, err
	}
	// 原子扣减：仅扣本金，禁止动用赠送权益。
	if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(customer.ID).
		SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-amount).SetBonusDeltaCents(0).
		SetSourceType("gift").SetSourceID(giftNo).
		SetOperatorID(adminID).SetNote("礼物消费：" + gift.Name).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	// 疑似关联交易先标记，复核通过前不生成可提现礼物分成（§9.4）。
	if !flagged {
		if err = createGiftCommissionEntry(ctx, tx, order, income); err != nil {
			return nil, err
		}
	}
	if _, err = tx.EsportsGrowthLedger.Create().
		SetCustomerID(customer.ID).SetDelta(amount).
		SetSourceType(esportsgrowthledger.SourceTypeGift).
		SetSourceID(giftNo).SetOperatorID(adminID).SetNote("礼物消费计提").Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = recomputeCustomerLevel(ctx, tx.Client(), customer.ID, now); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsIdempotency.Create().SetOperation("send_gift").SetPrincipal(principal).
		SetRequestKey(key).SetRequestHash(hash).SetResultRef(giftNo).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	// 通知陪玩与客服（§9.2）。
	if _, err = tx.EsportsOutbox.Create().SetEventKey("gift.paid:" + giftNo).
		SetEventType("gift.paid").SetAggregateRef(giftNo).
		SetPayload(map[string]interface{}{
			"gift_no": giftNo, "customer_id": customer.ID, "player_id": player.ID,
			"amount_cents": amount, "income_cents": income, "review_status": reviewStatus.String(),
		}).Save(ctx); err != nil && !ent.IsConstraintError(err) {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsGiftOrder.Get(ctx, order.ID)
}

// createGiftCommissionEntry 把礼物收入写成一条 kind=gift 的分成明细，随订单收入一起结算（§9.3），
// 明细里以 kind 区分。幂等靠 idem_key 唯一索引。
func createGiftCommissionEntry(ctx context.Context, tx *ent.Tx, order *ent.EsportsGiftOrder, income int64) error {
	if income <= 0 {
		return nil
	}
	_, err := tx.EsportsCommissionEntry.Create().
		SetIdemKey("commission:gift:" + order.GiftNo).
		SetPlayerID(order.PlayerID).SetKind(esportscommissionentry.KindGift).
		SetBaseCents(order.AmountCents).SetAmountCents(income).
		SetCalcMode(giftCalcMode(order.CommissionMode.String())).
		SetStatus(esportscommissionentry.StatusPendingSettlement).
		SetNote("礼物收入").Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return errConflict
		}
		return err
	}
	return nil
}

// reviewGiftOrder 复核疑似关联礼物（§9.4）：approve 补建分成明细（释放收入），reject 不释放。
// 仅 flagged 状态可复核；复核后状态不可再改。
func reviewGiftOrder(ctx context.Context, db *ent.Client, adminID, giftOrderID int, decision string, now time.Time) (*ent.EsportsGiftOrder, error) {
	if giftOrderID < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	var target esportsgiftorder.ReviewStatus
	switch decision {
	case "approve":
		target = esportsgiftorder.ReviewStatusApproved
	case "reject":
		target = esportsgiftorder.ReviewStatusRejected
	default:
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsGiftOrder.Get(ctx, giftOrderID)
	if err != nil {
		return nil, err
	}
	if order.ReviewStatus != esportsgiftorder.ReviewStatusFlagged {
		return nil, errConflict
	}
	if target == esportsgiftorder.ReviewStatusApproved {
		if err = createGiftCommissionEntry(ctx, tx, order, order.IncomeCents); err != nil {
			return nil, err
		}
	}
	changed, err := tx.EsportsGiftOrder.Update().Where(
		esportsgiftorder.IDEQ(order.ID), esportsgiftorder.VersionEQ(order.Version),
		esportsgiftorder.ReviewStatusEQ(esportsgiftorder.ReviewStatusFlagged),
	).SetReviewStatus(target).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsGiftOrder.Get(ctx, order.ID)
}

// refundGift 礼物退款（§9.5）：审核通过后由管理员执行——退还本金、冲回礼物分成与成长值。
// 仅已支付且未退款的礼物可退；幂等靠已退款状态与钱包流水唯一索引兜底。
// ponytail: 独立的争议/退款申请与审核流程未接入（复用现有售后的思路，此处直接由管理员执行）；
// 仅支持全额退款。
func refundGift(ctx context.Context, db *ent.Client, adminID, giftOrderID int, reason string, now time.Time) (*ent.EsportsGiftOrder, error) {
	reason = strings.TrimSpace(reason)
	if giftOrderID < 1 || adminID < 1 || reason == "" || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsGiftOrder.Get(ctx, giftOrderID)
	if err != nil {
		return nil, err
	}
	if order.Status != esportsgiftorder.StatusPaid || order.RefundCents > 0 {
		return nil, errConflict
	}
	refund := order.AmountCents
	if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(order.CustomerID).
		SetEntryType(esportswallettx.EntryTypeRefundReturn).
		SetPrincipalDeltaCents(refund).SetSourceType("gift").SetSourceID(order.GiftNo + ":refund").
		SetOperatorID(adminID).SetNote("礼物退款：" + reason).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	// 只有已生成礼物收入（未因待复核而挂起）才需要冲回分成。
	incomeExists, err := tx.EsportsCommissionEntry.Query().
		Where(esportscommissionentry.IdemKeyEQ("commission:gift:" + order.GiftNo)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if incomeExists {
		if _, err = tx.EsportsCommissionEntry.Create().
			SetIdemKey("commission:gift_reversal:" + order.GiftNo).
			SetPlayerID(order.PlayerID).SetKind(esportscommissionentry.KindGiftReversal).
			SetBaseCents(order.AmountCents).SetAmountCents(-order.IncomeCents).
			SetCalcMode(giftCalcMode(order.CommissionMode.String())).
			SetStatus(esportscommissionentry.StatusPendingSettlement).
			SetNote("礼物退款冲回").Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
	}
	if _, err = tx.EsportsGrowthLedger.Create().
		SetCustomerID(order.CustomerID).SetDelta(-refund).
		SetSourceType(esportsgrowthledger.SourceTypeGift).
		SetSourceID(order.GiftNo + ":refund").SetOperatorID(adminID).
		SetNote("礼物退款冲回成长值").Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	changed, err := tx.EsportsGiftOrder.Update().Where(
		esportsgiftorder.IDEQ(order.ID), esportsgiftorder.VersionEQ(order.Version),
		esportsgiftorder.StatusEQ(esportsgiftorder.StatusPaid),
	).SetRefundCents(refund).SetRefundStatus(esportsgiftorder.RefundStatusFull).
		SetStatus(esportsgiftorder.StatusRefunded).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = recomputeCustomerLevel(ctx, tx.Client(), order.CustomerID, now); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("gift.refunded:" + order.GiftNo).
		SetEventType("gift.refunded").SetAggregateRef(order.GiftNo).
		SetPayload(map[string]interface{}{
			"gift_no": order.GiftNo, "refund_cents": refund, "reason": reason,
		}).Save(ctx); err != nil && !ent.IsConstraintError(err) {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsGiftOrder.Get(ctx, order.ID)
}

// listGiftOrders 列出礼物单据，按客户 / 陪玩 / 复核状态过滤，按 ID 倒序。
func listGiftOrders(ctx context.Context, db *ent.Client, customerID, playerID int, reviewStatus string, limit int) ([]*ent.EsportsGiftOrder, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	q := db.EsportsGiftOrder.Query()
	if customerID > 0 {
		q.Where(esportsgiftorder.CustomerIDEQ(customerID))
	}
	if playerID > 0 {
		q.Where(esportsgiftorder.PlayerIDEQ(playerID))
	}
	switch reviewStatus = strings.TrimSpace(reviewStatus); reviewStatus {
	case "":
	case "normal", "flagged", "approved", "rejected":
		q.Where(esportsgiftorder.ReviewStatusEQ(esportsgiftorder.ReviewStatus(reviewStatus)))
	default:
		return nil, errInvalidInput
	}
	return q.Order(ent.Desc(esportsgiftorder.FieldID)).Limit(limit).All(ctx)
}

// giftLeaderboardRow 是榜单一行：归属人、展示名、汇总金额与笔数。
type giftLeaderboardRow struct {
	OwnerID     int    `json:"owner_id"`
	OwnerName   string `json:"owner_name"`
	AmountCents int64  `json:"amount_cents"`
	Count       int    `json:"count"`
}

// giftPeriodStart 按日/周/月推算榜单统计起点（UTC 自然周期）。
func giftPeriodStart(period string, now time.Time) (time.Time, error) {
	utc := now.UTC()
	day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case "daily":
		return day, nil
	case "weekly":
		offset := (int(day.Weekday()) + 6) % 7 // 距本周一的天数
		return day.AddDate(0, 0, -offset), nil
	case "monthly":
		return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC), nil
	default:
		return time.Time{}, errInvalidInput
	}
}

// giftLeaderboard 礼物榜单（§9.4）：board=customer 为客户贡献榜（按送礼人），board=player 为
// 陪玩收礼榜（按收礼陪玩）；period 支持 daily/weekly/monthly。只统计已支付且未退款的礼物。
// ponytail: 应用层内存聚合，礼物量级到十万级时改为 SQL 分组聚合。
func giftLeaderboard(ctx context.Context, db *ent.Client, board, period string, now time.Time, limit int) ([]giftLeaderboardRow, error) {
	if board != "customer" && board != "player" {
		return nil, errInvalidInput
	}
	start, err := giftPeriodStart(period, now)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		limit = 20
	}
	orders, err := db.EsportsGiftOrder.Query().Where(
		esportsgiftorder.CreatedAtGTE(start),
		esportsgiftorder.RefundStatusNEQ(esportsgiftorder.RefundStatusFull),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	index := map[int]int{}
	rows := make([]giftLeaderboardRow, 0, len(orders))
	for _, order := range orders {
		ownerID := order.PlayerID
		if board == "customer" {
			ownerID = order.CustomerID
		}
		if at, ok := index[ownerID]; ok {
			rows[at].AmountCents += order.AmountCents
			rows[at].Count++
			continue
		}
		index[ownerID] = len(rows)
		rows = append(rows, giftLeaderboardRow{OwnerID: ownerID, AmountCents: order.AmountCents, Count: 1})
	}
	for i := range rows {
		rows[i].OwnerName = giftOwnerName(ctx, db, board, rows[i].OwnerID)
	}
	// 按金额倒序、同额按笔数倒序。
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].AmountCents > rows[i].AmountCents ||
				(rows[j].AmountCents == rows[i].AmountCents && rows[j].Count > rows[i].Count) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// giftOwnerName 取榜单归属人的展示名；查不到时回落为空串。
func giftOwnerName(ctx context.Context, db *ent.Client, board string, ownerID int) string {
	if board == "customer" {
		item, err := db.EsportsCustomer.Get(ctx, ownerID)
		if err == nil {
			return item.DisplayName
		}
		return ""
	}
	item, err := db.EsportsPlayer.Get(ctx, ownerID)
	if err == nil {
		return item.DisplayName
	}
	return ""
}

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
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsgift"
	"go-admin/internal/ent/esportsgiftintent"
	"go-admin/internal/ent/esportsgiftorder"
	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsidempotency"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsstoredvaluecard"
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
	CustomerID        int    `json:"customer_id"`
	GiftID            int    `json:"gift_id"`
	PlayerID          int    `json:"player_id"`
	Quantity          int    `json:"quantity"`
	OrderID           *int   `json:"order_id"`
	PaymentSource     string `json:"payment_source"`
	StoredValueCardID int    `json:"stored_value_card_id"`
	Confirmed         bool   `json:"confirmed"`
	AuthorizationID   int    `json:"authorization_id"`
	ClientKey         string `json:"client_key"`
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

// giftPairFlagged 检测同一「客户→陪玩」异常集中及已验证身份之间的往返送礼。
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
	if count >= giftFlagPairLimit {
		return true, nil
	}
	customer, err := db.EsportsCustomer.Get(ctx, customerID)
	if err != nil {
		return false, err
	}
	if customer.UserID == nil {
		return false, nil
	}
	player, err := db.EsportsPlayer.Get(ctx, playerID)
	if err != nil {
		return false, err
	}
	reciprocalCustomer, err := db.EsportsCustomer.Query().Where(
		esportscustomer.UserIDEQ(player.UserID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	reciprocalPlayer, err := db.EsportsPlayer.Query().Where(
		esportsplayer.UserIDEQ(*customer.UserID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return db.EsportsGiftOrder.Query().Where(
		esportsgiftorder.CustomerIDEQ(reciprocalCustomer.ID),
		esportsgiftorder.PlayerIDEQ(reciprocalPlayer.ID),
		esportsgiftorder.StatusEQ(esportsgiftorder.StatusPaid),
		esportsgiftorder.CreatedAtGTE(since),
	).Exist(ctx)
}

// sendGift 送礼：先查幂等记录，未命中则走 createGiftOnce；幂等冲突时回查已建单据。
func sendGift(ctx context.Context, db *ent.Client, adminID int, in giftSendInput, now time.Time) (*ent.EsportsGiftOrder, error) {
	return sendGiftWithRole(ctx, db, adminID, false, in, now)
}

func sendGiftWithRole(ctx context.Context, db *ent.Client, adminID int, isSuper bool, in giftSendInput, now time.Time) (*ent.EsportsGiftOrder, error) {
	if in.PaymentSource == "" {
		in.PaymentSource = "customer_balance_principal"
	}
	if _, err := loadOwnedCustomer(ctx, db, adminID, isSuper, in.CustomerID); err != nil {
		return nil, err
	}
	order, err := sendGiftOnce(ctx, db, adminID, isSuper, in, now)
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
func sendGiftOnce(ctx context.Context, db *ent.Client, adminID int, isSuper bool, in giftSendInput, now time.Time) (*ent.EsportsGiftOrder, error) {
	key := strings.TrimSpace(in.ClientKey)
	if in.CustomerID < 1 || in.GiftID < 1 || in.PlayerID < 1 || in.Quantity < 1 || in.Quantity > 999 ||
		len(key) < 8 || len(key) > 128 {
		return nil, errInvalidInput
	}
	if (in.PaymentSource != "customer_balance_principal" || in.StoredValueCardID != 0) &&
		(in.PaymentSource != "stored_value_card_principal" || in.StoredValueCardID < 1) {
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
	var authorization *ent.EsportsGiftIntent
	if in.AuthorizationID > 0 {
		authorization, err = tx.EsportsGiftIntent.Get(ctx, in.AuthorizationID)
		if err != nil {
			return nil, err
		}
		if authorization.Status != esportsgiftintent.StatusConfirmed || authorization.ConfirmationRef == "" ||
			!now.UTC().Before(authorization.ExpiresAt) || authorization.CustomerID != in.CustomerID ||
			authorization.GiftID != in.GiftID || authorization.PlayerID != in.PlayerID ||
			authorization.Quantity != in.Quantity || !sameOptionalID(authorization.OrderID, in.OrderID) ||
			authorization.PaymentSource != in.PaymentSource ||
			!(in.StoredValueCardID == 0 && authorization.StoredValueCardID == nil ||
				authorization.StoredValueCardID != nil && *authorization.StoredValueCardID == in.StoredValueCardID) {
			return nil, errConflict
		}
	}
	customer, err := tx.EsportsCustomer.Get(ctx, in.CustomerID)
	if err != nil {
		return nil, err
	}
	if !isSuper && (customer.OwnerAdminID == nil || *customer.OwnerAdminID != adminID) {
		return nil, errForbidden
	}
	// 已验证身份：无登录绑定的客户不可送礼（§9.2 校验收付款来源）。
	if customer.UserID == nil {
		return nil, fmt.Errorf("%w：客户未绑定已验证身份，无法送礼", errInvalidInput)
	}
	if authorization != nil {
		account, lookupErr := tx.User.Get(ctx, *customer.UserID)
		if lookupErr != nil {
			return nil, errForbidden
		}
		verified, lookupErr := customerForVerifiedUser(ctx, tx.Client(), account.UID)
		if lookupErr != nil || verified.ID != customer.ID {
			return nil, errForbidden
		}
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
	if authorization != nil && (authorization.GiftVersion != gift.Version ||
		authorization.GiftName != gift.Name || authorization.PlayerName != player.DisplayName ||
		authorization.UnitPriceCents != gift.PriceCents) {
		return nil, errConflict
	}
	if gift.PriceCents > math.MaxInt64/int64(in.Quantity) {
		return nil, errInvalidInput
	}
	amount := gift.PriceCents * int64(in.Quantity)
	if authorization != nil && authorization.AmountCents != amount {
		return nil, errConflict
	}
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
	// 客户确认的付款来源不可由客服更换，余额与储值卡均只动本金。
	paymentAccount := walletCustomerBalance
	paymentOwnerID := customer.ID
	var card *ent.EsportsStoredValueCard
	if in.StoredValueCardID > 0 {
		card, err = tx.EsportsStoredValueCard.Get(ctx, in.StoredValueCardID)
		if err != nil {
			return nil, err
		}
		if err = storedValueCardUsableForGift(card, customer.ID, now); err != nil {
			return nil, err
		}
		if authorization != nil && authorization.StoredValueCardNo != card.CardNo {
			return nil, errConflict
		}
		paymentAccount, paymentOwnerID = walletStoredValueCard, card.ID
	}
	balance, err := walletBalanceTx(ctx, tx, paymentAccount, paymentOwnerID)
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
	var giftCsRuleID int
	if customer.OwnerAdminID != nil {
		rule, ruleErr := resolveCsCommissionRuleIn(ctx, tx.EsportsCsCommissionRule, *customer.OwnerAdminID, now)
		if ruleErr != nil {
			return nil, ruleErr
		}
		if rule != nil && rule.IncludeGifts {
			giftCsRuleID = rule.ID
		}
	}
	create := tx.EsportsGiftOrder.Create().SetGiftNo(giftNo).SetGiftID(gift.ID).
		SetCustomerID(customer.ID).SetPlayerID(player.ID).SetPlayerName(player.DisplayName).
		SetQuantity(in.Quantity).SetUnitPriceCents(gift.PriceCents).SetAmountCents(amount).
		SetPrincipalCents(amount).SetBonusCents(0).
		SetPaymentSource(in.PaymentSource).
		SetCommissionMode(esportsgiftorder.CommissionMode(gift.CommissionMode.String())).
		SetCommissionBp(gift.CommissionBp).SetCommissionCents(gift.CommissionCents).
		SetIncomeCents(income).SetReviewStatus(reviewStatus).
		SetStatus(esportsgiftorder.StatusPaid)
	if customer.OwnerAdminID != nil {
		create.SetResponsibleAdminID(*customer.OwnerAdminID)
	}
	if giftCsRuleID > 0 {
		create.SetCsCommissionRuleID(giftCsRuleID)
	}
	if in.OrderID != nil {
		create.SetOrderID(*in.OrderID)
	}
	if authorization != nil {
		create.SetGiftIntentID(authorization.ID)
	}
	if card != nil {
		create.SetStoredValueCardID(card.ID)
	}
	order, err := create.Save(ctx)
	if err != nil {
		return nil, err
	}
	// 原子扣减：仅扣本金，禁止动用赠送权益。
	if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountType(paymentAccount)).SetOwnerID(paymentOwnerID).
		SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-amount).SetBonusDeltaCents(0).
		SetSourceType("gift").SetSourceID(giftNo).
		SetOperatorID(adminID).SetNote("礼物消费：" + gift.Name).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if card != nil {
		if _, err = tx.EsportsLedgerEntry.Create().SetAccountType(storedValuePrepayAccount).
			SetAccountID(strconv.Itoa(card.ID)).SetSourceType("gift").SetSourceID(giftNo).
			SetEntryRole("card_gift_consume").SetDeltaCents(-amount).Save(ctx); err != nil {
			return nil, err
		}
		if err = allocateLedgerConsumption(ctx, tx, storedValuePrepayAccount, strconv.Itoa(card.ID), "gift", giftNo, amount); err != nil {
			return nil, err
		}
		if balance.total() == amount {
			if _, err = tx.EsportsStoredValueCard.Update().Where(esportsstoredvaluecard.IDEQ(card.ID),
				esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.StatusActive)).
				SetStatus(esportsstoredvaluecard.StatusExhausted).AddVersion(1).Save(ctx); err != nil {
				return nil, err
			}
		}
	}
	// 疑似关联交易先标记，复核通过前不生成可提现礼物分成（§9.4）。
	if !flagged {
		if err = createGiftCommissionEntry(ctx, tx, order, income); err != nil {
			return nil, err
		}
		if err = accrueGiftCsCommission(ctx, tx, order, now); err != nil {
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
	if authorization != nil {
		changed, updateErr := tx.EsportsGiftIntent.Update().Where(
			esportsgiftintent.IDEQ(authorization.ID), esportsgiftintent.StatusEQ(esportsgiftintent.StatusConfirmed),
			esportsgiftintent.VersionEQ(authorization.Version)).
			SetStatus(esportsgiftintent.StatusConsumed).SetGiftOrderID(order.ID).AddVersion(1).Save(ctx)
		if updateErr != nil {
			return nil, updateErr
		}
		if changed != 1 {
			return nil, errConflict
		}
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
	if order.ResponsibleAdminID != nil && *order.ResponsibleAdminID == adminID {
		return nil, errForbidden
	}
	if order.ReviewStatus != esportsgiftorder.ReviewStatusFlagged ||
		order.Status != esportsgiftorder.StatusPaid || order.RefundCents > 0 {
		return nil, errConflict
	}
	if target == esportsgiftorder.ReviewStatusApproved {
		if err = createGiftCommissionEntry(ctx, tx, order, order.IncomeCents); err != nil {
			return nil, err
		}
		if err = accrueGiftCsCommission(ctx, tx, order, now); err != nil {
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

// refundGift 礼物全额退款（§9.5）：由管理员直接执行——退还本金、冲回礼物分成、客服提成与成长值。
// 仅已支付且未退款的礼物可全额退；礼物争议（客户申请 → 审核 → 执行）走 refundGiftIn 的部分退款路径。
func refundGift(ctx context.Context, db *ent.Client, adminID int, isSuper bool, giftOrderID int, reason string, now time.Time) (*ent.EsportsGiftOrder, error) {
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
	if !isSuper {
		customer, err := tx.EsportsCustomer.Get(ctx, order.CustomerID)
		if err != nil {
			return nil, err
		}
		if customer.OwnerAdminID == nil || *customer.OwnerAdminID != adminID {
			return nil, errForbidden
		}
	}
	if order.Status != esportsgiftorder.StatusPaid || order.RefundCents > 0 {
		return nil, errConflict
	}
	if err = refundGiftIn(ctx, tx, order, order.AmountCents, order.GiftNo+":refund", reason, adminID, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsGiftOrder.Get(ctx, order.ID)
}

// refundGiftIn 在同一事务内执行礼物退款，amount 可小于礼物金额（§9.5 部分退款）。
// sourceID 作为钱包流水 / 卡批次账本 / 成长值的来源标识，保证多次部分退款各自幂等。
// 礼物分成与客服提成按退款比例冲回并封顶在已计提金额内；成长值按退款额反冲。
func refundGiftIn(ctx context.Context, tx *ent.Tx, order *ent.EsportsGiftOrder, amount int64,
	sourceID, reason string, adminID int, now time.Time) error {
	if amount <= 0 || amount > order.AmountCents {
		return errInvalidInput
	}
	if order.RefundCents+amount > order.AmountCents {
		return errConflict
	}
	accountType := esportswallettx.AccountTypeCustomerBalance
	ownerID := order.CustomerID
	var card *ent.EsportsStoredValueCard
	if order.StoredValueCardID != nil {
		var err error
		card, err = tx.EsportsStoredValueCard.Get(ctx, *order.StoredValueCardID)
		if err != nil {
			return err
		}
		if card.CustomerID != order.CustomerID || card.Status == esportsstoredvaluecard.StatusRefundPending ||
			card.Status == esportsstoredvaluecard.StatusRefunded || card.Status == esportsstoredvaluecard.StatusFrozen {
			return errConflict
		}
		accountType, ownerID = esportswallettx.AccountTypeStoredValueCard, card.ID
	}
	if _, err := tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(accountType).SetOwnerID(ownerID).
		SetEntryType(esportswallettx.EntryTypeRefundReturn).
		SetPrincipalDeltaCents(amount).SetSourceType("gift").SetSourceID(sourceID).
		SetOperatorID(adminID).SetNote("礼物退款：" + reason).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errConflict
		}
		return err
	}
	if card != nil {
		if _, err := tx.EsportsLedgerEntry.Create().SetAccountType(storedValuePrepayAccount).
			SetAccountID(strconv.Itoa(card.ID)).SetSourceType("gift").SetSourceID(sourceID).
			SetEntryRole("card_gift_refund_return").SetDeltaCents(amount).Save(ctx); err != nil {
			return err
		}
		if card.Status == esportsstoredvaluecard.StatusExhausted && now.UTC().Before(card.ValidUntil) {
			if _, err := tx.EsportsStoredValueCard.Update().Where(esportsstoredvaluecard.IDEQ(card.ID),
				esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.StatusExhausted)).
				SetStatus(esportsstoredvaluecard.StatusActive).AddVersion(1).Save(ctx); err != nil {
				return err
			}
		}
	}
	// 礼物分成按退款比例冲回（累计不超过已计提收入）。
	if err := reverseGiftCommissionIn(ctx, tx, order, amount); err != nil {
		return err
	}
	csEntry, err := tx.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.GiftOrderIDEQ(order.ID),
		esportscscommissionentry.KindEQ(esportscscommissionentry.KindGift),
	).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return err
	}
	if csEntry != nil {
		if err = reverseGiftCsCommission(ctx, tx, csEntry, order, amount); err != nil {
			return err
		}
	}
	if _, err = tx.EsportsGrowthLedger.Create().
		SetCustomerID(order.CustomerID).SetDelta(-amount).
		SetSourceType(esportsgrowthledger.SourceTypeGift).
		SetSourceID(sourceID).SetOperatorID(adminID).
		SetNote("礼物退款冲回成长值").Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errConflict
		}
		return err
	}
	refundedAfter := order.RefundCents + amount
	full := refundedAfter >= order.AmountCents
	update := tx.EsportsGiftOrder.Update().Where(
		esportsgiftorder.IDEQ(order.ID), esportsgiftorder.VersionEQ(order.Version),
		esportsgiftorder.StatusEQ(esportsgiftorder.StatusPaid),
	).SetRefundCents(refundedAfter).AddVersion(1)
	if full {
		update.SetRefundStatus(esportsgiftorder.RefundStatusFull).SetStatus(esportsgiftorder.StatusRefunded)
	} else {
		update.SetRefundStatus(esportsgiftorder.RefundStatusPartial)
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	if err = recomputeCustomerLevel(ctx, tx.Client(), order.CustomerID, now); err != nil {
		return err
	}
	// 首次全额退款沿用历史事件键，多次部分退款按累计退款额区分键值。
	eventKey := "gift.refunded:" + order.GiftNo
	if order.RefundCents > 0 {
		eventKey += ":" + strconv.FormatInt(refundedAfter, 10)
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey(eventKey).
		SetEventType("gift.refunded").SetAggregateRef(order.GiftNo).
		SetPayload(map[string]interface{}{
			"gift_no": order.GiftNo, "refund_cents": amount, "refunded_cents": refundedAfter, "reason": reason,
		}).Save(ctx); err != nil && !ent.IsConstraintError(err) {
		return err
	}
	return nil
}

// reverseGiftCommissionIn 按退款比例冲回礼物分成（§9.5）：累计冲回不超过已计提的陪玩礼物收入。
// 只有已生成礼物收入（未因待复核而挂起）才需要冲回；多次部分退款的幂等键按累计退款额区分。
func reverseGiftCommissionIn(ctx context.Context, tx *ent.Tx, order *ent.EsportsGiftOrder, amount int64) error {
	incomeExists, err := tx.EsportsCommissionEntry.Query().
		Where(esportscommissionentry.IdemKeyEQ("commission:gift:" + order.GiftNo)).Exist(ctx)
	if err != nil {
		return err
	}
	if !incomeExists || order.IncomeCents <= 0 || order.AmountCents <= 0 {
		return nil
	}
	prior, err := tx.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.IdemKeyHasPrefix("commission:gift_reversal:" + order.GiftNo),
	).Select(esportscommissionentry.FieldAmountCents).All(ctx)
	if err != nil {
		return err
	}
	var reversed int64
	for _, entry := range prior {
		reversed -= entry.AmountCents
	}
	remaining := order.IncomeCents - reversed
	target := order.IncomeCents * amount / order.AmountCents
	if target > remaining {
		target = remaining
	}
	if target <= 0 {
		return nil
	}
	idem := "commission:gift_reversal:" + order.GiftNo
	if order.RefundCents > 0 {
		idem += ":" + strconv.FormatInt(order.RefundCents+amount, 10)
	}
	if _, err = tx.EsportsCommissionEntry.Create().
		SetIdemKey(idem).
		SetPlayerID(order.PlayerID).SetKind(esportscommissionentry.KindGiftReversal).
		SetBaseCents(order.AmountCents).SetAmountCents(-target).
		SetCalcMode(giftCalcMode(order.CommissionMode.String())).
		SetStatus(esportscommissionentry.StatusPendingSettlement).
		SetNote("礼物退款冲回").Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errConflict
		}
		return err
	}
	return nil
}

// listGiftOrders 按管理员归属及客户 / 陪玩 / 复核状态过滤，按 ID 倒序。
func listGiftOrders(ctx context.Context, db *ent.Client, adminID int, isSuper bool, customerID, playerID int, reviewStatus string, limit int) ([]*ent.EsportsGiftOrder, error) {
	if adminID < 1 {
		return nil, errForbidden
	}
	if limit < 1 || limit > 500 {
		limit = 200
	}
	q := db.EsportsGiftOrder.Query()
	if !isSuper {
		ids, err := db.EsportsCustomer.Query().Where(esportscustomer.OwnerAdminIDEQ(adminID)).IDs(ctx)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []*ent.EsportsGiftOrder{}, nil
		}
		q.Where(esportsgiftorder.CustomerIDIn(ids...))
	}
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

func loadOwnedGiftOrder(ctx context.Context, db *ent.Client, adminID int, isSuper bool, giftOrderID int) (*ent.EsportsGiftOrder, error) {
	if adminID < 1 || giftOrderID < 1 {
		return nil, errInvalidInput
	}
	order, err := db.EsportsGiftOrder.Get(ctx, giftOrderID)
	if err != nil {
		return nil, err
	}
	if !isSuper {
		if _, err := loadOwnedCustomer(ctx, db, adminID, false, order.CustomerID); err != nil {
			return nil, err
		}
	}
	return order, nil
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

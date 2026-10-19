package esports

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsgift"
	"go-admin/internal/ent/esportsgiftintent"
	"go-admin/internal/ent/esportsidentitybinding"
)

type giftIntentInput struct {
	CustomerID int `json:"customer_id"`
	GiftID     int `json:"gift_id"`
	// 价格档位（§9.1-02）：0 表示不分档。
	GiftTierID        int    `json:"gift_tier_id"`
	PlayerID          int    `json:"player_id"`
	Quantity          int    `json:"quantity"`
	OrderID           *int   `json:"order_id"`
	PaymentSource     string `json:"payment_source"`
	StoredValueCardID int    `json:"stored_value_card_id"`
	ClientKey         string `json:"client_key"`
}

// customerGiftIntentInput 是客户自助创建礼物确认单的入参（§9.2）：客户身份由登录态解析。
type customerGiftIntentInput struct {
	GiftID            int    `json:"gift_id"`
	GiftTierID        int    `json:"gift_tier_id"`
	PlayerID          int    `json:"player_id"`
	Quantity          int    `json:"quantity"`
	PaymentSource     string `json:"payment_source"`
	StoredValueCardID int    `json:"stored_value_card_id"`
	ClientKey         string `json:"client_key"`
}

// giftIntentView 是礼物确认单的对外返回结构（§9.2）：对齐 EsportsGiftIntent，不暴露归属客服 ID。
type giftIntentView struct {
	ID                int    `json:"id"`
	CustomerID        int    `json:"customer_id"`
	GiftID            int    `json:"gift_id"`
	GiftName          string `json:"gift_name"`
	GiftVersion       int    `json:"gift_version"`
	PlayerID          int    `json:"player_id"`
	PlayerName        string `json:"player_name"`
	OrderID           *int   `json:"order_id"`
	Quantity          int    `json:"quantity"`
	UnitPriceCents    int64  `json:"unit_price_cents"`
	AmountCents       int64  `json:"amount_cents"`
	PaymentSource     string `json:"payment_source"`
	StoredValueCardID *int   `json:"stored_value_card_id"`
	StoredValueCardNo string `json:"stored_value_card_no"`
	// 价格档位快照（§9.1-02）。
	GiftTierID   int    `json:"gift_tier_id"`
	GiftTierCode string `json:"gift_tier_code"`
	HighValue    bool   `json:"high_value"`
	RequireAge   bool   `json:"require_age"`
	// 收款信息（§9.2-01）。
	PaymentChannel   string     `json:"payment_channel"`
	PaymentStatus    string     `json:"payment_status"`
	PaymentIntentNo  *string    `json:"payment_intent_no"`
	PaymentRef       string     `json:"payment_ref"`
	PaymentExpiresAt *time.Time `json:"payment_expires_at"`
	ClientKey        string     `json:"client_key"`
	Status           string     `json:"status"`
	Version          int        `json:"version"`
	ConfirmationRef  string     `json:"confirmation_ref"`
	GiftOrderID      *int       `json:"gift_order_id"`
	ExpiresAt        time.Time  `json:"expires_at"`
	ConfirmedAt      *time.Time `json:"confirmed_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func toGiftIntentView(item *ent.EsportsGiftIntent) giftIntentView {
	return giftIntentView{
		ID: item.ID, CustomerID: item.CustomerID, GiftID: item.GiftID,
		GiftName: item.GiftName, GiftVersion: item.GiftVersion,
		PlayerID: item.PlayerID, PlayerName: item.PlayerName, OrderID: item.OrderID,
		Quantity: item.Quantity, UnitPriceCents: item.UnitPriceCents, AmountCents: item.AmountCents,
		PaymentSource: item.PaymentSource, StoredValueCardID: item.StoredValueCardID,
		StoredValueCardNo: item.StoredValueCardNo, ClientKey: item.ClientKey,
		GiftTierID: item.GiftTierID, GiftTierCode: item.GiftTierCode,
		HighValue: item.HighValue, RequireAge: item.RequireAge,
		PaymentChannel: item.PaymentChannel.String(), PaymentStatus: item.PaymentStatus.String(),
		PaymentIntentNo: item.PaymentIntentNo, PaymentRef: item.PaymentRef,
		PaymentExpiresAt: item.PaymentExpiresAt,
		Status:           item.Status.String(), Version: item.Version,
		ConfirmationRef: item.ConfirmationRef, GiftOrderID: item.GiftOrderID,
		ExpiresAt: item.ExpiresAt, ConfirmedAt: item.ConfirmedAt,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

// createCustomerGiftIntent 客户自助创建礼物确认单（§9.2 / §9.2-01）：解析登录客户，复用 createGiftIntent
// 的校验与落库。本金充足时直接可送礼；本金不足时**不再直接报本金不足**，而是落成确认单并提示客户
// 走「发起收款」（余额 / 卡本金都不足时的第三种付款来源），收款核验后才成单。
func createCustomerGiftIntent(ctx context.Context, db *ent.Client, uid string, in customerGiftIntentInput, now time.Time) (*ent.EsportsGiftIntent, error) {
	customer, err := customerForVerifiedUser(ctx, db, uid)
	if err != nil {
		return nil, err
	}
	if customer.OwnerAdminID == nil {
		return nil, fmt.Errorf("%w：该客户暂无归属客服，无法自助送礼", errForbidden)
	}
	in.PaymentSource = strings.TrimSpace(in.PaymentSource)
	if in.PaymentSource == "" {
		in.PaymentSource = "customer_balance_principal"
	}
	return createGiftIntent(ctx, db, *customer.OwnerAdminID, false, giftIntentInput{
		CustomerID:        customer.ID,
		GiftID:            in.GiftID,
		GiftTierID:        in.GiftTierID,
		PlayerID:          in.PlayerID,
		Quantity:          in.Quantity,
		PaymentSource:     in.PaymentSource,
		StoredValueCardID: in.StoredValueCardID,
		ClientKey:         in.ClientKey,
	}, now)
}

// customerGiftIntentFunding 判断确认单的本金来源是否充足（§9.2 / §9.2-01）：
// 余额本金与储值卡本金两条链路在发起时都要能足额核销；不足时客户端应改走发起收款。
// 返回 (是否充足, 提示)。
func customerGiftIntentFunding(ctx context.Context, db *ent.Client,
	intent *ent.EsportsGiftIntent) (bool, string, error) {
	if intent == nil {
		return false, "", errInvalidInput
	}
	if intent.PaymentSource != "stored_value_card_principal" {
		balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, intent.CustomerID)
		if err != nil {
			return false, "", err
		}
		if balance.PrincipalCents >= intent.AmountCents {
			return true, "", nil
		}
		return false, "本金余额不足，请充值或发起收款", nil
	}
	if intent.StoredValueCardID == nil {
		return false, "储值卡信息缺失，请重新发起", nil
	}
	balance, err := storedValueCardBalance(ctx, db, *intent.StoredValueCardID)
	if err != nil {
		return false, "", err
	}
	if balance.PrincipalCents >= intent.AmountCents {
		return true, "", nil
	}
	return false, "卡内本金不足，请充值或发起收款", nil
}

func createGiftIntent(ctx context.Context, db *ent.Client, adminID int, isSuper bool, in giftIntentInput, now time.Time) (*ent.EsportsGiftIntent, error) {
	key := strings.TrimSpace(in.ClientKey)
	in.PaymentSource = strings.TrimSpace(in.PaymentSource)
	if in.PaymentSource == "" {
		in.PaymentSource = "customer_balance_principal"
	}
	if adminID < 1 || in.CustomerID < 1 || in.GiftID < 1 || in.PlayerID < 1 || in.Quantity < 1 || in.Quantity > 999 || len(key) < 8 || len(key) > 128 {
		return nil, errInvalidInput
	}
	if (in.PaymentSource != "customer_balance_principal" || in.StoredValueCardID != 0) &&
		(in.PaymentSource != "stored_value_card_principal" || in.StoredValueCardID < 1) {
		return nil, errInvalidInput
	}
	customer, err := loadOwnedCustomer(ctx, db, adminID, isSuper, in.CustomerID)
	if err != nil {
		return nil, err
	}
	prior, err := db.EsportsGiftIntent.Query().Where(esportsgiftintent.CreatedByAdminIDEQ(adminID), esportsgiftintent.ClientKeyEQ(key)).Only(ctx)
	if err == nil {
		if prior.CustomerID == in.CustomerID && prior.GiftID == in.GiftID && prior.PlayerID == in.PlayerID && prior.Quantity == in.Quantity &&
			sameOptionalID(prior.OrderID, in.OrderID) && prior.PaymentSource == in.PaymentSource &&
			(in.StoredValueCardID == 0 && prior.StoredValueCardID == nil || prior.StoredValueCardID != nil && *prior.StoredValueCardID == in.StoredValueCardID) {
			return prior, nil
		}
		return nil, errConflict
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	if customer.UserID == nil || customer.AgeStatus != esportscustomer.AgeStatusAdult {
		return nil, errAgeUnverified
	}
	account, err := db.User.Get(ctx, *customer.UserID)
	if err != nil {
		return nil, errForbidden
	}
	verifiedCustomer, err := customerForVerifiedUser(ctx, db, account.UID)
	if err != nil || verifiedCustomer.ID != customer.ID {
		return nil, errForbidden
	}
	player, err := db.EsportsPlayer.Get(ctx, in.PlayerID)
	if err != nil {
		return nil, err
	}
	if player.UserID == *customer.UserID {
		return nil, errInvalidInput
	}
	gift, err := db.EsportsGift.Get(ctx, in.GiftID)
	if err != nil {
		return nil, err
	}
	if gift.Status != esportsgift.StatusOnSale {
		return nil, errConflict
	}
	terms, err := giftSaleTermsFor(ctx, db, gift, in.GiftTierID)
	if err != nil {
		return nil, err
	}
	if terms.unitPrice > math.MaxInt64/int64(in.Quantity) {
		return nil, errConflict
	}
	if terms.requireAdult && customer.AgeStatus != esportscustomer.AgeStatusAdult {
		return nil, errAgeUnverified
	}
	// 余额 / 卡本金不足时不再直接拒绝：客户可改走「发起收款」（§9.2-01），
	// 真正的资金校验仍在送礼核销时强制执行，不存在伪成功。
	var cardNo string
	if in.StoredValueCardID > 0 {
		card, err := db.EsportsStoredValueCard.Get(ctx, in.StoredValueCardID)
		if err != nil {
			return nil, err
		}
		if err := storedValueCardUsableForGift(card, customer.ID, now); err != nil {
			return nil, err
		}
		cardNo = card.CardNo
	}
	if in.OrderID != nil {
		order, err := db.EsportsOrder.Get(ctx, *in.OrderID)
		if err != nil {
			return nil, err
		}
		if order.CustomerID != customer.ID {
			return nil, errForbidden
		}
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	create := tx.EsportsGiftIntent.Create().SetCustomerID(customer.ID).SetGiftID(gift.ID).
		SetGiftName(gift.Name).SetGiftVersion(gift.Version).SetPlayerID(player.ID).
		SetPlayerName(player.DisplayName).SetQuantity(in.Quantity).
		SetUnitPriceCents(terms.unitPrice).SetAmountCents(terms.unitPrice * int64(in.Quantity)).
		SetPaymentSource(in.PaymentSource).SetStoredValueCardNo(cardNo).
		SetGiftTierID(in.GiftTierID).SetHighValue(terms.highValue).SetRequireAge(terms.requireAge).
		SetCreatedByAdminID(adminID).SetClientKey(key).SetExpiresAt(now.UTC().Add(30 * time.Minute))
	if terms.tier != nil {
		create.SetGiftTierCode(terms.tier.TierCode)
	}
	if in.StoredValueCardID > 0 {
		create.SetStoredValueCardID(in.StoredValueCardID)
	}
	if in.OrderID != nil {
		create.SetOrderID(*in.OrderID)
	}
	intent, err := create.Save(ctx)
	if ent.IsConstraintError(err) {
		return nil, errConflict
	}
	if err != nil {
		return nil, err
	}
	_, err = tx.EsportsOutbox.Create().SetEventKey(fmt.Sprintf("gift.intent_created:%d", intent.ID)).
		SetEventType("gift.intent_created").SetAggregateRef(strconv.Itoa(intent.ID)).
		SetPayload(map[string]interface{}{"intent_id": intent.ID, "customer_id": customer.ID, "amount_cents": intent.AmountCents}).Save(ctx)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsGiftIntent.Get(ctx, intent.ID)
}

func sameOptionalID(a, b *int) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func listAdminGiftIntents(ctx context.Context, db *ent.Client, adminID int, isSuper bool) ([]*ent.EsportsGiftIntent, error) {
	q := db.EsportsGiftIntent.Query().Order(ent.Desc(esportsgiftintent.FieldID)).Limit(200)
	if !isSuper {
		ids, err := db.EsportsCustomer.Query().Where(esportscustomer.OwnerAdminIDEQ(adminID)).IDs(ctx)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []*ent.EsportsGiftIntent{}, nil
		}
		q.Where(esportsgiftintent.CustomerIDIn(ids...))
	}
	return q.All(ctx)
}

func listCustomerGiftIntents(ctx context.Context, db *ent.Client, uid string) ([]*ent.EsportsGiftIntent, error) {
	customer, err := customerForVerifiedUser(ctx, db, uid)
	if err != nil {
		return nil, err
	}
	return db.EsportsGiftIntent.Query().Where(esportsgiftintent.CustomerIDEQ(customer.ID)).
		Order(ent.Desc(esportsgiftintent.FieldID)).Limit(100).All(ctx)
}

func confirmGiftIntent(ctx context.Context, db *ent.Client, uid string, intentID, expectedVersion int, accepted bool, now time.Time) (*ent.EsportsGiftIntent, error) {
	if intentID < 1 || expectedVersion < 1 || !accepted {
		return nil, errInvalidInput
	}
	customer, err := customerForVerifiedUser(ctx, db, uid)
	if err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	intent, err := tx.EsportsGiftIntent.Get(ctx, intentID)
	if err != nil {
		return nil, err
	}
	// 年龄限制按确认单上的档位快照生效（§9.1-02）：require_age=false 的档位不拦未成年客户。
	if customer.AgeStatus != esportscustomer.AgeStatusAdult && intent.RequireAge {
		return nil, errAgeUnverified
	}
	if intent.CustomerID != customer.ID {
		return nil, errForbidden
	}
	if intent.Status == esportsgiftintent.StatusConfirmed && intent.Version == expectedVersion+1 {
		return intent, nil
	}
	if intent.Status != esportsgiftintent.StatusPending || intent.Version != expectedVersion || !now.UTC().Before(intent.ExpiresAt) {
		return nil, errConflict
	}
	binding, err := tx.EsportsIdentityBinding.Query().Where(
		esportsidentitybinding.CustomerIDEQ(customer.ID),
		esportsidentitybinding.ProviderEQ("user"),
		esportsidentitybinding.ExternalSubjectEQ(uid),
		esportsidentitybinding.StatusEQ(esportsidentitybinding.StatusActive),
	).Only(ctx)
	if err != nil {
		return nil, errForbidden
	}
	ref := "user_binding:" + strconv.Itoa(binding.ID) + ":action:" + uuid.NewString()
	changed, err := tx.EsportsGiftIntent.Update().Where(esportsgiftintent.IDEQ(intent.ID),
		esportsgiftintent.VersionEQ(expectedVersion), esportsgiftintent.StatusEQ(esportsgiftintent.StatusPending)).
		SetStatus(esportsgiftintent.StatusConfirmed).SetConfirmationRef(ref).SetConfirmedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey(fmt.Sprintf("gift.intent_confirmed:%d", intent.ID)).
		SetEventType("gift.intent_confirmed").SetAggregateRef(strconv.Itoa(intent.ID)).
		SetPayload(map[string]interface{}{"intent_id": intent.ID, "customer_id": customer.ID}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsGiftIntent.Get(ctx, intent.ID)
}

func executeGiftIntent(ctx context.Context, db *ent.Client, adminID int, isSuper bool, intentID int, now time.Time) (*ent.EsportsGiftOrder, error) {
	if intentID < 1 {
		return nil, errInvalidInput
	}
	intent, err := db.EsportsGiftIntent.Get(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if _, err := loadOwnedCustomer(ctx, db, adminID, isSuper, intent.CustomerID); err != nil {
		return nil, err
	}
	if intent.Status == esportsgiftintent.StatusConsumed && intent.GiftOrderID != nil {
		return db.EsportsGiftOrder.Get(ctx, *intent.GiftOrderID)
	}
	if intent.Status != esportsgiftintent.StatusConfirmed || !now.UTC().Before(intent.ExpiresAt) {
		return nil, errConflict
	}
	paymentSource := intent.PaymentSource
	// 已发起外部收款的确认单：钱从渠道来，核销时按外部收款来源成单（不再动余额 / 卡本金）。
	if validGiftPaymentChannel(intent.PaymentChannel.String()) {
		paymentSource = giftPaymentSourceExternal
	}
	in := giftSendInput{CustomerID: intent.CustomerID, GiftID: intent.GiftID,
		GiftTierID: intent.GiftTierID,
		PlayerID:   intent.PlayerID, Quantity: intent.Quantity, OrderID: intent.OrderID,
		PaymentSource: paymentSource,
		Confirmed:     true, AuthorizationID: intent.ID, ClientKey: fmt.Sprintf("gift-intent:%d", intent.ID)}
	if intent.StoredValueCardID != nil {
		in.StoredValueCardID = *intent.StoredValueCardID
	}
	order, err := sendGiftWithRole(ctx, db, adminID, isSuper, in, now)
	if errors.Is(err, errConflict) {
		latest, lookupErr := db.EsportsGiftIntent.Get(ctx, intentID)
		if lookupErr == nil && latest.Status == esportsgiftintent.StatusConsumed && latest.GiftOrderID != nil {
			return db.EsportsGiftOrder.Get(ctx, *latest.GiftOrderID)
		}
	}
	return order, err
}

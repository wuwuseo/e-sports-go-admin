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
	CustomerID        int    `json:"customer_id"`
	GiftID            int    `json:"gift_id"`
	PlayerID          int    `json:"player_id"`
	Quantity          int    `json:"quantity"`
	OrderID           *int   `json:"order_id"`
	PaymentSource     string `json:"payment_source"`
	StoredValueCardID int    `json:"stored_value_card_id"`
	ClientKey         string `json:"client_key"`
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
	if gift.Status != esportsgift.StatusOnSale || gift.PriceCents > math.MaxInt64/int64(in.Quantity) {
		return nil, errConflict
	}
	var cardNo string
	if in.StoredValueCardID > 0 {
		card, err := db.EsportsStoredValueCard.Get(ctx, in.StoredValueCardID)
		if err != nil {
			return nil, err
		}
		if err := storedValueCardUsableForGift(card, customer.ID, now); err != nil {
			return nil, err
		}
		balance, err := storedValueCardBalance(ctx, db, card.ID)
		if err != nil {
			return nil, err
		}
		if balance.PrincipalCents < gift.PriceCents*int64(in.Quantity) {
			return nil, errInsufficientBalance
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
		SetUnitPriceCents(gift.PriceCents).SetAmountCents(gift.PriceCents * int64(in.Quantity)).
		SetPaymentSource(in.PaymentSource).SetStoredValueCardNo(cardNo).
		SetCreatedByAdminID(adminID).SetClientKey(key).SetExpiresAt(now.UTC().Add(30 * time.Minute))
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
	if customer.AgeStatus != esportscustomer.AgeStatusAdult {
		return nil, errAgeUnverified
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
	in := giftSendInput{CustomerID: intent.CustomerID, GiftID: intent.GiftID,
		PlayerID: intent.PlayerID, Quantity: intent.Quantity, OrderID: intent.OrderID,
		PaymentSource: intent.PaymentSource,
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

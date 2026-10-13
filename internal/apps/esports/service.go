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

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsidempotency"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsproductcategory"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/systemconfig"
)

var (
	errInvalidInput  = errors.New("请求参数无效")
	errConflict      = errors.New("业务状态已变化，请刷新后重试")
	errForbidden     = errors.New("无权访问该客户或订单")
	errAgeUnverified = errors.New("客户年龄资格尚未核验或不符合购买要求")
	errNotConfigured = errors.New("支付时限尚未由业务方配置")
)

type skuInput struct {
	Code            string `json:"code"`
	Title           string `json:"title"`
	ServiceType     string `json:"service_type"`
	CategoryID      *int   `json:"category_id"`
	Description     string `json:"description"`
	UnitPriceCents  int64  `json:"unit_price_cents"`
	ServiceUnits    int64  `json:"service_units"`
	ServiceUnitName string `json:"service_unit_name"`
	RequiredSeats   int    `json:"required_seats"`
	QuotaTotal      int64  `json:"quota_total"`
	SortOrder       int    `json:"sort_order"`
	Status          string `json:"status"`
}

type categoryInput struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Game      string `json:"game"`
	SortOrder int    `json:"sort_order"`
	Status    string `json:"status"`
}

type skuUpdateInput struct {
	Title           string `json:"title"`
	ServiceType     string `json:"service_type"`
	CategoryID      *int   `json:"category_id"`
	Description     string `json:"description"`
	UnitPriceCents  int64  `json:"unit_price_cents"`
	ServiceUnits    int64  `json:"service_units"`
	ServiceUnitName string `json:"service_unit_name"`
	RequiredSeats   int    `json:"required_seats"`
	QuotaTotal      int64  `json:"quota_total"`
	SortOrder       int    `json:"sort_order"`
	Status          string `json:"status"`
}

type customerInput struct {
	DisplayName string `json:"display_name"`
}

type orderInput struct {
	CustomerID int   `json:"customer_id"`
	SkuID      int   `json:"sku_id"`
	Quantity   int64 `json:"quantity"`
	// 客户指定陪玩（§5.4）：可选。omitempty 让不传该字段时的幂等哈希与既有口径保持一致。
	PreferredPlayerID int `json:"preferred_player_id,omitempty"`
}

func currentAdmin(ctx context.Context, db *ent.Client, uid string) (*ent.Admin, error) {
	if uid == "" {
		return nil, errForbidden
	}
	return db.Admin.Query().Where(admin.UIDEQ(uid), admin.StatusEQ("active")).Only(ctx)
}

func saveSku(ctx context.Context, db *ent.Client, in skuInput) (*ent.EsportsSku, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Title = strings.TrimSpace(in.Title)
	in.ServiceUnitName = strings.TrimSpace(in.ServiceUnitName)
	in.Description = strings.TrimSpace(in.Description)
	if in.Code == "" || len(in.Code) > 80 || in.Title == "" || len([]rune(in.Title)) > 160 ||
		in.ServiceUnitName == "" || len([]rune(in.Description)) > 500 ||
		in.UnitPriceCents <= 0 || in.ServiceUnits <= 0 ||
		in.RequiredSeats < 1 || in.QuotaTotal < -1 || (in.QuotaTotal != -1 && in.QuotaTotal < int64(in.RequiredSeats)) ||
		(in.ServiceType != "play" && in.ServiceType != "teaching" && in.ServiceType != "entertainment") ||
		(in.Status != "draft" && in.Status != "on_sale") {
		return nil, errInvalidInput
	}
	if in.CategoryID != nil {
		exists, err := db.EsportsProductCategory.Query().Where(
			esportsproductcategory.IDEQ(*in.CategoryID),
			esportsproductcategory.StatusEQ(esportsproductcategory.StatusEnabled),
		).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, errInvalidInput
		}
	}
	create := db.EsportsSku.Create().SetCode(in.Code).SetTitle(in.Title).
		SetServiceType(esportssku.ServiceType(in.ServiceType)).SetDescription(in.Description).
		SetUnitPriceCents(in.UnitPriceCents).
		SetServiceUnits(in.ServiceUnits).SetServiceUnitName(in.ServiceUnitName).
		SetRequiredSeats(in.RequiredSeats).SetQuotaTotal(in.QuotaTotal).
		SetSortOrder(in.SortOrder).SetStatus(esportssku.Status(in.Status))
	if in.CategoryID != nil {
		create.SetCategoryID(*in.CategoryID)
	}
	return create.Save(ctx)
}

func saveCustomer(ctx context.Context, db *ent.Client, adminID int, in customerInput) (*ent.EsportsCustomer, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if in.DisplayName == "" || len([]rune(in.DisplayName)) > 120 {
		return nil, errInvalidInput
	}
	return db.EsportsCustomer.Create().SetDisplayName(in.DisplayName).SetOwnerAdminID(adminID).Save(ctx)
}

// 分类编码创建后不可变，更新时不校验编码。
func validateCategoryFields(in categoryInput) (categoryInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Game = strings.TrimSpace(in.Game)
	if in.Name == "" || len([]rune(in.Name)) > 120 ||
		len([]rune(in.Game)) > 120 || (in.Status != "enabled" && in.Status != "disabled") {
		return in, errInvalidInput
	}
	return in, nil
}

func saveCategory(ctx context.Context, db *ent.Client, in categoryInput) (*ent.EsportsProductCategory, error) {
	in.Code = strings.TrimSpace(in.Code)
	in, err := validateCategoryFields(in)
	if err != nil || in.Code == "" || len(in.Code) > 80 {
		return nil, errInvalidInput
	}
	return db.EsportsProductCategory.Create().SetCode(in.Code).SetName(in.Name).
		SetGame(in.Game).SetSortOrder(in.SortOrder).
		SetStatus(esportsproductcategory.Status(in.Status)).Save(ctx)
}

func updateCategory(ctx context.Context, db *ent.Client, id int, in categoryInput) (*ent.EsportsProductCategory, error) {
	in, err := validateCategoryFields(in)
	if err != nil {
		return nil, err
	}
	changed, err := db.EsportsProductCategory.Update().Where(esportsproductcategory.IDEQ(id)).
		SetName(in.Name).SetGame(in.Game).SetSortOrder(in.SortOrder).
		SetStatus(esportsproductcategory.Status(in.Status)).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, &ent.NotFoundError{}
	}
	return db.EsportsProductCategory.Get(ctx, id)
}

// 分类被任一商品引用时禁止删除，避免商品失去归属。
func deleteCategory(ctx context.Context, db *ent.Client, id int) error {
	used, err := db.EsportsSku.Query().Where(esportssku.CategoryIDEQ(id)).Exist(ctx)
	if err != nil {
		return err
	}
	if used {
		return errConflict
	}
	changed, err := db.EsportsProductCategory.Delete().Where(esportsproductcategory.IDEQ(id)).Exec(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return &ent.NotFoundError{}
	}
	return nil
}

// updateSku 编辑商品。历史订单已快照价格与规格，改价只对新订单生效。
// ponytail: 变更操作人/时间依赖管理端操作日志中间件，未再建独立审计表。
func updateSku(ctx context.Context, db *ent.Client, id int, in skuUpdateInput) (*ent.EsportsSku, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.ServiceUnitName = strings.TrimSpace(in.ServiceUnitName)
	in.Description = strings.TrimSpace(in.Description)
	if in.Title == "" || len([]rune(in.Title)) > 160 || in.ServiceUnitName == "" ||
		len([]rune(in.ServiceUnitName)) > 32 || len([]rune(in.Description)) > 500 ||
		in.UnitPriceCents <= 0 || in.ServiceUnits <= 0 || in.RequiredSeats < 1 ||
		in.QuotaTotal < -1 ||
		(in.ServiceType != "play" && in.ServiceType != "teaching" && in.ServiceType != "entertainment") ||
		(in.Status != "draft" && in.Status != "on_sale" && in.Status != "off_sale") {
		return nil, errInvalidInput
	}
	if in.QuotaTotal != -1 && in.QuotaTotal < int64(in.RequiredSeats) {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := tx.EsportsSku.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// 限量名额不得调到已占用/已售数量以下。
	if in.QuotaTotal != -1 && in.QuotaTotal < current.QuotaReserved+current.QuotaSold {
		return nil, errConflict
	}
	if in.CategoryID != nil {
		exists, err := tx.EsportsProductCategory.Query().Where(
			esportsproductcategory.IDEQ(*in.CategoryID),
			esportsproductcategory.StatusEQ(esportsproductcategory.StatusEnabled),
		).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, errInvalidInput
		}
	}
	update := tx.EsportsSku.UpdateOne(current).SetTitle(in.Title).
		SetServiceType(esportssku.ServiceType(in.ServiceType)).SetDescription(in.Description).
		SetUnitPriceCents(in.UnitPriceCents).SetServiceUnits(in.ServiceUnits).
		SetServiceUnitName(in.ServiceUnitName).SetRequiredSeats(in.RequiredSeats).
		SetQuotaTotal(in.QuotaTotal).SetSortOrder(in.SortOrder).
		SetStatus(esportssku.Status(in.Status)).AddVersion(1)
	if in.CategoryID == nil {
		update.ClearCategoryID()
	} else {
		update.SetCategoryID(*in.CategoryID)
	}
	if _, err = update.Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsSku.Get(ctx, id)
}

func paymentDeadlineMinutes(ctx context.Context, db *ent.Client) (int, error) {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ("app.esports.payment_deadline_minutes")).Only(ctx)
	if ent.IsNotFound(err) {
		return 0, errNotConfigured
	}
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.Atoi(item.Value)
	if err != nil || minutes < 1 || minutes > 24*60 {
		return 0, errNotConfigured
	}
	return minutes, nil
}

func orderRequestHash(in orderInput) string {
	data, _ := json.Marshal(in)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func existingOrder(ctx context.Context, db *ent.Client, principal, key, hash string) (*ent.EsportsOrder, error) {
	record, err := db.EsportsIdempotency.Query().Where(
		esportsidempotency.OperationEQ("create_order"),
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
	return db.EsportsOrder.Query().Where(esportsorder.OrderNoEQ(record.ResultRef)).Only(ctx)
}

// placeOrder is an instant-order slice. Appointment and external payment entry
// points remain disabled until their separate source-feature workflows are built.
func placeOrder(ctx context.Context, db *ent.Client, adminID int, in orderInput, key string, now time.Time) (*ent.EsportsOrder, error) {
	order, err := placeOrderOnce(ctx, db, adminID, in, key, now)
	if errors.Is(err, errConflict) {
		prior, lookupErr := existingOrder(ctx, db, fmt.Sprintf("admin:%d", adminID), key, orderRequestHash(in))
		if lookupErr == nil && prior != nil {
			return prior, nil
		}
	}
	return order, err
}

func placeOrderOnce(ctx context.Context, db *ent.Client, adminID int, in orderInput, key string, now time.Time) (*ent.EsportsOrder, error) {
	if in.CustomerID < 1 || in.SkuID < 1 || in.Quantity < 1 || in.Quantity > 1000 || len(key) < 8 || len(key) > 128 {
		return nil, errInvalidInput
	}
	principal := fmt.Sprintf("admin:%d", adminID)
	hash := orderRequestHash(in)
	if previous, err := existingOrder(ctx, db, principal, key, hash); err != nil || previous != nil {
		return previous, err
	}
	minutes, err := paymentDeadlineMinutes(ctx, db)
	if err != nil {
		return nil, err
	}
	deadline := now.UTC().Add(time.Duration(minutes) * time.Minute)
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	customer, err := tx.EsportsCustomer.Get(ctx, in.CustomerID)
	if err != nil {
		return nil, err
	}
	if customer.OwnerAdminID == nil || *customer.OwnerAdminID != adminID {
		return nil, errForbidden
	}
	if customer.AgeStatus != esportscustomer.AgeStatusAdult {
		return nil, errAgeUnverified
	}
	sku, err := tx.EsportsSku.Get(ctx, in.SkuID)
	if err != nil {
		return nil, err
	}
	if sku.Status != esportssku.StatusOnSale || sku.UnitPriceCents > math.MaxInt64/in.Quantity ||
		sku.ServiceUnits > math.MaxInt64/in.Quantity {
		return nil, errConflict
	}
	// A conditional UPDATE, rather than a prior SELECT alone, wins the final unit.
	updated, err := tx.EsportsSku.Update().Where(
		esportssku.IDEQ(sku.ID), esportssku.VersionEQ(sku.Version),
		esportssku.StatusEQ(esportssku.StatusOnSale),
		func(s *sql.Selector) {
			s.Where(sql.Or(sql.EQ(esportssku.FieldQuotaTotal, -1),
				sql.ExprP("quota_total - quota_reserved - quota_sold >= ?", in.Quantity)))
		},
	).AddQuotaReserved(in.Quantity).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if updated != 1 {
		return nil, errConflict
	}
	// 优惠顺序（§10.4）：商品规格价 → 会员折扣 → 经授权人工调整；下单只落规格价与会员折扣，
	// 人工调整在改价时写入 manual_adjust_cents。
	subtotal := sku.UnitPriceCents * in.Quantity
	discountBps, err := memberDiscountBps(ctx, tx.Client(), customer.MemberLevelCode)
	if err != nil {
		return nil, err
	}
	discount := memberDiscountCents(subtotal, discountBps)
	payable := subtotal - discount
	if payable <= 0 {
		// §4.6：普通服务单折后应付为零时拦截。
		return nil, fmt.Errorf("%w：该订单会员折扣后应付金额为零，无法报单", errInvalidInput)
	}
	// §4.6 报单校验：指定陪玩须存在且资质合格（未通过考核或已停用不允许选择）。
	if in.PreferredPlayerID > 0 {
		player, playerErr := tx.EsportsPlayer.Get(ctx, in.PreferredPlayerID)
		if playerErr != nil {
			return nil, playerErr
		}
		if player.Qualification != esportsplayer.QualificationQualified {
			return nil, fmt.Errorf("%w：指定陪玩未通过考核或已停用，不可指定", errInvalidInput)
		}
	}
	orderNo := uuid.NewString()
	creator := tx.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(customer.ID).
		SetSkuID(sku.ID).SetCreatedByAdminID(adminID).SetResponsibleAdminID(adminID).
		SetQuantity(in.Quantity).SetUnitPriceCents(sku.UnitPriceCents).
		SetDiscountCents(discount).SetPayableCents(payable).
		SetPromisedServiceUnits(sku.ServiceUnits * in.Quantity).
		SetRequiredSeats(sku.RequiredSeats).SetRequiredLevel(esportsorder.RequiredLevel(sku.RequiredLevel)).
		SetPaymentDeadline(deadline)
	if in.PreferredPlayerID > 0 {
		creator = creator.SetPreferredPlayerID(in.PreferredPlayerID)
	}
	order, err := creator.Save(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsQuotaHold.Create().SetOrderID(order.ID).SetSkuID(sku.ID).
		SetQuantity(in.Quantity).SetStatus("reserved").SetExpiresAt(deadline).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).SetOrderVersion(1).
		SetEventType("order.created").SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{"sku_code": sku.Code, "quantity": in.Quantity}).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsIdempotency.Create().SetOperation("create_order").SetPrincipal(principal).
		SetRequestKey(key).SetRequestHash(hash).SetResultRef(orderNo).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("order.created:" + orderNo).
		SetEventType("order.created").SetAggregateRef(orderNo).
		SetPayload(map[string]interface{}{"order_no": orderNo}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return order, nil
}

// expireOrder releases an unpaid order's SKU units in the same transaction as
// its state transition. A payment callback must use the same version predicate.
func expireOrder(ctx context.Context, db *ent.Client, orderID int, now time.Time) (bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return false, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid || order.PaymentDeadline.After(now.UTC()) {
		return false, nil
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
		esportsorder.PaymentDeadlineLTE(now.UTC()),
	).SetFulfillmentStatus(esportsorder.FulfillmentStatusCancelled).AddVersion(1).Save(ctx)
	if err != nil {
		return false, err
	}
	if changed != 1 {
		return false, errConflict
	}
	if err = releaseReservedQuota(ctx, tx, orderID); err != nil {
		return false, err
	}
	if _, err = tx.EsportsPaymentIntent.Update().Where(
		esportspaymentintent.OrderIDEQ(orderID),
		esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
	).SetStatus(esportspaymentintent.StatusExpired).Save(ctx); err != nil {
		return false, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.payment_expired").
		SetActorType("system").SetActorID("expiry").Save(ctx); err != nil {
		return false, err
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("order.payment_expired:" + order.OrderNo).
		SetEventType("order.payment_expired").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{"order_no": order.OrderNo}).Save(ctx); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func expireDueOrders(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errInvalidInput
	}
	ids, err := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
		esportsorder.PaymentDeadlineLTE(now.UTC()),
	).Order(ent.Asc(esportsorder.FieldPaymentDeadline)).Limit(limit).IDs(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		expired, err := expireOrder(ctx, db, id, now)
		if errors.Is(err, errConflict) {
			continue // a payment or another worker won the version race
		}
		if err != nil {
			return count, err
		}
		if expired {
			count++
		}
	}
	return count, nil
}

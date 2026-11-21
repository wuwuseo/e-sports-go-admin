package esports

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscheckout"
	"go-admin/internal/ent/esportscheckoutattempt"
	"go-admin/internal/ent/esportsgiftintent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportspaymentreceipt"
	"go-admin/internal/ent/systemconfig"
)

// 订单 / 充值 / 礼物聚合收款：checkout 入口与支付尝试生命周期。
//
// 业务口径（用户确认，必须遵守）：
//  1. 一个业务单（订单 / 充值意图 / 礼物确认单）只有一条 checkout；重复创建按幂等返回；
//  2. 动态支付与固定商家码在同一业务单上互斥：
//     - 动态尝试有效（creating / pending）时拒绝提交固定码凭证；
//     - 已有待核验凭证 / 礼物其他收款进行中时拒绝新建动态尝试；
//  3. 一个业务单同一时刻只允许一条有效支付尝试（active_dedup_key 唯一索引兜底）；
//     超时后可在业务时限内换新商户单号重试；已受理（pending）的尝试不静默改投另一家服务商，
//     只有服务商受理前（创建失败）才按 priority 切到下一家；
//  4. 只有服务商验签回调或服务端查单确认成功才进入结算：
//     - 订单 → applyVerifiedPayment（payment.go 唯一落账边界）
//     - 充值 → settleRechargeIntent / creditRechargeIntent（recharge_intent.go）
//     - 礼物 → settleGiftPayment（gift_payment.go 现有收款核销路径）
//  5. 金额不符 / 渠道不符 / 业务已不可收款等异常到账一律记 checkout.payment_exception 外盒事件
//     并把尝试置 failed（清空有效键），绝不重复入账或送礼，交由财务按异常事件人工处理。

const (
	checkoutBusinessTypeOrder    = "order"
	checkoutBusinessTypeRecharge = "recharge"
	checkoutBusinessTypeGift     = "gift"

	checkoutModeAggregate = "aggregate"
	checkoutModeManual    = "manual"

	// checkoutAttemptWindowKey 单次支付尝试的有效期（分钟）：缺省 5 分钟，上限 60 分钟。
	// 服务商预下单二维码自身有效期通常更短，超时后客户端需重新创建尝试（新商户单号）。
	checkoutAttemptWindowKey            = "app.esports.checkout_attempt_window_minutes"
	checkoutAttemptWindowDefaultMinutes = 5
	checkoutAttemptWindowMaxMinutes     = 60

	// checkoutEntryTokenBytes 入口令牌随机字节数（十六进制后 48 字符）。
	checkoutEntryTokenBytes = 24
)

// checkoutCreateInput 创建 / 复用聚合收款入口。
type checkoutCreateInput struct {
	BusinessType string `json:"business_type"`
	BusinessID   int    `json:"business_id"`
	Mode         string `json:"mode"`
	// AccountID 可选：固定码模式先校验所选收款账户可用（启用 + 财务审核通过 + 渠道未停用），
	// 不落 checkout（凭证提交时仍以 account_id 为准复核）。
	AccountID int `json:"account_id"`
}

// checkoutView 管理 / 客户侧入口视图。
type checkoutView struct {
	ID                int                  `json:"id"`
	BusinessType      string               `json:"business_type"`
	BusinessID        int                  `json:"business_id"`
	AmountCents       int64                `json:"amount_cents"`
	ChannelMode       string               `json:"channel_mode"`
	Status            string               `json:"status"`
	EntryToken        string               `json:"entry_token"`
	BusinessExpiresAt time.Time            `json:"business_expires_at"`
	Attempt           *checkoutAttemptView `json:"attempt,omitempty"`
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`
}

// checkoutAttemptView 支付尝试视图：不返回商户单号之外的服务商内部信息。
type checkoutAttemptView struct {
	AttemptID  int       `json:"attempt_id"`
	Provider   string    `json:"provider"`
	PayChannel string    `json:"pay_channel"`
	Status     string    `json:"status"`
	PayURL     string    `json:"pay_url,omitempty"`
	QRPayload  string    `json:"qr_payload,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// publicCheckoutView 扫码页最小信息：不泄露业务单号 / 客户 / 服务商配置。
type publicCheckoutView struct {
	BusinessType      string                 `json:"business_type"`
	AmountCents       int64                  `json:"amount_cents"`
	Status            string                 `json:"status"`
	BusinessExpiresAt time.Time              `json:"business_expires_at"`
	Channels          publicCheckoutChannels `json:"channels"`
}

type publicCheckoutChannels struct {
	Wechat bool `json:"wechat"`
	Alipay bool `json:"alipay"`
}

// checkoutStatusView 扫码页轮询结果。
type checkoutStatusView struct {
	Status string `json:"status"`
	Paid   bool   `json:"paid"`
}

// checkoutPaymentMethodsView 带业务上下文的收款方式视图（GET /user/payment-methods?business_type=&business_id=）。
type checkoutPaymentMethodsView struct {
	Methods       []customerPaymentMethodView   `json:"methods"`
	ManualMethods []customerPaymentMethodView   `json:"manual_methods"`
	Aggregate     checkoutAggregateAvailability `json:"aggregate"`
}

// checkoutAggregateAvailability 动态聚合支付可用性：不可用时给出中文原因。
type checkoutAggregateAvailability struct {
	Available    bool            `json:"available"`
	Providers    []string        `json:"providers"`
	Channels     map[string]bool `json:"channels"`
	Reason       string          `json:"reason,omitempty"`
	CheckoutID   int             `json:"checkout_id,omitempty"`
	CheckoutMode string          `json:"checkout_mode,omitempty"`
}

// checkoutBusinessSnapshot 业务单在创建入口时的金额 / 时限快照。
type checkoutBusinessSnapshot struct {
	BusinessType string
	BusinessID   int
	CustomerID   int
	AmountCents  int64
	ExpiresAt    time.Time
}

func checkoutBusinessTypeValid(value string) bool {
	switch value {
	case checkoutBusinessTypeOrder, checkoutBusinessTypeRecharge, checkoutBusinessTypeGift:
		return true
	}
	return false
}

func checkoutModeValid(value string) bool {
	return value == checkoutModeAggregate || value == checkoutModeManual
}

// checkoutSettlementChannel 动态支付入账时写入支付意图 / 收据的渠道编码：
// 按实际支付渠道细分为 wechat_merchant_qr / alipay_merchant_qr，既有订单渠道枚举会映射回 merchant_qr。
func checkoutSettlementChannel(payChannel string) string {
	if payChannel == "alipay" {
		return "alipay_merchant_qr"
	}
	return "wechat_merchant_qr"
}

// aggregateCheckoutChannel 聚合动态支付可用的收款渠道（充值意图发起时选择的渠道必须落在其中，
// 否则充值意图的支付意图渠道与实际到账渠道无法对齐）。
func aggregateCheckoutChannel(channel string) bool {
	switch channel {
	case "merchant_qr", "wechat_merchant_qr", "alipay_merchant_qr":
		return true
	}
	return false
}

// checkoutAttemptWindow 支付尝试有效期：配置缺失 / 非法回落 5 分钟。
func checkoutAttemptWindow(ctx context.Context, db *ent.Client) time.Duration {
	fallback := time.Duration(checkoutAttemptWindowDefaultMinutes) * time.Minute
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(checkoutAttemptWindowKey)).Only(ctx)
	if err != nil {
		return fallback
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || minutes < 1 || minutes > checkoutAttemptWindowMaxMinutes {
		return fallback
	}
	return time.Duration(minutes) * time.Minute
}

// newCheckoutEntryToken 生成入口令牌（crypto/rand）。令牌唯一由数据库唯一索引兜底。
func newCheckoutEntryToken() (string, error) {
	raw := make([]byte, checkoutEntryTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// checkoutDedupKey 有效支付尝试的唯一键：一个 checkout 同时只允许一条。
func checkoutDedupKey(checkoutID int) string { return "checkout:" + strconv.Itoa(checkoutID) }

// loadCheckoutBusinessSnapshot 校验业务存在、归属该客户，并返回金额与业务时限快照。
func loadCheckoutBusinessSnapshot(ctx context.Context, db *ent.Client, customerID int,
	businessType string, businessID int, now time.Time) (*checkoutBusinessSnapshot, error) {
	if customerID < 1 || businessID < 1 || !checkoutBusinessTypeValid(businessType) {
		return nil, errInvalidInput
	}
	snapshot := &checkoutBusinessSnapshot{
		BusinessType: businessType, BusinessID: businessID, CustomerID: customerID,
	}
	switch businessType {
	case checkoutBusinessTypeOrder:
		order, err := db.EsportsOrder.Get(ctx, businessID)
		if err != nil {
			return nil, err
		}
		if order.CustomerID != customerID {
			return nil, errForbidden
		}
		if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
			order.PaymentStatus != esportsorder.PaymentStatusUnpaid {
			return nil, fmt.Errorf("%w：订单当前不是待付款状态", errConflict)
		}
		if err = ensurePaymentDeadlineConfigured(ctx, db); err != nil {
			return nil, err
		}
		if err = ensureOrderSkuOnSale(ctx, db, order); err != nil {
			return nil, err
		}
		if err = ensureOrderPerSeatPromise(order); err != nil {
			return nil, err
		}
		if !now.UTC().Before(order.PaymentDeadline) {
			return nil, fmt.Errorf("%w：订单已超过支付时限", errConflict)
		}
		snapshot.AmountCents = order.PayableCents
		snapshot.ExpiresAt = order.PaymentDeadline
	case checkoutBusinessTypeRecharge:
		recharge, err := db.EsportsRechargeIntent.Get(ctx, businessID)
		if err != nil {
			return nil, err
		}
		if recharge.CustomerID != customerID {
			return nil, errForbidden
		}
		intent, err := db.EsportsPaymentIntent.Query().Where(
			esportspaymentintent.IntentNoEQ(recharge.IntentNo)).Only(ctx)
		if err != nil {
			return nil, err
		}
		if intent.Status != esportspaymentintent.StatusPending {
			return nil, fmt.Errorf("%w：充值支付意图已不是待支付状态", errConflict)
		}
		if intent.AmountCents != recharge.PrincipalCents {
			return nil, fmt.Errorf("%w：充值支付意图金额与充值本金不一致", errConflict)
		}
		if !now.UTC().Before(intent.ExpiresAt) {
			return nil, fmt.Errorf("%w：充值支付意图已过期，请重新发起充值", errConflict)
		}
		snapshot.AmountCents = recharge.PrincipalCents
		snapshot.ExpiresAt = intent.ExpiresAt
	case checkoutBusinessTypeGift:
		gift, err := db.EsportsGiftIntent.Get(ctx, businessID)
		if err != nil {
			return nil, err
		}
		if gift.CustomerID != customerID {
			return nil, errForbidden
		}
		if gift.Status != esportsgiftintent.StatusConfirmed {
			return nil, fmt.Errorf("%w：礼物确认单未确认或已失效，不能发起收款", errConflict)
		}
		if gift.PaymentStatus == esportsgiftintent.PaymentStatusVerified {
			return nil, fmt.Errorf("%w：礼物收款已到账", errConflict)
		}
		if !now.UTC().Before(gift.ExpiresAt) {
			return nil, fmt.Errorf("%w：礼物确认单已过期，请重新发起", errConflict)
		}
		snapshot.AmountCents = gift.AmountCents
		snapshot.ExpiresAt = gift.ExpiresAt
	}
	return snapshot, nil
}

// createOrReuseCheckout 创建 / 复用业务单的聚合收款入口（幂等）。
func createOrReuseCheckout(ctx context.Context, db *ent.Client, customer *ent.EsportsCustomer,
	in checkoutCreateInput, now time.Time) (*ent.EsportsCheckout, error) {
	in.BusinessType = strings.TrimSpace(in.BusinessType)
	in.Mode = strings.TrimSpace(in.Mode)
	if customer == nil || customer.ID < 1 || in.BusinessID < 1 ||
		!checkoutBusinessTypeValid(in.BusinessType) || !checkoutModeValid(in.Mode) {
		return nil, errInvalidInput
	}
	snapshot, err := loadCheckoutBusinessSnapshot(ctx, db, customer.ID, in.BusinessType,
		in.BusinessID, now)
	if err != nil {
		return nil, err
	}
	// 固定码模式先校验所选账户：账户必须启用且渠道未停用；
	// 动态模式不接受 account_id（避免客户端误把聚合支付当固定码转账）。
	if in.Mode == checkoutModeManual && in.AccountID > 0 {
		account, err := db.EsportsPaymentAccount.Get(ctx, in.AccountID)
		if err != nil {
			return nil, err
		}
		if !paymentChannelEnabled(ctx, db, account.Channel.String()) {
			return nil, fmt.Errorf("%w：收款方式已停用", errForbidden)
		}
		if _, err = requireActivePaymentAccount(ctx, db, account.ID, account.Channel.String()); err != nil {
			return nil, err
		}
	}
	if in.Mode == checkoutModeAggregate && in.BusinessType == checkoutBusinessTypeRecharge {
		// 聚合模式是对「尚未受理的充值支付意图」的显式切换：充值意图与活动快照已锁定，
		// 只把支付意图渠道切到聚合商户码，避免客户先前选的企微 / 对公渠道与动态到账对不上。
		if err = ensureRechargeAggregateChannel(ctx, db, in.BusinessID, now); err != nil {
			return nil, err
		}
	}
	if err = ensureCheckoutModeAllowed(ctx, db, snapshot, in.Mode, 0, now); err != nil {
		return nil, err
	}

	existing, lookupErr := db.EsportsCheckout.Query().Where(
		esportscheckout.BusinessTypeEQ(esportscheckout.BusinessType(in.BusinessType)),
		esportscheckout.BusinessIDEQ(in.BusinessID),
	).Only(ctx)
	if lookupErr == nil {
		return reconcileCheckout(ctx, db, existing, snapshot, in.Mode, now)
	}
	if !ent.IsNotFound(lookupErr) {
		return nil, lookupErr
	}

	token, err := newCheckoutEntryToken()
	if err != nil {
		return nil, err
	}
	item, err := db.EsportsCheckout.Create().
		SetBusinessType(esportscheckout.BusinessType(in.BusinessType)).
		SetBusinessID(in.BusinessID).SetCustomerID(customer.ID).
		SetAmountCents(snapshot.AmountCents).SetBusinessExpiresAt(snapshot.ExpiresAt.UTC()).
		SetChannelMode(esportscheckout.ChannelMode(in.Mode)).
		SetStatus(esportscheckout.StatusOpen).SetEntryToken(token).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			// 并发创建：回读既有入口，按幂等返回。
			existing, lookupErr = db.EsportsCheckout.Query().Where(
				esportscheckout.BusinessTypeEQ(esportscheckout.BusinessType(in.BusinessType)),
				esportscheckout.BusinessIDEQ(in.BusinessID),
			).Only(ctx)
			if lookupErr == nil {
				return reconcileCheckout(ctx, db, existing, snapshot, in.Mode, now)
			}
		}
		return nil, err
	}
	return item, nil
}

// reconcileCheckout 复用既有入口：金额快照变化 / 模式切换只在没有有效支付尝试时允许，
// 避免把已受理的支付尝试静默改金额或改投固定码。
func reconcileCheckout(ctx context.Context, db *ent.Client, existing *ent.EsportsCheckout,
	snapshot *checkoutBusinessSnapshot, mode string, now time.Time) (*ent.EsportsCheckout, error) {
	if existing.Status == esportscheckout.StatusPaid {
		return existing, nil
	}
	// 过期 / 取消的入口只做幂等读：业务时限已过或业务已关闭时不再改金额 / 模式。
	if existing.Status != esportscheckout.StatusOpen {
		return existing, nil
	}
	active, err := activeCheckoutAttempt(ctx, db, existing.ID)
	if err != nil {
		return nil, err
	}
	if mode == checkoutModeAggregate && snapshot.BusinessType == checkoutBusinessTypeRecharge && active == nil {
		// 有有效尝试时不动支付意图渠道（尝试创建时已保证是聚合渠道）；否则显式切到聚合商户码。
		if err = ensureRechargeAggregateChannel(ctx, db, snapshot.BusinessID, now); err != nil {
			return nil, err
		}
	}
	changed := false
	update := db.EsportsCheckout.Update().Where(
		esportscheckout.IDEQ(existing.ID), esportscheckout.VersionEQ(existing.Version))
	if existing.AmountCents != snapshot.AmountCents || !existing.BusinessExpiresAt.Equal(snapshot.ExpiresAt) {
		if active != nil {
			return nil, fmt.Errorf("%w：该业务单有进行中的支付尝试，金额或时限已变化，请等待支付结果或超时后重试", errConflict)
		}
		update.SetAmountCents(snapshot.AmountCents).SetBusinessExpiresAt(snapshot.ExpiresAt.UTC())
		changed = true
	}
	if existing.ChannelMode != esportscheckout.ChannelMode(mode) {
		if active != nil {
			return nil, fmt.Errorf("%w：该业务单有进行中的支付尝试，不能切换收款模式", errConflict)
		}
		if err = ensureCheckoutModeAllowed(ctx, db, snapshot, mode, existing.ID, now); err != nil {
			return nil, err
		}
		update.SetChannelMode(esportscheckout.ChannelMode(mode))
		changed = true
	}
	if !changed {
		return existing, nil
	}
	count, err := update.AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, errConflict
	}
	return db.EsportsCheckout.Get(ctx, existing.ID)
}

// ensureCheckoutModeAllowed 模式互斥与可用性前置：
//   - aggregate：服务商必须可用；订单不得已有待核验固定码凭证；礼物不得已有其他收款在进行；
//     且订单大额仍须走对公（既有口径）；
//   - manual：若存在有效动态尝试则拒绝（同一业务单不能一边动态支付一边固定码）。
func ensureCheckoutModeAllowed(ctx context.Context, db *ent.Client, snapshot *checkoutBusinessSnapshot,
	mode string, checkoutID int, now time.Time) error {
	if mode == checkoutModeManual {
		active, err := checkoutActiveAttemptForBusiness(ctx, db, snapshot.BusinessType, snapshot.BusinessID)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("%w：该业务单有进行中的在线支付，请等待支付结果或超时后再使用固定码付款", errConflict)
		}
		return nil
	}
	_, _, providers, err := checkoutChannelAvailability(ctx, db)
	if err != nil {
		return err
	}
	if len(providers) == 0 {
		return fmt.Errorf("%w：聚合收款服务商未启用或配置不完整", errPaymentGatewayUnavailable)
	}
	switch snapshot.BusinessType {
	case checkoutBusinessTypeOrder:
		if err = ensureCorporateChannelForAmount(ctx, db, "merchant_qr", snapshot.AmountCents); err != nil {
			return err
		}
		pending, err := otherPendingPaymentProofExists(ctx, db.EsportsOfflinePaymentProof.Query(),
			snapshot.BusinessID, 0)
		if err != nil {
			return err
		}
		if pending {
			return fmt.Errorf("%w：该订单已有待核验的固定码 / 转账凭证，不能再发起动态支付", errConflict)
		}
	case checkoutBusinessTypeGift:
		gift, err := db.EsportsGiftIntent.Get(ctx, snapshot.BusinessID)
		if err != nil {
			return err
		}
		if gift.PaymentStatus == esportsgiftintent.PaymentStatusAwaitingPayment {
			// 已有收款在途：允许复用本 checkout 自己的有效尝试；
			// 上一次尝试已过期（PaymentExpiresAt 已过）时允许重新发起。
			active, err := checkoutActiveAttemptForBusiness(ctx, db, snapshot.BusinessType, snapshot.BusinessID)
			if err != nil {
				return err
			}
			expired := gift.PaymentExpiresAt == nil || !now.UTC().Before(*gift.PaymentExpiresAt)
			if !active && !expired {
				return fmt.Errorf("%w：该礼物确认单已发起其他收款，请先等待结果或超时", errConflict)
			}
		}
	}
	return nil
}

// checkoutActiveAttemptForBusiness 判断业务单是否已有有效动态支付尝试（creating / pending）。
func checkoutActiveAttemptForBusiness(ctx context.Context, db *ent.Client,
	businessType string, businessID int) (bool, error) {
	checkout, err := db.EsportsCheckout.Query().Where(
		esportscheckout.BusinessTypeEQ(esportscheckout.BusinessType(businessType)),
		esportscheckout.BusinessIDEQ(businessID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	active, err := activeCheckoutAttempt(ctx, db, checkout.ID)
	if err != nil {
		return false, err
	}
	return active != nil, nil
}

// ensureNoActiveCheckoutAttempt 供固定码凭证提交 / 人工核验 / 旧在线收款入口做互斥守卫：
// 有效动态支付尝试存在时，不允许同业务单再走另一条收款路径。
func ensureNoActiveCheckoutAttempt(ctx context.Context, db *ent.Client,
	businessType string, businessID int) error {
	active, err := checkoutActiveAttemptForBusiness(ctx, db, businessType, businessID)
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("%w：该业务单有进行中的在线支付，请等待支付结果或超时后再操作", errConflict)
	}
	return nil
}

// activeCheckoutAttempt 返回当前有效尝试（creating / pending），没有返回 nil。
func activeCheckoutAttempt(ctx context.Context, db *ent.Client,
	checkoutID int) (*ent.EsportsCheckoutAttempt, error) {
	item, err := db.EsportsCheckoutAttempt.Query().Where(
		esportscheckoutattempt.CheckoutIDEQ(checkoutID),
		esportscheckoutattempt.StatusIn(
			esportscheckoutattempt.StatusCreating, esportscheckoutattempt.StatusPending),
	).Order(ent.Desc(esportscheckoutattempt.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return item, nil
}

// loadCheckoutByID 读取入口并做惰性过期（业务时限已过 → expired，并释放有效尝试）。
func loadCheckoutByID(ctx context.Context, db *ent.Client, id int, now time.Time) (*ent.EsportsCheckout, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsCheckout.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return expireCheckoutIfDue(ctx, db, item, now)
}

// loadCheckoutByToken 按入口令牌读取并做惰性过期。
func loadCheckoutByToken(ctx context.Context, db *ent.Client, token string, now time.Time) (*ent.EsportsCheckout, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 64 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsCheckout.Query().Where(esportscheckout.EntryTokenEQ(token)).Only(ctx)
	if err != nil {
		return nil, err
	}
	return expireCheckoutIfDue(ctx, db, item, now)
}

// expireCheckoutIfDue 惰性过期：业务时限已过且未支付 → 入口 expired，同时把有效尝试置 expired。
// 幂等：已支付 / 已过期 / 已取消不受影响。
func expireCheckoutIfDue(ctx context.Context, db *ent.Client, item *ent.EsportsCheckout,
	now time.Time) (*ent.EsportsCheckout, error) {
	if item == nil {
		return nil, errInvalidInput
	}
	if item.Status != esportscheckout.StatusOpen || now.UTC().Before(item.BusinessExpiresAt) {
		return item, nil
	}
	changed, err := db.EsportsCheckout.Update().Where(
		esportscheckout.IDEQ(item.ID),
		esportscheckout.StatusEQ(esportscheckout.StatusOpen),
		esportscheckout.BusinessExpiresAtLTE(now.UTC()),
	).SetStatus(esportscheckout.StatusExpired).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed == 1 {
		// 有效尝试一并过期；迟到的服务商回调仍会进入结算，由业务边界决定是否可入账。
		if _, err = db.EsportsCheckoutAttempt.Update().Where(
			esportscheckoutattempt.CheckoutIDEQ(item.ID),
			esportscheckoutattempt.StatusIn(
				esportscheckoutattempt.StatusCreating, esportscheckoutattempt.StatusPending),
		).SetStatus(esportscheckoutattempt.StatusExpired).ClearActiveDedupKey().Save(ctx); err != nil {
			return nil, err
		}
	}
	return db.EsportsCheckout.Get(ctx, item.ID)
}

// checkoutViewOf 组装入口视图（带最近一条支付尝试）。
func checkoutViewOf(ctx context.Context, db *ent.Client, item *ent.EsportsCheckout) (*checkoutView, error) {
	if item == nil {
		return nil, errInvalidInput
	}
	view := &checkoutView{
		ID: item.ID, BusinessType: item.BusinessType.String(), BusinessID: item.BusinessID,
		AmountCents: item.AmountCents, ChannelMode: item.ChannelMode.String(),
		Status: item.Status.String(), EntryToken: item.EntryToken,
		BusinessExpiresAt: item.BusinessExpiresAt, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
	attempt, err := db.EsportsCheckoutAttempt.Query().Where(
		esportscheckoutattempt.CheckoutIDEQ(item.ID),
	).Order(ent.Desc(esportscheckoutattempt.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return view, nil
	}
	if err != nil {
		return nil, err
	}
	// 惰性过期：读视图时把已超时的有效尝试收敛为 expired，避免前端一直显示「处理中」。
	if (attempt.Status == esportscheckoutattempt.StatusCreating ||
		attempt.Status == esportscheckoutattempt.StatusPending) &&
		!time.Now().UTC().Before(attempt.ExpiresAt) {
		if expireErr := expireCheckoutAttempt(ctx, db, attempt.ID, time.Now().UTC()); expireErr == nil {
			if reloaded, reloadErr := db.EsportsCheckoutAttempt.Get(ctx, attempt.ID); reloadErr == nil {
				attempt = reloaded
			}
		}
	}
	view.Attempt = checkoutAttemptViewOf(attempt)
	return view, nil
}

func checkoutAttemptViewOf(item *ent.EsportsCheckoutAttempt) *checkoutAttemptView {
	if item == nil {
		return nil
	}
	return &checkoutAttemptView{
		AttemptID: item.ID, Provider: item.Provider.String(), PayChannel: item.PayChannel.String(),
		Status: item.Status.String(), PayURL: item.PayURL, QRPayload: item.QrPayload,
		ExpiresAt: item.ExpiresAt,
	}
}

// checkoutBusinessExpiredError 统一「业务时限已过」的错误文案。
func checkoutBusinessExpiredError() error {
	return fmt.Errorf("%w：业务支付时限已过，无法再发起支付", errConflict)
}

// ensureRechargeAggregateChannel 把「尚未受理」的充值支付意图渠道切到聚合商户码。
// 充值意图与活动快照在发起充值时已锁定，这里只改收款渠道，不改金额 / 赠送；
// 已支付 / 已过期 / 已失效的支付意图不动（改道会破坏核销一致性）。
func ensureRechargeAggregateChannel(ctx context.Context, db *ent.Client,
	rechargeIntentID int, now time.Time) error {
	recharge, err := db.EsportsRechargeIntent.Get(ctx, rechargeIntentID)
	if err != nil {
		return err
	}
	intent, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(recharge.IntentNo)).Only(ctx)
	if err != nil {
		return err
	}
	if intent.Status != esportspaymentintent.StatusPending {
		return fmt.Errorf("%w：充值支付意图已不是待支付状态，不能切换为聚合收款", errConflict)
	}
	if !now.UTC().Before(intent.ExpiresAt) {
		return fmt.Errorf("%w：充值支付意图已过期，请重新发起充值", errConflict)
	}
	if aggregateCheckoutChannel(intent.Channel) {
		return nil
	}
	changed, err := db.EsportsPaymentIntent.Update().Where(
		esportspaymentintent.IDEQ(intent.ID),
		esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
		esportspaymentintent.ChannelEQ(intent.Channel),
	).SetChannel("merchant_qr").Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	return nil
}

// createCheckoutAttempt 为聚合模式 checkout 创建一笔动态支付尝试。
func createCheckoutAttempt(ctx context.Context, db *ent.Client, checkout *ent.EsportsCheckout,
	payChannel string, now time.Time) (*ent.EsportsCheckoutAttempt, error) {
	payChannel = strings.TrimSpace(payChannel)
	if checkout == nil || (payChannel != "wechat" && payChannel != "alipay") {
		return nil, errInvalidInput
	}
	current, err := loadCheckoutByID(ctx, db, checkout.ID, now)
	if err != nil {
		return nil, err
	}
	if current.Status != esportscheckout.StatusOpen {
		return nil, fmt.Errorf("%w：聚合收款入口已%s，不能发起支付", errConflict,
			checkoutStatusText(current.Status.String()))
	}
	if current.ChannelMode != esportscheckout.ChannelModeAggregate {
		return nil, fmt.Errorf("%w：该入口已选择固定码收款，不能发起动态支付", errConflict)
	}
	snapshot, err := loadCheckoutBusinessSnapshot(ctx, db, current.CustomerID,
		current.BusinessType.String(), current.BusinessID, now)
	if err != nil {
		return nil, err
	}
	if snapshot.AmountCents != current.AmountCents || !snapshot.ExpiresAt.Equal(current.BusinessExpiresAt) {
		return nil, fmt.Errorf("%w：业务金额或时限已变化，请刷新后重新发起", errConflict)
	}
	if !now.UTC().Before(current.BusinessExpiresAt) {
		return nil, checkoutBusinessExpiredError()
	}
	if err = ensureCheckoutModeAllowed(ctx, db, snapshot, checkoutModeAggregate, current.ID, now); err != nil {
		return nil, err
	}

	active, err := activeCheckoutAttempt(ctx, db, current.ID)
	if err != nil {
		return nil, err
	}
	if active != nil {
		if !now.UTC().Before(active.ExpiresAt) {
			if err = expireCheckoutAttempt(ctx, db, active.ID, now); err != nil {
				return nil, err
			}
			active = nil
		} else {
			if active.PayChannel.String() != payChannel {
				return nil, fmt.Errorf("%w：当前已有进行中的支付尝试（%s），请等待结果或超时后再切换渠道",
					errConflict, checkoutPayChannelText(active.PayChannel.String()))
			}
			switch active.Status {
			case esportscheckoutattempt.StatusPending:
				// 幂等：同一业务单同一渠道的重复请求直接返回已受理的尝试，不新建商户单号。
				return active, nil
			case esportscheckoutattempt.StatusCreating:
				// 上次受理结果未知（例如进程中断）：用同一商户单号重试下单，服务商按单号幂等。
				return resumeCheckoutAttempt(ctx, db, active, now)
			}
		}
	}

	providers, err := availableCheckoutProviders(ctx, db, payChannel)
	if err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("%w：聚合收款服务商未启用或配置不完整", errPaymentGatewayUnavailable)
	}
	provider := pickCheckoutProvider(ctx, db, current.ID, providers)
	if provider == nil {
		return nil, fmt.Errorf("%w：没有可用的聚合收款服务商", errPaymentGatewayUnavailable)
	}
	settlementChannel, err := checkoutAttemptSettlementChannel(ctx, db, current, payChannel)
	if err != nil {
		return nil, err
	}
	attemptExpiry := now.UTC().Add(checkoutAttemptWindow(ctx, db))
	if attemptExpiry.After(current.BusinessExpiresAt) {
		attemptExpiry = current.BusinessExpiresAt
	}
	merchantNo := newCheckoutMerchantNo(provider.Name(), now)

	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	switch current.BusinessType {
	case esportscheckout.BusinessTypeOrder:
		order, err := tx.EsportsOrder.Get(ctx, current.BusinessID)
		if err != nil {
			return nil, err
		}
		if order.CustomerID != current.CustomerID || order.PayableCents != current.AmountCents {
			return nil, errConflict
		}
		intent, err := ensureOrderPaymentIntent(ctx, tx, order, settlementChannel, now)
		if err != nil {
			return nil, err
		}
		if intent.AmountCents != current.AmountCents {
			return nil, errConflict
		}
	case esportscheckout.BusinessTypeRecharge:
		recharge, err := tx.EsportsRechargeIntent.Get(ctx, current.BusinessID)
		if err != nil {
			return nil, err
		}
		intent, err := tx.EsportsPaymentIntent.Query().Where(
			esportspaymentintent.IntentNoEQ(recharge.IntentNo)).Only(ctx)
		if err != nil {
			return nil, err
		}
		if intent.Status != esportspaymentintent.StatusPending ||
			intent.Channel != settlementChannel || intent.AmountCents != current.AmountCents {
			return nil, errConflict
		}
	case esportscheckout.BusinessTypeGift:
		// 礼物收款有效期按确认单业务时限登记（而不是 5 分钟的尝试窗口）：
		// 服务商回调常有延迟，实际到账只要在业务时限内就应正常核销。
		if err = reserveGiftPaymentForCheckout(ctx, tx, current.BusinessID, settlementChannel,
			merchantNo, current.BusinessExpiresAt, now); err != nil {
			return nil, err
		}
	}
	attempt, err := tx.EsportsCheckoutAttempt.Create().
		SetCheckoutID(current.ID).
		SetProvider(esportscheckoutattempt.Provider(provider.Name())).
		SetPayChannel(esportscheckoutattempt.PayChannel(payChannel)).
		SetMerchantNo(merchantNo).
		SetCreatedAt(now.UTC()).
		SetStatus(esportscheckoutattempt.StatusCreating).
		SetExpiresAt(attemptExpiry).
		SetActiveDedupKey(checkoutDedupKey(current.ID)).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			// 并发尝试：回读有效尝试按幂等返回。
			latest, lookupErr := activeCheckoutAttempt(ctx, db, current.ID)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if latest != nil {
				return latest, nil
			}
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}

	// 服务商受理放在事务外：网络失败不把本地事务拖成回滚，失败后尝试置 failed 并释放有效键，
	// 下次请求可按 priority 切到下一家（此时尚未受理，允许改投）。
	subject, subjectErr := checkoutPaymentSubject(ctx, db, current)
	if subjectErr != nil {
		subject = "陪玩服务"
	}
	result, err := provider.CreatePayment(ctx, checkoutPaymentRequest{
		RequestedAt: attempt.CreatedAt, ClientIP: checkoutClientIP(ctx),
		MerchantNo: merchantNo, PayChannel: payChannel, AmountCents: current.AmountCents,
		Subject: subject, ExpiresAt: attemptExpiry,
	})
	if err != nil {
		// 只有服务商「明确拒绝」才置 failed 并按 priority 切换；网络超时 / 响应丢失属于
		// 「受理结果未知」，尝试保持 creating，下次用同一商户单号重试或由查单补偿收敛，
		// 绝不把可能已受理的尝试静默改投另一家。
		if errors.Is(err, errCheckoutProviderRejected) {
			_ = markCheckoutAttemptFailed(ctx, db, attempt, "服务商拒绝受理")
		}
		return nil, err
	}
	update := db.EsportsCheckoutAttempt.UpdateOneID(attempt.ID).
		SetStatus(esportscheckoutattempt.StatusPending).
		SetPayURL(strings.TrimSpace(result.PayURL)).
		SetQrPayload(strings.TrimSpace(result.QRPayload))
	if strings.TrimSpace(result.ProviderTradeNo) != "" {
		update.SetProviderTradeNo(strings.TrimSpace(result.ProviderTradeNo))
	}
	if err = update.Exec(ctx); err != nil {
		return nil, err
	}
	return db.EsportsCheckoutAttempt.Get(ctx, attempt.ID)
}

// resumeCheckoutAttempt 用同一商户单号重试服务商下单（受理结果未知的场景）。
func resumeCheckoutAttempt(ctx context.Context, db *ent.Client,
	attempt *ent.EsportsCheckoutAttempt, now time.Time) (*ent.EsportsCheckoutAttempt, error) {
	_ = now
	provider, err := checkoutProviderFor(ctx, db, attempt.Provider.String())
	if err != nil {
		return nil, err
	}
	checkout, err := db.EsportsCheckout.Get(ctx, attempt.CheckoutID)
	if err != nil {
		return nil, err
	}
	subject, subjectErr := checkoutPaymentSubject(ctx, db, checkout)
	if subjectErr != nil {
		subject = "陪玩服务"
	}
	result, err := provider.CreatePayment(ctx, checkoutPaymentRequest{
		RequestedAt: attempt.CreatedAt, ClientIP: checkoutClientIP(ctx),
		MerchantNo: attempt.MerchantNo, PayChannel: attempt.PayChannel.String(),
		AmountCents: checkout.AmountCents, Subject: subject, ExpiresAt: attempt.ExpiresAt,
	})
	if err != nil {
		if errors.Is(err, errCheckoutProviderRejected) {
			_ = markCheckoutAttemptFailed(ctx, db, attempt, "服务商拒绝受理")
		}
		return nil, err
	}
	update := db.EsportsCheckoutAttempt.UpdateOneID(attempt.ID).
		SetStatus(esportscheckoutattempt.StatusPending).
		SetPayURL(strings.TrimSpace(result.PayURL)).
		SetQrPayload(strings.TrimSpace(result.QRPayload))
	if strings.TrimSpace(result.ProviderTradeNo) != "" {
		update.SetProviderTradeNo(strings.TrimSpace(result.ProviderTradeNo))
	}
	if err = update.Exec(ctx); err != nil {
		return nil, err
	}
	return db.EsportsCheckoutAttempt.Get(ctx, attempt.ID)
}

// pickCheckoutProvider 按 priority 选服务商：跳过该 checkout 已「受理失败」的服务商，
// 全部失败过时回到第一家（允许重新尝试），已受理的尝试不会走到这里。
func pickCheckoutProvider(ctx context.Context, db *ent.Client, checkoutID int,
	providers []checkoutProvider) checkoutProvider {
	failed := map[string]bool{}
	rows, err := db.EsportsCheckoutAttempt.Query().Where(
		esportscheckoutattempt.CheckoutIDEQ(checkoutID),
		esportscheckoutattempt.StatusEQ(esportscheckoutattempt.StatusFailed),
	).All(ctx)
	if err == nil {
		for _, row := range rows {
			failed[row.Provider.String()] = true
		}
	}
	for _, provider := range providers {
		if !failed[provider.Name()] {
			return provider
		}
	}
	if len(providers) > 0 {
		return providers[0]
	}
	return nil
}

// checkoutAttemptSettlementChannel 本次尝试入账时使用的渠道编码：
// 订单 / 礼物按实际支付渠道细分；充值的支付意图渠道在发起充值时已固定，必须是聚合可用渠道。
func checkoutAttemptSettlementChannel(ctx context.Context, db *ent.Client,
	checkout *ent.EsportsCheckout, payChannel string) (string, error) {
	if checkout.BusinessType != esportscheckout.BusinessTypeRecharge {
		return checkoutSettlementChannel(payChannel), nil
	}
	recharge, err := db.EsportsRechargeIntent.Get(ctx, checkout.BusinessID)
	if err != nil {
		return "", err
	}
	intent, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(recharge.IntentNo)).Only(ctx)
	if err != nil {
		return "", err
	}
	if !aggregateCheckoutChannel(intent.Channel) {
		return "", fmt.Errorf("%w：该充值意图的收款渠道 %s 不支持聚合动态支付，请以 merchant_qr 渠道重新发起充值",
			errInvalidInput, intent.Channel)
	}
	return intent.Channel, nil
}

// reserveGiftPaymentForCheckout 在礼物确认单上登记本次动态收款（不调用旧网关）：
// 只有 confirmed 状态的确认单可收款；已有未超时的其他收款在途时拒绝改投。
func reserveGiftPaymentForCheckout(ctx context.Context, tx *ent.Tx, giftIntentID int,
	channel, merchantNo string, expiresAt, now time.Time) error {
	gift, err := tx.EsportsGiftIntent.Get(ctx, giftIntentID)
	if err != nil {
		return err
	}
	if gift.Status != esportsgiftintent.StatusConfirmed {
		return fmt.Errorf("%w：礼物确认单未确认，不能发起收款", errConflict)
	}
	if gift.PaymentStatus == esportsgiftintent.PaymentStatusVerified {
		return fmt.Errorf("%w：礼物收款已到账", errConflict)
	}
	if !now.UTC().Before(gift.ExpiresAt) {
		return fmt.Errorf("%w：礼物确认单已过期，请重新发起", errConflict)
	}
	if gift.PaymentStatus == esportsgiftintent.PaymentStatusAwaitingPayment {
		if gift.PaymentExpiresAt != nil && now.UTC().Before(*gift.PaymentExpiresAt) {
			if gift.PaymentIntentNo != nil && *gift.PaymentIntentNo != merchantNo {
				return fmt.Errorf("%w：该礼物确认单已发起其他收款，请先等待结果或超时", errConflict)
			}
		}
	}
	update := tx.EsportsGiftIntent.Update().Where(
		esportsgiftintent.IDEQ(gift.ID), esportsgiftintent.VersionEQ(gift.Version))
	switch gift.PaymentStatus {
	case esportsgiftintent.PaymentStatusNotRequired,
		esportsgiftintent.PaymentStatusAwaitingPayment,
		esportsgiftintent.PaymentStatusFailed:
	default:
		return fmt.Errorf("%w：礼物确认单收款状态不允许发起动态支付", errConflict)
	}
	changed, err := update.SetPaymentChannel(esportsgiftintent.PaymentChannel(channel)).
		SetPaymentStatus(esportsgiftintent.PaymentStatusAwaitingPayment).
		SetPaymentIntentNo(merchantNo).SetPaymentExpiresAt(expiresAt.UTC()).
		AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	return nil
}

// releaseGiftPaymentReservation 服务商受理失败 / 查单明确关闭时释放礼物收款登记：
// 只有登记号仍是本尝试的商户单号且尚未到账时才清理，避免覆盖后续尝试。
func releaseGiftPaymentReservation(ctx context.Context, db *ent.Client, checkout *ent.EsportsCheckout,
	merchantNo string) {
	if checkout == nil || checkout.BusinessType != esportscheckout.BusinessTypeGift {
		return
	}
	_, _ = db.EsportsGiftIntent.Update().Where(
		esportsgiftintent.IDEQ(checkout.BusinessID),
		esportsgiftintent.PaymentStatusEQ(esportsgiftintent.PaymentStatusAwaitingPayment),
		esportsgiftintent.PaymentIntentNoEQ(merchantNo),
	).SetPaymentStatus(esportsgiftintent.PaymentStatusNotRequired).
		ClearPaymentIntentNo().ClearPaymentExpiresAt().AddVersion(1).Save(ctx)
}

// checkoutPaymentSubject 服务商收银台标题（不含敏感信息）。
func checkoutPaymentSubject(ctx context.Context, db *ent.Client,
	checkout *ent.EsportsCheckout) (string, error) {
	switch checkout.BusinessType {
	case esportscheckout.BusinessTypeOrder:
		order, err := db.EsportsOrder.Get(ctx, checkout.BusinessID)
		if err != nil {
			return "", err
		}
		sku, skuErr := db.EsportsSku.Get(ctx, order.SkuID)
		if skuErr != nil {
			return "订单 " + order.OrderNo, nil
		}
		return orderPaymentSubject(order, sku), nil
	case esportscheckout.BusinessTypeRecharge:
		return "账户充值", nil
	case esportscheckout.BusinessTypeGift:
		gift, err := db.EsportsGiftIntent.Get(ctx, checkout.BusinessID)
		if err != nil {
			return "", err
		}
		return "礼物 " + gift.GiftName, nil
	}
	return "陪玩服务", nil
}

// checkoutStatusText 中文状态文案。
func checkoutStatusText(status string) string {
	switch status {
	case "open":
		return "待支付"
	case "paid":
		return "已支付"
	case "expired":
		return "过期"
	case "cancelled":
		return "取消"
	}
	return status
}

// checkoutPayChannelText 中文渠道文案。
func checkoutPayChannelText(channel string) string {
	if channel == "alipay" {
		return "支付宝"
	}
	return "微信"
}

// expireCheckoutAttempt 把尝试置 expired 并释放有效键（保留支付渠道 / 商户单号供对账）。
func expireCheckoutAttempt(ctx context.Context, db *ent.Client, attemptID int, now time.Time) error {
	_ = now
	_, err := db.EsportsCheckoutAttempt.Update().Where(
		esportscheckoutattempt.IDEQ(attemptID),
		esportscheckoutattempt.StatusIn(
			esportscheckoutattempt.StatusCreating, esportscheckoutattempt.StatusPending),
	).SetStatus(esportscheckoutattempt.StatusExpired).ClearActiveDedupKey().Save(ctx)
	if err != nil {
		return err
	}
	// 过期释放礼物收款登记：允许在业务时限内换新商户单号重试；
	// 旧尝试的迟到到账不再自动送礼，按异常到账交财务处理。
	if attempt, lookupErr := db.EsportsCheckoutAttempt.Get(ctx, attemptID); lookupErr == nil &&
		attempt.Status == esportscheckoutattempt.StatusExpired {
		if checkout, checkoutErr := db.EsportsCheckout.Get(ctx, attempt.CheckoutID); checkoutErr == nil {
			releaseGiftPaymentReservation(ctx, db, checkout, attempt.MerchantNo)
		}
	}
	return nil
}

// markCheckoutAttemptFailed 受理失败 / 异常到账：置 failed、清空有效键；
// 礼物登记按商户单号释放（期间若已有到账则不动）。
func markCheckoutAttemptFailed(ctx context.Context, db *ent.Client,
	attempt *ent.EsportsCheckoutAttempt, note string) error {
	if attempt == nil {
		return errInvalidInput
	}
	changed, err := db.EsportsCheckoutAttempt.Update().Where(
		esportscheckoutattempt.IDEQ(attempt.ID),
		esportscheckoutattempt.StatusIn(
			esportscheckoutattempt.StatusCreating, esportscheckoutattempt.StatusPending),
	).SetStatus(esportscheckoutattempt.StatusFailed).ClearActiveDedupKey().Save(ctx)
	if err != nil {
		return err
	}
	if changed == 1 {
		if checkout, lookupErr := db.EsportsCheckout.Get(ctx, attempt.CheckoutID); lookupErr == nil {
			releaseGiftPaymentReservation(ctx, db, checkout, attempt.MerchantNo)
		}
	}
	_ = note
	return nil
}

// settleCheckoutPayment 服务商验签回调 / 服务端查单确认成功后的统一结算入口。
// 依次校验：服务商与商户单号 → 金额 → 实际支付渠道 → 业务单快照 → 边界入账 → 尝试 / 入口置 paid。
// 任何异常都记 checkout.payment_exception 外盒事件并置 failed，绝不重复入账或送礼。
func settleCheckoutPayment(ctx context.Context, db *ent.Client, providerName string,
	notice *checkoutNotification, now time.Time) (*ent.EsportsCheckoutAttempt, error) {
	if notice == nil || strings.TrimSpace(notice.MerchantNo) == "" ||
		(notice.Paid && strings.TrimSpace(notice.ProviderTradeNo) == "") {
		return nil, errInvalidInput
	}
	providerName = strings.TrimSpace(providerName)
	if providerName != "" && notice.Provider != "" && providerName != notice.Provider {
		return nil, fmt.Errorf("%w：回调服务商与验签服务商不一致", errForbidden)
	}
	attempt, err := db.EsportsCheckoutAttempt.Query().Where(
		esportscheckoutattempt.MerchantNoEQ(strings.TrimSpace(notice.MerchantNo))).Only(ctx)
	if err != nil {
		return nil, err
	}
	if notice.Provider != "" && attempt.Provider.String() != notice.Provider {
		return nil, fmt.Errorf("%w：服务商与商户单号不匹配", errForbidden)
	}
	// 非成功通知（处理中 / 关单 / 支付失败）不结算：明确关单的状态把尝试置 failed，
	// 其余保持 pending 交给查单补偿继续确认。
	if !notice.Paid {
		if checkoutProviderTerminalFailure(notice.Status) {
			_ = markCheckoutAttemptFailed(ctx, db, attempt, "服务商关闭")
		}
		return attempt, nil
	}
	if attempt.Status == esportscheckoutattempt.StatusPaid {
		// 重复通知：幂等成功。若进程在「尝试已 paid、入口未 paid」之间中断，这里补齐入口状态。
		checkout, lookupErr := db.EsportsCheckout.Get(ctx, attempt.CheckoutID)
		if lookupErr == nil {
			if markErr := markCheckoutPaid(ctx, db, checkout); markErr != nil {
				return nil, markErr
			}
		}
		return attempt, nil
	}
	checkout, err := db.EsportsCheckout.Get(ctx, attempt.CheckoutID)
	if err != nil {
		return nil, err
	}
	if attempt.ProviderTradeNo != nil && strings.TrimSpace(*attempt.ProviderTradeNo) != "" &&
		*attempt.ProviderTradeNo != notice.ProviderTradeNo {
		return checkoutException(ctx, db, checkout, attempt, "trade_no_conflict",
			"同一商户单号出现不同的服务商交易号", now)
	}
	// 服务商交易号全站唯一：被其它尝试占用说明重复推送 / 串单，转人工。
	duplicated, err := db.EsportsCheckoutAttempt.Query().Where(
		esportscheckoutattempt.ProviderEQ(esportscheckoutattempt.Provider(attempt.Provider.String())),
		esportscheckoutattempt.ProviderTradeNoEQ(notice.ProviderTradeNo),
		esportscheckoutattempt.IDNEQ(attempt.ID),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if duplicated {
		return checkoutException(ctx, db, checkout, attempt, "trade_no_reused",
			"该服务商交易号已被其它支付尝试占用", now)
	}
	if notice.AmountCents != checkout.AmountCents {
		return checkoutException(ctx, db, checkout, attempt, "amount_mismatch",
			fmt.Sprintf("服务商金额 %d 分与业务金额 %d 分不一致", notice.AmountCents, checkout.AmountCents),
			now)
	}
	if notice.PayChannel != "" && notice.PayChannel != attempt.PayChannel.String() {
		return checkoutException(ctx, db, checkout, attempt, "channel_mismatch",
			fmt.Sprintf("实际支付渠道 %s 与尝试渠道 %s 不一致", notice.PayChannel, attempt.PayChannel.String()),
			now)
	}
	if notice.PaidAt.IsZero() {
		notice.PaidAt = now.UTC()
	}

	// 先落服务商交易号，保证结算失败后重试仍能按同一笔交易幂等。
	if attempt.ProviderTradeNo == nil || *attempt.ProviderTradeNo == "" {
		_, _ = db.EsportsCheckoutAttempt.Update().Where(
			esportscheckoutattempt.IDEQ(attempt.ID),
			esportscheckoutattempt.ProviderTradeNoIsNil(),
		).SetProviderTradeNo(notice.ProviderTradeNo).Save(ctx)
	}

	if err = applyCheckoutBusinessPayment(ctx, db, checkout, attempt, notice, now); err != nil {
		if isCheckoutBusinessException(err) {
			return checkoutException(ctx, db, checkout, attempt, "business_rejected", err.Error(), now)
		}
		return nil, err
	}
	paidAttempt, err := markCheckoutAttemptPaid(ctx, db, attempt, notice.ProviderTradeNo)
	if err != nil {
		return nil, err
	}
	if err = markCheckoutPaid(ctx, db, checkout); err != nil {
		return nil, err
	}
	_, _ = db.EsportsOutbox.Create().
		SetEventKey("checkout.payment_confirmed:" + attempt.MerchantNo).
		SetEventType("checkout.payment_confirmed").SetAggregateRef(attempt.MerchantNo).
		SetPayload(map[string]interface{}{
			"checkout_id": checkout.ID, "business_type": checkout.BusinessType.String(),
			"business_id": checkout.BusinessID, "attempt_id": attempt.ID,
			"provider": attempt.Provider.String(), "pay_channel": attempt.PayChannel.String(),
			"amount_cents": checkout.AmountCents, "provider_trade_no": notice.ProviderTradeNo,
		}).Save(ctx)
	return paidAttempt, nil
}

// isCheckoutBusinessException 判断结算边界返回的是否「业务不可入账」（而非临时故障）：
// 业务冲突 / 参数不符 / 权限（业务归属）都转人工异常，不自动重试。
func isCheckoutBusinessException(err error) bool {
	if err == nil {
		return false
	}
	for _, target := range []error{errConflict, errInvalidInput, errForbidden, errAgeUnverified} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// applyCheckoutBusinessPayment 按业务类型分派到既有唯一结算边界。
func applyCheckoutBusinessPayment(ctx context.Context, db *ent.Client, checkout *ent.EsportsCheckout,
	attempt *ent.EsportsCheckoutAttempt, notice *checkoutNotification, now time.Time) error {
	systemOperator := giftPaymentSystemOperator
	switch checkout.BusinessType {
	case esportscheckout.BusinessTypeOrder:
		intent, err := db.EsportsPaymentIntent.Query().Where(
			esportspaymentintent.OrderIDEQ(checkout.BusinessID),
			esportspaymentintent.ChannelEQ(checkoutSettlementChannel(attempt.PayChannel.String())),
		).Order(ent.Desc(esportspaymentintent.FieldID)).First(ctx)
		if err != nil {
			return fmt.Errorf("%w：订单缺少对应渠道的支付意图", errConflict)
		}
		if intent.AmountCents != checkout.AmountCents {
			return fmt.Errorf("%w：支付意图金额与业务金额不一致", errConflict)
		}
		if _, err = applyVerifiedPayment(ctx, db, verifiedPayment{
			IntentNo: intent.IntentNo, Channel: intent.Channel,
			TransactionNo: notice.ProviderTradeNo, AmountCents: notice.AmountCents,
			ConfirmedAt: notice.PaidAt.UTC(), AppliedAt: now.UTC(),
		}); err != nil {
			return err
		}
		// applyVerifiedPayment 在订单已不可核销时会落「异常回执」而不报错（AppliedCents=0）：
		// 此时不得把 checkout 当成功，按业务异常交财务处理。
		receipt, receiptErr := db.EsportsPaymentReceipt.Query().Where(
			esportspaymentreceipt.ChannelEQ(intent.Channel),
			esportspaymentreceipt.TransactionNoEQ(notice.ProviderTradeNo),
		).Only(ctx)
		if receiptErr != nil {
			return receiptErr
		}
		if receipt.AppliedCents <= 0 {
			return fmt.Errorf("%w：订单当前不可核销（异常回执 %d）", errConflict, receipt.ID)
		}
		return nil
	case esportscheckout.BusinessTypeRecharge:
		recharge, err := db.EsportsRechargeIntent.Get(ctx, checkout.BusinessID)
		if err != nil {
			return err
		}
		intent, err := db.EsportsPaymentIntent.Query().Where(
			esportspaymentintent.IntentNoEQ(recharge.IntentNo)).Only(ctx)
		if err != nil {
			return err
		}
		if !aggregateCheckoutChannel(intent.Channel) {
			return fmt.Errorf("%w：充值支付意图渠道不支持聚合收款入账", errConflict)
		}
		_, err = settleRechargeIntent(ctx, db, rechargeSettlementInput{
			IntentNo: recharge.IntentNo, Channel: intent.Channel,
			TransactionNo: notice.ProviderTradeNo, ReceivedCents: notice.AmountCents,
			ConfirmedAt: notice.PaidAt.UTC(),
			EvidenceNote: fmt.Sprintf("聚合收款 %s 回调验签通过，商户单号 %s",
				attempt.Provider.String(), attempt.MerchantNo),
		}, now.UTC())
		if err != nil {
			return err
		}
		return nil
	case esportscheckout.BusinessTypeGift:
		gift, err := db.EsportsGiftIntent.Get(ctx, checkout.BusinessID)
		if err != nil {
			return err
		}
		operator := gift.CreatedByAdminID
		if operator < 1 {
			operator = systemOperator
		}
		_, err = settleGiftPayment(ctx, db, operator, false, gift.ID, giftPaymentVerifyInput{
			Approve: true, PaymentRef: notice.ProviderTradeNo, ReceivedCents: notice.AmountCents,
			EvidenceNote: "聚合收款回调验签：" + truncateText(notice.ProviderTradeNo, 200),
		}, notice.PaidAt.UTC(), false)
		if err == nil {
			return nil
		}
		// 并发重复回调 / 上次核验后成单中断：收款已核验但礼物单未生成时必须补执行送礼，
		// 不能仅凭「已核验」就当作成功，否则客户付了钱却收不到礼物。
		latest, lookupErr := db.EsportsGiftIntent.Get(ctx, gift.ID)
		if lookupErr == nil {
			if latest.Status == esportsgiftintent.StatusConsumed && latest.PaymentRef == notice.ProviderTradeNo {
				return nil
			}
			if latest.PaymentStatus == esportsgiftintent.PaymentStatusVerified &&
				latest.PaymentRef == notice.ProviderTradeNo {
				if _, execErr := executeGiftIntent(ctx, db, operator, false, latest.ID, notice.PaidAt.UTC()); execErr == nil {
					return nil
				}
				refreshed, refreshErr := db.EsportsGiftIntent.Get(ctx, gift.ID)
				if refreshErr == nil && refreshed.Status == esportsgiftintent.StatusConsumed &&
					refreshed.PaymentRef == notice.ProviderTradeNo {
					return nil
				}
			}
		}
		return err
	}
	return errInvalidInput
}

// markCheckoutAttemptPaid 尝试置 paid：条件更新兜底并发，已 paid 幂等返回。
func markCheckoutAttemptPaid(ctx context.Context, db *ent.Client,
	attempt *ent.EsportsCheckoutAttempt, tradeNo string) (*ent.EsportsCheckoutAttempt, error) {
	update := db.EsportsCheckoutAttempt.Update().Where(
		esportscheckoutattempt.IDEQ(attempt.ID),
		esportscheckoutattempt.StatusNEQ(esportscheckoutattempt.StatusPaid),
	).SetStatus(esportscheckoutattempt.StatusPaid).ClearActiveDedupKey()
	if attempt.ProviderTradeNo == nil || strings.TrimSpace(*attempt.ProviderTradeNo) == "" {
		update.SetProviderTradeNo(strings.TrimSpace(tradeNo))
	}
	if _, err := update.Save(ctx); err != nil {
		return nil, err
	}
	return db.EsportsCheckoutAttempt.Get(ctx, attempt.ID)
}

// markCheckoutPaid 入口置 paid（已 paid 幂等）。
func markCheckoutPaid(ctx context.Context, db *ent.Client, checkout *ent.EsportsCheckout) error {
	_, err := db.EsportsCheckout.Update().Where(
		esportscheckout.IDEQ(checkout.ID),
		esportscheckout.StatusNEQ(esportscheckout.StatusPaid),
	).SetStatus(esportscheckout.StatusPaid).AddVersion(1).Save(ctx)
	return err
}

// checkoutException 异常到账：写外盒事件供财务处理，尝试置 failed 并释放有效键，返回业务冲突。
func checkoutException(ctx context.Context, db *ent.Client, checkout *ent.EsportsCheckout,
	attempt *ent.EsportsCheckoutAttempt, kind, detail string, now time.Time) (*ent.EsportsCheckoutAttempt, error) {
	_ = now
	_, outboxErr := db.EsportsOutbox.Create().
		SetEventKey("checkout.exception:" + attempt.MerchantNo + ":" + kind).
		SetEventType("checkout.payment_exception").SetAggregateRef(attempt.MerchantNo).
		SetPayload(map[string]interface{}{
			"checkout_id": checkout.ID, "business_type": checkout.BusinessType.String(),
			"business_id": checkout.BusinessID, "attempt_id": attempt.ID,
			"provider": attempt.Provider.String(), "pay_channel": attempt.PayChannel.String(),
			"merchant_no": attempt.MerchantNo, "amount_cents": checkout.AmountCents,
			"exception_kind": kind, "detail": truncateText(detail, 300),
		}).Save(ctx)
	if outboxErr != nil && !ent.IsConstraintError(outboxErr) {
		return nil, outboxErr
	}
	if err := markCheckoutAttemptFailed(ctx, db, attempt, kind); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w：聚合收款异常到账（%s），已转财务处理", errConflict, kind)
}

// compensateCheckoutPayments 定时查单补偿：
//   - open 且业务时限已过的入口置 expired；
//   - pending / creating 尝试先查单：确认成功 → 结算；服务商明确关闭 → failed；
//     已过尝试有效期且查单未成功 → expired；
//   - 查单网络错误不影响其它尝试，返回首个错误供任务日志。
func compensateCheckoutPayments(ctx context.Context, db *ent.Client, now time.Time,
	limit int) (queried, settled, expired int, err error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	// 入口惰性过期兜底。
	checkouts, lookupErr := db.EsportsCheckout.Query().Where(
		esportscheckout.StatusEQ(esportscheckout.StatusOpen),
		esportscheckout.BusinessExpiresAtLTE(now.UTC()),
	).Limit(limit).All(ctx)
	if lookupErr != nil {
		return 0, 0, 0, lookupErr
	}
	for _, checkout := range checkouts {
		if _, expireErr := expireCheckoutIfDue(ctx, db, checkout, now); expireErr != nil {
			return queried, settled, expired, expireErr
		}
	}
	attempts, lookupErr := db.EsportsCheckoutAttempt.Query().Where(
		esportscheckoutattempt.StatusIn(
			esportscheckoutattempt.StatusCreating, esportscheckoutattempt.StatusPending),
	).Order(ent.Asc(esportscheckoutattempt.FieldExpiresAt)).Limit(limit).All(ctx)
	if lookupErr != nil {
		return 0, 0, 0, lookupErr
	}
	providers := map[string]checkoutProvider{}
	for _, attempt := range attempts {
		provider := providers[attempt.Provider.String()]
		if provider == nil {
			built, buildErr := checkoutProviderFor(ctx, db, attempt.Provider.String())
			if buildErr != nil {
				if err == nil {
					err = buildErr
				}
				// 服务商配置被移除时也要把超时尝试收敛为 expired，避免 pending 永久堆积。
				if !now.UTC().Before(attempt.ExpiresAt) {
					if expireErr := expireCheckoutAttempt(ctx, db, attempt.ID, now); expireErr == nil {
						expired++
					}
				}
				continue
			}
			provider = built
			providers[attempt.Provider.String()] = provider
		}
		queried++
		notice, queryErr := provider.QueryPayment(ctx, attempt.MerchantNo, attempt.CreatedAt)
		if queryErr != nil {
			if err == nil {
				err = queryErr
			}
			if !now.UTC().Before(attempt.ExpiresAt) {
				if expireErr := expireCheckoutAttempt(ctx, db, attempt.ID, now); expireErr == nil {
					expired++
				}
			}
			continue
		}
		if notice != nil && notice.Paid {
			if _, settleErr := settleCheckoutPayment(ctx, db, attempt.Provider.String(), notice, now); settleErr == nil {
				settled++
				continue
			} else if err == nil {
				err = settleErr
			}
		}
		if notice != nil && checkoutProviderTerminalFailure(notice.Status) && !notice.Paid {
			if failErr := markCheckoutAttemptFailed(ctx, db, attempt, "服务商关闭"); failErr == nil {
				expired++
			}
			continue
		}
		if !now.UTC().Before(attempt.ExpiresAt) {
			if expireErr := expireCheckoutAttempt(ctx, db, attempt.ID, now); expireErr == nil {
				expired++
			}
		}
	}
	return queried, settled, expired, err
}

// checkoutProviderTerminalFailure 服务商明确不会再成功的状态（关单 / 撤销 / 失败）。
func checkoutProviderTerminalFailure(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "CANCELED", "CANCELLED", "REVOKED", "FAILED", "PAYERROR", "CLOSED", "F":
		return true
	}
	return false
}

// customerPaymentMethodsForBusiness GET /user/payment-methods?business_type=&business_id= 的扩展视图：
// 校验业务归属后返回动态支付可用性、可用的服务商渠道与固定码账户。
func customerPaymentMethodsForBusiness(ctx context.Context, db *ent.Client,
	customer *ent.EsportsCustomer, businessType string, businessID int, now time.Time) (*checkoutPaymentMethodsView, error) {
	businessType = strings.TrimSpace(businessType)
	if customer == nil || customer.ID < 1 || businessID < 1 || !checkoutBusinessTypeValid(businessType) {
		return nil, errInvalidInput
	}
	// 归属校验不要求业务仍可支付（已付款也要能展示收款方式），因此单独轻量校验。
	if err := ensureCheckoutBusinessOwnership(ctx, db, customer.ID, businessType, businessID); err != nil {
		return nil, err
	}
	methods, err := listCustomerPaymentMethods(ctx, db)
	if err != nil {
		return nil, err
	}
	view := &checkoutPaymentMethodsView{Methods: methods, ManualMethods: []customerPaymentMethodView{}}
	for _, method := range methods {
		if method.Kind == "external" {
			view.ManualMethods = append(view.ManualMethods, method)
		}
	}
	availability := checkoutAggregateAvailability{Channels: map[string]bool{"wechat": false, "alipay": false}}
	wechat, alipay, providers, availabilityErr := checkoutChannelAvailability(ctx, db)
	if availabilityErr != nil {
		availability.Reason = availabilityErr.Error()
	} else {
		availability.Providers = providers
		availability.Channels = map[string]bool{"wechat": wechat, "alipay": alipay}
		availability.Available = len(providers) > 0
		if !availability.Available {
			availability.Reason = "聚合收款服务商未启用或配置不完整"
		}
	}
	existing, lookupErr := db.EsportsCheckout.Query().Where(
		esportscheckout.BusinessTypeEQ(esportscheckout.BusinessType(businessType)),
		esportscheckout.BusinessIDEQ(businessID),
	).Only(ctx)
	if lookupErr == nil {
		existing, _ = expireCheckoutIfDue(ctx, db, existing, now)
		availability.CheckoutID = existing.ID
		availability.CheckoutMode = existing.ChannelMode.String()
		if existing.Status != esportscheckout.StatusOpen {
			availability.Available = false
			availability.Reason = "聚合收款入口已" + checkoutStatusText(existing.Status.String())
		} else if existing.ChannelMode == esportscheckout.ChannelModeManual {
			availability.Available = false
			availability.Reason = "该业务单已选择固定码收款"
		}
	}
	if businessType == checkoutBusinessTypeOrder && availability.Available {
		pending, pendingErr := otherPendingPaymentProofExists(ctx, db.EsportsOfflinePaymentProof.Query(),
			businessID, 0)
		if pendingErr != nil {
			return nil, pendingErr
		}
		if pending {
			availability.Available = false
			availability.Reason = "该订单已有待核验的固定码 / 转账凭证"
		}
	}
	view.Aggregate = availability
	return view, nil
}

// ensureCheckoutBusinessOwnership 轻量归属校验（不要求业务仍可支付）。
func ensureCheckoutBusinessOwnership(ctx context.Context, db *ent.Client,
	customerID int, businessType string, businessID int) error {
	switch businessType {
	case checkoutBusinessTypeOrder:
		order, err := db.EsportsOrder.Get(ctx, businessID)
		if err != nil {
			return err
		}
		if order.CustomerID != customerID {
			return errForbidden
		}
	case checkoutBusinessTypeRecharge:
		recharge, err := db.EsportsRechargeIntent.Get(ctx, businessID)
		if err != nil {
			return err
		}
		if recharge.CustomerID != customerID {
			return errForbidden
		}
	case checkoutBusinessTypeGift:
		gift, err := db.EsportsGiftIntent.Get(ctx, businessID)
		if err != nil {
			return err
		}
		if gift.CustomerID != customerID {
			return errForbidden
		}
	default:
		return errInvalidInput
	}
	return nil
}

// errorsIs 不再需要：统一使用标准库 errors.Is。

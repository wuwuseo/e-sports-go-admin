package esports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsgiftintent"
	"go-admin/internal/ent/systemconfig"
)

// 礼物「直接支付 / 发起收款」。
//
// 背景：礼物原本只接受「客户余额本金」与「储值卡本金」两种付款来源，余额不足即报错。
// 本链路补上第三种来源：余额与卡本金都不足时，客户（或客服代办）对礼物确认单**发起收款**，
// 由在线渠道回调或财务人工核销确认到账后再成单。
//
// 资金安全：
//   - 发起收款前先校验渠道启停（未开通渠道明确失败，不生成收款意图、不伪成功）；
//   - 在线渠道（wechat_work）额外要求网关可用 + 存在启用中的收款账户（与订单收款同口径）；
//   - 人工核销须由「非发起人」执行（职责分离）；调用方是登录管理员，自审一律 403；
//   - 渠道回调没有管理员身份，由系统代持操作人（`giftPaymentSystemOperator`），
//     但仍以验签通过 + 金额一致为前提；
//   - 外部收款不产生钱包流水（钱没有进入预付账户），只在礼物单上留收款凭证。

const (
	// 礼物收款时限（分钟）：缺省 1440（24 小时），允许 1 分钟–7 天。
	giftPaymentWindowKey            = "app.esports.gift_payment_window_minutes"
	giftPaymentWindowDefaultMinutes = 24 * 60
	giftPaymentWindowMaxMinutes     = 7 * 24 * 60
	// 礼物收款意图号的保留前缀。
	giftPaymentIntentPrefix = "GIFTPAY"
	// 渠道回调代持操作人：回调没有管理员身份，用固定系统账号标识留痕。
	giftPaymentSystemOperator = 1
)

// errGiftPaymentRejected 礼物收款被人工驳回（非异常，是业务终态，调用方按提示返回）。
var errGiftPaymentRejected = errors.New("礼物收款已驳回")

// giftPaymentStartInput 是发起礼物收款的入参。
type giftPaymentStartInput struct {
	Channel   string `json:"channel"`
	ClientKey string `json:"client_key"`
}

// giftPaymentVerifyInput 是核销礼物收款的入参：人工核销与在线回调共用同一落库边界。
type giftPaymentVerifyInput struct {
	Approve       bool   `json:"approve"`
	PaymentRef    string `json:"payment_ref"`
	ReceivedCents int64  `json:"received_cents"`
	EvidenceNote  string `json:"evidence_note"`
}

// giftPaymentView 是礼物收款意图视图。
type giftPaymentView struct {
	IntentID        int       `json:"intent_id"`
	PaymentIntentNo string    `json:"payment_intent_no"`
	Channel         string    `json:"channel"`
	PaymentStatus   string    `json:"payment_status"`
	PayableCents    int64     `json:"payable_cents"`
	PayURL          string    `json:"pay_url"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// giftPaymentWindow 礼物收款时限：配置缺失或非法时回落 24 小时。
func giftPaymentWindow(ctx context.Context, db *ent.Client) time.Duration {
	fallback := time.Duration(giftPaymentWindowDefaultMinutes) * time.Minute
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(giftPaymentWindowKey)).Only(ctx)
	if err != nil {
		return fallback
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || minutes < 1 || minutes > giftPaymentWindowMaxMinutes {
		return fallback
	}
	return time.Duration(minutes) * time.Minute
}

// newGiftPaymentIntentNo 生成礼物收款意图号（渠道流水 / 人工核销凭证按号定位）。
func newGiftPaymentIntentNo() string {
	raw := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))
	return giftPaymentIntentPrefix + raw[:12]
}

// startGiftPayment 对未终态的礼物确认单发起收款：
//   - 渠道必须有效且**未停用**（停用 → 明确失败，不生成收款意图）；
//   - 在线渠道须网关可用且有启用中的收款账户（未配置 → 明确失败，不伪成功）；
//   - 同一确认单重复发起同一渠道时幂等返回既有收款意图。
func startGiftPayment(ctx context.Context, db *ent.Client, adminID int, isSuper bool,
	intentID int, in giftPaymentStartInput, now time.Time) (*giftPaymentView, error) {
	in.Channel = strings.TrimSpace(in.Channel)
	if intentID < 1 || adminID < 1 || !validGiftPaymentChannel(in.Channel) {
		return nil, errInvalidInput
	}
	intent, err := db.EsportsGiftIntent.Get(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if _, err = loadOwnedCustomer(ctx, db, adminID, isSuper, intent.CustomerID); err != nil {
		return nil, err
	}
	// 聚合收款互斥：礼物确认单已有进行中的 checkout 动态支付尝试时，不再从旧收款入口另起一笔。
	if err = ensureNoActiveCheckoutAttempt(ctx, db, checkoutBusinessTypeGift, intent.ID); err != nil {
		return nil, err
	}
	if intent.Status == esportsgiftintent.StatusConsumed || intent.Status == esportsgiftintent.StatusCancelled {
		return nil, errConflict
	}
	if !now.UTC().Before(intent.ExpiresAt) {
		return nil, fmt.Errorf("%w：礼物确认单已过期，请重新发起", errConflict)
	}
	// 幂等：已对同一确认单发起过同一渠道的收款，直接返回既有收款意图。
	if intent.PaymentIntentNo != nil && intent.PaymentChannel.String() == in.Channel &&
		intent.PaymentStatus == esportsgiftintent.PaymentStatusAwaitingPayment {
		return giftPaymentViewOf(intent, ""), nil
	}
	// 渠道启停：未开通不得发起，不伪成功。
	if !paymentChannelEnabled(ctx, db, in.Channel) {
		return nil, fmt.Errorf("%w：收款方式 %s 已停用", errPaymentGatewayUnavailable, in.Channel)
	}
	expiresAt := now.UTC().Add(giftPaymentWindow(ctx, db))
	var payURL string
	if giftPaymentSourceExternalChannel(in.Channel) {
		gateway, err := gatewayFor(ctx, db, in.Channel)
		if err != nil {
			return nil, err
		}
		accounts, err := listPaymentAccounts(ctx, db, in.Channel, "active", 1)
		if err != nil {
			return nil, err
		}
		if len(accounts) == 0 {
			return nil, fmt.Errorf("%w：渠道 %s 暂无启用中的收款账户", errPaymentGatewayUnavailable, in.Channel)
		}
		payURL, err = gateway.CreatePayment(ctx, giftPaymentIntentNoFor(intent.ID), intent.AmountCents,
			expiresAt, "礼物 "+intent.GiftName+"（确认单 "+strconv.Itoa(intent.ID)+"）")
		if err != nil {
			return nil, err
		}
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := tx.EsportsGiftIntent.Get(ctx, intent.ID)
	if err != nil {
		return nil, err
	}
	if current.PaymentIntentNo != nil || current.PaymentStatus != esportsgiftintent.PaymentStatusNotRequired {
		return nil, errConflict
	}
	intentNo := newGiftPaymentIntentNo()
	changed, err := tx.EsportsGiftIntent.Update().Where(
		esportsgiftintent.IDEQ(current.ID), esportsgiftintent.VersionEQ(current.Version),
		esportsgiftintent.PaymentStatusEQ(esportsgiftintent.PaymentStatusNotRequired),
	).SetPaymentChannel(esportsgiftintent.PaymentChannel(in.Channel)).
		SetPaymentStatus(esportsgiftintent.PaymentStatusAwaitingPayment).
		SetPaymentIntentNo(intentNo).SetPaymentExpiresAt(expiresAt).AddVersion(1).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("gift.payment_started:" + intentNo).
		SetEventType("gift.payment_started").SetAggregateRef(intentNo).
		SetPayload(map[string]interface{}{
			"intent_id": current.ID, "customer_id": current.CustomerID,
			"channel": in.Channel, "amount_cents": current.AmountCents,
		}).Save(ctx); err != nil && !ent.IsConstraintError(err) {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	updated, err := db.EsportsGiftIntent.Get(ctx, intent.ID)
	if err != nil {
		return nil, err
	}
	return giftPaymentViewOf(updated, payURL), nil
}

// giftPaymentIntentNoFor 在线网关用的收款单号（与库内 gift intent 一一对应，便于回调定位）。
func giftPaymentIntentNoFor(intentID int) string {
	sum := sha256.Sum256([]byte("gift:" + strconv.Itoa(intentID)))
	return giftPaymentIntentPrefix + strings.ToUpper(hex.EncodeToString(sum[:])[:12])
}

// giftPaymentViewOf 组装礼物收款意图视图。
func giftPaymentViewOf(intent *ent.EsportsGiftIntent, payURL string) *giftPaymentView {
	view := &giftPaymentView{
		IntentID: intent.ID, Channel: intent.PaymentChannel.String(),
		PaymentStatus: intent.PaymentStatus.String(), PayableCents: intent.AmountCents,
		PayURL: payURL,
	}
	if intent.PaymentIntentNo != nil {
		view.PaymentIntentNo = *intent.PaymentIntentNo
	}
	if intent.PaymentExpiresAt != nil {
		view.ExpiresAt = *intent.PaymentExpiresAt
	}
	return view
}

// verifyGiftPayment 财务人工核销礼物收款：核对到账凭证后把收款意图置 verified
// 并按外部收款来源成单；approve=false 驳回并把收款意图置 failed（明确失败，不伪成功）。
// 职责分离：创建该礼物确认单的经办人不得自行核销到账。
func verifyGiftPayment(ctx context.Context, db *ent.Client, adminID int, isSuper bool, intentID int,
	in giftPaymentVerifyInput, now time.Time) (*ent.EsportsGiftOrder, error) {
	// 聚合收款互斥：动态支付在途时不得人工核销礼物收款，避免同一确认单两边同时成单。
	if err := ensureNoActiveCheckoutAttempt(ctx, db, checkoutBusinessTypeGift, intentID); err != nil {
		return nil, err
	}
	return settleGiftPayment(ctx, db, adminID, isSuper, intentID, in, now, true)
}

// settleGiftPayment 礼物收款核销实现。enforceSeparation 为 true 时校验「不得自审」，
// 渠道回调路径以系统操作人代持、不适用该人工职责分离校验。
func settleGiftPayment(ctx context.Context, db *ent.Client, adminID int, isSuper bool, intentID int,
	in giftPaymentVerifyInput, now time.Time, enforceSeparation bool) (*ent.EsportsGiftOrder, error) {
	in.PaymentRef = strings.TrimSpace(in.PaymentRef)
	in.EvidenceNote = strings.TrimSpace(in.EvidenceNote)
	if adminID < 1 || intentID < 1 || len([]rune(in.EvidenceNote)) < 8 || len([]rune(in.EvidenceNote)) > 255 {
		return nil, errInvalidInput
	}
	intent, err := db.EsportsGiftIntent.Get(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if intent.PaymentStatus != esportsgiftintent.PaymentStatusAwaitingPayment ||
		intent.PaymentIntentNo == nil || intent.PaymentChannel.String() == "none" {
		return nil, errConflict
	}
	if enforceSeparation && intent.CreatedByAdminID == adminID {
		return nil, fmt.Errorf("%w：创建该礼物确认单的经办人不得自行核销到账", errForbidden)
	}
	if !paymentChannelEnabled(ctx, db, intent.PaymentChannel.String()) {
		return nil, fmt.Errorf("%w：收款方式已停用，无法核销", errForbidden)
	}
	if intent.PaymentExpiresAt != nil && now.UTC().After(*intent.PaymentExpiresAt) {
		return nil, fmt.Errorf("%w：礼物收款已超时，请让客户重新发起收款", errConflict)
	}
	if !in.Approve {
		// 驳回是明确的业务终态：收款意图置 failed，不产生任何资金或礼物单据变更。
		_, err = db.EsportsGiftIntent.Update().Where(
			esportsgiftintent.IDEQ(intent.ID),
			esportsgiftintent.PaymentStatusEQ(esportsgiftintent.PaymentStatusAwaitingPayment),
		).SetPaymentStatus(esportsgiftintent.PaymentStatusFailed).
			SetPaymentRef(in.PaymentRef).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		return nil, errGiftPaymentRejected
	}
	if in.PaymentRef == "" || in.ReceivedCents != intent.AmountCents {
		return nil, fmt.Errorf("%w：到账凭证号或实收金额与礼物收款不一致", errInvalidInput)
	}
	changed, err := db.EsportsGiftIntent.Update().Where(
		esportsgiftintent.IDEQ(intent.ID),
		esportsgiftintent.PaymentStatusEQ(esportsgiftintent.PaymentStatusAwaitingPayment),
	).SetPaymentStatus(esportsgiftintent.PaymentStatusVerified).
		SetPaymentRef(in.PaymentRef).
		SetPaymentVerifiedAt(now.UTC()).SetPaymentVerifiedByAdminID(adminID).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	// 已核验 → 以外部收款来源成单（sendGiftOnce 会再次校验核验状态与金额）。
	return executeGiftIntent(ctx, db, adminID, isSuper, intent.ID, now)
}

// handleGiftPaymentNotification 在线渠道回调核销礼物收款（，AuthPublic）：
// 按收款单号定位礼物确认单 → 网关验签 → 标记到账 → 成单。未开通渠道 / 验签失败 / 金额不符
// 一律明确失败，不做任何资金或单据变更。
func handleGiftPaymentNotification(ctx context.Context, db *ent.Client, channel string,
	body []byte, signature string, now time.Time) (*ent.EsportsGiftOrder, error) {
	channel = strings.TrimSpace(channel)
	if !validGiftPaymentChannel(channel) || len(body) == 0 || strings.TrimSpace(signature) == "" {
		return nil, errInvalidInput
	}
	gateway, err := gatewayFor(ctx, db, channel)
	if err != nil {
		return nil, err
	}
	payment, err := gateway.VerifyNotification(ctx, body, signature)
	if err != nil {
		return nil, err
	}
	if payment.Channel != channel {
		return nil, fmt.Errorf("%w：回调渠道与验签渠道不一致", errForbidden)
	}
	intent, err := giftIntentByPaymentNo(ctx, db, payment.IntentNo)
	if err != nil {
		return nil, err
	}
	operator := intent.CreatedByAdminID
	if operator < 1 {
		operator = giftPaymentSystemOperator
	}
	return settleGiftPayment(ctx, db, operator, false, intent.ID, giftPaymentVerifyInput{
		Approve: true, PaymentRef: payment.TransactionNo, ReceivedCents: payment.AmountCents,
		EvidenceNote: "在线渠道回调验签通过：" + payment.TransactionNo,
	}, payment.ConfirmedAt, false)
}

// giftIntentByPaymentNo 按收款意图号定位礼物确认单（回调分流依据）。
func giftIntentByPaymentNo(ctx context.Context, db *ent.Client, paymentIntentNo string) (*ent.EsportsGiftIntent, error) {
	paymentIntentNo = strings.TrimSpace(paymentIntentNo)
	if paymentIntentNo == "" {
		return nil, errInvalidInput
	}
	intent, err := db.EsportsGiftIntent.Query().Where(
		esportsgiftintent.PaymentIntentNoEQ(paymentIntentNo)).Only(ctx)
	if err != nil {
		return nil, err
	}
	return intent, nil
}

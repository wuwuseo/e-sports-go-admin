package esports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsconfirmation"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsidempotency"
	"go-admin/internal/ent/esportsidentitybinding"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportspaymentreceipt"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/esportsstoredvaluepackage"
	"go-admin/internal/ent/systemconfig"
)

// 客户自助购买储值卡（§4.4 / §13.2）。
//
// 与客服代办开卡共用同一套落库口径（openStoredValueCard / verifyStoredValueCardPayment），自助端只增加三件事：
//  1. 合同确认：客户必须显式确认开卡合同，落一条「绑定登录身份」的确认书（EsportsConfirmation）；
//  2. 支付意图：为本次购卡生成独立于订单的支付意图（EsportsPaymentIntent），客户凭付款码外部付款；
//  3. 数据边界：一律按「登录用户绑定的客户」隔离，风控与成年资格按既有自助入口口径拦截。
//
// 资金安全：自助发起的卡一律零余额、status=pending_verification，只有财务核验（或已验签通道回调）确认实际
// 到账后才激活并发卡内余额与预付款批次——自助端没有任何「跳过核验直接入账」的路径。
const (
	// 自助购卡的支付意图落在「订单中心化」的 EsportsPaymentIntent 上：该表以 order_id 为主维度，
	// 而自助购卡没有订单。为不改动他人 schema，卡类意图用保留段（≥ storedValueIntentOrderBase）编码卡 ID，
	// 保证不会与真实订单 ID 相撞而被订单侧查询（线下凭证核销 / 取消 / 过期）误命中。
	storedValueIntentOrderBase = 1_000_000_000
	// 自助购卡幂等操作名（与 create_order 同口径，按 principal=customer:<id> 隔离）。
	customerStoredValuePurchaseOperation = "customer_stored_value_purchase"
	// 自助购卡支付意图有效期（分钟）：缺省 24 小时，允许 1 分钟–7 天。
	customerStoredValuePayWindowKey            = "app.esports.stored_value_payment_window_minutes"
	customerStoredValuePayWindowDefaultMinutes = 24 * 60
	customerStoredValuePayWindowMaxMinutes     = 7 * 24 * 60
	// 本金档与有效期上下限：避免零元卡与超长期卡。
	customerStoredValueMinPrincipalCents = 100
	customerStoredValueMaxPrincipalCents = 100_000_000
	customerStoredValueDefaultValidDays  = 365
	customerStoredValueMaxValidDays      = 3650
	// 合同确认书的主题与渠道口径：主题为储值卡合同，渠道固定为「已认证自助」。
	customerStoredValueContractSubject = "stored_value_contract"
	customerStoredValueSelfChannel     = "authenticated_self_service"
)

// customerStoredValuePurchaseInput 是自助购卡入参（§4.4）：选定套餐时本金与赠送由套餐决定（套餐优先），
// 未选套餐时用本金档并按住户会员等级比例赠送。
type customerStoredValuePurchaseInput struct {
	PackageID      int    `json:"package_id"`
	PrincipalCents int64  `json:"principal_cents"`
	PaymentChannel string `json:"payment_channel"`
	ValidityDays   int    `json:"validity_days"`
	ValidUntil     string `json:"valid_until"`
	// 仅为显式拒绝客户端传入赠送权益（赠送只能来自套餐或会员等级比例），不接受任何取值。
	BonusCents        int64  `json:"bonus_cents"`
	ContractConfirmed bool   `json:"contract_confirmed"`
	IdempotencyKey    string `json:"idempotency_key"`
}

// customerStoredValuePurchaseView 是自助购卡申请视图：卡（待核验）+ 合同确认 + 支付意图与付款指引。
type customerStoredValuePurchaseView struct {
	Card                *storedValueCardView `json:"card"`
	NextAction          string               `json:"next_action"`
	CardSource          string               `json:"card_source"`
	PackageID           int                  `json:"package_id"`
	PackageName         string               `json:"package_name"`
	ConfirmationID      int                  `json:"confirmation_id"`
	ContractConfirmedAt time.Time            `json:"contract_confirmed_at"`
	IntentNo            string               `json:"intent_no"`
	IntentStatus        string               `json:"intent_status"`
	PaymentChannel      string               `json:"payment_channel"`
	PaymentRef          string               `json:"payment_ref"`
	PayableCents        int64                `json:"payable_cents"`
	PayWindowExpiresAt  time.Time            `json:"pay_window_expires_at"`
}

// customerStoredValueSettlementInput 是「已验签通道适配器」核销自助购卡到账的入参。
// 与 payment.go 的 verifiedPayment 同口径：只能由已完成签名与到账证据校验的通道适配器构造，无公开路由。
type customerStoredValueSettlementInput struct {
	IntentNo        string
	Channel         string
	TransactionNo   string
	ReceivedCents   int64
	ConfirmedAt     time.Time
	EvidenceNote    string
	OperatorAdminID int
}

// customerStoredValueIntentOrderID 把卡 ID 编码进支付意图的 order_id 保留段。
func customerStoredValueIntentOrderID(cardID int) int {
	if cardID < 1 {
		return 0
	}
	return storedValueIntentOrderBase + cardID
}

// customerStoredValueIntentCardID 还原保留段里的卡 ID；非卡类意图返回 0。
func customerStoredValueIntentCardID(orderID int) int {
	if orderID <= storedValueIntentOrderBase {
		return 0
	}
	return orderID - storedValueIntentOrderBase
}

// customerSelfPrincipal 自助入口的幂等主体：与 placeCustomerSelfOrder 同一口径。
func customerSelfPrincipal(customerID int) string {
	return "customer:" + strconv.Itoa(customerID)
}

// customerStoredValueOpenKey 由客户身份 + 客户端幂等键派生卡上的唯一开卡请求键：
// 同一客户重复提交命中同一键（唯一索引拒绝重复建卡），不同客户即使撞键也互不影响。
func customerStoredValueOpenKey(customerID int, key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return fmt.Sprintf("customer:%d:sv:%s", customerID, hex.EncodeToString(sum[:])[:40])
}

func newCustomerStoredValuePayRef() string {
	raw := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))
	return "SVPAY" + raw[:12]
}

// customerStoredValuePayWindow 支付意图有效期：配置缺失或非法时回落 24 小时。
func customerStoredValuePayWindow(ctx context.Context, db *ent.Client) time.Duration {
	fallback := time.Duration(customerStoredValuePayWindowDefaultMinutes) * time.Minute
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(customerStoredValuePayWindowKey)).Only(ctx)
	if err != nil {
		return fallback
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || minutes < 1 || minutes > customerStoredValuePayWindowMaxMinutes {
		return fallback
	}
	return time.Duration(minutes) * time.Minute
}

// validateCustomerStoredValuePurchase 自助购卡入参校验（任一不满足即拒绝，不写任何数据）。
func validateCustomerStoredValuePurchase(in customerStoredValuePurchaseInput) error {
	key := strings.TrimSpace(in.IdempotencyKey)
	if len(key) < 8 || len(key) > 128 {
		return fmt.Errorf("%w：幂等键长度须为 8–128", errInvalidInput)
	}
	// 合同确认是自助开卡的前置：未显式确认不得建卡（与 validateStoredValueOpen 同一口径）。
	if !in.ContractConfirmed {
		return fmt.Errorf("%w：自助购卡前须确认储值卡合同", errInvalidInput)
	}
	// 赠送权益一律由服务端口径决定（套餐 > 会员等级比例），不接受客户端传入。
	if in.BonusCents != 0 {
		return fmt.Errorf("%w：自助购卡的赠送权益由套餐或会员等级决定，不接受客户端传入", errInvalidInput)
	}
	switch in.PaymentChannel {
	case "wechat_work", "merchant_qr", "corporate_transfer":
	case "customer_balance":
		return fmt.Errorf("%w：自助购卡不走余额划转，请选择外部收款渠道并由财务核验", errInvalidInput)
	default:
		return fmt.Errorf("%w：自助购卡收款渠道无效", errInvalidInput)
	}
	if in.PackageID < 0 || in.PrincipalCents < 0 {
		return errInvalidInput
	}
	// 套餐优先（与 openStoredValueCard 同一口径）：选定套餐后本金 / 赠送由套餐决定，忽略传入的本金档。
	if in.PackageID == 0 && (in.PrincipalCents < customerStoredValueMinPrincipalCents ||
		in.PrincipalCents > customerStoredValueMaxPrincipalCents) {
		return fmt.Errorf("%w：指定本金档须在 %d–%d 分之间",
			errInvalidInput, customerStoredValueMinPrincipalCents, customerStoredValueMaxPrincipalCents)
	}
	if in.ValidityDays < 0 || in.ValidityDays > customerStoredValueMaxValidDays {
		return fmt.Errorf("%w：有效期天数须为 1–%d", errInvalidInput, customerStoredValueMaxValidDays)
	}
	if raw := strings.TrimSpace(in.ValidUntil); raw != "" {
		if _, err := parseReportTime(raw); err != nil {
			return errInvalidInput
		}
		if in.ValidityDays > 0 {
			return fmt.Errorf("%w：有效期天数与到期日只能选一个", errInvalidInput)
		}
	}
	return nil
}

// customerStoredValuePrincipal 解析本金与赠送（沿用既有口径）：套餐 > 手填（自助端不接受客户端手填）> 会员等级比例。
// 选套餐时本金与赠送由套餐决定；指定本金档只定本金，赠送留 0 交给 openStoredValueCard 按会员等级比例计算。
func customerStoredValuePrincipal(ctx context.Context, db *ent.Client,
	in customerStoredValuePurchaseInput) (principal, bonus int64, packageID int, packageName string, err error) {
	if in.PackageID == 0 {
		return in.PrincipalCents, 0, 0, "", nil
	}
	pkg, err := db.EsportsStoredValuePackage.Get(ctx, in.PackageID)
	if err != nil {
		return 0, 0, 0, "", err
	}
	if pkg.Status != esportsstoredvaluepackage.StatusOnSale {
		return 0, 0, 0, "", fmt.Errorf("%w：该储值卡套餐已下架", errInvalidInput)
	}
	return pkg.PrincipalCents, pkg.BonusCents, pkg.ID, pkg.Name, nil
}

// customerSelfIdentityBinding 取客户当前生效的登录身份绑定：自助购卡必须已绑定登录身份。
func customerSelfIdentityBinding(ctx context.Context, db *ent.Client,
	customerID int) (*ent.EsportsIdentityBinding, error) {
	if customerID < 1 {
		return nil, errForbidden
	}
	binding, err := db.EsportsIdentityBinding.Query().Where(
		esportsidentitybinding.CustomerIDEQ(customerID),
		esportsidentitybinding.ProviderEQ("user"),
		esportsidentitybinding.SubjectScopeEQ(""),
		esportsidentitybinding.StatusEQ(esportsidentitybinding.StatusActive),
	).Order(ent.Asc(esportsidentitybinding.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, fmt.Errorf("%w：该客户尚未绑定登录身份，无法自助购卡", errForbidden)
	}
	if err != nil {
		return nil, err
	}
	return binding, nil
}

func customerStoredValuePurchaseRequestHash(in customerStoredValuePurchaseInput) string {
	in.IdempotencyKey = ""
	data, _ := json.Marshal(in)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// existingCustomerStoredValuePurchase 幂等前置查询：同一客户同一幂等键已受理时直接返回原申请。
// 请求内容与首次不一致（哈希不符）返回冲突，避免「换个本金复用同一键」。
func existingCustomerStoredValuePurchase(ctx context.Context, db *ent.Client, customerID int,
	key, hash string) (*customerStoredValuePurchaseView, error) {
	record, err := db.EsportsIdempotency.Query().Where(
		esportsidempotency.OperationEQ(customerStoredValuePurchaseOperation),
		esportsidempotency.PrincipalEQ(customerSelfPrincipal(customerID)),
		esportsidempotency.RequestKeyEQ(key),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if record.RequestHash != hash {
		return nil, fmt.Errorf("%w：同一幂等键的购卡请求内容不一致", errConflict)
	}
	card, err := db.EsportsStoredValueCard.Query().Where(
		esportsstoredvaluecard.CardNoEQ(record.ResultRef),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if card.CustomerID != customerID {
		return nil, errForbidden
	}
	return customerStoredValuePurchaseViewOf(ctx, db, card)
}

// purchaseCustomerStoredValueCard 客户自助购卡（§4.4）：须已绑定登录身份、成年、未被限制自助下单且已分配归属客服；
// 套餐或指定本金档二选一；建卡后落合同确认书与支付意图。卡先为零余额待核验，核验到账后才入账。
func purchaseCustomerStoredValueCard(ctx context.Context, db *ent.Client, customer *ent.EsportsCustomer,
	in customerStoredValuePurchaseInput, now time.Time) (*customerStoredValuePurchaseView, error) {
	if customer == nil || customer.ID < 1 {
		return nil, errForbidden
	}
	// 风险与资格拦截（§13.7 / §4.4）：与客户自助下单同一口径。
	if err := ensureCustomerSelfServiceAllowed(customer); err != nil {
		return nil, err
	}
	if customer.AgeStatus != esportscustomer.AgeStatusAdult {
		return nil, errAgeUnverified
	}
	if customer.OwnerAdminID == nil || *customer.OwnerAdminID < 1 {
		return nil, fmt.Errorf("%w：该客户尚未分配归属客服，请联系客服购卡", errForbidden)
	}
	if err := validateCustomerStoredValuePurchase(in); err != nil {
		return nil, err
	}
	// 渠道启停校验（§4.3-08 / §4.3-03）：停用的收款渠道不得发起自助购卡（不建卡、不伪成功）。
	if !paymentChannelEnabled(ctx, db, in.PaymentChannel) {
		return nil, fmt.Errorf("%w：收款方式 %s 已停用", errPaymentGatewayUnavailable, in.PaymentChannel)
	}
	// 登录身份绑定既是数据隔离依据，也是合同确认书的证据来源。
	if _, err := customerSelfIdentityBinding(ctx, db, customer.ID); err != nil {
		return nil, err
	}
	principal, bonus, packageID, packageName, err := customerStoredValuePrincipal(ctx, db, in)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(in.IdempotencyKey)
	hash := customerStoredValuePurchaseRequestHash(in)
	if prior, err := existingCustomerStoredValuePurchase(ctx, db, customer.ID, key, hash); err != nil || prior != nil {
		return prior, err
	}
	validityDays := in.ValidityDays
	if strings.TrimSpace(in.ValidUntil) == "" && validityDays == 0 {
		validityDays = customerStoredValueDefaultValidDays
	}
	card, err := openStoredValueCard(ctx, db, 0, storedValueOpenInput{
		CustomerID: customer.ID, Source: "self", PaymentChannel: in.PaymentChannel,
		PaymentRef: newCustomerStoredValuePayRef(), PackageID: packageID,
		PrincipalCents: principal, BonusCents: bonus,
		ContractConfirmed: true, ValidityDays: validityDays, ValidUntil: in.ValidUntil,
		Note:           "客户自助购卡（外部收款待财务核验）",
		IdempotencyKey: customerStoredValueOpenKey(customer.ID, key),
	}, now)
	if err != nil {
		if !errors.Is(err, errConflict) {
			return nil, err
		}
		// 重复提交（或并发提交）：卡已存在，落到同一张卡上补齐确认书 / 支付意图 / 幂等记录。
		existing, lookupErr := db.EsportsStoredValueCard.Query().Where(
			esportsstoredvaluecard.OpenRequestKeyEQ(customerStoredValueOpenKey(customer.ID, key)),
		).Only(ctx)
		if lookupErr != nil {
			return nil, err
		}
		if existing.CustomerID != customer.ID {
			return nil, errForbidden
		}
		card = &storedValueCardView{EsportsStoredValueCard: existing}
	}
	return finalizeCustomerStoredValuePurchase(ctx, db, customer, card.EsportsStoredValueCard,
		packageName, hash, key, now)
}

// finalizeCustomerStoredValuePurchase 幂等地补齐自助购卡的三份留痕：合同确认书、支付意图与幂等记录。
// 全部写入同一事务；卡已核验 / 已驳回时只读返回，不再改动任何资金。
func finalizeCustomerStoredValuePurchase(ctx context.Context, db *ent.Client, customer *ent.EsportsCustomer,
	card *ent.EsportsStoredValueCard, packageName, hash, key string, now time.Time) (*customerStoredValuePurchaseView, error) {
	binding, err := customerSelfIdentityBinding(ctx, db, customer.ID)
	if err != nil {
		return nil, err
	}
	deadline := now.UTC().Add(customerStoredValuePayWindow(ctx, db))
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := tx.EsportsStoredValueCard.Get(ctx, card.ID)
	if err != nil {
		return nil, err
	}
	if current.CustomerID != customer.ID {
		return nil, errForbidden
	}
	if current.Status != esportsstoredvaluecard.StatusPendingVerification {
		// 已激活 / 已驳回：重复提交只返回现状。
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return customerStoredValuePurchaseViewOf(ctx, db, current)
	}
	// 自助开卡没有经办客服：清空 opened_by_admin_id，避免把「谁开的卡」记成不存在的管理员。
	if current.OpenedByAdminID != nil {
		if _, err = tx.EsportsStoredValueCard.UpdateOneID(current.ID).
			ClearOpenedByAdminID().Save(ctx); err != nil {
			return nil, err
		}
		current.OpenedByAdminID = nil
	}
	confirmation, err := ensureCustomerStoredValueContract(ctx, tx, customer, current, binding.ID, deadline, now)
	if err != nil {
		return nil, err
	}
	intent, err := ensureCustomerStoredValuePaymentIntent(ctx, tx, current, confirmation.ID, deadline)
	if err != nil {
		return nil, err
	}
	if err = recordCustomerStoredValuePurchase(ctx, tx, customer.ID, key, hash, current); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	view, err := customerStoredValuePurchaseViewOf(ctx, db, current)
	if err != nil {
		return nil, err
	}
	view.PackageName = packageName
	view.IntentNo = intent.IntentNo
	view.IntentStatus = intent.Status.String()
	view.ConfirmationID = confirmation.ID
	return view, nil
}

// ensureCustomerStoredValueContract 落「储值卡合同确认」确认书：以登录身份绑定为证据，不可改只可查。
func ensureCustomerStoredValueContract(ctx context.Context, tx *ent.Tx, customer *ent.EsportsCustomer,
	card *ent.EsportsStoredValueCard, bindingID int, deadline, now time.Time) (*ent.EsportsConfirmation, error) {
	query := tx.EsportsConfirmation.Query().Where(
		esportsconfirmation.SubjectTypeEQ(customerStoredValueContractSubject),
		esportsconfirmation.SubjectIDEQ(card.ID),
		esportsconfirmation.CustomerIDEQ(customer.ID),
		esportsconfirmation.ChannelEQ(customerStoredValueSelfChannel),
	)
	confirmation, err := query.Only(ctx)
	if err == nil {
		return confirmation, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	// sku_id 是确认书的必填商品引用（Positive）；储值卡不属于任何 SKU，此处落卡 ID 作为「商品」引用。
	confirmation, err = tx.EsportsConfirmation.Create().
		SetCustomerID(customer.ID).SetSubjectType(customerStoredValueContractSubject).
		SetSubjectID(card.ID).SetSubjectVersion(card.Version).
		SetSkuID(card.ID).SetQuantity(1).SetAmountCents(card.PrincipalCents).
		SetChannel(customerStoredValueSelfChannel).
		SetEvidenceRef("user_binding:" + strconv.Itoa(bindingID) + ":action:" + uuid.NewString()).
		SetStatus(esportsconfirmation.StatusVerified).SetVerifiedAt(now.UTC()).
		SetExpiresAt(deadline).Save(ctx)
	if err != nil {
		if !ent.IsConstraintError(err) {
			return nil, err
		}
		// 并发重复提交：确认书唯一索引兜底，读回既有记录。
		return query.Only(ctx)
	}
	return confirmation, nil
}

// ensureCustomerStoredValuePaymentIntent 落独立于订单的支付意图：金额为开卡本金（客户实付额），
// 到期时间为付款窗口；客户凭「自助付款码」外部付款，到账后才核验激活。
func ensureCustomerStoredValuePaymentIntent(ctx context.Context, tx *ent.Tx,
	card *ent.EsportsStoredValueCard, confirmationID int, deadline time.Time) (*ent.EsportsPaymentIntent, error) {
	query := tx.EsportsPaymentIntent.Query().Where(esportspaymentintent.IntentNoEQ(card.PaymentRef))
	intent, err := query.Only(ctx)
	if err == nil {
		return intent, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	intent, err = tx.EsportsPaymentIntent.Create().
		SetIntentNo(card.PaymentRef).
		SetOrderID(customerStoredValueIntentOrderID(card.ID)).
		SetConfirmationID(confirmationID).SetQuoteVersion(card.Version).
		SetAmountCents(card.PrincipalCents).SetChannel(string(card.PaymentChannel)).
		SetStatus(esportspaymentintent.StatusPending).SetExpiresAt(deadline).Save(ctx)
	if err != nil {
		if !ent.IsConstraintError(err) {
			return nil, err
		}
		return query.Only(ctx)
	}
	return intent, nil
}

// recordCustomerStoredValuePurchase 写幂等记录与外盒留痕；重复提交只补写缺失的一份。
func recordCustomerStoredValuePurchase(ctx context.Context, tx *ent.Tx, customerID int, key, hash string,
	card *ent.EsportsStoredValueCard) error {
	exists, err := tx.EsportsIdempotency.Query().Where(
		esportsidempotency.OperationEQ(customerStoredValuePurchaseOperation),
		esportsidempotency.PrincipalEQ(customerSelfPrincipal(customerID)),
		esportsidempotency.RequestKeyEQ(key),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		if _, err = tx.EsportsIdempotency.Create().
			SetOperation(customerStoredValuePurchaseOperation).
			SetPrincipal(customerSelfPrincipal(customerID)).SetRequestKey(key).
			SetRequestHash(hash).SetResultRef(card.CardNo).Save(ctx); err != nil {
			if !ent.IsConstraintError(err) {
				return err
			}
		}
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("stored_value.self_purchase:" + card.CardNo).
		SetEventType("stored_value.self_purchase_created").SetAggregateRef(card.CardNo).
		SetPayload(map[string]interface{}{
			"card_no": card.CardNo, "customer_id": customerID, "intent_no": card.PaymentRef,
			"payable_cents": card.PrincipalCents, "payment_channel": string(card.PaymentChannel),
		}).Save(ctx); err != nil && !ent.IsConstraintError(err) {
		return err
	}
	return nil
}

// customerStoredValuePurchaseViewOf 组装购卡申请视图：卡 + 合同确认 + 支付意图与付款指引。
func customerStoredValuePurchaseViewOf(ctx context.Context, db *ent.Client,
	card *ent.EsportsStoredValueCard) (*customerStoredValuePurchaseView, error) {
	if card == nil {
		return nil, errInvalidInput
	}
	view, err := storedValueCardViewOf(ctx, db, card)
	if err != nil {
		return nil, err
	}
	out := &customerStoredValuePurchaseView{
		Card: view, CardSource: card.Source.String(), PayableCents: card.PrincipalCents,
		PaymentChannel: string(card.PaymentChannel), PaymentRef: card.PaymentRef,
		NextAction: customerStoredValueNextAction(card),
	}
	intent, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(card.PaymentRef),
	).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if intent != nil {
		out.IntentNo = intent.IntentNo
		out.IntentStatus = intent.Status.String()
		out.PayWindowExpiresAt = intent.ExpiresAt
	}
	confirmation, err := db.EsportsConfirmation.Query().Where(
		esportsconfirmation.SubjectTypeEQ(customerStoredValueContractSubject),
		esportsconfirmation.SubjectIDEQ(card.ID),
		esportsconfirmation.CustomerIDEQ(card.CustomerID),
	).Order(ent.Desc(esportsconfirmation.FieldID)).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if confirmation != nil {
		out.ConfirmationID = confirmation.ID
		if confirmation.VerifiedAt != nil {
			out.ContractConfirmedAt = *confirmation.VerifiedAt
		}
	}
	if card.PackageID != nil {
		out.PackageID = *card.PackageID
		if pkg, pkgErr := db.EsportsStoredValuePackage.Get(ctx, *card.PackageID); pkgErr == nil {
			out.PackageName = pkg.Name
		}
	}
	return out, nil
}

// customerStoredValueNextAction 给客户端的下一步指引（不参与任何资金判定）。
func customerStoredValueNextAction(card *ent.EsportsStoredValueCard) string {
	switch card.Status {
	case esportsstoredvaluecard.StatusPendingVerification:
		return "pay_and_wait_verification"
	case esportsstoredvaluecard.StatusActive:
		return "ready"
	case esportsstoredvaluecard.StatusRejected:
		return "rejected"
	default:
		return "none"
	}
}

// listCustomerStoredValueCards 客户自助「我的储值卡」（§4.4）：只返回本人卡，按 ID 倒序。
func listCustomerStoredValueCards(ctx context.Context, db *ent.Client, customerID int,
	status string, limit int) ([]*storedValueCardView, error) {
	if customerID < 1 {
		return nil, errForbidden
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsStoredValueCard.Query().Where(esportsstoredvaluecard.CustomerIDEQ(customerID))
	if status = strings.TrimSpace(status); status != "" {
		switch status {
		case "pending_verification", "rejected", "active", "exhausted",
			"refund_pending", "refunded", "expired", "frozen":
			q.Where(esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.Status(status)))
		default:
			return nil, errInvalidInput
		}
	}
	cards, err := q.Order(ent.Desc(esportsstoredvaluecard.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]*storedValueCardView, 0, len(cards))
	for _, card := range cards {
		view, err := storedValueCardViewOf(ctx, db, card)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

// getCustomerStoredValueCard 本人储值卡详情（卡信息 + 派生余额 + 流水）；他人卡返回 403。
func getCustomerStoredValueCard(ctx context.Context, db *ent.Client, customerID, cardID, limit int) (*storedValueCardView, []*ent.EsportsWalletTx, error) {
	if customerID < 1 || cardID < 1 {
		return nil, nil, errInvalidInput
	}
	card, err := db.EsportsStoredValueCard.Get(ctx, cardID)
	if err != nil {
		return nil, nil, err
	}
	if card.CustomerID != customerID {
		return nil, nil, errForbidden
	}
	view, err := storedValueCardViewOf(ctx, db, card)
	if err != nil {
		return nil, nil, err
	}
	transactions, err := listStoredValueCardTransactions(ctx, db, cardID, limit)
	if err != nil {
		return nil, nil, err
	}
	return view, transactions, nil
}

// getCustomerStoredValuePurchase 本人自助购卡申请详情（他人申请 / 非自助卡返回 403）。
func getCustomerStoredValuePurchase(ctx context.Context, db *ent.Client, customerID, cardID int) (*customerStoredValuePurchaseView, error) {
	if customerID < 1 || cardID < 1 {
		return nil, errInvalidInput
	}
	card, err := db.EsportsStoredValueCard.Get(ctx, cardID)
	if err != nil {
		return nil, err
	}
	if card.CustomerID != customerID || card.Source != esportsstoredvaluecard.SourceSelf {
		return nil, errForbidden
	}
	return customerStoredValuePurchaseViewOf(ctx, db, card)
}

// verifyCustomerStoredValuePurchasePayment 财务核验自助购卡到账（§4.4）：复用既有核验落库边界
// （状态、卡内余额、预付款批次、自审拦截都在其中），核验后同步支付意图状态与渠道流水回执。
// approve=false 驳回申请并把支付意图置无效。
func verifyCustomerStoredValuePurchasePayment(ctx context.Context, db *ent.Client, adminID, cardID int,
	approve bool, receivedCents int64, evidenceNote string, now time.Time) (*storedValueCardView, error) {
	card, err := db.EsportsStoredValueCard.Get(ctx, cardID)
	if err != nil {
		return nil, err
	}
	if card.Source != esportsstoredvaluecard.SourceSelf {
		return nil, fmt.Errorf("%w：该卡不是客户自助购卡申请", errInvalidInput)
	}
	receiptRef := card.PaymentRef
	if !approve {
		// 驳回不比对到账流水，传空凭证号即可。
		receiptRef = ""
	}
	view, err := verifyStoredValueCardPayment(ctx, db, adminID, cardID, approve, receiptRef,
		receivedCents, evidenceNote, now)
	if err != nil {
		return nil, err
	}
	status := esportspaymentintent.StatusPaid
	if !approve {
		status = esportspaymentintent.StatusInvalidated
	}
	if err = syncCustomerStoredValueIntentStatus(ctx, db, card.PaymentRef, status); err != nil {
		return nil, err
	}
	if approve {
		if _, err = db.EsportsOutbox.Create().
			SetEventKey("stored_value.card_activated:" + card.CardNo).
			SetEventType("stored_value.card_activated").SetAggregateRef(card.CardNo).
			SetPayload(map[string]interface{}{
				"card_no": card.CardNo, "customer_id": card.CustomerID, "card_source": "self",
				"intent_no": card.PaymentRef, "activated_cents": card.PrincipalCents + card.BonusCents,
			}).Save(ctx); err != nil && !ent.IsConstraintError(err) {
			return nil, err
		}
	}
	return view, nil
}

// settleCustomerStoredValuePurchase 已验签通道适配器核销入口（§4.4 / §4.2 同口径）：
// 只接受已通过签名与到账证据校验的通道通知，校验支付意图—卡—金额—渠道一致后用既有核验流程激活。
// 渠道流水号 (channel, transaction_no) 唯一：同一笔到账只能激活一张卡，重复回调按幂等返回已激活卡。
func settleCustomerStoredValuePurchase(ctx context.Context, db *ent.Client,
	in customerStoredValueSettlementInput, now time.Time) (*storedValueCardView, error) {
	in.IntentNo = strings.TrimSpace(in.IntentNo)
	in.Channel = strings.TrimSpace(in.Channel)
	in.TransactionNo = strings.TrimSpace(in.TransactionNo)
	in.EvidenceNote = strings.TrimSpace(in.EvidenceNote)
	if in.IntentNo == "" || in.Channel == "" || in.TransactionNo == "" || len(in.TransactionNo) > 128 ||
		in.ReceivedCents <= 0 || in.ConfirmedAt.IsZero() || in.OperatorAdminID < 1 ||
		len([]rune(in.EvidenceNote)) < 8 {
		return nil, errInvalidInput
	}
	intent, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(in.IntentNo),
	).Only(ctx)
	if err != nil {
		return nil, err
	}
	cardID := customerStoredValueIntentCardID(intent.OrderID)
	if cardID < 1 {
		return nil, fmt.Errorf("%w：该支付意图不属于客户自助购卡", errInvalidInput)
	}
	card, err := db.EsportsStoredValueCard.Get(ctx, cardID)
	if err != nil {
		return nil, err
	}
	if card.Source != esportsstoredvaluecard.SourceSelf || card.PaymentRef != intent.IntentNo {
		return nil, fmt.Errorf("%w：支付意图与购卡申请不匹配", errInvalidInput)
	}
	if intent.Channel != in.Channel || string(card.PaymentChannel) != in.Channel {
		return nil, fmt.Errorf("%w：到账渠道与购卡申请的收款渠道不一致", errInvalidInput)
	}
	if intent.AmountCents != card.PrincipalCents || in.ReceivedCents != card.PrincipalCents {
		return nil, fmt.Errorf("%w：实收金额与开卡本金不一致", errInvalidInput)
	}
	if in.ConfirmedAt.After(intent.ExpiresAt) {
		return nil, fmt.Errorf("%w：支付意图已过期，请重新发起购卡并由财务人工核验", errConflict)
	}
	note := fmt.Sprintf("通道核销 %s 流水 %s：%s", in.Channel, in.TransactionNo, in.EvidenceNote)
	if len([]rune(note)) > 255 {
		return nil, errInvalidInput
	}
	// 渠道流水唯一：同笔到账重复回调返回已激活卡；被别的单据占用则冲突。
	receipt, err := db.EsportsPaymentReceipt.Query().Where(
		esportspaymentreceipt.ChannelEQ(in.Channel),
		esportspaymentreceipt.TransactionNoEQ(in.TransactionNo),
	).Only(ctx)
	if err == nil {
		if receipt.IntentID != intent.ID || receipt.ActualCents != in.ReceivedCents {
			return nil, fmt.Errorf("%w：该渠道流水已用于其他单据", errConflict)
		}
		reloaded, reloadErr := db.EsportsStoredValueCard.Get(ctx, cardID)
		if reloadErr != nil {
			return nil, reloadErr
		}
		return storedValueCardViewOf(ctx, db, reloaded)
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	reserved, err := db.EsportsPaymentReceipt.Create().SetIntentID(intent.ID).
		SetChannel(in.Channel).SetTransactionNo(in.TransactionNo).
		SetActualCents(in.ReceivedCents).SetAppliedCents(0).
		SetExceptionCents(in.ReceivedCents).SetStatus(esportspaymentreceipt.StatusException).
		SetConfirmedAt(in.ConfirmedAt.UTC()).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	view, verifyErr := verifyCustomerStoredValuePurchasePayment(ctx, db, in.OperatorAdminID, cardID,
		true, in.ReceivedCents, note, now)
	if verifyErr != nil {
		// 核验未落地：释放预占的渠道流水号，允许同一笔到账重试或人工处理。
		if delErr := db.EsportsPaymentReceipt.DeleteOneID(reserved.ID).Exec(ctx); delErr != nil {
			return nil, delErr
		}
		return nil, verifyErr
	}
	if _, err = db.EsportsPaymentReceipt.UpdateOneID(reserved.ID).
		SetStatus(esportspaymentreceipt.StatusVerified).SetAppliedCents(card.PrincipalCents).
		SetExceptionCents(0).SetVerifiedByAdminID(in.OperatorAdminID).Save(ctx); err != nil {
		return nil, err
	}
	return view, nil
}

// syncCustomerStoredValueIntentStatus 同步支付意图状态（仅动待支付意图，重复调用幂等）。
func syncCustomerStoredValueIntentStatus(ctx context.Context, db *ent.Client, intentNo string,
	status esportspaymentintent.Status) error {
	if strings.TrimSpace(intentNo) == "" || status == esportspaymentintent.StatusPending {
		return nil
	}
	if _, err := db.EsportsPaymentIntent.Update().Where(
		esportspaymentintent.IntentNoEQ(intentNo),
		esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
	).SetStatus(status).Save(ctx); err != nil {
		return err
	}
	return nil
}

// expireCustomerStoredValueIntents 把过期的自助购卡支付意图标记为 expired（§4.4）：
// 只动「卡仍未核验」的意图，已激活的卡不受影响；卡本身保持待核验，可由财务人工核验后激活。
func expireCustomerStoredValueIntents(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	intents, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
		esportspaymentintent.OrderIDGT(storedValueIntentOrderBase),
		esportspaymentintent.ExpiresAtLTE(now.UTC()),
	).Order(ent.Asc(esportspaymentintent.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, intent := range intents {
		cardID := customerStoredValueIntentCardID(intent.OrderID)
		if cardID < 1 {
			continue
		}
		card, err := db.EsportsStoredValueCard.Get(ctx, cardID)
		if err != nil {
			if ent.IsNotFound(err) {
				continue
			}
			return total, err
		}
		if card.Status != esportsstoredvaluecard.StatusPendingVerification {
			continue
		}
		changed, err := db.EsportsPaymentIntent.Update().Where(
			esportspaymentintent.IDEQ(intent.ID),
			esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
		).SetStatus(esportspaymentintent.StatusExpired).Save(ctx)
		if err != nil {
			return total, err
		}
		total += changed
	}
	return total, nil
}

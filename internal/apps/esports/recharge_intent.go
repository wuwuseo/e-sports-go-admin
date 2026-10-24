package esports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsidempotency"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportspaymentreceipt"
	"go-admin/internal/ent/esportsrechargeintent"
	"go-admin/internal/ent/systemconfig"
)

// 客户充值独立支付意图：客户发起充值时先匹配当前生效活动、锁定「活动资格与赠送明细」快照，
// 生成独立于订单的支付意图 EsportsPaymentIntent，财务核验或已验签渠道核销到账后再按快照入账，
// 避免核验时点活动已改导致赠送漂移。与管理员即时充值 rechargeWithCampaigns 互补：后者即时匹配并入账，
// 本链路走「发起 → 核验 → 入账」，供客户自助充值与真实收款核验使用。
const (
	// 充值意图的支付意图落在「订单中心化」的 EsportsPaymentIntent 上，充值没有订单，
	// 用保留段（≥ rechargeIntentOrderBase）编码客户 ID，保证不与真实订单 ID 及储值卡意图段相撞。
	rechargeIntentOrderBase = 2_000_000_000
	// 充值意图幂等操作名（与 create_order 同口径，按 principal=customer:<id> 隔离）。
	rechargeIntentOperation = "customer_recharge_intent"
	// 充值支付意图有效期（分钟）：缺省 24 小时，允许 1 分钟–7 天。
	rechargeIntentPayWindowKey            = "app.esports.recharge_payment_window_minutes"
	rechargeIntentPayWindowDefaultMinutes = 24 * 60
	rechargeIntentPayWindowMaxMinutes     = 7 * 24 * 60
	// 充值本金上下限：避免零元充值与超巨额充值。
	rechargeIntentMinPrincipalCents = 100
	rechargeIntentMaxPrincipalCents = 100_000_000
)

// rechargeIntentInput 是客户发起充值的入参：实充本金 + 收款渠道 + 客户端幂等键。
// 赠送一律由服务端按当前生效活动匹配，不接受客户端传入。
type rechargeIntentInput struct {
	PrincipalCents int64  `json:"principal_cents"`
	PaymentChannel string `json:"payment_channel"`
	IdempotencyKey string `json:"idempotency_key"`
}

// rechargeIntentView 是充值意图视图：支付意图 + 锁定的活动快照与付款指引。
type rechargeIntentView struct {
	IntentNo           string         `json:"intent_no"`
	IntentStatus       string         `json:"intent_status"`
	CustomerID         int            `json:"customer_id"`
	PrincipalCents     int64          `json:"principal_cents"`
	BonusCents         int64          `json:"bonus_cents"`
	RuleSnapshot       map[string]any `json:"rule_snapshot"`
	SnapshotAt         time.Time      `json:"snapshot_at"`
	PaymentChannel     string         `json:"payment_channel"`
	PaymentRef         string         `json:"payment_ref"`
	PayableCents       int64          `json:"payable_cents"`
	PayWindowExpiresAt time.Time      `json:"pay_window_expires_at"`
}

// rechargeSettlementInput 是「已验签通道适配器」核销充值到账的入参。
// 与 payment.go 的 verifiedPayment 同口径：只能由已完成签名与到账证据校验的通道适配器构造，无公开路由。
type rechargeSettlementInput struct {
	IntentNo        string
	Channel         string
	TransactionNo   string
	ReceivedCents   int64
	ConfirmedAt     time.Time
	EvidenceNote    string
	OperatorAdminID int
}

// rechargeIntentOrderID 把客户 ID 编码进支付意图的 order_id 保留段。
func rechargeIntentOrderID(customerID int) int {
	if customerID < 1 {
		return 0
	}
	return rechargeIntentOrderBase + customerID
}

// rechargeIntentCustomerID 还原保留段里的客户 ID；非充值意图返回 0。
func rechargeIntentCustomerID(orderID int) int {
	if orderID <= rechargeIntentOrderBase || orderID >= rechargeIntentOrderBase+1_000_000_000 {
		return 0
	}
	return orderID - rechargeIntentOrderBase
}

func newRechargeIntentNo() string {
	raw := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))
	return "RCHG" + raw[:12]
}

// rechargeIntentPayWindow 支付意图有效期：配置缺失或非法时回落 24 小时。
func rechargeIntentPayWindow(ctx context.Context, db *ent.Client) time.Duration {
	fallback := time.Duration(rechargeIntentPayWindowDefaultMinutes) * time.Minute
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(rechargeIntentPayWindowKey)).Only(ctx)
	if err != nil {
		return fallback
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || minutes < 1 || minutes > rechargeIntentPayWindowMaxMinutes {
		return fallback
	}
	return time.Duration(minutes) * time.Minute
}

// validateRechargeIntentInput 充值发起入参校验（任一不满足即拒绝，不写任何数据）。
func validateRechargeIntentInput(in rechargeIntentInput) error {
	key := strings.TrimSpace(in.IdempotencyKey)
	if len(key) < 8 || len(key) > 128 {
		return fmt.Errorf("%w：幂等键长度须为 8–128", errInvalidInput)
	}
	switch in.PaymentChannel {
	case "wechat_work", "merchant_qr", "corporate_transfer":
	case "customer_balance":
		return fmt.Errorf("%w：充值不走余额划转，请选择外部收款渠道并由财务核验", errInvalidInput)
	default:
		return fmt.Errorf("%w：充值收款渠道无效", errInvalidInput)
	}
	if in.PrincipalCents < rechargeIntentMinPrincipalCents ||
		in.PrincipalCents > rechargeIntentMaxPrincipalCents {
		return fmt.Errorf("%w：充值本金须在 %d–%d 分之间",
			errInvalidInput, rechargeIntentMinPrincipalCents, rechargeIntentMaxPrincipalCents)
	}
	return nil
}

func rechargeIntentRequestHash(in rechargeIntentInput) string {
	in.IdempotencyKey = ""
	data, _ := json.Marshal(in)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// existingRechargeIntent 幂等前置查询：同一客户同一幂等键已受理时直接返回原意图。
// 请求内容与首次不一致（哈希不符）返回冲突，避免「换个本金复用同一键」。
func existingRechargeIntent(ctx context.Context, db *ent.Client, customerID int,
	key, hash string) (*rechargeIntentView, error) {
	record, err := db.EsportsIdempotency.Query().Where(
		esportsidempotency.OperationEQ(rechargeIntentOperation),
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
		return nil, fmt.Errorf("%w：同一幂等键的充值请求内容不一致", errConflict)
	}
	return rechargeIntentViewByNo(ctx, db, record.ResultRef)
}

// createRechargeIntent 客户发起充值：按实充本金与当前生效活动匹配出活动资格与赠送快照，
// 生成独立支付意图 + 充值意图快照，返回付款码供客户外部付款。资金须财务核验或渠道核销后才入账。
func createRechargeIntent(ctx context.Context, db *ent.Client, customer *ent.EsportsCustomer,
	in rechargeIntentInput, now time.Time) (*rechargeIntentView, error) {
	if customer == nil || customer.ID < 1 {
		return nil, errForbidden
	}
	// 风险与资格拦截：与客户自助下单 / 购卡同一口径。
	if err := ensureCustomerSelfServiceAllowed(customer); err != nil {
		return nil, err
	}
	if customer.AgeStatus != esportscustomer.AgeStatusAdult {
		return nil, errAgeUnverified
	}
	in.PaymentChannel = strings.TrimSpace(in.PaymentChannel)
	if err := validateRechargeIntentInput(in); err != nil {
		return nil, err
	}
	// 渠道启停校验：停用的收款渠道不得发起充值（不生成支付意图、不伪成功）。
	if !paymentChannelEnabled(ctx, db, in.PaymentChannel) {
		return nil, fmt.Errorf("%w：收款方式 %s 已停用", errPaymentGatewayUnavailable, in.PaymentChannel)
	}
	key := strings.TrimSpace(in.IdempotencyKey)
	hash := rechargeIntentRequestHash(in)
	if prior, err := existingRechargeIntent(ctx, db, customer.ID, key, hash); err != nil || prior != nil {
		return prior, err
	}
	deadline := now.UTC().Add(rechargeIntentPayWindow(ctx, db))
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// 活动资格与赠送快照在发起时锁定：核验入账以本快照为准，不重新匹配活动。
	bonus, snapshot, expiresAt, err := resolveRechargeBonus(ctx, tx.Client(), walletCustomerBalance,
		customer.ID, in.PrincipalCents, now)
	if err != nil {
		return nil, err
	}
	intentNo := newRechargeIntentNo()
	rechargeCreate := tx.EsportsRechargeIntent.Create().
		SetIntentNo(intentNo).SetCustomerID(customer.ID).
		SetPrincipalCents(in.PrincipalCents).SetBonusCents(bonus).
		SetSnapshotAt(now.UTC())
	if snapshot != nil {
		rechargeCreate.SetRuleSnapshot(snapshot)
	}
	if expiresAt != nil {
		rechargeCreate.SetBonusExpiresAt(expiresAt.UTC())
	}
	recharge, err := rechargeCreate.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsPaymentIntent.Create().
		SetIntentNo(intentNo).
		SetOrderID(rechargeIntentOrderID(customer.ID)).
		SetConfirmationID(recharge.ID).
		SetQuoteVersion(1).
		SetAmountCents(in.PrincipalCents).
		SetChannel(in.PaymentChannel).
		SetStatus(esportspaymentintent.StatusPending).
		SetExpiresAt(deadline).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = recordRechargeIntent(ctx, tx, customer.ID, in, key, hash, intentNo); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	// 返回从库中重读的视图，保证快照与后续核验入账读取到的是同一份 JSON 往返后的结构。
	return rechargeIntentViewByNo(ctx, db, intentNo)
}

// recordRechargeIntent 写幂等记录与外盒留痕；重复提交只补写缺失的一份。
func recordRechargeIntent(ctx context.Context, tx *ent.Tx, customerID int, in rechargeIntentInput,
	key, hash, intentNo string) error {
	exists, err := tx.EsportsIdempotency.Query().Where(
		esportsidempotency.OperationEQ(rechargeIntentOperation),
		esportsidempotency.PrincipalEQ(customerSelfPrincipal(customerID)),
		esportsidempotency.RequestKeyEQ(key),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		if _, err = tx.EsportsIdempotency.Create().
			SetOperation(rechargeIntentOperation).
			SetPrincipal(customerSelfPrincipal(customerID)).SetRequestKey(key).
			SetRequestHash(hash).SetResultRef(intentNo).Save(ctx); err != nil {
			if !ent.IsConstraintError(err) {
				return err
			}
		}
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("recharge.intent_created:" + intentNo).
		SetEventType("recharge.intent_created").SetAggregateRef(intentNo).
		SetPayload(map[string]interface{}{
			"intent_no": intentNo, "customer_id": customerID,
			"principal_cents": in.PrincipalCents, "payment_channel": in.PaymentChannel,
		}).Save(ctx); err != nil && !ent.IsConstraintError(err) {
		return err
	}
	return nil
}

// creditRechargeIntent 核验通过后按意图快照入账：在一个事务内把实充本金 + 赠送
// （以意图快照为准，不重新匹配活动）写入客户余额充值流水，并把支付意图置 paid。
// 幂等靠支付意图状态与钱包流水的 (source_type, source_id, entry_type) 唯一索引兜底。
func creditRechargeIntent(ctx context.Context, db *ent.Client, operatorID int,
	intent *ent.EsportsPaymentIntent, recharge *ent.EsportsRechargeIntent, note string) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := tx.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(intent.IntentNo)).Only(ctx)
	if err != nil {
		return err
	}
	if current.Status == esportspaymentintent.StatusPaid {
		// 已入账：幂等直接返回。
		return tx.Commit()
	}
	if current.Status != esportspaymentintent.StatusPending {
		return errConflict
	}
	sourceID := "recharge:" + recharge.IntentNo
	if err = writeCampaignRechargeTx(ctx, tx, operatorID, walletCustomerBalance, recharge.CustomerID,
		recharge.PrincipalCents, recharge.BonusCents, recharge.RuleSnapshot,
		recharge.BonusExpiresAt, note, sourceID); err != nil {
		return err
	}
	changed, err := tx.EsportsPaymentIntent.Update().Where(
		esportspaymentintent.IDEQ(current.ID),
		esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
	).SetStatus(esportspaymentintent.StatusPaid).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("recharge.intent_credited:" + recharge.IntentNo).
		SetEventType("recharge.intent_credited").SetAggregateRef(recharge.IntentNo).
		SetPayload(map[string]interface{}{
			"intent_no": recharge.IntentNo, "customer_id": recharge.CustomerID,
			"principal_cents": recharge.PrincipalCents, "bonus_cents": recharge.BonusCents,
		}).Save(ctx); err != nil && !ent.IsConstraintError(err) {
		return err
	}
	return tx.Commit()
}

// verifyRechargeIntentPayment 财务核验充值到账：核验通过后按意图快照入账，
// 并同步支付意图状态；approve=false 驳回申请并把支付意图置无效。
func verifyRechargeIntentPayment(ctx context.Context, db *ent.Client, adminID int, intentNo string,
	approve bool, receivedCents int64, evidenceNote string, now time.Time) (*rechargeIntentView, error) {
	intentNo = strings.TrimSpace(intentNo)
	evidenceNote = strings.TrimSpace(evidenceNote)
	if adminID < 1 || intentNo == "" || len([]rune(evidenceNote)) < 8 || len([]rune(evidenceNote)) > 255 {
		return nil, errInvalidInput
	}
	intent, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(intentNo)).Only(ctx)
	if err != nil {
		return nil, err
	}
	if rechargeIntentCustomerID(intent.OrderID) < 1 {
		return nil, fmt.Errorf("%w：该支付意图不属于客户充值", errInvalidInput)
	}
	recharge, err := db.EsportsRechargeIntent.Query().Where(
		esportsrechargeintent.IntentNoEQ(intentNo)).Only(ctx)
	if err != nil {
		return nil, err
	}
	if !approve {
		if _, err = db.EsportsPaymentIntent.Update().Where(
			esportspaymentintent.IDEQ(intent.ID),
			esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
		).SetStatus(esportspaymentintent.StatusInvalidated).Save(ctx); err != nil {
			return nil, err
		}
		return rechargeIntentViewByNo(ctx, db, intentNo)
	}
	if receivedCents != recharge.PrincipalCents {
		return nil, fmt.Errorf("%w：实收金额与充值本金不一致", errInvalidInput)
	}
	if now.UTC().After(intent.ExpiresAt) {
		return nil, fmt.Errorf("%w：支付意图已过期，请驳回并让客户重新发起充值", errConflict)
	}
	note := "财务核验充值到账：" + evidenceNote
	if len([]rune(note)) > 255 {
		return nil, errInvalidInput
	}
	if err = creditRechargeIntent(ctx, db, adminID, intent, recharge, note); err != nil {
		return nil, err
	}
	return rechargeIntentViewByNo(ctx, db, intentNo)
}

// settleRechargeIntent 已验签通道适配器核销入口（同口径）：只接受已通过签名与
// 到账证据校验的通道通知，校验支付意图—充值意图—金额—渠道一致后按快照入账。
// 渠道流水号 (channel, transaction_no) 唯一：同一笔到账重复回调按幂等返回已入账视图。
//
// ：本入口同时服务「渠道自动回调」与「财务人工核销」——回调由 payment.go 分流进入且
// 没有管理员操作人（OperatorAdminID=0），此时回执的 verified_by_admin_id 保持为空。
func settleRechargeIntent(ctx context.Context, db *ent.Client,
	in rechargeSettlementInput, now time.Time) (*rechargeIntentView, error) {
	in.IntentNo = strings.TrimSpace(in.IntentNo)
	in.Channel = strings.TrimSpace(in.Channel)
	in.TransactionNo = strings.TrimSpace(in.TransactionNo)
	in.EvidenceNote = strings.TrimSpace(in.EvidenceNote)
	if in.IntentNo == "" || in.Channel == "" || in.TransactionNo == "" || len(in.TransactionNo) > 128 ||
		in.ReceivedCents <= 0 || in.ConfirmedAt.IsZero() || in.OperatorAdminID < 0 ||
		len([]rune(in.EvidenceNote)) < 8 {
		return nil, errInvalidInput
	}
	intent, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(in.IntentNo)).Only(ctx)
	if err != nil {
		return nil, err
	}
	customerID := rechargeIntentCustomerID(intent.OrderID)
	if customerID < 1 {
		return nil, fmt.Errorf("%w：该支付意图不属于客户充值", errInvalidInput)
	}
	recharge, err := db.EsportsRechargeIntent.Query().Where(
		esportsrechargeintent.IntentNoEQ(in.IntentNo)).Only(ctx)
	if err != nil {
		return nil, err
	}
	if recharge.CustomerID != customerID {
		return nil, fmt.Errorf("%w：支付意图与充值申请不匹配", errInvalidInput)
	}
	if intent.Channel != in.Channel {
		return nil, fmt.Errorf("%w：到账渠道与充值申请的收款渠道不一致", errInvalidInput)
	}
	if intent.AmountCents != recharge.PrincipalCents || in.ReceivedCents != recharge.PrincipalCents {
		return nil, fmt.Errorf("%w：实收金额与充值本金不一致", errInvalidInput)
	}
	if in.ConfirmedAt.After(intent.ExpiresAt) {
		return nil, fmt.Errorf("%w：支付意图已过期，请重新发起充值并由财务人工核验", errConflict)
	}
	note := fmt.Sprintf("通道核销 %s 流水 %s：%s", in.Channel, in.TransactionNo, in.EvidenceNote)
	if len([]rune(note)) > 255 {
		return nil, errInvalidInput
	}
	// 渠道流水唯一：同笔到账重复回调返回已入账视图；被别的单据占用则冲突。
	receipt, err := db.EsportsPaymentReceipt.Query().Where(
		esportspaymentreceipt.ChannelEQ(in.Channel),
		esportspaymentreceipt.TransactionNoEQ(in.TransactionNo),
	).Only(ctx)
	if err == nil {
		if receipt.IntentID != intent.ID || receipt.ActualCents != in.ReceivedCents {
			return nil, fmt.Errorf("%w：该渠道流水已用于其他单据", errConflict)
		}
		return rechargeIntentViewByNo(ctx, db, in.IntentNo)
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
	if err = creditRechargeIntent(ctx, db, in.OperatorAdminID, intent, recharge, note); err != nil {
		// 入账未落地：释放预占的渠道流水号，允许同一笔到账重试或人工处理。
		if delErr := db.EsportsPaymentReceipt.DeleteOneID(reserved.ID).Exec(ctx); delErr != nil {
			return nil, delErr
		}
		return nil, err
	}
	if _, err = db.EsportsPaymentReceipt.UpdateOneID(reserved.ID).
		SetStatus(esportspaymentreceipt.StatusVerified).SetAppliedCents(recharge.PrincipalCents).
		SetExceptionCents(0).SetNillableVerifiedByAdminID(rechargeSettlementOperator(in.OperatorAdminID)).Save(ctx); err != nil {
		return nil, err
	}
	return rechargeIntentViewByNo(ctx, db, in.IntentNo)
}

// rechargeSettlementOperator 把核销操作人转成可空值：0 表示渠道自动回调（无管理员操作人）。
func rechargeSettlementOperator(adminID int) *int {
	if adminID < 1 {
		return nil
	}
	return &adminID
}

// expireRechargeIntents 把过期的充值支付意图标记为 expired：只动「仍未核验且已超时」的意图，
// 已入账 / 已驳回的意图不受影响。重复运行幂等。
func expireRechargeIntents(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	intents, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
		esportspaymentintent.OrderIDGT(rechargeIntentOrderBase),
		esportspaymentintent.ExpiresAtLTE(now.UTC()),
	).Order(ent.Asc(esportspaymentintent.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, intent := range intents {
		if rechargeIntentCustomerID(intent.OrderID) < 1 {
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

func rechargeIntentViewOf(intent *ent.EsportsPaymentIntent, recharge *ent.EsportsRechargeIntent) *rechargeIntentView {
	return &rechargeIntentView{
		IntentNo:           recharge.IntentNo,
		IntentStatus:       intent.Status.String(),
		CustomerID:         recharge.CustomerID,
		PrincipalCents:     recharge.PrincipalCents,
		BonusCents:         recharge.BonusCents,
		RuleSnapshot:       recharge.RuleSnapshot,
		SnapshotAt:         recharge.SnapshotAt,
		PaymentChannel:     intent.Channel,
		PaymentRef:         recharge.IntentNo,
		PayableCents:       intent.AmountCents,
		PayWindowExpiresAt: intent.ExpiresAt,
	}
}

// rechargeIntentViewByNo 按意图号组装充值意图视图（支付意图 + 锁定的活动快照）。
func rechargeIntentViewByNo(ctx context.Context, db *ent.Client, intentNo string) (*rechargeIntentView, error) {
	intent, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(intentNo)).Only(ctx)
	if err != nil {
		return nil, err
	}
	recharge, err := db.EsportsRechargeIntent.Query().Where(
		esportsrechargeintent.IntentNoEQ(intentNo)).Only(ctx)
	if err != nil {
		return nil, err
	}
	return rechargeIntentViewOf(intent, recharge), nil
}

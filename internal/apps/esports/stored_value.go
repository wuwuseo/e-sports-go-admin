package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsquotahold"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/esportsstoredvaluepackage"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/role"
)

// 储值卡（预付账户，§4.4 / §11.3）。
//
// 记账口径：卡内余额不落库，由 EsportsWalletTx 中 account_type=stored_value_card、owner_id=卡 ID 的
// 流水求和派生，本金与赠送分列——每张卡就是一个独立账户，归属人由卡上的 customer_id 得出。
// 预付款批次另记 EsportsLedgerEntry（account_type=stored_value_prepay，account_id=卡 ID），
// 供 §11.6「来源批次」追溯；(source_type, source_id, entry_role) 唯一索引保证重复提交不重复入账。
const (
	walletStoredValueCard = "stored_value_card"
	// 开卡的流水来源类型，source_id 取调用方幂等键，重复提交由唯一索引兜底。
	storedValueSourceOpen = "stored_value_card_open"
	// 预付款批次账本账户类型。
	storedValuePrepayAccount = "stored_value_prepay"
	// 到期提醒默认提前天数（§4.4 到期提醒）。
	storedValueDueDays = 30
	// 单张开卡最多绑定的适用商品数。
	storedValueMaxScope = 200
)

// errStoredValueUnusable 储值卡当前不可用于该笔核销。
var errStoredValueUnusable = errors.New("储值卡不可用：已过期或状态不允许消费")

// storedValueCardView 是储值卡视图：卡登记信息 + 由流水派生的余额与已消费额。
type storedValueCardView struct {
	*ent.EsportsStoredValueCard
	PrincipalRemainingCents int64 `json:"principal_remaining_cents"`
	BonusRemainingCents     int64 `json:"bonus_remaining_cents"`
	BalanceCents            int64 `json:"balance_cents"`
	ConsumedCents           int64 `json:"consumed_cents"`
}

// storedValueOpenInput 是开卡入参（§4.4）：客服代办或客户自助购买。
type storedValueOpenInput struct {
	CustomerID     int    `json:"customer_id"`
	Source         string `json:"source"`
	PaymentChannel string `json:"payment_channel"`
	PaymentRef     string `json:"payment_ref"`
	// 可选：选定套餐后本金 / 赠送由套餐决定，忽略入参的 principal_cents / bonus_cents（§4.4）。
	PackageID         int    `json:"package_id"`
	PrincipalCents    int64  `json:"principal_cents"`
	BonusCents        int64  `json:"bonus_cents"`
	ApplicableScope   []int  `json:"applicable_scope"`
	AllowGifts        bool   `json:"allow_gifts"`
	ContractConfirmed bool   `json:"contract_confirmed"`
	ValidUntil        string `json:"valid_until"`
	ValidityDays      int    `json:"validity_days"`
	// 卡级扣减顺序（§11.3-02）：bonus_first / principal_first / proportional；留空回落全局键。
	DeductionOrder string `json:"deduction_order"`
	Note           string `json:"note"`
	IdempotencyKey string `json:"idempotency_key"`
}

// storedValueDeductionOrderValues 卡级扣减顺序的合法取值（空 = 回落全局键）。
func storedValueDeductionOrderValue(value string) (string, bool) {
	switch strings.TrimSpace(value) {
	case "", "bonus_first", "principal_first", "proportional":
		return strings.TrimSpace(value), true
	default:
		return "", false
	}
}

// storedValueDeductionOrder 解析某张卡实际生效的扣减顺序（§11.3-02）：卡级字段优先，
// 留空（含历史卡）回落全局键 app.esports.prepay_deduction_order。
func storedValueDeductionOrder(ctx context.Context, db *ent.Client, card *ent.EsportsStoredValueCard) (string, error) {
	if card == nil {
		return "", errInvalidInput
	}
	if value, ok := storedValueDeductionOrderValue(card.DeductionOrder); ok && value != "" {
		return value, nil
	}
	return prepayDeductionOrder(ctx, db), nil
}

func newStoredValueCardNo() string {
	raw := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))
	return "SV" + raw[:12]
}

// validateStoredValueOpen 校验开卡入参，返回规范化后的有效期与适用商品（SKU ID 去重）。
// 任一不满足返回 errInvalidInput，调用方不得写库。
func validateStoredValueOpen(in storedValueOpenInput, now time.Time) (time.Time, []int, error) {
	key := strings.TrimSpace(in.IdempotencyKey)
	ref := strings.TrimSpace(in.PaymentRef)
	if in.CustomerID < 1 || in.PrincipalCents <= 0 || in.BonusCents < 0 {
		return time.Time{}, nil, errInvalidInput
	}
	if in.Source != "staff" && in.Source != "self" {
		return time.Time{}, nil, errInvalidInput
	}
	if len(key) < 8 || len(key) > 128 || len([]rune(in.Note)) > 255 {
		return time.Time{}, nil, errInvalidInput
	}
	// 卡级扣减顺序（§11.3-02）：非法取值直接拒绝，留空表示回落全局键。
	if _, ok := storedValueDeductionOrderValue(in.DeductionOrder); !ok {
		return time.Time{}, nil, fmt.Errorf("%w：储值卡扣减顺序取值非法", errInvalidInput)
	}
	// 开卡必须确认合同（§4.4 记录合同确认）。
	if !in.ContractConfirmed {
		return time.Time{}, nil, fmt.Errorf("%w：开卡前须确认储值卡合同", errInvalidInput)
	}
	switch in.PaymentChannel {
	case "wechat_work", "merchant_qr", "corporate_transfer":
		// 外部收款须留凭证号，便于财务核验。
		if ref == "" || len(ref) > 128 {
			return time.Time{}, nil, fmt.Errorf("%w：外部收款开卡须填写支付凭证号", errInvalidInput)
		}
	case "customer_balance":
		// 账户间划转：钱来自客户余额本金，不存在外部凭证，也不产生赠送权益。
		if ref != "" {
			return time.Time{}, nil, errInvalidInput
		}
		if in.BonusCents != 0 {
			return time.Time{}, nil, fmt.Errorf("%w：用余额购卡不支持赠送权益", errInvalidInput)
		}
	default:
		return time.Time{}, nil, errInvalidInput
	}
	validUntil := now.UTC().AddDate(0, 0, in.ValidityDays)
	if raw := strings.TrimSpace(in.ValidUntil); raw != "" {
		parsed, err := parseReportTime(raw)
		if err != nil {
			return time.Time{}, nil, errInvalidInput
		}
		validUntil = parsed.UTC()
	}
	if !now.UTC().Before(validUntil) {
		return time.Time{}, nil, fmt.Errorf("%w：储值卡有效期必须晚于当前时间", errInvalidInput)
	}
	scope := make([]int, 0, len(in.ApplicableScope))
	seen := map[int]bool{}
	for _, skuID := range in.ApplicableScope {
		if skuID < 1 || seen[skuID] {
			return time.Time{}, nil, errInvalidInput
		}
		seen[skuID] = true
		scope = append(scope, skuID)
	}
	if len(scope) > storedValueMaxScope {
		return time.Time{}, nil, errInvalidInput
	}
	return validUntil, scope, nil
}

// storedValueCardBalance 汇总卡内余额（本金 / 赠送分列）。
func storedValueCardBalance(ctx context.Context, db *ent.Client, cardID int) (walletBalance, error) {
	return walletBalanceOf(ctx, db, walletStoredValueCard, cardID)
}

func storedValueCardBalanceTx(ctx context.Context, tx *ent.Tx, cardID int) (walletBalance, error) {
	return walletBalanceTx(ctx, tx, walletStoredValueCard, cardID)
}

// storedValueCardViewOf 组装卡视图。已消费额由「开卡总额 − 当前余额」派生，不额外落库。
func storedValueCardViewOf(ctx context.Context, db *ent.Client, card *ent.EsportsStoredValueCard) (*storedValueCardView, error) {
	balance, err := storedValueCardBalance(ctx, db, card.ID)
	if err != nil {
		return nil, err
	}
	consumed := card.PrincipalCents + card.BonusCents - balance.total()
	if card.Status == esportsstoredvaluecard.StatusPendingVerification || card.Status == esportsstoredvaluecard.StatusRejected {
		consumed = 0
	}
	return &storedValueCardView{
		EsportsStoredValueCard:  card,
		PrincipalRemainingCents: balance.PrincipalCents,
		BonusRemainingCents:     balance.BonusCents,
		BalanceCents:            balance.total(),
		ConsumedCents:           consumed,
	}, nil
}

// openStoredValueCard 开卡（§4.4）：外部收款先待核验，不得提前发放余额；用客户余额购卡属账户间划转，
// 同一事务内减少余额本金、增加储值卡本金并产生配对流水。重复幂等键由唯一索引兜底返回冲突。
func openStoredValueCard(ctx context.Context, db *ent.Client, adminID int, in storedValueOpenInput, now time.Time) (*storedValueCardView, error) {
	key := strings.TrimSpace(in.IdempotencyKey)
	ref := strings.TrimSpace(in.PaymentRef)
	note := strings.TrimSpace(in.Note)
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	customer, err := tx.EsportsCustomer.Get(ctx, in.CustomerID)
	if err != nil {
		return nil, err
	}
	// 选套餐（§4.4）：本金 / 赠送由套餐决定，覆盖入参给定值；记录套餐 ID 供追溯。
	var packageID *int
	if in.PackageID > 0 {
		pkg, pkgErr := tx.EsportsStoredValuePackage.Get(ctx, in.PackageID)
		if pkgErr != nil {
			return nil, pkgErr
		}
		if pkg.Status != esportsstoredvaluepackage.StatusOnSale {
			return nil, fmt.Errorf("%w：该储值卡套餐已下架", errInvalidInput)
		}
		in.PrincipalCents = pkg.PrincipalCents
		in.BonusCents = pkg.BonusCents
		packageID = &pkg.ID
	}
	// 开卡赠送的权益来源有「套餐赠送」与「会员等级赠送比例」两处：同一权益多来源取最高档、不叠加（§10.5-04）。
	// 未选套餐且未手填赠送时按等级比例；已选套餐时与套餐赠送取大（套餐低于等级比例时按等级发放）；
	// 余额购卡属账户间划转，不产生赠送权益（§4.4）。
	if in.PaymentChannel != "customer_balance" {
		bonus, bonusErr := memberPrepayBonusCents(ctx, tx.Client(), customer.MemberLevelCode, in.PrincipalCents)
		if bonusErr != nil {
			return nil, bonusErr
		}
		if packageID != nil {
			if bonus > in.BonusCents {
				in.BonusCents = bonus
			}
		} else if in.BonusCents == 0 {
			in.BonusCents = bonus
		}
	}
	validUntil, scope, err := validateStoredValueOpen(in, now)
	if err != nil {
		return nil, err
	}
	// 渠道启停校验（§4.3-08 / §4.3-03）：停用的收款渠道不得发起开卡收款（客服代开卡与自助购卡同一口径）。
	if !paymentChannelEnabled(ctx, db, in.PaymentChannel) {
		return nil, fmt.Errorf("%w：收款方式 %s 已停用", errPaymentGatewayUnavailable, in.PaymentChannel)
	}
	utc := now.UTC()
	if in.PaymentChannel == "customer_balance" {
		// 划转与余额核销共用同一把余额锁，避免并发把余额扣成负数。
		unlockBalance := lockMoneyAccount("balance:" + strconv.Itoa(in.CustomerID))
		defer unlockBalance()
		balance, err := walletBalanceTx(ctx, tx, walletCustomerBalance, in.CustomerID)
		if err != nil {
			return nil, err
		}
		// 划转只动本金批次（§4.4）；赠送权益不可由划转产生，已在入参校验拦住。
		if balance.PrincipalCents < in.PrincipalCents {
			return nil, errInsufficientBalance
		}
		if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
			SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(in.CustomerID).
			SetEntryType(esportswallettx.EntryTypeConsume).
			SetPrincipalDeltaCents(-in.PrincipalCents).SetBonusDeltaCents(0).
			SetSourceType(storedValueSourceOpen).SetSourceID(key).
			SetOperatorID(adminID).SetNote("用余额购卡").Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
		if _, err = tx.EsportsLedgerEntry.Create().
			SetAccountType(walletCustomerBalance).SetAccountID(strconv.Itoa(in.CustomerID)).
			SetSourceType(storedValueSourceOpen).SetSourceID(key).
			SetEntryRole("stored_value_transfer_out").SetDeltaCents(-in.PrincipalCents).
			SetFundBatch("stored_value_card").Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
	}
	// 卡号冲突（理论概率极低）交由唯一索引拒绝，调用方重试即可。
	status := esportsstoredvaluecard.StatusActive
	if in.PaymentChannel != "customer_balance" {
		status = esportsstoredvaluecard.StatusPendingVerification
	}
	creator := tx.EsportsStoredValueCard.Create().SetCardNo(newStoredValueCardNo()).
		SetCustomerID(in.CustomerID).
		SetOpenRequestKey(key).
		SetSource(esportsstoredvaluecard.Source(in.Source)).
		SetPaymentChannel(esportsstoredvaluecard.PaymentChannel(in.PaymentChannel)).
		SetPaymentRef(ref).SetPrincipalCents(in.PrincipalCents).SetBonusCents(in.BonusCents).
		SetApplicableScope(scope).SetAllowGifts(in.AllowGifts).SetContractConfirmed(true).
		SetValidFrom(utc).SetValidUntil(validUntil).
		SetDeductionOrder(in.DeductionOrder).
		SetStatus(status).
		SetOpenedByAdminID(adminID)
	if strings.TrimSpace(in.ValidUntil) == "" && in.ValidityDays > 0 {
		creator.SetRequestedValidityDays(in.ValidityDays)
	}
	if packageID != nil {
		creator = creator.SetPackageID(*packageID)
	}
	card, err := creator.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if status == esportsstoredvaluecard.StatusPendingVerification {
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return storedValueCardViewOf(ctx, db, card)
	}
	create := tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypeStoredValueCard).SetOwnerID(card.ID).
		SetEntryType(esportswallettx.EntryTypeRecharge).
		SetPrincipalDeltaCents(in.PrincipalCents).SetBonusDeltaCents(in.BonusCents).
		SetSourceType(storedValueSourceOpen).SetSourceID(key).
		SetBeneficiaryType(esportswallettx.BeneficiaryTypeCustomer).SetBeneficiaryID(card.CustomerID).
		SetBalanceBeforeCents(0).SetBalanceAfterCents(in.PrincipalCents + in.BonusCents).
		SetOperatorID(adminID).SetNote(note).
		SetRuleSnapshot(map[string]any{
			"card_no": card.CardNo, "payment_channel": string(card.PaymentChannel),
			"payment_ref": ref, "applicable_scope": scope, "valid_until": validUntil,
		})
	// 赠送权益随卡有效期到期（§4.5 赠送过期）。
	if in.BonusCents > 0 {
		create.SetBonusExpiresAt(validUntil)
	}
	if _, err = create.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	fundBatch := ref
	if fundBatch == "" {
		fundBatch = "balance_transfer"
	}
	if _, err = tx.EsportsLedgerEntry.Create().
		SetAccountType(storedValuePrepayAccount).SetAccountID(strconv.Itoa(card.ID)).
		SetSourceType(storedValueSourceOpen).SetSourceID(key).
		SetEntryRole("card_open").SetDeltaCents(in.PrincipalCents + in.BonusCents).
		SetPrincipalDeltaCents(in.PrincipalCents).SetBonusDeltaCents(in.BonusCents).
		SetFundBatch(fundBatch).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return storedValueCardViewOf(ctx, db, card)
}

// verifyStoredValueCardPayment 由另一名具有核验权限的管理员对照实际到账流水确认。
// 申请凭证只用于定位，不能单凭申请时填写的号码发放卡内余额。
func verifyStoredValueCardPayment(ctx context.Context, db *ent.Client, adminID, cardID int, approve bool, receiptRef string, receivedCents int64, evidenceNote string, now time.Time) (*storedValueCardView, error) {
	receiptRef = strings.TrimSpace(receiptRef)
	evidenceNote = strings.TrimSpace(evidenceNote)
	if adminID < 1 || cardID < 1 || len([]rune(evidenceNote)) < 8 || len([]rune(evidenceNote)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	card, err := tx.EsportsStoredValueCard.Get(ctx, cardID)
	if err != nil {
		return nil, err
	}
	if card.Status != esportsstoredvaluecard.StatusPendingVerification {
		return nil, errConflict
	}
	if card.OpenedByAdminID != nil && *card.OpenedByAdminID == adminID {
		return nil, fmt.Errorf("%w：开卡申请人不得核验本人提交的收款", errForbidden)
	}
	if approve {
		if receiptRef == "" || len(receiptRef) > 128 || receiptRef != card.PaymentRef || receivedCents != card.PrincipalCents {
			return nil, fmt.Errorf("%w：到账流水号或实收金额与开卡申请不一致", errInvalidInput)
		}
		validUntil := card.ValidUntil
		if card.RequestedValidityDays > 0 {
			validUntil = now.UTC().AddDate(0, 0, card.RequestedValidityDays)
		}
		if !now.UTC().Before(validUntil) {
			return nil, fmt.Errorf("%w：储值卡有效期已过，请驳回并重新申请", errInvalidInput)
		}
		key := string(card.PaymentChannel) + ":" + receiptRef
		changed, err := tx.EsportsStoredValueCard.Update().Where(
			esportsstoredvaluecard.IDEQ(card.ID),
			esportsstoredvaluecard.VersionEQ(card.Version),
			esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.StatusPendingVerification),
		).SetStatus(esportsstoredvaluecard.StatusActive).
			SetVerifiedPaymentKey(key).SetPaymentVerifiedByAdminID(adminID).
			SetPaymentVerifiedAt(now.UTC()).SetPaymentVerifiedAmountCents(receivedCents).
			SetPaymentVerificationNote(evidenceNote).SetValidFrom(now.UTC()).SetValidUntil(validUntil).AddVersion(1).Save(ctx)
		if err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
		sourceID := card.CardNo
		if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
			SetAccountType(esportswallettx.AccountTypeStoredValueCard).SetOwnerID(card.ID).
			SetEntryType(esportswallettx.EntryTypeRecharge).
			SetPrincipalDeltaCents(card.PrincipalCents).SetBonusDeltaCents(card.BonusCents).
			SetSourceType(storedValueSourceOpen).SetSourceID(sourceID).
			SetBeneficiaryType(esportswallettx.BeneficiaryTypeCustomer).SetBeneficiaryID(card.CustomerID).
			SetBalanceBeforeCents(0).SetBalanceAfterCents(card.PrincipalCents + card.BonusCents).
			SetFundBatch(sourceID).
			SetOperatorID(adminID).SetNote("财务核验外部收款后开卡：" + evidenceNote).
			SetRuleSnapshot(map[string]any{
				"card_no": card.CardNo, "payment_channel": string(card.PaymentChannel),
				"payment_ref": receiptRef, "verified_received_cents": receivedCents,
				"verified_by_admin_id": adminID, "verified_at": now.UTC(),
				"applicable_scope": card.ApplicableScope, "valid_until": validUntil,
			}).
			SetNillableBonusExpiresAt(func() *time.Time {
				if card.BonusCents > 0 {
					return &validUntil
				}
				return nil
			}()).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
		if _, err = tx.EsportsLedgerEntry.Create().
			SetAccountType(storedValuePrepayAccount).SetAccountID(strconv.Itoa(card.ID)).
			SetSourceType(storedValueSourceOpen).SetSourceID(sourceID).
			SetEntryRole("card_open").SetDeltaCents(card.PrincipalCents + card.BonusCents).
			SetPrincipalDeltaCents(card.PrincipalCents).SetBonusDeltaCents(card.BonusCents).
			SetFundBatch(key).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
	} else {
		changed, err := tx.EsportsStoredValueCard.Update().Where(
			esportsstoredvaluecard.IDEQ(card.ID),
			esportsstoredvaluecard.VersionEQ(card.Version),
			esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.StatusPendingVerification),
		).SetStatus(esportsstoredvaluecard.StatusRejected).
			SetPaymentVerifiedByAdminID(adminID).SetPaymentVerifiedAt(now.UTC()).
			SetPaymentVerificationNote(evidenceNote).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	updated, err := db.EsportsStoredValueCard.Get(ctx, card.ID)
	if err != nil {
		return nil, err
	}
	return storedValueCardViewOf(ctx, db, updated)
}

// storedValueCardUsable 校验卡可用于该订单：归属客户一致、状态可用、在有效期内、适用商品命中。
func storedValueCardUsable(card *ent.EsportsStoredValueCard, customerID, skuID int, now time.Time) error {
	if card.CustomerID != customerID {
		return fmt.Errorf("%w：储值卡不属于该客户", errInvalidInput)
	}
	if card.Status == esportsstoredvaluecard.StatusRefundPending ||
		card.Status == esportsstoredvaluecard.StatusRefunded ||
		card.Status == esportsstoredvaluecard.StatusFrozen {
		return errStoredValueUnusable
	}
	utc := now.UTC()
	if utc.Before(card.ValidFrom) || !utc.Before(card.ValidUntil) ||
		card.Status == esportsstoredvaluecard.StatusExpired {
		return errStoredValueUnusable
	}
	if scope := card.ApplicableScope; len(scope) > 0 {
		matched := false
		for _, id := range scope {
			if id == skuID {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%w：该订单商品不在储值卡适用范围内", errInvalidInput)
		}
	}
	return nil
}

// 礼物只可使用合同明确允许的卡内本金；SKU 适用范围不代表礼物授权。
func storedValueCardUsableForGift(card *ent.EsportsStoredValueCard, customerID int, now time.Time) error {
	if card.CustomerID != customerID {
		return errForbidden
	}
	if !card.AllowGifts || card.Status != esportsstoredvaluecard.StatusActive ||
		now.UTC().Before(card.ValidFrom) || !now.UTC().Before(card.ValidUntil) {
		return errStoredValueUnusable
	}
	return nil
}

// payOrderWithStoredValueCard 用储值卡足额核销订单（§4.4 消费）：同事务扣减卡内流水、置订单
// 储值卡标记与关联卡 ID 并转入待派单。扣减顺序固定为先赠送权益、后本金。
// ponytail: 扣减顺序可配置（先本金/按比例）未落地；存单与储值卡混合支付未实现。
func payOrderWithStoredValueCard(ctx context.Context, db *ent.Client, adminID, orderID, cardID int, now time.Time) (*ent.EsportsOrder, error) {
	return payOrderWithStoredValueCardCore(ctx, db, adminID, 0, orderID, cardID, now)
}

// payOrderWithStoredValueCardAsCustomer 客户自助储值卡核销（§4.4 / §13.2）：与管理端共用同一核销边界
// （卡归属与可用性、金额、订单状态、支付时限、配额与账本口径全部复用），差异只有三点：
//   - 订单必须属于当前客户（actorCustomerID）；
//   - 卡流水不写 operator_id（客户不是后台操作人），备注标记「客户自助」；
//   - 订单事件 actor 记为 customer，且站内方式受后台渠道启停约束（§4.3-08）。
func payOrderWithStoredValueCardAsCustomer(ctx context.Context, db *ent.Client, customerID, orderID, cardID int,
	now time.Time) (*ent.EsportsOrder, error) {
	if customerID < 1 || orderID < 1 || cardID < 1 {
		return nil, errInvalidInput
	}
	if !paymentChannelEnabled(ctx, db, walletStoredValueCard) {
		return nil, fmt.Errorf("%w：储值卡支付已被后台停用，暂不可用于订单支付", errForbidden)
	}
	return payOrderWithStoredValueCardCore(ctx, db, 0, customerID, orderID, cardID, now)
}

// payOrderWithStoredValueCardCore 是储值卡核销的唯一实现：operatorID > 0 表示后台操作人
// （写 operator_id、事件 actor=admin）；actorCustomerID > 0 表示客户自助（不写 operator_id、事件 actor=customer）。
func payOrderWithStoredValueCardCore(ctx context.Context, db *ent.Client, operatorID, actorCustomerID, orderID,
	cardID int, now time.Time) (*ent.EsportsOrder, error) {
	if orderID < 1 || cardID < 1 {
		return nil, errInvalidInput
	}
	unlock := lockMoneyAccount(fmt.Sprintf("card:%d", cardID))
	defer unlock()
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
	// 客户自助路径必须核对订单归属：不能用一个客户的身份核销另一个客户的订单。
	if actorCustomerID > 0 && order.CustomerID != actorCustomerID {
		return nil, errForbidden
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid || !now.UTC().Before(order.PaymentDeadline) {
		return nil, errConflict
	}
	// P0-11 重复收款防护：该订单已有待核验的到账凭证时，不得再用储值卡核销——否则同一订单被收两次钱，
	// 后到的转账凭证只能挂成悬账。判定与后面的「订单转已付款」条件更新同事务，避免并发穿插。
	if duplicated, err := otherPendingPaymentProofExists(ctx, tx.EsportsOfflinePaymentProof.Query(),
		order.ID, 0); err != nil {
		return nil, err
	} else if duplicated {
		return nil, fmt.Errorf("%w：该订单已有待核验的转账凭证，请先核验或驳回后再改用其它收款方式", errConflict)
	}
	customer, err := tx.EsportsCustomer.Get(ctx, order.CustomerID)
	if err != nil {
		return nil, err
	}
	if customer.AgeStatus != esportscustomer.AgeStatusAdult {
		return nil, errAgeUnverified
	}
	sku, err := tx.EsportsSku.Get(ctx, order.SkuID)
	if err != nil {
		return nil, err
	}
	if sku.Status != esportssku.StatusOnSale {
		return nil, errConflict
	}
	card, err := tx.EsportsStoredValueCard.Get(ctx, cardID)
	if err != nil {
		return nil, err
	}
	if err = storedValueCardUsable(card, order.CustomerID, order.SkuID, now); err != nil {
		return nil, err
	}
	balance, err := storedValueCardBalanceTx(ctx, tx, card.ID)
	if err != nil {
		return nil, err
	}
	payable := order.PayableCents
	if balance.total() < payable {
		return nil, errInsufficientBalance
	}
	// 卡级扣减顺序优先，留空回落全局键（§11.3-02）。
	order_, err := storedValueDeductionOrder(ctx, tx.Client(), card)
	if err != nil {
		return nil, err
	}
	principalDeduct, bonusDeduct := splitPrepayDeduction(
		order_, balance.PrincipalCents, balance.BonusCents, payable)
	walletTxCreate := tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypeStoredValueCard).SetOwnerID(card.ID).
		SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-principalDeduct).SetBonusDeltaCents(-bonusDeduct).
		SetSourceType("order").SetSourceID(order.OrderNo).
		SetBeneficiaryType(esportswallettx.BeneficiaryTypeCustomer).SetBeneficiaryID(card.CustomerID).
		SetBalanceBeforeCents(balance.total()).SetBalanceAfterCents(balance.total() - payable)
	// operator_id 是后台操作人字段：客户自助不写，改用备注与订单事件 actor 区分来源。
	if operatorID > 0 {
		walletTxCreate.SetOperatorID(operatorID)
	}
	note := "订单储值卡核销"
	if actorCustomerID > 0 {
		note = "订单储值卡核销（客户自助）"
	}
	if _, err = walletTxCreate.SetNote(note).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	update := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
	).SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetPaymentChannel(esportsorder.PaymentChannelStoredValueCard).
		SetStoredValueCardID(card.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SetPaidAt(now.UTC()).AddVersion(1)
	if err := lockCsCommissionOnPayment(ctx, tx, order, update, now); err != nil {
		return nil, err
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	// 进入待派单：若该游戏分区有优先派单权生效名单，则只通知名单内陪玩（§5.4 业务口径）。
	if err = onOrderEnteredPendingDispatch(ctx, tx, order.ID, now); err != nil {
		return nil, err
	}
	hold, err := tx.EsportsQuotaHold.Query().Where(esportsquotahold.OrderIDEQ(order.ID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	if hold.Status != esportsquotahold.StatusReserved {
		return nil, errConflict
	}
	changed, err = tx.EsportsQuotaHold.Update().Where(
		esportsquotahold.IDEQ(hold.ID), esportsquotahold.StatusEQ(esportsquotahold.StatusReserved),
	).SetStatus(esportsquotahold.StatusSold).Save(ctx)
	if err != nil || changed != 1 {
		return nil, errConflict
	}
	changed, err = tx.EsportsSku.Update().Where(
		esportssku.IDEQ(hold.SkuID), esportssku.QuotaReservedGTE(hold.Quantity),
	).AddQuotaReserved(-hold.Quantity).AddQuotaSold(hold.Quantity).AddVersion(1).Save(ctx)
	if err != nil || changed != 1 {
		return nil, errConflict
	}
	// 预付款批次账本：核销冲减对应卡片批次（本金 / 赠送分列，§4.5-04）。
	if _, err = tx.EsportsLedgerEntry.Create().
		SetAccountType(storedValuePrepayAccount).SetAccountID(strconv.Itoa(card.ID)).
		SetSourceType("order").SetSourceID(order.OrderNo).
		SetEntryRole("card_consume").SetDeltaCents(-payable).
		SetPrincipalDeltaCents(-principalDeduct).SetBonusDeltaCents(-bonusDeduct).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	// 批次级逐笔扣减（§4.3 / §11.3 / §4.5-04）：记录本次卡消费动用了哪些开卡批次，按实际构成分列。
	if err = allocateLedgerConsumptionWithComposition(ctx, tx, storedValuePrepayAccount, strconv.Itoa(card.ID),
		"order", order.OrderNo, principalDeduct, bonusDeduct); err != nil {
		return nil, err
	}
	eventActorType, eventActorID := "admin", strconv.Itoa(operatorID)
	if actorCustomerID > 0 {
		eventActorType, eventActorID = "customer", strconv.Itoa(actorCustomerID)
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.payment_verified").
		SetActorType(eventActorType).SetActorID(eventActorID).
		SetDetails(map[string]interface{}{
			"channel": walletStoredValueCard, "card_id": card.ID, "card_no": card.CardNo,
			"applied_cents": payable, "principal_deducted": principalDeduct, "bonus_deducted": bonusDeduct,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("order.payment_verified:" + order.OrderNo).
		SetEventType("order.payment_verified").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{"order_no": order.OrderNo, "channel": walletStoredValueCard}).Save(ctx); err != nil {
		return nil, err
	}
	// 余额用尽即转 exhausted：避免列表里把空卡显示为可用卡。
	if remaining := balance.total() - payable; remaining <= 0 {
		if _, err = tx.EsportsStoredValueCard.Update().Where(
			esportsstoredvaluecard.IDEQ(card.ID),
			esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.StatusActive),
		).SetStatus(esportsstoredvaluecard.StatusExhausted).AddVersion(1).Save(ctx); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// storedValueOrderConsistent 校验储值卡订单的「标记 / 关联卡 / 核销流水」三者一致（§4.6）。
// 不一致时结单与结算都应拒绝，避免拿一张没有核销凭证的卡单去结单。
// 判定与异常队列共用 storedValueOrderLinkMissing（§4.6-06），错误文案保持不变。
func storedValueOrderConsistent(ctx context.Context, db *ent.Client, order *ent.EsportsOrder) error {
	kind, reason, _, err := storedValueOrderLinkMissing(ctx, db, order)
	if err != nil {
		return err
	}
	if kind == "" {
		return nil
	}
	return fmt.Errorf("%w：%s", errInvalidInput, reason)
}

// listStoredValueCards 列出储值卡：customer_id / status 可选过滤，remind_days 命中「临近有效期」。
func listStoredValueCards(ctx context.Context, db *ent.Client, adminID int, isSuper bool, customerID int, status string, remindDays, limit int, now time.Time) ([]*storedValueCardView, error) {
	if adminID < 1 {
		return nil, errForbidden
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsStoredValueCard.Query()
	if !isSuper {
		ids, err := db.EsportsCustomer.Query().Where(esportscustomer.OwnerAdminIDEQ(adminID)).IDs(ctx)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []*storedValueCardView{}, nil
		}
		q.Where(esportsstoredvaluecard.CustomerIDIn(ids...))
	}
	if customerID > 0 {
		q.Where(esportsstoredvaluecard.CustomerIDEQ(customerID))
	}
	if status = strings.TrimSpace(status); status != "" {
		switch status {
		case "pending_verification", "rejected", "active", "exhausted", "refund_pending", "refunded", "expired", "frozen":
			q.Where(esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.Status(status)))
		default:
			return nil, errInvalidInput
		}
	}
	if remindDays > 0 {
		// 到期提醒：仅看仍可用且有效期落在提醒窗口内的卡。
		q.Where(
			esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.StatusActive),
			esportsstoredvaluecard.ValidUntilLTE(now.UTC().AddDate(0, 0, remindDays)),
		)
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

// listStoredValueCardTransactions 列出某张卡的流水（开卡 / 核销 / 退款 / 作废），供档案与对账查看。
func listStoredValueCardTransactions(ctx context.Context, db *ent.Client, cardID, limit int) ([]*ent.EsportsWalletTx, error) {
	if cardID < 1 {
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypeStoredValueCard),
		esportswallettx.OwnerIDEQ(cardID),
	).Order(ent.Desc(esportswallettx.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	// 卡流水的归属人是持卡客户（owner_id 是卡 ID），历史行按读模型回填（§11.6-02）。
	if err = backfillWalletTxSelfProving(ctx, db, walletStoredValueCard, cardID, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// countStoredValueCardsDue 统计临近有效期、仍可用的储值卡，用于客服待办提醒（§4.4 到期提醒）。
func countStoredValueCardsDue(ctx context.Context, db *ent.Client, adminID int, isSuper bool, now time.Time, withinDays int) (int, error) {
	if withinDays < 1 {
		withinDays = storedValueDueDays
	}
	q := db.EsportsStoredValueCard.Query().Where(
		esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.StatusActive),
		esportsstoredvaluecard.ValidUntilLTE(now.UTC().AddDate(0, 0, withinDays)),
	)
	if !isSuper {
		// ponytail: 用「归属客服的客户 ID 子集」过滤，客服客户到数千级时改为冗余归属列或反向边。
		ids, err := db.EsportsCustomer.Query().Where(esportscustomer.OwnerAdminIDEQ(adminID)).
			Limit(2000).Select(esportscustomer.FieldID).Ints(ctx)
		if err != nil {
			return 0, err
		}
		if len(ids) == 0 {
			return 0, nil
		}
		q.Where(esportsstoredvaluecard.CustomerIDIn(ids...))
	}
	return q.Count(ctx)
}

// expireStoredValueCards 把已过有效期的可用卡标记为 expired，并作废未消费赠送权益（§4.4 到期）。
// 未消费本金仍按合同走退卡流程、不作废，避免到期直接没收预付款引发争议；赠送权益到期即作废
// （只增不改写 entry_type=expire 的负数流水）。
func expireStoredValueCards(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	ids, err := db.EsportsStoredValueCard.Query().Where(
		esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.StatusActive),
		esportsstoredvaluecard.ValidUntilLTE(now.UTC()),
	).Limit(limit).Select(esportsstoredvaluecard.FieldID).Ints(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, id := range ids {
		changed, err := expireStoredValueCard(ctx, db, id, now)
		if err != nil {
			return total, err
		}
		total += changed
	}
	return total, nil
}

// expireStoredValueCard 单卡到期处理（同一事务）：状态置 expired 并作废未消费赠送权益。
// 状态更新以 status=active 为条件，保证赠送作废只随「首次到期」执行一次；作废流水幂等另靠
// (source_type, source_id, entry_type) 唯一索引兜底。
func expireStoredValueCard(ctx context.Context, db *ent.Client, cardID int, now time.Time) (int, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	card, err := tx.EsportsStoredValueCard.Get(ctx, cardID)
	if err != nil {
		return 0, err
	}
	if card.Status != esportsstoredvaluecard.StatusActive || card.ValidUntil.After(now.UTC()) {
		return 0, nil
	}
	// 作废赠送与卡消费/退卡共用卡锁，避免并发重复作废或与消费互相覆盖。
	unlockCard := lockMoneyAccount("card:" + strconv.Itoa(cardID))
	defer unlockCard()
	balance, err := storedValueCardBalanceTx(ctx, tx, cardID)
	if err != nil {
		return 0, err
	}
	changed, err := tx.EsportsStoredValueCard.Update().Where(
		esportsstoredvaluecard.IDEQ(cardID),
		esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.StatusActive),
		esportsstoredvaluecard.ValidUntilLTE(now.UTC()),
	).SetStatus(esportsstoredvaluecard.StatusExpired).AddVersion(1).Save(ctx)
	if err != nil {
		return 0, err
	}
	if changed != 1 {
		return 0, nil
	}
	// 作废未消费赠送权益（本金保留待退卡）。
	if balance.BonusCents > 0 {
		if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
			SetAccountType(esportswallettx.AccountTypeStoredValueCard).SetOwnerID(cardID).
			SetEntryType(esportswallettx.EntryTypeExpire).
			SetPrincipalDeltaCents(0).SetBonusDeltaCents(-balance.BonusCents).
			SetSourceType("stored_value_expire").SetSourceID(card.CardNo).
			SetBeneficiaryType(esportswallettx.BeneficiaryTypeCustomer).SetBeneficiaryID(card.CustomerID).
			SetNote("储值卡到期赠送权益作废").Save(ctx); err != nil {
			if !ent.IsConstraintError(err) {
				return 0, err
			}
			// 已作废过：批次账本与分摊同样无需重复写。
			if err = tx.Commit(); err != nil {
				return 0, err
			}
			return 1, nil
		}
		// 批次账本同步减少并登记分摊（§4.5-04）：赠送作废也要能在批次层追溯到。
		if _, err = tx.EsportsLedgerEntry.Create().
			SetAccountType(storedValuePrepayAccount).SetAccountID(strconv.Itoa(cardID)).
			SetSourceType("stored_value_expire").SetSourceID(card.CardNo).
			SetEntryRole("card_bonus_expire").SetDeltaCents(-balance.BonusCents).
			SetPrincipalDeltaCents(0).SetBonusDeltaCents(-balance.BonusCents).Save(ctx); err != nil {
			if !ent.IsConstraintError(err) {
				return 0, err
			}
		} else if err = allocateLedgerConsumptionWithComposition(ctx, tx, storedValuePrepayAccount,
			strconv.Itoa(cardID), "stored_value_expire", card.CardNo, 0, balance.BonusCents); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return 1, nil
}

// storedValueRefundQuote 退卡试算（§4.4）：可退 = 未消费本金；未消费赠送按合同作废、不折现。
// 纯函数，便于测试与在接口上「展示计算明细」。
func storedValueRefundQuote(card *ent.EsportsStoredValueCard, balance walletBalance, now time.Time) (map[string]any, int64) {
	refundable := balance.PrincipalCents
	if refundable < 0 {
		refundable = 0
	}
	bonusForfeited := balance.BonusCents
	if bonusForfeited < 0 {
		bonusForfeited = 0
	}
	quote := map[string]any{
		"card_no":                   card.CardNo,
		"opened_principal_cents":    card.PrincipalCents,
		"opened_bonus_cents":        card.BonusCents,
		"consumed_principal_cents":  card.PrincipalCents - balance.PrincipalCents,
		"consumed_bonus_cents":      card.BonusCents - balance.BonusCents,
		"remaining_principal_cents": balance.PrincipalCents,
		"remaining_bonus_cents":     balance.BonusCents,
		"refundable_cents":          refundable,
		"bonus_forfeited_cents":     bonusForfeited,
		"valid_until":               card.ValidUntil,
		"quoted_at":                 now.UTC(),
	}
	return quote, refundable
}

// storedValueRefundQuoteFor 读取当前余额并试算，供接口直接展示。
func storedValueRefundQuoteFor(ctx context.Context, db *ent.Client, card *ent.EsportsStoredValueCard, now time.Time) (map[string]any, int64, error) {
	balance, err := storedValueCardBalance(ctx, db, card.ID)
	if err != nil {
		return nil, 0, err
	}
	quote, refundable := storedValueRefundQuote(card, balance, now)
	return quote, refundable, nil
}

// requestStoredValueRefund 申请退卡：状态转 refund_pending 并快照试算明细，等财务审核。
func requestStoredValueRefund(ctx context.Context, db *ent.Client, adminID, cardID int, now time.Time) (*storedValueCardView, error) {
	if cardID < 1 {
		return nil, errInvalidInput
	}
	card, err := db.EsportsStoredValueCard.Get(ctx, cardID)
	if err != nil {
		return nil, err
	}
	switch card.Status {
	case esportsstoredvaluecard.StatusActive, esportsstoredvaluecard.StatusExhausted, esportsstoredvaluecard.StatusExpired:
	case esportsstoredvaluecard.StatusRefundPending:
		return nil, errConflict
	default:
		return nil, errStoredValueUnusable
	}
	quote, refundable, err := storedValueRefundQuoteFor(ctx, db, card, now)
	if err != nil {
		return nil, err
	}
	if refundable <= 0 {
		return nil, fmt.Errorf("%w：该卡已无未消费本金，无需退卡", errInvalidInput)
	}
	changed, err := db.EsportsStoredValueCard.Update().Where(
		esportsstoredvaluecard.IDEQ(card.ID),
		esportsstoredvaluecard.VersionEQ(card.Version),
		esportsstoredvaluecard.StatusIn(
			esportsstoredvaluecard.StatusActive,
			esportsstoredvaluecard.StatusExhausted,
			esportsstoredvaluecard.StatusExpired),
	).SetStatus(esportsstoredvaluecard.StatusRefundPending).
		SetRefundQuote(quote).SetRefundRequestedByAdminID(adminID).
		SetRefundRequestedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	updated, err := db.EsportsStoredValueCard.Get(ctx, card.ID)
	if err != nil {
		return nil, err
	}
	return storedValueCardViewOf(ctx, db, updated)
}

// storedValueRefundPayout 退卡出账的执行凭证（P1）：财务实际打款后登记的打款凭证 / 银行流水号与打款备注。
// 审核通过即「已出账」，因此出账凭据必须先有：不登记的审核一律拒绝，避免一键出账无法对账。
type storedValueRefundPayout struct {
	PayoutRef  string `json:"payout_ref"`
	PayoutNote string `json:"payout_note"`
}

// normalizeStoredValuePayoutRef 校验打款凭证 / 银行流水号（P1）：去空白后 6–128 个字符，
// 只允许字母（含中文）、数字与 - _ / . 等银行流水、回单号常见字符；空白或其它符号一律判参数错误。
func normalizeStoredValuePayoutRef(raw string) (string, error) {
	ref := strings.TrimSpace(raw)
	if n := len([]rune(ref)); n < 6 || n > 128 {
		return "", fmt.Errorf("%w：退卡出账必须填写打款凭证或银行流水号（6-128 个字符）", errInvalidInput)
	}
	for _, r := range ref {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
		case r == '-', r == '_', r == '/', r == '.':
		default:
			return "", fmt.Errorf("%w：打款凭证号只能包含字母、数字与 - _ / . 等字符", errInvalidInput)
		}
	}
	return ref, nil
}

// adminHasFinanceRole 判断操作人是否具备财务类角色（P1 退卡出账门槛）：超级管理员直接放行；
// 否则须命中既有配置 app.esports.finance_report_role_names 中**启用中**的角色名，复用既有角色机制、
// 不新增配置项。配置为空表示仅超级管理员可出账（与既有「财务角色」口径一致）；账号不存在视为无权限。
func adminHasFinanceRole(ctx context.Context, db *ent.Client, adminID int) (bool, error) {
	if adminID < 1 {
		return false, nil
	}
	operator, err := db.Admin.Get(ctx, adminID)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if operator.IsSuperAdmin {
		return true, nil
	}
	allowed := financeReportRoleNames(ctx, db)
	if len(allowed) == 0 {
		return false, nil
	}
	rows, err := db.AdminRole.Query().Where(adminrole.AdminIDEQ(adminID)).WithRole().All(ctx)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.Edges.Role == nil || row.Edges.Role.Status != role.StatusActive {
			continue
		}
		for _, name := range allowed {
			if row.Edges.Role.Name == name {
				return true, nil
			}
		}
	}
	return false, nil
}

// decideStoredValueRefund 财务审核退卡：通过与驳回都要求与申请人不同人（不得自审）。
// 通过时在同一事务内写 refund_return 冲减本金与赠送并置 refunded；驳回把状态还原为可用状态。
//
// P1 出账门槛：通过（即「已出账」）必须同时满足——
//   - 登记打款凭证 / 银行流水号（payout.PayoutRef，缺失或格式不合理判 errInvalidInput）；
//   - 操作人具备财务类角色（adminHasFinanceRole，否则 errForbidden）；
//   - 审核人 ≠ 申请人（既有硬约束）。
//
// payout 用可变参数保持既有调用点（handler）不变；未传凭据时审核通过会被明确拒绝，不会静默出一笔无凭据的钱。
func decideStoredValueRefund(ctx context.Context, db *ent.Client, adminID, cardID int, approve bool, now time.Time,
	payouts ...storedValueRefundPayout) (*storedValueCardView, error) {
	if cardID < 1 {
		return nil, errInvalidInput
	}
	var payout storedValueRefundPayout
	if len(payouts) > 0 {
		payout = payouts[0]
	}
	payout.PayoutNote = strings.TrimSpace(payout.PayoutNote)
	var payoutRef string
	var err error
	if approve {
		// 出账前的强校验统一放在事务之前：缺凭据 / 无财务角色一律不开事务，避免留下半截状态。
		if payoutRef, err = normalizeStoredValuePayoutRef(payout.PayoutRef); err != nil {
			return nil, err
		}
		if len([]rune(payout.PayoutNote)) > 255 {
			return nil, fmt.Errorf("%w：打款备注不得超过 255 个字符", errInvalidInput)
		}
		allowed, err := adminHasFinanceRole(ctx, db, adminID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, fmt.Errorf("%w：退卡出账须由财务角色执行", errForbidden)
		}
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	card, err := tx.EsportsStoredValueCard.Get(ctx, cardID)
	if err != nil {
		return nil, err
	}
	if card.Status != esportsstoredvaluecard.StatusRefundPending {
		return nil, errConflict
	}
	if card.RefundRequestedByAdminID != nil && *card.RefundRequestedByAdminID == adminID {
		return nil, fmt.Errorf("%w：退卡审核不得由申请人本人执行", errInvalidInput)
	}
	utc := now.UTC()
	if !approve {
		// 驳回：按当前余额与有效期还原为可用状态，并清掉试算快照避免误读。
		balance, err := storedValueCardBalanceTx(ctx, tx, card.ID)
		if err != nil {
			return nil, err
		}
		restored := esportsstoredvaluecard.StatusActive
		switch {
		case !utc.Before(card.ValidUntil):
			restored = esportsstoredvaluecard.StatusExpired
		case balance.total() <= 0:
			restored = esportsstoredvaluecard.StatusExhausted
		}
		changed, err := tx.EsportsStoredValueCard.Update().Where(
			esportsstoredvaluecard.IDEQ(card.ID),
			esportsstoredvaluecard.VersionEQ(card.Version),
			esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.StatusRefundPending),
		).SetStatus(restored).ClearRefundQuote().
			SetRefundDecidedByAdminID(adminID).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		updated, err := db.EsportsStoredValueCard.Get(ctx, card.ID)
		if err != nil {
			return nil, err
		}
		return storedValueCardViewOf(ctx, db, updated)
	}
	// 退卡与卡消费/到期作废共用卡锁，避免并发把同一笔未消费本金退两次。
	unlockCard := lockMoneyAccount("card:" + strconv.Itoa(card.ID))
	defer unlockCard()
	balance, err := storedValueCardBalanceTx(ctx, tx, card.ID)
	if err != nil {
		return nil, err
	}
	_, refundable := storedValueRefundQuote(card, balance, now)
	if refundable <= 0 {
		return nil, fmt.Errorf("%w：该卡已无未消费本金，无需退卡", errInvalidInput)
	}
	forfeited := balance.total() - refundable
	// 退卡：本金退回、赠送作废，两者分列记账（§4.4 赠送与本金额分开记账）。
	// 打款凭证 / 流水号与出账人、出账时点一并快照在流水的 rule_snapshot 里，
	// 事后可按「谁出的账、凭哪条流水出账」对账；流水只增不改，快照随本次出账一次性写入。
	refundNote := "退卡：本金退回，赠送作废；打款凭证 " + payoutRef
	if payout.PayoutNote != "" {
		refundNote += "（" + payout.PayoutNote + "）"
	}
	if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypeStoredValueCard).SetOwnerID(card.ID).
		SetEntryType(esportswallettx.EntryTypeRefundReturn).
		SetPrincipalDeltaCents(-balance.PrincipalCents).SetBonusDeltaCents(-balance.BonusCents).
		SetSourceType("stored_value_refund").SetSourceID(card.CardNo).
		SetOperatorID(adminID).SetNote(refundNote).
		SetRuleSnapshot(map[string]any{
			"card_no": card.CardNo, "payout_ref": payoutRef, "payout_note": payout.PayoutNote,
			"refunded_cents": refundable, "bonus_forfeited_cents": forfeited,
			"decided_by_admin_id": adminID, "decided_at": utc,
		}).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsLedgerEntry.Create().
		SetAccountType(storedValuePrepayAccount).SetAccountID(strconv.Itoa(card.ID)).
		SetSourceType("stored_value_refund").SetSourceID(card.CardNo).
		SetEntryRole("card_refund").SetDeltaCents(-balance.total()).
		SetFundBatch(card.CardNo).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	changed, err := tx.EsportsStoredValueCard.Update().Where(
		esportsstoredvaluecard.IDEQ(card.ID),
		esportsstoredvaluecard.VersionEQ(card.Version),
		esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.StatusRefundPending),
	).SetStatus(esportsstoredvaluecard.StatusRefunded).
		SetRefundedCents(refundable).
		SetRefundDecidedByAdminID(adminID).SetRefundedAt(utc).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	// 试算里的作废赠送额随卡落档，便于对账解释「本金退了、赠送没退」。
	if forfeited > 0 {
		if _, err = tx.EsportsLedgerEntry.Create().
			SetAccountType(storedValuePrepayAccount).SetAccountID(strconv.Itoa(card.ID)).
			SetSourceType("stored_value_refund").SetSourceID(card.CardNo).
			SetEntryRole("card_bonus_forfeit").SetDeltaCents(0).
			SetFundBatch(card.CardNo).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	updated, err := db.EsportsStoredValueCard.Get(ctx, card.ID)
	if err != nil {
		return nil, err
	}
	return storedValueCardViewOf(ctx, db, updated)
}

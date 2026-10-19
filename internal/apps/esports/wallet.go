package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsquotahold"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/esportswalletaccount"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

const (
	walletCustomerBalance = "customer_balance"
	walletPlayerCatfood   = "player_catfood"
)

// errInsufficientBalance 余额不足以完成扣减。
var errInsufficientBalance = errors.New("钱包余额不足")

// prepayDeductionOrderKey 预付款扣减顺序配置键（§4.5）：bonus_first 先赠送后本金 /
// principal_first 先本金后赠送 / proportional 本金与赠送按剩余比例分摊。
const prepayDeductionOrderKey = "app.esports.prepay_deduction_order"

// prepayDeductionOrder 读取扣减顺序配置；缺失或非法回落 bonus_first（§4.5 默认「先赠送后本金」）。
func prepayDeductionOrder(ctx context.Context, db *ent.Client) string {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(prepayDeductionOrderKey)).Only(ctx)
	if err != nil {
		return "bonus_first"
	}
	switch value := strings.TrimSpace(item.Value); value {
	case "principal_first", "proportional":
		return value
	default:
		return "bonus_first"
	}
}

// splitPrepayDeduction 按扣减顺序把应付额拆成本金 / 赠送两部分（§4.5）。
// principal_first 先扣本金（余额 / 储值卡的本金批次优先）；proportional 按本金 / 赠送剩余额比例分摊
// （本金向下取整，尾差归赠送），先扣赠送则为默认。拆分之和恒等于应付额，故调用前须保证余额充足。
func splitPrepayDeduction(order string, principalCents, bonusCents, amountCents int64) (principalDeduct, bonusDeduct int64) {
	switch order {
	case "principal_first":
		principalDeduct = min(principalCents, amountCents)
		return principalDeduct, amountCents - principalDeduct
	case "proportional":
		total := principalCents + bonusCents
		if total <= 0 {
			return 0, 0
		}
		principalDeduct = amountCents * principalCents / total
		return principalDeduct, amountCents - principalDeduct
	default:
		bonusDeduct = min(bonusCents, amountCents)
		return amountCents - bonusDeduct, bonusDeduct
	}
}

type walletInput struct {
	PrincipalCents int64  `json:"principal_cents"`
	BonusCents     int64  `json:"bonus_cents"`
	Note           string `json:"note"`
}

type walletAccountView struct {
	AccountType    string `json:"account_type"`
	OwnerID        int    `json:"owner_id"`
	DisplayName    string `json:"display_name"`
	Status         string `json:"status"`
	PrincipalCents int64  `json:"principal_cents"`
	BonusCents     int64  `json:"bonus_cents"`
	BalanceCents   int64  `json:"balance_cents"`
	// 展示口径（§11.4）：工作室可配的账户展示名称、图标、说明与固定倍率换算金额。
	Display walletDisplay `json:"display"`
}

type walletBalance struct {
	PrincipalCents int64
	BonusCents     int64
}

// userWalletDisplayHandler 客户端钱包展示口径（§11.4）：按账户类型下发展示名称、图标、说明、
// 单位名与固定倍率，客户余额 / 陪玩猫粮共用同一份配置。
// 只读系统配置、不涉及余额，故不下发 display_amount：金额由客户端拿自己的余额按 unit_cents 换算一次，
// 避免服务端与客户端各算一遍造成二次换算。
func userWalletDisplayHandler(c fiber.Ctx) error {
	return reply(c, walletDisplayConfig(c.Context(), global.ORM))
}

func (b walletBalance) total() int64 { return b.PrincipalCents + b.BonusCents }

// walletBeneficiaryFor 由账户类型推出归属人（§11.6-02）：客户余额归属客户、陪玩猫粮归属陪玩。
// account_type=stored_value_card 的归属人须查卡上的 customer_id，不在本函数覆盖范围
// （卡账户的写入点自行解析后 SetBeneficiaryID）。
func walletBeneficiaryFor(accountType string, ownerID int) (esportswallettx.BeneficiaryType, int) {
	if accountType == walletPlayerCatfood {
		return esportswallettx.BeneficiaryTypePlayer, ownerID
	}
	return esportswallettx.BeneficiaryTypeCustomer, ownerID
}

func normalizeWalletType(accountType string) (string, error) {
	switch strings.TrimSpace(accountType) {
	case walletCustomerBalance:
		return walletCustomerBalance, nil
	case walletPlayerCatfood:
		return walletPlayerCatfood, nil
	default:
		return "", errInvalidInput
	}
}

// walletBalanceOf 汇总某账户流水得出余额。余额不落库，一律按流水求和，
// 结构上不存在"账户余额与流水之和对不上"的状态。
// ponytail: 逐行求和为 O(n)，单账户流水量级到十万级时改为 SQL SUM 聚合。
func walletBalanceOf(ctx context.Context, db *ent.Client, accountType string, ownerID int) (walletBalance, error) {
	rows, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountType(accountType)),
		esportswallettx.OwnerIDEQ(ownerID),
	).Select(esportswallettx.FieldPrincipalDeltaCents, esportswallettx.FieldBonusDeltaCents).All(ctx)
	if err != nil {
		return walletBalance{}, err
	}
	var balance walletBalance
	for _, row := range rows {
		balance.PrincipalCents += row.PrincipalDeltaCents
		balance.BonusCents += row.BonusDeltaCents
	}
	return balance, nil
}

func walletAccountExists(ctx context.Context, db *ent.Client, accountType string, ownerID int) (bool, error) {
	if accountType == walletCustomerBalance {
		return db.EsportsCustomer.Query().Where(esportscustomer.IDEQ(ownerID)).Exist(ctx)
	}
	return db.EsportsPlayer.Query().Where(esportsplayer.IDEQ(ownerID)).Exist(ctx)
}

func ensureWalletAccount(ctx context.Context, tx *ent.Tx, accountType string, ownerID int, displayName string) (*ent.EsportsWalletAccount, error) {
	q := func() *ent.EsportsWalletAccountQuery {
		return tx.EsportsWalletAccount.Query().Where(
			esportswalletaccount.AccountTypeEQ(esportswalletaccount.AccountType(accountType)),
			esportswalletaccount.OwnerIDEQ(ownerID),
		)
	}
	account, err := q().Only(ctx)
	if err == nil {
		return account, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	account, err = tx.EsportsWalletAccount.Create().SetAccountType(esportswalletaccount.AccountType(accountType)).
		SetOwnerID(ownerID).SetDisplayName(displayName).Save(ctx)
	if ent.IsConstraintError(err) {
		return q().Only(ctx)
	}
	return account, err
}

// walletOwnerName 校验归属人存在，并返回钱包展示名。
func walletOwnerName(ctx context.Context, tx *ent.Tx, accountType string, ownerID int) (string, error) {
	if accountType == walletCustomerBalance {
		customer, err := tx.EsportsCustomer.Get(ctx, ownerID)
		if err != nil {
			return "", err
		}
		return customer.DisplayName, nil
	}
	player, err := tx.EsportsPlayer.Get(ctx, ownerID)
	if err != nil {
		return "", err
	}
	return player.DisplayName, nil
}

func walletView(ctx context.Context, db *ent.Client, account *ent.EsportsWalletAccount) (*walletAccountView, error) {
	return walletViewWith(ctx, db, account, walletDisplayConfig(ctx, db))
}

// walletViewWith 组装钱包视图，展示配置由调用方统一读取一次，避免列表接口逐行读配置。
func walletViewWith(ctx context.Context, db *ent.Client, account *ent.EsportsWalletAccount,
	displays map[string]walletDisplay) (*walletAccountView, error) {
	balance, err := walletBalanceOf(ctx, db, account.AccountType.String(), account.OwnerID)
	if err != nil {
		return nil, err
	}
	return &walletAccountView{
		AccountType: account.AccountType.String(), OwnerID: account.OwnerID,
		DisplayName: account.DisplayName, Status: account.Status.String(),
		PrincipalCents: balance.PrincipalCents, BonusCents: balance.BonusCents,
		BalanceCents: balance.total(),
		Display:      walletDisplayFor(displays, account.AccountType.String(), balance.total()),
	}, nil
}

// listWalletAccounts 列出某类钱包账户及其派生余额，keyword 匹配展示名。
func listWalletAccounts(ctx context.Context, db *ent.Client, adminID int, isSuper bool, accountType, keyword string, limit int) ([]*walletAccountView, error) {
	normalized, err := normalizeWalletType(accountType)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsWalletAccount.Query().Where(
		esportswalletaccount.AccountTypeEQ(esportswalletaccount.AccountType(normalized)),
	)
	if normalized == walletCustomerBalance && !isSuper {
		ids, err := db.EsportsCustomer.Query().Where(esportscustomer.OwnerAdminIDEQ(adminID)).IDs(ctx)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []*walletAccountView{}, nil
		}
		q.Where(esportswalletaccount.OwnerIDIn(ids...))
	}
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		q.Where(esportswalletaccount.DisplayNameContains(keyword))
	}
	accounts, err := q.Order(ent.Desc(esportswalletaccount.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]*walletAccountView, 0, len(accounts))
	displays := walletDisplayConfig(ctx, db)
	for _, account := range accounts {
		view, err := walletViewWith(ctx, db, account, displays)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func listWalletTransactions(ctx context.Context, db *ent.Client, accountType string, ownerID, limit int) ([]*ent.EsportsWalletTx, error) {
	normalized, err := normalizeWalletType(accountType)
	if err != nil {
		return nil, err
	}
	if ownerID < 1 {
		return nil, errInvalidInput
	}
	if exists, err := walletAccountExists(ctx, db, normalized, ownerID); err != nil {
		return nil, err
	} else if !exists {
		return nil, &ent.NotFoundError{}
	}
	if limit < 1 || limit > 500 {
		limit = 200
	}
	rows, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountType(normalized)),
		esportswallettx.OwnerIDEQ(ownerID),
	).Order(ent.Desc(esportswallettx.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	if err = backfillWalletTxSelfProving(ctx, db, normalized, ownerID, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// backfillWalletTxSelfProving 为「未快照」的历史流水补齐读模型字段（§11.6-02）：归属人、资金批次、
// 变动前后余额。只改内存中的返回副本，不落库（钱包流水只增不改）。
// 已写入的三项保持原值；仅当字段确为未快照（归属人 0 / 批次空 / 前后余额均为 0）时才派生。
func backfillWalletTxSelfProving(ctx context.Context, db *ent.Client, accountType string, ownerID int,
	rows []*ent.EsportsWalletTx) error {
	if len(rows) == 0 {
		return nil
	}
	derivedType, derivedID := walletBeneficiaryFor(accountType, ownerID)
	if accountType == walletStoredValueCard {
		if card, err := db.EsportsStoredValueCard.Get(ctx, ownerID); err == nil {
			derivedType, derivedID = esportswallettx.BeneficiaryTypeCustomer, card.CustomerID
		}
	}
	needBalance := false
	for _, row := range rows {
		row.BeneficiaryType, row.BeneficiaryID = derivedType, derivedID
		if row.FundBatch == "" && row.EntryType == esportswallettx.EntryTypeRecharge {
			row.FundBatch = row.SourceID
		}
		if row.BalanceBeforeCents == 0 && row.BalanceAfterCents == 0 {
			needBalance = true
		}
	}
	if !needBalance {
		return nil
	}
	// 按 ID 升序重放该账户全部流水，得到每笔的变动前后余额。
	all, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountType(accountType)),
		esportswallettx.OwnerIDEQ(ownerID),
	).Order(ent.Asc(esportswallettx.FieldID)).
		Select(esportswallettx.FieldID, esportswallettx.FieldPrincipalDeltaCents,
			esportswallettx.FieldBonusDeltaCents).All(ctx)
	if err != nil {
		return err
	}
	type snapshot struct{ before, after int64 }
	replay := make(map[int]snapshot, len(all))
	var running int64
	for _, row := range all {
		before := running
		running += row.PrincipalDeltaCents + row.BonusDeltaCents
		replay[row.ID] = snapshot{before: before, after: running}
	}
	for _, row := range rows {
		if row.BalanceBeforeCents != 0 || row.BalanceAfterCents != 0 {
			continue
		}
		if item, ok := replay[row.ID]; ok {
			row.BalanceBeforeCents, row.BalanceAfterCents = item.before, item.after
		}
	}
	return nil
}

// walletRecharge 充值 / 发放本金与赠送权益。本金与赠送同事务写入同一条流水，分列记账。
// ponytail: 充值活动、阶梯赠送与余额拆分批次未实现，赠送金额由调用方直接给定。
func walletRecharge(ctx context.Context, db *ent.Client, operatorID int, accountType string, ownerID int, in walletInput, key string) (*walletAccountView, error) {
	return walletEntry(ctx, db, operatorID, accountType, ownerID, in, key, esportswallettx.EntryTypeRecharge)
}

// walletAdjust 管理员调整余额。允许负数，但不得把任一子账户调成负数，且必须填写原因。
func walletAdjust(ctx context.Context, db *ent.Client, operatorID int, accountType string, ownerID int, in walletInput, key string) (*walletAccountView, error) {
	if strings.TrimSpace(in.Note) == "" {
		return nil, errInvalidInput
	}
	return walletEntry(ctx, db, operatorID, accountType, ownerID, in, key, esportswallettx.EntryTypeAdjust)
}

func walletEntry(ctx context.Context, db *ent.Client, operatorID int, accountType string, ownerID int, in walletInput, key string, entryType esportswallettx.EntryType) (*walletAccountView, error) {
	normalized, err := normalizeWalletType(accountType)
	if err != nil {
		return nil, err
	}
	in.Note = strings.TrimSpace(in.Note)
	key = strings.TrimSpace(key)
	negative := in.PrincipalCents < 0 || in.BonusCents < 0
	if ownerID < 1 || in.PrincipalCents+in.BonusCents == 0 ||
		(entryType == esportswallettx.EntryTypeRecharge && negative) ||
		len([]rune(in.Note)) > 255 || key == "" || len(key) > 80 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	displayName, err := walletOwnerName(ctx, tx, normalized, ownerID)
	if err != nil {
		return nil, err
	}
	account, err := ensureWalletAccount(ctx, tx, normalized, ownerID, displayName)
	if err != nil {
		return nil, err
	}
	if account.Status != esportswalletaccount.StatusActive {
		return nil, errConflict
	}
	current, err := walletBalanceTx(ctx, tx, normalized, ownerID)
	if err != nil {
		return nil, err
	}
	if current.PrincipalCents+in.PrincipalCents < 0 || current.BonusCents+in.BonusCents < 0 {
		return nil, errInsufficientBalance
	}
	txNo := uuid.NewString()
	beneficiaryType, beneficiaryID := walletBeneficiaryFor(normalized, ownerID)
	if _, err = tx.EsportsWalletTx.Create().SetTxNo(txNo).
		SetAccountType(esportswallettx.AccountType(normalized)).SetOwnerID(ownerID).
		SetEntryType(entryType).SetPrincipalDeltaCents(in.PrincipalCents).
		SetBonusDeltaCents(in.BonusCents).SetSourceType("manual_" + entryType.String()).
		SetSourceID(key).SetOperatorID(operatorID).SetNote(in.Note).
		SetBeneficiaryType(beneficiaryType).SetBeneficiaryID(beneficiaryID).
		SetBalanceBeforeCents(current.total()).
		SetBalanceAfterCents(current.total() + in.PrincipalCents + in.BonusCents).
		SetFundBatch(key).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	// 批次账本（§4.5 / §4.5-04）：充值的正向部分写入批次，本金 / 赠送分列，
	// 供消费时按批次逐笔扣减（§4.3）与按来源性质还原。
	if batchCents := in.PrincipalCents + in.BonusCents; batchCents > 0 {
		if _, err = tx.EsportsLedgerEntry.Create().
			SetAccountType(normalized).SetAccountID(strconv.Itoa(ownerID)).
			SetSourceType("wallet_tx").SetSourceID(txNo).
			SetEntryRole("recharge_batch").SetDeltaCents(batchCents).
			SetPrincipalDeltaCents(in.PrincipalCents).SetBonusDeltaCents(in.BonusCents).
			SetFundBatch(key).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return walletView(ctx, db, account)
}

// walletBalanceTx 事务内汇总余额，避免先 SELECT 后写入造成的并发超扣。
func walletBalanceTx(ctx context.Context, tx *ent.Tx, accountType string, ownerID int) (walletBalance, error) {
	rows, err := tx.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountType(accountType)),
		esportswallettx.OwnerIDEQ(ownerID),
	).Select(esportswallettx.FieldPrincipalDeltaCents, esportswallettx.FieldBonusDeltaCents).All(ctx)
	if err != nil {
		return walletBalance{}, err
	}
	var balance walletBalance
	for _, row := range rows {
		balance.PrincipalCents += row.PrincipalDeltaCents
		balance.BonusCents += row.BonusDeltaCents
	}
	return balance, nil
}

// payOrderWithBalance 用客户余额足额核销订单：同事务扣减钱包流水并把订单转入待派单。
// 扣减顺序固定为先赠送权益、后本金。
// ponytail: 存单扣减与余额/存单混合支付未实现，仅支持余额足额支付；扣减顺序可配置项未落地。
func payOrderWithBalance(ctx context.Context, db *ent.Client, adminID, orderID int, now time.Time) (*ent.EsportsOrder, error) {
	return payOrderWithBalanceCore(ctx, db, adminID, 0, orderID, now)
}

// payOrderWithBalanceAsCustomer 客户自助余额核销（§4.3 / §13.2）：与管理端共用同一核销边界
// （金额、订单状态、支付时限、配额与账本口径全部复用），差异只有三点：
//   - 订单必须属于当前客户（actorCustomerID），避免用他人身份核销；
//   - 钱包流水不写 operator_id（客户不是后台操作人），备注标记「客户自助」；
//   - 订单事件 actor 记为 customer，且站内方式受后台渠道启停约束（§4.3-08）。
func payOrderWithBalanceAsCustomer(ctx context.Context, db *ent.Client, customerID, orderID int,
	now time.Time) (*ent.EsportsOrder, error) {
	if customerID < 1 || orderID < 1 {
		return nil, errInvalidInput
	}
	if !paymentChannelEnabled(ctx, db, walletCustomerBalance) {
		return nil, fmt.Errorf("%w：余额支付已被后台停用，暂不可用于订单支付", errForbidden)
	}
	return payOrderWithBalanceCore(ctx, db, 0, customerID, orderID, now)
}

// payOrderWithBalanceCore 是余额核销的唯一实现：operatorID > 0 表示后台操作人（写 operator_id、
// 事件 actor=admin）；actorCustomerID > 0 表示客户自助（不写 operator_id、事件 actor=customer）。
func payOrderWithBalanceCore(ctx context.Context, db *ent.Client, operatorID, actorCustomerID, orderID int,
	now time.Time) (*ent.EsportsOrder, error) {
	meta, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	// 订单级串行化（§8.2-15）：以「进事务前读到的订单版本」为本次收款的仲裁依据，
	// 任何并发收款动作（另一收款方式核销 / 提交待核验凭证）都会推进该版本，本次写回随之冲突。
	runPaymentOrderInterleaveHook(orderID)
	unlock := lockMoneyAccount("balance:" + strconv.Itoa(meta.CustomerID))
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
	// 订单级占位（§8.2-15）：先把该订单行按读到的版本推进一格，等价于拿订单行写锁。
	// 影响行数为 0 说明另一个收款动作已经推进过该订单（含并发提交的待核验凭证），本次必须失败回滚。
	if err = lockOrderPaymentSlot(ctx, tx, order.ID, meta.Version); err != nil {
		return nil, err
	}
	// 占位已把库里的版本推进到 meta.Version+1，后续条件更新与订单事件版本都以它为准。
	order.Version = meta.Version + 1
	// P0-11 重复收款防护：该订单已有待核验的到账凭证时，不得再用余额核销——否则同一订单被收两次钱，
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
	balance, err := walletBalanceTx(ctx, tx, walletCustomerBalance, customer.ID)
	if err != nil {
		return nil, err
	}
	payable := order.PayableCents
	if balance.total() < payable {
		return nil, errInsufficientBalance
	}
	principalDeduct, bonusDeduct := splitPrepayDeduction(
		prepayDeductionOrder(ctx, tx.Client()), balance.PrincipalCents, balance.BonusCents, payable)
	walletTxCreate := tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(customer.ID).
		SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-principalDeduct).SetBonusDeltaCents(-bonusDeduct).
		SetSourceType("order").SetSourceID(order.OrderNo).
		SetBeneficiaryType(esportswallettx.BeneficiaryTypeCustomer).SetBeneficiaryID(customer.ID).
		SetBalanceBeforeCents(balance.total()).SetBalanceAfterCents(balance.total() - payable)
	// operator_id 是后台操作人字段：客户自助不写，改用备注与订单事件 actor 区分来源。
	if operatorID > 0 {
		walletTxCreate.SetOperatorID(operatorID)
	}
	note := "订单余额核销"
	if actorCustomerID > 0 {
		note = "订单余额核销（客户自助）"
	}
	walletTx, err := walletTxCreate.SetNote(note).Save(ctx)
	if err != nil {
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
		SetPaymentChannel(esportsorder.PaymentChannelCustomerBalance).
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
	if _, err = tx.EsportsLedgerEntry.Create().SetAccountType("customer_order_prepayment").
		SetAccountID(strconv.Itoa(customer.ID)).SetSourceType("wallet_tx").
		SetSourceID(strconv.Itoa(walletTx.ID)).SetEntryRole("applied_order_prepayment").
		SetDeltaCents(payable).Save(ctx); err != nil {
		return nil, err
	}
	// 批次级逐笔扣减（§4.3 / §4.5 / §4.5-04）：记录本次余额消费动用了哪些充值批次，
	// 并按实际扣减构成分别记本金 / 赠送动用量。
	if err = allocateLedgerConsumptionWithComposition(ctx, tx, walletCustomerBalance, strconv.Itoa(customer.ID),
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
			"channel": "customer_balance", "applied_cents": payable,
			"principal_deducted": principalDeduct, "bonus_deducted": bonusDeduct,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("order.payment_verified:" + order.OrderNo).
		SetEventType("order.payment_verified").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{"order_no": order.OrderNo, "channel": "customer_balance"}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// ── 收款并发窗口的订单级串行化（§8.2-15 / 附录 B.3-1） ──
//
// 余额核销、储值卡核销、转账凭证提交/核验三条路径都要先判「同一订单是否已有待核验凭证」，
// 再决定是否把订单转已付款。只靠「同事务内读凭证」关不掉极端并发窗口：两个请求可能各自读到
// 「无凭证」，随后都写入收款结果——后到的那笔钱只能挂成悬账（同一订单收两次钱）。
//
// 选用机制：**订单行乐观版本守卫（条件更新式占位）**，而不是 SELECT ... FOR UPDATE。
//   - MySQL 下 `UPDATE esports_orders SET version = version + 1 WHERE id = ? AND version = ?`
//     本身就会对该行加行级写锁：并发事务被串行化到前一个提交之后，后到者按旧版本更新影响 0 行，
//     与 FOR UPDATE 的串行化语义等价，且不依赖 Ent 生成代码的方言特性（本项目 Ent 版本未提供
//     ForUpdate 查询构建器，SQLite 方言也不支持）。
//   - 内存 SQLite 没有 SELECT ... FOR UPDATE，且并发写事务会直接互相锁表（"database is locked"），
//     无法在测试里稳定复现「两个写事务同时挂起」。因此三条路径统一用同一套版本守卫表达约束，
//     测试改以可控交错复现同一时序：A 读到旧版本 → B 完整提交 → A 写回时必须影响 0 行。
//     两种数据库下的可观察结果一致：后写者拿到 errConflict，且不会产生任何资金/状态变更。
//
// 关键点：占位用的 expectedVersion 必须来自「进入事务之前」的读，否则事务内重新读到的
// 已是新版本，守卫恒通过、窗口依然存在（这正是本机制与既有「最终更新加 VersionEQ」的差别）。

// paymentOrderInterleaveHook 是仅用于测试的可控交错点：在「读订单版本」与「版本守卫写回」
// 之间插入一次外部提交。生产路径恒为 nil。
var paymentOrderInterleaveHook func(orderID int)

// runPaymentOrderInterleaveHook 在测试注入交错点时执行它（nil 时零开销）。
func runPaymentOrderInterleaveHook(orderID int) {
	if hook := paymentOrderInterleaveHook; hook != nil {
		hook(orderID)
	}
}

// lockOrderPaymentSlot 在事务内对订单行做一次版本守卫占位：版本推进 1，影响行数不为 1 即冲突。
// expectedVersion 来自调用方在事务前的读；同时要求订单仍处于「待付款」状态，避免占位
// 把已付款订单当成可收款对象。
func lockOrderPaymentSlot(ctx context.Context, tx *ent.Tx, orderID, expectedVersion int) error {
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID),
		esportsorder.VersionEQ(expectedVersion),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
	).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w：该订单已被其它收款动作占单处理，请刷新后用最新状态重试", errConflict)
	}
	return nil
}

// reserveOrderPaymentVersion 在独立短事务里对订单行做一次版本守卫占位并提交。
// 用于「收款判定与落库不在同一事务」的路径（凭证核验复用 applyVerifiedPayment 自带的事务边界）：
// 提交式占位推进订单版本，使并发中已读到旧版本的余额 / 储值卡核销在写回时守卫失败。
func reserveOrderPaymentVersion(ctx context.Context, db *ent.Client, orderID int) error {
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return err
	}
	runPaymentOrderInterleaveHook(orderID)
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockOrderPaymentSlot(ctx, tx, order.ID, order.Version); err != nil {
		return err
	}
	return tx.Commit()
}

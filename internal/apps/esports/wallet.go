package esports

import (
	"context"
	"errors"
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
	return db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountType(normalized)),
		esportswallettx.OwnerIDEQ(ownerID),
	).Order(ent.Desc(esportswallettx.FieldID)).Limit(limit).All(ctx)
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
	if _, err = tx.EsportsWalletTx.Create().SetTxNo(txNo).
		SetAccountType(esportswallettx.AccountType(normalized)).SetOwnerID(ownerID).
		SetEntryType(entryType).SetPrincipalDeltaCents(in.PrincipalCents).
		SetBonusDeltaCents(in.BonusCents).SetSourceType("manual_" + entryType.String()).
		SetSourceID(key).SetOperatorID(operatorID).SetNote(in.Note).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	// 批次账本（§4.5）：充值的正向部分写入批次，供消费时按批次逐笔扣减（§4.3）。
	if batchCents := in.PrincipalCents + in.BonusCents; batchCents > 0 {
		if _, err = tx.EsportsLedgerEntry.Create().
			SetAccountType(normalized).SetAccountID(strconv.Itoa(ownerID)).
			SetSourceType("wallet_tx").SetSourceID(txNo).
			SetEntryRole("recharge_batch").SetDeltaCents(batchCents).
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
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid || !now.UTC().Before(order.PaymentDeadline) {
		return nil, errConflict
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
	walletTx, err := tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(customer.ID).
		SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-principalDeduct).SetBonusDeltaCents(-bonusDeduct).
		SetSourceType("order").SetSourceID(order.OrderNo).
		SetOperatorID(adminID).SetNote("订单余额核销").Save(ctx)
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
	// 批次级逐笔扣减（§4.3 / §4.5）：记录本次余额消费动用了哪些充值批次。
	if err = allocateLedgerConsumption(ctx, tx, walletCustomerBalance, strconv.Itoa(customer.ID),
		"order", order.OrderNo, payable); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.payment_verified").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
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

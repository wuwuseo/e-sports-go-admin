package esports

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/esportswithdrawal"
	"go-admin/internal/ent/systemconfig"
)

// 提现配置键（§6.3 提现门槛与频率、§11.2 手续费与代扣税费）。缺失或非法时回落默认值：
// 门槛与频率不限、手续费与税费为 0。
const (
	withdrawalMinCentsKey    = "app.esports.withdrawal_min_cents"
	withdrawalMinIntervalKey = "app.esports.withdrawal_min_interval_hours"
	withdrawalDailyLimitKey  = "app.esports.withdrawal_daily_limit"
	withdrawalFeeBpKey       = "app.esports.withdrawal_fee_bp"
	withdrawalTaxBpKey       = "app.esports.withdrawal_tax_bp"
	// 阶梯费率（§11.2）：JSON 数组按金额分档覆盖单一基点，如
	// [{"min_cents":0,"bp":100},{"min_cents":50000,"bp":50}]，取「min_cents ≤ 金额」的最高档。
	withdrawalFeeTiersKey = "app.esports.withdrawal_fee_tiers"
	withdrawalTaxTiersKey = "app.esports.withdrawal_tax_tiers"
	// 提现明细来源类型，驳回退回用独立来源类型以便区分。
	withdrawalSourceType       = "withdrawal"
	withdrawalRejectSourceType = "withdrawal_reject"
)

// withdrawalTier 是阶梯费率的一档：金额 ≥ MinCents 时适用 Bp 基点（万分比）。
type withdrawalTier struct {
	MinCents int64 `json:"min_cents"`
	Bp       int   `json:"bp"`
}

// withdrawalPolicy 提现策略快照。
type withdrawalPolicy struct {
	MinCents         int64
	MinIntervalHours int
	// DailyLimit 每日提现次数上限（按业务时区 UTC+8 的自然日），0 = 不限。
	DailyLimit int
	FeeBp      int
	TaxBp      int
	// 阶梯费率：非空时按金额命中档位覆盖单一基点，FeeBp / TaxBp 作为无阶梯时的回落值。
	FeeTiers []withdrawalTier
	TaxTiers []withdrawalTier
}

// feeBpFor 返回该金额适用的手续费基点：配置了阶梯则取命中档，否则回落单一费率。
func (p withdrawalPolicy) feeBpFor(amountCents int64) int {
	if bp, ok := tierBpFor(p.FeeTiers, amountCents); ok {
		return bp
	}
	return p.FeeBp
}

// taxBpFor 返回该金额适用的代扣税费基点，口径同 feeBpFor。
func (p withdrawalPolicy) taxBpFor(amountCents int64) int {
	if bp, ok := tierBpFor(p.TaxTiers, amountCents); ok {
		return bp
	}
	return p.TaxBp
}

// tierBpFor 在阶梯中取「MinCents ≤ 金额」的最高档基点；无命中返回 ok=false（由调用方回落单一费率）。
func tierBpFor(tiers []withdrawalTier, amountCents int64) (int, bool) {
	idx := -1
	for i, tier := range tiers {
		if amountCents >= tier.MinCents && (idx < 0 || tier.MinCents > tiers[idx].MinCents) {
			idx = i
		}
	}
	if idx < 0 {
		return 0, false
	}
	return tiers[idx].Bp, true
}

// parseWithdrawalTiers 解析阶梯费率配置：JSON 数组，逐档校验基点 0~10000、金额非负；
// 任一项非法或为空即视为未配置（返回 nil，回落单一费率）。
func parseWithdrawalTiers(raw string) []withdrawalTier {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var tiers []withdrawalTier
	if err := json.Unmarshal([]byte(raw), &tiers); err != nil || len(tiers) == 0 {
		return nil
	}
	for _, tier := range tiers {
		if tier.MinCents < 0 || tier.Bp < 0 || tier.Bp > 10000 {
			return nil
		}
	}
	return tiers
}

// withdrawalPolicyOf 读取提现策略配置。各键一次查出，缺失或非法回落默认值。
func withdrawalPolicyOf(ctx context.Context, db *ent.Client) withdrawalPolicy {
	items, err := db.SystemConfig.Query().Where(systemconfig.KeyIn(
		withdrawalMinCentsKey, withdrawalMinIntervalKey, withdrawalDailyLimitKey,
		withdrawalFeeBpKey, withdrawalTaxBpKey, withdrawalFeeTiersKey, withdrawalTaxTiersKey,
	)).Select(systemconfig.FieldKey, systemconfig.FieldValue).All(ctx)
	if err != nil {
		return withdrawalPolicy{}
	}
	values := map[string]string{}
	for _, item := range items {
		values[item.Key] = strings.TrimSpace(item.Value)
	}
	parse := func(key string, min, max int, fallback int) int {
		raw, ok := values[key]
		if !ok {
			return fallback
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < min || value > max {
			return fallback
		}
		return value
	}
	return withdrawalPolicy{
		MinCents:         int64(parse(withdrawalMinCentsKey, 0, 100_000_000, 0)),
		MinIntervalHours: parse(withdrawalMinIntervalKey, 0, 24*30, 0),
		DailyLimit:       parse(withdrawalDailyLimitKey, 0, 100, 0),
		FeeBp:            parse(withdrawalFeeBpKey, 0, 10000, 0),
		TaxBp:            parse(withdrawalTaxBpKey, 0, 10000, 0),
		FeeTiers:         parseWithdrawalTiers(values[withdrawalFeeTiersKey]),
		TaxTiers:         parseWithdrawalTiers(values[withdrawalTaxTiersKey]),
	}
}

// withdrawalApplyInput 提现申请入参（§6.3）。
type withdrawalApplyInput struct {
	AmountCents    int64  `json:"amount_cents"`
	Channel        string `json:"channel"`
	AccountRef     string `json:"account_ref"`
	Note           string `json:"note"`
	IdempotencyKey string `json:"idempotency_key"`
}

// withdrawalReviewInput 财务审核入参。
type withdrawalReviewInput struct {
	Approve bool   `json:"approve"`
	Reason  string `json:"reason"`
}

// applyWithdrawal 陪玩申请提现：校验门槛与频率、试算手续费与代扣税费，并在同一事务内扣减猫粮
// （entry_type=withdraw），避免重复申请造成超额。首版一律待财务审核。
// 幂等：idem_key 唯一索引兜底，重复提交返回既有提现单，不重复扣减。
func applyWithdrawal(ctx context.Context, db *ent.Client, playerID int, in withdrawalApplyInput, now time.Time) (*ent.EsportsWithdrawal, error) {
	key := strings.TrimSpace(in.IdempotencyKey)
	in.AccountRef = strings.TrimSpace(in.AccountRef)
	in.Note = strings.TrimSpace(in.Note)
	switch in.Channel {
	case "bank_card", "wechat", "alipay":
	default:
		return nil, errInvalidInput
	}
	if playerID < 1 || in.AmountCents <= 0 || in.AccountRef == "" ||
		len(in.AccountRef) > 128 || len([]rune(in.Note)) > 255 ||
		len(key) < 8 || len(key) > 128 {
		return nil, errInvalidInput
	}
	policy := withdrawalPolicyOf(ctx, db)
	if policy.MinCents > 0 && in.AmountCents < policy.MinCents {
		return nil, fmt.Errorf("%w：低于最低提现金额 %d 分", errInvalidInput, policy.MinCents)
	}
	// 提现频率：与本人最近一次提现申请（含已驳回）的间隔须满足配置。
	if policy.MinIntervalHours > 0 {
		latest, err := db.EsportsWithdrawal.Query().Where(
			esportswithdrawal.PlayerIDEQ(playerID),
		).Order(ent.Desc(esportswithdrawal.FieldID)).First(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, err
		}
		if latest != nil {
			earliest := latest.CreatedAt.Add(time.Duration(policy.MinIntervalHours) * time.Hour)
			if now.UTC().Before(earliest) {
				return nil, fmt.Errorf("%w：距上次提现申请不足 %d 小时", errInvalidInput, policy.MinIntervalHours)
			}
		}
	}
	// 每日提现次数上限（§6.3）：按业务时区（UTC+8）自然日统计当日申请数（含已驳回）。
	if policy.DailyLimit > 0 {
		count, err := db.EsportsWithdrawal.Query().Where(
			esportswithdrawal.PlayerIDEQ(playerID),
			esportswithdrawal.CreatedAtGTE(businessDayStart(now)),
		).Count(ctx)
		if err != nil {
			return nil, err
		}
		if count >= policy.DailyLimit {
			return nil, fmt.Errorf("%w：今日提现申请已达上限 %d 次", errInvalidInput, policy.DailyLimit)
		}
	}
	// 阶梯费率（§11.2）：命中档位按档位基点，否则回落单一费率。
	fee := in.AmountCents * int64(policy.feeBpFor(in.AmountCents)) / 10000
	tax := in.AmountCents * int64(policy.taxBpFor(in.AmountCents)) / 10000
	net := in.AmountCents - fee - tax
	if net <= 0 {
		return nil, fmt.Errorf("%w：扣除手续费与税费后实际到账须为正", errInvalidInput)
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.EsportsPlayer.Get(ctx, playerID); err != nil {
		return nil, err
	}
	balance, err := walletBalanceTx(ctx, tx, walletPlayerCatfood, playerID)
	if err != nil {
		return nil, err
	}
	if balance.total() < in.AmountCents {
		return nil, errInsufficientBalance
	}
	withdrawal, err := tx.EsportsWithdrawal.Create().SetWithdrawalNo("WD" + uuid.NewString()).
		SetPlayerID(playerID).SetIdemKey(key).
		SetAmountCents(in.AmountCents).SetFeeCents(fee).SetTaxCents(tax).SetNetCents(net).
		SetChannel(esportswithdrawal.Channel(in.Channel)).SetAccountRef(in.AccountRef).
		SetStatus(esportswithdrawal.StatusPendingReview).SetNote(in.Note).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			// 幂等重放：返回既有提现单，不重复扣减。用事务内客户端查询，
			// 避免未提交事务与外部连接不可见造成的误判。
			existing, lookupErr := tx.EsportsWithdrawal.Query().Where(
				esportswithdrawal.IdemKeyEQ(key),
			).Only(ctx)
			if lookupErr != nil {
				return nil, errConflict
			}
			return existing, nil
		}
		return nil, err
	}
	if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypePlayerCatfood).SetOwnerID(playerID).
		SetEntryType(esportswallettx.EntryTypeWithdraw).
		SetPrincipalDeltaCents(-in.AmountCents).
		SetSourceType(withdrawalSourceType).SetSourceID(withdrawal.WithdrawalNo).
		SetNote("陪玩申请提现").Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsWithdrawal.Get(ctx, withdrawal.ID)
}

// reviewWithdrawal 财务审核提现：通过与驳回都要求当前为待审。
// 驳回时写一条 refund_return 把猫粮退回原额（幂等靠唯一索引兜底），并留下驳回原因。
func reviewWithdrawal(ctx context.Context, db *ent.Client, adminID, id int, in withdrawalReviewInput, now time.Time) (*ent.EsportsWithdrawal, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if !in.Approve && (in.Reason == "" || len([]rune(in.Reason)) > 255) {
		return nil, fmt.Errorf("%w：驳回须填写原因", errInvalidInput)
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsWithdrawal.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportswithdrawal.StatusPendingReview {
		return nil, errConflict
	}
	next := esportswithdrawal.StatusApproved
	if !in.Approve {
		next = esportswithdrawal.StatusRejected
	}
	update := tx.EsportsWithdrawal.Update().Where(
		esportswithdrawal.IDEQ(id),
		esportswithdrawal.VersionEQ(item.Version),
		esportswithdrawal.StatusEQ(esportswithdrawal.StatusPendingReview),
	).SetStatus(next).SetReviewedByAdminID(adminID).SetReviewedAt(now.UTC()).AddVersion(1)
	if !in.Approve {
		update.SetRejectReason(in.Reason)
	}
	if _, err = update.Save(ctx); err != nil {
		return nil, err
	}
	if !in.Approve {
		if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
			SetAccountType(esportswallettx.AccountTypePlayerCatfood).SetOwnerID(item.PlayerID).
			SetEntryType(esportswallettx.EntryTypeRefundReturn).
			SetPrincipalDeltaCents(item.AmountCents).
			SetSourceType(withdrawalRejectSourceType).SetSourceID(item.WithdrawalNo).
			SetOperatorID(adminID).SetNote("提现驳回退回").Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsWithdrawal.Get(ctx, id)
}

// payWithdrawal 登记打款：审核通过 → 已打款，留外部打款凭证与完税凭证、到账时间与打款人（§11.2）。
// 打款不再动猫粮钱包——申请时钱已离开猫粮账户。
func payWithdrawal(ctx context.Context, db *ent.Client, adminID, id int, payoutRef, taxRef string, now time.Time) (*ent.EsportsWithdrawal, error) {
	payoutRef = strings.TrimSpace(payoutRef)
	taxRef = strings.TrimSpace(taxRef)
	if id < 1 || payoutRef == "" || len(payoutRef) > 128 || len(taxRef) > 128 {
		return nil, fmt.Errorf("%w：打款须填写外部打款凭证号", errInvalidInput)
	}
	changed, err := db.EsportsWithdrawal.Update().Where(
		esportswithdrawal.IDEQ(id),
		esportswithdrawal.StatusEQ(esportswithdrawal.StatusApproved),
	).SetStatus(esportswithdrawal.StatusPaid).SetPayoutRef(payoutRef).SetTaxRef(taxRef).
		SetPaidByAdminID(adminID).SetPaidAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsWithdrawal.Get(ctx, id)
}

// listWithdrawals 列出提现单，playerID / status 可选过滤，按 ID 倒序。
func listWithdrawals(ctx context.Context, db *ent.Client, playerID int, status string, limit int) ([]*ent.EsportsWithdrawal, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsWithdrawal.Query()
	if playerID > 0 {
		q.Where(esportswithdrawal.PlayerIDEQ(playerID))
	}
	if status = strings.TrimSpace(status); status != "" {
		switch status {
		case "pending_review", "approved", "paid", "rejected":
			q.Where(esportswithdrawal.StatusEQ(esportswithdrawal.Status(status)))
		default:
			return nil, errInvalidInput
		}
	}
	return q.Order(ent.Desc(esportswithdrawal.FieldID)).Limit(limit).All(ctx)
}

func getWithdrawal(ctx context.Context, db *ent.Client, id int) (*ent.EsportsWithdrawal, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	return db.EsportsWithdrawal.Get(ctx, id)
}

// playerWithdrawableView 陪玩端提现概览（§6.3 收入状态）：可提现猫粮余额与待审提现金额。
type playerWithdrawableView struct {
	WithdrawableCents   int64 `json:"withdrawable_cents"`
	PendingReviewCents  int64 `json:"pending_review_cents"`
	TotalWithdrawnCents int64 `json:"total_withdrawn_cents"`
}

// playerWithdrawalSummary 汇总陪玩的可提现余额与提现情况。
func playerWithdrawalSummary(ctx context.Context, db *ent.Client, playerID int) (*playerWithdrawableView, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	if _, err := db.EsportsPlayer.Get(ctx, playerID); err != nil {
		return nil, err
	}
	balance, err := walletBalanceOf(ctx, db, walletPlayerCatfood, playerID)
	if err != nil {
		return nil, err
	}
	rows, err := db.EsportsWithdrawal.Query().Where(
		esportswithdrawal.PlayerIDEQ(playerID),
	).Select(esportswithdrawal.FieldAmountCents, esportswithdrawal.FieldStatus).All(ctx)
	if err != nil {
		return nil, err
	}
	view := playerWithdrawableView{WithdrawableCents: balance.total()}
	for _, row := range rows {
		switch row.Status {
		case esportswithdrawal.StatusPendingReview, esportswithdrawal.StatusApproved:
			view.PendingReviewCents += row.AmountCents
		case esportswithdrawal.StatusPaid:
			view.TotalWithdrawnCents += row.AmountCents
		}
	}
	return &view, nil
}

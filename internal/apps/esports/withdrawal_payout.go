package esports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportswithdrawal"
	"go-admin/internal/ent/esportswithdrawalpayout"
)

// 打款执行状态机（补全）：提现审核通过后的落账拆成明确阶段
// approved → pay_requested → paid / pay_failed，打款失败可退回重试。
// 提现单自身状态保持 approved，直到打款成功才置为 paid；猫粮在申请时已出账，
// 打款阶段不再动钱包，只落外部打款凭证与到账留痕。

// withdrawalPayoutNo 生成打款执行单号，前缀 WP，仿提现单号生成器。
func withdrawalPayoutNo() string { return "WP" + uuid.NewString() }

// requestWithdrawalPayoutInput 财务提交打款执行入参。
type requestWithdrawalPayoutInput struct {
	Channel       string `json:"channel"`
	TransactionNo string `json:"transaction_no"`
	AmountCents   int64  `json:"amount_cents"`
}

// failWithdrawalPayoutInput 标记打款失败入参。
type failWithdrawalPayoutInput struct {
	Reason string `json:"reason"`
}

// requestWithdrawalPayout 财务提交打款执行：仅「已审核通过」的提现单可提交，金额须等于净到账额。
// 写入打款渠道、外部流水号与金额，落为 pay_requested；同一流水号重复提交靠唯一索引兜底（幂等）。
func requestWithdrawalPayout(ctx context.Context, db *ent.Client, adminID, withdrawalID int,
	in requestWithdrawalPayoutInput, now time.Time) (*ent.EsportsWithdrawalPayout, error) {
	in.TransactionNo = strings.TrimSpace(in.TransactionNo)
	if adminID < 1 || withdrawalID < 1 || in.TransactionNo == "" ||
		len(in.TransactionNo) > 128 || in.AmountCents < 1 {
		return nil, errInvalidInput
	}
	switch in.Channel {
	case "bank_card", "wechat", "alipay":
	default:
		return nil, errInvalidInput
	}
	withdrawal, err := db.EsportsWithdrawal.Get(ctx, withdrawalID)
	if err != nil {
		return nil, err
	}
	if withdrawal.Status != esportswithdrawal.StatusApproved {
		return nil, errConflict
	}
	// 资金职责分离：审核人与打款提交人不得为同一人。
	if withdrawal.ReviewedByAdminID != nil && *withdrawal.ReviewedByAdminID == adminID {
		return nil, fmt.Errorf("%w：审核人不得提交打款", errForbidden)
	}
	if in.AmountCents != withdrawal.NetCents {
		return nil, fmt.Errorf("%w：打款金额须等于实际到账额 %d 分", errInvalidInput, withdrawal.NetCents)
	}
	// 同一提现单不允许存在多条进行中的打款执行，避免并发重复落账。
	active, err := db.EsportsWithdrawalPayout.Query().Where(
		esportswithdrawalpayout.WithdrawalIDEQ(withdrawalID),
		esportswithdrawalpayout.StatusEQ(esportswithdrawalpayout.StatusPayRequested),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if active {
		return nil, errConflict
	}
	payout, err := db.EsportsWithdrawalPayout.Create().SetPayoutNo(withdrawalPayoutNo()).
		SetWithdrawalID(withdrawalID).
		SetChannel(esportswithdrawalpayout.Channel(in.Channel)).
		SetTransactionNo(in.TransactionNo).
		SetAmountCents(in.AmountCents).
		SetStatus(esportswithdrawalpayout.StatusPayRequested).
		SetSubmittedByAdminID(adminID).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	return db.EsportsWithdrawalPayout.Get(ctx, payout.ID)
}

// markWithdrawalPayoutPaid 标记打款成功：pay_requested → paid，同一事务把提现单 approved → paid，
// 回填完税凭证、到账时间与打款人。猫粮在申请时已出账，此处不再动钱包。
// 幂等：提现单状态守卫 + 流水号唯一索引兜底，同一流水号不重复落账。
func markWithdrawalPayoutPaid(ctx context.Context, db *ent.Client, adminID, payoutID int,
	taxRef string, now time.Time) (*ent.EsportsWithdrawalPayout, error) {
	taxRef = strings.TrimSpace(taxRef)
	if adminID < 1 || payoutID < 1 || len(taxRef) > 128 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	payout, err := tx.EsportsWithdrawalPayout.Get(ctx, payoutID)
	if err != nil {
		return nil, err
	}
	if payout.Status != esportswithdrawalpayout.StatusPayRequested {
		return nil, errConflict
	}
	// 提交打款的人不得自行确认到账，须由另一名财务确认。
	if payout.SubmittedByAdminID != nil && *payout.SubmittedByAdminID == adminID {
		return nil, fmt.Errorf("%w：打款提交人不得自行确认到账", errForbidden)
	}
	changed, err := tx.EsportsWithdrawalPayout.Update().Where(
		esportswithdrawalpayout.IDEQ(payoutID),
		esportswithdrawalpayout.StatusEQ(esportswithdrawalpayout.StatusPayRequested),
		esportswithdrawalpayout.VersionEQ(payout.Version),
	).SetStatus(esportswithdrawalpayout.StatusPaid).SetTaxRef(taxRef).
		SetPaidByAdminID(adminID).SetPaidAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	changed, err = tx.EsportsWithdrawal.Update().Where(
		esportswithdrawal.IDEQ(payout.WithdrawalID),
		esportswithdrawal.StatusEQ(esportswithdrawal.StatusApproved),
	).SetStatus(esportswithdrawal.StatusPaid).
		SetPayoutRef(payout.TransactionNo).SetTaxRef(taxRef).
		SetPaidByAdminID(adminID).SetPaidAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsWithdrawalPayout.Get(ctx, payoutID)
}

// markWithdrawalPayoutFailed 标记打款失败：pay_requested → pay_failed，须填失败原因。
// 提现单仍保持 approved，可重新提交打款执行（重试）。
func markWithdrawalPayoutFailed(ctx context.Context, db *ent.Client, adminID, payoutID int,
	in failWithdrawalPayoutInput) (*ent.EsportsWithdrawalPayout, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	if adminID < 1 || payoutID < 1 || in.Reason == "" || len([]rune(in.Reason)) > 255 {
		return nil, fmt.Errorf("%w：标记打款失败须填写原因", errInvalidInput)
	}
	changed, err := db.EsportsWithdrawalPayout.Update().Where(
		esportswithdrawalpayout.IDEQ(payoutID),
		esportswithdrawalpayout.StatusEQ(esportswithdrawalpayout.StatusPayRequested),
	).SetStatus(esportswithdrawalpayout.StatusPayFailed).SetFailReason(in.Reason).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsWithdrawalPayout.Get(ctx, payoutID)
}

// listWithdrawalPayouts 列出某提现单的打款执行记录，按 ID 倒序。
func listWithdrawalPayouts(ctx context.Context, db *ent.Client, withdrawalID, limit int) ([]*ent.EsportsWithdrawalPayout, error) {
	if withdrawalID < 1 {
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return db.EsportsWithdrawalPayout.Query().Where(
		esportswithdrawalpayout.WithdrawalIDEQ(withdrawalID),
	).Order(ent.Desc(esportswithdrawalpayout.FieldID)).Limit(limit).All(ctx)
}

package esports

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/esportswallettx"
)

// 本文件覆盖 P1「退卡审核是一键出账」的后端整改：
// 出账（审核通过）必须登记打款凭证 / 银行流水号、必须由财务类角色执行，且审核人 ≠ 申请人。

// seedRefundPendingCard 造一张已核验开卡、并已提交退卡申请的卡（待财务审核）。
func seedRefundPendingCard(t *testing.T, db *ent.Client, applicantID int, receiptRef, key string) *storedValueCardView {
	t.Helper()
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)
	card, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "wechat_work",
		PaymentRef: receiptRef, PrincipalCents: 10000, BonusCents: 2000,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: key,
	}, now)
	if err != nil {
		t.Fatalf("开卡失败：%v", err)
	}
	pending, err := requestStoredValueRefund(ctx, db, applicantID, card.ID, now)
	if err != nil {
		t.Fatalf("申请退卡失败：%v", err)
	}
	if pending.Status != esportsstoredvaluecard.StatusRefundPending {
		t.Fatalf("申请后应为待审核：%+v", pending)
	}
	return card
}

// seedFinanceRoleAdmin 造一个具财务角色的管理员（复用既有 app.esports.finance_report_role_names 配置），
// 一并返回角色 ID，便于测试里再给第二名管理员授同一角色。
func seedFinanceRoleAdmin(t *testing.T, db *ent.Client, uid, roleName string) (*ent.Admin, int) {
	t.Helper()
	ctx := t.Context()
	poSeedConfig(t, db, financeReportRoleNamesKey, roleName)
	role := db.Role.Create().SetName(roleName).SetDisplayName("财务出账员").SetStatus("active").SaveX(ctx)
	operator := seedAdminAccount(t, db, uid)
	db.AdminRole.Create().SetAdminID(operator.ID).SetRoleID(role.ID).SaveX(ctx)
	return operator, role.ID
}

// 出账缺打款凭证 / 流水号（或格式不合理）一律拒绝，且卡状态保持待审核。
func TestStoredValueRefundApprovalRequiresPayoutRef(t *testing.T) {
	db := openStoredValueDB(t, "esports_sv_refund_audit_payout")
	ctx := t.Context()
	now := time.Now()
	card := seedRefundPendingCard(t, db, 11, "WX-SV-AUDIT-1", "sv-audit-open-1")

	for _, item := range []struct {
		name string
		ref  string
		want string
	}{
		{name: "空凭证", ref: "   ", want: "打款凭证"},
		{name: "过短", ref: "AB12", want: "打款凭证"},
		{name: "含空格与符号", ref: "银行 回单#123456", want: "只能包含"},
	} {
		t.Run(item.name, func(t *testing.T) {
			_, err := decideStoredValueRefund(ctx, db, 22, card.ID, true, now,
				storedValueRefundPayout{PayoutRef: item.ref})
			if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), item.want) {
				t.Fatalf("缺合法打款凭证应判参数错误（含 %q），err=%v", item.want, err)
			}
		})
	}
	reload := db.EsportsStoredValueCard.GetX(ctx, card.ID)
	if reload.Status != esportsstoredvaluecard.StatusRefundPending || reload.RefundedCents != 0 {
		t.Fatalf("被拒的出账不得改卡状态：%+v", reload)
	}
	if count := db.EsportsWalletTx.Query().
		Where(esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn)).CountX(ctx); count != 0 {
		t.Fatalf("被拒的出账不得写退款流水：%d", count)
	}
}

// 非财务角色出账被拒；财务角色（含已停用角色不算）才放行。
func TestStoredValueRefundApprovalRequiresFinanceRole(t *testing.T) {
	db := openStoredValueDB(t, "esports_sv_refund_audit_role")
	ctx := t.Context()
	now := time.Now()
	card := seedRefundPendingCard(t, db, 11, "WX-SV-AUDIT-2", "sv-audit-open-2")
	payout := storedValueRefundPayout{PayoutRef: "BANK-2026-000123", PayoutNote: "对公转账回单"}

	// 未配置财务角色：仅超级管理员可出账，普通账号一律拒绝。
	plain := seedAdminAccount(t, db, "sv-audit-plain")
	if _, err := decideStoredValueRefund(ctx, db, plain.ID, card.ID, true, now, payout); !errors.Is(err, errForbidden) ||
		!strings.Contains(err.Error(), "财务角色") {
		t.Fatalf("非财务角色出账应被拒（errForbidden），err=%v", err)
	}

	// 角色已停用同样不算财务角色。
	poSeedConfig(t, db, financeReportRoleNamesKey, "sv-audit-inactive")
	inactive := db.Role.Create().SetName("sv-audit-inactive").SetDisplayName("已停用财务").
		SetStatus("inactive").SaveX(ctx)
	inactiveAdmin := seedAdminAccount(t, db, "sv-audit-inactive-user")
	db.AdminRole.Create().SetAdminID(inactiveAdmin.ID).SetRoleID(inactive.ID).SaveX(ctx)
	if _, err := decideStoredValueRefund(ctx, db, inactiveAdmin.ID, card.ID, true, now, payout); !errors.Is(err, errForbidden) {
		t.Fatalf("停用角色不得出账，err=%v", err)
	}

	// 驳回不是出账动作，不受财务角色门槛限制（仍需审核人 ≠ 申请人）。
	rejectCard := seedRefundPendingCard(t, db, 11, "WX-SV-AUDIT-3", "sv-audit-open-3")
	rejected, err := decideStoredValueRefund(ctx, db, plain.ID, rejectCard.ID, false, now)
	if err != nil || rejected.Status != esportsstoredvaluecard.StatusActive {
		t.Fatalf("驳回退卡应正常执行：%+v err=%v", rejected, err)
	}

	// 具财务角色的管理员可出账。
	finance, _ := seedFinanceRoleAdmin(t, db, "sv-audit-finance", "sv-audit-finance-role")
	refunded, err := decideStoredValueRefund(ctx, db, finance.ID, card.ID, true, now, payout)
	if err != nil {
		t.Fatalf("财务角色出账应成功：%v", err)
	}
	if refunded.Status != esportsstoredvaluecard.StatusRefunded || refunded.RefundedCents != 10000 ||
		refunded.BalanceCents != 0 {
		t.Fatalf("出账结果不正确：%+v", refunded)
	}
}

// 审核人 = 申请人仍然被拒（既有硬约束），且提示明确。
func TestStoredValueRefundApprovalRejectsSelfReview(t *testing.T) {
	db := openStoredValueDB(t, "esports_sv_refund_audit_self")
	ctx := t.Context()
	now := time.Now()
	finance, financeRoleID := seedFinanceRoleAdmin(t, db, "sv-audit-self", "sv-audit-self-role")
	// 申请人本身就是财务角色：仍不得自审自出账。
	card := seedRefundPendingCard(t, db, finance.ID, "WX-SV-AUDIT-4", "sv-audit-open-4")
	if _, err := decideStoredValueRefund(ctx, db, finance.ID, card.ID, true, now,
		storedValueRefundPayout{PayoutRef: "BANK-2026-000456"}); !errors.Is(err, errInvalidInput) ||
		!strings.Contains(err.Error(), "不得由申请人本人执行") {
		t.Fatalf("申请人自审应被拒，err=%v", err)
	}
	// 换一名财务出账则放行。
	other := seedAdminAccount(t, db, "sv-audit-other")
	db.AdminRole.Create().SetAdminID(other.ID).SetRoleID(financeRoleID).SaveX(ctx)
	if _, err := decideStoredValueRefund(ctx, db, other.ID, card.ID, true, now,
		storedValueRefundPayout{PayoutRef: "BANK-2026-000456"}); err != nil {
		t.Fatalf("换人出账应成功：%v", err)
	}
}

// 出账时把打款凭证 / 流水号快照进退款流水（只增不改），事后可按凭据对账。
func TestStoredValueRefundApprovalRecordsPayoutEvidence(t *testing.T) {
	db := openStoredValueDB(t, "esports_sv_refund_audit_evidence")
	ctx := t.Context()
	now := time.Now()
	finance, _ := seedFinanceRoleAdmin(t, db, "sv-audit-evidence", "sv-audit-evidence-role")
	card := seedRefundPendingCard(t, db, 11, "WX-SV-AUDIT-5", "sv-audit-open-5")

	const payoutRef = "BANK-2026-000789"
	if _, err := decideStoredValueRefund(ctx, db, finance.ID, card.ID, true, now,
		storedValueRefundPayout{PayoutRef: payoutRef, PayoutNote: "网银转账"}); err != nil {
		t.Fatalf("出账应成功：%v", err)
	}
	row := db.EsportsWalletTx.Query().
		Where(esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn)).OnlyX(ctx)
	if !strings.Contains(row.Note, payoutRef) || !strings.Contains(row.Note, "网银转账") {
		t.Fatalf("退款流水备注应带打款凭证与备注：%s", row.Note)
	}
	if got := fmt.Sprint(row.RuleSnapshot["payout_ref"]); got != payoutRef {
		t.Fatalf("退款流水快照应记录打款凭证，got=%q", got)
	}
	if got := fmt.Sprint(row.RuleSnapshot["refunded_cents"]); got != "10000" {
		t.Fatalf("退款流水快照应记录退款金额，got=%q", got)
	}
	if got := fmt.Sprint(row.RuleSnapshot["decided_by_admin_id"]); got != fmt.Sprint(finance.ID) {
		t.Fatalf("退款流水快照应记录出账人，got=%q", got)
	}
}

package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportswithdrawal"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/systemconfig"
)

func openWithdrawalDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// seedCatfoodPlayer 建一个猫粮余额为 cents 的陪玩。
func seedCatfoodPlayer(t *testing.T, db *ent.Client, userID int, cents int64) *ent.EsportsPlayer {
	t.Helper()
	ctx := t.Context()
	player := seedPlayer(t, db, userID, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	if _, err := walletRecharge(ctx, db, 1, walletPlayerCatfood, player.ID,
		walletInput{PrincipalCents: cents, Note: "结算入账"}, "wd-seed-"+strconv.Itoa(userID)+"-"+time.Now().Format("150405.000000")); err != nil {
		t.Fatalf("猫粮入账失败：%v", err)
	}
	return player
}

// setWithdrawalConfig 写入（或覆盖）一条提现配置。
func setWithdrawalConfig(t *testing.T, db *ent.Client, key, value string) {
	t.Helper()
	ctx := t.Context()
	if changed, err := db.SystemConfig.Update().Where(systemconfig.KeyEQ(key)).
		SetValue(value).Save(ctx); err != nil {
		t.Fatal(err)
	} else if changed > 0 {
		return
	}
	db.SystemConfig.Create().SetKey(key).SetName(key).SetValue(value).
		SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
}

func validApply(key string, amount int64) withdrawalApplyInput {
	return withdrawalApplyInput{
		AmountCents: amount, Channel: "bank_card",
		AccountRef: "6222 **** 1234", IdempotencyKey: key,
	}
}

// 申请提现：申请即扣减猫粮、幂等重放不重复扣减，余额不足 / 参数非法被拒。
func TestWithdrawalApplyFreezesCatfood(t *testing.T) {
	db := openWithdrawalDB(t, "esports_withdrawal_apply")
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9001, 10000)
	now := time.Now()

	item, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-key-0001", 3000), now)
	if err != nil {
		t.Fatalf("申请提现应成功，err=%v", err)
	}
	if item.Status != esportswithdrawal.StatusPendingReview || item.AmountCents != 3000 ||
		item.FeeCents != 0 || item.TaxCents != 0 || item.NetCents != 3000 {
		t.Fatalf("提现单初始状态不正确：%+v", item)
	}
	balance, err := walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.total() != 7000 {
		t.Fatalf("申请后猫粮应扣减到 7000，got=%d", balance.total())
	}
	tx, err := db.EsportsWalletTx.Query().Where().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range tx {
		if row.SourceType == "withdrawal" && row.SourceID == item.WithdrawalNo {
			found = true
			if row.PrincipalDeltaCents != -3000 {
				t.Fatalf("提现流水应为 -3000，got=%d", row.PrincipalDeltaCents)
			}
		}
	}
	if !found {
		t.Fatal("应写入一条提现流水")
	}

	// 幂等：同一幂等键重复提交返回既有单，不重复扣减。
	replay, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-key-0001", 3000), now)
	if err != nil {
		t.Fatalf("幂等重放不应报错，err=%v", err)
	}
	if replay.ID != item.ID {
		t.Fatalf("幂等重放应返回同一张提现单，got=%d want=%d", replay.ID, item.ID)
	}
	if balance, err = walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID); err != nil {
		t.Fatal(err)
	}
	if balance.total() != 7000 {
		t.Fatalf("幂等重放不应重复扣减，got=%d", balance.total())
	}

	// 余额不足。
	if _, err = applyWithdrawal(ctx, db, player.ID, validApply("wd-key-0002", 8000), now); !errors.Is(err, errInsufficientBalance) {
		t.Fatalf("余额不足应被拒，err=%v", err)
	}
	// 参数非法：渠道、收款账号、幂等键。
	badChannel := validApply("wd-key-0003", 100)
	badChannel.Channel = "cash"
	if _, err = applyWithdrawal(ctx, db, player.ID, badChannel, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法渠道应被拒，err=%v", err)
	}
	badRef := validApply("wd-key-0004", 100)
	badRef.AccountRef = "  "
	if _, err = applyWithdrawal(ctx, db, player.ID, badRef, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("缺收款账号应被拒，err=%v", err)
	}
	if _, err = applyWithdrawal(ctx, db, player.ID, validApply("short", 100), now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("幂等键过短应被拒，err=%v", err)
	}
}

// 提现门槛、频率与手续费税费试算。
func TestWithdrawalPolicyGateAndFee(t *testing.T) {
	db := openWithdrawalDB(t, "esports_withdrawal_policy")
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9002, 100000)
	now := time.Now()

	setWithdrawalConfig(t, db, withdrawalMinCentsKey, "5000")
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-policy-0001", 1000), now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("低于最低提现金额应被拒，err=%v", err)
	}

	// 手续费 1%、税费 2%：申请 10000 → 手续费 100、税费 200、实际到账 9700。
	setWithdrawalConfig(t, db, withdrawalFeeBpKey, "100")
	setWithdrawalConfig(t, db, withdrawalTaxBpKey, "200")
	item, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-policy-0002", 10000), now)
	if err != nil {
		t.Fatalf("申请应成功，err=%v", err)
	}
	if item.FeeCents != 100 || item.TaxCents != 200 || item.NetCents != 9700 {
		t.Fatalf("手续费 / 税费 / 到账试算不正确：%+v", item)
	}

	// 频率：24 小时内不得再次申请。
	setWithdrawalConfig(t, db, withdrawalMinIntervalKey, "24")
	if _, err = applyWithdrawal(ctx, db, player.ID, validApply("wd-policy-0003", 10000), now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("频率限制内再次申请应被拒，err=%v", err)
	}
	// 间隔满足后可再次申请。
	if _, err = applyWithdrawal(ctx, db, player.ID, validApply("wd-policy-0004", 10000),
		now.Add(25*time.Hour)); err != nil {
		t.Fatalf("间隔满足后应可申请，err=%v", err)
	}
	// 费率 100% 时实际到账为 0，应被拒。
	setWithdrawalConfig(t, db, withdrawalFeeBpKey, "10000")
	if _, err = applyWithdrawal(ctx, db, player.ID, validApply("wd-policy-0005", 10000),
		now.Add(50*time.Hour)); !errors.Is(err, errInvalidInput) {
		t.Fatalf("到账为零应被拒，err=%v", err)
	}
}

// 审核与打款：通过 → 打款留凭证与到账时间；驳回退回猫粮并留原因；重复操作冲突。
func TestWithdrawalReviewAndPayout(t *testing.T) {
	db := openWithdrawalDB(t, "esports_withdrawal_review")
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9003, 10000)
	now := time.Now()

	item, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-review-0001", 4000), now)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := reviewWithdrawal(ctx, db, 7, item.ID, withdrawalReviewInput{Approve: true}, now)
	if err != nil {
		t.Fatalf("审核通过应成功，err=%v", err)
	}
	if approved.Status != esportswithdrawal.StatusApproved ||
		approved.ReviewedByAdminID == nil || *approved.ReviewedByAdminID != 7 || approved.ReviewedAt == nil {
		t.Fatalf("审核留痕不正确：%+v", approved)
	}
	// 未打款不得填写空凭证。
	if _, err = payWithdrawal(ctx, db, 8, item.ID, "  ", "", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("空打款凭证应被拒，err=%v", err)
	}
	// 审核人不得自打款。
	if _, err = payWithdrawal(ctx, db, 7, item.ID, "BANK-SELF", "", now); !errors.Is(err, errForbidden) {
		t.Fatalf("审核人自打款应被拒，err=%v", err)
	}
	paid, err := payWithdrawal(ctx, db, 8, item.ID, "BANK-TX-2026-01", "TAX-2026-01", now)
	if err != nil {
		t.Fatalf("登记打款应成功，err=%v", err)
	}
	if paid.Status != esportswithdrawal.StatusPaid || paid.PayoutRef != "BANK-TX-2026-01" ||
		paid.TaxRef != "TAX-2026-01" || paid.PaidByAdminID == nil || *paid.PaidByAdminID != 8 ||
		paid.PaidAt == nil {
		t.Fatalf("打款留痕不正确：%+v", paid)
	}
	// 重复审核 / 重复打款冲突。
	if _, err = reviewWithdrawal(ctx, db, 7, item.ID, withdrawalReviewInput{Approve: true}, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复审核应冲突，err=%v", err)
	}
	if _, err = payWithdrawal(ctx, db, 8, item.ID, "BANK-TX-2", "", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复打款应冲突，err=%v", err)
	}
	// 打款不动钱包：申请时已扣减。
	balance, err := walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.total() != 6000 {
		t.Fatalf("打款后猫粮应保持 6000，got=%d", balance.total())
	}
}

// 驳回：须填原因，退回猫粮原额，且不重复退回。
func TestWithdrawalRejectRestoresCatfood(t *testing.T) {
	db := openWithdrawalDB(t, "esports_withdrawal_reject")
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9004, 10000)
	now := time.Now()

	item, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-reject-0001", 4000), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reviewWithdrawal(ctx, db, 7, item.ID, withdrawalReviewInput{Approve: false}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("驳回未填原因应被拒，err=%v", err)
	}
	rejected, err := reviewWithdrawal(ctx, db, 7, item.ID,
		withdrawalReviewInput{Approve: false, Reason: "收款账号与实名不一致"}, now)
	if err != nil {
		t.Fatalf("驳回应成功，err=%v", err)
	}
	if rejected.Status != esportswithdrawal.StatusRejected || rejected.RejectReason == "" {
		t.Fatalf("驳回留痕不正确：%+v", rejected)
	}
	balance, err := walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.total() != 10000 {
		t.Fatalf("驳回应退回猫粮原额 10000，got=%d", balance.total())
	}
	// 重复驳回被拒，退回只发生一次。
	if _, err = reviewWithdrawal(ctx, db, 7, item.ID,
		withdrawalReviewInput{Approve: false, Reason: "再驳一次"}, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复驳回应冲突，err=%v", err)
	}
	if balance, err = walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID); err != nil {
		t.Fatal(err)
	}
	if balance.total() != 10000 {
		t.Fatalf("退回不应重复发生，got=%d", balance.total())
	}
}

// 提现概览与陪玩工作台的可提现余额。
func TestWithdrawalSummaryAndDashboard(t *testing.T) {
	db := openWithdrawalDB(t, "esports_withdrawal_summary")
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9005, 10000)
	now := time.Now()

	first, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-sum-0001", 3000), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reviewWithdrawal(ctx, db, 7, first.ID, withdrawalReviewInput{Approve: true}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = payWithdrawal(ctx, db, 8, first.ID, "PAY-SUM-1", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err = applyWithdrawal(ctx, db, player.ID, validApply("wd-sum-0002", 2000), now); err != nil {
		t.Fatal(err)
	}

	view, err := playerWithdrawalSummary(ctx, db, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.WithdrawableCents != 5000 || view.PendingReviewCents != 2000 || view.TotalWithdrawnCents != 3000 {
		t.Fatalf("提现概览不正确：%+v", view)
	}
	dashboard, err := playerDashboard(ctx, db, player.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.CatfoodBalanceCents != 5000 {
		t.Fatalf("工作台可提现余额应为 5000，got=%d", dashboard.CatfoodBalanceCents)
	}
	// 列表过滤：只看待审。
	pending, err := listWithdrawals(ctx, db, player.ID, "pending_review", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID == first.ID {
		t.Fatalf("待审列表不正确：%+v", pending)
	}
}

func TestWithdrawalIdempotencyRejectsChangedBodyOrOtherPlayer(t *testing.T) {
	db := openWithdrawalDB(t, "esports_withdrawal_idem_scope")
	ctx := t.Context()
	p1 := seedCatfoodPlayer(t, db, 9101, 100000)
	p2 := seedCatfoodPlayer(t, db, 9102, 100000)
	now := time.Now()

	first, err := applyWithdrawal(ctx, db, p1.ID, validApply("wd-key-shared", 3000), now)
	if err != nil {
		t.Fatalf("首次申请失败: %v", err)
	}
	again, err := applyWithdrawal(ctx, db, p1.ID, validApply("wd-key-shared", 3000), now)
	if err != nil {
		t.Fatalf("同人同请求幂等重放应返回既有单: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("幂等重放返回不同单据: %d vs %d", again.ID, first.ID)
	}
	if _, err := applyWithdrawal(ctx, db, p1.ID, validApply("wd-key-shared", 4000), now); err == nil {
		t.Fatal("同人改金额不应被当作幂等成功")
	}
	if _, err := applyWithdrawal(ctx, db, p2.ID, validApply("wd-key-shared", 3000), now); err == nil {
		t.Fatal("不同陪玩复用同一幂等键不应返回他人单据")
	}
}

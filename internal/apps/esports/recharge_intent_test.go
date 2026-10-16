package esports

import (
	"testing"
	"time"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportswallettx"
)

// 发起充值意图：命中活动、赠送与有效期都锁定进快照，支付意图独立于订单。
func TestRechargeIntentCreatesSnapshot(t *testing.T) {
	db := openCampaignDB(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)

	in := baseCampaignInput("RINT1", now)
	in.Tiers = []schema.CampaignTier{{ThresholdCents: 5000, BonusCents: 500}, {ThresholdCents: 10000, BonusCents: 1200}}
	in.BonusValidDays = 30
	createActiveCampaign(t, db, in)

	view, err := createRechargeIntent(ctx, db, customer, rechargeIntentInput{
		PrincipalCents: 10000, PaymentChannel: "wechat_work", IdempotencyKey: "recharge-key-001",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if view.PrincipalCents != 10000 || view.BonusCents != 1200 {
		t.Fatalf("本金/赠送快照不正确：%+v", view)
	}
	if view.RuleSnapshot == nil {
		t.Fatal("活动快照为空")
	}
	if got := snapshotTotalBonus(t, view.RuleSnapshot); got != 1200 {
		t.Fatalf("快照赠送额不正确：%d", got)
	}
	if names := snapshotCampaignCodes(t, view.RuleSnapshot); len(names) != 1 || names[0] != "RINT1" {
		t.Fatalf("快照活动信息不正确：%v", names)
	}
	if view.SnapshotAt.Unix() != now.Unix() {
		t.Fatalf("活动资格判定时刻不正确：%v", view.SnapshotAt)
	}

	intent, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(view.IntentNo)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Status != esportspaymentintent.StatusPending || intent.AmountCents != 10000 {
		t.Fatalf("支付意图不正确：%+v", intent)
	}
	if rechargeIntentCustomerID(intent.OrderID) != customer.ID {
		t.Fatalf("order_id 保留段未正确编码客户 ID：%d", intent.OrderID)
	}

	// 幂等：重复提交同一键返回同一意图，不重复建单。
	again, err := createRechargeIntent(ctx, db, customer, rechargeIntentInput{
		PrincipalCents: 10000, PaymentChannel: "wechat_work", IdempotencyKey: "recharge-key-001",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if again.IntentNo != view.IntentNo {
		t.Fatalf("幂等应返回同一意图：%s vs %s", again.IntentNo, view.IntentNo)
	}
	if total := db.EsportsRechargeIntent.Query().CountX(ctx); total != 1 {
		t.Fatalf("重复提交后意图应仍为 1 条：%d", total)
	}
}

// 核验按快照入账：核验前修改活动不影响入账赠送，仍按发起时锁定的快照。
func TestRechargeIntentCreditUsesSnapshotNotLiveCampaign(t *testing.T) {
	db := openCampaignDB(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)

	campaign := createActiveCampaign(t, db, baseCampaignInput("RINT2", now))
	view, err := createRechargeIntent(ctx, db, customer, rechargeIntentInput{
		PrincipalCents: 5000, PaymentChannel: "merchant_qr", IdempotencyKey: "recharge-key-002",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if view.BonusCents != 500 {
		t.Fatalf("应命中活动赠送 500：%+v", view)
	}

	// 核验前把活动改为赠送 999：核验入账必须仍按快照赠送 500，不随活动漂移。
	updated := baseCampaignInput("RINT2", now)
	updated.Name = "已改活动"
	updated.Tiers = []schema.CampaignTier{{ThresholdCents: 5000, BonusCents: 999}}
	if _, err := updateCampaign(ctx, db, campaign.ID, campaign.Version, updated); err != nil {
		t.Fatal(err)
	}

	verified, err := verifyRechargeIntentPayment(ctx, db, 11, view.IntentNo, true, 5000,
		"财务核验充值到账凭证", now)
	if err != nil {
		t.Fatal(err)
	}
	if verified.IntentStatus != "paid" || verified.BonusCents != 500 {
		t.Fatalf("应按快照入账赠送 500：%+v", verified)
	}
	txRow, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.OwnerIDEQ(customer.ID),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRecharge),
	).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if txRow.PrincipalDeltaCents != 5000 || txRow.BonusDeltaCents != 500 {
		t.Fatalf("钱包流水本金/赠送分列不正确：%+v", txRow)
	}
	if txRow.RuleSnapshot == nil {
		t.Fatal("钱包流水缺少活动快照")
	}
	if got := snapshotTotalBonus(t, txRow.RuleSnapshot); got != 500 {
		t.Fatalf("钱包流水快照赠送额应为 500：%d", got)
	}
	if txRow.BonusExpiresAt != nil {
		// BonusValidDays=0 为永久赠送，不应有到期时间。
		t.Fatalf("永久赠送不应有到期时间：%v", txRow.BonusExpiresAt)
	}
}

// 过期任务：未核验且超时的意图转 expired，重复运行幂等。
func TestRechargeIntentExpireIdempotent(t *testing.T) {
	db := openCampaignDB(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)
	view, err := createRechargeIntent(ctx, db, customer, rechargeIntentInput{
		PrincipalCents: 5000, PaymentChannel: "corporate_transfer", IdempotencyKey: "recharge-key-003",
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	expireAt := now.Add(48 * time.Hour)
	count, err := expireRechargeIntents(ctx, db, expireAt, 200)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("应过期 1 条意图：%d", count)
	}
	intent, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(view.IntentNo)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Status != esportspaymentintent.StatusExpired {
		t.Fatalf("意图应转 expired：%v", intent.Status)
	}
	// 重复运行不再变化。
	again, err := expireRechargeIntents(ctx, db, expireAt, 200)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("重复运行应过期 0 条：%d", again)
	}
}

// 渠道核销：同一渠道流水重复回调不重复入账。
func TestRechargeIntentChannelSettlementDedup(t *testing.T) {
	db := openCampaignDB(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)
	createActiveCampaign(t, db, baseCampaignInput("RINT4", now))

	view, err := createRechargeIntent(ctx, db, customer, rechargeIntentInput{
		PrincipalCents: 5000, PaymentChannel: "wechat_work", IdempotencyKey: "recharge-key-004",
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	settle := func() *rechargeIntentView {
		v, e := settleRechargeIntent(ctx, db, rechargeSettlementInput{
			IntentNo: view.IntentNo, Channel: "wechat_work", TransactionNo: "CH-TX-004",
			ReceivedCents: 5000, ConfirmedAt: now, EvidenceNote: "已验签通道到账凭证",
			OperatorAdminID: 11,
		}, now)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	first := settle()
	if first.IntentStatus != "paid" {
		t.Fatalf("首次核销应入账：%+v", first)
	}
	second := settle()
	if second.IntentStatus != "paid" {
		t.Fatalf("重复回调应返回已入账视图：%+v", second)
	}
	if count := db.EsportsWalletTx.Query().Where(
		esportswallettx.OwnerIDEQ(customer.ID),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRecharge),
	).CountX(ctx); count != 1 {
		t.Fatalf("重复渠道流水应只入账一次：%d", count)
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.PrincipalCents != 5000 || balance.BonusCents != 500 {
		t.Fatalf("余额本金/赠送不正确：%+v", balance)
	}
}

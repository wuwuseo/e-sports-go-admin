package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportspaymentaccount"
	"go-admin/internal/ent/migrate"
)

// seedPayableOrderForOffline 建一张待付款订单 + 已验证报价 + 指定渠道的收款意图。
func seedPayableOrderForOffline(t *testing.T, db *ent.Client, suffix, channel string) *ent.EsportsPaymentIntent {
	t.Helper()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("60").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetAgeStatus("adult").ExecX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "OFFLINE-" + suffix, Title: "陪玩一小时", ServiceType: "play", UnitPriceCents: 5000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: 2, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 时间基准取当前时刻：支付时限由订单创建时间往后计算，核销须落在时限内。
	now := time.Now().UTC()
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"offline-order-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	confirmation := db.EsportsConfirmation.Create().SetCustomerID(customer.ID).
		SetSubjectType("order_quote").SetSubjectID(order.ID).SetSubjectVersion(order.QuoteVersion).
		SetSkuID(sku.ID).SetQuantity(order.Quantity).SetAmountCents(order.PayableCents).
		SetChannel("verified_customer_message").SetEvidenceRef("offline-evidence-" + suffix).
		SetStatus("verified").SetVerifiedAt(now).SetExpiresAt(order.PaymentDeadline).SaveX(ctx)
	return db.EsportsPaymentIntent.Create().SetIntentNo("offline-intent-" + suffix).
		SetOrderID(order.ID).SetConfirmationID(confirmation.ID).
		SetQuoteVersion(order.QuoteVersion).SetAmountCents(order.PayableCents).
		SetChannel(channel).SetExpiresAt(order.PaymentDeadline).SaveX(ctx)
}

// seedActivePaymentAccount 建一个已审核启用的收款账户。
func seedActivePaymentAccount(t *testing.T, db *ent.Client, channel, accountNo string) *ent.EsportsPaymentAccount {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	item, err := createPaymentAccount(ctx, db, paymentAccountInput{
		Channel: channel, SubjectName: "示例工作室有限公司", AccountNo: accountNo,
		QrURL: "https://example.com/qr.png", BankName: "示例银行",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	// 提交人 11 不得自审，由 12 审核启用。
	item, err = reviewPaymentAccount(ctx, db, item.ID, 12, true, "主体与账户一致", now)
	if err != nil || item.Status != esportspaymentaccount.StatusActive {
		t.Fatalf("账户审核启用失败：%+v err=%v", item, err)
	}
	return item
}

// 收款账户审核与收款方式启停（§4.3）：提交人不得自审，停用渠道 / 停用账户后不可提交到账凭证。
func TestPaymentAccountReviewAndChannelSwitch(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_payment_account?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	// 对公转账必须填账号；企业微信收款须有收款码或商户号。
	if _, err := createPaymentAccount(ctx, db, paymentAccountInput{
		Channel: "corporate_transfer", SubjectName: "示例工作室有限公司",
	}, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("对公转账缺账号应被拒，err=%v", err)
	}
	account, err := createPaymentAccount(ctx, db, paymentAccountInput{
		Channel: "wechat_work", SubjectName: "示例工作室有限公司",
		QrURL: "https://example.com/wecom.png",
	}, 11, now)
	if err != nil || account.Status != esportspaymentaccount.StatusPendingReview {
		t.Fatalf("提交账户应进入待审核：%+v err=%v", account, err)
	}
	if _, err = reviewPaymentAccount(ctx, db, account.ID, 11, true, "自审", now); !errors.Is(err, errForbidden) {
		t.Fatalf("提交人不得自审收款账户，err=%v", err)
	}
	if _, err = reviewPaymentAccount(ctx, db, account.ID, 12, false, "", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("驳回必须给原因，err=%v", err)
	}
	account, err = reviewPaymentAccount(ctx, db, account.ID, 12, true, "主体与账户一致", now)
	if err != nil || account.Status != esportspaymentaccount.StatusActive {
		t.Fatalf("审核启用失败：%+v err=%v", account, err)
	}
	channels, err := listPaymentChannels(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range channels {
		if channel.Channel == "wechat_work" && (!channel.Enabled || channel.ActiveAccounts != 1) {
			t.Fatalf("企微收款应启用且统计到 1 个启用账户：%+v", channel)
		}
	}

	intent := seedPayableOrderForOffline(t, db, "switch", "wechat_work")
	proof, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: intent.OrderID, AccountID: account.ID, TransactionNo: "WX-SWITCH-0001",
		AmountCents: intent.AmountCents, ProofRef: "proof/wecom-0001.png",
	}, 11, now)
	if err != nil || proof.Status != "pending_verification" {
		t.Fatalf("提交凭证应生成待核验收款：%+v err=%v", proof, err)
	}
	// 停用收款方式后不再接受新的到账凭证。
	if err = setPaymentChannelEnabled(ctx, db, "wechat_work", false); err != nil {
		t.Fatal(err)
	}
	if _, err = submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: intent.OrderID, AccountID: account.ID, TransactionNo: "WX-SWITCH-0002",
		AmountCents: intent.AmountCents, ProofRef: "proof/wecom-0002.png",
	}, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("停用收款方式后提交凭证应被拒，err=%v", err)
	}
	if _, err = verifyOfflinePaymentProof(ctx, db, proof.ID, 12, now); !errors.Is(err, errForbidden) {
		t.Fatalf("停用收款方式后核销应被拒，err=%v", err)
	}
	if err = setPaymentChannelEnabled(ctx, db, "wechat_work", true); err != nil {
		t.Fatal(err)
	}
	// 停用账户后同样不可核销。
	if _, err = disablePaymentAccount(ctx, db, account.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = verifyOfflinePaymentProof(ctx, db, proof.ID, 12, now); !errors.Is(err, errForbidden) {
		t.Fatalf("停用账户后核销应被拒，err=%v", err)
	}
}

// 外部到账核销闭环（§4.2 / §4.3）：客服提交凭证 → 财务核销 → 订单转已付款并放行派单；
// 提交人不得自审、同一渠道流水不可核销两次、金额不足与账户未启用一律拒绝。
func TestOfflinePaymentProofVerificationAppliesOrder(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_offline_payment?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-1111")
	pending, err := createPaymentAccount(ctx, db, paymentAccountInput{
		Channel: "corporate_transfer", SubjectName: "另一主体有限公司", AccountNo: "6222-0000-0000-2222",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	intent := seedPayableOrderForOffline(t, db, "apply", "corporate_transfer")

	// 金额不足应付一律拒绝。
	if _, err = submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: intent.OrderID, AccountID: account.ID, TransactionNo: "CORP-LOW-0001",
		AmountCents: intent.AmountCents - 100, ProofRef: "proof/corp-low.png",
	}, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("金额不足应付应被拒，err=%v", err)
	}
	// 凭证引用过短（无有效凭证）拒绝。
	if _, err = submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: intent.OrderID, AccountID: account.ID, TransactionNo: "CORP-SHORT-0001",
		AmountCents: intent.AmountCents, ProofRef: "x",
	}, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("凭证引用过短应被拒，err=%v", err)
	}
	// 未启用（待审核）账户不可作为收款账户。
	if _, err = submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: intent.OrderID, AccountID: pending.ID, TransactionNo: "CORP-PENDING-0001",
		AmountCents: intent.AmountCents, ProofRef: "proof/corp-pending.png",
	}, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("未启用账户不可核销，err=%v", err)
	}

	proof, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: intent.OrderID, AccountID: account.ID, TransactionNo: "CORP-OK-0001",
		AmountCents: intent.AmountCents, ProofRef: "proof/corp-ok-0001.pdf", Note: "对公转账回单",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	// 核销前订单保持待付款、不派单。
	order := db.EsportsOrder.GetX(ctx, intent.OrderID)
	if order.PaymentStatus != "unpaid" || order.FulfillmentStatus != "pending_payment" {
		t.Fatalf("核销前订单不应变更：%+v", order)
	}
	// 同一渠道流水不可重复提交。
	if _, err = submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: intent.OrderID, AccountID: account.ID, TransactionNo: "CORP-OK-0001",
		AmountCents: intent.AmountCents, ProofRef: "proof/corp-ok-dup.pdf",
	}, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复渠道流水应被拒，err=%v", err)
	}
	// 提交人不得自审。
	if _, err = verifyOfflinePaymentProof(ctx, db, proof.ID, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("提交人不得自审到账凭证，err=%v", err)
	}
	verified, err := verifyOfflinePaymentProof(ctx, db, proof.ID, 12, time.Now().UTC())
	if err != nil || verified.Status != "verified" || verified.ReceiptID == nil {
		t.Fatalf("财务核销失败：%+v err=%v", verified, err)
	}
	order = db.EsportsOrder.GetX(ctx, intent.OrderID)
	if order.PaymentStatus != "paid" || order.FulfillmentStatus != "pending_dispatch" {
		t.Fatalf("核销后订单应转已付款待派单：%+v", order)
	}
	receipt := db.EsportsPaymentReceipt.GetX(ctx, *verified.ReceiptID)
	if receipt.Channel != "corporate_transfer" || receipt.TransactionNo != "CORP-OK-0001" ||
		receipt.Status != "verified" || receipt.AppliedCents != intent.AmountCents {
		t.Fatalf("收款收据不正确：%+v", receipt)
	}
	// 重复核销不重复入账。
	if _, err = verifyOfflinePaymentProof(ctx, db, proof.ID, 12, time.Now().UTC()); !errors.Is(err, errConflict) {
		t.Fatalf("重复核销应冲突，err=%v", err)
	}
	if count := db.EsportsPaymentReceipt.Query().CountX(ctx); count != 1 {
		t.Fatalf("收据不应重复落库：%d", count)
	}
}

// 财务驳回到账凭证：须填原因、提交人不得自审，驳回后订单仍为待付款。
func TestOfflinePaymentProofRejection(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_offline_payment_reject?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "merchant_qr", "MCH-0001")
	intent := seedPayableOrderForOffline(t, db, "reject", "merchant_qr")
	proof, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: intent.OrderID, AccountID: account.ID, TransactionNo: "QR-0001",
		AmountCents: intent.AmountCents, ProofRef: "proof/qr-0001.png",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rejectOfflinePaymentProof(ctx, db, proof.ID, 11, "自审", now); !errors.Is(err, errForbidden) {
		t.Fatalf("提交人不得自审驳回，err=%v", err)
	}
	if _, err = rejectOfflinePaymentProof(ctx, db, proof.ID, 12, "  ", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("驳回必须给原因，err=%v", err)
	}
	rejected, err := rejectOfflinePaymentProof(ctx, db, proof.ID, 12, "截图金额与订单不符", now)
	if err != nil || rejected.Status != "rejected" {
		t.Fatalf("驳回失败：%+v err=%v", rejected, err)
	}
	order := db.EsportsOrder.GetX(ctx, intent.OrderID)
	if order.PaymentStatus != "unpaid" || order.FulfillmentStatus != "pending_payment" {
		t.Fatalf("驳回后订单应保持待付款：%+v", order)
	}
	// 驳回后可重新提交（不同流水号）。
	if _, err = submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: intent.OrderID, AccountID: account.ID, TransactionNo: "QR-0002",
		AmountCents: intent.AmountCents, ProofRef: "proof/qr-0002.png",
	}, 11, now); err != nil {
		t.Fatalf("驳回后应可重新提交凭证，err=%v", err)
	}
}

// 纯客服报单（无客户自助支付意图）：客服提交外部到账凭证时应自动补建
// 「客服代确认」报价确认书与支付意图，财务核验后回写收款方式。
func TestCSOfflineProofAutoCreatesIntent(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_offline_cs_auto?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	intent := seedPayableOrderForOffline(t, db, "csauto", "wechat_work")
	account := seedActivePaymentAccount(t, db, "wechat_work", "WX-CS-AUTO")
	// 模拟纯客服报单：客户从未在 H5 生成支付意图。
	db.EsportsPaymentIntent.DeleteOneID(intent.ID).ExecX(ctx)
	now := time.Now().UTC()

	proof, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: intent.OrderID, AccountID: account.ID, TransactionNo: "WX-CS-AUTO-0001",
		AmountCents: 5000, ProofRef: "proof/cs-auto.png", Note: "客服代收",
	}, 11, now)
	if err != nil {
		t.Fatalf("纯客服报单提交凭证应自动补建支付意图: %v", err)
	}
	if _, err := verifyOfflinePaymentProof(ctx, db, proof.ID, 12, now.Add(time.Minute)); err != nil {
		t.Fatalf("财务核验失败: %v", err)
	}
	order := db.EsportsOrder.GetX(ctx, intent.OrderID)
	if order.PaymentStatus != "paid" || order.PaymentChannel != "wechat_work" {
		t.Fatalf("核验后订单状态/收款方式不正确: status=%s channel=%s", order.PaymentStatus, order.PaymentChannel)
	}
}

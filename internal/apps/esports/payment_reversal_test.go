package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentreversal"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/systemconfig"
)

// seedReversalFixture 走完「待付款订单 → 已核销外部收款」拿到一张可被撤销的收款回执。
// 与 offline_payment_test 的种子不同，这里按需写支付时限配置，允许同一测试库内造多笔。
func seedReversalFixture(t *testing.T, db *ent.Client, suffix string) (*ent.EsportsPaymentReceipt, *ent.EsportsOrder) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	if !db.SystemConfig.Query().Where(systemconfig.KeyEQ("app.esports.payment_deadline_minutes")).ExistX(ctx) {
		db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
			SetValue("60").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	}
	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetAgeStatus("adult").ExecX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "REV-" + suffix, Title: "撤销测试商品", ServiceType: "play", UnitPriceCents: 3000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"rev-order-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	confirmation := db.EsportsConfirmation.Create().SetCustomerID(customer.ID).
		SetSubjectType("order_quote").SetSubjectID(order.ID).SetSubjectVersion(order.QuoteVersion).
		SetSkuID(sku.ID).SetQuantity(order.Quantity).SetAmountCents(order.PayableCents).
		SetChannel("verified_customer_message").SetEvidenceRef("rev-evidence-"+suffix).
		SetStatus("verified").SetVerifiedAt(now).SetExpiresAt(order.PaymentDeadline).SaveX(ctx)
	intent := db.EsportsPaymentIntent.Create().SetIntentNo("rev-intent-" + suffix).
		SetOrderID(order.ID).SetConfirmationID(confirmation.ID).
		SetQuoteVersion(order.QuoteVersion).SetAmountCents(order.PayableCents).
		SetChannel("corporate_transfer").SetExpiresAt(order.PaymentDeadline).SaveX(ctx)
	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-9999-0000-"+suffix)
	proof, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: order.ID, AccountID: account.ID, TransactionNo: "CORP-" + suffix,
		AmountCents: intent.AmountCents, ProofRef: "proof/" + suffix + ".pdf",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifyOfflinePaymentProof(ctx, db, proof.ID, 12, now)
	if err != nil {
		t.Fatal(err)
	}
	return db.EsportsPaymentReceipt.GetX(ctx, *verified.ReceiptID), db.EsportsOrder.GetX(ctx, order.ID)
}

// 已核销收款的渠道撤销 / 拒付（§3.2）：登记只记账不改订单，处置才落地资金异常，且登记人不得自审。
func TestPaymentReversalRegistersAndMarksOrderException(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_payment_reversal?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	receipt, order := seedReversalFixture(t, db, "001")

	// 金额不得超过该笔收款实际到账额；原因与凭证长度受约束。
	if _, err := registerPaymentReversal(ctx, db, 11, paymentReversalInput{
		ReceiptID: receipt.ID, AmountCents: receipt.ActualCents + 1, Reason: "银行通知客户拒付",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超额撤销未被拦截：%v", err)
	}
	if _, err := registerPaymentReversal(ctx, db, 11, paymentReversalInput{
		ReceiptID: receipt.ID, AmountCents: receipt.ActualCents, Reason: "x",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("过短原因未被拦截：%v", err)
	}
	if _, err := registerPaymentReversal(ctx, db, 11, paymentReversalInput{
		ReceiptID: receipt.ID, AmountCents: receipt.ActualCents, Reason: "银行通知客户拒付", Kind: "refund",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法类型未被拦截：%v", err)
	}

	reversal, err := registerPaymentReversal(ctx, db, 11, paymentReversalInput{
		ReceiptID: receipt.ID, Kind: "chargeback", AmountCents: receipt.ActualCents,
		Reason: "银行通知客户拒付", EvidenceRef: "bank/chargeback-001.pdf",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if reversal.Status != esportspaymentreversal.StatusPending ||
		reversal.OrderID != order.ID || reversal.Channel != "corporate_transfer" ||
		reversal.TransactionNo != receipt.TransactionNo ||
		reversal.SubmittedByAdminID == nil || *reversal.SubmittedByAdminID != 11 {
		t.Fatalf("撤销事件登记内容不正确：%+v", reversal)
	}
	// 登记只是风险事件：订单资金状态不动，等人工处置。
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.PaymentStatus != esportsorder.PaymentStatusPaid {
		t.Fatalf("登记撤销不应改订单资金状态：%+v", got)
	}
	// 一笔回执只能登记一次。
	if _, err := registerPaymentReversal(ctx, db, 11, paymentReversalInput{
		ReceiptID: receipt.ID, AmountCents: 100, Reason: "重复登记撤销",
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复登记未被拦截：%v", err)
	}
	events := countEventsByType(t, db)
	if events["order.payment_reversal_registered"] != 1 {
		t.Fatalf("缺少撤销登记事件：%+v", events)
	}

	// 登记人不得自审；非法动作与缺说明被拒。
	if _, err := decidePaymentReversal(ctx, db, reversal.ID, 11, paymentReversalDecisionInput{
		Action: reversalActionMarkException, Resolution: "自审",
	}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("登记人自审未被拦截：%v", err)
	}
	if _, err := decidePaymentReversal(ctx, db, reversal.ID, 12, paymentReversalDecisionInput{
		Action: "refund",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法处置动作未被拦截：%v", err)
	}
	if _, err := decidePaymentReversal(ctx, db, reversal.ID, 12, paymentReversalDecisionInput{
		Action: reversalActionMarkException,
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("缺处置说明未被拦截：%v", err)
	}

	handled, err := decidePaymentReversal(ctx, db, reversal.ID, 12, paymentReversalDecisionInput{
		Action: reversalActionMarkException, Resolution: "已确认渠道撤销，转人工对账追款",
	}, now)
	if err != nil || handled.Status != esportspaymentreversal.StatusHandled ||
		handled.Action != esportspaymentreversal.ActionMarkException ||
		handled.HandledByAdminID == nil || *handled.HandledByAdminID != 12 || handled.HandledAt == nil {
		t.Fatalf("处置失败：%+v err=%v", handled, err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.PaymentStatus != esportsorder.PaymentStatusException {
		t.Fatalf("处置后订单未转资金异常：%+v", got)
	}
	if countEventsByType(t, db)["order.payment_reversal_handled"] != 1 {
		t.Fatal("缺少撤销处置事件")
	}
	// 已处置的事件不能重复处置。
	if _, err := decidePaymentReversal(ctx, db, reversal.ID, 13, paymentReversalDecisionInput{
		Action: reversalActionIgnore,
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复处置未被拦截：%v", err)
	}
	if rows, err := listPaymentReversals(ctx, db, order.ID, "handled", 10); err != nil || len(rows) != 1 {
		t.Fatalf("按订单与状态查询撤销事件不正确：%+v %v", rows, err)
	}
	if _, err := listPaymentReversals(ctx, db, 0, "unknown", 10); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法状态过滤未被拦截：%v", err)
	}
}

// 处置动作二：限制客户自助下单（同时把订单置资金异常）；误报则只留结论不动订单。
func TestPaymentReversalSuspendCustomerAndIgnore(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_payment_reversal_decide?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	firstReceipt, firstOrder := seedReversalFixture(t, db, "101")
	suspended, err := registerPaymentReversal(ctx, db, 11, paymentReversalInput{
		ReceiptID: firstReceipt.ID, AmountCents: firstReceipt.ActualCents, Reason: "客户申请银行拒付",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	// 限制客户必须同时给风控原因。
	if _, err := decidePaymentReversal(ctx, db, suspended.ID, 12, paymentReversalDecisionInput{
		Action: reversalActionSuspendCustomer, Resolution: "确认拒付",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("限制客户缺风控原因未被拦截：%v", err)
	}
	decided, err := decidePaymentReversal(ctx, db, suspended.ID, 12, paymentReversalDecisionInput{
		Action: reversalActionSuspendCustomer, Resolution: "确认拒付，已通知客户补款",
		RiskReason: "渠道拒付且未补款",
	}, now)
	if err != nil || decided.Action != esportspaymentreversal.ActionSuspendCustomer {
		t.Fatalf("限制客户处置失败：%+v err=%v", decided, err)
	}
	if got := db.EsportsOrder.GetX(ctx, firstOrder.ID); got.PaymentStatus != esportsorder.PaymentStatusException {
		t.Fatalf("限制客户处置应同时把订单置资金异常：%+v", got)
	}
	customer := db.EsportsCustomer.GetX(ctx, firstOrder.CustomerID)
	if customer.RiskStatus != esportscustomer.RiskStatusRestricted ||
		customer.RiskReason != "渠道拒付且未补款" || customer.RiskMarkedAt == nil {
		t.Fatalf("客户未按风控口径受限：%+v", customer)
	}
	// 客户自助下单被阻断。
	if err := ensureCustomerSelfServiceAllowed(customer); err == nil {
		t.Fatal("受限客户仍可自助下单")
	}

	// 误报：只留结论，订单与客户都不动。
	secondReceipt, secondOrder := seedReversalFixture(t, db, "102")
	second, err := registerPaymentReversal(ctx, db, 11, paymentReversalInput{
		ReceiptID: secondReceipt.ID, Kind: "revoked",
		AmountCents: secondReceipt.ActualCents, Reason: "渠道通知商户撤销",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	ignored, err := decidePaymentReversal(ctx, db, second.ID, 13, paymentReversalDecisionInput{
		Action: reversalActionIgnore,
	}, now)
	if err != nil || ignored.Status != esportspaymentreversal.StatusRejected ||
		ignored.Action != esportspaymentreversal.ActionIgnore {
		t.Fatalf("误报处置失败：%+v err=%v", ignored, err)
	}
	if got := db.EsportsOrder.GetX(ctx, secondOrder.ID); got.PaymentStatus != esportsorder.PaymentStatusPaid {
		t.Fatalf("误报不应改订单资金状态：%+v", got)
	}
	if count := countEventsByType(t, db)["order.payment_reversal_handled"]; count != 2 {
		t.Fatalf("处置留痕条数不正确：%d", count)
	}
}

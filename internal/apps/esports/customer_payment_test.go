package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportspaymentaccount"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/systemconfig"
)

// seedCustomerPaymentFixture 造一个「已绑定登录身份 + 成年 + 已核验报价」的待付款订单客户，
// 且订单尚无支付意图（由 submitCustomerOfflinePaymentProof 现场保障）。
func seedCustomerPaymentFixture(t *testing.T, db *ent.Client, suffix string) (*ent.EsportsCustomer, *ent.EsportsOrder) {
	t.Helper()
	ctx := t.Context()
	if ok, _ := db.SystemConfig.Query().Where(systemconfig.KeyEQ("app.esports.payment_deadline_minutes")).Exist(ctx); !ok {
		db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
			SetValue("60").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	}
	uid := "pay-cust-" + suffix
	account := db.User.Create().SetEmail(uid + "@example.com").SetPassword("test").
		SetUID(uid).SetUsername(uid).SetStatus("active").SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	customer = db.EsportsCustomer.UpdateOneID(customer.ID).SetUserID(account.ID).SetAgeStatus("adult").SaveX(ctx)
	db.EsportsIdentityBinding.Create().SetCustomerID(customer.ID).SetProvider("user").
		SetSubjectScope("").SetExternalSubject(uid).
		SetVerificationMethod("verified_account_link").SetVerifiedAt(time.Now().UTC()).SaveX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "PAY-" + suffix, Title: "陪玩一小时", ServiceType: "play", UnitPriceCents: 5000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: 2, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"pay-order-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsConfirmation.Create().SetCustomerID(customer.ID).
		SetSubjectType("order_quote").SetSubjectID(order.ID).SetSubjectVersion(order.QuoteVersion).
		SetSkuID(sku.ID).SetQuantity(order.Quantity).SetAmountCents(order.PayableCents).
		SetChannel("authenticated_self_service").SetEvidenceRef("pay-evidence-" + suffix).
		SetStatus("verified").SetVerifiedAt(now).SetExpiresAt(order.PaymentDeadline).SaveX(ctx)
	return customer, order
}

// seedPaymentProofAttachment 造一个本人上传的付款凭证附件，返回附件 ID。
func seedPaymentProofAttachment(t *testing.T, db *ent.Client, uploadedBy string) int {
	t.Helper()
	return db.Attachment.Create().SetName("付款凭证").SetOriginalName("proof.png").
		SetPath("private/payment-proof/proof.png").SetMimeType("image/png").SetExtension("png").
		SetDirectory(paymentProofEvidenceDirectory).SetSize(128).SetUploadedBy(uploadedBy).
		SetContentHash(strings.Repeat("b", 64)).SaveX(t.Context()).ID
}

// 客户读收款方式：启用的外部渠道返回且仅含审核通过账户；后台关闭的渠道不返回；待审核账户不返回。
func TestCustomerPaymentMethodsList(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_customer_payment_methods?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedPaymentSubjectWhitelist(t, db)
	active := seedActivePaymentAccount(t, db, "wechat_work", "WX-ACCT-1")
	pending, err := createPaymentAccount(ctx, db, paymentAccountInput{
		Channel: "wechat_work", SubjectName: "待审主体有限公司", QrURL: "https://example.com/pending.png",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != esportspaymentaccount.StatusPendingReview {
		t.Fatalf("待审核账户应保持待审核：%+v", pending)
	}

	methods, err := listCustomerPaymentMethods(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	var wecom *customerPaymentMethodView
	for i := range methods {
		if methods[i].Channel == "wechat_work" {
			wecom = &methods[i]
		}
	}
	if wecom == nil || !wecom.Enabled || len(wecom.Accounts) != 1 || wecom.Accounts[0].ID != active.ID {
		t.Fatalf("企微收款应启用且仅返回审核通过账户：%+v", wecom)
	}
	for _, acc := range wecom.Accounts {
		if acc.ID == pending.ID {
			t.Fatalf("待审核账户不应返回：%+v", acc)
		}
	}

	// 后台关闭商户码后不返回。
	if err = setPaymentChannelEnabled(ctx, db, "merchant_qr", false); err != nil {
		t.Fatal(err)
	}
	methods, err = listCustomerPaymentMethods(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range methods {
		if m.Channel == "merchant_qr" {
			t.Fatalf("关闭的商户码不应返回：%+v", m)
		}
	}
}

// 客户提交付款凭证 → 生成待核验收款，财务核验后订单转已付款待派单，核验进度可查。
func TestCustomerSubmitPaymentProofCreatesPendingVerification(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_customer_payment_proof?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-3333")
	customer, order := seedCustomerPaymentFixture(t, db, "submit")
	attachmentID := seedPaymentProofAttachment(t, db, "user:pay-cust-submit")

	proof, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-CORP-0001", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{attachmentID},
	}, now)
	if err != nil || proof.Status != "pending_verification" {
		t.Fatalf("提交凭证应生成待核验：%+v err=%v", proof, err)
	}
	reload := db.EsportsOrder.GetX(ctx, order.ID)
	if reload.PaymentStatus != "unpaid" || reload.FulfillmentStatus != "pending_payment" {
		t.Fatalf("核验前订单不应变更：%+v", reload)
	}

	verified, err := verifyOfflinePaymentProof(ctx, db, proof.ID, 12, time.Now().UTC())
	if err != nil || verified.Status != "verified" || verified.ReceiptID == nil {
		t.Fatalf("财务核验失败：%+v err=%v", verified, err)
	}
	reload = db.EsportsOrder.GetX(ctx, order.ID)
	if reload.PaymentStatus != "paid" || reload.FulfillmentStatus != "pending_dispatch" {
		t.Fatalf("核验后订单应转已付款待派单：%+v", reload)
	}

	views, err := listCustomerPaymentProofs(ctx, db, customer.ID, order.ID)
	if err != nil || len(views) != 1 || views[0].Status != "verified" {
		t.Fatalf("核验进度应可查询：%+v err=%v", views, err)
	}
}

// 客户查询核验进度：他人订单 403；关闭的收款方式下提交凭证被拒。
func TestCustomerPaymentProofQueryIsolationAndChannelOff(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_customer_payment_query?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "merchant_qr", "MCH-QR-9")
	customer, order := seedCustomerPaymentFixture(t, db, "query")
	other, _ := seedCustomerPaymentFixture(t, db, "other")
	attachmentID := seedPaymentProofAttachment(t, db, "user:pay-cust-query")

	if _, err := listCustomerPaymentProofs(ctx, db, other.ID, order.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("他人订单查询核验进度应 403：%v", err)
	}

	if err := setPaymentChannelEnabled(ctx, db, "merchant_qr", false); err != nil {
		t.Fatal(err)
	}
	if _, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-QR-0001", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{attachmentID},
	}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("关闭收款方式后提交凭证应被拒：%v", err)
	}
}

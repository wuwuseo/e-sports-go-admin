package esports

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

// poPaymentApp 构造收款相关的管理端最小路由（与 app.go 同路径）。
func poPaymentApp(admin *ent.Admin, isSuper bool) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Get("/admin/payment-accounts", adminPaymentAccounts)
	app.Post("/admin/payment-accounts", createPaymentAccountHandler)
	app.Get("/admin/payment-channels", adminPaymentChannels)
	app.Post("/admin/payment-channels/:channel/status", setPaymentChannelStatusHandler)
	app.Get("/admin/offline-payment-proofs", adminOfflinePaymentProofs)
	app.Post("/admin/offline-payment-proofs", submitOfflinePaymentProofHandler)
	return app
}

// poSeedProofAttachment 造一条付款凭证附件（可指定 MIME），返回附件 ID。
func poSeedProofAttachment(t *testing.T, db *ent.Client, uploadedBy, mime string) int {
	t.Helper()
	return db.Attachment.Create().SetName("转账截图").SetOriginalName("proof.bin").
		SetPath("private/payment-proof/proof.bin").SetMimeType(mime).SetExtension("bin").
		SetDirectory(paymentProofEvidenceDirectory).SetSize(256).SetUploadedBy(uploadedBy).
		SetContentHash(strings.Repeat("c", 64)).SaveX(t.Context()).ID
}

// poPendingPaymentOrder 造一张指定单价的待付款订单（客户已绑定前台用户，可供客户侧收款链路使用）。
func poPendingPaymentOrder(t *testing.T, db *ent.Client, suffix string, unitPrice int64) (*ent.EsportsCustomer, *ent.EsportsOrder) {
	t.Helper()
	ctx := t.Context()
	poSeedConfig(t, db, "app.esports.payment_deadline_minutes", "60")
	uid := "po-pay-" + suffix
	account := db.User.Create().SetEmail(uid + "@example.com").SetPassword("test").
		SetUID(uid).SetUsername(uid).SetStatus("active").SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	customer = db.EsportsCustomer.UpdateOneID(customer.ID).SetUserID(account.ID).SetAgeStatus("adult").SaveX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "PO-PAY-" + suffix, Title: "收款测试商品", ServiceType: "play", UnitPriceCents: unitPrice,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"po-pay-order-"+suffix, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return customer, order
}

// ── 15.7-01 SKU 下架后发起收款必须直接失败，不得先生成收款单 ──

func TestRound4PO15701OffSaleSkuBlocksPaymentInitiation(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_off_sale_payment")
	defer db.Close()
	ctx := t.Context()
	account := seedActivePaymentAccount(t, db, "wechat_work", "WX-PO-15701")
	customer, order := seedCustomerPaymentFixture(t, db, "po15701")
	attachmentID := seedPaymentProofAttachment(t, db, "user:pay-cust-po15701")
	now := time.Now().UTC()
	db.EsportsSku.Update().Where(esportssku.IDEQ(order.SkuID)).SetStatus(esportssku.StatusOffSale).SaveX(ctx)

	// 客户自助提交付款凭证 → 明确失败。
	_, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "PO-OFF-CUST-1", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{attachmentID},
	}, now)
	if err == nil || !strings.Contains(err.Error(), "下架") {
		t.Fatalf("下架商品应直接拒绝客户发起收款：%v", err)
	}

	// 客服提交到账凭证 → 明确失败。
	_, err = submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: order.ID, AccountID: account.ID, TransactionNo: "PO-OFF-CS-1",
		AmountCents: order.PayableCents, ProofRef: "po-offline-proof-ref-15701",
	}, 12, now)
	if err == nil || !strings.Contains(err.Error(), "下架") {
		t.Fatalf("下架商品应直接拒绝客服发起收款：%v", err)
	}

	// 两份收款单与支付意图都不应新增，订单保持待付款。
	if got := db.EsportsOfflinePaymentProof.Query().CountX(ctx); got != 0 {
		t.Fatalf("下架商品不应生成收款单，实际 %d 条", got)
	}
	if got := db.EsportsPaymentIntent.Query().CountX(ctx); got != 0 {
		t.Fatalf("下架商品不应生成支付意图，实际 %d 条", got)
	}
	reload := db.EsportsOrder.GetX(ctx, order.ID)
	if reload.PaymentStatus != "unpaid" || reload.FulfillmentStatus != "pending_payment" {
		t.Fatalf("订单状态不应变化：%+v", reload)
	}
}

// ── 4.2-04 客服转账凭证落附件并校验归属与 MIME ──

func TestRound4PO4204CSProofEvidenceAttachment(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_cs_proof_evidence")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	ctx := t.Context()

	account := seedActivePaymentAccount(t, db, "wechat_work", "WX-PO-4204")
	_, order := seedCustomerPaymentFixture(t, db, "po4204")
	admin := seedAdminAccount(t, db, "po-cs-proof")
	app := poPaymentApp(admin, true)
	own := poSeedProofAttachment(t, db, "admin:"+admin.UID, "image/png")
	foreign := poSeedProofAttachment(t, db, "admin:someone-else", "image/png")
	notImage := poSeedProofAttachment(t, db, "admin:"+admin.UID, "text/plain")
	bodyOf := func(txn string, ids []int) map[string]any {
		return map[string]any{
			"order_id": order.ID, "account_id": account.ID, "transaction_no": txn,
			"amount_cents": order.PayableCents, "proof_ref": "po-offline-proof-ref-4204",
			"evidence_attachment_ids": ids,
		}
	}

	// 未传附件时按存量文本 proof_ref 兼容受理（避免破坏既有客服入口）。
	legacy := bodyOf("PO-EVID-LEGACY", nil)
	legacy["proof_ref"] = "proof/legacy-wecom-0001.png"
	status, payload := poSendJSON(t, app, http.MethodPost, "/admin/offline-payment-proofs", "po-4204-001", legacy)
	if status != fiber.StatusOK {
		t.Fatalf("存量文本凭证应保持可用：status=%d msg=%s", status, poMessage(payload))
	}
	// 引用他人上传的附件 → 403。
	status, payload = poSendJSON(t, app, http.MethodPost, "/admin/offline-payment-proofs", "po-4204-002",
		bodyOf("PO-EVID-FOREIGN", []int{foreign}))
	if status != fiber.StatusForbidden {
		t.Fatalf("引用他人附件应 403：status=%d msg=%s", status, poMessage(payload))
	}
	// MIME 非图片 / PDF → 参数错误。
	status, payload = poSendJSON(t, app, http.MethodPost, "/admin/offline-payment-proofs", "po-4204-003",
		bodyOf("PO-EVID-MIME", []int{notImage}))
	if status != fiber.StatusBadRequest {
		t.Fatalf("非图片/PDF 附件应被拒：status=%d msg=%s", status, poMessage(payload))
	}
	// 合法附件 → 生成待核验收款，凭证可解析出附件 ID 与提交人。
	status, payload = poSendJSON(t, app, http.MethodPost, "/admin/offline-payment-proofs", "po-4204-004",
		bodyOf("PO-EVID-OK", []int{own}))
	if status != fiber.StatusOK {
		t.Fatalf("合法附件应被接受：status=%d msg=%s", status, poMessage(payload))
	}
	data, _ := payload["data"].(map[string]any)
	if fmt.Sprint(data["proof_ref"]) != fmt.Sprintf("[%d]", own) {
		t.Fatalf("凭证未落附件 ID：%+v", data)
	}
	proofID := int(data["id"].(float64))
	proof := db.EsportsOfflinePaymentProof.GetX(ctx, proofID)
	if proof.SubmittedByAdminID == nil || *proof.SubmittedByAdminID != admin.ID {
		t.Fatalf("凭证未记录提交人：%+v", proof.SubmittedByAdminID)
	}
}

// ── 18-02 发起支付 / 核销入口须校验支付时限配置非零 ──

func TestRound4PO1802PaymentDeadlineRequiredOnInitiation(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_deadline_guard")
	defer db.Close()
	ctx := t.Context()
	account := seedActivePaymentAccount(t, db, "wechat_work", "WX-PO-1802")
	customer, order := seedCustomerPaymentFixture(t, db, "po1802")
	attachmentID := seedPaymentProofAttachment(t, db, "user:pay-cust-po1802")
	now := time.Now().UTC()
	db.SystemConfig.Delete().Where(systemconfig.KeyEQ("app.esports.payment_deadline_minutes")).ExecX(ctx)

	if _, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: order.ID, AccountID: account.ID, TransactionNo: "PO-DEAD-1",
		AmountCents: order.PayableCents, ProofRef: "po-offline-proof-ref-1802",
	}, 12, now); !errors.Is(err, errNotConfigured) {
		t.Fatalf("支付时限未配置时应拒绝客服发起收款：%v", err)
	}
	if _, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "PO-DEAD-2", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{attachmentID},
	}, now); !errors.Is(err, errNotConfigured) {
		t.Fatalf("支付时限未配置时应拒绝客户发起收款：%v", err)
	}
	if _, err := startOrderPayment(ctx, db, customer.ID, order.ID, "wechat_work", now); !errors.Is(err, errNotConfigured) {
		t.Fatalf("支付时限未配置时应拒绝在线发起支付：%v", err)
	}
	if got := db.EsportsOfflinePaymentProof.Query().CountX(ctx); got != 0 {
		t.Fatalf("未配置支付时限不应生成收款单，实际 %d 条", got)
	}
	if got := db.EsportsPaymentIntent.Query().CountX(ctx); got != 0 {
		t.Fatalf("未配置支付时限不应生成支付意图，实际 %d 条", got)
	}
}

// ── 4.3-05 大额订单必须走对公转账 ──

func TestRound4PO4305LargeAmountRequiresCorporateTransfer(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_corporate_threshold")
	defer db.Close()
	ctx := t.Context()
	poSeedConfig(t, db, "app.esports.corporate_transfer_threshold_cents", "30000")
	wecom := seedActivePaymentAccount(t, db, "wechat_work", "WX-PO-4305")
	corporate := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-4305")
	customer, order := poPendingPaymentOrder(t, db, "4305", 50000)
	now := time.Now().UTC()

	// 大额 + 非对公渠道 → 明确拒绝。
	_, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: order.ID, AccountID: wecom.ID, TransactionNo: "PO-CORP-1",
		AmountCents: order.PayableCents, ProofRef: "po-offline-proof-ref-4305",
	}, 12, now)
	if err == nil || !strings.Contains(err.Error(), "对公") {
		t.Fatalf("大额订单非对公渠道应被拒：%v", err)
	}
	// 在线发起支付同样受约束。
	if _, err = startOrderPayment(ctx, db, customer.ID, order.ID, "wechat_work", now); err == nil ||
		!strings.Contains(err.Error(), "对公") {
		t.Fatalf("大额订单在线渠道应被拒：%v", err)
	}
	// 对公渠道放行。
	if _, err = submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: order.ID, AccountID: corporate.ID, TransactionNo: "PO-CORP-2",
		AmountCents: order.PayableCents, ProofRef: "po-offline-proof-ref-4305b",
	}, 12, now); err != nil {
		t.Fatalf("大额订单走对公应放行：%v", err)
	}
}

// ── 4.3-07 收款主体白名单 ──

func TestRound4PO4307PaymentSubjectWhitelist(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_subject_whitelist")
	defer db.Close()
	ctx := t.Context()
	poSeedConfig(t, db, "app.esports.payment_subject_whitelist", "示例工作室有限公司,备用主体")
	now := time.Now().UTC()

	item, err := createPaymentAccount(ctx, db, paymentAccountInput{
		Channel: "wechat_work", SubjectName: "示例工作室有限公司", QrURL: "https://example.com/ok.png",
	}, 11, now)
	if err != nil {
		t.Fatalf("白名单内主体应可提交：%v", err)
	}
	if item.Status != "pending_review" {
		t.Fatalf("白名单内主体仍须财务审核：%+v", item)
	}
	if _, err = createPaymentAccount(ctx, db, paymentAccountInput{
		Channel: "merchant_qr", SubjectName: "冒名主体有限公司", QrURL: "https://example.com/bad.png",
	}, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("白名单外主体应被拒：%v", err)
	}
	// 白名单清空 = 未配置：fail-closed，任何主体一律拒绝（旧口径「留空=不限制」已废弃）。
	poSeedConfig(t, db, "app.esports.payment_subject_whitelist", "")
	if _, err = createPaymentAccount(ctx, db, paymentAccountInput{
		Channel: "merchant_qr", SubjectName: "冒名主体有限公司", QrURL: "https://example.com/bad.png",
	}, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未配置白名单时必须拒绝任何主体：%v", err)
	}
	if _, err = createPaymentAccount(ctx, db, paymentAccountInput{
		Channel: "merchant_qr", SubjectName: "示例工作室有限公司", QrURL: "https://example.com/ok2.png",
	}, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未配置白名单时名单内主体同样应被拒（须先登记白名单）：%v", err)
	}
}

// ── 4.3-08 余额（站内方式）纳入渠道启停；储值卡已下线，仅校验其被明确拒绝 ──

func TestRound4PO4308InternalPaymentChannelToggles(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_internal_channels")
	defer db.Close()
	ctx := t.Context()

	methods, err := listCustomerPaymentMethods(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	enabledOf := func(list []customerPaymentMethodView, channel string) (bool, bool) {
		for _, item := range list {
			if item.Channel == channel {
				return item.Enabled, true
			}
		}
		return false, false
	}
	if enabled, ok := enabledOf(methods, "customer_balance"); !ok || !enabled {
		t.Fatalf("余额默认应启用：%+v", methods)
	}
	if err = setPaymentChannelEnabled(ctx, db, "customer_balance", false); err != nil {
		t.Fatalf("余额应可被后台停用：%v", err)
	}
	// 储值卡已随功能下线，不再是可启停的收款方式：显式拒绝而不是静默写配置。
	if err = setPaymentChannelEnabled(ctx, db, "stored_value_card", false); !errors.Is(err, errInvalidInput) {
		t.Fatalf("已下线的储值卡收款方式应被拒绝启停，err=%v", err)
	}
	channels, err := listPaymentChannels(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range channels {
		if item.Channel == "customer_balance" {
			found = true
			if item.Enabled {
				t.Fatalf("余额停用后启停列表应显示停用：%+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("渠道启停列表应包含站内收款方式：%+v", channels)
	}
	methods, err = listCustomerPaymentMethods(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if enabled, _ := enabledOf(methods, "customer_balance"); enabled {
		t.Fatalf("停用后客户侧不应看到余额收款：%+v", methods)
	}
}

// ── 4.3-04 收款自动对账：按渠道流水号与金额比对并识别差异 ──

func TestRound4PO4304ChannelReconciliationDiff(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_reconcile")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	ctx := t.Context()

	account := seedActivePaymentAccount(t, db, "wechat_work", "WX-PO-4304")
	_, order := seedCustomerPaymentFixture(t, db, "po4304")
	admin := seedAdminAccount(t, db, "po-reconcile-admin")
	app := poPaymentApp(admin, true)
	post := func(key, txn string, amount int64) *ent.EsportsOfflinePaymentProof {
		status, payload := poSendJSON(t, app, http.MethodPost, "/admin/offline-payment-proofs", key, map[string]any{
			"order_id": order.ID, "account_id": account.ID, "transaction_no": txn,
			"amount_cents": amount, "proof_ref": "po-reconcile-proof-" + key,
		})
		if status != fiber.StatusOK {
			t.Fatalf("提交凭证 %s 失败：status=%d msg=%s", key, status, poMessage(payload))
		}
		data, _ := payload["data"].(map[string]any)
		return db.EsportsOfflinePaymentProof.GetX(ctx, int(data["id"].(float64)))
	}
	// 1) 与渠道回执金额一致且等于订单应付 → 对账通过。
	matched := post("po-rec-001", "TXN-MATCH", order.PayableCents)
	// 2) 渠道回执金额与收款单不一致 → amount_mismatch。
	mismatched := post("po-rec-002", "TXN-RECEIPT-DIFF", order.PayableCents)
	// 3) 渠道账单无此流水 → channel_unmatched。
	unmatched := post("po-rec-003", "TXN-NO-RECEIPT", order.PayableCents)
	// 4) 与渠道回执一致但高于订单应付 → order_amount_mismatch。
	overpaid := post("po-rec-004", "TXN-OVERPAID", order.PayableCents+500)
	seedReceipt := func(proof *ent.EsportsOfflinePaymentProof, actualCents int64) {
		db.EsportsPaymentReceipt.Create().SetIntentID(proof.IntentID).SetChannel("wechat_work").
			SetTransactionNo(proof.TransactionNo).SetActualCents(actualCents).
			SetAppliedCents(actualCents).SetStatus("verified").
			SetConfirmedAt(time.Now().UTC()).SaveX(ctx)
	}
	seedReceipt(matched, order.PayableCents)
	seedReceipt(mismatched, order.PayableCents-100)
	seedReceipt(overpaid, order.PayableCents+500)

	status, payload := poSendJSON(t, app, http.MethodGet,
		fmt.Sprintf("/admin/offline-payment-proofs?order_id=%d&reconcile=1", order.ID), "", nil)
	if status != fiber.StatusOK {
		t.Fatalf("自动对账请求失败：status=%d msg=%s", status, poMessage(payload))
	}
	data, _ := payload["data"].(map[string]any)
	report, _ := data["reconcile"].(map[string]any)
	if report == nil {
		t.Fatalf("未返回对账结果：%+v", data)
	}
	if fmt.Sprint(report["compared"]) != "4" || fmt.Sprint(report["matched"]) != "1" {
		t.Fatalf("对账计数不正确：%+v", report)
	}
	kinds := map[string]int{}
	mismatches, _ := report["mismatches"].([]any)
	for _, raw := range mismatches {
		item, _ := raw.(map[string]any)
		kinds[fmt.Sprint(item["kind"])]++
	}
	if kinds["amount_mismatch"] != 1 || kinds["channel_unmatched"] != 1 || kinds["order_amount_mismatch"] != 1 {
		t.Fatalf("未正确识别渠道流水号 / 金额差异：%+v", report)
	}
	_ = unmatched
}

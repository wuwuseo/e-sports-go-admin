package esports

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/user"
)

// seedBoundCustomer 造一个绑定了前台用户（User.UID）的客户，用于「本人上传附件」的归属校验。
func seedBoundCustomer(t *testing.T, db *ent.Client, uid string) *ent.EsportsCustomer {
	t.Helper()
	account := db.User.Create().SetEmail(uid + "@example.com").SetPassword("test").
		SetUID(uid).SetUsername(uid).SetStatus(user.StatusActive).SaveX(t.Context())
	customer := saveCustomerOrFail(t, db, 11)
	return db.EsportsCustomer.UpdateOneID(customer.ID).SetUserID(account.ID).SaveX(t.Context())
}

// seedAftersaleEvidenceAttachment 造一个售后凭证附件，返回附件 ID。
func seedAftersaleEvidenceAttachment(t *testing.T, db *ent.Client, uploadedBy, directory, mime string) int {
	t.Helper()
	return db.Attachment.Create().SetName("凭证").SetOriginalName("proof.png").
		SetPath("private/aftersale/proof.png").SetMimeType(mime).SetExtension("png").
		SetDirectory(directory).SetSize(128).SetUploadedBy(uploadedBy).
		SetContentHash(strings.Repeat("a", 64)).SaveX(t.Context()).ID
}

// 客户自助售后：引用本人上传的私有附件成功，evidence_ref 序列化为 JSON 数组。
func TestAfterSaleEvidenceCustomerOwnAttachment(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_evidence_own")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := seedBoundCustomer(t, db, "cust1")
	order := seedCustomerOrder(t, db, "ev-own", customer.ID, esportsorder.FulfillmentStatusCompleted)
	attachmentID := seedAftersaleEvidenceAttachment(t, db, "user:cust1", aftersaleEvidenceDirectory, "image/png")

	item, err := createCustomerAfterSaleWithEvidence(ctx, db, customer, afterSaleInput{
		SourceID: order.ID, Kind: "refund_request", Demand: "服务不符，附凭证", Contact: "wx-1",
	}, []int{attachmentID}, now)
	if err != nil {
		t.Fatalf("本人附件售后应成功：%v", err)
	}
	if item.EvidenceRef != fmt.Sprintf("[%d]", attachmentID) {
		t.Fatalf("evidence_ref 应序列化为附件 ID JSON 数组：%+v", item)
	}
	ids, err := parseAfterSaleEvidenceRef(item.EvidenceRef)
	if err != nil || len(ids) != 1 || ids[0] != attachmentID {
		t.Fatalf("evidence_ref 解析不正确：%v %v", ids, err)
	}
}

// 客户自助售后：他人附件 403，非图片 / 非私有目录附件被拒。
func TestAfterSaleEvidenceRejectsForeignOrInvalid(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_evidence_guard")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := seedBoundCustomer(t, db, "cust2")
	order := seedCustomerOrder(t, db, "ev-guard", customer.ID, esportsorder.FulfillmentStatusCompleted)
	foreign := seedAftersaleEvidenceAttachment(t, db, "user:other", aftersaleEvidenceDirectory, "image/png")
	notImage := seedAftersaleEvidenceAttachment(t, db, "user:cust2", aftersaleEvidenceDirectory, "text/plain")
	notPrivate := seedAftersaleEvidenceAttachment(t, db, "user:cust2", "public/aftersale", "image/png")

	if _, err := createCustomerAfterSaleWithEvidence(ctx, db, customer, afterSaleInput{
		SourceID: order.ID, Kind: "refund_request", Demand: "引用他人附件",
	}, []int{foreign}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("引用他人附件应 403：%v", err)
	}
	if _, err := createCustomerAfterSaleWithEvidence(ctx, db, customer, afterSaleInput{
		SourceID: order.ID, Kind: "refund_request", Demand: "引用非图片附件",
	}, []int{notImage}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非图片附件应被拒：%v", err)
	}
	if _, err := createCustomerAfterSaleWithEvidence(ctx, db, customer, afterSaleInput{
		SourceID: order.ID, Kind: "refund_request", Demand: "引用非私有目录附件",
	}, []int{notPrivate}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非私有目录附件应被拒：%v", err)
	}
}

// 售后复核：申请人引用本人上传附件成功，他人附件 403。
func TestAfterSaleEvidenceReviewAttachment(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_evidence_review")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	staff := db.Admin.Create().SetEmail("rev@example.com").SetPassword("test").
		SetUID("rev-staff").SetUsername("rev").SetStatus("active").SaveX(ctx)
	order := seedBalancePaidOrder(t, db, "ev-review")
	item := newRefundAfterSale(t, db, order.ID, "refund_request", 500, 11, 22, now)
	if _, err := rejectAfterSale(ctx, db, item.ID, 22, afterSaleDecisionInput{Reason: "证据不足"}, now); err != nil {
		t.Fatal(err)
	}
	attachmentID := seedAftersaleEvidenceAttachment(t, db, staff.UID, aftersaleEvidenceDirectory, "image/png")

	review, err := requestAfterSaleReview(ctx, db, item.ID, staff.ID,
		afterSaleReviewInput{Reason: "客户补充凭证", EvidenceAttachmentIDs: []int{attachmentID}}, now)
	if err != nil {
		t.Fatalf("本人附件复核申请应成功：%v", err)
	}
	if review.EvidenceRef != fmt.Sprintf("[%d]", attachmentID) {
		t.Fatalf("复核 evidence_ref 应序列化为附件 ID JSON：%+v", review)
	}

	// 另一张终态售后引用他人附件：归属校验 403。
	other := newRefundAfterSale(t, db, order.ID, "duration_short", 400, 11, 22, now)
	if _, err := rejectAfterSale(ctx, db, other.ID, 22, afterSaleDecisionInput{Reason: "证据不足"}, now); err != nil {
		t.Fatal(err)
	}
	foreign := seedAftersaleEvidenceAttachment(t, db, "other-staff", aftersaleEvidenceDirectory, "image/png")
	if _, err := requestAfterSaleReview(ctx, db, other.ID, staff.ID,
		afterSaleReviewInput{Reason: "引用他人附件", EvidenceAttachmentIDs: []int{foreign}}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("复核引用他人附件应 403：%v", err)
	}
}

// 历史纯文本 evidence_ref 不受影响：既不改写，也不会被解析成附件 ID。
func TestAfterSaleEvidenceLegacyTextRefUnaffected(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_evidence_legacy")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := saveCustomerOrFail(t, db, 11)
	order := seedCustomerOrder(t, db, "ev-legacy", customer.ID, esportsorder.FulfillmentStatusCompleted)

	item, err := createCustomerAfterSale(ctx, db, customer, afterSaleInput{
		SourceID: order.ID, Kind: "refund_request", Demand: "历史文本凭证",
		EvidenceRef: "oss://evidence/1.mp4", Contact: "wx-legacy",
	}, now)
	if err != nil {
		t.Fatalf("历史文本证据售后应成功：%v", err)
	}
	if item.EvidenceRef != "oss://evidence/1.mp4" {
		t.Fatalf("历史文本 evidence_ref 不应被改写：%+v", item)
	}
	if ids, err := parseAfterSaleEvidenceRef(item.EvidenceRef); err != nil || len(ids) != 0 {
		t.Fatalf("历史文本不应解析出附件 ID：%v %v", ids, err)
	}
}

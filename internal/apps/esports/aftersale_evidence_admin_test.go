package esports

import (
	"errors"
	"fmt"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// 客服代建售后附件接入（§13.2）：客服引用本人上传的私有附件成功，evidence_ref 序列化为 JSON 数组。
func TestAdminAfterSaleEvidenceOwnAttachment(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_aftersale_evidence_own")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	admin := seedAdmin(t, db, "admin-ev-own")
	order := seedBalancePaidOrder(t, db, "admin-ev-own")
	attachmentID := seedAftersaleEvidenceAttachment(t, db, admin.UID, aftersaleEvidenceDirectory, "image/png")

	item, err := createAfterSaleWithEvidence(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "refund_request", Channel: "admin",
		Demand: "客户反馈服务不符，附凭证", Contact: "wx-1",
	}, []int{attachmentID}, admin.ID, now)
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

// 客服代建售后引用他人附件应 403（§13.2 本人限定）。
func TestAdminAfterSaleEvidenceForeignAttachment(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_aftersale_evidence_foreign")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	admin := seedAdmin(t, db, "admin-ev-foreign")
	other := seedAdmin(t, db, "admin-ev-other")
	order := seedBalancePaidOrder(t, db, "admin-ev-foreign")
	foreign := seedAftersaleEvidenceAttachment(t, db, other.UID, aftersaleEvidenceDirectory, "image/png")

	if _, err := createAfterSaleWithEvidence(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "refund_request", Channel: "admin",
		Demand: "引用他人附件", Contact: "wx-2",
	}, []int{foreign}, admin.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("引用他人附件应 403：%v", err)
	}
}

// 客服代建售后：历史纯文本 evidence_ref 不受附件解析影响，不被改写也不被解析出附件 ID。
func TestAdminAfterSaleEvidenceLegacyTextRef(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_aftersale_evidence_legacy")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	admin := seedAdmin(t, db, "admin-ev-legacy")
	order := seedBalancePaidOrder(t, db, "admin-ev-legacy")

	item, err := createAfterSaleWithEvidence(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "refund_request", Channel: "admin",
		Demand: "历史文本凭证", EvidenceRef: "oss://evidence/1.mp4", Contact: "wx-legacy",
	}, nil, admin.ID, now)
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

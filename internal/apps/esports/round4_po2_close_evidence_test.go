package esports

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsservicesegment"
)

// ── 第 2 波：结单凭证必看（3.4-03）与按订单类型绑定模板（3.4-07） ──

// po2SeedCloseableOrderWithEvidence 造一张待结单订单，服务段凭证为真实图片附件 ID（JSON 数组）。
func po2SeedCloseableOrderWithEvidence(t *testing.T, db *ent.Client, orderNo string,
	payable int64, adminID, evidenceID int) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	order := db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(payable).SetPayableCents(payable).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).SetResponsibleAdminID(adminID).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).
		SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).
		SetSystemSeconds(600).SetReportedUnits(1).
		SetEvidenceRef(jsonIntArray(evidenceID)).SaveX(ctx)
	return order
}

// jsonIntArray 拼出 evidence_ref 的 JSON 数组形态（陪玩上传图片的附件 ID）。
func jsonIntArray(ids ...int) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.Itoa(id))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// ：结单条件返回待查看凭证附件；结单必须回传覆盖全部附件的已查看列表并留痕。
func TestRound4W2CloseRequiresEvidenceViewed(t *testing.T) {
	db := openCloseFormDB(t, "esports_po2_close_evidence")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	evidence := seedEvidenceAttachment(t, db, "user:player-1", serviceEvidenceDirectory, "image/png")
	order := po2SeedCloseableOrderWithEvidence(t, db, "po2-close-1", 2000, 11, evidence.ID)

	status, err := orderCloseConditionStatus(ctx, db, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.EvidenceAttachmentIDs) != 1 || status.EvidenceAttachmentIDs[0] != evidence.ID {
		t.Fatalf("结单条件未返回待查看凭证附件：%+v", status.EvidenceAttachmentIDs)
	}

	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); !errors.Is(err, errInvalidInput) ||
		!strings.Contains(err.Error(), "查看") {
		t.Fatalf("未确认已查看凭证应被拒：%v", err)
	}
	if _, err = closeOrder(ctx, db, order.ID, 11,
		closeOrderInput{Note: "结单", ViewedEvidenceAttachmentIDs: []int{999999}}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("回传无关附件 ID 仍应被拒：%v", err)
	}

	closed, err := closeOrder(ctx, db, order.ID, 11,
		closeOrderInput{Note: "结单", ViewedEvidenceAttachmentIDs: []int{evidence.ID}}, now)
	if err != nil {
		t.Fatalf("已确认查看后应可结单：%v", err)
	}
	if closed.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted {
		t.Fatalf("订单未结单：%s", closed.FulfillmentStatus)
	}
	event := db.EsportsOrderEvent.Query().
		Where(esportsorderevent.EventTypeEQ("order.closed")).OnlyX(ctx)
	if event.Details["viewed_by_admin_id"] == nil {
		t.Fatalf("结单事件未落操作人：%+v", event.Details)
	}
	ids, ok := event.Details["viewed_evidence_attachment_ids"].([]interface{})
	if !ok || len(ids) != 1 {
		t.Fatalf("结单事件未落已查看附件：%+v", event.Details)
	}
	if got, _ := ids[0].(float64); int(got) != evidence.ID {
		t.Fatalf("结单事件已查看附件不正确：%+v", ids)
	}
}

// ：按订单类型（即时 / 预约）绑定结单表单模板，预约单命中、即时单不命中。
func TestRound4W2CloseFormScopeOrderType(t *testing.T) {
	db := openCloseFormDB(t, "esports_po2_close_order_type")
	defer db.Close()
	poSeedBaseConfigs(t, db)
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	if _, err := createCloseFormField(ctx, db, closeFormFieldInput{
		Key: "po2_request_result", Label: "预约结单结果", FieldType: "text",
	}); err != nil {
		t.Fatal(err)
	}
	tpl, err := createCloseFormTemplate(ctx, db, closeFormTemplateInput{
		Code: "PO2-REQUEST", Name: "预约单结单表单", Scope: "order_type", ScopeKey: "request",
		Fields: []schema.CloseFormFieldBinding{{FieldKey: "po2_request_result", Required: true, Sort: 1}},
	}, 11)
	if err != nil {
		t.Fatalf("按订单类型创建模板失败：%v", err)
	}
	if _, err = activateCloseFormTemplate(ctx, db, tpl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = createCloseFormTemplate(ctx, db, closeFormTemplateInput{
		Code: "PO2-BAD-KEY", Name: "非法范围", Scope: "order_type", ScopeKey: "whenever",
		Fields: []schema.CloseFormFieldBinding{{FieldKey: "po2_request_result", Sort: 1}},
	}, 11); !errors.Is(err, errInvalidInput) {
		t.Fatalf("order_type 非法 scope_key 应被拒：%v", err)
	}

	customer := saveCustomerOrFail(t, db, 11)
	sku := po2SeedSku(t, db, "PO2-CF-SKU", 1, 1, 0)
	startAt := now.Add(24 * time.Hour)
	requestOrder, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, RequestedStartAt: &startAt,
	}, "po2-ot-key-001", now)
	if err != nil {
		t.Fatal(err)
	}
	view, err := orderCloseForm(ctx, db, requestOrder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Template == nil || view.Template.Code != "PO2-REQUEST" {
		t.Fatalf("预约单未命中 order_type=request 模板：%+v", view.Template)
	}

	instant, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
	}, "po2-ot-key-002", now)
	if err != nil {
		t.Fatal(err)
	}
	instantView, err := orderCloseForm(ctx, db, instant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if instantView.Template != nil && instantView.Template.Code == "PO2-REQUEST" {
		t.Fatalf("即时单不应命中预约模板：%+v", instantView.Template)
	}
}

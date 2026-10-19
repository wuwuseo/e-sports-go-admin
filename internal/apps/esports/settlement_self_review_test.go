package esports

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderaddon"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsservicesegment"
)

// closedSnapshot 是结单快照（order.closed 事件 details）里与结单净额相关的字段。
type closedSnapshot struct {
	CloseNetService int64 `json:"close_net_service_cents"`
	AddonRevenue    int64 `json:"addon_revenue_cents"`
	AddonCommission int64 `json:"addon_commission_cents"`
	CloseNetTotal   int64 `json:"close_net_total_cents"`
}

// closedSnapshotOf 读取订单结单快照。
func closedSnapshotOf(t *testing.T, db *ent.Client, orderID int) closedSnapshot {
	t.Helper()
	event := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(orderID), esportsorderevent.EventTypeEQ("order.closed"),
	).OnlyX(t.Context())
	raw, err := json.Marshal(event.Details)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot closedSnapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// P1-16：结单快照须对称纳入加钟收入（只算已收款加钟的实收额）与对应加钟分成。
func TestCloseSnapshotIncludesAddonRevenue(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_addon_net")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	order := seedSimpleCloseableOrder(t, db, "addon-net-1", 3000, 11)
	// 已收款加钟：实收 4000 分，确认时快照的分成 1600 分。
	db.EsportsOrderAddon.Create().SetAddonNo("AO-1").SetOrderID(order.ID).
		SetAddedCents(4000).SetStatus(esportsorderaddon.StatusConfirmed).
		SetPaymentStatus(esportsorderaddon.PaymentStatusPaid).SetPaidCents(4000).
		SetCommissionCents(1600).SaveX(ctx)
	// 已取消（未收款）的加钟：收入与成本两边都不计（与 report.go 的加钟口径一致）。
	// 不能造「待确认 / 已确认未收款」的加钟——那会被结单条件「未处理加钟数 = 0」先拦掉。
	db.EsportsOrderAddon.Create().SetAddonNo("AO-2").SetOrderID(order.ID).
		SetAddedCents(2000).SetStatus(esportsorderaddon.StatusCancelled).
		SetPaymentStatus(esportsorderaddon.PaymentStatusUnpaid).SetCommissionCents(800).SaveX(ctx)

	closed, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "含加钟结单"}, now)
	if err != nil {
		t.Fatalf("含已收款加钟的订单应可结单：%v", err)
	}
	if closed.CloseNetServiceCents == nil || *closed.CloseNetServiceCents != 3000 {
		t.Fatalf("close_net_service_cents 语义应保持「订单本体净服务额」：%+v", closed.CloseNetServiceCents)
	}
	snapshot := closedSnapshotOf(t, db, order.ID)
	if snapshot.AddonRevenue != 4000 || snapshot.AddonCommission != 1600 {
		t.Fatalf("结单快照应体现加钟实收 4000 / 加钟分成 1600：%+v", snapshot)
	}
	if snapshot.CloseNetService != 3000 || snapshot.CloseNetTotal != 7000 {
		t.Fatalf("合计净额应为 本体 3000 + 加钟 4000 = 7000：%+v", snapshot)
	}
}

// P1-16 回归：不含加钟的订单，结单净额与加钟两项均为 0 / 原值不变。
func TestCloseSnapshotWithoutAddonUnchanged(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_addon_net_plain")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	order := seedSimpleCloseableOrder(t, db, "addon-net-2", 3000, 11)
	closed, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "无加钟结单"}, now)
	if err != nil {
		t.Fatalf("无加钟订单应可结单：%v", err)
	}
	if closed.CloseNetServiceCents == nil || *closed.CloseNetServiceCents != 3000 {
		t.Fatalf("无加钟时结单净服务额不应变化：%+v", closed.CloseNetServiceCents)
	}
	snapshot := closedSnapshotOf(t, db, order.ID)
	if snapshot.AddonRevenue != 0 || snapshot.AddonCommission != 0 {
		t.Fatalf("无加钟时加钟口径应为 0：%+v", snapshot)
	}
	if snapshot.CloseNetService != 3000 || snapshot.CloseNetTotal != 3000 {
		t.Fatalf("无加钟时合计净额应等于本体净额：%+v", snapshot)
	}
}

// seedEvidenceImageOrder 建一张待结单订单，逐席位写入 evidence_ref（图片附件 ID 的 JSON 数组）。
func seedEvidenceImageOrder(t *testing.T, db *ent.Client, orderNo string, refs []string, adminID int) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	order := db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(2000).SetPayableCents(2000).
		SetPromisedServiceUnits(1).SetRequiredSeats(len(refs)).SetResponsibleAdminID(adminID).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	for i, ref := range refs {
		db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(100 + i).
			SetSeatIndex(i + 1).SetStatus(esportsservicesegment.StatusFinished).
			SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).
			SetSystemSeconds(600).SetReportedUnits(1).SetEvidenceRef(ref).SaveX(ctx)
	}
	return order
}

// P0-1：结单表单读取视图须透出该订单全部图片附件 ID，供两端前端逐张确认后回传；
// 字段命名与 GET /admin/orders/{id}/close-conditions 的 evidence_attachment_ids 保持一致。
func TestOrderCloseFormExposesEvidenceAttachmentIDs(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_form_evidence_ids")
	defer db.Close()
	ctx := t.Context()

	order := seedEvidenceImageOrder(t, db, "form-evidence", []string{"[101]", "[102,103]"}, 11)
	view, err := orderCloseForm(ctx, db, order.ID)
	if err != nil {
		t.Fatalf("获取结单配置失败：%v", err)
	}
	want, err := serviceEvidenceAttachmentIDs(ctx, db, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(view.EvidenceAttachmentIDs) != "[101 102 103]" ||
		fmt.Sprint(view.EvidenceAttachmentIDs) != fmt.Sprint(want) {
		t.Fatalf("结单视图应透出全部图片附件 ID（升序去重，与 close-conditions 同口径）：got=%v want=%v",
			view.EvidenceAttachmentIDs, want)
	}
	// 纯文本凭证：为空，前端不需要逐张确认。
	textOrder := seedEvidenceImageOrder(t, db, "form-evidence-text", []string{"oss://shot-1"}, 11)
	textView, err := orderCloseForm(ctx, db, textOrder.ID)
	if err != nil {
		t.Fatalf("获取结单配置失败：%v", err)
	}
	if len(textView.EvidenceAttachmentIDs) != 0 {
		t.Fatalf("纯文本凭证不应要求逐张确认：%v", textView.EvidenceAttachmentIDs)
	}
}

// P0-1：缺一张「已查看」必须被拒，且中文提示要明确指出还差哪张附件。
func TestCloseEvidenceViewedMissingAttachment(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_evidence_missing")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	order := seedEvidenceImageOrder(t, db, "ev-missing", []string{"[101]", "[102]"}, 11)
	_, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "只确认看过一张",
		ViewedEvidenceAttachmentIDs: []int{101}}, now)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("少看一张凭证图片应被拒：%v", err)
	}
	if !strings.Contains(err.Error(), "102") || strings.Contains(err.Error(), "101、") {
		t.Fatalf("提示应只列出未查看的附件 102：%v", err)
	}
	if kept := db.EsportsOrder.GetX(ctx, order.ID); kept.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		t.Fatalf("被拒后订单应保持待结单：%+v", kept.FulfillmentStatus)
	}
	// 不传「已查看」列表同样不得放行（不能放宽成「不传就通过」）。
	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "不传已查看"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("不传已查看附件应被拒：%v", err)
	}
}

// P0-1：全部给齐且乱序 / 带重复 / 带无关 ID 时通过，留痕只记需查看的附件集合。
func TestCloseEvidenceViewedUnorderedAndDedup(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_evidence_unordered")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	order := seedEvidenceImageOrder(t, db, "ev-unordered", []string{"[101]", "[102]", "[103]"}, 11)
	closed, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "逐张看过",
		ViewedEvidenceAttachmentIDs: []int{103, 101, 103, 999, 102}}, now)
	if err != nil {
		t.Fatalf("全部覆盖（乱序 / 重复 / 无关 ID）时应结单成功：%v", err)
	}
	if closed.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted {
		t.Fatalf("应已完成：%+v", closed.FulfillmentStatus)
	}
	event := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID), esportsorderevent.EventTypeEQ("order.closed"),
	).OnlyX(ctx)
	raw, err := json.Marshal(event.Details)
	if err != nil {
		t.Fatal(err)
	}
	var details struct {
		Viewed []int `json:"viewed_evidence_attachment_ids"`
		ByID   int   `json:"viewed_by_admin_id"`
	}
	if err = json.Unmarshal(raw, &details); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(details.Viewed) != "[101 102 103]" || details.ByID != 11 {
		t.Fatalf("留痕应只含需查看的附件与操作人：%s", raw)
	}
}

// P0-1：纯文本凭证（无图片附件）时不传「已查看」也能结单。
func TestCloseEvidenceViewedSkippedWithoutImages(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_evidence_textonly")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	order := seedEvidenceImageOrder(t, db, "ev-text", []string{"oss://shot-1"}, 11)
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "纯文本凭证"}, now); err != nil {
		t.Fatalf("无图片附件时不传已查看列表应可结单：%v", err)
	}
}

// P1：负责客服为空（自助单 / 公共池单）时，不得变成「任何管理员都能复核自己结的单」。
func TestCloseReviewSelfCheckWithoutResponsibleAdmin(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_review_no_owner")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveApprovalFlow(t, db, closeApprovalFlowInput{Code: "AF-NO-OWNER", Name: "单节点复核",
		Scope: "global", MinCents: 1000, Nodes: []schema.CloseApprovalNode{
			{Seq: 1, Name: "主管复核", Role: "admin"},
		}})

	order := seedSimpleCloseableOrder(t, db, "self-review-1", 2000, 11)
	db.EsportsOrder.UpdateOneID(order.ID).ClearResponsibleAdminID().SaveX(ctx)
	reviewing, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now)
	if err != nil || reviewing.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCloseReview {
		t.Fatalf("应进入待复核：%v %+v", err, reviewing)
	}
	// 与订单无关的普通管理员不在可见范围内，不得复核。
	if _, err = approveCloseReview(ctx, db, order.ID, 12, false, "无负责客服时越权复核", now); !errors.Is(err, errForbidden) {
		t.Fatalf("负责客服为空时无关管理员复核应被拒：%v", err)
	}
	if kept := db.EsportsOrder.GetX(ctx, order.ID); kept.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCloseReview {
		t.Fatalf("被拒后应仍待复核：%+v", kept.FulfillmentStatus)
	}
	// 超级管理员可复核。
	done, err := approveCloseReview(ctx, db, order.ID, 13, true, "超管复核通过", now)
	if err != nil || done.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted {
		t.Fatalf("超管复核应通过：%v %+v", err, done)
	}

	// 驳回路径同样收严。
	second := seedSimpleCloseableOrder(t, db, "self-review-2", 2000, 11)
	db.EsportsOrder.UpdateOneID(second.ID).ClearResponsibleAdminID().SaveX(ctx)
	if _, err = closeOrder(ctx, db, second.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("第二单应进入待复核：%v", err)
	}
	if _, err = rejectCloseReview(ctx, db, second.ID, 12, false, "越权驳回", now); !errors.Is(err, errForbidden) {
		t.Fatalf("负责客服为空时无关管理员驳回应被拒：%v", err)
	}
	if _, err = rejectCloseReview(ctx, db, second.ID, 13, true, "超管驳回", now); err != nil {
		t.Fatalf("超管驳回应通过：%v", err)
	}
}

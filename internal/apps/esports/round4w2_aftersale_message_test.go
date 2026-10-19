package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersalemessage"
	"go-admin/internal/ent/esportsorder"
)

// ar4SeedAfterSale 建一张可控证据 / 状态的售后单（§13.3-04 / §13.7-03 用）。
func ar4SeedAfterSale(t *testing.T, db *ent.Client, order *ent.EsportsOrder, customerID int,
	kind, evidenceRef string, status esportsaftersale.Status, now time.Time) *ent.EsportsAfterSale {
	t.Helper()
	return db.EsportsAfterSale.Create().SetAfterSaleNo(afterSaleNo()).
		SetSourceType(esportsaftersale.SourceTypeOrder).SetSourceID(order.ID).
		SetKind(esportsaftersale.Kind(kind)).SetChannel(esportsaftersale.ChannelCustomer).
		SetDemand("客户反馈问题，申请处理").SetEvidenceRef(evidenceRef).
		SetStatus(status).SetCustomerID(customerID).SaveX(t.Context())
}

// §13.3-04：客服回复后该售后单可查到该条沟通，客户侧可见 visibility=all，internal 备注不可见。
func TestAfterSaleMessageAdminReplyVisibleToCustomerAndInternalHidden(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_message_flow")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)
	order := ar4SeedOrder(t, db, "MSG", 2000, 2000, 0, 0, 0, 1,
		esportsorder.FulfillmentStatusPendingDispatch, nil)
	item := ar4SeedAfterSale(t, db, order, customer.ID, "refund_request", "",
		esportsaftersale.StatusPendingAccept, now)

	reply, err := postAdminAfterSaleMessage(ctx, db, item, 11, "客服小李", afterSaleMessageInput{
		Content: "已受理，请在 24 小时内补充服务截图", Visibility: "all",
	}, now)
	if err != nil {
		t.Fatalf("客服回复失败：%v", err)
	}
	if reply.AuthorType != esportsaftersalemessage.AuthorTypeAdmin || reply.AuthorName != "客服小李" {
		t.Fatalf("回复应记录发送方身份快照：%+v", reply)
	}
	if _, err = postAdminAfterSaleMessage(ctx, db, item, 11, "客服小李", afterSaleMessageInput{
		Content: "内部备注：疑似恶意索赔", Visibility: "internal",
	}, now.Add(time.Minute)); err != nil {
		t.Fatalf("客服内部备注失败：%v", err)
	}
	all, err := listAfterSaleMessages(ctx, db, item.ID, true, 100)
	if err != nil || len(all) != 2 {
		t.Fatalf("客服侧应看到全部沟通：%v %d", err, len(all))
	}
	visible, err := listAfterSaleMessages(ctx, db, item.ID, false, 100)
	if err != nil || len(visible) != 1 {
		t.Fatalf("客户侧只应看到 visibility=all 的沟通：%v %d", err, len(visible))
	}
	if visible[0].Content != "已受理，请在 24 小时内补充服务截图" {
		t.Fatalf("客户侧可见内容错误：%q", visible[0].Content)
	}

	// 客户回复（带附件）→ 记录归属本人，且默认客户可见。
	replyByCustomer, err := postCustomerAfterSaleMessage(ctx, db, customer.ID, item.ID,
		afterSaleMessageInput{Content: "截图已上传", Attachments: []string{"oss://aftersale/1.png"}}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("客户回复失败：%v", err)
	}
	if replyByCustomer.AuthorType != esportsaftersalemessage.AuthorTypeCustomer ||
		replyByCustomer.Visibility != esportsaftersalemessage.VisibilityAll {
		t.Fatalf("客户回复应为客户可见：%+v", replyByCustomer)
	}
	visible, err = listAfterSaleMessages(ctx, db, item.ID, false, 100)
	if err != nil || len(visible) != 2 {
		t.Fatalf("客户回复后客户侧应看到 2 条：%v %d", err, len(visible))
	}

	// 客户不得写内部备注（不得静默降级为客户可见）。
	if _, err = postCustomerAfterSaleMessage(ctx, db, customer.ID, item.ID,
		afterSaleMessageInput{Content: "我也想写内部备注", Visibility: "internal"}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("客户写内部备注应被拒，实际 err=%v", err)
	}
	// 他人售后单不可回复。
	if _, err = postCustomerAfterSaleMessage(ctx, db, customer.ID+99, item.ID,
		afterSaleMessageInput{Content: "越权回复"}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非本人售后单回复应被拒，实际 err=%v", err)
	}
	// 空内容明确拒绝。
	if _, err = postCustomerAfterSaleMessage(ctx, db, customer.ID, item.ID,
		afterSaleMessageInput{Content: "   "}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("空沟通内容应被拒，实际 err=%v", err)
	}
	// 已结束的售后单不可继续沟通。
	db.EsportsAfterSale.UpdateOneID(item.ID).SetStatus(esportsaftersale.StatusCompleted).ExecX(ctx)
	if _, err = postCustomerAfterSaleMessage(ctx, db, customer.ID, item.ID,
		afterSaleMessageInput{Content: "还能回复吗"}, now); !errors.Is(err, errConflict) {
		t.Fatalf("已结束售后单继续沟通应被拒，实际 err=%v", err)
	}
	// 沟通记录只增不改：追加后历史条数只增不减。
	rows, err := db.EsportsAfterSaleMessage.Query().Count(ctx)
	if err != nil || rows != 3 {
		t.Fatalf("沟通记录应只增（1 客服可见 + 1 内部备注 + 1 客户回复，其余写入均被拒），实际 %d", rows)
	}
}

// §13.7-03：客服可对缺证售后单发起补证提示，客户侧能查到提示与待补证标记；
// 客户回复带附件后待补证标记解除；已结束单据明确拒绝。
func TestAfterSaleEvidenceRequestVisibleAndResolvedByAttachment(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_evidence_request")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)
	order := ar4SeedOrder(t, db, "EVID", 2000, 2000, 0, 0, 0, 1,
		esportsorder.FulfillmentStatusPendingDispatch, nil)
	item := ar4SeedAfterSale(t, db, order, customer.ID, "refund_request", "",
		esportsaftersale.StatusPendingAccept, now)

	before, err := afterSaleEvidenceStatusOf(ctx, db, customer.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.PendingEvidence || before.RequestedAt != nil {
		t.Fatalf("未发起补证前不应有待补证标记：%+v", before)
	}

	if _, err = requestAfterSaleEvidence(ctx, db, item.ID, 11, "客服小李",
		afterSaleEvidenceRequestInput{Note: "  "}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("空补证说明应被拒，实际 err=%v", err)
	}
	message, err := requestAfterSaleEvidence(ctx, db, item.ID, 11, "客服小李",
		afterSaleEvidenceRequestInput{Note: "请补充服务时长截图"}, now)
	if err != nil {
		t.Fatalf("发起补证提示失败：%v", err)
	}
	if !strings.Contains(message.Content, "请补充服务时长截图") {
		t.Fatalf("补证提示文案错误：%q", message.Content)
	}
	// 客户侧可见该提示（客户可见列表 + 待补证标记）。
	visible, err := listAfterSaleMessages(ctx, db, item.ID, false, 100)
	if err != nil || len(visible) != 1 {
		t.Fatalf("补证提示应对客户可见：%v %d", err, len(visible))
	}
	status, err := afterSaleEvidenceStatusOf(ctx, db, customer.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.PendingEvidence || status.RequestNote != "请补充服务时长截图" || status.RequestedAt == nil {
		t.Fatalf("客户侧应看到待补证标记与说明：%+v", status)
	}
	if status.EvidencePresent {
		t.Fatalf("该售后单本就没有凭证，evidence_present 应为 false：%+v", status)
	}

	// 客户仅文字回复不解除标记；带附件回复才视为已补证。
	if _, err = postCustomerAfterSaleMessage(ctx, db, customer.ID, item.ID,
		afterSaleMessageInput{Content: "好的，稍等"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	status, err = afterSaleEvidenceStatusOf(ctx, db, customer.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.PendingEvidence {
		t.Fatalf("客户未提供附件时仍应待补证：%+v", status)
	}
	if _, err = postCustomerAfterSaleMessage(ctx, db, customer.ID, item.ID,
		afterSaleMessageInput{Content: "截图已上传", Attachments: []string{"oss://aftersale/2.png"}},
		now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	status, err = afterSaleEvidenceStatusOf(ctx, db, customer.ID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.PendingEvidence {
		t.Fatalf("客户已回复附件后不应再待补证：%+v", status)
	}

	// 越权读取明确拒绝。
	if _, err = afterSaleEvidenceStatusOf(ctx, db, customer.ID+99, item.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("非本人售后单查询补证状态应被拒，实际 err=%v", err)
	}
	// 已结束售后单不可再发起补证。
	db.EsportsAfterSale.UpdateOneID(item.ID).SetStatus(esportsaftersale.StatusRejected).ExecX(ctx)
	if _, err = requestAfterSaleEvidence(ctx, db, item.ID, 11, "客服小李",
		afterSaleEvidenceRequestInput{Note: "请补充凭证"}, now); !errors.Is(err, errConflict) {
		t.Fatalf("已结束售后单发起补证应被拒，实际 err=%v", err)
	}
}

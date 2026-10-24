package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersalereview"
	"go-admin/internal/ent/esportscommissionentry"
)

// 终态售后新证据复核：仅终态可申请、同单只允许一张进行中复核、异人审查、发回重处理可重新走流程。
func TestAfterSaleReviewReopensRejectedTicket(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_review_reopen")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	order := seedBalancePaidOrder(t, db, "review-reopen")
	item := newRefundAfterSale(t, db, order.ID, "refund_request", 800, 11, 22, now)

	// 进行中的售后直接改方案即可，不接受复核申请。
	if _, err := requestAfterSaleReview(ctx, db, item.ID, 11,
		afterSaleReviewInput{Reason: "客户补充录屏"}, now); !errors.Is(err, errConflict) {
		t.Fatalf("非终态售后接受了复核申请：%v", err)
	}
	if _, err := rejectAfterSale(ctx, db, item.ID, 22, afterSaleDecisionInput{Reason: "证据不足"}, now); err != nil {
		t.Fatal(err)
	}

	review, err := requestAfterSaleReview(ctx, db, item.ID, 11,
		afterSaleReviewInput{Reason: "客户补充了录屏", EvidenceRef: "oss://evidence/1.mp4"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if review.Status != esportsaftersalereview.StatusPending ||
		review.OrderID != order.ID || review.ApplicantAdminID != 11 ||
		review.EvidenceRef != "oss://evidence/1.mp4" {
		t.Fatalf("复核申请记录不正确：%+v", review)
	}
	// 同一售后单同一时刻只允许一张进行中复核。
	if _, err := requestAfterSaleReview(ctx, db, item.ID, 11,
		afterSaleReviewInput{Reason: "再来一次"}, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复复核申请未拦截：%v", err)
	}
	// 申请人不得自审。
	if _, err := decideAfterSaleReview(ctx, db, review.ID, 11,
		afterSaleReviewDecisionInput{Decision: "reopen", Note: "自己重开"}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("申请人自审未被拦截：%v", err)
	}
	// 原审批人（22）同样不得推翻自己的结论。
	if _, err := decideAfterSaleReview(ctx, db, review.ID, 22,
		afterSaleReviewDecisionInput{Decision: "reopen", Note: "改判"}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("原审批人审查未被拦截：%v", err)
	}
	// 非法结论被拦截。
	if _, err := decideAfterSaleReview(ctx, db, review.ID, 33,
		afterSaleReviewDecisionInput{Decision: "approve", Note: "乱填"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法复核结论未拦截：%v", err)
	}

	decided, err := decideAfterSaleReview(ctx, db, review.ID, 33,
		afterSaleReviewDecisionInput{Decision: "reopen", Note: "新证据成立"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if decided.Status != esportsaftersalereview.StatusReopened ||
		decided.ReviewerAdminID == nil || *decided.ReviewerAdminID != 33 || decided.ReviewedAt == nil {
		t.Fatalf("复核结论记录不正确：%+v", decided)
	}
	// 售后单回到处理中，允许重新提交方案。
	reopened := db.EsportsAfterSale.GetX(ctx, item.ID)
	if reopened.Status != esportsaftersale.StatusProcessing || reopened.ResolvedAt != nil {
		t.Fatalf("发回重处理后售后单状态不正确：%+v", reopened)
	}
	if _, err := submitAfterSalePlan(ctx, db, item.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: 800, Resolution: "复核后按差额退款",
	}, now); err != nil {
		t.Fatalf("发回重处理后无法重新提交方案：%v", err)
	}
	// 已结论的复核不能重复审查或撤回。
	if _, err := decideAfterSaleReview(ctx, db, review.ID, 44,
		afterSaleReviewDecisionInput{Decision: "uphold"}, now); !errors.Is(err, errConflict) {
		t.Fatalf("已结论复核被重复审查：%v", err)
	}
	if _, err := withdrawAfterSaleReview(ctx, db, review.ID, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("已结论复核被撤回：%v", err)
	}
	events := countEventsByType(t, db)
	if events["order.after_sale_review_requested"] != 1 || events["order.after_sale_review_decided"] != 1 {
		t.Fatalf("复核事件留痕不正确：%+v", events)
	}
}

// 复核维持原结论与撤回：维持不改售后单状态；撤回只允许申请人操作。
func TestAfterSaleReviewUpholdAndWithdraw(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_review_uphold")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	order := seedBalancePaidOrder(t, db, "review-uphold")
	item := newRefundAfterSale(t, db, order.ID, "refund_request", 600, 11, 22, now)
	if _, err := rejectAfterSale(ctx, db, item.ID, 22, afterSaleDecisionInput{Reason: "证据不足"}, now); err != nil {
		t.Fatal(err)
	}
	review, err := requestAfterSaleReview(ctx, db, item.ID, 11, afterSaleReviewInput{Reason: "复核"}, now)
	if err != nil {
		t.Fatal(err)
	}
	// 非申请人不得撤回。
	if _, err := withdrawAfterSaleReview(ctx, db, review.ID, 33, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非申请人撤回了复核申请：%v", err)
	}
	upheld, err := decideAfterSaleReview(ctx, db, review.ID, 33,
		afterSaleReviewDecisionInput{Decision: "uphold", Note: "证据不足以改判"}, now)
	if err != nil || upheld.Status != esportsaftersalereview.StatusUpheld {
		t.Fatalf("维持原结论失败：%v %+v", err, upheld)
	}
	if got := db.EsportsAfterSale.GetX(ctx, item.ID); got.Status != esportsaftersale.StatusRejected {
		t.Fatalf("维持原结论不应改变售后单状态：%+v", got)
	}

	// 撤回路径：另一张单的复核申请由申请人本人撤回。
	other := newRefundAfterSale(t, db, order.ID, "duration_short", 400, 11, 22, now)
	if _, err := rejectAfterSale(ctx, db, other.ID, 22, afterSaleDecisionInput{Reason: "证据不足"}, now); err != nil {
		t.Fatal(err)
	}
	pending, err := requestAfterSaleReview(ctx, db, other.ID, 11, afterSaleReviewInput{Reason: "复核"}, now)
	if err != nil {
		t.Fatal(err)
	}
	withdrawn, err := withdrawAfterSaleReview(ctx, db, pending.ID, 11, now)
	if err != nil || withdrawn.Status != esportsaftersalereview.StatusWithdrawn {
		t.Fatalf("撤回复核申请失败：%v %+v", err, withdrawn)
	}
	// 撤回后可再次申请。
	if _, err := requestAfterSaleReview(ctx, db, other.ID, 11, afterSaleReviewInput{Reason: "再次复核"}, now); err != nil {
		t.Fatalf("撤回后无法再次申请：%v", err)
	}
}

// 分成已结算 / 已入结算单的订单不允许复核改判，避免退款无法冲回已结算分成。
func TestAfterSaleReviewReopenBlockedWhenCommissionSettled(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_review_settled")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	order := seedBalancePaidOrder(t, db, "review-settled")
	item := newRefundAfterSale(t, db, order.ID, "refund_request", 700, 11, 22, now)
	if _, err := rejectAfterSale(ctx, db, item.ID, 22, afterSaleDecisionInput{Reason: "证据不足"}, now); err != nil {
		t.Fatal(err)
	}
	db.EsportsCommissionEntry.Create().SetIdemKey("commission:service:review-settled:1").
		SetOrderID(order.ID).SetPlayerID(9001).
		SetKind(esportscommissionentry.KindService).
		SetStatus(esportscommissionentry.StatusSettled).SaveX(ctx)

	review, err := requestAfterSaleReview(ctx, db, item.ID, 11, afterSaleReviewInput{Reason: "复核"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decideAfterSaleReview(ctx, db, review.ID, 33,
		afterSaleReviewDecisionInput{Decision: "reopen", Note: "改判"}, now); !errors.Is(err, errConflict) {
		t.Fatalf("已结算分成的订单被允许复核改判：%v", err)
	}
	if got := db.EsportsAfterSale.GetX(ctx, item.ID); got.Status != esportsaftersale.StatusRejected {
		t.Fatalf("改判失败后售后单状态被改动：%+v", got)
	}
	if got := db.EsportsAfterSaleReview.GetX(ctx, review.ID); got.Status != esportsaftersalereview.StatusPending {
		t.Fatalf("改判失败后复核申请状态被改动：%+v", got)
	}
}

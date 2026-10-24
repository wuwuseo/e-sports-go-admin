package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersalereview"
	"go-admin/internal/ent/esportscommissionentry"
)

// 复核结论：维持原结论 / 发回重新处理。
const (
	reviewDecisionUphold = "uphold"
	reviewDecisionReopen = "reopen"
)

type afterSaleReviewInput struct {
	Reason      string `json:"reason"`
	EvidenceRef string `json:"evidence_ref"`
	// EvidenceAttachmentIDs 复核附带凭证附件 ID 列表：须为申请人本人上传的私有附件，
	// 非空时校验归属并把引用序列化为 evidence_ref 的 JSON 数组。
	EvidenceAttachmentIDs []int `json:"evidence_attachment_ids"`
}

type afterSaleReviewDecisionInput struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

func afterSaleReviewNo() string { return "AR" + uuid.NewString() }

// requestAfterSaleReview 提交终态售后的新证据复核申请：
// 仅 rejected / completed 的终态售后可申请，同一售后单同一时刻只允许一张进行中复核。
func requestAfterSaleReview(ctx context.Context, db *ent.Client, afterSaleID, adminID int,
	in afterSaleReviewInput, now time.Time) (*ent.EsportsAfterSaleReview, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	in.EvidenceRef = strings.TrimSpace(in.EvidenceRef)
	if afterSaleID < 1 || adminID < 1 || in.Reason == "" || len([]rune(in.Reason)) > 500 {
		return nil, errInvalidInput
	}
	// ：复核附带凭证附件时按申请人（客服本人）校验归属，并把引用序列化进 evidence_ref。
	if len(in.EvidenceAttachmentIDs) > 0 {
		staff, err := db.Admin.Get(ctx, adminID)
		if err != nil {
			return nil, err
		}
		if _, err := resolveAfterSaleEvidence(ctx, db, in.EvidenceAttachmentIDs, staff.UID); err != nil {
			return nil, err
		}
		ref, err := afterSaleEvidenceRef(in.EvidenceAttachmentIDs)
		if err != nil {
			return nil, err
		}
		in.EvidenceRef = ref
	}
	if len([]rune(in.EvidenceRef)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsAfterSale.Get(ctx, afterSaleID)
	if err != nil {
		return nil, err
	}
	// 只有终态售后需要复核通道：进行中的售后直接改方案即可。
	if item.Status != esportsaftersale.StatusRejected && item.Status != esportsaftersale.StatusCompleted {
		return nil, errConflict
	}
	duplicated, err := tx.EsportsAfterSaleReview.Query().Where(
		esportsaftersalereview.AfterSaleIDEQ(afterSaleID),
		esportsaftersalereview.StatusEQ(esportsaftersalereview.StatusPending),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if duplicated {
		return nil, errConflict
	}
	order, err := tx.EsportsOrder.Get(ctx, item.SourceID)
	if err != nil {
		return nil, err
	}
	review, err := tx.EsportsAfterSaleReview.Create().SetReviewNo(afterSaleReviewNo()).
		SetAfterSaleID(afterSaleID).SetOrderID(item.SourceID).
		SetApplicantAdminID(adminID).SetReason(in.Reason).SetEvidenceRef(in.EvidenceRef).
		SetStatus(esportsaftersalereview.StatusPending).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	newVersion, err := bumpOrder(ctx, tx, item.SourceID, order.Version, "")
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(item.SourceID).
		SetOrderVersion(newVersion).SetEventType("order.after_sale_review_requested").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"after_sale_no": item.AfterSaleNo, "review_no": review.ReviewNo,
			"reason": in.Reason,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("aftersale.review_requested:" + review.ReviewNo).
		SetEventType("aftersale.review_requested").SetAggregateRef(item.AfterSaleNo).
		SetPayload(map[string]interface{}{
			"after_sale_no": item.AfterSaleNo, "review_no": review.ReviewNo,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSaleReview.Get(ctx, review.ID)
}

// decideAfterSaleReview 异人审查复核申请：
// uphold 维持原结论（售后单保持终态）；reopen 发回重新处理（售后单回到 processing 允许重新提交方案）。
func decideAfterSaleReview(ctx context.Context, db *ent.Client, reviewID, adminID int,
	in afterSaleReviewDecisionInput, now time.Time) (*ent.EsportsAfterSaleReview, error) {
	in.Decision = strings.TrimSpace(in.Decision)
	in.Note = strings.TrimSpace(in.Note)
	if reviewID < 1 || adminID < 1 ||
		(in.Decision != reviewDecisionUphold && in.Decision != reviewDecisionReopen) ||
		len([]rune(in.Note)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	review, err := tx.EsportsAfterSaleReview.Get(ctx, reviewID)
	if err != nil {
		return nil, err
	}
	if review.Status != esportsaftersalereview.StatusPending {
		return nil, errConflict
	}
	// 异人审查：申请人不得自审。发回重处理会推翻原审批结论，故原审批人同样不得审查。
	if review.ApplicantAdminID == adminID {
		return nil, errForbidden
	}
	item, err := tx.EsportsAfterSale.Get(ctx, review.AfterSaleID)
	if err != nil {
		return nil, err
	}
	if item.ApproverAdminID != nil && *item.ApproverAdminID == adminID {
		return nil, errForbidden
	}
	order, err := tx.EsportsOrder.Get(ctx, item.SourceID)
	if err != nil {
		return nil, err
	}
	status := esportsaftersalereview.StatusUpheld
	if in.Decision == reviewDecisionReopen {
		status = esportsaftersalereview.StatusReopened
	}
	changed, err := tx.EsportsAfterSaleReview.Update().Where(
		esportsaftersalereview.IDEQ(reviewID),
		esportsaftersalereview.StatusEQ(esportsaftersalereview.StatusPending),
		esportsaftersalereview.VersionEQ(review.Version),
	).SetStatus(status).SetReviewerAdminID(adminID).SetReviewedAt(now.UTC()).
		SetDecisionNote(in.Note).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if status == esportsaftersalereview.StatusReopened {
		// 已结算 / 已入结算单的分成无法再随退款冲回，这类订单不允许复核改判，转人工财务处理。
		blocked, err := tx.EsportsCommissionEntry.Query().Where(
			esportscommissionentry.OrderIDEQ(item.SourceID),
			esportscommissionentry.Or(
				esportscommissionentry.StatusNEQ(esportscommissionentry.StatusPendingSettlement),
				esportscommissionentry.SettlementIDNotNil(),
			),
		).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if blocked {
			return nil, fmt.Errorf("%w：订单分成已结算，无法复核改判", errConflict)
		}
		// 争议重新开启：重新冻结该订单待结算分成，与首次受理口径一致。
		if _, err = freezeCommissionForOrder(ctx, tx.EsportsCommissionEntry, item.SourceID, nil,
			"售后复核改判："+item.AfterSaleNo, adminID, now); err != nil {
			return nil, err
		}
		changed, err = tx.EsportsAfterSale.Update().Where(
			esportsaftersale.IDEQ(item.ID),
			esportsaftersale.StatusIn(esportsaftersale.StatusCompleted, esportsaftersale.StatusRejected),
		).SetStatus(esportsaftersale.StatusProcessing).
			SetResolution("复核发回重新处理：" + in.Note).
			ClearResolvedAt().AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
	}
	newVersion, err := bumpOrder(ctx, tx, item.SourceID, order.Version, "")
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(item.SourceID).
		SetOrderVersion(newVersion).SetEventType("order.after_sale_review_decided").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"after_sale_no": item.AfterSaleNo, "review_no": review.ReviewNo,
			"decision": in.Decision, "note": in.Note,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSaleReview.Get(ctx, reviewID)
}

// withdrawAfterSaleReview 申请人撤回待审查的复核申请。
func withdrawAfterSaleReview(ctx context.Context, db *ent.Client, reviewID, adminID int,
	now time.Time) (*ent.EsportsAfterSaleReview, error) {
	if reviewID < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	review, err := db.EsportsAfterSaleReview.Get(ctx, reviewID)
	if err != nil {
		return nil, err
	}
	if review.ApplicantAdminID != adminID {
		return nil, errForbidden
	}
	changed, err := db.EsportsAfterSaleReview.Update().Where(
		esportsaftersalereview.IDEQ(reviewID),
		esportsaftersalereview.StatusEQ(esportsaftersalereview.StatusPending),
		esportsaftersalereview.VersionEQ(review.Version),
	).SetStatus(esportsaftersalereview.StatusWithdrawn).SetReviewedAt(now.UTC()).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsAfterSaleReview.Get(ctx, reviewID)
}

// listAfterSaleReviews 查询某售后单的复核历史（按提交倒序）。
func listAfterSaleReviews(ctx context.Context, db *ent.Client, afterSaleID, limit int) ([]*ent.EsportsAfterSaleReview, error) {
	if afterSaleID < 1 {
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	return db.EsportsAfterSaleReview.Query().Where(
		esportsaftersalereview.AfterSaleIDEQ(afterSaleID),
	).Order(ent.Desc(esportsaftersalereview.FieldID)).Limit(limit).All(ctx)
}

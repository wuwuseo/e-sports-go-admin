package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersalemessage"
	"go-admin/internal/ent/esportsoutbox"
)

// 提示补证：缺证不阻断受理，但客服必须能主动「提示补证」，且客户侧看得到这一提示。
//
// 口径：
//   - 客服发起补证 = 一条客户可见的沟通记录（复用，客户在售后单内直接看到）；
//   - 同时写一条 `aftersale.evidence_requested` 外盒留痕，作为「是否待补证」的判定依据与通知事件；
//   - 客户回复带附件的沟通记录即视为已补证，待补证标记随之解除（无需新增 schema 字段）。
const (
	afterSaleEvidenceRequestEventType = "aftersale.evidence_requested"
	afterSaleEvidenceRequestPrefix    = "【提示补证】"
)

// afterSaleEvidenceRequestInput 补证提示入参。
type afterSaleEvidenceRequestInput struct {
	Note string `json:"note"`
}

// afterSaleEvidenceStatus 客户侧「是否待补证」状态。
type afterSaleEvidenceStatus struct {
	AfterSaleID     int        `json:"after_sale_id"`
	AfterSaleNo     string     `json:"after_sale_no"`
	EvidencePresent bool       `json:"evidence_present"`
	PendingEvidence bool       `json:"pending_evidence"`
	RequestNote     string     `json:"request_note"`
	RequestedAt     *time.Time `json:"requested_at"`
}

// requestAfterSaleEvidence 客服发起补证提示：同一事务内写客户可见沟通记录 + 外盒留痕。
// 售后单已落终态时明确失败（不静默成功）；说明文案必填。
func requestAfterSaleEvidence(ctx context.Context, db *ent.Client, id, adminID int, adminName string,
	in afterSaleEvidenceRequestInput, now time.Time) (*ent.EsportsAfterSaleMessage, error) {
	in.Note = strings.TrimSpace(in.Note)
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	if in.Note == "" || len([]rune(in.Note)) > 200 {
		return nil, fmt.Errorf("%w：请填写 1–200 字的补证说明", errInvalidInput)
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !afterSaleStatusActive(item.Status) {
		return nil, fmt.Errorf("%w：售后单已结束（%s），无法提示补证", errConflict, item.Status)
	}
	message, err := appendAfterSaleMessage(ctx, tx.Client(), item, "admin", adminID, adminName,
		afterSaleMessageInput{Content: afterSaleEvidenceRequestPrefix + in.Note, Visibility: "all"}, now)
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey(afterSaleEvidenceRequestEventType + ":" + item.AfterSaleNo + ":" + strconv.Itoa(message.ID)).
		SetEventType(afterSaleEvidenceRequestEventType).SetAggregateRef(item.AfterSaleNo).
		SetCreatedAt(now.UTC()).
		SetPayload(map[string]interface{}{
			"after_sale_no": item.AfterSaleNo, "note": in.Note, "admin_id": adminID,
			"requested_at": now.UTC().Format(time.RFC3339),
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSaleMessage.Get(ctx, message.ID)
}

// afterSaleEvidenceStatusOf 客户侧查询补证状态：以最近一次补证提示为锚点，
// 之后若客户回复过带附件的沟通记录则视为已补证。
func afterSaleEvidenceStatusOf(ctx context.Context, db *ent.Client, customerID, afterSaleID int) (*afterSaleEvidenceStatus, error) {
	if customerID < 1 || afterSaleID < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsAfterSale.Get(ctx, afterSaleID)
	if err != nil {
		return nil, err
	}
	if item.CustomerID == nil || *item.CustomerID != customerID {
		return nil, errForbidden
	}
	status := &afterSaleEvidenceStatus{
		AfterSaleID: item.ID, AfterSaleNo: item.AfterSaleNo,
		EvidencePresent: strings.TrimSpace(item.EvidenceRef) != "",
	}
	request, err := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ(afterSaleEvidenceRequestEventType),
		esportsoutbox.AggregateRefEQ(item.AfterSaleNo),
	).Order(ent.Desc(esportsoutbox.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return status, nil
	}
	if err != nil {
		return nil, err
	}
	requestedAt := request.CreatedAt.UTC()
	status.RequestedAt = &requestedAt
	if note, ok := request.Payload["note"].(string); ok {
		status.RequestNote = note
	}
	replies, err := db.EsportsAfterSaleMessage.Query().Where(
		esportsaftersalemessage.AfterSaleIDEQ(item.ID),
		esportsaftersalemessage.AuthorTypeEQ(esportsaftersalemessage.AuthorTypeCustomer),
		esportsaftersalemessage.CreatedAtGTE(requestedAt),
	).Select(esportsaftersalemessage.FieldAttachments).All(ctx)
	if err != nil {
		return nil, err
	}
	submitted := false
	for _, reply := range replies {
		if len(reply.Attachments) > 0 {
			submitted = true
			break
		}
	}
	status.PendingEvidence = !submitted
	return status, nil
}

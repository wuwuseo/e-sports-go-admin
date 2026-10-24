package esports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersalemessage"
)

// 售后单内回复 / 沟通记录：客服与客户在同一张售后单里来回回复，
// 记录只增不改（EsportsAfterSaleMessage + appendOnlyHooks），
// visibility=internal 的内部备注仅客服侧可见，客户侧列表只返回 visibility=all。

// maxAfterSaleMessageAttachments 单条沟通最多引用的附件数。
const maxAfterSaleMessageAttachments = 20

// afterSaleMessageInput 沟通记录入参。
type afterSaleMessageInput struct {
	Content     string   `json:"content"`
	Visibility  string   `json:"visibility"`
	Attachments []string `json:"attachments"`
}

// normalizeAfterSaleMessageInput 归一化并校验沟通内容；附件引用沿用 evidence_ref 口径（非空、≤255 字符）。
func normalizeAfterSaleMessageInput(in afterSaleMessageInput) (afterSaleMessageInput, error) {
	in.Content = strings.TrimSpace(in.Content)
	in.Visibility = strings.TrimSpace(in.Visibility)
	if in.Visibility == "" {
		in.Visibility = string(esportsaftersalemessage.VisibilityAll)
	}
	switch esportsaftersalemessage.Visibility(in.Visibility) {
	case esportsaftersalemessage.VisibilityAll, esportsaftersalemessage.VisibilityInternal:
	default:
		return in, fmt.Errorf("%w：可见性只能是 all（客户可见）或 internal（仅客服可见）", errInvalidInput)
	}
	if in.Content == "" || len([]rune(in.Content)) > 1000 {
		return in, fmt.Errorf("%w：沟通内容必填且不超过 1000 字", errInvalidInput)
	}
	if len(in.Attachments) > maxAfterSaleMessageAttachments {
		return in, fmt.Errorf("%w：单条沟通最多引用 %d 个附件", errInvalidInput, maxAfterSaleMessageAttachments)
	}
	refs := make([]string, 0, len(in.Attachments))
	for _, ref := range in.Attachments {
		ref = strings.TrimSpace(ref)
		if ref == "" || len(ref) > 255 {
			return in, fmt.Errorf("%w：附件引用无效", errInvalidInput)
		}
		refs = append(refs, ref)
	}
	in.Attachments = refs
	return in, nil
}

// listAfterSaleMessages 列出沟通记录（按时间升序）；includeInternal=false 时只返回客户可见消息。
func listAfterSaleMessages(ctx context.Context, db *ent.Client, afterSaleID int, includeInternal bool, limit int) ([]*ent.EsportsAfterSaleMessage, error) {
	if afterSaleID < 1 {
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsAfterSaleMessage.Query().Where(
		esportsaftersalemessage.AfterSaleIDEQ(afterSaleID),
	)
	if !includeInternal {
		q = q.Where(esportsaftersalemessage.VisibilityEQ(esportsaftersalemessage.VisibilityAll))
	}
	return q.Order(ent.Asc(esportsaftersalemessage.FieldCreatedAt), ent.Asc(esportsaftersalemessage.FieldID)).
		Limit(limit).All(ctx)
}

// appendAfterSaleMessage 追加一条沟通记录（只增不改）。
// 内部备注（visibility=internal）只有客服可写：客户 / 陪玩 / 系统写入一律明确拒绝，不静默降级为可见。
func appendAfterSaleMessage(ctx context.Context, db *ent.Client, item *ent.EsportsAfterSale,
	authorType string, authorID int, authorName string, in afterSaleMessageInput, now time.Time) (*ent.EsportsAfterSaleMessage, error) {
	if item == nil || item.ID < 1 {
		return nil, errInvalidInput
	}
	in, err := normalizeAfterSaleMessageInput(in)
	if err != nil {
		return nil, err
	}
	switch esportsaftersalemessage.AuthorType(authorType) {
	case esportsaftersalemessage.AuthorTypeAdmin, esportsaftersalemessage.AuthorTypeCustomer,
		esportsaftersalemessage.AuthorTypePlayer, esportsaftersalemessage.AuthorTypeSystem:
	default:
		return nil, errForbidden
	}
	if authorType != string(esportsaftersalemessage.AuthorTypeAdmin) &&
		in.Visibility == string(esportsaftersalemessage.VisibilityInternal) {
		return nil, fmt.Errorf("%w：仅客服可写内部备注", errForbidden)
	}
	if authorID < 0 {
		return nil, errInvalidInput
	}
	create := db.EsportsAfterSaleMessage.Create().SetAfterSaleID(item.ID).
		SetAuthorType(esportsaftersalemessage.AuthorType(authorType)).SetAuthorID(authorID).
		SetAuthorName(truncateRunes(strings.TrimSpace(authorName), 120)).
		SetContent(in.Content).
		SetVisibility(esportsaftersalemessage.Visibility(in.Visibility)).
		SetCreatedAt(now.UTC())
	if len(in.Attachments) > 0 {
		create.SetAttachments(in.Attachments)
	}
	return create.Save(ctx)
}

// postAdminAfterSaleMessage 客服侧回复（可写内部备注）。
func postAdminAfterSaleMessage(ctx context.Context, db *ent.Client, item *ent.EsportsAfterSale,
	adminID int, adminName string, in afterSaleMessageInput, now time.Time) (*ent.EsportsAfterSaleMessage, error) {
	if adminID < 1 {
		return nil, errForbidden
	}
	return appendAfterSaleMessage(ctx, db, item, "admin", adminID, adminName, in, now)
}

// postCustomerAfterSaleMessage 客户侧回复：售后单须归属本人且未落终态。
func postCustomerAfterSaleMessage(ctx context.Context, db *ent.Client, customerID int,
	afterSaleID int, in afterSaleMessageInput, now time.Time) (*ent.EsportsAfterSaleMessage, error) {
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
	if !afterSaleStatusActive(item.Status) {
		return nil, fmt.Errorf("%w：售后单已结束（%s），无法继续沟通", errConflict, item.Status)
	}
	customer, err := db.EsportsCustomer.Get(ctx, customerID)
	if err != nil {
		return nil, err
	}
	return appendAfterSaleMessage(ctx, db, item, "customer", customerID, customer.DisplayName, in, now)
}

// afterSaleStatusActive 是否为进行中状态（非终态）。
func afterSaleStatusActive(status esportsaftersale.Status) bool {
	for _, active := range activeAfterSaleStatuses() {
		if status == active {
			return true
		}
	}
	return false
}

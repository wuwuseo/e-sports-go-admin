package esports

import (
	"context"
	"encoding/json"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/attachment"
)

// §13.2 客户售后凭证上传：移动端先把图片/文档作为私有附件上传（前台统一上传端点），
// 再把附件 ID 通过 evidence_attachment_ids 引用进售后单 / 复核单。引用以 JSON 数组形式
// 序列化进既有 evidence_ref 文本字段，历史纯文本证据（非 JSON）不受影响。

// aftersaleEvidenceDirectory 客户售后凭证附件的私有目录。
const aftersaleEvidenceDirectory = "private/esports/aftersale"

// maxAfterSaleEvidenceAttachments 单次可引用的凭证附件上限，避免撑爆 evidence_ref 的 255 字符上限。
const maxAfterSaleEvidenceAttachments = 20

// validAftersaleEvidenceMime 售后凭证限定类型：图片与 PDF 文档。
func validAftersaleEvidenceMime(mime string) bool {
	switch mime {
	case "image/jpeg", "image/png", "application/pdf":
		return true
	}
	return false
}

// resolveAfterSaleEvidence 校验售后凭证附件：须为 expectedUploadedBy 本人上传、
// 落在 esports 私有售后目录、内容哈希为 64 位 hex、MIME 为图片/PDF。
// 归属不符返回 errForbidden（他人附件），其余不满足返回 errInvalidInput。
func resolveAfterSaleEvidence(ctx context.Context, db *ent.Client, ids []int, expectedUploadedBy string) ([]*ent.Attachment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > maxAfterSaleEvidenceAttachments {
		return nil, errInvalidInput
	}
	if expectedUploadedBy == "" {
		return nil, errForbidden
	}
	attachments := make([]*ent.Attachment, 0, len(ids))
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id < 1 {
			return nil, errInvalidInput
		}
		if _, dup := seen[id]; dup {
			return nil, errInvalidInput
		}
		seen[id] = struct{}{}
		item, err := db.Attachment.Get(ctx, id)
		if err != nil {
			return nil, errInvalidInput
		}
		if item.UploadedBy != expectedUploadedBy {
			return nil, errForbidden
		}
		if item.Directory != aftersaleEvidenceDirectory ||
			!validEvidenceHash(item.ContentHash) ||
			!validAftersaleEvidenceMime(item.MimeType) {
			return nil, errInvalidInput
		}
		attachments = append(attachments, item)
	}
	return attachments, nil
}

// afterSaleEvidenceRef 把凭证附件 ID 序列化为 evidence_ref 的 JSON 数组形式。
func afterSaleEvidenceRef(ids []int) (string, error) {
	raw, err := json.Marshal(ids)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// parseAfterSaleEvidenceRef 解析 evidence_ref 中的附件 ID：JSON 数组返回其元素，
// 历史纯文本（如 oss:// 引用）无法解析时返回空切片，兼容既有数据。
func parseAfterSaleEvidenceRef(ref string) ([]int, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	var ids []int
	if err := json.Unmarshal([]byte(ref), &ids); err != nil {
		return nil, nil
	}
	return ids, nil
}

// customerAfterSaleInput 客户自助建售后入参：在既有 afterSaleInput 基础上增加凭证附件 ID 列表。
// 供主代理接线客户建售后 handler 时 bind 使用（匿名嵌入后 JSON 字段扁平化，evidence_attachment_ids
// 可一并解析，其余字段与 afterSaleInput 一致）。
type customerAfterSaleInput struct {
	afterSaleInput
	EvidenceAttachmentIDs []int `json:"evidence_attachment_ids"`
}

// customerUploadSubject 返回客户上传附件的归属域标识 "user:<uid>"（§13.2 本人限定）。
// 客户未绑定前台用户（无 user_id / 无 uid）时无法校验凭证归属，返回 errForbidden。
func customerUploadSubject(ctx context.Context, db *ent.Client, customer *ent.EsportsCustomer) (string, error) {
	if customer == nil || customer.UserID == nil {
		return "", errForbidden
	}
	account, err := db.User.Get(ctx, *customer.UserID)
	if err != nil {
		return "", errForbidden
	}
	if account.UID == "" {
		return "", errForbidden
	}
	return "user:" + account.UID, nil
}

// listCustomerAfterSaleEvidence 客户自助读取售后凭证附件清单（§13.2 只读确认）：
// 解析 evidence_ref 的 JSON 附件 ID 返回附件详情，仅售后单归属客户本人可读。
func listCustomerAfterSaleEvidence(ctx context.Context, db *ent.Client, customerID, afterSaleID int) ([]*ent.Attachment, error) {
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
	ids, err := parseAfterSaleEvidenceRef(item.EvidenceRef)
	if err != nil || len(ids) == 0 {
		return []*ent.Attachment{}, nil
	}
	return db.Attachment.Query().Where(attachment.IDIn(ids...)).All(ctx)
}

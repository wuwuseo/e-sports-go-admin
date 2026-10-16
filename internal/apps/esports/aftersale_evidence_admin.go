package esports

import (
	"context"
	"time"

	"go-admin/internal/ent"
)

// §13.2 客服代建售后附件接入：客服通过 admin 通道代客户建售后时可附带凭证附件，
// 附件须为客服本人上传（admin.UID），校验通过后把引用序列化进 evidence_ref 的 JSON 数组。

// adminCreateAfterSaleInput 客服代建售后入参：在既有 afterSaleInput 基础上增加凭证附件 ID 列表。
// 供主代理接线客服建售后 handler 时 bind 使用（匿名嵌入后 JSON 字段扁平化，evidence_attachment_ids
// 可一并解析，其余字段与 afterSaleInput 一致）。
type adminCreateAfterSaleInput struct {
	afterSaleInput
	EvidenceAttachmentIDs []int `json:"evidence_attachment_ids"`
}

// createAfterSaleWithEvidence 客服代建售后（§13.2）：在 createAfterSale 外层附加附件解析，
// 凭证附件按客服本人（admin.UID）校验归属，非空时把引用序列化进 evidence_ref 后复用 createAfterSale
// 落库；引用他人附件返回 errForbidden。不改 createAfterSale 的既有校验逻辑。
func createAfterSaleWithEvidence(ctx context.Context, db *ent.Client, in afterSaleInput,
	evidenceAttachmentIDs []int, operatorID int, now time.Time) (*ent.EsportsAfterSale, error) {
	if len(evidenceAttachmentIDs) > 0 {
		staff, err := db.Admin.Get(ctx, operatorID)
		if err != nil {
			return nil, err
		}
		if _, err := resolveAfterSaleEvidence(ctx, db, evidenceAttachmentIDs, staff.UID); err != nil {
			return nil, err
		}
		ref, err := afterSaleEvidenceRef(evidenceAttachmentIDs)
		if err != nil {
			return nil, err
		}
		in.EvidenceRef = ref
	}
	return createAfterSale(ctx, db, in, operatorID, now)
}

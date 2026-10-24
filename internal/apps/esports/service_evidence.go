package esports

import (
	"context"
	"encoding/json"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsservicesegment"
)

// serviceEvidenceDirectory 是陪玩服务截图的私有附件目录。
const serviceEvidenceDirectory = "private/esports/service-evidence"

// maxServiceEvidenceAttachments 限制单次提交的服务截图数量，避免 evidence_ref 超出 255 字符上限。
const maxServiceEvidenceAttachments = 9

// attachmentUploadedByPlayer 校验附件上传者是否为该陪玩对应的用户。
// 前台路由上传的 uploaded_by 带 "user:" 域前缀，管理端/存量数据为裸 uid，两者都视为本人。
func attachmentUploadedByPlayer(attachment *ent.Attachment, uid string) bool {
	if uid == "" {
		return false
	}
	return attachment.UploadedBy == uid || attachment.UploadedBy == "user:"+uid
}

// validateServiceEvidence 校验陪玩提交的服务截图附件：须为本人上传的 jpeg/png，
// 且落在 esports 私有目录 serviceEvidenceDirectory。通过后返回落库到服务段 evidence_ref 的
// JSON 数组（如 [12,13]）；空列表返回空字符串表示未提交凭证。
func validateServiceEvidence(ctx context.Context, db *ent.Client, playerID int, attachmentIDs []int) (string, error) {
	if len(attachmentIDs) == 0 {
		return "", nil
	}
	if playerID < 1 || len(attachmentIDs) > maxServiceEvidenceAttachments {
		return "", errInvalidInput
	}
	player, err := db.EsportsPlayer.Get(ctx, playerID)
	if err != nil {
		return "", errInvalidInput
	}
	account, err := db.User.Get(ctx, player.UserID)
	if err != nil {
		return "", errInvalidInput
	}
	ids := make([]int, 0, len(attachmentIDs))
	seen := map[int]bool{}
	for _, id := range attachmentIDs {
		if id < 1 || seen[id] {
			return "", errInvalidInput
		}
		seen[id] = true
		attachment, err := db.Attachment.Get(ctx, id)
		if err != nil {
			return "", errInvalidInput
		}
		if !attachmentUploadedByPlayer(attachment, account.UID) ||
			attachment.Directory != serviceEvidenceDirectory ||
			(attachment.MimeType != "image/jpeg" && attachment.MimeType != "image/png") {
			return "", errInvalidInput
		}
		ids = append(ids, id)
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return "", err
	}
	if len([]rune(string(data))) > 255 {
		return "", errInvalidInput
	}
	return string(data), nil
}

// finishPlayerServiceWithEvidence 结束服务并提交本人服务段凭证：
// 先校验本人服务段归属（非本人 403），再校验截图附件并把附件 ID 落为 evidence_ref JSON 数组，
// 最后复用既有 finishPlayerService，保持结单口径对「有凭证」的判定不变。
func finishPlayerServiceWithEvidence(ctx context.Context, db *ent.Client, playerID, orderID int,
	reportedUnits, reportedSeconds int64, evidenceAttachmentIDs []int, now time.Time) (*ent.EsportsOrder, error) {
	if playerID < 1 || orderID < 1 || reportedUnits < 0 || reportedSeconds <= 0 {
		return nil, errInvalidInput
	}
	// 本人服务段校验：本人须在该订单上有一个服务中的服务段；
	// 本人无服务段但该订单存在他人服务中的服务段时按越权拒绝。
	ownCount, err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
		esportsservicesegment.PlayerIDEQ(playerID),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusInService),
	).Count(ctx)
	if err != nil {
		return nil, err
	}
	if ownCount == 0 {
		inServiceCount, err := db.EsportsServiceSegment.Query().Where(
			esportsservicesegment.OrderIDEQ(orderID),
			esportsservicesegment.StatusEQ(esportsservicesegment.StatusInService),
		).Count(ctx)
		if err != nil {
			return nil, err
		}
		if inServiceCount > 0 {
			return nil, errForbidden
		}
		return nil, errConflict
	}
	evidenceRef, err := validateServiceEvidence(ctx, db, playerID, evidenceAttachmentIDs)
	if err != nil {
		return nil, err
	}
	return finishPlayerService(ctx, db, playerID, orderID, reportedUnits, reportedSeconds, evidenceRef, now)
}

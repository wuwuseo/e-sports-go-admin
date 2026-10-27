package esports

import (
	"context"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayeridentity"
	"go-admin/internal/ent/esportsrealnameverification"
)

// MigratePlayerIdentityToRealName 把旧陪玩实名 verified 记录幂等迁入统一实名档案。
// 只搬 verified：pending_verification / rejected 不迁移（用户走新入口重新提交）。
// 目标主体已有档案时跳过 —— 新表状态永远更权威，迁移不覆盖。
// 返回本次新增条数；旧表无数据等异常一律容忍，绝不阻塞功能集初始化。
func MigratePlayerIdentityToRealName(ctx context.Context, db *ent.Client) (int, error) {
	rows, err := db.EsportsPlayerIdentity.Query().
		Where(esportsplayeridentity.StatusEQ(esportsplayeridentity.StatusVerified)).
		All(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return 0, nil
		}
		return 0, err
	}
	migrated := 0
	for _, row := range rows {
		exists, err := db.EsportsRealNameVerification.Query().Where(
			esportsrealnameverification.SubjectTypeEQ(esportsrealnameverification.SubjectTypePlayer),
			esportsrealnameverification.SubjectIDEQ(row.PlayerID),
		).Exist(ctx)
		if err != nil {
			return migrated, err
		}
		if exists {
			continue
		}
		verifiedAt := row.CreatedAt
		if row.ReviewedAt != nil {
			verifiedAt = *row.ReviewedAt
		}
		create := db.EsportsRealNameVerification.Create().
			SetSubjectType(esportsrealnameverification.SubjectTypePlayer).
			SetSubjectID(row.PlayerID).
			SetRealName(row.RealName).
			SetIDNumberMasked(row.IDNumberMasked).
			SetChannel(esportsrealnameverification.ChannelManual).
			SetMethod(esportsrealnameverification.MethodManual).
			SetStatus(esportsrealnameverification.StatusVerified).
			SetVerifiedAt(verifiedAt).
			SetRequestedBy(row.RequestedBy)
		if row.ReviewedByAdminID != nil {
			create = create.SetNillableReviewedByAdminID(row.ReviewedByAdminID)
		}
		if _, err := create.Save(ctx); err != nil {
			// 并发启动撞 (subject_type, subject_id) 唯一索引：视为已迁移
			if ent.IsConstraintError(err) {
				continue
			}
			return migrated, err
		}
		migrated++
	}
	return migrated, nil
}

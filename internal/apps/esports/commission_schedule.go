package esports

import (
	"context"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionrule"
	"go-admin/internal/ent/esportscscommissionrule"
)

// activateDueCommissionRules 把已到预设时间的陪玩分成规则转为 active，并归档同编号旧版本。
// 业务解析器按时间可直接命中 due scheduled 版本，因此任务短暂延迟不改变计提结果。
func activateDueCommissionRules(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	due, err := db.EsportsCommissionRule.Query().Where(
		esportscommissionrule.StatusEQ(esportscommissionrule.StatusScheduled),
		esportscommissionrule.EffectiveAtLTE(now.UTC()),
	).Order(ent.Asc(esportscommissionrule.FieldEffectiveAt), ent.Asc(esportscommissionrule.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, rule := range due {
		tx, err := db.Tx(ctx)
		if err != nil {
			return count, err
		}
		current, err := tx.EsportsCommissionRule.Get(ctx, rule.ID)
		if err != nil {
			_ = tx.Rollback()
			return count, err
		}
		if current.Status != esportscommissionrule.StatusScheduled || current.EffectiveAt == nil ||
			current.EffectiveAt.After(now.UTC()) {
			_ = tx.Rollback()
			continue
		}
		if _, err = tx.EsportsCommissionRule.Update().Where(
			esportscommissionrule.RuleNoEQ(current.RuleNo),
			esportscommissionrule.StatusEQ(esportscommissionrule.StatusActive),
		).SetStatus(esportscommissionrule.StatusHistory).Save(ctx); err != nil {
			_ = tx.Rollback()
			return count, err
		}
		changed, err := tx.EsportsCommissionRule.Update().Where(
			esportscommissionrule.IDEQ(current.ID),
			esportscommissionrule.StatusEQ(esportscommissionrule.StatusScheduled),
		).SetStatus(esportscommissionrule.StatusActive).Save(ctx)
		if err != nil || changed != 1 {
			_ = tx.Rollback()
			if err != nil {
				return count, err
			}
			return count, errConflict
		}
		if err = tx.Commit(); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// activateDueCsCommissionRules 与陪玩分成使用相同的预设生效语义。
func activateDueCsCommissionRules(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	due, err := db.EsportsCsCommissionRule.Query().Where(
		esportscscommissionrule.StatusEQ(esportscscommissionrule.StatusScheduled),
		esportscscommissionrule.EffectiveAtLTE(now.UTC()),
	).Order(ent.Asc(esportscscommissionrule.FieldEffectiveAt), ent.Asc(esportscscommissionrule.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, rule := range due {
		tx, err := db.Tx(ctx)
		if err != nil {
			return count, err
		}
		current, err := tx.EsportsCsCommissionRule.Get(ctx, rule.ID)
		if err != nil {
			_ = tx.Rollback()
			return count, err
		}
		if current.Status != esportscscommissionrule.StatusScheduled || current.EffectiveAt == nil ||
			current.EffectiveAt.After(now.UTC()) {
			_ = tx.Rollback()
			continue
		}
		if _, err = tx.EsportsCsCommissionRule.Update().Where(
			esportscscommissionrule.RuleNoEQ(current.RuleNo),
			esportscscommissionrule.StatusEQ(esportscscommissionrule.StatusActive),
		).SetStatus(esportscscommissionrule.StatusHistory).Save(ctx); err != nil {
			_ = tx.Rollback()
			return count, err
		}
		changed, err := tx.EsportsCsCommissionRule.Update().Where(
			esportscscommissionrule.IDEQ(current.ID),
			esportscscommissionrule.StatusEQ(esportscscommissionrule.StatusScheduled),
		).SetStatus(esportscscommissionrule.StatusActive).Save(ctx)
		if err != nil || changed != 1 {
			_ = tx.Rollback()
			if err != nil {
				return count, err
			}
			return count, errConflict
		}
		if err = tx.Commit(); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

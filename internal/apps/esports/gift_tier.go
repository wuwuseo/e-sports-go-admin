package esports

import (
	"context"
	"fmt"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsgiftintent"
	"go-admin/internal/ent/esportsgiftorder"
	"go-admin/internal/ent/esportsgifttier"
)

// 礼物价格档位：同一礼物可配置多档价格，每档独立设置高额二次确认（high_value）
// 与年龄限制（require_age），使 require_age 真正按档生效。
//
// 口径：
//   - 同一礼物下 (gift_id, price_cents) 唯一（schema 唯一索引兜底）；
//   - 每个礼物至多一个 is_default 档位，供未指定档位的旧客户端回落；
//   - 已被礼物确认单 / 礼物单引用过的档位不可物理删除，只能停用（保历史单可追溯）；
//   - 礼物自身的 price_cents / high_value / require_age 保留为「不分档」时的回落口径。

// giftTierInput 是档位可编辑字段。gift_id / tier_code 创建后不可变。
type giftTierInput struct {
	GiftID     int    `json:"gift_id"`
	TierCode   string `json:"tier_code"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	HighValue  bool   `json:"high_value"`
	RequireAge bool   `json:"require_age"`
	IsDefault  bool   `json:"is_default"`
	SortOrder  int    `json:"sort_order"`
	Status     string `json:"status"`
}

// validateGiftTierInput 校验档位配置；任一不满足返回 errInvalidInput。
func validateGiftTierInput(in giftTierInput) error {
	code := strings.TrimSpace(in.TierCode)
	name := strings.TrimSpace(in.Name)
	if in.GiftID < 1 || code == "" || len(code) > 48 || name == "" || len([]rune(name)) > 120 ||
		in.PriceCents <= 0 {
		return errInvalidInput
	}
	switch in.Status {
	case "", "enabled", "disabled":
		return nil
	default:
		return errInvalidInput
	}
}

// createGiftTier 新建礼物价格档位；同礼物同价格与 tier_code 冲突返回 errConflict。
// is_default=true 时清除同礼物的其他默认档（每个礼物至多一档默认）。
func createGiftTier(ctx context.Context, db *ent.Client, in giftTierInput) (*ent.EsportsGiftTier, error) {
	if err := validateGiftTierInput(in); err != nil {
		return nil, err
	}
	if _, err := db.EsportsGift.Get(ctx, in.GiftID); err != nil {
		return nil, err
	}
	status := in.Status
	if status == "" {
		status = "enabled"
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if in.IsDefault {
		if _, err = tx.EsportsGiftTier.Update().Where(esportsgifttier.GiftIDEQ(in.GiftID)).
			SetIsDefault(false).Save(ctx); err != nil {
			return nil, err
		}
	}
	item, err := tx.EsportsGiftTier.Create().
		SetGiftID(in.GiftID).SetTierCode(strings.TrimSpace(in.TierCode)).
		SetName(strings.TrimSpace(in.Name)).SetPriceCents(in.PriceCents).
		SetHighValue(in.HighValue).SetRequireAge(in.RequireAge).
		SetIsDefault(in.IsDefault).SetSortOrder(in.SortOrder).
		SetStatus(esportsgifttier.Status(status)).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsGiftTier.Get(ctx, item.ID)
}

// updateGiftTier 条件更新档位（version 乐观锁）；gift_id、tier_code 不可改。
func updateGiftTier(ctx context.Context, db *ent.Client, id, expectedVersion int, in giftTierInput) (*ent.EsportsGiftTier, error) {
	if id < 1 || expectedVersion < 1 {
		return nil, errInvalidInput
	}
	current, err := db.EsportsGiftTier.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	in.GiftID = current.GiftID
	if in.TierCode == "" {
		in.TierCode = current.TierCode
	}
	if in.Status == "" {
		in.Status = current.Status.String()
	}
	if err = validateGiftTierInput(in); err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if in.IsDefault {
		if _, err = tx.EsportsGiftTier.Update().Where(
			esportsgifttier.GiftIDEQ(current.GiftID), esportsgifttier.IDNEQ(id),
		).SetIsDefault(false).Save(ctx); err != nil {
			return nil, err
		}
	}
	changed, err := tx.EsportsGiftTier.Update().Where(
		esportsgifttier.IDEQ(id), esportsgifttier.VersionEQ(expectedVersion),
	).SetName(strings.TrimSpace(in.Name)).SetPriceCents(in.PriceCents).
		SetHighValue(in.HighValue).SetRequireAge(in.RequireAge).
		SetIsDefault(in.IsDefault).SetSortOrder(in.SortOrder).
		SetStatus(esportsgifttier.Status(in.Status)).AddVersion(1).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsGiftTier.Get(ctx, id)
}

// deleteGiftTier 删除档位：已被礼物确认单 / 礼物单引用过的档位不可删除（保历史单可追溯），
// 只能停用；未被引用时物理删除。
func deleteGiftTier(ctx context.Context, db *ent.Client, id, expectedVersion int) error {
	if id < 1 || expectedVersion < 1 {
		return errInvalidInput
	}
	current, err := db.EsportsGiftTier.Get(ctx, id)
	if err != nil {
		return err
	}
	if current.Version != expectedVersion {
		return errConflict
	}
	usedIntent, err := db.EsportsGiftIntent.Query().Where(esportsgiftintent.GiftTierIDEQ(id)).Exist(ctx)
	if err != nil {
		return err
	}
	usedOrder, err := db.EsportsGiftOrder.Query().Where(esportsgiftorder.GiftTierIDEQ(id)).Exist(ctx)
	if err != nil {
		return err
	}
	if usedIntent || usedOrder {
		return fmt.Errorf("%w：该档位已被礼物单据引用，只能停用不能删除", errConflict)
	}
	return db.EsportsGiftTier.DeleteOneID(id).Exec(ctx)
}

// listGiftTiers 列出某礼物的价格档位；giftID 为 0 时列出全部（按礼物 + 排序值升序）。
func listGiftTiers(ctx context.Context, db *ent.Client, giftID int, status string) ([]*ent.EsportsGiftTier, error) {
	q := db.EsportsGiftTier.Query()
	if giftID > 0 {
		q.Where(esportsgifttier.GiftIDEQ(giftID))
	}
	switch status = strings.TrimSpace(status); status {
	case "":
	case "enabled", "disabled":
		q.Where(esportsgifttier.StatusEQ(esportsgifttier.Status(status)))
	default:
		return nil, errInvalidInput
	}
	return q.Order(ent.Asc(esportsgifttier.FieldGiftID), ent.Asc(esportsgifttier.FieldSortOrder),
		ent.Asc(esportsgifttier.FieldPriceCents), ent.Asc(esportsgifttier.FieldID)).All(ctx)
}

// giftSaleTerms 是一次送礼实际生效的价格与限制口径。
type giftSaleTerms struct {
	tier         *ent.EsportsGiftTier
	unitPrice    int64
	highValue    bool
	requireAge   bool
	requireAdult bool
}

// giftSaleTermsFor 解析本次送礼生效的档位与限制：指定档位时按档（价格、二次确认、年龄限制均按档），
// 未指定档位时回落到礼物自身字段；不分档的礼物沿用「一律要求成年」的既有硬约束（不放松存量口径）。
func giftSaleTermsFor(ctx context.Context, db *ent.Client, gift *ent.EsportsGift, tierID int) (giftSaleTerms, error) {
	if gift == nil {
		return giftSaleTerms{}, errInvalidInput
	}
	if tierID < 1 {
		return giftSaleTerms{
			unitPrice: gift.PriceCents, highValue: gift.HighValue,
			requireAge: gift.RequireAge, requireAdult: true,
		}, nil
	}
	tier, err := db.EsportsGiftTier.Query().Where(
		esportsgifttier.IDEQ(tierID), esportsgifttier.GiftIDEQ(gift.ID),
		esportsgifttier.StatusEQ(esportsgifttier.StatusEnabled),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return giftSaleTerms{}, fmt.Errorf("%w：礼物档位不存在或已停用", errInvalidInput)
	}
	if err != nil {
		return giftSaleTerms{}, err
	}
	return giftSaleTerms{
		tier: tier, unitPrice: tier.PriceCents,
		highValue:  gift.HighValue || tier.HighValue,
		requireAge: tier.RequireAge, requireAdult: tier.RequireAge,
	}, nil
}

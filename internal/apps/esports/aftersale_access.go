package esports

import (
	"context"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/global"
)

// loadOwnedAfterSaleSourceByRef 按来源类型 + 来源 ID 校验归属（§13.1 / §13.3）。
// order 校验负责客服；gift / recharge 校验来源单据客户归属客服；
// assessment（陪玩申诉）无客户归属，任意管理员可见。
// 超级管理员直接放行。
func loadOwnedAfterSaleSourceByRef(ctx context.Context, db *ent.Client, adminID int, isSuper bool, sourceType string, sourceID int) error {
	if isSuper {
		return nil
	}
	switch esportsaftersale.SourceType(sourceType) {
	case esportsaftersale.SourceTypeOrder:
		if _, err := loadOwnedOrder(ctx, db, adminID, false, sourceID); err != nil {
			return err
		}
		return nil
	case esportsaftersale.SourceTypeGift:
		if _, err := loadOwnedGiftOrder(ctx, db, adminID, false, sourceID); err != nil {
			return err
		}
		return nil
	case esportsaftersale.SourceTypeRecharge:
		row, err := db.EsportsWalletTx.Get(ctx, sourceID)
		if err != nil {
			return err
		}
		if row.EntryType != esportswallettx.EntryTypeRecharge {
			return errInvalidInput
		}
		customerID, err := walletTxCustomerID(ctx, db, row)
		if err != nil {
			return err
		}
		if customerID == nil {
			return errForbidden
		}
		if _, err := loadOwnedCustomer(ctx, db, adminID, false, *customerID); err != nil {
			return err
		}
		return nil
	case esportsaftersale.SourceTypeAssessment:
		// 陪玩申诉无客户归属，任意管理员可见。
		return nil
	}
	return errForbidden
}

func loadOwnedAfterSale(ctx context.Context, db *ent.Client, adminID int, isSuper bool, afterSaleID int) (*ent.EsportsAfterSale, error) {
	item, err := db.EsportsAfterSale.Get(ctx, afterSaleID)
	if err != nil {
		return nil, err
	}
	if err := loadOwnedAfterSaleSourceByRef(ctx, db, adminID, isSuper, item.SourceType.String(), item.SourceID); err != nil {
		return nil, err
	}
	return item, nil
}

func ownedAfterSaleHandler(next fiber.Handler) fiber.Handler {
	return func(c fiber.Ctx) error {
		adminID, err := requireAdmin(c)
		if err != nil {
			return businessError(c, err)
		}
		id, err := parsePathID(c, "售后单")
		if err != nil {
			return businessError(c, err)
		}
		isSuper, _ := c.Locals("isSuperAdmin").(bool)
		if _, err = loadOwnedAfterSale(c.Context(), global.ORM, adminID, isSuper, id); err != nil {
			return businessError(c, err)
		}
		return next(c)
	}
}

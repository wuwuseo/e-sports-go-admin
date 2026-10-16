package esports

import (
	"context"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/global"
)

func loadOwnedAfterSale(ctx context.Context, db *ent.Client, adminID int, isSuper bool, afterSaleID int) (*ent.EsportsAfterSale, error) {
	item, err := db.EsportsAfterSale.Get(ctx, afterSaleID)
	if err != nil {
		return nil, err
	}
	if _, err := loadOwnedOrder(ctx, db, adminID, isSuper, item.SourceID); err != nil {
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

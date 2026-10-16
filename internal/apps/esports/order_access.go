package esports

import (
	"context"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/global"
)

// loadOwnedOrder enforces the same row boundary used by the admin order list and detail.
func loadOwnedOrder(ctx context.Context, db *ent.Client, adminID int, isSuper bool, orderID int) (*ent.EsportsOrder, error) {
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if !isSuper && (order.ResponsibleAdminID == nil || *order.ResponsibleAdminID != adminID) {
		return nil, errForbidden
	}
	return order, nil
}

// ownedOrderHandler protects routes that operate on a responsible CS's order.
// Public-pool claim and independent close-approval routes have separate authorization rules.
func ownedOrderHandler(next fiber.Handler) fiber.Handler {
	return func(c fiber.Ctx) error {
		operator, err := requestAdmin(c)
		if err != nil {
			return businessError(c, err)
		}
		orderID, err := parsePathID(c, "订单")
		if err != nil {
			return businessError(c, err)
		}
		isSuper, _ := c.Locals("isSuperAdmin").(bool)
		if _, err = loadOwnedOrder(c.Context(), global.ORM, operator.ID, isSuper, orderID); err != nil {
			return businessError(c, err)
		}
		return next(c)
	}
}

func loadOwnedAddon(ctx context.Context, db *ent.Client, adminID int, isSuper bool, addonID int) (*ent.EsportsOrderAddon, error) {
	addon, err := db.EsportsOrderAddon.Get(ctx, addonID)
	if err != nil {
		return nil, err
	}
	if _, err := loadOwnedOrder(ctx, db, adminID, isSuper, addon.OrderID); err != nil {
		return nil, err
	}
	return addon, nil
}

func ownedAddonHandler(next fiber.Handler) fiber.Handler {
	return func(c fiber.Ctx) error {
		adminID, err := requireAdmin(c)
		if err != nil {
			return businessError(c, err)
		}
		addonID, err := parsePathID(c, "加钟单")
		if err != nil {
			return businessError(c, err)
		}
		isSuper, _ := c.Locals("isSuperAdmin").(bool)
		if _, err = loadOwnedAddon(c.Context(), global.ORM, adminID, isSuper, addonID); err != nil {
			return businessError(c, err)
		}
		return next(c)
	}
}

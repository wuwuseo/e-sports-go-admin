package esports

import (
	"context"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsordercollaborator"
	"go-admin/internal/global"
)

// loadOwnedOrder 是订单的**读取**边界：非超管可见「本人负责」或「被授权协作」的订单。
// 协作只看读取权限，不转移负责关系；写操作请使用 loadStrictOwnedOrder。
func loadOwnedOrder(ctx context.Context, db *ent.Client, adminID int, isSuper bool, orderID int) (*ent.EsportsOrder, error) {
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if isSuper {
		return order, nil
	}
	if order.ResponsibleAdminID != nil && *order.ResponsibleAdminID == adminID {
		return order, nil
	}
	// 协作文档按当前数据判定：移除后立即失去访问权，不做缓存。
	collaborating, err := db.EsportsOrderCollaborator.Query().Where(
		esportsordercollaborator.OrderIDEQ(orderID),
		esportsordercollaborator.AdminIDEQ(adminID),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !collaborating {
		return nil, errForbidden
	}
	return order, nil
}

// loadStrictOwnedOrder 是订单的**写入**边界：仅负责客服或超级管理员。
// 结单、退款、派单、改单等会改变资金或履约状态的操作必须走这里。
func loadStrictOwnedOrder(ctx context.Context, db *ent.Client, adminID int, isSuper bool, orderID int) (*ent.EsportsOrder, error) {
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if !isSuper && (order.ResponsibleAdminID == nil || *order.ResponsibleAdminID != adminID) {
		return nil, errForbidden
	}
	return order, nil
}

// ownedOrderHandler protects write routes that operate on a responsible CS's order.
// 协作客服（协作文档）不能复用该包装，只能走 readableOrderHandler 的只读路由。
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
		if _, err = loadStrictOwnedOrder(c.Context(), global.ORM, operator.ID, isSuper, orderID); err != nil {
			return businessError(c, err)
		}
		return next(c)
	}
}

// readableOrderHandler 用于只读路由：负责客服或被授权协作的客服都可访问。
func readableOrderHandler(next fiber.Handler) fiber.Handler {
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
	// 取消加钟 / 加钟收款都会动钱，仍按负责人边界收紧。
	if _, err := loadStrictOwnedOrder(ctx, db, adminID, isSuper, addon.OrderID); err != nil {
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

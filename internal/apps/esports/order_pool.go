package esports

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/global"
)

// releaseOrderToPublicPool 把「待派单」订单释放到公共池（§5.1 / §14）：
// 仅负责客服或超管可操作；清空 responsible_admin_id 后由值班客服认领。
// 已接单/服务中的订单不允许直接释放，必须先撤单/转单，避免绕过履约责任。
func releaseOrderToPublicPool(ctx context.Context, db *ent.Client, adminID int, isSuper bool,
	orderID int, reason string, now time.Time) (*ent.EsportsOrder, error) {
	reason = strings.TrimSpace(reason)
	if orderID < 1 || adminID < 1 || reason == "" || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := loadStrictOwnedOrder(ctx, tx.Client(), adminID, isSuper, orderID)
	if err != nil {
		return nil, err
	}
	if order.ResponsibleAdminID == nil {
		return nil, errConflict
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		return nil, errConflict
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID),
		esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingDispatch),
	).ClearResponsibleAdminID().AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.released_to_public_pool").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{"reason": reason, "from_admin_id": *order.ResponsibleAdminID}).
		Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, order.ID)
}

func releaseOrderToPublicPoolHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	order, err := releaseOrderToPublicPool(c.Context(), global.ORM, adminID, isSuper, orderID, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

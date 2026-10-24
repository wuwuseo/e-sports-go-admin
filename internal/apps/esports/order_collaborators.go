package esports

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsordercollaborator"
	"go-admin/internal/global"
)

// addOrderCollaborator 授予客服对订单的只读协作权限（功能列表口径：查看负责或获授权协作的订单）。
// 权限：仅超管或该订单负责人可授予；负责人自己无需再加入协作。
// 重复添加按幂等处理：直接返回既有记录，不报冲突。
func addOrderCollaborator(ctx context.Context, db *ent.Client, operatorID int, isSuper bool,
	orderID, adminID, grantedBy int, now time.Time) (*ent.EsportsOrderCollaborator, error) {
	if orderID < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	order, err := loadStrictOwnedOrder(ctx, db, operatorID, isSuper, orderID)
	if err != nil {
		return nil, err
	}
	// ：存单冻结期间禁止一切写操作，增删协作同属写操作。
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.ResponsibleAdminID != nil && *order.ResponsibleAdminID == adminID {
		return nil, fmt.Errorf("%w：订单负责人无需加入协作", errInvalidInput)
	}
	existing, err := db.EsportsOrderCollaborator.Query().Where(
		esportsordercollaborator.OrderIDEQ(orderID),
		esportsordercollaborator.AdminIDEQ(adminID),
	).Only(ctx)
	if err == nil {
		return existing, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	if grantedBy < 1 {
		grantedBy = operatorID
	}
	item, err := db.EsportsOrderCollaborator.Create().
		SetOrderID(orderID).
		SetAdminID(adminID).
		SetGrantedByAdminID(grantedBy).
		SetCreatedAt(now).
		Save(ctx)
	if ent.IsConstraintError(err) {
		// 并发重复授予：由唯一索引 (order_id, admin_id) 兜底，回读既有记录保证幂等。
		return db.EsportsOrderCollaborator.Query().Where(
			esportsordercollaborator.OrderIDEQ(orderID),
			esportsordercollaborator.AdminIDEQ(adminID),
		).Only(ctx)
	}
	if err != nil {
		return nil, err
	}
	return item, nil
}

// removeOrderCollaborator 撤销协作：删除后该客服立即失去订单读取权限。
// 权限：仅超管或该订单负责人可撤销；重复撤销返回记录不存在。
func removeOrderCollaborator(ctx context.Context, db *ent.Client, operatorID int, isSuper bool,
	orderID, adminID int) error {
	if orderID < 1 || adminID < 1 {
		return errInvalidInput
	}
	order, err := loadStrictOwnedOrder(ctx, db, operatorID, isSuper, orderID)
	if err != nil {
		return err
	}
	// ：存单冻结期间禁止一切写操作，撤销协作同属写操作。
	if err = ensureOrderNotFrozen(order); err != nil {
		return err
	}
	deleted, err := db.EsportsOrderCollaborator.Delete().Where(
		esportsordercollaborator.OrderIDEQ(orderID),
		esportsordercollaborator.AdminIDEQ(adminID),
	).Exec(ctx)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return &ent.NotFoundError{}
	}
	return nil
}

// listOrderCollaborators 返回订单当前的协作客服列表，按授予先后排序。
func listOrderCollaborators(ctx context.Context, db *ent.Client, orderID int) ([]*ent.EsportsOrderCollaborator, error) {
	if orderID < 1 {
		return nil, errInvalidInput
	}
	return db.EsportsOrderCollaborator.Query().
		Where(esportsordercollaborator.OrderIDEQ(orderID)).
		Order(ent.Asc(esportsordercollaborator.FieldID)).
		All(ctx)
}

type orderCollaboratorInput struct {
	AdminID int `json:"admin_id"`
}

func listOrderCollaboratorsHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	if _, err := loadOwnedOrder(c.Context(), global.ORM, operator.ID, isSuper, orderID); err != nil {
		return businessError(c, err)
	}
	items, err := listOrderCollaborators(c.Context(), global.ORM, orderID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func addOrderCollaboratorHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in orderCollaboratorInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	item, err := addOrderCollaborator(c.Context(), global.ORM, operator.ID, isSuper,
		orderID, in.AdminID, operator.ID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func removeOrderCollaboratorHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	adminID, err := strconv.Atoi(c.Params("adminId"))
	if err != nil || adminID < 1 {
		return businessError(c, errInvalidInput)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	if err := removeOrderCollaborator(c.Context(), global.ORM, operator.ID, isSuper, orderID, adminID); err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"order_id": orderID, "admin_id": adminID})
}

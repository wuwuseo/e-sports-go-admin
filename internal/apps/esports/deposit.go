package esports

import (
	"context"
	"errors"
	"strconv"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
)

// errOrderFrozen 是冻结订单写操作统一拦截错误：存单冻结期间禁止一切变更，
// 调用方以 errors.Is 识别并返回明确中文提示。
var errOrderFrozen = errors.New("订单已存单冻结，请先恢复后再操作")

// ensureOrderNotFrozen 统一存单守卫：冻结（deposit_status=frozen）的订单禁止任何写操作，
// 包括改价/改期/取消/派单/加钟/核销/结单/转单/暂停恢复服务段/启动服务等。
// 存单（depositOrder）与恢复（restoreOrder）本身不经过此守卫，可正常操作。
func ensureOrderNotFrozen(order *ent.EsportsOrder) error {
	if order != nil && order.DepositStatus == esportsorder.DepositStatusFrozen {
		return errOrderFrozen
	}
	return nil
}

// depositOrder 存单：对一张已有订单点「存单」，把订单冻结存起来。
// 存单不是货币 / 储值账户 / 开卡套餐，只是订单上的一个冻结标记：
// 冻结不改变履约状态，也不释放 SKU 名额占用（不动 quota_reserved、不碰 EsportsQuotaHold）。
//
// 前置条件（任一不满足返回 errConflict）：
//  1. 订单已付款（payment_status = paid），未付款单一律不许存单；
//  2. 订单尚未存单（deposit_status = none）；
//  3. 订单未结束（履约状态不属于 completed / cancelled）。
//
// 并发保护：条件 UPDATE 带 version + deposit_status=none + payment_status=paid，
// changed != 1 即视为状态已变化返回 errConflict，避免并发重复存单。
//
// §3.3-11：冻结期间禁止一切订单变更 —— 各写入口统一经 ensureOrderNotFrozen 拦截；
// 调用方须用「事务内 / 库内最新」的订单对象判定，不得拿冻结前的旧快照绕过
// （历史兜底函数 ensureOrderWritable 因无生产调用已删除，行为由 ensureOrderNotFrozen + 重读保证）。
func depositOrder(ctx context.Context, db *ent.Client, orderID, adminID int, now time.Time) (*ent.EsportsOrder, error) {
	if orderID < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	// 只有已付款、未存单、未结束的订单可存单。
	if order.PaymentStatus != esportsorder.PaymentStatusPaid ||
		order.DepositStatus != esportsorder.DepositStatusNone ||
		order.FulfillmentStatus == esportsorder.FulfillmentStatusCompleted ||
		order.FulfillmentStatus == esportsorder.FulfillmentStatusCancelled {
		return nil, errConflict
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
		esportsorder.DepositStatusEQ(esportsorder.DepositStatusNone),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusPaid),
	).SetDepositStatus(esportsorder.DepositStatusFrozen).
		SetDepositedAt(now.UTC()).SetDepositedByAdminID(adminID).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.deposited").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"deposit_status": "frozen", "note": "存单冻结标记，不改变履约状态、不释放名额",
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// restoreOrder 恢复存单：把冻结标记清掉（叫「恢复」，不叫「解冻」）。
// 恢复不动 fulfillment_status、不动名额占用，仅清空存单标记与存单时间。
func restoreOrder(ctx context.Context, db *ent.Client, orderID, adminID int, now time.Time) (*ent.EsportsOrder, error) {
	if orderID < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.DepositStatus != esportsorder.DepositStatusFrozen {
		return nil, errConflict
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
		esportsorder.DepositStatusEQ(esportsorder.DepositStatusFrozen),
	).SetDepositStatus(esportsorder.DepositStatusNone).
		ClearDepositedAt().ClearDepositedByAdminID().AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.restored").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"deposit_status": "none", "note": "恢复存单，仅清除冻结标记",
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

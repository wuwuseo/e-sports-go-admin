package esports

import (
	"context"
	"fmt"
	"time"

	"go-admin/internal/apps/esports/schematier"
	"go-admin/internal/ent"
)

// 旧订单保留已写入的逐席快照，新的单商品订单不再创建席位定义。
func orderSeatRoles(ctx context.Context, db *ent.Client, orderID int) ([]schematier.SeatSpec, error) {
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.SeatRoles == nil {
		return []schematier.SeatSpec{}, nil
	}
	return order.SeatRoles, nil
}

// 兼容内部历史调用；新报单直接使用单商品的服务量。
func placeOrderWithSeatRoles(ctx context.Context, db *ent.Client, adminID int, in orderInput, key string, now time.Time) (*ent.EsportsOrder, error) {
	return placeOrder(ctx, db, adminID, in, key, now)
}

// resolveSeatPromise 只读旧商品的服务量字段以保持历史订单和名额口径。
// 新商品固定 required_seats=1，不再关联规格组。
func resolveSeatPromise(sku *ent.EsportsSku) (int64, int64, []schematier.SeatSpec, error) {
	if sku == nil || sku.ServiceUnits <= 0 || sku.RequiredSeats < 1 {
		return 0, 0, nil, errInvalidInput
	}
	perSeat := sku.PerSeatServiceUnits
	if perSeat <= 0 {
		if sku.RequiredSeats > 1 {
			return 0, 0, nil, fmt.Errorf("%w：历史多人商品缺少每人服务量", errInvalidInput)
		}
		perSeat = sku.ServiceUnits
	}
	return perSeat, perSeat * int64(sku.RequiredSeats), []schematier.SeatSpec{}, nil
}

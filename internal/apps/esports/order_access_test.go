package esports

import (
	"errors"
	"testing"
)

func TestLoadOwnedOrder(t *testing.T) {
	db := openAfterSaleDB(t, "esports_owned_order")
	defer db.Close()
	ctx := t.Context()
	order := seedBalancePaidOrder(t, db, "owned-order")
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(11).SaveX(ctx)

	if _, err := loadOwnedOrder(ctx, db, 11, false, order.ID); err != nil {
		t.Fatalf("负责客服无法操作本人订单：%v", err)
	}
	if _, err := loadOwnedOrder(ctx, db, 22, false, order.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("其他客服应被拒绝：%v", err)
	}
	if _, err := loadOwnedOrder(ctx, db, 22, true, order.ID); err != nil {
		t.Fatalf("超级管理员无法处理订单：%v", err)
	}
	db.EsportsOrder.UpdateOneID(order.ID).ClearResponsibleAdminID().SaveX(ctx)
	if _, err := loadOwnedOrder(ctx, db, 11, false, order.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("未认领公共池订单应先认领：%v", err)
	}
}

func TestLoadOwnedAddon(t *testing.T) {
	db := openAfterSaleDB(t, "esports_owned_addon")
	defer db.Close()
	ctx := t.Context()
	order := seedBalancePaidOrder(t, db, "owned-addon")
	addon := db.EsportsOrderAddon.Create().SetAddonNo("OWNED-ADDON").SetOrderID(order.ID).SetAddedCents(100).SaveX(ctx)
	if _, err := loadOwnedAddon(ctx, db, 22, false, addon.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("其他客服访问加钟单：%v", err)
	}
	if _, err := loadOwnedAddon(ctx, db, 11, false, addon.ID); err != nil {
		t.Fatalf("负责客服无法访问加钟单：%v", err)
	}
}

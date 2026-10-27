package esports

import (
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
)

// TestListViewsCustomerName 守住订单 / 礼物单 / 礼物确认单三个列表视图的客户名补全：
// 有客户时补出 display_name，客户不存在（或脱敏 ID=0）时保持空串，由前端回落 #ID。
func TestListViewsCustomerName(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_list_view_names?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	customer := db.EsportsCustomer.Create().SetDisplayName("客户小红").SaveX(ctx)

	orders, err := adminOrderViewsOf(ctx, db, []*ent.EsportsOrder{
		{ID: 1, CustomerID: customer.ID},
		{ID: 2, CustomerID: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if orders[0].CustomerName != "客户小红" || orders[1].CustomerName != "" {
		t.Fatalf("订单视图客户名不符合预期：%+v", orders)
	}

	giftOrders, err := giftOrderViewsOf(ctx, db, []*ent.EsportsGiftOrder{
		{ID: 1, CustomerID: customer.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if giftOrders[0].CustomerName != "客户小红" {
		t.Fatalf("礼物单视图客户名不符合预期：%+v", giftOrders)
	}

	intents, err := adminGiftIntentViewsOf(ctx, db, []*ent.EsportsGiftIntent{
		{ID: 1, CustomerID: customer.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if intents[0].CustomerName != "客户小红" {
		t.Fatalf("礼物确认单视图客户名不符合预期：%+v", intents)
	}
}

package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayer"
)

// 报单多陪玩（多人单）：normalizePreferredPlayerIDs 的合并/去重/上限口径与落库快照。
func TestPlaceOrderWithMultiplePreferredPlayers(t *testing.T) {
	db := openProductDB(t, "esports_multi_preferred")
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "MULTI", Title: "双人陪玩", ServiceType: "play", UnitPriceCents: 10000,
		ServiceUnits: 2, ServiceUnitName: "局", RequiredSeats: 2, PerSeatServiceUnits: 1, QuotaTotal: 10, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	userSeq := 0
	newPlayer := func(name string, qualification esportsplayer.Qualification) *ent.EsportsPlayer {
		userSeq += 1
		player := db.EsportsPlayer.Create().SetUserID(9000 + userSeq).SetDisplayName(name).
			SetQualification(qualification).SetPresence(esportsplayer.PresenceOnline).
			SetLevel("novice").SaveX(ctx)
		return player
	}
	p1 := newPlayer("主陪玩", esportsplayer.QualificationQualified)
	p2 := newPlayer("固排陪玩", esportsplayer.QualificationQualified)
	p3 := newPlayer("第三陪玩", esportsplayer.QualificationQualified)
	unqualified := newPlayer("未合格", esportsplayer.QualificationPending)

	// 多陪玩落单：第一个为主陪玩，整体快照落到订单。
	order, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		PreferredPlayerIDs: []int{p1.ID, p2.ID},
	}, "multi-preferred-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if order.PreferredPlayerID == nil || *order.PreferredPlayerID != p1.ID {
		t.Fatalf("主陪玩应为第一个指定陪玩：%+v", order.PreferredPlayerID)
	}
	if len(order.PreferredPlayerIds) != 2 || order.PreferredPlayerIds[0] != p1.ID ||
		order.PreferredPlayerIds[1] != p2.ID {
		t.Fatalf("多陪玩快照错误：%+v", order.PreferredPlayerIds)
	}

	// 超过商品席位数：拒绝。
	if _, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		PreferredPlayerIDs: []int{p1.ID, p2.ID, p3.ID},
	}, "multi-preferred-2", time.Now()); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超过席位数应拒绝：%v", err)
	}

	// 重复 ID 去重后不超过上限：放行且快照只保留唯一项。
	dedup, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		PreferredPlayerIDs: []int{p1.ID, p1.ID},
	}, "multi-preferred-3", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(dedup.PreferredPlayerIds) != 1 || dedup.PreferredPlayerIds[0] != p1.ID {
		t.Fatalf("重复 ID 应去重：%+v", dedup.PreferredPlayerIds)
	}

	// 任一指定陪玩资质不合格：整体拒绝（与单陪玩口径一致）。
	if _, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		PreferredPlayerIDs: []int{p1.ID, unqualified.ID},
	}, "multi-preferred-4", time.Now()); !errors.Is(err, errInvalidInput) {
		t.Fatalf("不合格陪玩应拒绝：%v", err)
	}

	// 单字段与多陪玩同时传：单字段作为首项兜底。
	mixed, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		PreferredPlayerID: p2.ID, PreferredPlayerIDs: []int{p1.ID},
	}, "multi-preferred-5", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if mixed.PreferredPlayerIds[0] != p2.ID || len(mixed.PreferredPlayerIds) != 2 {
		t.Fatalf("单字段应作为首项：%+v", mixed.PreferredPlayerIds)
	}
}

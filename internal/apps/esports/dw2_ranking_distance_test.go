package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
)

// dw2LocatedPlayer 造一个带定位/时区的合格在线陪玩。
func dw2LocatedPlayer(t *testing.T, db *ent.Client, userID int, timezone string, latE6, lonE6 int) *ent.EsportsPlayer {
	t.Helper()
	player := seedPlayer(t, db, userID, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetTimezone(timezone).
		SetLatitudeE6(latE6).SetLongitudeE6(lonE6).SaveX(t.Context())
	return db.EsportsPlayer.GetX(t.Context(), player.ID)
}

// ：排序分必须含可复现、可断言的「距离或时区」分量。
//   - 权重为 0（默认）时排序行为与既有完全一致（不参与排序）；
//   - 打开 distance 权重后，离客户更近的候选排前；
//   - 打开 timezone 权重后，与客户同时区的候选排前。
func TestDw2DispatchRankingDistanceAndTimezone(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dw2_rank_distance")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"

	sku := seedGameSku(t, db, "DW2D", game)
	customer := db.EsportsCustomer.Create().SetDisplayName("上海客户").
		SetTimezone("Asia/Shanghai").SetLatitudeE6(31230000).SetLongitudeE6(121470000).SaveX(ctx)
	order := db.EsportsOrder.Create().SetOrderNo("DW2-RANK-1").SetCustomerID(customer.ID).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(ctx)

	// 先建「远」的玩家（档案 ID 更小），默认顺序下它本该排第一；再建「近」的。
	far := dw2LocatedPlayer(t, db, 9001, "Asia/Tokyo", 39900000, 116400000) // 北京
	near := dw2LocatedPlayer(t, db, 9002, "Asia/Shanghai", 31230000, 121470000)

	// ① 权重全部为 0（含缺省 distance/timezone）：排序行为与既有一致 —— 按档案 ID 稳定兜底。
	setUpConfig(t, db, dispatchRankWeightsKey,
		`{"rating":40,"history":30,"preference":60,"balance":20}`)
	items, err := dispatchCandidates(ctx, db, order, 0, 10, now)
	if err != nil {
		t.Fatalf("候选排序应成功：%v", err)
	}
	if len(items) != 2 || items[0].PlayerID != far.ID {
		t.Fatalf("距离/时区权重为 0 时不得改变既有排序：%+v", dw2CandidateIDs(items))
	}

	// ② 只打开距离分量：近的排前。
	setUpConfig(t, db, dispatchRankWeightsKey,
		`{"rating":0,"history":0,"preference":0,"balance":0,"distance":100,"timezone":0}`)
	items, err = dispatchCandidates(ctx, db, order, 0, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].PlayerID != near.ID || items[1].PlayerID != far.ID {
		t.Fatalf("距离分量未生效：%+v", dw2CandidateIDs(items))
	}
	if items[0].DistanceMeters > 5000 || items[1].DistanceMeters < 1000000 {
		t.Fatalf("距离展示字段不正确：near=%d far=%d", items[0].DistanceMeters, items[1].DistanceMeters)
	}

	// ③ 只打开时区分量：同客户时区的排前。
	setUpConfig(t, db, dispatchRankWeightsKey,
		`{"rating":0,"history":0,"preference":0,"balance":0,"distance":0,"timezone":100}`)
	items, err = dispatchCandidates(ctx, db, order, 0, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].PlayerID != near.ID {
		t.Fatalf("时区分量未生效：%+v", dw2CandidateIDs(items))
	}
	if !items[0].TimezoneMatched || items[1].TimezoneMatched {
		t.Fatalf("时区匹配展示字段不正确：near=%v far=%v",
			items[0].TimezoneMatched, items[1].TimezoneMatched)
	}

	// ④ 客户无定位/时区时不产生任何加分（存量数据不因新分量抖动）。
	db.EsportsCustomer.UpdateOneID(customer.ID).SetLatitudeE6(0).SetLongitudeE6(0).
		SetTimezone("").SaveX(ctx)
	items, err = dispatchCandidates(ctx, db, order, 0, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].PlayerID != far.ID {
		t.Fatalf("客户无定位时应回落「无加分」并稳定兜底：%+v", dw2CandidateIDs(items))
	}
	if items[0].DistanceMeters != 0 || items[1].DistanceMeters != 0 {
		t.Fatal("客户无定位时距离展示字段应为 0（未知），不得臆造")
	}
}

// dw2CandidateIDs 便于失败信息定位。
func dw2CandidateIDs(items []*dispatchCandidate) []int {
	ids := make([]int, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.PlayerID)
	}
	return ids
}

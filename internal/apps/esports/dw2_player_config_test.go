package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
)

// dw2WithinSlot 返回一个落在指定 UTC+8 分钟窗口内的「当前时刻」，用于服务时段用例。
func dw2WithinSlot(t *testing.T, startMinute, endMinute int) time.Time {
	t.Helper()
	base := time.Now().In(cstZone)
	day := time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, cstZone)
	// 取窗口中间（跨天窗口取 start 之后 30 分钟，按 24h 取模）。
	minutes := startMinute
	if endMinute > startMinute {
		minutes = startMinute + (endMinute-startMinute)/2
	} else if endMinute == 0 {
		minutes = startMinute
	} else {
		minutes = (startMinute + 24*60) % (24 * 60)
	}
	return day.Add(time.Duration(minutes) * time.Minute)
}

// §6.4-06：服务时段 —— 设置服务时段的陪玩在时段外不被派单（候选池与人工指派双向拦截）。
func TestDw2ServiceSlotBlocksDispatchOutsideWindow(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dw2_service_slot")
	defer db.Close()
	ctx := t.Context()
	now := dw2WithinSlot(t, 0, 0)
	admin := seedAdminAccount(t, db, "dw2slotadmin")

	// 窗口取「现在所在小时」的整点区间，保证 now 在窗口内、now+2h 在窗口外。
	local := now.In(cstZone)
	startMinute := local.Hour() * 60
	endMinute := startMinute + 60
	inside := now
	outside := inside.Add(2 * time.Hour)

	outsidePlayer := seedPlayer(t, db, 9401, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(outsidePlayer.ID).
		SetServiceSlotStartMinute(startMinute).SetServiceSlotEndMinute(endMinute).SaveX(ctx)
	anyTimePlayer := seedPlayer(t, db, 9402, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	sku := seedGameSku(t, db, "DW2SLOT", "王者荣耀")
	order := dw2AppointmentOrder(t, db, "DW2-SLOTCFG-1", sku.ID, inside.Add(time.Hour), 60)

	// 时段外：候选池不出现该陪玩。
	items, err := dispatchCandidates(ctx, db, order, 0, 10, outside)
	if err != nil {
		t.Fatalf("候选排序应成功：%v", err)
	}
	for _, item := range items {
		if item.PlayerID == outsidePlayer.ID {
			t.Fatalf("服务时段外的陪玩不得进入候选池：%+v", dw2CandidateIDs(items))
		}
	}
	// 时段内：候选池出现。
	items, err = dispatchCandidates(ctx, db, order, 0, 10, inside)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item.PlayerID == outsidePlayer.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("服务时段内应进入候选池：%+v", dw2CandidateIDs(items))
	}
	// 人工指派同样在时段外被拒，且给出中文原因。
	if _, err = assignDispatchSeat(ctx, db, order.ID, 1, outsidePlayer.ID, admin.ID, outside); err == nil {
		t.Fatal("服务时段外的人工指派必须被拒")
	}
	// 未配置时段的陪玩不受影响。
	if _, err = assignDispatchSeat(ctx, db, order.ID, 1, anyTimePlayer.ID, admin.ID, outside); err != nil {
		t.Fatalf("未配置服务时段的陪玩应可指派：%v", err)
	}
}

// §6.4-06：可接单价范围 —— 超出本人设定价区间的订单不被派单。
func TestDw2AcceptPriceRangeBlocksDispatch(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dw2_accept_price")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	admin := seedAdminAccount(t, db, "dw2pricerange")

	cheapOnly := seedPlayer(t, db, 9411, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(cheapOnly.ID).
		SetAcceptPriceMinCents(500).SetAcceptPriceMaxCents(1500).SaveX(ctx)
	premium := seedPlayer(t, db, 9412, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(premium.ID).
		SetAcceptPriceMinCents(3000).SetAcceptPriceMaxCents(0).SaveX(ctx)

	sku := seedGameSku(t, db, "DW2PRICE", "王者荣耀")
	// 每席 2000 分的订单：cheapOnly 上限 1500 → 不接；premium 下限 3000 → 不接。
	order := dw2AppointmentOrder(t, db, "DW2-PRICE-1", sku.ID, now.Add(time.Hour), 60)
	db.EsportsOrder.UpdateOneID(order.ID).SetUnitPriceCents(2000).SetPayableCents(2000).SaveX(ctx)
	order = db.EsportsOrder.GetX(ctx, order.ID)

	items, err := dispatchCandidates(ctx, db, order, 0, 10, now)
	if err != nil {
		t.Fatalf("候选排序应成功：%v", err)
	}
	if len(items) != 0 {
		t.Fatalf("价区间不匹配的陪玩不得进入候选池：%+v", dw2CandidateIDs(items))
	}
	if _, err = assignDispatchSeat(ctx, db, order.ID, 1, cheapOnly.ID, admin.ID, now); err == nil {
		t.Fatal("低于可接价下限 / 高于上限的指派必须被拒")
	}
	// 调价到 1200 分（落在 cheapOnly 区间内、低于 premium 下限）→ 只有 cheapOnly 可派。
	db.EsportsOrder.UpdateOneID(order.ID).SetUnitPriceCents(1200).SetPayableCents(1200).SaveX(ctx)
	order = db.EsportsOrder.GetX(ctx, order.ID)
	items, err = dispatchCandidates(ctx, db, order, 0, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].PlayerID != cheapOnly.ID {
		t.Fatalf("应按可接单价范围过滤：%+v", dw2CandidateIDs(items))
	}
	if _, err = assignDispatchSeat(ctx, db, order.ID, 1, cheapOnly.ID, admin.ID, now); err != nil {
		t.Fatalf("区间内的指派应成功：%v", err)
	}
}

// §6.4-06：自动上线 —— 开启后，服务时段内心跳保活自动把陪玩置为在线（时段外不动）。
func TestDw2AutoOnlineOnHeartbeat(t *testing.T) {
	db := openTransferDB(t, "esports_dw2_auto_online")
	defer db.Close()
	ctx := t.Context()
	now := dw2WithinSlot(t, 0, 0)
	local := now.In(cstZone)
	startMinute := local.Hour() * 60
	endMinute := startMinute + 60

	auto := seedPlayer(t, db, 9421, esportsplayer.QualificationQualified, esportsplayer.PresenceOffline)
	db.EsportsPlayer.UpdateOneID(auto.ID).SetAutoOnline(true).
		SetServiceSlotStartMinute(startMinute).SetServiceSlotEndMinute(endMinute).SaveX(ctx)
	manual := seedPlayer(t, db, 9422, esportsplayer.QualificationQualified, esportsplayer.PresenceOffline)

	// 时段内：自动上线。
	if _, err := recordPlayerHeartbeat(ctx, db, auto.ID, now); err != nil {
		t.Fatalf("心跳应成功：%v", err)
	}
	if got := db.EsportsPlayer.GetX(ctx, auto.ID); got.Presence != esportsplayer.PresenceOnline {
		t.Fatalf("开启自动上线且处于服务时段内，应自动置在线：%v", got.Presence)
	}
	// 未开启自动上线的陪玩不受影响。
	if _, err := recordPlayerHeartbeat(ctx, db, manual.ID, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsPlayer.GetX(ctx, manual.ID); got.Presence != esportsplayer.PresenceOffline {
		t.Fatalf("未开启自动上线不得改变在线状态：%v", got.Presence)
	}
	// 时段外不下线（自动上线只负责上线，不在时段外踢人）。
	db.EsportsPlayer.UpdateOneID(auto.ID).SetPresence(esportsplayer.PresenceOnline).SaveX(ctx)
	if _, err := recordPlayerHeartbeat(ctx, db, auto.ID, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsPlayer.GetX(ctx, auto.ID); got.Presence != esportsplayer.PresenceOnline {
		t.Fatalf("自动上线不得在时段外自动下线：%v", got.Presence)
	}
}

// §6.4-06：固排开关与固排时段 —— 开启后仅在固排时段内可被固排成队。
func TestDw2FixedTeamSlotGatesTeamDispatch(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dw2_fixed_team_slot")
	defer db.Close()
	ctx := t.Context()
	now := dw2WithinSlot(t, 0, 0)
	game := "王者荣耀"
	local := now.In(cstZone)
	startMinute := local.Hour() * 60
	endMinute := startMinute + 60

	main := seedPlayer(t, db, 9431, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	teammate := seedPlayer(t, db, 9432, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(teammate.ID).SetFixedTeamEnabled(true).
		SetFixedTeamSlotStartMinute(startMinute).SetFixedTeamSlotEndMinute(endMinute).SaveX(ctx)

	rel, err := requestTeammate(ctx, db, main.ID, teammate.ID, game, "dw2-ft-rel-0001", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptTeammateRequest(ctx, db, teammate.ID, rel.ID, now); err != nil {
		t.Fatal(err)
	}
	sku := seedGameSku(t, db, "DW2FT", game)
	mkOrder := func(no string) int {
		return db.EsportsOrder.Create().SetOrderNo(no).SetCustomerID(1).SetSkuID(sku.ID).
			SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
			SetPromisedServiceUnits(1).SetRequiredSeats(2).
			SetPaymentDeadline(now.Add(time.Hour)).
			SetPaymentStatus(esportsorder.PaymentStatusPaid).
			SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
			SaveX(ctx).ID
	}

	// 固排时段内：候选可见且可成队。
	insideOrder := mkOrder("DW2-FT-1")
	cands, err := listTeammateCandidatesAt(ctx, db, insideOrder, main.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].PlayerID != teammate.ID {
		t.Fatalf("固排时段内应可见固排候选：%+v", cands)
	}
	if _, err = teamDispatch(ctx, db, insideOrder, main.ID, []int{teammate.ID}, 11, now); err != nil {
		t.Fatalf("固排时段内成队应成功：%v", err)
	}
	// 固排时段外：候选不可见，成队被拒。
	outsideOrder := mkOrder("DW2-FT-2")
	cands, err = listTeammateCandidatesAt(ctx, db, outsideOrder, main.ID, now.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Fatalf("固排时段外不得出现候选：%+v", cands)
	}
	if _, err = teamDispatch(ctx, db, outsideOrder, main.ID, []int{teammate.ID}, 11, now.Add(3*time.Hour)); err == nil {
		t.Fatal("固排时段外成队必须被拒")
	}
	// 未开启固排时段限制的陪玩不受影响（兼容存量：开关默认 false 即不限时段）。
	open := seedPlayer(t, db, 9433, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	rel2, err := requestTeammate(ctx, db, main.ID, open.ID, game, "dw2-ft-rel-0002", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptTeammateRequest(ctx, db, open.ID, rel2.ID, now); err != nil {
		t.Fatal(err)
	}
	openOrder := mkOrder("DW2-FT-3")
	if _, err = teamDispatch(ctx, db, openOrder, main.ID, []int{open.ID}, 11, now.Add(3*time.Hour)); err != nil {
		t.Fatalf("未开启固排时段限制的固排应可成队：%v", err)
	}
}

// §6.4-06：五项配置必须可由陪玩本人写入（服务时段 / 自动上线 / 可接价 / 固排开关 / 固排时段）。
func TestDw2PlayerAcceptConfigSelfService(t *testing.T) {
	db := openTransferDB(t, "esports_dw2_accept_config")
	defer db.Close()
	ctx := t.Context()
	player := seedPlayer(t, db, 9441, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	startMinute, endMinute := 540, 120
	fixedStart, fixedEnd := 1080, 0
	autoOnline := true
	fixedTeam := true
	minPrice, maxPrice := int64(800), int64(2600)
	in := playerSelfProfileInput{
		DisplayName:            "配置陪玩",
		ServiceSlotStartMinute: &startMinute, ServiceSlotEndMinute: &endMinute,
		AutoOnline:          &autoOnline,
		AcceptPriceMinCents: &minPrice, AcceptPriceMaxCents: &maxPrice,
		FixedTeamEnabled:         &fixedTeam,
		FixedTeamSlotStartMinute: &fixedStart, FixedTeamSlotEndMinute: &fixedEnd,
	}
	if _, err := updatePlayerSelfProfile(ctx, db, player.ID, in); err != nil {
		t.Fatalf("自助写入接单配置应成功：%v", err)
	}
	got := db.EsportsPlayer.GetX(ctx, player.ID)
	if got.ServiceSlotStartMinute != startMinute || got.ServiceSlotEndMinute != endMinute ||
		!got.AutoOnline || got.AcceptPriceMinCents != minPrice || got.AcceptPriceMaxCents != maxPrice ||
		!got.FixedTeamEnabled || got.FixedTeamSlotStartMinute != fixedStart ||
		got.FixedTeamSlotEndMinute != fixedEnd {
		t.Fatalf("接单配置未落库：%+v", got)
	}
	// 非法分钟数必须被拒（0~1439）。
	bad := 1440
	badIn := playerSelfProfileInput{DisplayName: "配置陪玩", ServiceSlotStartMinute: &bad}
	if _, err := updatePlayerSelfProfile(ctx, db, player.ID, badIn); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法服务时段应被拒：%v", err)
	}
	// 价区间上下限颠倒必须被拒。
	lo, hi := int64(3000), int64(1000)
	badIn = playerSelfProfileInput{DisplayName: "配置陪玩", AcceptPriceMinCents: &lo, AcceptPriceMaxCents: &hi}
	if _, err := updatePlayerSelfProfile(ctx, db, player.ID, badIn); !errors.Is(err, errInvalidInput) {
		t.Fatalf("价区间上下限颠倒应被拒：%v", err)
	}
	// 管理端同口径可改（未传的字段不覆盖）。
	newStart := 600
	if _, err := updatePlayer(ctx, db, player.ID, playerUpdateInput{
		DisplayName: "配置陪玩", Qualification: "pending", Presence: "online",
		ServiceSlotStartMinute: &newStart,
	}); err != nil {
		t.Fatalf("管理端修改服务时段应成功：%v", err)
	}
	after := db.EsportsPlayer.GetX(ctx, player.ID)
	if after.ServiceSlotStartMinute != newStart || after.ServiceSlotEndMinute != endMinute ||
		!after.AutoOnline || after.AcceptPriceMinCents != minPrice {
		t.Fatalf("管理端改动不应覆盖未传字段：%+v", after)
	}
}

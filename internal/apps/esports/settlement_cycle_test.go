package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportssettlement"
)

// seedPendingCommissionEntry 直接写入一条待结算的服务分成明细（聚焦周期与冻结口径）。
func seedPendingCommissionEntry(t *testing.T, db *ent.Client, idem string, orderID, playerID int,
	amount int64, at time.Time) *ent.EsportsCommissionEntry {
	t.Helper()
	return db.EsportsCommissionEntry.Create().
		SetIdemKey(idem).SetOrderID(orderID).SetPlayerID(playerID).
		SetKind(esportscommissionentry.KindService).SetSeatIndex(1).
		SetBaseCents(amount * 2).SetAmountCents(amount).
		SetCreatedAt(at).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(t.Context())
}

// 周期配置校验：维度与键值、周期与间隔天数的非法组合一律拒绝。
func TestSettlementCycleValidation(t *testing.T) {
	db := openAfterSaleDB(t, "esports_settlement_cycle_validation")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	invalid := []settlementCycleInput{
		{Scope: "global", ScopeKey: "x", Period: "weekly"},
		{Scope: "game", Period: "weekly"},
		{Scope: "level", ScopeKey: "legend", Period: "weekly"},
		{Scope: "player", ScopeKey: "abc", Period: "weekly"},
		{Scope: "player", ScopeKey: "3", Period: "weekly", IntervalDays: 5},
		{Scope: "global", Period: "custom"},
		{Scope: "global", Period: "custom", IntervalDays: 400},
	}
	for i, in := range invalid {
		if _, err := createSettlementCycle(ctx, db, in, 11, now); !errors.Is(err, errInvalidInput) {
			t.Fatalf("第 %d 组非法周期配置应被拒，err=%v", i+1, err)
		}
	}
	created, err := createSettlementCycle(ctx, db, settlementCycleInput{
		Scope: "player", ScopeKey: "7", Period: "custom", IntervalDays: 10, Note: "陪玩专属十日结",
	}, 11, now)
	if err != nil || created.Period.String() != "custom" || created.IntervalDays != 10 || !created.Enabled {
		t.Fatalf("合法自定义周期应创建成功：%+v err=%v", created, err)
	}
	// 同一维度同一 key 只保留一条。
	if _, err = createSettlementCycle(ctx, db, settlementCycleInput{
		Scope: "player", ScopeKey: "7", Period: "daily",
	}, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复配置同维度同 key 应冲突，err=%v", err)
	}
	// 非自定义周期不允许带间隔天数；停用后不再参与解析。
	if _, err = updateSettlementCycle(ctx, db, created.ID, settlementCycleInput{
		Period: "daily", IntervalDays: 3,
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非自定义周期带间隔天数应被拒，err=%v", err)
	}
	if _, err = setSettlementCycleEnabled(ctx, db, created.ID, false, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsSettlementCycle.GetX(ctx, created.ID); got.Enabled {
		t.Fatalf("停用未生效：%+v", got)
	}
}

// 逐级覆盖解析：陪玩 → 等级 → 游戏 → 全局；停用后回落下一级，全缺时回落系统配置。
func TestSettlementCycleResolutionPrefersMostSpecific(t *testing.T) {
	db := openAfterSaleDB(t, "esports_settlement_cycle_resolve")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedPlayer(t, db, 4711, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetLevel("senior").
		SetGames([]string{"王者荣耀", "和平精英"}).SaveX(ctx)
	player = db.EsportsPlayer.GetX(ctx, player.ID)

	mustCreate := func(in settlementCycleInput) *ent.EsportsSettlementCycle {
		t.Helper()
		row, err := createSettlementCycle(ctx, db, in, 11, now)
		if err != nil {
			t.Fatalf("创建周期配置失败：%v", err)
		}
		return row
	}
	global := mustCreate(settlementCycleInput{Scope: "global", Period: "weekly"})
	game := mustCreate(settlementCycleInput{Scope: "game", ScopeKey: "王者荣耀", Period: "daily"})
	level := mustCreate(settlementCycleInput{Scope: "level", ScopeKey: "senior", Period: "monthly"})
	self := mustCreate(settlementCycleInput{Scope: "player", ScopeKey: strconv.Itoa(player.ID), Period: "custom", IntervalDays: 14})

	resolve := func() effectiveSettlementCycle {
		t.Helper()
		cycle, err := resolveSettlementCycle(ctx, db, db.EsportsPlayer.GetX(ctx, player.ID))
		if err != nil {
			t.Fatal(err)
		}
		return cycle
	}
	if got := resolve(); got.Scope != "player" || got.Period != "custom" || got.IntervalDays != 14 {
		t.Fatalf("应命中陪玩维度，got=%+v", got)
	}
	if _, err := setSettlementCycleEnabled(ctx, db, self.ID, false, now); err != nil {
		t.Fatal(err)
	}
	if got := resolve(); got.Scope != "level" || got.Period != "monthly" {
		t.Fatalf("陪玩维度停用后应命中等级维度，got=%+v", got)
	}
	if _, err := setSettlementCycleEnabled(ctx, db, level.ID, false, now); err != nil {
		t.Fatal(err)
	}
	if got := resolve(); got.Scope != "game" || got.Period != "daily" {
		t.Fatalf("等级维度停用后应命中游戏维度，got=%+v", got)
	}
	if _, err := setSettlementCycleEnabled(ctx, db, game.ID, false, now); err != nil {
		t.Fatal(err)
	}
	if got := resolve(); got.Scope != "global" || got.Period != "weekly" {
		t.Fatalf("游戏维度停用后应命中全局默认，got=%+v", got)
	}
	if _, err := setSettlementCycleEnabled(ctx, db, global.ID, false, now); err != nil {
		t.Fatal(err)
	}
	if got := resolve(); got.Scope != "system_config" || got.Period != "weekly" {
		t.Fatalf("全部停用后应回落系统配置（缺省 weekly），got=%+v", got)
	}
}

// 自定义间隔窗口：以 epoch 对齐切分，窗口长度等于间隔天数且首尾相接。
func TestSettlementCycleCustomWindow(t *testing.T) {
	interval := 10
	cycle := effectiveSettlementCycle{Period: "custom", IntervalDays: interval}
	now := time.Date(2026, 10, 16, 13, 45, 30, 0, time.UTC)
	start, end, next := settlementCycleWindow(cycle, now)
	if !start.Before(now.UTC()) || !now.UTC().Before(end) {
		t.Fatalf("窗口应包含当前时刻：start=%s end=%s now=%s", start, end, now)
	}
	if got := end.Sub(start); got != time.Duration(interval)*24*time.Hour {
		t.Fatalf("窗口长度应为 %d 天，got=%s", interval, got)
	}
	if !next.Equal(end) {
		t.Fatalf("下一周期起点应等于本周期结束：next=%s end=%s", next, end)
	}
	// epoch 对齐：窗口起点距 1970-01-01 的天数必须是间隔的整数倍。
	epoch := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	days := int(start.Sub(epoch).Hours() / 24)
	if days%interval != 0 {
		t.Fatalf("窗口起点未与 epoch 对齐：days=%d interval=%d", days, interval)
	}
	// 上一完整周期首尾相接。
	prevStart, prevEnd := previousSettlementCycleWindow(cycle, now)
	if !prevEnd.Equal(start) || !prevStart.Before(prevEnd) {
		t.Fatalf("上一完整周期应截至本周期起点：prev=[%s,%s) start=%s", prevStart, prevEnd, start)
	}
}

// 自动结算：按各陪玩生效周期取「上一完整周期」生成结算单，同周期幂等，冻结明细不参与。
func TestGenerateDuePlayerSettlementsUsesPlayerCycle(t *testing.T) {
	db := openAfterSaleDB(t, "esports_settlement_cycle_generate")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	if _, err := createSettlementCycle(ctx, db, settlementCycleInput{
		Scope: "global", Period: "custom", IntervalDays: 7,
	}, 11, now); err != nil {
		t.Fatal(err)
	}
	cycle := effectiveSettlementCycle{Period: "custom", IntervalDays: 7}
	start, end := previousSettlementCycleWindow(cycle, now)

	player := seedPlayer(t, db, 4712, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	other := seedPlayer(t, db, 4713, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seedPendingCommissionEntry(t, db, "cycle-1", 1, player.ID, 300, start.Add(time.Hour))
	// 冻结明细不构成「可结算」依据：另一陪玩只有冻结明细，不应生成结算单。
	frozen := seedPendingCommissionEntry(t, db, "cycle-2", 1, other.ID, 500, start.Add(2*time.Hour))
	db.EsportsCommissionEntry.UpdateOneID(frozen.ID).SetFrozenAt(now).SetFrozenReason("争议").
		SetFrozenByAdminID(11).SaveX(ctx)

	created, err := generateDuePlayerSettlements(ctx, db, now, 200)
	if err != nil || created != 1 {
		t.Fatalf("应只为有可结算明细的陪玩生成 1 张结算单，created=%d err=%v", created, err)
	}
	settlements := db.EsportsSettlement.Query().Where(
		esportssettlement.OwnerTypeEQ(esportssettlement.OwnerTypePlayer),
	).AllX(ctx)
	if len(settlements) != 1 {
		t.Fatalf("结算单数量不正确：%d", len(settlements))
	}
	got := settlements[0]
	if got.OwnerID != player.ID || got.NetCents != 300 || got.EntryCount != 1 ||
		!got.PeriodStart.Equal(start) || !got.PeriodEnd.Equal(end) {
		t.Fatalf("结算单口径不正确：%+v", got)
	}
	// 幂等：同陪玩同周期不重复生成。
	if created, err = generateDuePlayerSettlements(ctx, db, now, 200); err != nil || created != 0 {
		t.Fatalf("重复执行不应新增结算单，created=%d err=%v", created, err)
	}
}

// 争议冻结（§12.6）：冻结明细不参与结算，解冻后恢复；人工冻结可按陪玩收窄。
func TestCommissionFreezeExcludesEntryFromSettlement(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_freeze")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedPlayer(t, db, 4714, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedCloseableOrder(t, db, "freeze-1", 2000, []segSeed{{player.ID, 1, 600}})
	entry := seedPendingCommissionEntry(t, db, "freeze-1-service", order.ID, player.ID, 400, now)

	frozen, err := freezeOrderCommissionForDispute(ctx, db, order.ID, nil, "客户争议", 11, now)
	if err != nil || frozen != 1 {
		t.Fatalf("冻结应命中 1 条明细，frozen=%d err=%v", frozen, err)
	}
	// 重复冻结不新增。
	if frozen, err = freezeOrderCommissionForDispute(ctx, db, order.ID, nil, "客户争议", 11, now); err != nil || frozen != 0 {
		t.Fatalf("重复冻结不应命中，frozen=%d err=%v", frozen, err)
	}
	if _, err = settlePlayerCommission(ctx, db, player.ID, now.Add(-time.Hour), now.Add(time.Hour), 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("冻结明细不应参与结算，err=%v", err)
	}
	got := db.EsportsCommissionEntry.GetX(ctx, entry.ID)
	if got.FrozenAt == nil || got.FrozenReason != "客户争议" || got.FrozenByAdminID == nil {
		t.Fatalf("冻结留痕不完整：%+v", got)
	}
	if unfrozen, err := unfreezeOrderCommission(ctx, db, order.ID); err != nil || unfrozen != 1 {
		t.Fatalf("解冻应命中 1 条，unfrozen=%d err=%v", unfrozen, err)
	}
	settlement, err := settlePlayerCommission(ctx, db, player.ID, now.Add(-time.Hour), now.Add(time.Hour), 11, now)
	if err != nil || settlement.NetCents != 400 || settlement.EntryCount != 1 {
		t.Fatalf("解冻后应可结算：%+v err=%v", settlement, err)
	}
	// 已结算明细不再被人工冻结影响。
	if frozen, err = freezeOrderCommissionForDispute(ctx, db, order.ID, nil, "二次争议", 11, now); err != nil || frozen != 0 {
		t.Fatalf("已结算明细不应被冻结，frozen=%d err=%v", frozen, err)
	}
}

// 售后联动：受理即冻结该订单待结算分成，驳回 / 执行完成后解冻。
func TestAfterSaleFreezesAndUnfreezesCommission(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_freeze_link")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	order := seedBalancePaidOrder(t, db, "freeze-link")
	entry := seedPendingCommissionEntry(t, db, "freeze-link-service", order.ID, 1, 600, now)

	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "refund_request", Channel: "customer",
		Demand: "客户对本次服务提出争议", ExpectedPlan: "待核实",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptAfterSale(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsCommissionEntry.GetX(ctx, entry.ID); got.FrozenAt == nil {
		t.Fatalf("受理售后应冻结该订单待结算分成：%+v", got)
	}
	if _, err = submitAfterSalePlan(ctx, db, created.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: 500, Resolution: "按差额退款",
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = confirmAfterSaleByAdmin(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	// 待审批阶段驳回：争议终结即解冻。
	if _, err = rejectAfterSale(ctx, db, created.ID, 12, afterSaleDecisionInput{Reason: "证据不足"}, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsCommissionEntry.GetX(ctx, entry.ID); got.FrozenAt != nil {
		t.Fatalf("驳回后应解冻分成明细：%+v", got)
	}

	// 执行完成后同样解冻。
	second, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "service_mismatch", Channel: "admin",
		Demand: "再次确认服务差异", ExpectedPlan: "按差额退款",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptAfterSale(ctx, db, second.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsCommissionEntry.GetX(ctx, entry.ID); got.FrozenAt == nil {
		t.Fatalf("二次受理应再次冻结：%+v", got)
	}
	if _, err = submitAfterSalePlan(ctx, db, second.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: 500, Resolution: "按差额退款",
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = confirmAfterSaleByAdmin(ctx, db, second.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err = approveAfterSale(ctx, db, second.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if _, err = executeAfterSale(ctx, db, second.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsCommissionEntry.GetX(ctx, entry.ID); got.FrozenAt != nil {
		t.Fatalf("执行完成后应解冻分成明细：%+v", got)
	}
}

package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportssettlement"
)

// 上一完整周期窗口：日 / 周 / 月（2026-10-14 为周三）。
func TestCsCommissionPreviousPeriodWindow(t *testing.T) {
	now := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)
	cases := []struct {
		period    string
		wantStart time.Time
		wantEnd   time.Time
	}{
		{"daily",
			time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)},
		{"weekly",
			time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)},
		{"monthly",
			time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		start, end := csCommissionPreviousPeriodWindow(c.period, now)
		if !start.Equal(c.wantStart) || !end.Equal(c.wantEnd) {
			t.Fatalf("%s 上一周期应为 [%s, %s)，实际 [%s, %s)",
				c.period, c.wantStart, c.wantEnd, start, end)
		}
	}
}

// 自动生成结算单（§12.2）：只结算上一完整周期、当前周期不受影响、重复运行幂等。
func TestGenerateDueCsSettlements(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_commission_auto")
	defer db.Close()
	ctx := t.Context()
	admin := seedAdmin(t, db, "cc-auto-admin")
	now := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)

	rule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 1000, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, rule.ID, admin.ID, now); err != nil {
		t.Fatal(err)
	}
	seedEntry := func(amountCents int64, at time.Time, key string) {
		t.Helper()
		db.EsportsCsCommissionEntry.Create().SetIdemKey(key).
			SetOrderID(1).SetAdminID(admin.ID).
			SetKind(esportscscommissionentry.KindService).
			SetCalcMode(esportscscommissionentry.CalcModePercent).
			SetBaseMode(esportscscommissionentry.BaseModeOrderAmount).
			SetBaseCents(amountCents * 10).SetAmountCents(amountCents).
			SetStatus(esportscscommissionentry.StatusPendingSettlement).
			SetCreatedAt(at).SaveX(ctx)
	}
	seedEntry(1000, time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC), "cc-auto-sep")
	seedEntry(500, time.Date(2026, 10, 3, 3, 0, 0, 0, time.UTC), "cc-auto-oct")

	created, err := generateDueCsSettlements(ctx, db, now, 200)
	if err != nil || created != 1 {
		t.Fatalf("应生成 1 张结算单：%v %d", err, created)
	}
	items := db.EsportsSettlement.Query().Where(
		esportssettlement.OwnerTypeEQ(esportssettlement.OwnerTypeCs),
	).AllX(ctx)
	if len(items) != 1 {
		t.Fatalf("结算单应为 1 张：%d", len(items))
	}
	item := items[0]
	if item.OwnerID != admin.ID || item.NetCents != 1000 || item.EntryCount != 1 ||
		!item.PeriodStart.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) ||
		!item.PeriodEnd.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("结算单不正确：%+v", item)
	}
	// 上一周期明细转 settled，当前周期明细仍待结算。
	settled := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.StatusEQ(esportscscommissionentry.StatusSettled),
	).CountX(ctx)
	if settled != 1 {
		t.Fatalf("应只结算上一周期明细：%d", settled)
	}
	if again, err := generateDueCsSettlements(ctx, db, now, 200); err != nil || again != 0 {
		t.Fatalf("重复运行应幂等：%v %d", err, again)
	}
}

// 上一周期没有明细时不生成结算单。
func TestGenerateDueCsSettlementsSkipNoEntries(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_commission_auto_skip")
	defer db.Close()
	ctx := t.Context()
	admin := seedAdmin(t, db, "cc-auto-skip-admin")
	now := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)

	rule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 1000, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, rule.ID, admin.ID, now); err != nil {
		t.Fatal(err)
	}
	db.EsportsCsCommissionEntry.Create().SetIdemKey("cc-auto-skip-oct").
		SetOrderID(1).SetAdminID(admin.ID).
		SetKind(esportscscommissionentry.KindService).
		SetCalcMode(esportscscommissionentry.CalcModePercent).
		SetBaseMode(esportscscommissionentry.BaseModeOrderAmount).
		SetBaseCents(5000).SetAmountCents(500).
		SetStatus(esportscscommissionentry.StatusPendingSettlement).
		SetCreatedAt(time.Date(2026, 10, 3, 3, 0, 0, 0, time.UTC)).SaveX(ctx)

	if created, err := generateDueCsSettlements(ctx, db, now, 200); err != nil || created != 0 {
		t.Fatalf("当前周期有明细但上一周期为空时不应生成：%v %d", err, created)
	}
}

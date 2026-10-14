package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscommissionrule"
	"go-admin/internal/ent/esportsorderaddon"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/migrate"
)

func openAddonDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// 加钟收款（§3.3）：仅「已确认且未收款」的加钟单可按客户余额核销；余额不足与重复收款均被拒。
func TestPayOrderAddonByBalance(t *testing.T) {
	db := openAddonDB(t, "esports_addon_pay")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := saveCustomerOrFail(t, db, 11)
	player := seedPlayer(t, db, 9301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedInServiceOrder(t, db, "addon-pay-1", player.ID)
	db.EsportsCommissionRule.Create().SetRuleNo("R-ADDON-PAY").
		SetStatus(esportscommissionrule.StatusActive).SetScope(esportscommissionrule.ScopeGlobal).
		SetCalcMode(esportscommissionrule.CalcModePercent).SetValueBp(5000).SaveX(ctx)

	addon, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{AddedCents: 4000}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = payOrderAddonByBalance(ctx, db, 11, addon.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("待确认的加钟不应可收款：%v", err)
	}
	if _, err = confirmOrderAddon(ctx, db, player.ID, addon.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = payOrderAddonByBalance(ctx, db, 11, addon.ID, now); !errors.Is(err, errInsufficientBalance) {
		t.Fatalf("余额不足应被拒：%v", err)
	}
	if _, err = walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 4000, Note: "测试充值"}, "addon-pay-seed-1"); err != nil {
		t.Fatal(err)
	}
	paid, err := payOrderAddonByBalance(ctx, db, 11, addon.ID, now)
	if err != nil {
		t.Fatalf("收款应成功：%v", err)
	}
	if paid.PaymentStatus != esportsorderaddon.PaymentStatusPaid || paid.PaidCents != 4000 ||
		paid.PaymentRef == "" || paid.PaidAt == nil {
		t.Fatalf("加钟收款信息不正确：%+v", paid)
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil || balance.total() != 0 {
		t.Fatalf("余额应被扣减到 0：%v %+v", err, balance)
	}
	if countEventsByType(t, db)["order.addon_paid"] != 1 {
		t.Fatal("缺少 order.addon_paid 事件")
	}
	if _, err = payOrderAddonByBalance(ctx, db, 11, addon.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复收款应冲突：%v", err)
	}
}

// 加钟退款分摊（§12.5）：售后退款按比例同时冲回原单服务分成与加钟分成。
func TestAddonCommissionReversalOnRefund(t *testing.T) {
	db := openAddonDB(t, "esports_addon_reversal")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedPlayer(t, db, 9302, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedInServiceOrder(t, db, "addon-rev-1", player.ID)
	db.EsportsCommissionRule.Create().SetRuleNo("R-ADDON-REV").
		SetStatus(esportscommissionrule.StatusActive).SetScope(esportscommissionrule.ScopeGlobal).
		SetCalcMode(esportscommissionrule.CalcModePercent).SetValueBp(5000).SaveX(ctx)
	// 直接造一条 service 分成明细，聚焦退款冲回口径。
	db.EsportsCommissionEntry.Create().
		SetIdemKey("commission:service:addon-rev-1:1").
		SetOrderID(order.ID).SetPlayerID(player.ID).
		SetKind(esportscommissionentry.KindService).SetSeatIndex(1).
		SetBaseCents(1000).SetAmountCents(500).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)

	addon, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{AddedCents: 4000}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = confirmOrderAddon(ctx, db, player.ID, addon.ID, now); err != nil {
		t.Fatal(err)
	}
	if n := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(esportscommissionentry.KindAddon),
	).CountX(ctx); n != 1 {
		t.Fatalf("应生成一条加钟分成明细：%d", n)
	}

	// 退款 500 / 应付 1000 = 50%：service 冲回 -250、addon 冲回 -1000。
	paid := db.EsportsOrder.GetX(ctx, order.ID)
	item := &ent.EsportsAfterSale{AfterSaleNo: "AS-ADDON-1", RefundCents: 500}
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = reverseOrderCommission(ctx, tx, paid, item, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("退款冲回应成功：%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	total := int64(0)
	reversals := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal),
	).AllX(ctx)
	if len(reversals) != 2 {
		t.Fatalf("应生成两条冲回明细（服务 + 加钟）：%d", len(reversals))
	}
	for _, entry := range reversals {
		total += entry.AmountCents
	}
	if total != -1250 {
		t.Fatalf("冲回合计应为 -1250，实际 %d", total)
	}
	// 重复冲回被幂等键拦截。
	tx, err = db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = reverseOrderCommission(ctx, tx, paid, item, now); !errors.Is(err, errConflict) {
		_ = tx.Rollback()
		t.Fatalf("重复冲回应冲突：%v", err)
	}
	_ = tx.Rollback()
}

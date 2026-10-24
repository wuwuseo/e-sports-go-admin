package esports

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/systemconfig"
)

// ar4SeedOrder 构造一张金额与状态可控的订单（规则扫描 / 并发查重用）。
// payable 与「数量×单价−折扣+人工调整」的差额即金额偏离，结单时间由 closedAt 控制。
func ar4SeedOrder(t *testing.T, db *ent.Client, suffix string, payable, unitPrice, discount, adjust int64,
	durationMinutes, seats int, status esportsorder.FulfillmentStatus, closedAt *time.Time) *ent.EsportsOrder {
	t.Helper()
	create := db.EsportsOrder.Create().
		SetOrderNo("ASR4-" + suffix).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(unitPrice).SetDiscountCents(discount).
		SetManualAdjustCents(adjust).SetPayableCents(payable).
		SetPromisedServiceUnits(4).SetRequiredSeats(seats).
		SetServiceDurationMinutes(durationMinutes).
		SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(status)
	if closedAt != nil {
		create.SetClosedAt(*closedAt)
	}
	return create.SaveX(t.Context())
}

// ar4SetConfig 幂等写入系统配置（同键覆盖），避免测试内重复建同键配置。
func ar4SetConfig(t *testing.T, db *ent.Client, key, value string) {
	t.Helper()
	changed, err := db.SystemConfig.Update().Where(systemconfig.KeyEQ(key)).SetValue(value).Save(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if changed == 0 {
		setSystemConfigForTest(t, db, key, value)
	}
}

// ：建单即写进行中唯一键（DB 唯一索引兜底并发），落终态必须释放，否则同来源同问题再也建不了单。
func TestAfterSaleActiveDedupKeyWrittenAndReleasedOnTerminal(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_active_key")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	order := ar4SeedOrder(t, db, "KEY", 2000, 2000, 0, 0, 0, 1,
		esportsorder.FulfillmentStatusPendingDispatch, nil)
	input := afterSaleInput{SourceType: "order", SourceID: order.ID, Kind: "refund_request",
		Channel: "admin", Demand: "客户申请退款"}

	first, err := createAfterSale(ctx, db, input, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	want := "order:" + strconv.Itoa(order.ID) + ":refund_request"
	if first.ActiveDedupKey == nil || *first.ActiveDedupKey != want {
		t.Fatalf("建单应写入进行中唯一键 %q，实际 %v", want, first.ActiveDedupKey)
	}

	// DB 级兜底：绕过 service 层查重直接插入同键第二条，唯一索引必须拒绝。
	_, err = db.EsportsAfterSale.Create().SetAfterSaleNo("ASR4-KEY-DUP").
		SetSourceType(esportsaftersale.SourceTypeOrder).SetSourceID(order.ID).
		SetKind(esportsaftersale.KindRefundRequest).SetChannel(esportsaftersale.ChannelAdmin).
		SetDemand("并发插入的同源同问题售后单").SetStatus(esportsaftersale.StatusPendingAccept).
		SetActiveDedupKey(want).Save(ctx)
	if !ent.IsConstraintError(err) {
		t.Fatalf("同键第二条进行中售后单必须被唯一索引拒绝，实际 err=%v", err)
	}
	if n := db.EsportsAfterSale.Query().Where(esportsaftersale.ActiveDedupKeyEQ(want)).CountX(ctx); n != 1 {
		t.Fatalf("同源同问题进行中售后单必须恰为 1 张，实际 %d", n)
	}

	// 撤回 → 释放键；同来源同问题可以重新发起。
	withdrawn, err := withdrawAfterSale(ctx, db, first.ID, 11, now)
	if err != nil {
		t.Fatalf("撤回失败：%v", err)
	}
	if withdrawn.ActiveDedupKey != nil {
		t.Fatalf("撤回后必须释放进行中唯一键，实际 %v", *withdrawn.ActiveDedupKey)
	}
	second, err := createAfterSale(ctx, db, input, 11, now)
	if err != nil {
		t.Fatalf("释放后应能重新建单：%v", err)
	}
	if second.ActiveDedupKey == nil {
		t.Fatal("重新建单应再次写入进行中唯一键")
	}

	// 驳回（审批环节）→ 释放键。
	db.EsportsAfterSale.UpdateOneID(second.ID).
		SetStatus(esportsaftersale.StatusPendingApproval).SetHandlerAdminID(11).ExecX(ctx)
	rejected, err := rejectAfterSale(ctx, db, second.ID, 22, afterSaleDecisionInput{Reason: "证据不足"}, now)
	if err != nil {
		t.Fatalf("驳回失败：%v", err)
	}
	if rejected.ActiveDedupKey != nil {
		t.Fatalf("驳回后必须释放进行中唯一键，实际 %v", *rejected.ActiveDedupKey)
	}

	// 执行完成 → 释放键。
	third, err := createAfterSale(ctx, db, input, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsAfterSale.UpdateOneID(third.ID).
		SetStatus(esportsaftersale.StatusPendingExecution).ExecX(ctx)
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	txOrder, err := tx.EsportsOrder.Get(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	newVersion, err := bumpOrder(ctx, tx, order.ID, txOrder.Version, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = completeAfterSaleExecution(ctx, tx, third.ID, order.ID, 11, newVersion,
		"order.after_sale_executed", map[string]interface{}{"after_sale_no": third.AfterSaleNo}, now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	completed := db.EsportsAfterSale.GetX(ctx, third.ID)
	if completed.Status != esportsaftersale.StatusCompleted || completed.ActiveDedupKey != nil {
		t.Fatalf("执行完成后应落 completed 且释放唯一键：%+v", completed)
	}
}

// ：自助申请时限配置化——超时限的自助申请必须被拒，且提示联系客服。
func TestCustomerSelfAfterSaleWindowRejectsOverdueApplication(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_window")
	defer db.Close()
	ctx := t.Context()
	ar4SetConfig(t, db, "app.esports.customer_aftersale_window_days", "30")
	customer := saveCustomerOrFail(t, db, 11)
	closedAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	order := ar4SeedOrder(t, db, "WINDOW", 2000, 2000, 0, 0, 0, 1,
		esportsorder.FulfillmentStatusCompleted, &closedAt)
	db.EsportsOrder.UpdateOneID(order.ID).SetCustomerID(customer.ID).ExecX(ctx)

	_, err := createCustomerAfterSale(ctx, db, customer, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "refund_request", Demand: "服务不符要求退款",
	}, time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC))
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "时限") {
		t.Fatalf("超过自助申请时限应被拒并提示时限，实际 err=%v", err)
	}
	// 配置为 0（不限时）后同一订单可自助发起。
	ar4SetConfig(t, db, "app.esports.customer_aftersale_window_days", "0")
	if _, err = createCustomerAfterSale(ctx, db, customer, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "refund_request", Demand: "服务不符要求退款",
	}, time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("自助申请时限为 0 时不应拦截：%v", err)
	}
}

// ：时限配置非法时必须明确报错，不静默回落。
func TestCustomerSelfAfterSaleWindowInvalidConfigFails(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_window_invalid")
	defer db.Close()
	ctx := t.Context()
	ar4SetConfig(t, db, "app.esports.customer_aftersale_window_days", "abc")
	if _, err := customerAfterSaleWindowDays(ctx, db); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法时限配置应明确报错，实际 err=%v", err)
	}
}

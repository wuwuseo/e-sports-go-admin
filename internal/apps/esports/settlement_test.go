package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/migrate"
)

func openSettlementDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// seedPendingCloseOrder 构造一张待结单订单，并挂一条已结束服务段（凭证与系统秒数可指定）。
func seedPendingCloseOrder(t *testing.T, db *ent.Client, orderNo, evidenceRef string, systemSeconds int64) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	order := seedPaidOrder(t, db, orderNo, 1)
	order = db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	if _, err := db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(1).
		SetSeatIndex(1).SetStatus(esportsservicesegment.StatusFinished).
		SetStartedAt(time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)).
		SetEndedAt(time.Date(2026, 10, 11, 9, 10, 0, 0, time.UTC)).
		SetSystemSeconds(systemSeconds).SetEvidenceRef(evidenceRef).Save(ctx); err != nil {
		t.Fatal(err)
	}
	return order
}

// 结单条件不满足时必须拦截且不改动订单状态。
func TestCloseOrderRequiresEvidenceAndDuration(t *testing.T) {
	db := openSettlementDB(t, "esports_settlement_guard")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	noEvidence := seedPendingCloseOrder(t, db, "close-no-evidence", "", 600)
	if _, err := closeOrder(ctx, db, noEvidence.ID, 11, closeOrderInput{Note: "结单"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("无服务凭证被放行：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, noEvidence.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		t.Fatalf("校验失败却改变了订单状态：%+v", got)
	}

	noDuration := seedPendingCloseOrder(t, db, "close-no-duration", "ev-1.png", 0)
	if _, err := closeOrder(ctx, db, noDuration.ID, 11, closeOrderInput{Note: "结单"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("服务时长为零被放行：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, noDuration.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		t.Fatalf("校验失败却改变了订单状态：%+v", got)
	}
}

// 条件满足时结单成功：状态转 completed、留结单时间、写 order.closed 事件与外盒，重复结单被拦截。
func TestCloseOrderCompletesAndRecordsEvent(t *testing.T) {
	db := openSettlementDB(t, "esports_settlement_ok")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedPendingCloseOrder(t, db, "close-ok-1", "ev/close-1.png", 600)
	closed, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "服务完成，正常结单"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if closed.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted || closed.ClosedAt == nil {
		t.Fatalf("结单未生效：%+v", closed)
	}
	if countEventsByType(t, db)["order.closed"] != 1 {
		t.Fatal("缺少 order.closed 事件")
	}
	if n := db.EsportsOutbox.Query().CountX(ctx); n != 1 {
		t.Fatalf("缺少结单外盒事件：%d", n)
	}
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "重复结单"}, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复结单未拦截：%v", err)
	}
}

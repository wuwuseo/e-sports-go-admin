package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
)

// seedPendingConfirmAfterSale 建一张已提交方案、处于「待客户确认」的售后单，并返回归属客户 ID。
func seedPendingConfirmAfterSale(t *testing.T, db *ent.Client, orderID int, refundCents int64, now time.Time) (*ent.EsportsAfterSale, int) {
	t.Helper()
	ctx := t.Context()
	customer := db.EsportsCustomer.Query().OnlyX(ctx)
	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: orderID, Kind: "refund_request", Channel: "customer",
		Demand: "客户反馈服务时长不足，申请退款", ExpectedPlan: "按差额退款",
		CustomerID: &customer.ID,
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptAfterSale(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	planned, err := submitAfterSalePlan(ctx, db, created.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: refundCents,
		Resolution: "按差额退款",
	}, now)
	if err != nil || planned.Status != esportsaftersale.StatusPendingCustomerConfirm {
		t.Fatalf("方案应进入待客户确认：%v %+v", err, planned)
	}
	return planned, customer.ID
}

// 待客户确认：归属客户自助确认后进入待审批；非归属客户一律 403。
func TestAfterSaleCustomerConfirm(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_customer_confirm")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedBalancePaidOrder(t, db, "confirm")
	item, customerID := seedPendingConfirmAfterSale(t, db, order.ID, 1000, now)

	if _, err := confirmAfterSaleByCustomer(ctx, db, item.ID, customerID+999, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非归属客户确认应 403：%v", err)
	}
	confirmed, err := confirmAfterSaleByCustomer(ctx, db, item.ID, customerID, now)
	if err != nil {
		t.Fatalf("客户自助确认失败：%v", err)
	}
	if confirmed.Status != esportsaftersale.StatusPendingApproval ||
		confirmed.CustomerConfirmChannel != esportsaftersale.CustomerConfirmChannelSelfService ||
		confirmed.CustomerConfirmedAt == nil {
		t.Fatalf("确认结果不正确：%+v", confirmed)
	}
	// 重复确认因状态已变化被拒。
	if _, err := confirmAfterSaleByCustomer(ctx, db, item.ID, customerID, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复确认应冲突：%v", err)
	}
	// 确认后可正常审批执行。
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatalf("确认后审批应成功：%v", err)
	}
}

// 待客户确认：客户拒绝须填原因并退回处理中，客服可改方案后重新提交（再次进入待客户确认）。
func TestAfterSaleCustomerDecline(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_customer_decline")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedBalancePaidOrder(t, db, "decline")
	item, customerID := seedPendingConfirmAfterSale(t, db, order.ID, 1000, now)

	if _, err := declineAfterSaleByCustomer(ctx, db, item.ID, customerID, "  ", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("拒绝须填原因：%v", err)
	}
	declined, err := declineAfterSaleByCustomer(ctx, db, item.ID, customerID, "赔付金额太低", now)
	if err != nil {
		t.Fatalf("客户拒绝失败：%v", err)
	}
	if declined.Status != esportsaftersale.StatusProcessing ||
		declined.CustomerDeclineReason != "赔付金额太低" || declined.CustomerDeclinedAt == nil {
		t.Fatalf("拒绝结果不正确：%+v", declined)
	}
	// 拒绝后客服可改方案重新提交，并再次进入待客户确认。
	resubmitted, err := submitAfterSalePlan(ctx, db, item.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: 1500,
		Resolution: "按客户诉求提高赔付",
	}, now)
	if err != nil {
		t.Fatalf("重新提交方案失败：%v", err)
	}
	if resubmitted.Status != esportsaftersale.StatusPendingCustomerConfirm ||
		resubmitted.CustomerDeclineReason != "" || resubmitted.CustomerDeclinedAt != nil {
		t.Fatalf("重新提交应清空上一轮确认留痕：%+v", resubmitted)
	}
}

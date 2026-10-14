package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportscloserule"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/migrate"
)

func openCloseRuleDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// seedSimpleCloseableOrder 建一张可结单订单（待结单 + 带凭证与时长的一条服务段）。
func seedSimpleCloseableOrder(t *testing.T, db *ent.Client, orderNo string, payable int64, adminID int) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	order := db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(payable).SetPayableCents(payable).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).SetResponsibleAdminID(adminID).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).
		SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).
		SetSystemSeconds(600).SetReportedUnits(1).SetEvidenceRef("shot-1").SaveX(ctx)
	return order
}

// 结单规则版本管理：同 code 递增版本、生效互斥、回滚生成新的 active 版本。
func TestCloseRuleLifecycle(t *testing.T) {
	db := openCloseRuleDB(t, "esports_close_rule")
	defer db.Close()
	ctx := t.Context()
	in := closeRuleInput{Code: "CLOSE-DEFAULT", Name: "默认结单", Scope: "global",
		MinEvidence: 1, RequireDuration: true, RequireNoPendingAddon: true, RequireNoOpenAftersale: true}

	rule, err := createCloseRule(ctx, db, in, 11)
	if err != nil || rule.Status != esportscloserule.StatusDraft || rule.Version != 1 {
		t.Fatalf("创建失败：%v %+v", err, rule)
	}
	second, err := createCloseRule(ctx, db, in, 11)
	if err != nil || second.Version != 2 {
		t.Fatalf("同 code 应递增版本：%v %+v", err, second)
	}
	if _, err = activateCloseRule(ctx, db, second.ID, time.Now()); err != nil {
		t.Fatalf("生效 v2 失败：%v", err)
	}
	if _, err = activateCloseRule(ctx, db, rule.ID, time.Now()); err != nil {
		t.Fatalf("生效 v1 失败：%v", err)
	}
	if db.EsportsCloseRule.GetX(ctx, second.ID).Status != esportscloserule.StatusHistory {
		t.Fatal("旧 active 未转 history")
	}
	rolled, err := rollbackCloseRule(ctx, db, second.ID, 11)
	if err != nil || rolled.Version != 3 || rolled.Status != esportscloserule.StatusActive {
		t.Fatalf("回滚失败：%v %+v", err, rolled)
	}
}

// 无规则时回落内置默认条件：凭证 ≥1、时长 >0、无未处理加钟 / 售后，均可正常结单。
func TestCloseOrderDefaultConditions(t *testing.T) {
	db := openCloseRuleDB(t, "esports_close_default")
	defer db.Close()
	ctx := t.Context()
	order := seedSimpleCloseableOrder(t, db, "close-default", 1000, 11)
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, time.Now()); err != nil {
		t.Fatalf("默认条件结单失败：%v", err)
	}
}

// 规则要求凭证不少于 2 张：1 张凭证被拒。
func TestCloseOrderMinEvidenceRule(t *testing.T) {
	db := openCloseRuleDB(t, "esports_close_evidence")
	defer db.Close()
	ctx := t.Context()
	rule, err := createCloseRule(ctx, db, closeRuleInput{
		Code: "STRICT", Name: "严格", Scope: "global", MinEvidence: 2,
		RequireDuration: true, RequireNoPendingAddon: true, RequireNoOpenAftersale: true,
	}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCloseRule(ctx, db, rule.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	order := seedSimpleCloseableOrder(t, db, "close-strict", 1000, 11)
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, time.Now()); !errors.Is(err, errInvalidInput) {
		t.Fatalf("凭证不足未拦截：%v", err)
	}
}

// 金额达复核阈值：结单进入「待复核」，由非负责客服的管理员复核通过（§3.4 高额单管理员复核）。
func TestCloseOrderReviewThreshold(t *testing.T) {
	db := openCloseRuleDB(t, "esports_close_review")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	rule, err := createCloseRule(ctx, db, closeRuleInput{
		Code: "REVIEW", Name: "高额复核", Scope: "global", MinEvidence: 1,
		RequireDuration: true, RequireNoPendingAddon: true, RequireNoOpenAftersale: true,
		ReviewThresholdCents: 1000,
	}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCloseRule(ctx, db, rule.ID, now); err != nil {
		t.Fatal(err)
	}
	order := seedSimpleCloseableOrder(t, db, "close-review", 1000, 11)
	reviewing, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now)
	if err != nil || reviewing.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCloseReview {
		t.Fatalf("结单应进入待复核：%v %+v", err, reviewing)
	}
	if countEventsByType(t, db)["order.close_review_requested"] != 1 {
		t.Fatal("缺少 order.close_review_requested 事件")
	}
	// 负责客服本人复核被拒（不得自审）。
	if _, err := approveCloseReview(ctx, db, order.ID, 11, true, "", now); !errors.Is(err, errForbidden) {
		t.Fatalf("负责客服自审未拦截：%v", err)
	}
	done, err := approveCloseReview(ctx, db, order.ID, 12, true, "复核通过", now)
	if err != nil || done.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted {
		t.Fatalf("复核通过失败：%v %+v", err, done)
	}
}

// 复核驳回回退待结单，可再次提交结单。
func TestCloseReviewRejectRollsBack(t *testing.T) {
	db := openCloseRuleDB(t, "esports_close_review_reject")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	rule, err := createCloseRule(ctx, db, closeRuleInput{
		Code: "REVIEW", Name: "高额复核", Scope: "global", MinEvidence: 1,
		RequireDuration: true, RequireNoPendingAddon: true, RequireNoOpenAftersale: true,
		ReviewThresholdCents: 1000,
	}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCloseRule(ctx, db, rule.ID, now); err != nil {
		t.Fatal(err)
	}
	order := seedSimpleCloseableOrder(t, db, "close-reject", 1000, 11)
	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = rejectCloseReview(ctx, db, order.ID, 12, true, "  ", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未填驳回原因未拦截：%v", err)
	}
	rejected, err := rejectCloseReview(ctx, db, order.ID, 12, true, "凭证存疑", now)
	if err != nil || rejected.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		t.Fatalf("驳回失败：%v %+v", err, rejected)
	}
	if countEventsByType(t, db)["order.close_review_rejected"] != 1 {
		t.Fatal("缺少 order.close_review_rejected 事件")
	}
}

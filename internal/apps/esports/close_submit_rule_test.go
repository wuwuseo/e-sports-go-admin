package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsclosesubmitrule"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsservicesegment"
)

// seedSubmitOrder 建一张待结单订单，按 submitted 标记逐席位建服务段（false 表示未形成有效上报）。
func seedSubmitOrder(t *testing.T, db *ent.Client, orderNo string, payable int64, submitted []bool) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	order := db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(payable).SetPayableCents(payable).
		SetPromisedServiceUnits(1).SetRequiredSeats(len(submitted)).SetResponsibleAdminID(11).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	for i, ok := range submitted {
		create := db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(100 + i).
			SetSeatIndex(i + 1).SetStatus(esportsservicesegment.StatusFinished).
			SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).SetSystemSeconds(600)
		if ok {
			create.SetReportedUnits(1).SetEvidenceRef("shot")
		}
		create.SaveX(ctx)
	}
	return order
}

func seedActiveSubmitRule(t *testing.T, db *ent.Client, in closeSubmitRuleInput) *ent.EsportsCloseSubmitRule {
	t.Helper()
	ctx := t.Context()
	rule, err := createCloseSubmitRule(ctx, db, in, 11)
	if err != nil {
		t.Fatalf("创建提交规则失败：%v", err)
	}
	if _, err = activateCloseSubmitRule(ctx, db, rule.ID); err != nil {
		t.Fatalf("生效提交规则失败：%v", err)
	}
	return rule
}

// 提交规则校验：模式 / 未达标处理 / 范围 / 比例取值非法均被拒。
func TestCloseSubmitRuleValidation(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_submit_rule_validate")
	defer db.Close()
	ctx := t.Context()

	base := closeSubmitRuleInput{Code: "SR", Name: "默认提交规则", Scope: "global", Mode: "multi", OnMissing: "block"}
	if _, err := createCloseSubmitRule(ctx, db, base, 11); err != nil {
		t.Fatalf("基础规则应可创建：%v", err)
	}
	invalid := []closeSubmitRuleInput{
		{Code: "SR2", Name: "x", Scope: "global", Mode: "unknown", OnMissing: "block"},
		{Code: "SR3", Name: "x", Scope: "global", Mode: "custom", MinSubmitRatioBp: 0, OnMissing: "block"},
		{Code: "SR4", Name: "x", Scope: "global", Mode: "multi", OnMissing: "unknown"},
		{Code: "SR5", Name: "x", Scope: "game", Mode: "multi", OnMissing: "block"},
		{Code: "SR6", Name: "x", Scope: "global", Mode: "custom", MinSubmitRatioBp: 10001, OnMissing: "block"},
		{Code: "", Name: "x", Scope: "global", Mode: "multi", OnMissing: "block"},
	}
	for i, in := range invalid {
		if _, err := createCloseSubmitRule(ctx, db, in, 11); !errors.Is(err, errInvalidInput) {
			t.Fatalf("第 %d 组非法规则应被拒：%v", i+1, err)
		}
	}
}

// 版本管理：同 code 递增版本、生效互斥、回滚生成新的 active 版本。
func TestCloseSubmitRuleLifecycle(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_submit_rule_lifecycle")
	defer db.Close()
	ctx := t.Context()

	in := closeSubmitRuleInput{Code: "SR-LIFE", Name: "版本规则", Scope: "global", Mode: "multi", OnMissing: "block"}
	first, err := createCloseSubmitRule(ctx, db, in, 11)
	if err != nil || first.Status != esportsclosesubmitrule.StatusDraft || first.Version != 1 {
		t.Fatalf("创建失败：%v %+v", err, first)
	}
	second, err := createCloseSubmitRule(ctx, db, in, 11)
	if err != nil || second.Version != 2 {
		t.Fatalf("同 code 应递增版本：%v %+v", err, second)
	}
	if _, err = activateCloseSubmitRule(ctx, db, second.ID); err != nil {
		t.Fatalf("生效 v2 失败：%v", err)
	}
	if _, err = activateCloseSubmitRule(ctx, db, first.ID); err != nil {
		t.Fatalf("生效 v1 失败：%v", err)
	}
	if db.EsportsCloseSubmitRule.GetX(ctx, second.ID).Status != esportsclosesubmitrule.StatusHistory {
		t.Fatal("旧 active 未转 history")
	}
	rolled, err := rollbackCloseSubmitRule(ctx, db, second.ID, 11)
	if err != nil || rolled.Version != 3 || rolled.Status != esportsclosesubmitrule.StatusActive {
		t.Fatalf("回滚失败：%v %+v", err, rolled)
	}
	if db.EsportsCloseSubmitRule.GetX(ctx, first.ID).Status != esportsclosesubmitrule.StatusHistory {
		t.Fatal("回滚后原 active 未转 history")
	}
}

// 最少提交成员数：single / multi 均为逐人（功能列表 §3.4-76）、自定义按比例向上取整。
func TestSubmitRequiredCount(t *testing.T) {
	cases := []struct {
		mode   esportsclosesubmitrule.Mode
		bp     int
		total  int
		expect int
	}{
		// §3.4-76：单人单本人提交，临时多人单所有实际服务成员逐人提交；
		// mode=single 不得把多人单放宽成「任一人提交即可」。
		{esportsclosesubmitrule.ModeSingle, 0, 4, 4},
		{esportsclosesubmitrule.ModeSingle, 0, 1, 1},
		{esportsclosesubmitrule.ModeMulti, 0, 4, 4},
		{esportsclosesubmitrule.ModeCustom, 5000, 3, 2},
		{esportsclosesubmitrule.ModeCustom, 10000, 3, 3},
		{esportsclosesubmitrule.ModeCustom, 1, 3, 1},
		{esportsclosesubmitrule.ModeCustom, 0, 3, 0},
		{esportsclosesubmitrule.ModeMulti, 0, 0, 0},
	}
	for _, c := range cases {
		rule := &ent.EsportsCloseSubmitRule{Mode: c.mode, MinSubmitRatioBp: c.bp}
		if got := submitRequiredCount(rule, c.total); got != c.expect {
			t.Fatalf("模式 %s 比例 %d 共 %d 人：期望 %d，实际 %d", c.mode, c.bp, c.total, c.expect, got)
		}
	}
}

// 多人逐人提交：缺人上报禁止结单；规则允许代提交时客服可补填后结单并留痕。
func TestCloseSubmitRuleBlockAndSubstitute(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_submit_rule_block")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveSubmitRule(t, db, closeSubmitRuleInput{Code: "SR-MULTI", Name: "多人逐人提交",
		Scope: "global", Mode: "multi", AllowSubstitute: true, OnMissing: "block"})

	order := seedSubmitOrder(t, db, "sr-block-1", 2000, []bool{true, false})
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("缺成员上报应被拒：%v", err)
	}
	// 代提交内容非法（既无服务量也无凭证）应被拒。
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单",
		SubstituteReports: []closeSubstituteReport{{SeatIndex: 2}}}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("空代提交应被拒：%v", err)
	}
	// 代提交补齐第 2 席后应结单成功。
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单",
		SubstituteReports: []closeSubstituteReport{{SeatIndex: 2, ReportedUnits: 1, EvidenceRef: "admin-shot"}}},
		now); err != nil {
		t.Fatalf("代提交后应结单成功：%v", err)
	}
	second := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(order.ID), esportsservicesegment.SeatIndexEQ(2)).OnlyX(ctx)
	if second.ReportedUnits != 1 || second.EvidenceRef != "admin-shot" {
		t.Fatalf("代提交未写回服务段：%+v", second)
	}
	if countEventsByType(t, db)["order.service_submitted_by_admin"] != 1 {
		t.Fatal("缺少代提交留痕事件")
	}
}

// 未达标处理：remind 放行并写催办提醒；auto_accept 放行并留痕。
func TestCloseSubmitRuleRemindAndAutoAccept(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_submit_rule_relax")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveSubmitRule(t, db, closeSubmitRuleInput{Code: "SR-RELAX", Name: "仅提醒",
		Scope: "global", Mode: "multi", OnMissing: "remind"})
	remindOrder := seedSubmitOrder(t, db, "sr-remind", 2000, []bool{true, false})
	if _, err := closeOrder(ctx, db, remindOrder.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("提醒模式应放行结单：%v", err)
	}
	if n := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.close_submit_remind"),
	).CountX(ctx); n != 1 {
		t.Fatalf("应写一条催办提醒：%d", n)
	}

	// 生效更高版本切到 auto_accept。
	next, err := createCloseSubmitRule(ctx, db, closeSubmitRuleInput{Code: "SR-RELAX", Name: "视为已提交",
		Scope: "global", Mode: "multi", OnMissing: "auto_accept"}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCloseSubmitRule(ctx, db, next.ID); err != nil {
		t.Fatal(err)
	}
	autoOrder := seedSubmitOrder(t, db, "sr-auto", 2000, []bool{true, false})
	if _, err := closeOrder(ctx, db, autoOrder.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("视为已提交应放行结单：%v", err)
	}
	if countEventsByType(t, db)["order.close_submit_auto_accepted"] != 1 {
		t.Fatal("缺少「视为已提交」留痕事件")
	}
}

// 自定义比例：3 人 50% 需 2 人提交，达标放行、未达标拒绝。
func TestCloseSubmitRuleCustomRatio(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_submit_rule_ratio")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveSubmitRule(t, db, closeSubmitRuleInput{Code: "SR-RATIO", Name: "半数提交",
		Scope: "global", Mode: "custom", MinSubmitRatioBp: 5000, OnMissing: "block"})

	ok := seedSubmitOrder(t, db, "sr-ratio-ok", 2000, []bool{true, true, false})
	if _, err := closeOrder(ctx, db, ok.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("半数达标应结单成功：%v", err)
	}
	bad := seedSubmitOrder(t, db, "sr-ratio-bad", 2000, []bool{true, false, false})
	if _, err := closeOrder(ctx, db, bad.ID, 11, closeOrderInput{Note: "结单"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("半数未达标应被拒：%v", err)
	}
}

// 结单配置视图应带出提交规则摘要与未上报成员。
func TestOrderCloseFormCarriesSubmitRule(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_submit_rule_view")
	defer db.Close()
	ctx := t.Context()

	seedActiveSubmitRule(t, db, closeSubmitRuleInput{Code: "SR-VIEW", Name: "多人逐人提交",
		Scope: "global", Mode: "multi", AllowSubstitute: true, OnMissing: "block"})
	order := seedSubmitOrder(t, db, "sr-view", 2000, []bool{true, false})

	view, err := orderCloseForm(ctx, db, order.ID)
	if err != nil {
		t.Fatalf("获取结单配置失败：%v", err)
	}
	if view.SubmitRule == nil || view.SubmitRule["mode"] != "multi" || view.SubmitRule["required"] != 2 {
		t.Fatalf("提交规则摘要不正确：%+v", view.SubmitRule)
	}
	if len(view.Unsubmitted) != 1 || view.Unsubmitted[0].SeatIndex != 2 {
		t.Fatalf("未上报成员不正确：%+v", view.Unsubmitted)
	}
}

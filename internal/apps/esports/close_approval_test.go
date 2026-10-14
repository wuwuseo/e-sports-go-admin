package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscloseapproval"
	"go-admin/internal/ent/esportscloseapprovalflow"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
)

// 审批流配置校验：节点序号连续、角色合法、资金 / 争议节点禁止自动通过。
func TestCloseApprovalFlowValidation(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_approval_validate")
	defer db.Close()
	ctx := t.Context()

	valid := closeApprovalFlowInput{Code: "AF", Name: "两级审批", Scope: "global", MinCents: 1000,
		Nodes: []schema.CloseApprovalNode{
			{Seq: 1, Name: "客服主管复核", Role: "admin", TimeoutHours: 24},
			{Seq: 2, Name: "管理员终审", Role: "super_admin", TimeoutHours: 48},
		}}
	if _, err := createCloseApprovalFlow(ctx, db, valid, 11); err != nil {
		t.Fatalf("合法审批流应可创建：%v", err)
	}
	invalid := []closeApprovalFlowInput{
		{Code: "AF2", Name: "空节点", Scope: "global"},
		{Code: "AF3", Name: "序号不连续", Scope: "global",
			Nodes: []schema.CloseApprovalNode{{Seq: 2, Name: "越序", Role: "admin"}}},
		{Code: "AF4", Name: "角色非法", Scope: "global",
			Nodes: []schema.CloseApprovalNode{{Seq: 1, Name: "未知角色", Role: "finance"}}},
		{Code: "AF5", Name: "资金节点自动通过", Scope: "global",
			Nodes: []schema.CloseApprovalNode{{Seq: 1, Name: "财务复核", Role: "admin",
				TimeoutHours: 1, AutoPassOnTimeout: true, InvolvesFunds: true}}},
		{Code: "AF6", Name: "缺范围键", Scope: "game",
			Nodes: []schema.CloseApprovalNode{{Seq: 1, Name: "复核", Role: "admin"}}},
		{Code: "AF7", Name: "超时越界", Scope: "global",
			Nodes: []schema.CloseApprovalNode{{Seq: 1, Name: "复核", Role: "admin", TimeoutHours: 100000}}},
	}
	for i, in := range invalid {
		if _, err := createCloseApprovalFlow(ctx, db, in, 11); !errors.Is(err, errInvalidInput) {
			t.Fatalf("第 %d 组非法审批流应被拒：%v", i+1, err)
		}
	}
}

// 审批流版本管理：同 code 递增版本、生效互斥、回滚生成新的 active 版本。
func TestCloseApprovalFlowLifecycle(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_approval_lifecycle")
	defer db.Close()
	ctx := t.Context()

	in := closeApprovalFlowInput{Code: "AF-LIFE", Name: "版本流", Scope: "global",
		Nodes: []schema.CloseApprovalNode{{Seq: 1, Name: "复核", Role: "admin"}}}
	first, err := createCloseApprovalFlow(ctx, db, in, 11)
	if err != nil || first.Status != esportscloseapprovalflow.StatusDraft || first.Version != 1 {
		t.Fatalf("创建失败：%v %+v", err, first)
	}
	second, err := createCloseApprovalFlow(ctx, db, in, 11)
	if err != nil || second.Version != 2 {
		t.Fatalf("同 code 应递增版本：%v %+v", err, second)
	}
	if _, err = activateCloseApprovalFlow(ctx, db, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = activateCloseApprovalFlow(ctx, db, first.ID); err != nil {
		t.Fatal(err)
	}
	if db.EsportsCloseApprovalFlow.GetX(ctx, second.ID).Status != esportscloseapprovalflow.StatusHistory {
		t.Fatal("旧 active 未转 history")
	}
	rolled, err := rollbackCloseApprovalFlow(ctx, db, second.ID, 11)
	if err != nil || rolled.Version != 3 || rolled.Status != esportscloseapprovalflow.StatusActive {
		t.Fatalf("回滚失败：%v %+v", err, rolled)
	}
}

// seedActiveApprovalFlow 建一条生效审批流。
func seedActiveApprovalFlow(t *testing.T, db *ent.Client, in closeApprovalFlowInput) *ent.EsportsCloseApprovalFlow {
	t.Helper()
	flow, err := createCloseApprovalFlow(t.Context(), db, in, 11)
	if err != nil {
		t.Fatalf("创建审批流失败：%v", err)
	}
	if _, err = activateCloseApprovalFlow(t.Context(), db, flow.ID); err != nil {
		t.Fatalf("生效审批流失败：%v", err)
	}
	return flow
}

// 多节点串行审批（§3.4）：逐节点推进、super_admin 节点须超管、末节点通过才结单。
func TestCloseApprovalFlowMultiNode(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_approval_multi")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveApprovalFlow(t, db, closeApprovalFlowInput{Code: "AF-MULTI", Name: "两级审批",
		Scope: "global", MinCents: 1000, Nodes: []schema.CloseApprovalNode{
			{Seq: 1, Name: "客服主管复核", Role: "admin", TimeoutHours: 24},
			{Seq: 2, Name: "超级管理员终审", Role: "super_admin", TimeoutHours: 48},
		}})

	order := seedSimpleCloseableOrder(t, db, "af-order-1", 2000, 11)
	reviewing, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now)
	if err != nil || reviewing.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCloseReview {
		t.Fatalf("应进入待复核：%v %+v", err, reviewing)
	}
	view, err := orderCloseApproval(ctx, db, order.ID)
	if err != nil || view.Approval == nil || view.Current == nil ||
		view.Current.Seq != 1 || view.Current.Role != "admin" || len(view.Approval.Steps) != 2 {
		t.Fatalf("审批进度不正确：%v %+v %+v", err, view.Approval, view.Current)
	}
	first, err := approveCloseReview(ctx, db, order.ID, 12, false, "主管通过", now)
	if err != nil || first.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCloseReview {
		t.Fatalf("节点 1 通过后应仍在待复核：%v %+v", err, first)
	}
	if view, _ = orderCloseApproval(ctx, db, order.ID); view.Current == nil ||
		view.Current.Seq != 2 || view.Current.Name != "超级管理员终审" {
		t.Fatalf("应推进到节点 2：%+v", view.Current)
	}
	if _, err = approveCloseReview(ctx, db, order.ID, 13, false, "越权", now); !errors.Is(err, errForbidden) {
		t.Fatalf("非超管审批超管节点未拦截：%v", err)
	}
	done, err := approveCloseReview(ctx, db, order.ID, 13, true, "终审通过", now)
	if err != nil || done.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted {
		t.Fatalf("末节点通过后应结单：%v %+v", err, done)
	}
	if view, _ = orderCloseApproval(ctx, db, order.ID); view.Approval.Status != esportscloseapproval.StatusApproved ||
		view.Current != nil {
		t.Fatalf("审批应完结：%+v", view.Approval)
	}
	if countEventsByType(t, db)["order.close_approval_node_approved"] != 1 {
		t.Fatal("缺少中间节点通过事件")
	}
}

// 审批驳回回退待结单，重新结单重建审批进度（§3.4）。
func TestCloseApprovalRejectAndRestart(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_approval_reject")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveApprovalFlow(t, db, closeApprovalFlowInput{Code: "AF-REJECT", Name: "单节点审批",
		Scope: "global", MinCents: 1000, Nodes: []schema.CloseApprovalNode{
			{Seq: 1, Name: "主管复核", Role: "admin"},
		}})

	order := seedSimpleCloseableOrder(t, db, "af-order-2", 2000, 11)
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	rejected, err := rejectCloseReview(ctx, db, order.ID, 12, false, "凭证存疑", now)
	if err != nil || rejected.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		t.Fatalf("驳回应回退待结单：%v %+v", err, rejected)
	}
	view, _ := orderCloseApproval(ctx, db, order.ID)
	if view.Approval.Status != esportscloseapproval.StatusRejected || view.Current != nil {
		t.Fatalf("审批应转驳回：%+v", view.Approval)
	}
	// 重新结单：审批进度重建为 pending、回到首节点。
	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "补充凭证后重新结单"}, now); err != nil {
		t.Fatalf("重新结单失败：%v", err)
	}
	if view, _ = orderCloseApproval(ctx, db, order.ID); view.Approval.Status != esportscloseapproval.StatusPending ||
		view.Current == nil || view.Current.Seq != 1 {
		t.Fatalf("重新结单应重建审批进度：%+v", view.Approval)
	}
}

// 审批节点超时（§3.4）：允许自动通过的节点超时自动推进，资金节点只升级提醒。
func TestCloseApprovalTimeout(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_approval_timeout")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveApprovalFlow(t, db, closeApprovalFlowInput{Code: "AF-TIMEOUT", Name: "超时策略",
		Scope: "global", MinCents: 1000, Nodes: []schema.CloseApprovalNode{
			{Seq: 1, Name: "客服主管复核", Role: "admin", TimeoutHours: 1, AutoPassOnTimeout: true},
			{Seq: 2, Name: "财务复核", Role: "admin", TimeoutHours: 1, InvolvesFunds: true},
		}})

	order := seedSimpleCloseableOrder(t, db, "af-order-3", 2000, 11)
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	// 未到时限：不处理。
	if n, err := checkCloseApprovalTimeouts(ctx, db, now.Add(30*time.Minute), 200); err != nil || n != 0 {
		t.Fatalf("未超时不应处理：%v %d", err, n)
	}
	// 节点 1 超时 → 自动通过并推进到节点 2。
	if n, err := checkCloseApprovalTimeouts(ctx, db, now.Add(2*time.Hour), 200); err != nil || n != 1 {
		t.Fatalf("节点 1 应超时自动通过：%v %d", err, n)
	}
	view, _ := orderCloseApproval(ctx, db, order.ID)
	if view.Current == nil || view.Current.Seq != 2 {
		t.Fatalf("应推进到节点 2：%+v", view.Current)
	}
	if !view.Approval.Steps[0].AutoPassed {
		t.Fatal("节点 1 应标记为自动通过")
	}
	// 节点 2 是资金节点：超时只写升级外盒。
	if n, err := checkCloseApprovalTimeouts(ctx, db, now.Add(4*time.Hour), 200); err != nil || n != 1 {
		t.Fatalf("资金节点超时应升级提醒：%v %d", err, n)
	}
	if n := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("close.approval_timeout"),
	).CountX(ctx); n != 1 {
		t.Fatalf("升级外盒应为 1 条：%d", n)
	}
	// 重复跑幂等。
	if n, err := checkCloseApprovalTimeouts(ctx, db, now.Add(5*time.Hour), 200); err != nil || n != 0 {
		t.Fatalf("重复跑应幂等：%v %d", err, n)
	}
}

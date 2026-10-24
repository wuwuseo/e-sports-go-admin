package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsoutbox"
)

// 售后限时：各阶段按对应锚点计算截止时间，阶段不适用或时限为 0 时不计算。
func TestAfterSaleSLADeadline(t *testing.T) {
	policy := afterSaleSLAPolicy{AcceptHours: 2, PlanHours: 24, CustomerConfirmHours: 48, ExecuteHours: 24}
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	accepted := created.Add(time.Hour)
	planned := created.Add(3 * time.Hour)
	approved := created.Add(5 * time.Hour)

	cases := []struct {
		name         string
		item         *ent.EsportsAfterSale
		wantStage    string
		wantHours    int
		wantDeadline time.Time
		wantOK       bool
	}{
		{"待受理按创建时间", &ent.EsportsAfterSale{
			Status: esportsaftersale.StatusPendingAccept, CreatedAt: created},
			afterSaleStageAccept, 2, created.Add(2 * time.Hour), true},
		{"处理中按受理时间", &ent.EsportsAfterSale{
			Status: esportsaftersale.StatusProcessing, CreatedAt: created, AcceptedAt: &accepted},
			afterSaleStagePlan, 24, accepted.Add(24 * time.Hour), true},
		{"待客户确认按方案提交时间", &ent.EsportsAfterSale{
			Status: esportsaftersale.StatusPendingCustomerConfirm, CreatedAt: created, PlannedAt: &planned},
			afterSaleStageCustomerConfirm, 48, planned.Add(48 * time.Hour), true},
		{"待审批按方案提交时间", &ent.EsportsAfterSale{
			Status: esportsaftersale.StatusPendingApproval, CreatedAt: created, PlannedAt: &planned},
			afterSaleStageExecute, 24, planned.Add(24 * time.Hour), true},
		{"待执行按审批时间", &ent.EsportsAfterSale{
			Status: esportsaftersale.StatusPendingExecution, CreatedAt: created, ApprovedAt: &approved},
			afterSaleStageExecute, 24, approved.Add(24 * time.Hour), true},
		{"终态不计时", &ent.EsportsAfterSale{
			Status: esportsaftersale.StatusCompleted, CreatedAt: created},
			"", 0, time.Time{}, false},
	}
	for _, c := range cases {
		stage, hours, deadline, ok := afterSaleSLADeadline(c.item, policy)
		if ok != c.wantOK || stage != c.wantStage || hours != c.wantHours ||
			(ok && !deadline.Equal(c.wantDeadline)) {
			t.Fatalf("%s：期望 %s/%d/%s/%v，实际 %s/%d/%s/%v", c.name,
				c.wantStage, c.wantHours, c.wantDeadline, c.wantOK, stage, hours, deadline, ok)
		}
	}
	zero := afterSaleSLAPolicy{}
	if _, _, _, ok := afterSaleSLADeadline(&ent.EsportsAfterSale{
		Status: esportsaftersale.StatusPendingAccept, CreatedAt: created}, zero); ok {
		t.Fatal("时限为 0（不限）时不应计算截止时间")
	}
}

// seedAfterSaleSLARow 直接落一条指定状态与阶段时间戳的售后单。
func seedAfterSaleSLARow(t *testing.T, db *ent.Client, no string, status esportsaftersale.Status,
	createdAt, acceptedAt, plannedAt, approvedAt time.Time) *ent.EsportsAfterSale {
	t.Helper()
	create := db.EsportsAfterSale.Create().SetAfterSaleNo(no).
		SetSourceType(esportsaftersale.SourceTypeOrder).SetSourceID(1).
		SetKind(esportsaftersale.KindRefundRequest).SetChannel(esportsaftersale.ChannelCustomer).
		SetDemand("测试售后").SetStatus(status).SetCreatedAt(createdAt)
	if !acceptedAt.IsZero() {
		create.SetAcceptedAt(acceptedAt)
	}
	if !plannedAt.IsZero() {
		create.SetPlannedAt(plannedAt)
	}
	if !approvedAt.IsZero() {
		create.SetApprovedAt(approvedAt)
	}
	return create.SaveX(t.Context())
}

// 超时升级：只对超出当前阶段时限的单写升级外盒，幂等；调整时限后以新阈值再评估。
func TestCheckAfterSaleSLABreaches(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_sla_scan")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	zero := time.Time{}

	// 受理超时：待受理、3 小时前创建（默认 2 小时）。
	seedAfterSaleSLARow(t, db, "AS-SLA-ACCEPT", esportsaftersale.StatusPendingAccept,
		now.Add(-3*time.Hour), zero, zero, zero)
	// 方案超时：处理中、30 小时前受理（默认 24 小时）。
	seedAfterSaleSLARow(t, db, "AS-SLA-PLAN", esportsaftersale.StatusProcessing,
		now.Add(-40*time.Hour), now.Add(-30*time.Hour), zero, zero)
	// 未超时：待客户确认、1 小时前提交方案（默认 48 小时）。
	seedAfterSaleSLARow(t, db, "AS-SLA-OK", esportsaftersale.StatusPendingCustomerConfirm,
		now.Add(-2*time.Hour), now.Add(-2*time.Hour), now.Add(-time.Hour), zero)
	// 执行超时：待执行、25 小时前审批（默认 24 小时）。
	seedAfterSaleSLARow(t, db, "AS-SLA-EXEC", esportsaftersale.StatusPendingExecution,
		now.Add(-30*time.Hour), now.Add(-30*time.Hour), now.Add(-29*time.Hour), now.Add(-25*time.Hour))

	count, err := checkAfterSaleSLABreaches(ctx, db, now, 200)
	if err != nil || count != 3 {
		t.Fatalf("应有 3 张超时单升级：%v %d", err, count)
	}
	if again, err := checkAfterSaleSLABreaches(ctx, db, now, 200); err != nil || again != 0 {
		t.Fatalf("重复运行应幂等：%v %d", err, again)
	}
	events := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("aftersale.sla_breach"),
	).AllX(ctx)
	if len(events) != 3 {
		t.Fatalf("升级外盒应为 3 条：%d", len(events))
	}
	// 缩短受理时限到 1 小时：只有「受理」阶段换了阈值，会再升级一次。
	setWithdrawalConfig(t, db, afterSaleAcceptHoursKey, "1")
	if changed, err := checkAfterSaleSLABreaches(ctx, db, now, 200); err != nil || changed != 1 {
		t.Fatalf("调整时限后应以新阈值再升级一次：%v %d", err, changed)
	}
	// 放宽受理时限到 6 小时：不再命中，也不产生新升级。
	setWithdrawalConfig(t, db, afterSaleAcceptHoursKey, "6")
	if changed, err := checkAfterSaleSLABreaches(ctx, db, now, 200); err != nil || changed != 0 {
		t.Fatalf("放宽时限后不应升级：%v %d", err, changed)
	}
}

// 列表视图带出当前阶段时限与是否超时。
func TestAfterSaleViewCarriesSLA(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_sla_view")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

	item := seedAfterSaleSLARow(t, db, "AS-SLA-VIEW", esportsaftersale.StatusPendingAccept,
		now.Add(-3*time.Hour), time.Time{}, time.Time{}, time.Time{})
	policy := afterSaleSLAPolicyOf(ctx, db)
	view := afterSaleViewOf(item, policy, now)
	if view.SLALimitHours != 2 || view.SLADeadline == nil || !view.SLAOverdue {
		t.Fatalf("视图限时信息不正确：%+v", view)
	}
	if !view.SLADeadline.Equal(now.Add(-time.Hour)) {
		t.Fatalf("截止时间应为创建后 2 小时：%s", view.SLADeadline)
	}
}

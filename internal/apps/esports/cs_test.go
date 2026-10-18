package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/migrate"
)

func openCSDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// 企微自动建档幂等：同一「企业主体 + 外部联系人标识」只建一次；手建客户（企微字段为空）互不冲突。
func TestWecomIntakeIdempotent(t *testing.T) {
	db := openCSDB(t, "esports_cs_wecom")
	ctx := t.Context()
	now := time.Now()

	first, created, err := wecomUpsertCustomer(ctx, db, 1, wecomIntakeInput{
		CorpID: "corp-a", ExternalID: "ext-1", DisplayName: "企微客户甲",
	}, now)
	if err != nil || !created {
		t.Fatalf("首次建档应成功且 created=true，got created=%v err=%v", created, err)
	}
	second, created, err := wecomUpsertCustomer(ctx, db, 1, wecomIntakeInput{
		CorpID: "corp-a", ExternalID: "ext-1", DisplayName: "重复称呼",
	}, now)
	if err != nil {
		t.Fatalf("重复建档应返回既有档案，err=%v", err)
	}
	if created || second.ID != first.ID {
		t.Fatalf("重复建档应返回同一条且 created=false，got created=%v id=%d want id=%d", created, second.ID, first.ID)
	}
	if second.Source != esportscustomer.SourceWecom {
		t.Fatalf("建档来源应为 wecom，got %s", second.Source)
	}

	// 手建客户没有企微标识，唯一索引中的 NULL 不参与冲突。
	for i := 0; i < 2; i++ {
		if _, err := db.EsportsCustomer.Create().SetDisplayName("手建客户").SetOwnerAdminID(1).Save(ctx); err != nil {
			t.Fatalf("手建客户不应与企微唯一索引冲突，err=%v", err)
		}
	}
}

// 客户档案：标签去重去空、跟进状态更新，且写跟进记录会刷新最近联系时间。
func TestCustomerProfileAndFollowUp(t *testing.T) {
	db := openCSDB(t, "esports_cs_profile")
	ctx := t.Context()
	now := time.Now()

	customer, err := db.EsportsCustomer.Create().SetDisplayName("客户档案").SetOwnerAdminID(1).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := updateCustomerProfile(ctx, db, 1, false, customer.ID, customerProfileInput{
		Phone: "13800000000", ConsumptionPreference: "王者荣耀",
		Tags: []string{"vip", " vip ", "", "复购"}, FollowStatus: "following",
		SelfOrderEnabled: func() *bool { v := true; return &v }(),
	})
	if err != nil {
		t.Fatalf("更新档案失败：%v", err)
	}
	if !updated.SelfOrderEnabled {
		t.Fatal("自助下单开关应被更新为 true")
	}
	if len(updated.Tags) != 2 || updated.Tags[0] != "vip" || updated.Tags[1] != "复购" {
		t.Fatalf("标签应去空去重，got %v", updated.Tags)
	}
	if updated.FollowStatus != esportscustomer.FollowStatusFollowing {
		t.Fatalf("跟进状态应为 following，got %s", updated.FollowStatus)
	}

	record, err := addCustomerFollowUp(ctx, db, 1, false, customer.ID, followUpInput{
		Channel: "wecom", Summary: "确认下周报单意向", NextFollowAt: "2030-01-01",
	}, now)
	if err != nil {
		t.Fatalf("写跟进记录失败：%v", err)
	}
	if record.NextFollowAt == nil {
		t.Fatal("下次跟进时间应已写入")
	}
	reloaded, err := db.EsportsCustomer.Get(ctx, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.LastContactAt == nil {
		t.Fatal("写跟进后最近联系时间应被刷新")
	}

	view, err := customerProfileOverview(ctx, db, 1, false, customer.ID)
	if err != nil {
		t.Fatalf("档案汇总失败：%v", err)
	}
	if view["balance_cents"].(int64) != 0 || view["order_count"].(int) != 0 || view["growth_total"].(int64) != 0 {
		t.Fatalf("空档案汇总应为零，got %+v", view)
	}

	// 归属隔离：他人（非超管）访问被拒。
	if _, err := customerProfileOverview(ctx, db, 2, false, customer.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("非归属客服访问应返回 errForbidden，got %v", err)
	}
	// 超管可越权查看。
	if _, err := customerProfileOverview(ctx, db, 2, true, customer.ID); err != nil {
		t.Fatalf("超管应可查看，err=%v", err)
	}
}

// 话术库走 version 乐观锁，版本不符返回冲突。
func TestScriptOptimisticLock(t *testing.T) {
	db := openCSDB(t, "esports_cs_script")
	ctx := t.Context()

	item, err := createScript(ctx, db, 1, scriptInput{Title: "首单话术", Content: "您好，请问需要什么服务？"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := updateScript(ctx, db, item.ID, scriptInput{
		Title: "首单话术", Content: "您好，需要什么服务？", ExpectedVersion: item.Version, Status: "enabled",
	})
	if err != nil || updated.Version != item.Version+1 {
		t.Fatalf("版本正确应更新成功并自增，got version=%d err=%v", updated.Version, err)
	}
	if _, err := updateScript(ctx, db, item.ID, scriptInput{
		Title: "过期版本", Content: "内容", ExpectedVersion: item.Version,
	}); !errors.Is(err, errConflict) {
		t.Fatalf("版本不符应返回 errConflict，got %v", err)
	}
	disabled, err := setScriptStatus(ctx, db, item.ID, updated.Version, "disabled")
	if err != nil || disabled.Status != "disabled" {
		t.Fatalf("禁用话术失败，got status=%s err=%v", disabled.Status, err)
	}
}

// 工作台待办：待付款超时单与待结单各计入一项；非超管只统计本人负责的订单。
func TestCsWorkbenchTodos(t *testing.T) {
	db := openCSDB(t, "esports_cs_todos")
	ctx := t.Context()
	now := time.Now()

	create := func(no string, admin int, status esportsorder.FulfillmentStatus, deadline time.Time) {
		t.Helper()
		if _, err := db.EsportsOrder.Create().SetOrderNo(no).SetCustomerID(1).SetSkuID(1).
			SetQuantity(1).SetUnitPriceCents(100).SetPayableCents(100).
			SetPromisedServiceUnits(1).SetRequiredSeats(1).SetResponsibleAdminID(admin).
			SetFulfillmentStatus(status).SetPaymentDeadline(deadline).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// 时间统一用 UTC 存储，与查询条件 now.UTC() 口径一致，避免时区偏移导致比较错位。
	create("o-overdue", 1, esportsorder.FulfillmentStatusPendingPayment, now.UTC().Add(-time.Hour))
	create("o-close", 1, esportsorder.FulfillmentStatusPendingClose, now.UTC().Add(time.Hour))
	create("o-other", 2, esportsorder.FulfillmentStatusPendingClose, now.UTC().Add(time.Hour))

	todos, err := csWorkbenchTodos(ctx, db, 1, false, now)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, item := range todos {
		counts[item.Key] = item.Count
	}
	if counts["overdue_payment"] != 1 || counts["pending_close"] != 1 {
		t.Fatalf("本人待办应为超时 1 / 待结单 1，got %+v", counts)
	}
	if counts["pending_aftersale"] != 0 || counts["due_follow_up"] != 0 {
		t.Fatalf("无售后与跟进到期时应为 0，got %+v", counts)
	}
}

package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsprioritybookingintent"
)

// csTodoCountByKey 从工作台待办里取指定 key 的计数（缺失即 0）。
func csTodoCountByKey(items []csTodoItem, key string) int {
	for _, item := range items {
		if item.Key == key {
			return item.Count
		}
	}
	return 0
}

// P1-11：优先预约意向被客服处理后必须从待办消解（修复前意向「只增不减」）。
func TestPriorityBookingResolveDropsWorkbenchTodo(t *testing.T) {
	db := openMemberDB(t, "w4_priority_booking_resolve")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	owner := db.Admin.Create().SetEmail("w4-pb-owner@example.com").SetPassword("test").
		SetUID("w4-pb-owner").SetUsername("w4-pb-owner").SaveX(ctx)
	seedMemberLevel(t, db, memberLevelInput{Code: "w4-book", Name: "金卡", GrowthThreshold: 0,
		DiscountBps: 10000, PriorityBooking: memberBoolPtr(true)})
	customer := saveCustomerOrFail(t, db, owner.ID)
	customer = db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("w4-book").SaveX(ctx)
	player := seedPlayer(t, db, 88111, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	intent, err := createPriorityBooking(ctx, db, customer, priorityBookingInput{
		PlayerID: player.ID, RequestedStartAt: now.Add(48 * time.Hour).Format(time.RFC3339), Note: "想约周末",
	}, now)
	if err != nil {
		t.Fatalf("提交预约意向失败：%v", err)
	}
	todos, err := csWorkbenchTodos(ctx, db, owner.ID, false, now)
	if err != nil {
		t.Fatalf("工作台待办失败：%v", err)
	}
	if got := csTodoCountByKey(todos, "member_priority_booking"); got != 1 {
		t.Fatalf("处理前待办应为 1，got=%d", got)
	}

	resolved, err := resolvePriorityBookingIntent(ctx, db, owner.ID, false, intent.ID, "processed", now)
	if err != nil {
		t.Fatalf("处理预约意向失败：%v", err)
	}
	if resolved.Status != esportsprioritybookingintent.StatusProcessed {
		t.Fatalf("状态应为 processed，got=%s", resolved.Status)
	}
	if resolved.ProcessedByAdminID == nil || *resolved.ProcessedByAdminID != owner.ID || resolved.ProcessedAt == nil {
		t.Fatalf("应记录处理人与处理时间：%+v", resolved)
	}
	todos, err = csWorkbenchTodos(ctx, db, owner.ID, false, now)
	if err != nil {
		t.Fatalf("工作台待办失败：%v", err)
	}
	if got := csTodoCountByKey(todos, "member_priority_booking"); got != 0 {
		t.Fatalf("凭证处理后待办应消解为 0，got=%d", got)
	}

	// 重复处理 / 非法状态 / 越权处理都被拒。
	if _, err := resolvePriorityBookingIntent(ctx, db, owner.ID, false, intent.ID, "cancelled", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复处理应返回冲突，实际 %v", err)
	}
	second, err := createPriorityBooking(ctx, db, customer, priorityBookingInput{
		PlayerID: player.ID, RequestedStartAt: now.Add(72 * time.Hour).Format(time.RFC3339),
	}, now)
	if err != nil {
		t.Fatalf("再次提交预约意向失败：%v", err)
	}
	if _, err := resolvePriorityBookingIntent(ctx, db, owner.ID, false, second.ID, "done", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法处理结果应返回参数错误，实际 %v", err)
	}
	if _, err := resolvePriorityBookingIntent(ctx, db, 999, false, second.ID, "cancelled", now); !errors.Is(err, errForbidden) {
		t.Fatalf("非归属客服不得处理他人客户的意向，实际 %v", err)
	}
	// 超管可处理任意归属的意向。
	if _, err := resolvePriorityBookingIntent(ctx, db, 999, true, second.ID, "cancelled", now); err != nil {
		t.Fatalf("超管处理应成功：%v", err)
	}
}

// P1-11：已过期的优先预约意向在读取待办时被惰性消解（不依赖定时任务注册），
// 期望时段已过、以及长期未填期望时段两种情形都要消解。
func TestPriorityBookingExpiredIntentsDropWorkbenchTodo(t *testing.T) {
	db := openMemberDB(t, "w4_priority_booking_expiry")
	defer db.Close()
	ctx := t.Context()
	// 时间统一用 UTC 存储与比较，避免 +08:00 与 Z 两种序列化在同一列上错位比较。
	now := time.Now().UTC()

	owner := db.Admin.Create().SetEmail("w4-pb-expiry@example.com").SetPassword("test").
		SetUID("w4-pb-expiry").SetUsername("w4-pb-expiry").SaveX(ctx)
	seedMemberLevel(t, db, memberLevelInput{Code: "w4-book2", Name: "金卡", GrowthThreshold: 0,
		DiscountBps: 10000, PriorityBooking: memberBoolPtr(true)})
	customer := saveCustomerOrFail(t, db, owner.ID)
	customer = db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("w4-book2").SaveX(ctx)
	player := seedPlayer(t, db, 88112, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// 情形一：期望时段已过。
	past := db.EsportsPriorityBookingIntent.Create().SetCustomerID(customer.ID).SetPlayerID(player.ID).
		SetRequestedStartAt(now.Add(-2 * time.Hour)).
		SetStatus(esportsprioritybookingintent.StatusPending).SaveX(ctx)
	// 情形二：未填期望时段，且提交超过宽限期。
	stale := db.EsportsPriorityBookingIntent.Create().SetCustomerID(customer.ID).SetPlayerID(player.ID).
		SetCreatedAt(now.AddDate(0, 0, -(priorityBookingExpiryGraceDays + 1))).
		SetStatus(esportsprioritybookingintent.StatusPending).SaveX(ctx)

	if got, err := countPriorityBookingsPending(ctx, db, owner.ID, false); err != nil || got != 2 {
		t.Fatalf("消解前待办应为 2，got=%d err=%v", got, err)
	}
	todos, err := csWorkbenchTodos(ctx, db, owner.ID, false, now)
	if err != nil {
		t.Fatalf("工作台待办失败：%v", err)
	}
	if got := csTodoCountByKey(todos, "member_priority_booking"); got != 0 {
		t.Fatalf("过期意向应被惰性消解，待办 got=%d", got)
	}
	for _, id := range []int{past.ID, stale.ID} {
		got := db.EsportsPriorityBookingIntent.GetX(ctx, id)
		if got.Status != esportsprioritybookingintent.StatusCancelled {
			t.Fatalf("意向 %d 应被置为 cancelled，got=%s", id, got.Status)
		}
		if got.ProcessedAt == nil {
			t.Fatalf("意向 %d 应记录处理时间", id)
		}
	}
	// 批量任务重复执行幂等：已无 pending 可消解。
	if count, err := expirePriorityBookingIntents(ctx, db, now, 100); err != nil || count != 0 {
		t.Fatalf("重复执行应无改动，count=%d err=%v", count, err)
	}
	// 未来的意向不受影响。
	future := db.EsportsPriorityBookingIntent.Create().SetCustomerID(customer.ID).SetPlayerID(player.ID).
		SetRequestedStartAt(now.Add(24 * time.Hour)).
		SetStatus(esportsprioritybookingintent.StatusPending).SaveX(ctx)
	if count, err := expirePriorityBookingIntents(ctx, db, now, 100); err != nil || count != 0 {
		t.Fatalf("未来意向不应被过期消解，count=%d err=%v", count, err)
	}
	if got := db.EsportsPriorityBookingIntent.GetX(ctx, future.ID); got.Status != esportsprioritybookingintent.StatusPending {
		t.Fatalf("未来意向应保持 pending，got=%s", got.Status)
	}
}

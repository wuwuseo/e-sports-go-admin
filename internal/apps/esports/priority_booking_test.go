package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsprioritybookingintent"
)

// 专属客服权益（§10.4）：等级开启 dedicated_cs 时，客户卡片返回等级名、专属客服标识与负责客服姓名。
func TestMemberDedicatedCSProfileDisplay(t *testing.T) {
	db := openMemberDB(t, "esports_member_dedicated_cs")
	defer db.Close()
	ctx := t.Context()

	owner := db.Admin.Create().SetEmail("dedicated-cs@example.com").SetPassword("test").
		SetUID("dedicated-cs-owner").SetUsername("dedicated-cs-owner").SetNickname("专属客服小王").SaveX(ctx)
	seedMemberLevel(t, db, memberLevelInput{Code: "silver-dedicated", Name: "银卡", GrowthThreshold: 0, DiscountBps: 10000, DedicatedCs: memberBoolPtr(true)})
	customer := saveCustomerOrFail(t, db, owner.ID)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("silver-dedicated").SaveX(ctx)

	view, err := customerProfileOverview(ctx, db, owner.ID, false, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view["dedicated_cs"] != true {
		t.Fatalf("专属客服标识应为 true，got=%v", view["dedicated_cs"])
	}
	if view["member_level_name"] != "银卡" {
		t.Fatalf("等级名不正确，got=%v", view["member_level_name"])
	}
	if view["owner_admin_name"] != "专属客服小王" {
		t.Fatalf("负责客服姓名不正确，got=%v", view["owner_admin_name"])
	}
	if view["member_level_code"] != "silver-dedicated" {
		t.Fatalf("等级 code 不正确，got=%v", view["member_level_code"])
	}

	// 未开启专属客服权益的等级不显示标识。
	seedMemberLevel(t, db, memberLevelInput{Code: "normal", Name: "普通", GrowthThreshold: 0, DiscountBps: 10000})
	plain := saveCustomerOrFail(t, db, owner.ID)
	db.EsportsCustomer.UpdateOneID(plain.ID).SetMemberLevelCode("normal").SaveX(ctx)
	plainView, err := customerProfileOverview(ctx, db, owner.ID, false, plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plainView["dedicated_cs"] != false {
		t.Fatalf("普通等级不应有专属客服标识，got=%v", plainView["dedicated_cs"])
	}
}

// 优先预约（§10.4）：提交生成 pending 意向并计入工作台待办，非超管按归属隔离。
func TestPriorityBookingSubmitAndTodo(t *testing.T) {
	db := openMemberDB(t, "esports_priority_booking_todo")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	owner := db.Admin.Create().SetEmail("pb-owner@example.com").SetPassword("test").
		SetUID("pb-owner").SetUsername("pb-owner").SaveX(ctx)
	seedMemberLevel(t, db, memberLevelInput{Code: "gold-booking", Name: "金卡", GrowthThreshold: 0, DiscountBps: 10000, PriorityBooking: memberBoolPtr(true)})
	customer := saveCustomerOrFail(t, db, owner.ID)
	customer = db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("gold-booking").SaveX(ctx)
	player := seedPlayer(t, db, 88001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	item, err := createPriorityBooking(ctx, db, customer, priorityBookingInput{
		PlayerID: player.ID, RequestedStartAt: now.Add(48 * time.Hour).Format(time.RFC3339), Note: "想约周末晚场",
	}, now)
	if err != nil {
		t.Fatalf("提交预约意向失败：%v", err)
	}
	if item.Status.String() != "pending" || item.PlayerName == "" {
		t.Fatalf("预约意向落库不正确：%+v", item)
	}

	// 归属客服可见待办计数。
	if got, err := countPriorityBookingsPending(ctx, db, owner.ID, false); err != nil || got != 1 {
		t.Fatalf("归属客服待办计数应为 1，got=%d err=%v", got, err)
	}
	// 非归属客服不可见。
	if got, err := countPriorityBookingsPending(ctx, db, 999, false); err != nil || got != 0 {
		t.Fatalf("非归属客服待办计数应为 0，got=%d err=%v", got, err)
	}
	// 超管可见全部。
	if got, err := countPriorityBookingsPending(ctx, db, owner.ID, true); err != nil || got != 1 {
		t.Fatalf("超管待办计数应为 1，got=%d err=%v", got, err)
	}
	// 工作台待办出现「会员优先预约」条目。
	todos, err := csWorkbenchTodos(ctx, db, owner.ID, false, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range todos {
		if item.Key == "member_priority_booking" && item.Count == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("工作台待办缺少会员优先预约条目：%+v", todos)
	}
}

// 优先预约（§10.4）：无权益等级 / 未定级客户被拒；期望时段须为未来时间。
func TestPriorityBookingRequiresBenefit(t *testing.T) {
	db := openMemberDB(t, "esports_priority_booking_guard")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	owner := db.Admin.Create().SetEmail("pb-guard@example.com").SetPassword("test").
		SetUID("pb-guard").SetUsername("pb-guard").SaveX(ctx)
	seedMemberLevel(t, db, memberLevelInput{Code: "bronze", Name: "青铜", GrowthThreshold: 0, DiscountBps: 10000, PriorityBooking: memberBoolPtr(false)})
	player := seedPlayer(t, db, 88002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// 等级未开启 priority_booking。
	noBenefit := saveCustomerOrFail(t, db, owner.ID)
	noBenefit = db.EsportsCustomer.UpdateOneID(noBenefit.ID).SetMemberLevelCode("bronze").SaveX(ctx)
	if _, err := createPriorityBooking(ctx, db, noBenefit, priorityBookingInput{
		PlayerID: player.ID, RequestedStartAt: now.Add(24 * time.Hour).Format(time.RFC3339),
	}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("无权益等级应被拒，err=%v", err)
	}
	// 未定级。
	unset := saveCustomerOrFail(t, db, owner.ID)
	if _, err := createPriorityBooking(ctx, db, unset, priorityBookingInput{
		PlayerID: player.ID, RequestedStartAt: now.Add(24 * time.Hour).Format(time.RFC3339),
	}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("未定级客户应被拒，err=%v", err)
	}
	// 有权益但时段非法 / 已过期。
	seedMemberLevel(t, db, memberLevelInput{Code: "gold-guard", Name: "金卡", GrowthThreshold: 0, DiscountBps: 10000, PriorityBooking: memberBoolPtr(true)})
	benefit := saveCustomerOrFail(t, db, owner.ID)
	benefit = db.EsportsCustomer.UpdateOneID(benefit.ID).SetMemberLevelCode("gold-guard").SaveX(ctx)
	if _, err := createPriorityBooking(ctx, db, benefit, priorityBookingInput{
		PlayerID: player.ID, RequestedStartAt: now.Add(-time.Hour).Format(time.RFC3339),
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("过期时段应被拒，err=%v", err)
	}
	// 陪玩不存在。
	if _, err := createPriorityBooking(ctx, db, benefit, priorityBookingInput{
		PlayerID: 999999, RequestedStartAt: now.Add(24 * time.Hour).Format(time.RFC3339),
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("陪玩不存在应被拒，err=%v", err)
	}
}

// 优先预约（§10.4）：待办列表按会员等级置顶（门槛越高越靠前），未定级排最后。
func TestPriorityBookingSortedByLevel(t *testing.T) {
	db := openMemberDB(t, "esports_priority_booking_sort")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	owner := db.Admin.Create().SetEmail("pb-sort@example.com").SetPassword("test").
		SetUID("pb-sort").SetUsername("pb-sort").SaveX(ctx)
	seedMemberLevel(t, db, memberLevelInput{Code: "silver-sort", Name: "银卡", GrowthThreshold: 10000, DiscountBps: 10000, PriorityBooking: memberBoolPtr(true)})
	seedMemberLevel(t, db, memberLevelInput{Code: "gold-sort", Name: "金卡", GrowthThreshold: 50000, DiscountBps: 10000, PriorityBooking: memberBoolPtr(true)})
	player := seedPlayer(t, db, 88003, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	submit := func(name, levelCode string) {
		customer := saveCustomerOrFail(t, db, owner.ID)
		customer = db.EsportsCustomer.UpdateOneID(customer.ID).SetDisplayName(name).SetMemberLevelCode(levelCode).SaveX(ctx)
		if _, err := createPriorityBooking(ctx, db, customer, priorityBookingInput{
			PlayerID: player.ID, RequestedStartAt: now.Add(24 * time.Hour).Format(time.RFC3339),
		}, now); err != nil {
			t.Fatal(err)
		}
	}
	submit("银卡客户", "silver-sort")
	submit("金卡客户", "gold-sort")
	// 未定级客户无法自助提交（被拒），这里直接落一条 pending 意向验证排最后。
	unset := saveCustomerOrFail(t, db, owner.ID)
	db.EsportsPriorityBookingIntent.Create().SetCustomerID(unset.ID).SetPlayerID(player.ID).
		SetRequestedStartAt(now.Add(24 * time.Hour)).SetStatus(esportsprioritybookingintent.StatusPending).SaveX(ctx)

	views, err := listPriorityBookings(ctx, db, owner.ID, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("应返回 3 条意向，got=%d", len(views))
	}
	// 金卡（门槛高）→ 银卡 → 未定级。
	if views[0].MemberLevelCode != "gold-sort" || views[1].MemberLevelCode != "silver-sort" || views[2].MemberLevelCode != "" {
		t.Fatalf("排序不正确：%+v", views)
	}
}

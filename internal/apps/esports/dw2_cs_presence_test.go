package esports

import (
	"context"
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscspresence"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
)

// dw2SetPresenceErr 直接返回写入口的错误，供断言失败分支使用（自动转池用例）。
func dw2SetPresenceErr(ctx context.Context, db *ent.Client, adminID int, presence string, now time.Time) (*ent.EsportsCsPresence, error) {
	return setCsPresence(ctx, db, adminID, adminID, false, presence, now)
}

// dw2SetPresence 以「本人打点」调用客服在线状态写入口，避免每处重复传 actor/isSuper。
func dw2SetPresence(t *testing.T, db *ent.Client, adminID int, presence string, now time.Time) *ent.EsportsCsPresence {
	t.Helper()
	row, err := setCsPresence(t.Context(), db, adminID, adminID, false, presence, now)
	if err != nil {
		t.Fatalf("置 %s 应成功：%v", presence, err)
	}
	return row
}

// ：客服在线状态写入口（POST /admin/cs/presence）必须能落库并被同一套在线判定读取。
// 断言：online → 值班口径判在线；随后 offline → 立即判离线（presence 表是权威数据源）。
func TestDw2CsPresenceWriteEntry(t *testing.T) {
	db := openTransferDB(t, "esports_dw2_presence")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	admin := seedAdminAccount(t, db, "dw2presence")
	db.Admin.UpdateOneID(admin.ID).SetLastLogin(now).SaveX(ctx)

	row := dw2SetPresence(t, db, admin.ID, "online", now)
	if row.Presence != esportscspresence.PresenceOnline || row.LastActiveAt == nil || row.LastOnlineAt == nil {
		t.Fatalf("在线打点字段不完整：%+v", row)
	}
	activities, err := csAdminActivities(ctx, db, []int{admin.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !activities[admin.ID].Online {
		t.Fatal("置在线后应判在线")
	}

	// 幂等写入口：同一客服只有一条 presence 档案。
	dw2SetPresence(t, db, admin.ID, "busy", now.Add(time.Minute))
	if count := db.EsportsCsPresence.Query().CountX(ctx); count != 1 {
		t.Fatalf("同一客服只应有一条 presence 档案，got=%d", count)
	}
	dw2SetPresence(t, db, admin.ID, "offline", now.Add(2*time.Minute))
	activities, err = csAdminActivities(ctx, db, []int{admin.ID}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if activities[admin.ID].Online {
		t.Fatal("显式离线后应判离线（presence 表权威）")
	}
	// 非法状态、越权代他人打点、不存在的管理员都必须明确失败。
	if _, err = setCsPresence(ctx, db, admin.ID, admin.ID, false, "sleeping", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法 presence 应报参数错误：%v", err)
	}
	if _, err = setCsPresence(ctx, db, admin.ID, admin.ID+1, false, "online", now); !errors.Is(err, errForbidden) {
		t.Fatalf("非超管代他人打点应被拒：%v", err)
	}
	if _, err = setCsPresence(ctx, db, 999999, 999999, false, "online", now); err == nil {
		t.Fatal("不存在的管理员应报错")
	}
}

// 验收断言：客服离线后，其名下 pending_dispatch 订单被自动清空 responsible_admin_id
// 进入公共池并留事件。本用例用 presence 表（而非 last_login）驱动离线判定。
func TestDw2OfflineCsOrdersAutoTransferToPublicPool(t *testing.T) {
	db := openTransferDB(t, "esports_dw2_presence_transfer")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	setUpConfig(t, db, csBacklogAutoTransferMinutesKey, "60")

	offlineAdmin := seedAdminAccount(t, db, "dw2offline")
	// 离线且最近活跃早于滞留阈值（3h > 60min）：满足「离线超时」触发条件。
	db.Admin.UpdateOneID(offlineAdmin.ID).SetLastLogin(now.Add(-3 * time.Hour)).SaveX(ctx)
	if _, err := dw2SetPresenceErr(ctx, db, offlineAdmin.ID, "online", now.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := dw2SetPresenceErr(ctx, db, offlineAdmin.ID, "offline", now.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	onlineAdmin := seedAdminAccount(t, db, "dw2online")
	if _, err := dw2SetPresenceErr(ctx, db, onlineAdmin.ID, "online", now); err != nil {
		t.Fatal(err)
	}
	// 显式离线但 1 分钟前还在活跃：未超滞留阈值，不得转池（离线与「滞留时长」是两个独立条件）。
	freshOfflineAdmin := seedAdminAccount(t, db, "dw2freshoffline")
	if _, err := dw2SetPresenceErr(ctx, db, freshOfflineAdmin.ID, "online", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := dw2SetPresenceErr(ctx, db, freshOfflineAdmin.ID, "offline", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	offlineOrder := seedPaidOrder(t, db, "dw2-presence-offline", 1)
	db.EsportsOrder.UpdateOneID(offlineOrder.ID).SetResponsibleAdminID(offlineAdmin.ID).SaveX(ctx)
	onlineOrder := seedPaidOrder(t, db, "dw2-presence-online", 1)
	db.EsportsOrder.UpdateOneID(onlineOrder.ID).SetResponsibleAdminID(onlineAdmin.ID).SaveX(ctx)
	freshOrder := seedPaidOrder(t, db, "dw2-presence-fresh", 1)
	db.EsportsOrder.UpdateOneID(freshOrder.ID).SetResponsibleAdminID(freshOfflineAdmin.ID).SaveX(ctx)

	released, err := autoTransferBacklogOrders(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("自动转池应成功：%v", err)
	}
	if released != 1 {
		t.Fatalf("只应释放「离线且活跃超阈值」客服名下的 1 单，got=%d", released)
	}
	got := db.EsportsOrder.GetX(ctx, offlineOrder.ID)
	if got.ResponsibleAdminID != nil {
		t.Fatal("离线客服名下的待派单订单应进入公共池")
	}
	if db.EsportsOrder.GetX(ctx, onlineOrder.ID).ResponsibleAdminID == nil {
		t.Fatal("在线客服名下的订单不应被释放")
	}
	if db.EsportsOrder.GetX(ctx, freshOrder.ID).ResponsibleAdminID == nil {
		t.Fatal("刚活跃过的客服名下的订单不应被释放")
	}
	if !db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(offlineOrder.ID),
		esportsorderevent.EventTypeEQ("order.released_to_public_pool"),
	).ExistX(ctx) {
		t.Fatal("自动转池必须写订单事件留痕")
	}
	if status := db.EsportsOrder.GetX(ctx, offlineOrder.ID).FulfillmentStatus; status != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("转池后仍是待派单状态（仅清空归属客服）：%v", status)
	}
}

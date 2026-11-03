package esports

// 第 4 轮后台补缺（A/B/C/D 四条集成缺口）回归测试。
//
// A：GET /user/player/grab-hall 每单必须带 game（SKU → 商品分类 → 游戏分区），
//    解析不到就空串，不得编造；既有字段不得回归。
// B：GET /admin/dispatch/board 必须能拿到逐人上下线明细 players[]。
// C：企微侧边栏配置必须下发 footer_tip（底部提示，来自系统配置键，默认空串）。
// D：page 图标短标识必须命中允许清单，清单外的短标识明确 400（URL / 站内路径不受限）。

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"
)

const (
	// w4gapPlayerUID 抢单大厅测试用的陪玩端登录 uid。
	w4gapPlayerUID = "w4gap-player"
	// w4gapGame 测试用游戏分区名（分类 Game 字段）。
	w4gapGame = "王者荣耀"
)

// w4gapSwapORM 把包级 DB 换成测试库，返回还原函数。
func w4gapSwapORM(t *testing.T, db *ent.Client) func() {
	t.Helper()
	previous := global.ORM
	global.ORM = db
	return func() { global.ORM = previous }
}

// w4gapGrabHallApp 构造抢单大厅的最小用户端路由（与 app.go 同路径）。
func w4gapGrabHallApp(uid string) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", uid)
		return c.Next()
	})
	app.Get("/user/player/grab-hall", playerGrabHallHandler)
	return app
}

// w4gapSeedGrabbingOrder 建一张可抢的单席位 grabbing 订单（已支付、需 senior 等级）。
func w4gapSeedGrabbingOrder(t *testing.T, db *ent.Client, orderNo string, skuID int, now time.Time) *ent.EsportsOrder {
	t.Helper()
	return db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(skuID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetRequiredLevel("senior").
		SetPaymentDeadline(now.Add(time.Hour)).SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusGrabbing).SaveX(t.Context())
}

// ── A：抢单大厅每单带 game（游戏分区筛选） ──

// ：大厅按游戏分区筛选，客户端只能拿到 priority_game（多数单为空）；
// 服务端口径是「SKU → 商品分类 → game」，列表每单必须带上同一口径解析出的 game。
func TestW4GapGrabHallReturnsGamePerOrder(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_w4gap_grab_hall")
	defer db.Close()
	defer w4gapSwapORM(t, db)()
	ctx := t.Context()
	now := time.Now().UTC()

	sku := seedGameSku(t, db, "W4GAP", w4gapGame)
	order := w4gapSeedGrabbingOrder(t, db, "W4GAP-1", sku.ID, now)

	account := db.User.Create().SetEmail(w4gapPlayerUID + "@example.com").SetUID(w4gapPlayerUID).
		SetStatus(user.StatusActive).SetPhone("13900000009").SaveX(ctx)
	player := seedPlayer(t, db, account.ID, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetGames([]string{w4gapGame}).
		SetLevel("mentor").
		SetGameLevels(map[string]string{w4gapGame: "mentor"}).SaveX(ctx)

	status, payload := sendJSON(t, w4gapGrabHallApp(w4gapPlayerUID), "GET", "/user/player/grab-hall", "")
	if status != 200 {
		t.Fatalf("抢单大厅应成功，实际 %d %+v", status, payload)
	}
	rows, _ := payload["data"].([]any)
	if len(rows) != 1 {
		t.Fatalf("应回 1 条可抢单，实际 %+v", payload)
	}
	item, _ := rows[0].(map[string]any)
	if item == nil {
		t.Fatalf("大厅条目结构异常：%+v", rows[0])
	}
	game, ok := item["game"]
	if !ok {
		t.Fatalf("大厅条目缺少 game 字段（客户端无法按游戏分区筛选）：%+v", item)
	}
	if game != w4gapGame {
		t.Fatalf("game 应由 SKU → 分类解析为 %q，实际 %v", w4gapGame, game)
	}
	if item["id"] == nil || item["order_no"] != order.OrderNo {
		t.Fatalf("既有订单字段回归：%+v", item)
	}
	if _, ok := item["priority_game"]; !ok {
		t.Fatalf("既有 priority_game 字段缺失：%+v", item)
	}
}

// ：SKU 无分类（解析不到 game）时回空串，不得用 priority_game 或猜测值顶替。
func TestW4GapGrabHallGameEmptyWhenUnresolved(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_w4gap_grab_hall_empty")
	defer db.Close()
	defer w4gapSwapORM(t, db)()
	ctx := t.Context()
	now := time.Now().UTC()

	sku, err := saveSku(ctx, db, skuInput{
		Code: "W4GAP-NOCAT", Title: "无分类商品", ServiceType: "play",
		UnitPriceCents: 1000, ServiceUnits: 1, ServiceUnitName: "局",
		RequiredSeats: 1, QuotaTotal: 10, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	w4gapSeedGrabbingOrder(t, db, "W4GAP-2", sku.ID, now)

	account := db.User.Create().SetEmail("w4gap-nocat@example.com").SetUID("w4gap-nocat").
		SetStatus(user.StatusActive).SetPhone("13900000010").SaveX(ctx)
	player := seedPlayer(t, db, account.ID, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetLevel("mentor").SaveX(ctx)

	status, payload := sendJSON(t, w4gapGrabHallApp("w4gap-nocat"), "GET", "/user/player/grab-hall", "")
	if status != 200 {
		t.Fatalf("抢单大厅应成功，实际 %d %+v", status, payload)
	}
	rows, _ := payload["data"].([]any)
	if len(rows) != 1 {
		t.Fatalf("应回 1 条可抢单，实际 %+v", payload)
	}
	item, _ := rows[0].(map[string]any)
	if game, ok := item["game"]; !ok || game != "" {
		t.Fatalf("解析不到游戏分区时应回空串（不得编造），实际 %+v", item)
	}
}

// ── B：看板逐人上下线明细 ──

// 03：看板只有 online_players 计数，客服无法做逐人明细；
// 响应必须补 players[] = {player_id, nickname, presence, last_active_at}，
// 且在线口径与 online_players 计数完全一致（同一 presence 字段，不另立判定）。
func TestW4GapDispatchBoardPlayerPresences(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_w4gap_board_players")
	defer db.Close()
	defer w4gapSwapORM(t, db)()
	ctx := t.Context()
	now := time.Now().UTC()

	admin := seedAdminAccount(t, db, "w4gap-board-admin")

	online := seedPlayer(t, db, 8101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(online.ID).SetDisplayName("在线陪玩").SetLastHeartbeatAt(now).SaveX(ctx)
	stale := seedPlayer(t, db, 8104, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(stale.ID).SetDisplayName("挂机陪玩").
		SetLastHeartbeatAt(now.Add(-48 * time.Hour)).SaveX(ctx)
	offline := seedPlayer(t, db, 8102, esportsplayer.QualificationQualified, esportsplayer.PresenceOffline)
	db.EsportsPlayer.UpdateOneID(offline.ID).SetDisplayName("离线陪玩").SaveX(ctx)
	// 未合格陪玩不计入在线口径（与 online_players 计数一致）。
	seedPlayer(t, db, 8103, esportsplayer.QualificationPending, esportsplayer.PresenceOnline)

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", true)
		return c.Next()
	})
	app.Get("/admin/dispatch/board", dispatchBoardHandler)

	status, payload := sendJSON(t, app, "GET", "/admin/dispatch/board", "")
	if status != 200 {
		t.Fatalf("看板应成功，实际 %d %+v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	if data == nil {
		t.Fatalf("看板响应结构异常：%+v", payload)
	}
	if got, _ := data["online_players"].(float64); int(got) != 2 {
		t.Fatalf("online_players 既有口径（presence=online 计数）应为 2，实际 %v", data["online_players"])
	}
	rows, ok := data["players"].([]any)
	if !ok {
		t.Fatalf("看板缺少逐人明细 players[]：%+v", data)
	}
	if len(rows) != 3 {
		t.Fatalf("应回 3 名合格陪玩的在线明细，实际 %+v", rows)
	}
	first, _ := rows[0].(map[string]any)
	if first == nil {
		t.Fatalf("明细条目结构异常：%+v", rows[0])
	}
	for _, key := range []string{"player_id", "nickname", "presence", "last_active_at"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("明细条目缺少 %q：%+v", key, first)
		}
	}
	if id, _ := first["player_id"].(float64); int(id) != online.ID {
		t.Fatalf("在线陪玩应排在明细首位，实际 %+v", rows)
	}
	if first["presence"] != string(esportsplayer.PresenceOnline) || first["nickname"] != "在线陪玩" {
		t.Fatalf("明细条目内容不正确：%+v", first)
	}
	if first["last_active_at"] == nil || first["connection_healthy"] != true {
		t.Fatalf("心跳新鲜的在线陪玩应记为连接正常：%+v", first)
	}
	// 心跳失联的「挂机在线」：presence 仍为 online（与计数同口径），但连接健康为 false。
	staleRow, _ := rows[1].(map[string]any)
	if id, _ := staleRow["player_id"].(float64); int(id) != stale.ID {
		t.Fatalf("挂机陪玩应在明细第二位，实际 %+v", rows)
	}
	if staleRow["presence"] != string(esportsplayer.PresenceOnline) || staleRow["connection_healthy"] != false {
		t.Fatalf("心跳失联的挂机在线档案应标记连接异常：%+v", staleRow)
	}
	last, _ := rows[2].(map[string]any)
	if id, _ := last["player_id"].(float64); int(id) != offline.ID {
		t.Fatalf("离线陪玩应在明细末位，实际 %+v", rows)
	}
	if last["presence"] != string(esportsplayer.PresenceOffline) {
		t.Fatalf("离线陪玩 presence 应为 offline：%+v", last)
	}
}

// ── C：企微侧边栏底部提示 ──

// ：侧边栏 H5 需要后端下发「底部提示」区块内容；
// 值来自系统配置键，未配置 / 空值回落空串（= 不展示该区块）。
func TestW4GapWecomSidebarFooterTip(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_w4gap_sidebar_tip")
	defer db.Close()
	defer w4gapSwapORM(t, db)()
	ctx := t.Context()

	app := fiber.New()
	app.Get("/wecom/sidebar-config", publicWecomSidebarConfig)

	// 未配置：空串（前端据此不展示底部提示区块）。
	status, payload := sendJSON(t, app, "GET", "/wecom/sidebar-config", "")
	if status != 200 {
		t.Fatalf("侧边栏配置应成功，实际 %d %+v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	if data == nil {
		t.Fatalf("侧边栏配置结构异常：%+v", payload)
	}
	tip, ok := data["footer_tip"]
	if !ok {
		t.Fatalf("侧边栏配置缺少 footer_tip：%+v", data)
	}
	if tip != "" {
		t.Fatalf("未配置时 footer_tip 应为空串，实际 %v", tip)
	}
	if data["auth_mode"] == nil || data["js_sdk_enabled"] == nil {
		t.Fatalf("既有 auth_mode / js_sdk_enabled 字段回归：%+v", data)
	}

	// 运营配置后：原样下发（去首尾空白）。
	if err := db.SystemConfig.Create().SetKey("app.esports.wecom_sidebar_footer_tip").
		SetName("企微侧边栏底部提示").SetValue("  如有疑问请联系值班客服  ").SetType("string").
		SetGroup("esports").SetStatus(1).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	_, payload = sendJSON(t, app, "GET", "/wecom/sidebar-config", "")
	data, _ = payload["data"].(map[string]any)
	if data["footer_tip"] != "如有疑问请联系值班客服" {
		t.Fatalf("footer_tip 应取系统配置键的值，实际 %+v", data["footer_tip"])
	}
}

// ── D：page 图标短标识允许清单（残留风险） ──

// w4gapIconConfig 用给定短标识构造一份含客服入口的合法装修配置。
func w4gapIconConfig(icon string) pageConfig {
	visible := true
	return pageConfig{Components: []pageComponent{
		{Type: "quick_nav", Visible: &visible, Props: map[string]interface{}{
			"items": []interface{}{map[string]interface{}{
				"name": "我的订单", "target": "orders", "icon": icon,
			}},
		}},
		{Type: "support_entry", Visible: &visible},
	}}
}

// ：客户端没有内置图标集，后端不能放行任意短标识——
// 清单外的形如 icon-nonexistent 的短标识必须 400（中文提示），
// 而 https:// 图片地址与站内相对路径照旧放行。
func TestW4GapPageIconAllowlistRejectsUnknownShortID(t *testing.T) {
	err := validatePageConfig(w4gapIconConfig("icon-nonexistent"))
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("清单外短标识应返回参数错误，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "icon-nonexistent") {
		t.Fatalf("错误文案应指明被拒标识，实际：%v", err)
	}

	// 基准清单内的短标识（代码 / 文档中实际出现的 icon-cs）仍然放行。
	if err = validatePageConfig(w4gapIconConfig("icon-cs")); err != nil {
		t.Fatalf("基准清单内的短标识应放行，实际：%v", err)
	}
	// 图片 URL 与站内路径不受短标识清单限制。
	for _, icon := range []string{"https://cdn.example.com/icon.png", "/static/nav.png"} {
		if err = validatePageConfig(w4gapIconConfig(icon)); err != nil {
			t.Fatalf("图标 %q 应放行，实际：%v", icon, err)
		}
	}
	// 既有非法字符校验不回归。
	if err = validatePageConfig(w4gapIconConfig("icon cs")); !errors.Is(err, errInvalidInput) {
		t.Fatalf("含空格的图标取值应被拒，实际：%v", err)
	}
}

// ：允许清单可由运营通过系统配置键扩展（默认 = 基准清单）。
func TestW4GapPageIconAllowlistExtendsFromConfig(t *testing.T) {
	db := openPageDB(t, "esports_w4gap_page_icon_allowlist")
	defer db.Close()
	ctx := t.Context()

	channel := pageChannelCustomerHome
	adminID := 1
	if err := db.SystemConfig.Create().SetKey("app.esports.page_icon_allowlist").
		SetName("页面图标短标识允许清单").SetValue("icon-fancy").SetType("string").
		SetGroup("esports").SetStatus(1).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := savePageDraft(ctx, db, channel, adminID, pageDraftInput{
		Components: w4gapIconConfig("icon-fancy").Components,
	}); err != nil {
		t.Fatalf("运营扩展清单内的短标识应被放行，实际：%v", err)
	}
	// 扩展后清单外标识仍然被拒（扩展是加法，不是关闭校验）。
	if _, err := savePageDraft(ctx, db, channel, adminID, pageDraftInput{
		Components: w4gapIconConfig("icon-still-unknown").Components, ExpectedVersion: 1,
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("清单外短标识应仍被拒，实际：%v", err)
	}
}

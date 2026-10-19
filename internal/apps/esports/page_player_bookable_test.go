package esports

import (
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/global"
)

// 本文件是 T7 的回归：publicPlayerView.Bookable 必须回填「此刻真的可接单」，
// 客户端不能再拿 Online 当 Bookable（§6.4-06 服务时段 / 接单容量，§16.3-07 陪玩推荐组件）。
// 辅助物统一 pbk 前缀，避免与同包其它并行任务的测试辅助物撞名；播种 / HTTP 断言复用
// page_component_data_test.go 的 pcq* helper 与 dispatch_test.go 的 seedPaidOrder。

// pbkClosedSlot 返回一个「当前时刻一定不落在其中」的服务时段（UTC+8 当日分钟数口径）：
// 平时用 [0,1)（只含 00:00），恰逢 00:xx 时改用 [1,2)，避免测试对真实时刻的依赖。
func pbkClosedSlot(now time.Time) (int, int) {
	local := now.In(cstZone)
	minute := local.Hour()*60 + local.Minute()
	if minute == 0 {
		return 1, 2
	}
	return 0, 1
}

// pbkPayloadBookable 取出响应 data 每一项的 id → bookable。
// 字段缺失直接失败：默认调用（不传任何筛选参数）也必须下发 bookable。
func pbkPayloadBookable(t *testing.T, payload map[string]any) map[string]bool {
	t.Helper()
	items, ok := payload["data"].([]any)
	if !ok {
		t.Fatalf("响应 data 不是数组：%+v", payload)
	}
	out := make(map[string]bool, len(items))
	for _, item := range items {
		row, _ := item.(map[string]any)
		id := fmt.Sprint(row["id"])
		value, exists := row["bookable"]
		if !exists {
			t.Fatalf("返回行缺少 bookable 字段：%+v", row)
		}
		flag, isBool := value.(bool)
		if !isBool {
			t.Fatalf("bookable 不是布尔值：%+v", row)
		}
		out[id] = flag
	}
	return out
}

// 便宜判定（连接健康 / 服务时段 / 固排时段）任一不满足即 Bookable=false，
// 且未开 bookable 开关时这些行仍然返回（只是标记为 false）。
func TestPlayerBookableFlagsConnectionServiceSlotAndFixedTeamSlot(t *testing.T) {
	db := openGiftDB(t, "esports_player_bookable_flags")
	defer db.Close()
	ctx := t.Context()
	now := pcqTestNow()

	inSlot := pcqSeedPlayer(t, db, 9631, "skilled")
	outSlot := pcqSeedPlayer(t, db, 9632, "skilled")
	fixedClosed := pcqSeedPlayer(t, db, 9633, "skilled")
	stale := pcqSeedPlayer(t, db, 9634, "skilled")
	unconfigured := pcqSeedPlayer(t, db, 9635, "skilled")

	// 当前 20:00（分钟数 1200）：该窗口落在时段内。
	db.EsportsPlayer.UpdateOneID(inSlot.ID).
		SetServiceSlotStartMinute(1140).SetServiceSlotEndMinute(1260).ExecX(ctx)
	// 该窗口为 [00:00, 01:00)，当前时刻明显在时段外。
	db.EsportsPlayer.UpdateOneID(outSlot.ID).
		SetServiceSlotStartMinute(0).SetServiceSlotEndMinute(60).ExecX(ctx)
	// 固排开启但固排时段同样在窗口外。
	db.EsportsPlayer.UpdateOneID(fixedClosed.ID).
		SetFixedTeamEnabled(true).SetFixedTeamSlotStartMinute(0).SetFixedTeamSlotEndMinute(60).ExecX(ctx)
	// 心跳陈旧（30 分钟 > 默认 10 分钟阈值）：presence 仍在线，但连接不健康。
	db.EsportsPlayer.UpdateOneID(stale.ID).
		SetLastHeartbeatAt(now.Add(-30 * time.Minute)).ExecX(ctx)

	views, err := listPublicPlayersByProps(ctx, db, playerRecommendProps{Limit: 100}, now)
	if err != nil {
		t.Fatalf("取推荐陪玩失败：%v", err)
	}
	// 未开任何筛选开关：不可接单的陪玩照样返回，顺序仍为 ID 倒序（既有口径）。
	if got, want := pcqPlayerIDs(views), []int{
		unconfigured.ID, stale.ID, fixedClosed.ID, outSlot.ID, inSlot.ID,
	}; !slices.Equal(got, want) {
		t.Fatalf("默认返回应包含全部在线陪玩且保持 ID 倒序：得到 %v，期望 %v", got, want)
	}
	flags := make(map[int]bool, len(views))
	for _, view := range views {
		flags[view.ID] = view.Bookable
	}
	if !flags[inSlot.ID] {
		t.Fatalf("在线且在服务时段内、无接单上限的陪玩应可接单：%+v", flags)
	}
	if !flags[unconfigured.ID] {
		t.Fatalf("未配置服务时段 / 固排 / 接单上限（既有口径=不限）应可接单：%+v", flags)
	}
	for name, id := range map[string]int{
		"不在服务时段": outSlot.ID,
		"不在固排时段": fixedClosed.ID,
		"连接不健康":  stale.ID,
	} {
		if flags[id] {
			t.Fatalf("%s的陪玩不应被标记为可接单：%+v", name, flags)
		}
	}
}

// 容量判定沿用既有 checkPlayerDispatchLimit 口径：配置了每日上限且当日已占满 → Bookable=false，
// 未配置上限的陪玩即使有占位也仍可接单（不为他们执行容量判定）。
func TestPlayerBookableFalseWhenDailyCapacityFull(t *testing.T) {
	db := openGiftDB(t, "esports_player_bookable_capacity")
	defer db.Close()
	ctx := t.Context()
	now := pcqTestNow()

	dailyFull := pcqSeedPlayer(t, db, 9641, "skilled")
	db.EsportsPlayer.UpdateOneID(dailyFull.ID).SetDailyLimit(1).ExecX(ctx)
	fullOrder := seedPaidOrder(t, db, "PBK-CAP-DAILY-FULL", 1)
	db.EsportsDispatchSeat.Create().SetOrderID(fullOrder.ID).SetSeatIndex(1).
		SetPlayerID(dailyFull.ID).SetStatus(esportsdispatchseat.StatusAccepted).
		SetRespondedAt(now.UTC()).SaveX(ctx)

	dailyFree := pcqSeedPlayer(t, db, 9642, "skilled")
	db.EsportsPlayer.UpdateOneID(dailyFree.ID).SetDailyLimit(1).ExecX(ctx)

	// 有占位但未配置任何接单上限：既有口径为「不限」，应保持可接单。
	unbounded := pcqSeedPlayer(t, db, 9643, "skilled")
	unboundedOrder := seedPaidOrder(t, db, "PBK-CAP-UNBOUNDED", 1)
	db.EsportsDispatchSeat.Create().SetOrderID(unboundedOrder.ID).SetSeatIndex(1).
		SetPlayerID(unbounded.ID).SetStatus(esportsdispatchseat.StatusAccepted).
		SetRespondedAt(now.UTC()).SaveX(ctx)

	views, err := listPublicPlayersByProps(ctx, db, playerRecommendProps{Limit: 100}, now)
	if err != nil {
		t.Fatalf("取推荐陪玩失败：%v", err)
	}
	flags := make(map[int]bool, len(views))
	for _, view := range views {
		flags[view.ID] = view.Bookable
	}
	if flags[dailyFull.ID] {
		t.Fatalf("当日每日上限已用满的陪玩不应可接单：%+v", flags)
	}
	if !flags[dailyFree.ID] {
		t.Fatalf("配置了每日上限但当日未占用的陪玩应可接单：%+v", flags)
	}
	if !flags[unbounded.ID] {
		t.Fatalf("未配置接单上限的陪玩不应被容量判定拦截：%+v", flags)
	}

	// bookable=true 收窄：只留下真正可接单的（ID 倒序）。
	narrowed, err := listPublicPlayersByProps(ctx, db, playerRecommendProps{Bookable: true, Limit: 100}, now)
	if err != nil {
		t.Fatalf("bookable=true 取数失败：%v", err)
	}
	if got, want := pcqPlayerIDs(narrowed), []int{unbounded.ID, dailyFree.ID}; !slices.Equal(got, want) {
		t.Fatalf("bookable=true 应过滤掉当日已满的陪玩：得到 %v，期望 %v", got, want)
	}
}

// 默认调用（不传任何筛选参数）的响应 JSON 必须带 bookable 字段，
// 且「在线但不在服务时段」的陪玩为 false、在时段内的为 true。
func TestPublicPlayersBookableFlagInDefaultJSON(t *testing.T) {
	db := openGiftDB(t, "esports_player_bookable_json")
	defer db.Close()
	prev := global.ORM
	global.ORM = db
	defer func() { global.ORM = prev }()
	ctx := t.Context()

	// [0, 1440) 覆盖全天，无需关心真实请求时刻。
	open := pcqSeedPlayer(t, db, 9651, "skilled")
	db.EsportsPlayer.UpdateOneID(open.ID).
		SetServiceSlotStartMinute(0).SetServiceSlotEndMinute(1440).ExecX(ctx)

	closed := pcqSeedPlayer(t, db, 9652, "skilled")
	start, end := pbkClosedSlot(time.Now())
	db.EsportsPlayer.UpdateOneID(closed.ID).
		SetServiceSlotStartMinute(start).SetServiceSlotEndMinute(end).ExecX(ctx)

	app := fiber.New()
	app.Get("/players", publicPlayers)

	status, payload := pcqDoGet(t, app, "/players")
	if status != fiber.StatusOK {
		t.Fatalf("默认陪玩推荐失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	flags := pbkPayloadBookable(t, payload)
	if flag, ok := flags[strconv.Itoa(open.ID)]; !ok || !flag {
		t.Fatalf("在服务时段内的陪玩应为 bookable=true：%+v", flags)
	}
	if flag, ok := flags[strconv.Itoa(closed.ID)]; !ok || flag {
		t.Fatalf("在线但不在服务时段的陪玩应为 bookable=false：%+v", flags)
	}
}

// bookable=true 查询参数仍然只返回可接单陪玩（回归），并与默认调用的标记一致。
func TestPublicPlayersBookableQueryKeepsNarrowing(t *testing.T) {
	db := openGiftDB(t, "esports_player_bookable_query")
	defer db.Close()
	prev := global.ORM
	global.ORM = db
	defer func() { global.ORM = prev }()
	ctx := t.Context()
	realNow := time.Now().UTC()

	available := pcqSeedPlayer(t, db, 9661, "skilled")
	db.EsportsPlayer.UpdateOneID(available.ID).
		SetServiceSlotStartMinute(0).SetServiceSlotEndMinute(1440).ExecX(ctx)

	closed := pcqSeedPlayer(t, db, 9662, "skilled")
	start, end := pbkClosedSlot(time.Now())
	db.EsportsPlayer.UpdateOneID(closed.ID).
		SetServiceSlotStartMinute(start).SetServiceSlotEndMinute(end).ExecX(ctx)

	dailyFull := pcqSeedPlayer(t, db, 9663, "skilled")
	db.EsportsPlayer.UpdateOneID(dailyFull.ID).SetDailyLimit(1).ExecX(ctx)
	order := seedPaidOrder(t, db, "PBK-QUERY-DAILY-FULL", 1)
	db.EsportsDispatchSeat.Create().SetOrderID(order.ID).SetSeatIndex(1).
		SetPlayerID(dailyFull.ID).SetStatus(esportsdispatchseat.StatusAccepted).
		SetRespondedAt(realNow).SaveX(ctx)

	app := fiber.New()
	app.Get("/players", publicPlayers)

	status, payload := pcqDoGet(t, app, "/players?bookable=true")
	if status != fiber.StatusOK {
		t.Fatalf("bookable=true 取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got, want := pcqPayloadField(t, payload, "id"), []string{strconv.Itoa(available.ID)}; !slices.Equal(got, want) {
		t.Fatalf("bookable=true 应只返回可接单陪玩：得到 %v，期望 %v", got, want)
	}
	for id, flag := range pbkPayloadBookable(t, payload) {
		if !flag {
			t.Fatalf("bookable=true 的结果里出现 bookable=false 的行（id=%s）", id)
		}
	}

	// 默认调用仍返回全部在线陪玩，标记与上面的收窄结果一致。
	status, payload = pcqDoGet(t, app, "/players")
	if status != fiber.StatusOK {
		t.Fatalf("默认取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	flags := pbkPayloadBookable(t, payload)
	if len(flags) != 3 {
		t.Fatalf("默认调用应返回全部在线陪玩：%+v", flags)
	}
	if !flags[strconv.Itoa(available.ID)] || flags[strconv.Itoa(closed.ID)] || flags[strconv.Itoa(dailyFull.ID)] {
		t.Fatalf("默认调用的 bookable 标记错误：%+v", flags)
	}
}

// limit 与返回顺序保持既有口径：limit 只限制返回条数，
// 不可接单的行在未开 bookable 开关时同样占据名额（不被丢弃）。
func TestPublicPlayersBookableKeepsLimitAndOrder(t *testing.T) {
	db := openGiftDB(t, "esports_player_bookable_limit")
	defer db.Close()
	ctx := t.Context()
	now := pcqTestNow()

	first := pcqSeedPlayer(t, db, 9671, "skilled")
	second := pcqSeedPlayer(t, db, 9672, "skilled")
	closed := pcqSeedPlayer(t, db, 9673, "skilled")
	db.EsportsPlayer.UpdateOneID(closed.ID).
		SetServiceSlotStartMinute(0).SetServiceSlotEndMinute(60).ExecX(ctx)

	views, err := listPublicPlayersByProps(ctx, db, playerRecommendProps{Limit: 2}, now)
	if err != nil {
		t.Fatalf("limit=2 取数失败：%v", err)
	}
	if got, want := pcqPlayerIDs(views), []int{closed.ID, second.ID}; !slices.Equal(got, want) {
		t.Fatalf("limit 应只截断条数、不改变 ID 倒序，也不丢弃不可接单行：得到 %v，期望 %v", got, want)
	}
	if views[0].Bookable {
		t.Fatalf("不在服务时段的陪玩应回填 bookable=false：%+v", views[0])
	}
	if !views[1].Bookable {
		t.Fatalf("在服务时段内的陪玩应回填 bookable=true：%+v", views[1])
	}

	all, err := listPublicPlayersByProps(ctx, db, playerRecommendProps{Limit: 100}, now)
	if err != nil {
		t.Fatalf("limit=100 取数失败：%v", err)
	}
	if got, want := pcqPlayerIDs(all), []int{closed.ID, second.ID, first.ID}; !slices.Equal(got, want) {
		t.Fatalf("默认返回顺序应为 ID 倒序：得到 %v，期望 %v", got, want)
	}
}

package esports

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportspagedocument"
	"go-admin/internal/ent/esportspageversion"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/global"
)

// §16.3-06 / §16.3-07 的「取数半」：客户端组件接口必须按装修 props 真正收窄取数。
// 本文件全部辅助物以 pcq 前缀命名，避免与并行桶的同名测试辅助物撞名。

// pcqTestNow 固定测试时刻（UTC 12:00 = 业务时区 UTC+8 20:00，当日分钟数 1200）。
func pcqTestNow() time.Time {
	return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
}

// pcqSeedPlayer 造一个「资质合格 + 在线 + 指定技能等级」的陪玩档案。
func pcqSeedPlayer(t *testing.T, db *ent.Client, userID int, level string) *ent.EsportsPlayer {
	t.Helper()
	return db.EsportsPlayer.Create().SetUserID(userID).SetDisplayName("陪玩" + strconv.Itoa(userID)).
		SetQualification(esportsplayer.QualificationQualified).
		SetPresence(esportsplayer.PresenceOnline).
		SetLevel(esportsplayer.Level(level)).SaveX(t.Context())
}

// pcqSeedSku 造一个在售 SKU（CategoryID 为空表示不挂分类）。
func pcqSeedSku(t *testing.T, db *ent.Client, code string, priceCents int64, categoryID *int) *ent.EsportsSku {
	t.Helper()
	item, err := saveSku(t.Context(), db, skuInput{
		Code: code, Title: code, ServiceType: "play", CategoryID: categoryID,
		UnitPriceCents: priceCents, ServiceUnits: 1, ServiceUnitName: "小时",
		RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatalf("造 SKU %s 失败：%v", code, err)
	}
	return item
}

// pcqSeedCategory 造一个启用中的商品分类（game 即游戏分区）。
func pcqSeedCategory(t *testing.T, db *ent.Client, code, game string) *ent.EsportsProductCategory {
	t.Helper()
	return db.EsportsProductCategory.Create().SetCode(code).SetName(code).
		SetGame(game).SetStatus("enabled").SaveX(t.Context())
}

func pcqPlayerIDs(views []publicPlayerView) []int {
	out := make([]int, 0, len(views))
	for _, view := range views {
		out = append(out, view.ID)
	}
	return out
}

func pcqSkuCodes(items []*ent.EsportsSku) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Code)
	}
	return out
}

// pcqDoGet 发起一次公开读请求，返回状态码与解析后的响应体。
func pcqDoGet(t *testing.T, app *fiber.App, path string) (int, map[string]any) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil), fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("请求 %s 失败：%v", path, err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	return resp.StatusCode, payload
}

// pcqPayloadField 取出 data 数组每一项的指定字段（字符串化），顺序即响应顺序。
func pcqPayloadField(t *testing.T, payload map[string]any, field string) []string {
	t.Helper()
	items, ok := payload["data"].([]any)
	if !ok {
		t.Fatalf("响应 data 不是数组：%+v", payload)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		row, _ := item.(map[string]any)
		out = append(out, fmt.Sprint(row[field]))
	}
	return out
}

func pcqMessage(payload map[string]any) string {
	msg, _ := payload["message"].(string)
	return msg
}

// ── §16.3-07 陪玩推荐：game / level / online / bookable ──

// 四个筛选键都必须真的收窄，且各自复用既有判定口径：
// level 走 playerLevelRank（不低于该等级）、online 走 presence + playerConnectionHealthy、
// bookable 走服务时段 / 固排时段 / 在线判定 / 接单容量。
func TestPcqPlayerRecommendPropsNarrowByLevelOnlineBookable(t *testing.T) {
	db := openGiftDB(t, "esports_pcq_player_props")
	defer db.Close()
	ctx := t.Context()
	now := pcqTestNow()

	base := pcqSeedPlayer(t, db, 9301, "senior")
	mentor := pcqSeedPlayer(t, db, 9302, "mentor")
	novice := pcqSeedPlayer(t, db, 9303, "novice")
	staleHeartbeat := pcqSeedPlayer(t, db, 9304, "senior")
	slotClosed := pcqSeedPlayer(t, db, 9305, "senior")
	capacityFull := pcqSeedPlayer(t, db, 9306, "senior")

	// 心跳陈旧（30 分钟 > 默认 10 分钟阈值）：presence 仍是在线，但连接不健康。
	db.EsportsPlayer.UpdateOneID(staleHeartbeat.ID).
		SetLastHeartbeatAt(now.Add(-30 * time.Minute)).ExecX(ctx)
	// 服务时段 [00:00, 01:00)：当前 20:00 落在时段外。
	db.EsportsPlayer.UpdateOneID(slotClosed.ID).
		SetServiceSlotStartMinute(0).SetServiceSlotEndMinute(60).ExecX(ctx)
	// 并发接单上限 1，且已有一个已接单席位占位。
	db.EsportsPlayer.UpdateOneID(capacityFull.ID).SetConcurrentLimit(1).ExecX(ctx)
	order := db.EsportsOrder.Create().SetOrderNo("PCQ-CAPACITY-1").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(10000).SetPayableCents(10000).SetPromisedServiceUnits(1).
		SetRequiredSeats(1).SetPaymentDeadline(now.Add(time.Hour)).SaveX(ctx)
	db.EsportsDispatchSeat.Create().SetOrderID(order.ID).SetSeatIndex(1).
		SetPlayerID(capacityFull.ID).SetStatus(esportsdispatchseat.StatusAccepted).SaveX(ctx)
	// 其余档案心跳新鲜，服务时段与接单上限均未配置（不限）。
	db.EsportsPlayer.Update().Where(esportsplayer.IDNotIn(staleHeartbeat.ID)).
		SetLastHeartbeatAt(now.Add(-time.Minute)).ExecX(ctx)

	cases := []struct {
		name  string
		props playerRecommendProps
		want  []int
	}{
		{
			"未配置筛选键=保持既有默认（合格+在线，ID 倒序）",
			playerRecommendProps{Limit: 100},
			[]int{capacityFull.ID, slotClosed.ID, staleHeartbeat.ID, novice.ID, mentor.ID, base.ID},
		},
		{
			"level=senior 按 playerLevelRank 收窄到不低于 senior",
			playerRecommendProps{Level: "senior", Limit: 100},
			[]int{capacityFull.ID, slotClosed.ID, staleHeartbeat.ID, mentor.ID, base.ID},
		},
		{
			"online=true 复用在线 + 连接正常判定",
			playerRecommendProps{Online: true, Limit: 100},
			[]int{capacityFull.ID, slotClosed.ID, novice.ID, mentor.ID, base.ID},
		},
		{
			"bookable=true 复用服务时段 / 固排 / 容量判定",
			playerRecommendProps{Bookable: true, Limit: 100},
			[]int{novice.ID, mentor.ID, base.ID},
		},
		{
			"三个键同时生效",
			playerRecommendProps{Level: "senior", Online: true, Bookable: true, Limit: 100},
			[]int{mentor.ID, base.ID},
		},
		{
			"limit 收窄返回条数",
			playerRecommendProps{Limit: 1},
			[]int{capacityFull.ID},
		},
	}
	for _, tc := range cases {
		views, err := listPublicPlayersByProps(ctx, db, tc.props, now)
		if err != nil {
			t.Fatalf("%s：%v", tc.name, err)
		}
		if got := pcqPlayerIDs(views); !slices.Equal(got, tc.want) {
			t.Fatalf("%s：得到 %v，期望 %v", tc.name, got, tc.want)
		}
	}

	// 游戏分区沿用既有口径：命中「档案 games 含该分区」或「games 为空（不限分区）」，
	// 仅开通其它分区者被排除。
	db.EsportsPlayer.UpdateOneID(base.ID).SetGames([]string{"英雄联盟"}).ExecX(ctx)
	db.EsportsPlayer.UpdateOneID(novice.ID).SetGames([]string{"王者荣耀"}).ExecX(ctx)
	views, err := listPublicPlayersByProps(ctx, db, playerRecommendProps{Game: "英雄联盟", Limit: 100}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := pcqPlayerIDs(views); !slices.Equal(got,
		[]int{capacityFull.ID, slotClosed.ID, staleHeartbeat.ID, mentor.ID, base.ID}) {
		t.Fatalf("game 过滤应排除仅开通其它分区的陪玩：%v", got)
	}
}

// 客户端把 props 透传成查询参数：handler 必须按这些参数收窄，非法取值明确 400。
func TestPcqPublicPlayersHandlerHonoursProps(t *testing.T) {
	db := openGiftDB(t, "esports_pcq_player_handler")
	defer db.Close()
	prev := global.ORM
	global.ORM = db
	defer func() { global.ORM = prev }()
	ctx := t.Context()

	senior := pcqSeedPlayer(t, db, 9401, "senior")
	pcqSeedPlayer(t, db, 9402, "novice")
	staleSenior := pcqSeedPlayer(t, db, 9403, "senior")
	db.EsportsPlayer.UpdateOneID(senior.ID).
		SetLastHeartbeatAt(time.Now().UTC().Add(-time.Minute)).ExecX(ctx)
	db.EsportsPlayer.UpdateOneID(staleSenior.ID).
		SetLastHeartbeatAt(time.Now().UTC().Add(-30 * time.Minute)).ExecX(ctx)

	app := fiber.New()
	app.Get("/players", publicPlayers)

	status, payload := pcqDoGet(t, app, "/players?level=senior&online=true")
	if status != fiber.StatusOK {
		t.Fatalf("按 props 取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got := pcqPayloadField(t, payload, "id"); !slices.Equal(got, []string{strconv.Itoa(senior.ID)}) {
		t.Fatalf("level+online 未按 props 收窄：%v", got)
	}

	// filter 字典形态（§16.2-05 的「筛选条件」）同样被消费。
	status, payload = pcqDoGet(t, app, "/players?filter="+url.QueryEscape(`{"level":"senior","online":true}`))
	if status != fiber.StatusOK {
		t.Fatalf("filter 字典取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got := pcqPayloadField(t, payload, "id"); !slices.Equal(got, []string{strconv.Itoa(senior.ID)}) {
		t.Fatalf("filter 字典未按 props 收窄：%v", got)
	}

	// 非法等级 / 非法布尔值：明确参数错误，不静默忽略。
	if status, payload = pcqDoGet(t, app, "/players?level=magic"); status != fiber.StatusBadRequest {
		t.Fatalf("未知等级应 400，实际 %d：%+v", status, payload)
	}
	if status, payload = pcqDoGet(t, app, "/players?online=maybe"); status != fiber.StatusBadRequest {
		t.Fatalf("非法在线取值应 400，实际 %d：%+v", status, payload)
	}
}

// ── §16.3-06 / §16.3-11 商品列表：显式 id / limit / filter / sort / 规则取数 ──

// 显式 id 列表优先，limit 与既有 100 条上限取更严者；未配置时保持既有默认返回。
func TestPcqPublicSkusExplicitIDsAndLimit(t *testing.T) {
	db := openGiftDB(t, "esports_pcq_sku_ids")
	defer db.Close()
	prev := global.ORM
	global.ORM = db
	defer func() { global.ORM = prev }()
	ctx := t.Context()

	first := pcqSeedSku(t, db, "PCQ-SKU-A", 1000, nil)
	second := pcqSeedSku(t, db, "PCQ-SKU-B", 2000, nil)
	pcqSeedSku(t, db, "PCQ-SKU-C", 3000, nil)
	offSale := pcqSeedSku(t, db, "PCQ-SKU-OFF", 500, nil)
	db.EsportsSku.UpdateOneID(offSale.ID).SetStatus("off_sale").ExecX(ctx)

	app := fiber.New()
	app.Get("/skus", publicSkus)

	status, payload := pcqDoGet(t, app, "/skus")
	if status != fiber.StatusOK {
		t.Fatalf("默认商品列表失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got := pcqPayloadField(t, payload, "code"); !slices.Equal(got, []string{"PCQ-SKU-C", "PCQ-SKU-B", "PCQ-SKU-A"}) {
		t.Fatalf("未配置 props 时应保持既有默认（仅在售 + ID 倒序）：%v", got)
	}

	status, payload = pcqDoGet(t, app, fmt.Sprintf("/skus?sku_ids=%d,%d&limit=1", first.ID, second.ID))
	if status != fiber.StatusOK {
		t.Fatalf("显式 id 取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got := pcqPayloadField(t, payload, "code"); !slices.Equal(got, []string{"PCQ-SKU-B"}) {
		t.Fatalf("显式 id 列表应优先且 limit 生效：%v", got)
	}

	status, payload = pcqDoGet(t, app, "/skus?limit=2")
	if status != fiber.StatusOK {
		t.Fatalf("limit 取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got := pcqPayloadField(t, payload, "code"); !slices.Equal(got, []string{"PCQ-SKU-C", "PCQ-SKU-B"}) {
		t.Fatalf("limit 未生效：%v", got)
	}

	// 客户端 limit 大于既有上限时，取更严的 100（既有硬上限），而不是放宽。
	status, payload = pcqDoGet(t, app, "/skus?limit=500")
	if status != fiber.StatusOK {
		t.Fatalf("大 limit 取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got := pcqPayloadField(t, payload, "code"); len(got) != 3 {
		t.Fatalf("limit 超过既有上限时不得放宽返回条数：%v", got)
	}
}

// filter（价格区间 / 分类 / 游戏分区）与 sort 必须真的落到查询上。
func TestPcqPublicSkusFilterAndSortProps(t *testing.T) {
	db := openGiftDB(t, "esports_pcq_sku_filter")
	defer db.Close()
	prev := global.ORM
	global.ORM = db
	defer func() { global.ORM = prev }()
	ctx := t.Context()

	lol := pcqSeedCategory(t, db, "PCQ-CAT-LOL", "英雄联盟")
	wz := pcqSeedCategory(t, db, "PCQ-CAT-WZ", "王者荣耀")
	pcqSeedSku(t, db, "PCQ-CHEAP", 1000, &lol.ID)
	mid := pcqSeedSku(t, db, "PCQ-MID", 2000, &lol.ID)
	pcqSeedSku(t, db, "PCQ-PRICEY", 5000, &wz.ID)
	parentID := 77
	db.EsportsSku.UpdateOneID(mid.ID).SetProductID(parentID).ExecX(ctx)

	app := fiber.New()
	app.Get("/skus", publicSkus)

	status, payload := pcqDoGet(t, app, "/skus?filter="+
		url.QueryEscape(`{"min_price_cents":1500,"max_price_cents":6000}`)+"&sort=price_asc")
	if status != fiber.StatusOK {
		t.Fatalf("价格区间 + 排序取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got := pcqPayloadField(t, payload, "code"); !slices.Equal(got, []string{"PCQ-MID", "PCQ-PRICEY"}) {
		t.Fatalf("价格区间 + 价格升序未生效：%v", got)
	}

	status, payload = pcqDoGet(t, app, "/skus?sort=price_desc")
	if status != fiber.StatusOK {
		t.Fatalf("价格倒序取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got := pcqPayloadField(t, payload, "code"); !slices.Equal(got, []string{"PCQ-PRICEY", "PCQ-MID", "PCQ-CHEAP"}) {
		t.Fatalf("价格倒序未生效：%v", got)
	}

	status, payload = pcqDoGet(t, app, "/skus?filter="+url.QueryEscape(`{"game":"王者荣耀"}`))
	if status != fiber.StatusOK {
		t.Fatalf("按游戏分区取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got := pcqPayloadField(t, payload, "code"); !slices.Equal(got, []string{"PCQ-PRICEY"}) {
		t.Fatalf("按游戏分区（经分类）过滤未生效：%v", got)
	}

	status, payload = pcqDoGet(t, app, fmt.Sprintf("/skus?product_ids=%d", parentID))
	if status != fiber.StatusOK {
		t.Fatalf("按父商品取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got := pcqPayloadField(t, payload, "code"); !slices.Equal(got, []string{"PCQ-MID"}) {
		t.Fatalf("按父商品过滤未生效：%v", got)
	}
}

// 「按规则自动取数」（§16.3-11）：data_source / rule_code / rules 落到既有字段；
// 无数据关系的规则与数据来源明确 400，而不是静默当全量。
func TestPcqPublicSkusRuleDataSource(t *testing.T) {
	db := openGiftDB(t, "esports_pcq_sku_rule")
	defer db.Close()
	prev := global.ORM
	global.ORM = db
	defer func() { global.ORM = prev }()
	ctx := t.Context()

	hot := pcqSeedSku(t, db, "PCQ-HOT", 3000, nil)
	pcqSeedSku(t, db, "PCQ-NEW", 1000, nil)
	db.EsportsSku.UpdateOneID(hot.ID).SetQuotaSold(5).ExecX(ctx)

	app := fiber.New()
	app.Get("/skus", publicSkus)

	status, payload := pcqDoGet(t, app, "/skus?data_source=rule&rule_code=hot")
	if status != fiber.StatusOK {
		t.Fatalf("按规则取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got := pcqPayloadField(t, payload, "code"); !slices.Equal(got, []string{"PCQ-HOT", "PCQ-NEW"}) {
		t.Fatalf("rule_code=hot 未按既有 quota_sold 倒序取数：%v", got)
	}

	status, payload = pcqDoGet(t, app, "/skus?rules="+
		url.QueryEscape(`{"data_source":"rule","rule_code":"hot","limit":1}`))
	if status != fiber.StatusOK {
		t.Fatalf("rules 字典取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got := pcqPayloadField(t, payload, "code"); !slices.Equal(got, []string{"PCQ-HOT"}) {
		t.Fatalf("rules 字典未生效：%v", got)
	}

	// 手工指定却未给任何 id：明确空结果，而不是回落成全量。
	status, payload = pcqDoGet(t, app, "/skus?data_source=manual")
	if status != fiber.StatusOK {
		t.Fatalf("手工指定取数失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	if got := pcqPayloadField(t, payload, "code"); len(got) != 0 {
		t.Fatalf("手工指定未给 id 时应为空：%v", got)
	}

	for _, path := range []string{
		"/skus?data_source=magic",
		"/skus?data_source=rule&rule_code=magic_rule",
		"/skus?data_source=rule&rule_code=discount",
		"/skus?data_source=campaign",
		"/skus?data_source=rule",
	} {
		if status, payload = pcqDoGet(t, app, path); status != fiber.StatusBadRequest {
			t.Fatalf("%s 应明确 400，实际 %d：%+v", path, status, payload)
		}
	}
}

// 商品列表白名单里的 campaign / activity 键在 SKU 侧没有任何数据关系：
// 必须明确 400，而不是静默忽略（否则等于「配了筛选但取数没变」的假成功）。
func TestPcqPublicSkusCampaignPropsRejectedExplicitly(t *testing.T) {
	db := openGiftDB(t, "esports_pcq_sku_campaign")
	defer db.Close()
	prev := global.ORM
	global.ORM = db
	defer func() { global.ORM = prev }()

	pcqSeedSku(t, db, "PCQ-ANY", 1000, nil)

	app := fiber.New()
	app.Get("/skus", publicSkus)

	for _, path := range []string{
		"/skus?campaign_id=1",
		"/skus?campaign_ids=1,2",
		"/skus?activity_id=3",
		"/skus?activity_ids=3,4",
		"/skus?filter=" + url.QueryEscape(`{"campaign_id":1}`),
		"/skus?filter=" + url.QueryEscape(`{"activity_ids":[3,4]}`),
		"/skus?filter=" + url.QueryEscape(`{"tag":"hot"}`),
	} {
		if status, payload := pcqDoGet(t, app, path); status != fiber.StatusBadRequest {
			t.Fatalf("%s 应明确 400，实际 %d：%+v", path, status, payload)
		}
	}
}

// ── §16.3-06 口径收紧：商品列表不再受理活动键 + 按组件类型的筛选键白名单 ──

// pcqPropsFor 构造「含必需客服入口」的最小组件集（本文件自用，避免依赖并行桶的辅助物）。
func pcqPropsFor(compType string, props map[string]interface{}) []pageComponent {
	visible := true
	return []pageComponent{
		{Type: "search", Visible: &visible},
		{Type: compType, Visible: &visible, Props: props},
		{Type: "support_entry", Visible: &visible},
	}
}

// 活动键已从 product_list 白名单移除：保存期即 400（不再「保存 200、取数才 400」）；
// activity 组件仍受理这些键（那里才有真实语义，不能被误删）。
func TestPcqProductListRejectsActivityPropsAtSave(t *testing.T) {
	db := openPageDB(t, "esports_pcq_activity_props")
	defer db.Close()
	channel := pageChannelCustomerHome
	ctx := t.Context()

	for i, props := range []map[string]interface{}{
		{"title": "在售服务", "campaign_id": 3},
		{"title": "在售服务", "campaign_ids": []interface{}{1, 2}},
		{"title": "在售服务", "activity_id": 3},
		{"title": "在售服务", "activity_ids": []interface{}{3, 4}},
	} {
		if _, err := savePageDraft(ctx, db, channel, 1, pageDraftInput{
			Components: pcqPropsFor("product_list", props), ExpectedVersion: i,
		}); !errors.Is(err, errInvalidInput) {
			t.Fatalf("product_list 配活动键 %v 应在保存期被拒，实际 %v", props, err)
		}
	}
	if _, err := savePageDraft(ctx, db, channel, 1, pageDraftInput{
		Components:      pcqPropsFor("activity", map[string]interface{}{"title": "活动专区", "campaign_id": 3}),
		ExpectedVersion: 0,
	}); err != nil {
		t.Fatalf("activity 组件仍应受理活动键：%v", err)
	}
}

// 筛选条件按组件类型收窄：各组件只能用自己真正能取数的维度（正反用例）。
func TestPcqPageComponentFilterKeysAreScoped(t *testing.T) {
	db := openPageDB(t, "esports_pcq_filter_scope")
	defer db.Close()
	channel := pageChannelCustomerHome
	ctx := t.Context()

	positive := []struct {
		comp  string
		props map[string]interface{}
	}{
		{"product_list", map[string]interface{}{"filter": map[string]interface{}{
			"game": "英雄联盟", "category_id": 2, "min_price_cents": 1000, "max_price_cents": 5000,
			"status": "on_sale", "sku_id": 1, "product_id": 2,
		}}},
		{"player_recommend", map[string]interface{}{"title": "推荐", "filter": map[string]interface{}{
			"game": "英雄联盟", "level": "senior", "online": true, "bookable": true,
		}}},
		{"activity", map[string]interface{}{"title": "活动", "filter": map[string]interface{}{
			"campaign_id": 3, "activity_id": 3,
		}}},
		{"product_list", map[string]interface{}{"data_source": "rule", "rules": map[string]interface{}{
			"rule_code": "hot", "limit": 5, "filters": map[string]interface{}{"game": "王者荣耀"},
		}}},
	}
	for i, tc := range positive {
		if _, err := savePageDraft(ctx, db, channel, 1, pageDraftInput{
			Components: pcqPropsFor(tc.comp, tc.props), ExpectedVersion: i,
		}); err != nil {
			t.Fatalf("%s 的合法筛选条件应被放行，实际 %v", tc.comp, err)
		}
	}

	negative := []struct {
		name  string
		comp  string
		props map[string]interface{}
	}{
		{"商品列表配技能等级", "product_list", map[string]interface{}{
			"filter": map[string]interface{}{"level": "senior"}}},
		{"商品列表配充值活动", "product_list", map[string]interface{}{
			"filter": map[string]interface{}{"campaign_id": 3}}},
		{"陪玩推荐配价格区间", "player_recommend", map[string]interface{}{
			"filter": map[string]interface{}{"min_price_cents": 1000}}},
		{"公告配游戏分区", "notice", map[string]interface{}{
			"text": "公告", "filter": map[string]interface{}{"game": "英雄联盟"}}},
		{"商品列表 rules 内配活动键", "product_list", map[string]interface{}{
			"rules": map[string]interface{}{"filters": map[string]interface{}{"campaign_id": 1}}}},
	}
	for i, tc := range negative {
		if _, err := savePageDraft(ctx, db, channel, 1, pageDraftInput{
			Components: pcqPropsFor(tc.comp, tc.props), ExpectedVersion: len(positive) + i,
		}); !errors.Is(err, errInvalidInput) {
			t.Fatalf("%s：应返回参数错误，实际 %v", tc.name, err)
		}
	}
}

// 读取路径必须对历史脏配置保持宽容（白名单只作用于保存 / 发布受理）：
// 已落库、含已移除活动键的 product_list 配置仍要能回显与下发，不能因为收紧而在读取时炸。
func TestPcqHistoricalPageConfigWithRemovedPropStillReadable(t *testing.T) {
	db := openPageDB(t, "esports_pcq_legacy_read")
	defer db.Close()
	prev := global.ORM
	global.ORM = db
	defer func() { global.ORM = prev }()
	ctx := t.Context()
	now := time.Now().UTC()

	camp := seedActiveCampaign(t, db, "PCQ-LEGACY-CAMP")
	visible := true
	legacy := pageConfigToMap(pageConfig{Components: []pageComponent{
		{Type: "search", Visible: &visible},
		{Type: "product_list", Visible: &visible, Props: map[string]interface{}{
			"title": "历史活动商品", "campaign_id": camp.ID, "limit": 5,
		}},
		{Type: "support_entry", Visible: &visible},
	}})
	doc := db.EsportsPageDocument.Create().SetChannel(pageChannelCustomerHome).
		SetDraftConfig(legacy).SetDraftVersion(3).SetPublishedVersionNo(1).
		SetStatus(esportspagedocument.StatusPublished).SetUpdatedBy(1).SaveX(ctx)
	db.EsportsPageVersion.Create().SetDocumentID(doc.ID).SetChannel(pageChannelCustomerHome).
		SetVersionNo(1).SetConfig(legacy).SetPublishedBy(1).
		SetStatus(esportspageversion.StatusPublished).SaveX(ctx)

	// ① 客户端读取（GET /page）：只做 JSON 反序列化，历史 props 原样下发。
	app := fiber.New()
	app.Get("/page", publicPage)
	status, payload := pcqDoGet(t, app, "/page?channel="+pageChannelCustomerHome)
	if status != fiber.StatusOK {
		t.Fatalf("历史配置下发失败：status=%d msg=%s", status, pcqMessage(payload))
	}
	data, _ := payload["data"].(map[string]any)
	cfg, _ := data["config"].(map[string]any)
	comps, _ := cfg["components"].([]any)
	if len(comps) != 3 {
		t.Fatalf("历史组件未原样下发：%+v", cfg)
	}
	row, _ := comps[1].(map[string]any)
	props, _ := row["props"].(map[string]any)
	if fmt.Sprint(props["campaign_id"]) != strconv.Itoa(camp.ID) {
		t.Fatalf("历史 props 未保留：%+v", props)
	}

	// ② 管理端草稿回显：同样只反序列化，不报错。
	admin := seedAdminAccount(t, db, "pcq-legacy-admin")
	adminApp := fiber.New()
	adminApp.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", true)
		return c.Next()
	})
	adminApp.Get("/admin/pages/:channel", adminPageDocument)
	if status, payload = pcqDoGet(t, adminApp, "/admin/pages/"+pageChannelCustomerHome); status != fiber.StatusOK {
		t.Fatalf("管理端草稿回显失败：status=%d msg=%s", status, pcqMessage(payload))
	}

	// ③ 「重新受理」类动作（发布 / 回滚）按新口径拒绝该历史配置：读取不下发不炸，
	//    但把含已移除键的配置再推上线必须明确失败，而不是把无效配置发出去。
	if _, err := publishPage(ctx, db, pageChannelCustomerHome, admin.ID,
		pagePublishInput{Mode: "immediate"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("含已移除活动键的历史草稿再发布应被拒，实际 %v", err)
	}
}

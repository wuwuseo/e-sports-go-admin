package esports

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersalerule"
	"go-admin/internal/ent/esportsaftersalerulehit"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsservicesegment"
)

// ar4SeedRule 建一条规则，成功即返回（失败直接 fail 测试）。
func ar4SeedRule(t *testing.T, db *ent.Client, in afterSaleRuleInput, now time.Time) *ent.EsportsAfterSaleRule {
	t.Helper()
	rule, err := createAfterSaleRule(t.Context(), db, 11, in, now)
	if err != nil {
		t.Fatalf("建规则失败：%v", err)
	}
	return rule
}

// ar4HitDedupKey 命中幂等键（与实现同口径，测试显式写出以锁定契约）。
func ar4HitDedupKey(ruleCode, sourceType string, sourceID int) string {
	return ruleCode + ":" + sourceType + ":" + strconv.Itoa(sourceID)
}

// ar4JSONInt 读取 JSON 落库后回读的数值（JSON 反序列化为 float64）。
func ar4JSONInt(payload map[string]interface{}, key string) int64 {
	value, _ := payload[key].(float64)
	return int64(value)
}

// P0：结单后金额异常须能被可配置规则扫描出来，并自动生成 channel=system 的售后单 + 命中留痕。
func TestAfterSaleRuleScanCreatesSystemAfterSaleOnClosedAmountAnomaly(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_rule_amount")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	closedAt := now.Add(-10 * time.Minute)
	// 应付 5000 分，但「数量×单价−折扣+调整」= 2000 分 → 偏离 15000 基点。
	order := ar4SeedOrder(t, db, "AMOUNT", 5000, 2000, 0, 0, 0, 1,
		esportsorder.FulfillmentStatusCompleted, &closedAt)
	rule := ar4SeedRule(t, db, afterSaleRuleInput{
		Code: "closed_amount_anomaly", Name: "结单后金额异常", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "amount_deviation_bp", ThresholdValue: 2000,
		WindowMinutes: 120, AfterSaleKind: "overcharge", Channel: "system",
		FaultParty: "admin", Priority: 10, Status: "active",
	}, now)

	hits, err := scanAfterSaleRules(ctx, db, now, 200)
	if err != nil {
		t.Fatalf("规则扫描报错：%v", err)
	}
	if hits != 1 {
		t.Fatalf("结单后金额异常应命中 1 条，实际 %d", hits)
	}
	items, err := db.EsportsAfterSale.Query().Where(
		esportsaftersale.SourceTypeEQ(esportsaftersale.SourceTypeOrder),
		esportsaftersale.SourceIDEQ(order.ID),
	).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("规则命中应自动建 1 张售后单，实际 %d 张", len(items))
	}
	created := items[0]
	if created.Channel != esportsaftersale.ChannelSystem {
		t.Fatalf("规则自动建单通道应为 system，实际 %q", created.Channel)
	}
	if created.Kind != esportsaftersale.KindOvercharge {
		t.Fatalf("规则命中生成的问题类型应为 overcharge，实际 %q", created.Kind)
	}
	if created.Status != esportsaftersale.StatusPendingAccept {
		t.Fatalf("自动建单应进入待受理，实际 %q", created.Status)
	}
	if created.FaultParty != esportsaftersale.FaultPartyAdmin {
		t.Fatalf("规则配置的默认责任方应写入售后单，实际 %q", created.FaultParty)
	}
	if created.CustomerID == nil || *created.CustomerID != order.CustomerID {
		t.Fatalf("自动建单应绑定来源订单客户：%+v", created.CustomerID)
	}
	if !strings.Contains(created.Demand, "结单后金额异常") {
		t.Fatalf("自动建单诉求应说明命中的规则与实测值，实际 %q", created.Demand)
	}

	hit, err := db.EsportsAfterSaleRuleHit.Query().Where(
		esportsaftersalerulehit.DedupKeyEQ(ar4HitDedupKey("closed_amount_anomaly", "order", order.ID)),
	).Only(ctx)
	if err != nil {
		t.Fatalf("命中留痕缺失：%v", err)
	}
	if hit.RuleID != rule.ID || hit.AfterSaleID != created.ID {
		t.Fatalf("命中留痕未回填规则 / 售后单：%+v", hit)
	}
	if hit.MetricValue != 15000 || hit.ThresholdValue != 2000 {
		t.Fatalf("命中留痕应保存实测值与阈值快照，实际 %d / %d", hit.MetricValue, hit.ThresholdValue)
	}

	// 幂等：同一订单同一规则只命中一次、只建一张单。
	again, err := scanAfterSaleRules(ctx, db, now, 200)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("重复扫描不应再次命中，实际 %d", again)
	}
	if n := db.EsportsAfterSale.Query().CountX(ctx); n != 1 {
		t.Fatalf("重复扫描后售后单应仍为 1 张，实际 %d", n)
	}
}

// ：结单后时长短缺按「承诺分钟 − 系统计时分钟」判定，观察窗口真实生效。
func TestAfterSaleRuleScanDurationShortageRespectsWindow(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_rule_duration")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	// 承诺 60 分钟，系统计时 30 分钟 → 短缺 30 分钟，且结单于窗口内。
	inWindowClosed := now.Add(-10 * time.Minute)
	short := ar4SeedOrder(t, db, "DUR-SHORT", 2000, 2000, 0, 0, 60, 1,
		esportsorder.FulfillmentStatusCompleted, &inWindowClosed)
	db.EsportsServiceSegment.Create().SetOrderID(short.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).SetStartedAt(now.Add(-time.Hour)).
		SetEndedAt(now.Add(-30 * time.Minute)).SetSystemSeconds(1800).SaveX(ctx)

	// 结单于观察窗口之外（200 分钟前）：不应被扫描。
	outWindowClosed := now.Add(-200 * time.Minute)
	stale := ar4SeedOrder(t, db, "DUR-STALE", 2000, 2000, 0, 0, 60, 1,
		esportsorder.FulfillmentStatusCompleted, &outWindowClosed)
	db.EsportsServiceSegment.Create().SetOrderID(stale.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).SetStartedAt(now.Add(-4 * time.Hour)).
		SetEndedAt(now.Add(-3 * time.Hour)).SetSystemSeconds(600).SaveX(ctx)

	ar4SeedRule(t, db, afterSaleRuleInput{
		Code: "closed_duration_short", Name: "结单后时长短缺", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "duration_shortage_minutes", ThresholdValue: 20,
		WindowMinutes: 120, AfterSaleKind: "duration_short", Channel: "system", Status: "active",
	}, now)

	hits, err := scanAfterSaleRules(ctx, db, now, 200)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("仅窗口内短缺 30 分钟的订单应命中，实际命中 %d", hits)
	}
	if n := db.EsportsAfterSale.Query().Where(esportsaftersale.SourceIDEQ(short.ID)).CountX(ctx); n != 1 {
		t.Fatalf("窗口内时长短缺订单应自动建单，实际 %d 张", n)
	}
	if n := db.EsportsAfterSale.Query().Where(esportsaftersale.SourceIDEQ(stale.ID)).CountX(ctx); n != 0 {
		t.Fatalf("观察窗口外的订单不应被扫描建单，实际 %d 张", n)
	}
}

// ：不可计算或未达阈值的来源不得误报建单，也不得留痕。
func TestAfterSaleRuleScanSkipsNotComputableMetrics(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_rule_no_metric")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	closedAt := now.Add(-5 * time.Minute)
	// 未配置承诺时长（service_duration_minutes=0），时长口径无法计算。
	noDuration := ar4SeedOrder(t, db, "NODUR", 2000, 2000, 0, 0, 0, 1,
		esportsorder.FulfillmentStatusCompleted, &closedAt)
	// 承诺 60 分钟、系统计时 55 分钟：短缺 5 分钟低于阈值 20。
	above := ar4SeedOrder(t, db, "ABOVE", 2000, 2000, 0, 0, 60, 1,
		esportsorder.FulfillmentStatusCompleted, &closedAt)
	db.EsportsServiceSegment.Create().SetOrderID(above.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).SetStartedAt(now.Add(-time.Hour)).
		SetEndedAt(now.Add(-5 * time.Minute)).SetSystemSeconds(3300).SaveX(ctx)

	ar4SeedRule(t, db, afterSaleRuleInput{
		Code: "closed_duration_strict", Name: "结单后时长短缺（严格）", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "duration_shortage_minutes", ThresholdValue: 20,
		WindowMinutes: 0, AfterSaleKind: "duration_short", Channel: "system", Status: "active",
	}, now)

	hits, err := scanAfterSaleRules(ctx, db, now, 200)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("无法计算或未达阈值的来源不得命中，实际命中 %d", hits)
	}
	if n := db.EsportsAfterSale.Query().CountX(ctx); n != 0 {
		t.Fatalf("不得误报建单，实际 %d 张", n)
	}
	for _, id := range []int{noDuration.ID, above.ID} {
		if n := db.EsportsAfterSaleRuleHit.Query().Where(
			esportsaftersalerulehit.SourceIDEQ(id)).CountX(ctx); n != 0 {
			t.Fatalf("未命中的来源不应留痕，订单 %d 有 %d 条留痕", id, n)
		}
	}
}

// ：命中但规则不自动建单（channel=admin / auto_create=false / 全局开关关闭）时，
// 只写留痕 + 客服待办，不产生 channel=system 售后单。
func TestAfterSaleRuleHitWithoutAutoCreateLeavesPendingTodo(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_rule_todo")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	closedAt := now.Add(-5 * time.Minute)
	order := ar4SeedOrder(t, db, "TODO", 9000, 2000, 0, 0, 0, 1,
		esportsorder.FulfillmentStatusCompleted, &closedAt)

	// 规则 1：通道 admin → 只推待办。
	ar4SeedRule(t, db, afterSaleRuleInput{
		Code: "closed_admin_todo", Name: "结单后大额待办", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "amount_over_cents", ThresholdValue: 5000,
		WindowMinutes: 60, AfterSaleKind: "overcharge", Channel: "admin", Status: "active",
	}, now)
	hits, err := scanAfterSaleRules(ctx, db, now, 200)
	if err != nil || hits != 1 {
		t.Fatalf("channel=admin 的规则应命中 1 条：%v %d", err, hits)
	}
	if n := db.EsportsAfterSale.Query().CountX(ctx); n != 0 {
		t.Fatalf("channel=admin 不得自动建单，实际 %d 张", n)
	}
	pending, err := listAfterSaleRuleHits(ctx, db, 0, "", true, 100)
	if err != nil || len(pending) != 1 {
		t.Fatalf("未建单的命中应作为客服待办可查：%v %d", err, len(pending))
	}
	if pending[0].AfterSaleID != 0 {
		t.Fatalf("待办命中的 after_sale_id 应为 0，实际 %d", pending[0].AfterSaleID)
	}

	// 规则 2：system 通道 + auto_create=false → 同样只留痕。
	noAuto := false
	ar4SeedRule(t, db, afterSaleRuleInput{
		Code: "closed_no_auto", Name: "结单后大额仅提示", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "amount_over_cents", ThresholdValue: 5000,
		WindowMinutes: 60, AfterSaleKind: "other", Channel: "system", AutoCreate: &noAuto, Status: "active",
	}, now)
	if _, err = scanAfterSaleRules(ctx, db, now, 200); err != nil {
		t.Fatal(err)
	}
	if n := db.EsportsAfterSale.Query().CountX(ctx); n != 0 {
		t.Fatalf("auto_create=false 不得自动建单，实际 %d 张", n)
	}
	if n := db.EsportsAfterSaleRuleHit.Query().CountX(ctx); n != 2 {
		t.Fatalf("两条规则各命中一次，应留痕 2 条，实际 %d", n)
	}

	// 规则 3：system 通道，但全局开关置 "0" → 明确不建单（仍留痕）。
	ar4SetConfig(t, db, "app.esports.aftersale_rule_auto_create_enabled", "0")
	ar4SeedRule(t, db, afterSaleRuleInput{
		Code: "closed_switch_off", Name: "结单后大额（开关关）", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "amount_over_cents", ThresholdValue: 5000,
		WindowMinutes: 60, AfterSaleKind: "other", Channel: "system", Status: "active",
	}, now)
	if _, err = scanAfterSaleRules(ctx, db, now, 200); err != nil {
		t.Fatal(err)
	}
	if n := db.EsportsAfterSale.Query().CountX(ctx); n != 0 {
		t.Fatalf("总开关关闭时 system 规则不得自动建单，实际 %d 张", n)
	}
	if n := db.EsportsAfterSaleRuleHit.Query().CountX(ctx); n != 3 {
		t.Fatalf("三条规则各命中一次，应留痕 3 条，实际 %d", n)
	}
	// 开关恢复为 1 后，已命中来源不会被补建单（留痕即幂等，不重复处理）。
	ar4SetConfig(t, db, "app.esports.aftersale_rule_auto_create_enabled", "1")
	if _, err = scanAfterSaleRules(ctx, db, now, 200); err != nil {
		t.Fatal(err)
	}
	if n := db.EsportsAfterSale.Query().CountX(ctx); n != 0 {
		t.Fatalf("已命中来源不得因开关恢复而补建单，实际 %d 张", n)
	}
	if n := db.EsportsAfterSaleRuleHit.Query().Where(
		esportsaftersalerulehit.SourceIDEQ(order.ID)).CountX(ctx); n != 3 {
		t.Fatalf("该订单应恰有 3 条命中留痕（每规则 1 条），实际 %d", n)
	}
}

// ：规则本身的可配置性——非法组合明确报错，编码唯一，版本乐观锁，未命中可删除、已命中不可删除。
func TestAfterSaleRuleCrudValidationAndDeleteGuard(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_rule_crud")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		in   afterSaleRuleInput
	}{
		{"编码含大写", afterSaleRuleInput{Code: "BadCode", Name: "非法编码", Status: "active"}},
		{"礼物来源配时长口径", afterSaleRuleInput{Code: "gift_dur", Name: "礼物时长", SourceType: "gift",
			TriggerEvent: "gift_paid", Metric: "duration_shortage_minutes", ThresholdValue: 10, Status: "active"}},
		{"人工触发未提供入口", afterSaleRuleInput{Code: "manual_rule", Name: "人工触发", SourceType: "order",
			TriggerEvent: "manual", Metric: "amount_over_cents", ThresholdValue: 100, Status: "active"}},
		{"不判定口径带阈值", afterSaleRuleInput{Code: "none_metric", Name: "不判定带阈值", SourceType: "order",
			TriggerEvent: "order_closed", Metric: "none", ThresholdValue: 5, Status: "active"}},
		{"基点阈值越界", afterSaleRuleInput{Code: "bp_over", Name: "基点越界", SourceType: "order",
			TriggerEvent: "order_closed", Metric: "amount_deviation_bp", ThresholdValue: 10001, Status: "active"}},
		{"问题类型与来源不匹配", afterSaleRuleInput{Code: "kind_mismatch", Name: "类型不匹配", SourceType: "gift",
			TriggerEvent: "gift_paid", Metric: "amount_over_cents", ThresholdValue: 100,
			AfterSaleKind: "player_appeal", Status: "active"}},
	}
	for _, item := range cases {
		if _, err := createAfterSaleRule(ctx, db, 11, item.in, now); !errors.Is(err, errInvalidInput) {
			t.Fatalf("%s 应被明确拒绝（errInvalidInput），实际 err=%v", item.name, err)
		}
	}
	if n := db.EsportsAfterSaleRule.Query().CountX(ctx); n != 0 {
		t.Fatalf("被拒的规则不应落库，实际 %d 条", n)
	}

	rule := ar4SeedRule(t, db, afterSaleRuleInput{
		Code: "closed_amount_anomaly", Name: "结单后金额异常", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "amount_deviation_bp", ThresholdValue: 2000,
		Status: "active",
	}, now)
	if _, err := createAfterSaleRule(ctx, db, 11, afterSaleRuleInput{
		Code: "closed_amount_anomaly", Name: "重复编码", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "amount_over_cents", ThresholdValue: 100, Status: "active",
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复编码应返回 errConflict，实际 err=%v", err)
	}
	// 乐观锁：版本号过期明确冲突。
	if _, err := updateAfterSaleRule(ctx, db, rule.ID, 11, afterSaleRuleInput{
		Code: "closed_amount_anomaly", Name: "改名", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "amount_deviation_bp", ThresholdValue: 2000,
		Status: "active", Version: rule.Version + 7,
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("版本号不一致应返回 errConflict，实际 err=%v", err)
	}
	updated, err := updateAfterSaleRule(ctx, db, rule.ID, 11, afterSaleRuleInput{
		Code: "closed_amount_anomaly", Name: "结单后金额异常（收紧）", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "amount_deviation_bp", ThresholdValue: 3000,
		Status: "active", Version: rule.Version,
	}, now)
	if err != nil {
		t.Fatalf("按当前版本更新应成功：%v", err)
	}
	if updated.Version != rule.Version+1 || updated.Name != "结单后金额异常（收紧）" {
		t.Fatalf("更新后版本应自增且名称生效：%+v", updated)
	}
	// 编码不可修改。
	if _, err := updateAfterSaleRule(ctx, db, rule.ID, 11, afterSaleRuleInput{
		Code: "another_code", Name: "改编码", SourceType: "order", TriggerEvent: "order_closed",
		Metric: "amount_deviation_bp", ThresholdValue: 3000, Status: "active",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("规则编码不可修改，应返回 errInvalidInput，实际 err=%v", err)
	}

	// 命中后不可删除：须改为停用；未命中的规则可删除。
	closedAt := now.Add(-time.Minute)
	order := ar4SeedOrder(t, db, "CRUD", 9000, 2000, 0, 0, 0, 1,
		esportsorder.FulfillmentStatusCompleted, &closedAt)
	if _, err = scanAfterSaleRules(ctx, db, now, 200); err != nil {
		t.Fatal(err)
	}
	if n := db.EsportsAfterSale.Query().Where(esportsaftersale.SourceIDEQ(order.ID)).CountX(ctx); n != 1 {
		t.Fatalf("命中后应自动建单，实际 %d 张", n)
	}
	if err = deleteAfterSaleRule(ctx, db, rule.ID, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("已命中的规则不可删除，应返回 errConflict，实际 err=%v", err)
	}
	disabled, err := updateAfterSaleRule(ctx, db, rule.ID, 11, afterSaleRuleInput{
		Code: "closed_amount_anomaly", Name: "结单后金额异常（收紧）", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "amount_deviation_bp", ThresholdValue: 3000,
		Status: "disabled", Version: updated.Version,
	}, now)
	if err != nil || disabled.Status != "disabled" {
		t.Fatalf("停用规则应成功：%v %+v", err, disabled)
	}
	if err = deleteAfterSaleRule(ctx, db, rule.ID, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("已命中规则即使停用也不可删除，实际 err=%v", err)
	}
	fresh := ar4SeedRule(t, db, afterSaleRuleInput{
		Code: "never_hit_rule", Name: "从未命中的规则", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "amount_over_cents", ThresholdValue: 999999,
		Status: "draft",
	}, now)
	if err = deleteAfterSaleRule(ctx, db, fresh.ID, 11, now); err != nil {
		t.Fatalf("未命中的规则应可删除：%v", err)
	}
	if n := db.EsportsAfterSaleRule.Query().Where(
		esportsaftersalerule.CodeEQ("never_hit_rule")).CountX(ctx); n != 0 {
		t.Fatalf("删除后规则不应存在，实际 %d 条", n)
	}
}

// ：规则配置变更须留审计痕迹（谁在何时改了什么）。
func TestAfterSaleRuleChangesAreAudited(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_rule_audit")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	rule := ar4SeedRule(t, db, afterSaleRuleInput{
		Code: "closed_amount_anomaly", Name: "结单后金额异常", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "amount_deviation_bp", ThresholdValue: 2000,
		Status: "active",
	}, now)
	if _, err := updateAfterSaleRule(ctx, db, rule.ID, 22, afterSaleRuleInput{
		Code: "closed_amount_anomaly", Name: "结单后金额异常", SourceType: "order",
		TriggerEvent: "order_closed", Metric: "amount_deviation_bp", ThresholdValue: 2500,
		Status: "active", Version: rule.Version,
	}, now); err != nil {
		t.Fatal(err)
	}
	rows, err := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("aftersale_rule.changed"),
	).Order(ent.Asc(esportsoutbox.FieldID)).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("创建与更新各应留 1 条审计痕迹，实际 %d 条", len(rows))
	}
	payload := rows[len(rows)-1].Payload
	if payload["action"] != "updated" || ar4JSONInt(payload, "admin_id") != 22 {
		t.Fatalf("审计痕迹应记录动作与操作人：%+v", payload)
	}
	before, _ := payload["before"].(map[string]interface{})
	after, _ := payload["after"].(map[string]interface{})
	if ar4JSONInt(before, "threshold_value") != 2000 || ar4JSONInt(after, "threshold_value") != 2500 {
		t.Fatalf("审计痕迹应含改前 / 改后阈值：%+v / %+v", before, after)
	}
}

// ：service_segment_finished 规则在服务段结束（订单尚未结单）时也能命中建单，
// 与 player.go 的硬编码严重时长差异分支按「同来源同问题只留一张进行中」并存。
func TestAfterSaleRuleScanServiceSegmentFinishedTrigger(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_rule_segment")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	// 订单仍在待结单：结单后触发器扫不到它，服务段结束触发器必须扫到。
	order := ar4SeedOrder(t, db, "SEG", 2000, 2000, 0, 0, 60, 1,
		esportsorder.FulfillmentStatusPendingClose, nil)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).SetStartedAt(now.Add(-time.Hour)).
		SetEndedAt(now.Add(-50 * time.Minute)).SetSystemSeconds(600).SaveX(ctx)

	ar4SeedRule(t, db, afterSaleRuleInput{
		Code: "segment_duration_short", Name: "服务段结束时长短缺", SourceType: "order",
		TriggerEvent: "service_segment_finished", Metric: "duration_shortage_minutes",
		ThresholdValue: 30, WindowMinutes: 120, AfterSaleKind: "duration_short",
		Channel: "system", Status: "active",
	}, now)
	hits, err := scanAfterSaleRules(ctx, db, now, 200)
	if err != nil || hits != 1 {
		t.Fatalf("服务段结束触发器应命中 1 条：%v %d", err, hits)
	}
	rows, err := db.EsportsAfterSale.Query().Where(esportsaftersale.SourceIDEQ(order.ID)).All(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("应自动建 1 张 channel=system 售后单：%v %d", err, len(rows))
	}
	if rows[0].Kind != esportsaftersale.KindDurationShort || rows[0].Channel != esportsaftersale.ChannelSystem {
		t.Fatalf("建单口径错误：%+v", rows[0])
	}
}

// ：gift_paid 规则可对已付款礼物单按金额超阈值命中建单。
func TestAfterSaleRuleScanGiftPaidTrigger(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_rule_gift")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	gift := db.EsportsGiftOrder.Create().SetGiftNo("ASR4-GIFT-1").SetGiftID(1).
		SetCustomerID(1).SetPlayerID(2).SetQuantity(1).SetUnitPriceCents(1000).
		SetAmountCents(5000).SetCreatedAt(now.Add(-10 * time.Minute)).SaveX(ctx)

	ar4SeedRule(t, db, afterSaleRuleInput{
		Code: "gift_high_value", Name: "礼物高额自动建单", SourceType: "gift",
		TriggerEvent: "gift_paid", Metric: "amount_over_cents", ThresholdValue: 3000,
		WindowMinutes: 60, AfterSaleKind: "refund_request", Channel: "system", Status: "active",
	}, now)
	hits, err := scanAfterSaleRules(ctx, db, now, 200)
	if err != nil || hits != 1 {
		t.Fatalf("礼物支付完成触发器应命中 1 条：%v %d", err, hits)
	}
	rows, err := db.EsportsAfterSale.Query().Where(
		esportsaftersale.SourceTypeEQ(esportsaftersale.SourceTypeGift),
		esportsaftersale.SourceIDEQ(gift.ID),
	).All(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("礼物来源应自动建 1 张售后单：%v %d", err, len(rows))
	}
	if rows[0].Channel != esportsaftersale.ChannelSystem || rows[0].PlayerID == nil || *rows[0].PlayerID != 2 {
		t.Fatalf("礼物来源自动建单应带收礼陪玩：%+v", rows[0])
	}
	if rows[0].CustomerID == nil || *rows[0].CustomerID != 1 {
		t.Fatalf("礼物来源自动建单应绑定送礼客户：%+v", rows[0].CustomerID)
	}
}

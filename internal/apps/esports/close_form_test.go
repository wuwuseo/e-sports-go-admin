package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportscloseformfield"
	"go-admin/internal/ent/esportscloseformtemplate"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/migrate"
)

func openCloseFormDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// 字段库：key 唯一且格式受限、单选必须配选项、选项去空去重。
func TestCloseFormFieldValidation(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_form_field")
	defer db.Close()
	ctx := t.Context()

	base := closeFormFieldInput{Key: "result", Label: "服务结果", FieldType: "text", Required: true}
	if _, err := createCloseFormField(ctx, db, base); err != nil {
		t.Fatalf("创建字段失败：%v", err)
	}
	if _, err := createCloseFormField(ctx, db, base); !errors.Is(err, errConflict) {
		t.Fatalf("重复 key 应冲突：%v", err)
	}
	badKey := base
	badKey.Key = "1bad"
	if _, err := createCloseFormField(ctx, db, badKey); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法 key 应被拒：%v", err)
	}
	if _, err := createCloseFormField(ctx, db,
		closeFormFieldInput{Key: "level", Label: "水平", FieldType: "select"}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("单选无选项应被拒：%v", err)
	}
	if _, err := createCloseFormField(ctx, db, closeFormFieldInput{
		Key: "level", Label: "水平", FieldType: "select", Options: []string{"高", " ", "高", "低"},
	}); err != nil {
		t.Fatalf("合法单选字段应创建成功：%v", err)
	}
	level, err := db.EsportsCloseFormField.Query().Where(esportscloseformfield.KeyEQ("level")).Only(ctx)
	if err != nil || len(level.Options) != 2 {
		t.Fatalf("选项应去空去重：%v %+v", err, level)
	}
}

// 模板：引用不存在的字段被拒、同 code 递增版本、生效互斥、回滚生成新的 active 版本。
func TestCloseFormTemplateLifecycle(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_form_template")
	defer db.Close()
	ctx := t.Context()

	for _, def := range []closeFormFieldInput{
		{Key: "result", Label: "服务结果", FieldType: "text", Required: true},
		{Key: "amount", Label: "订单金额", FieldType: "money"},
	} {
		if _, err := createCloseFormField(ctx, db, def); err != nil {
			t.Fatal(err)
		}
	}
	missing := closeFormTemplateInput{Code: "CF", Name: "默认表单", Scope: "global",
		Fields: []schema.CloseFormFieldBinding{{FieldKey: "not_exist"}}}
	if _, err := createCloseFormTemplate(ctx, db, missing, 11); !errors.Is(err, errInvalidInput) {
		t.Fatalf("引用不存在字段应被拒：%v", err)
	}
	empty := closeFormTemplateInput{Code: "CF", Name: "默认表单", Scope: "global"}
	if _, err := createCloseFormTemplate(ctx, db, empty, 11); !errors.Is(err, errInvalidInput) {
		t.Fatalf("空字段模板应被拒：%v", err)
	}

	in := closeFormTemplateInput{Code: "CF", Name: "默认表单", Scope: "global",
		Fields: []schema.CloseFormFieldBinding{
			{FieldKey: "result", Required: true, Sort: 1},
			{FieldKey: "amount", Sort: 2},
		}}
	first, err := createCloseFormTemplate(ctx, db, in, 11)
	if err != nil || first.Status != esportscloseformtemplate.StatusDraft || first.Version != 1 {
		t.Fatalf("创建模板失败：%v %+v", err, first)
	}
	second, err := createCloseFormTemplate(ctx, db, in, 11)
	if err != nil || second.Version != 2 {
		t.Fatalf("同 code 应递增版本：%v %+v", err, second)
	}
	if _, err = activateCloseFormTemplate(ctx, db, second.ID); err != nil {
		t.Fatalf("生效 v2 失败：%v", err)
	}
	if _, err = activateCloseFormTemplate(ctx, db, first.ID); err != nil {
		t.Fatalf("生效 v1 失败：%v", err)
	}
	if db.EsportsCloseFormTemplate.GetX(ctx, second.ID).Status != esportscloseformtemplate.StatusHistory {
		t.Fatal("旧 active 未转 history")
	}
	rolled, err := rollbackCloseFormTemplate(ctx, db, second.ID, 11)
	if err != nil || rolled.Version != 3 || rolled.Status != esportscloseformtemplate.StatusActive {
		t.Fatalf("回滚失败：%v %+v", err, rolled)
	}
	if db.EsportsCloseFormTemplate.GetX(ctx, first.ID).Status != esportscloseformtemplate.StatusHistory {
		t.Fatal("回滚后原 active 未转 history")
	}
}

// 结单表单：无适用模板时不受约束；有模板时缺必填项拒绝结单，填全后留痕（含金额只读自动值）。
func TestCloseOrderFormRequiredAndSnapshot(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_form_order")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	// 先验证「无模板」路径：直接结单应成功且不写快照。
	plain := seedSimpleCloseableOrder(t, db, "cf-order-0", 2000, 11)
	if _, err := closeOrder(ctx, db, plain.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("无模板结单应成功：%v", err)
	}
	if n := db.EsportsCloseFormSnapshot.Query().CountX(ctx); n != 0 {
		t.Fatalf("无模板不应写快照：%d", n)
	}

	for _, def := range []closeFormFieldInput{
		{Key: "result", Label: "服务结果", FieldType: "text", Required: true},
		{Key: "amount", Label: "订单金额", FieldType: "money"},
	} {
		if _, err := createCloseFormField(ctx, db, def); err != nil {
			t.Fatal(err)
		}
	}
	tpl, err := createCloseFormTemplate(ctx, db, closeFormTemplateInput{Code: "CF-GLOBAL", Name: "全局结单表单",
		Scope: "global", Fields: []schema.CloseFormFieldBinding{
			{FieldKey: "result", Required: true, Sort: 1},
			{FieldKey: "amount", Sort: 2},
		}}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCloseFormTemplate(ctx, db, tpl.ID); err != nil {
		t.Fatal(err)
	}

	order := seedSimpleCloseableOrder(t, db, "cf-order-1", 2000, 11)
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("缺必填项应被拒：%v", err)
	}
	if _, err := closeOrder(ctx, db, order.ID, 11,
		closeOrderInput{Note: "结单", FormValues: map[string]any{"result": "已完成"}}, now); err != nil {
		t.Fatalf("填全后结单应成功：%v", err)
	}
	snapshots := db.EsportsCloseFormSnapshot.Query().AllX(ctx)
	if len(snapshots) != 1 {
		t.Fatalf("快照应有一条：%d", len(snapshots))
	}
	snap := snapshots[0]
	if snap.OrderID != order.ID || snap.TemplateID != tpl.ID ||
		snap.TemplateCode != "CF-GLOBAL" || snap.TemplateVersion != tpl.Version {
		t.Fatalf("快照模板信息不正确：%+v", snap)
	}
	if snap.Values["result"] != "已完成" {
		t.Fatalf("填写的字段未留痕：%+v", snap.Values)
	}
	if amount, ok := snap.Values["amount"].(float64); !ok || amount != 2000 {
		t.Fatalf("金额只读字段应由系统写入订单应付：%+v", snap.Values["amount"])
	}
}

func TestOrderKeepsCloseFormTemplateFromPlacement(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_form_locked_at_order")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	for _, field := range []closeFormFieldInput{
		{Key: "old_result", Label: "旧版结果", FieldType: "text", Required: true},
		{Key: "new_result", Label: "新版结果", FieldType: "text", Required: true},
	} {
		if _, err := createCloseFormField(ctx, db, field); err != nil {
			t.Fatal(err)
		}
	}
	createAndActivate := func(fieldKey string) *ent.EsportsCloseFormTemplate {
		t.Helper()
		template, err := createCloseFormTemplate(ctx, db, closeFormTemplateInput{
			Code: "PLACEMENT-FORM", Name: "结单表单", Scope: "global",
			Fields: []schema.CloseFormFieldBinding{{FieldKey: fieldKey, Required: true}},
		}, 11)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := activateCloseFormTemplate(ctx, db, template.ID); err != nil {
			t.Fatal(err)
		}
		return template
	}
	first := createAndActivate("old_result")
	order := seedBalancePaidOrder(t, db, "close-form-locked")
	if order.CloseFormTemplateLockedAt == nil || order.CloseFormTemplateID == nil ||
		*order.CloseFormTemplateID != first.ID {
		t.Fatalf("下单时未锁定结单模板：%+v", order)
	}
	second := createAndActivate("new_result")
	if first.ID == second.ID {
		t.Fatal("新版模板应独立存储")
	}
	field := db.EsportsCloseFormField.Query().Where(esportscloseformfield.KeyEQ("old_result")).OnlyX(ctx)
	if _, err := updateCloseFormField(ctx, db, field.ID, field.Version, closeFormFieldInput{
		Key: "old_result", Label: "已改成数字", FieldType: "number", Required: true,
	}); err != nil {
		t.Fatal(err)
	}
	view, err := orderCloseForm(ctx, db, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Template == nil || view.Template.ID != first.ID {
		t.Fatalf("旧单不应切换到新模板：%+v", view.Template)
	}
	if len(view.Fields) != 1 || view.Fields[0].FieldType != esportscloseformfield.FieldTypeText {
		t.Fatalf("旧单字段类型不应受字段库修改影响：%+v", view.Fields)
	}
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).
		SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).
		SetSystemSeconds(600).SetEvidenceRef("ev.png").SaveX(ctx)
	if _, err := closeOrder(ctx, db, order.ID, 11,
		closeOrderInput{Note: "结单", FormValues: map[string]any{"new_result": "新版"}}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("旧单只填新版字段应拒绝：%v", err)
	}
	if _, err := closeOrder(ctx, db, order.ID, 11,
		closeOrderInput{Note: "结单", FormValues: map[string]any{"old_result": "旧版"}}, now); err != nil {
		t.Fatalf("旧单填原版字段应结单：%v", err)
	}
	snapshot := db.EsportsCloseFormSnapshot.Query().OnlyX(ctx)
	if snapshot.TemplateID != first.ID || snapshot.TemplateVersion != first.Version {
		t.Fatalf("结单记录应引用下单模板版本：%+v", snapshot)
	}
}

func TestOrderWithoutCloseFormDoesNotGainLaterTemplate(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_form_absent_at_order")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	order := seedBalancePaidOrder(t, db, "close-form-absent")
	if order.CloseFormTemplateLockedAt == nil || order.CloseFormTemplateID != nil {
		t.Fatalf("无模板也应锁定为空：%+v", order)
	}
	if _, err := createCloseFormField(ctx, db, closeFormFieldInput{
		Key: "later_result", Label: "后来新增", FieldType: "text", Required: true,
	}); err != nil {
		t.Fatal(err)
	}
	template, err := createCloseFormTemplate(ctx, db, closeFormTemplateInput{
		Code: "LATER-FORM", Name: "后发模板", Scope: "global",
		Fields: []schema.CloseFormFieldBinding{{FieldKey: "later_result", Required: true}},
	}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activateCloseFormTemplate(ctx, db, template.ID); err != nil {
		t.Fatal(err)
	}
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).
		SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).
		SetSystemSeconds(600).SetEvidenceRef("ev.png").SaveX(ctx)
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("下单时无模板的订单不应被后来模板阻止：%v", err)
	}
	if got := db.EsportsCloseFormSnapshot.Query().CountX(ctx); got != 0 {
		t.Fatalf("无模板订单不应产生结单表单快照：%d", got)
	}
}

// 金额区间：不命中的订单不受模板约束、不写快照。
func TestCloseFormTemplateAmountRange(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_form_amount")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	if _, err := createCloseFormField(ctx, db,
		closeFormFieldInput{Key: "result", Label: "服务结果", FieldType: "text", Required: true}); err != nil {
		t.Fatal(err)
	}
	tpl, err := createCloseFormTemplate(ctx, db, closeFormTemplateInput{Code: "CF-HIGH", Name: "高额单表单",
		Scope: "global", MinCents: 10000, MaxCents: 20000, Fields: []schema.CloseFormFieldBinding{
			{FieldKey: "result", Required: true},
		}}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCloseFormTemplate(ctx, db, tpl.ID); err != nil {
		t.Fatal(err)
	}

	// 应付 2000 分不在 [10000, 20000) 内 → 不受约束。
	low := seedSimpleCloseableOrder(t, db, "cf-order-low", 2000, 11)
	if _, err := closeOrder(ctx, db, low.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("区间外的订单不应受模板约束：%v", err)
	}
	// 应付 15000 分命中区间 → 缺必填项被拒。
	high := seedSimpleCloseableOrder(t, db, "cf-order-high", 15000, 11)
	if _, err := closeOrder(ctx, db, high.ID, 11, closeOrderInput{Note: "结单"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("区间内缺必填项应被拒：%v", err)
	}
	if _, err := closeOrder(ctx, db, high.ID, 11,
		closeOrderInput{Note: "结单", FormValues: map[string]any{"result": "ok"}}, now); err != nil {
		t.Fatalf("区间内填全应结单成功：%v", err)
	}
}

// 字段值类型校验：单选值必须在选项内、文本超长被拒。
func TestCloseFormValueTypeValidation(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_form_value")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	if _, err := createCloseFormField(ctx, db, closeFormFieldInput{
		Key: "level", Label: "水平", FieldType: "select", Required: true, Options: []string{"高", "低"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := createCloseFormField(ctx, db,
		closeFormFieldInput{Key: "remark", Label: "备注", FieldType: "text", MaxLength: 4}); err != nil {
		t.Fatal(err)
	}
	tpl, err := createCloseFormTemplate(ctx, db, closeFormTemplateInput{Code: "CF-TYPE", Name: "类型表单",
		Scope: "global", Fields: []schema.CloseFormFieldBinding{
			{FieldKey: "level", Required: true}, {FieldKey: "remark"},
		}}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCloseFormTemplate(ctx, db, tpl.ID); err != nil {
		t.Fatal(err)
	}

	order := seedSimpleCloseableOrder(t, db, "cf-order-type", 2000, 11)
	if _, err := closeOrder(ctx, db, order.ID, 11,
		closeOrderInput{Note: "结单", FormValues: map[string]any{"level": "中"}}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("选项外的值应被拒：%v", err)
	}
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单",
		FormValues: map[string]any{"level": "高", "remark": "超出四字了"}}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超长文本应被拒：%v", err)
	}
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单",
		FormValues: map[string]any{"level": "高", "remark": "合格"}}, now); err != nil {
		t.Fatalf("合法值应结单成功：%v", err)
	}
}

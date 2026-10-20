package esports

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// layoutTestComponent 构造一个合法布局；children 按列传入，nil 表示空列。
func layoutTestComponent(columns interface{}, children ...*pageComponent) pageComponent {
	visible := true
	return pageComponent{
		Type:     "layout",
		Visible:  &visible,
		Props:    map[string]interface{}{"columns": columns},
		Children: children,
	}
}

// 布局列数、子组件归属与嵌套限制：保存 / 发布期即拒绝非法结构。
func TestPageLayoutValidation(t *testing.T) {
	visible := true
	support := pageComponent{Type: "support_entry", Visible: &visible}
	notice := func(text string) *pageComponent {
		return &pageComponent{Type: "notice", Visible: &visible, Props: map[string]interface{}{"text": text}}
	}

	valid := pageConfig{Components: []pageComponent{
		layoutTestComponent(float64(2), notice("左"), notice("右")),
		support,
	}}
	if err := validatePageConfig(valid); err != nil {
		t.Fatalf("合法 1×2 布局应通过，实际：%v", err)
	}

	emptySlot := pageConfig{Components: []pageComponent{
		layoutTestComponent(2, notice("左"), nil),
		support,
	}}
	if err := validatePageConfig(emptySlot); err != nil {
		t.Fatalf("布局允许空列，实际：%v", err)
	}

	missingColumns := pageConfig{Components: []pageComponent{
		{Type: "layout", Visible: &visible, Children: []*pageComponent{notice("左")}},
		support,
	}}
	if err := validatePageConfig(missingColumns); err != nil {
		t.Fatalf("未配置列数应按 1 列处理，实际：%v", err)
	}

	for _, columns := range []interface{}{0, 4, 1.5, "2"} {
		cfg := pageConfig{Components: []pageComponent{
			layoutTestComponent(columns, notice("左")),
			support,
		}}
		if err := validatePageConfig(cfg); !errors.Is(err, errInvalidInput) {
			t.Fatalf("列数 %v 应被拒，实际：%v", columns, err)
		}
	}

	tooManyChildren := pageConfig{Components: []pageComponent{
		layoutTestComponent(1, notice("左"), notice("右")),
		support,
	}}
	if err := validatePageConfig(tooManyChildren); !errors.Is(err, errInvalidInput) {
		t.Fatalf("子组件数量超过列数应被拒，实际：%v", err)
	}

	innerLayout := layoutTestComponent(1, notice("内层"))
	nested := pageConfig{Components: []pageComponent{
		layoutTestComponent(1, &innerLayout),
		support,
	}}
	if err := validatePageConfig(nested); !errors.Is(err, errInvalidInput) {
		t.Fatalf("布局嵌套布局应被拒，实际：%v", err)
	}

	leafWithChildren := pageConfig{Components: []pageComponent{
		{Type: "notice", Visible: &visible, Children: []*pageComponent{notice("子")}},
		support,
	}}
	if err := validatePageConfig(leafWithChildren); !errors.Is(err, errInvalidInput) {
		t.Fatalf("普通组件携带 children 应被拒，实际：%v", err)
	}

	hidden := false
	supportChild := pageComponent{Type: "support_entry", Visible: &visible}
	hiddenLayoutSupport := pageConfig{Components: []pageComponent{
		layoutTestComponent(1, &supportChild),
	}}
	hiddenLayoutSupport.Components[0].Visible = &hidden
	if err := validatePageConfig(hiddenLayoutSupport); !errors.Is(err, errInvalidInput) {
		t.Fatalf("隐藏布局中的客服入口不应满足必需入口，实际：%v", err)
	}

	hiddenChildSupport := pageConfig{Components: []pageComponent{
		layoutTestComponent(1, &pageComponent{Type: "support_entry", Visible: &hidden}),
	}}
	if err := validatePageConfig(hiddenChildSupport); !errors.Is(err, errInvalidInput) {
		t.Fatalf("布局内隐藏的客服入口不应满足必需入口，实际：%v", err)
	}

	visibleChildSupport := pageConfig{Components: []pageComponent{
		layoutTestComponent(1, &pageComponent{Type: "support_entry", Visible: &visible}),
	}}
	if err := validatePageConfig(visibleChildSupport); err != nil {
		t.Fatalf("布局内可见客服入口应满足必需入口，实际：%v", err)
	}

	tooMany := pageConfig{Components: []pageComponent{
		layoutTestComponent(2, notice("左"), notice("右")),
		support,
	}}
	for i := 0; i < pageMaxComponents; i++ {
		tooMany.Components = append(tooMany.Components, *notice("批量"))
	}
	if err := validatePageConfig(tooMany); !errors.Is(err, errInvalidInput) {
		t.Fatalf("布局子组件也应计入数量上限，实际：%v", err)
	}
}

// 布局子组件同样执行有效期过滤：失效列保留 null 占位，全部失效时整行不下发。
func TestPageLayoutExpiredChildrenFiltered(t *testing.T) {
	visible := true
	now := time.Now()
	past := now.Add(-2 * time.Hour).Format(time.RFC3339)
	future := now.Add(2 * time.Hour).Format(time.RFC3339)
	child := func(text, validTo string) *pageComponent {
		return &pageComponent{Type: "notice", Visible: &visible, Props: map[string]interface{}{
			"text": text, "valid_to": validTo,
		}}
	}
	prepayNav := &pageComponent{Type: "quick_nav", Visible: &visible, Props: map[string]interface{}{
		"items": []interface{}{map[string]interface{}{"name": "储值卡", "target": "prepay_card"}},
	}}

	cfg := pageConfig{Components: []pageComponent{
		layoutTestComponent(2, child("过期", past), child("有效", future)),
		layoutTestComponent(1, prepayNav),
		{Type: "support_entry", Visible: &visible},
	}}
	out := normalizePageConfigForClient(cfg, now)
	if len(out.Components) != 2 {
		t.Fatalf("全列失效的布局应整行剔除，实际组件：%+v", out.Components)
	}
	layout := out.Components[0]
	if len(layout.Children) != 2 || layout.Children[0] != nil {
		t.Fatalf("过期子组件应保留 null 占位，实际：%+v", layout.Children)
	}
	if layout.Children[1] == nil || layout.Children[1].Props["text"] != "有效" {
		t.Fatalf("有效子组件应保留在原来的列，实际：%+v", layout.Children)
	}
	if !strings.Contains(out.Components[1].Type, "support_entry") {
		t.Fatalf("客服入口应保留，实际：%+v", out.Components)
	}
}

// 客户端能力声明需要递归覆盖布局及其子组件；隐藏组件不参与能力裁剪。
func TestPageLayoutCapabilityCompat(t *testing.T) {
	visible := true
	hidden := false
	cfg := pageConfig{Components: []pageComponent{
		layoutTestComponent(1, &pageComponent{Type: "notice", Visible: &visible}),
	}}
	if !pageConfigSupportsCaps(cfg, map[string]bool{"layout": true, "notice": true}) {
		t.Fatal("显式声明 layout + notice 的客户端应兼容")
	}
	if pageConfigSupportsCaps(cfg, map[string]bool{"layout": true}) {
		t.Fatal("子组件类型未在能力清单内时不应兼容")
	}
	cfg.Components[0].Visible = &hidden
	if !pageConfigSupportsCaps(cfg, map[string]bool{"layout": true}) {
		t.Fatal("隐藏布局的子组件不参与能力判断")
	}
}

// 布局内的图片素材也能反查到引用位置（路径带列号）。
func TestPageLayoutAssetUsage(t *testing.T) {
	visible := true
	cfg := pageConfig{Components: []pageComponent{
		layoutTestComponent(1, &pageComponent{Type: "notice", Visible: &visible, Props: map[string]interface{}{
			"icon": "https://cdn.example.com/layout-icon.png",
		}}),
	}}
	raw := pageConfigToMap(cfg)
	usages := pageConfigAssetUsages("uni_customer_home", 1, "published", raw, "https://cdn.example.com/layout-icon.png")
	if len(usages) != 1 {
		t.Fatalf("布局内图片应被反查到，实际：%+v", usages)
	}
	if usages[0].PropPath != "children[0].props.icon" {
		t.Fatalf("引用路径应包含列号，实际：%s", usages[0].PropPath)
	}
}

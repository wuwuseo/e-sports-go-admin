package apps

import (
	"testing"

	cmfapp "github.com/wuwuseo/cmf/app"
)

// 后台菜单：页面装修是「运营管理」下的一条菜单，直接承载装修编辑器组件。
// 原「客户首页」子菜单已合并进来，权限声明里不应再出现 esports:pages:home_menu。
func TestEsportsPageMenuMerged(t *testing.T) {
	var esports *cmfapp.App
	for _, app := range cmfapp.Registered() {
		if app.Code == "esports" {
			copy := app
			esports = &copy
			break
		}
	}
	if esports == nil {
		t.Fatal("esports 功能集未注册")
	}

	byCode := map[string]cmfapp.Permission{}
	for _, p := range esports.Permissions {
		byCode[p.Code] = p
	}

	operations, ok := byCode["esports:operations:menu"]
	if !ok {
		t.Fatal("缺少「运营管理」父菜单 esports:operations:menu")
	}
	if operations.Type != cmfapp.PermMenu || operations.Name != "运营管理" {
		t.Fatalf("运营管理菜单声明不正确: %+v", operations)
	}
	if operations.Parent != "" {
		t.Fatalf("运营管理应为顶层菜单，实际 Parent=%q", operations.Parent)
	}

	pageMenu, ok := byCode["esports:page:menu"]
	if !ok {
		t.Fatal("缺少「页面装修」菜单 esports:page:menu")
	}
	if pageMenu.Parent != operations.Code {
		t.Fatalf("页面装修的父菜单 = %q，期望 %q", pageMenu.Parent, operations.Code)
	}
	if pageMenu.Type != cmfapp.PermMenu || pageMenu.Name != "页面装修" {
		t.Fatalf("页面装修菜单声明不正确: %+v", pageMenu)
	}
	if pageMenu.MenuPath != "/esports/pages/home" || pageMenu.Component != "esports/pages/index" {
		t.Fatalf("页面装修应直接指向装修页，实际 %+v", pageMenu)
	}

	if _, ok := byCode["esports:pages:home_menu"]; ok {
		t.Fatal("「客户首页」已合并进「页面装修」，不应再声明 esports:pages:home_menu")
	}

	// 商品、充值活动、结单表单、礼物、钱包、陪玩、会员等级、收款管理与页面装修
	// 同属运营管理，不再作为顶层菜单。
	for _, code := range []string{
		"esports:product:menu",
		"esports:campaign:menu",
		"esports:close_form:menu",
		"esports:gift:menu",
		"esports:wallet:menu",
		"esports:player:menu",
		"esports:member:menu",
		"esports:payment:menu",
	} {
		menu, ok := byCode[code]
		if !ok {
			t.Fatalf("缺少菜单 %s", code)
		}
		if menu.Parent != operations.Code {
			t.Fatalf("菜单 %s 的父菜单 = %q，期望 %q", code, menu.Parent, operations.Code)
		}
	}
}

package apps

import (
	"testing"

	cmfapp "github.com/wuwuseo/cmf/app"
)

// 后台菜单层级（§16.2-01）：页面装修必须是「运营管理 → 页面装修 → 客户首页」三级菜单链，
// 而不是顶层菜单；客户首页承载原有装修编辑器组件，父级为导航目录（无组件）。
func TestEsportsPageMenuHierarchy(t *testing.T) {
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
	if pageMenu.Type != cmfapp.PermMenu || pageMenu.MenuPath == "" {
		t.Fatalf("页面装修菜单声明不正确: %+v", pageMenu)
	}

	home, ok := byCode["esports:pages:home_menu"]
	if !ok {
		t.Fatal("缺少「客户首页」子菜单 esports:pages:home_menu")
	}
	if home.Type != cmfapp.PermMenu || home.Name != "客户首页" {
		t.Fatalf("客户首页菜单声明不正确: %+v", home)
	}
	if home.Parent != pageMenu.Code {
		t.Fatalf("客户首页的父菜单 = %q，期望 %q", home.Parent, pageMenu.Code)
	}
	if home.Component != "esports/pages/index" {
		t.Fatalf("客户首页应承载既有装修组件 esports/pages/index，实际 %q", home.Component)
	}
	if home.MenuPath == pageMenu.MenuPath {
		t.Fatalf("父子菜单路径不得相同：%q", home.MenuPath)
	}
}

package demo

import cmfapp "github.com/wuwuseo/cmf/app"

func init() {
	cmfapp.Register(cmfapp.App{
		Code: "demo", Name: "示例功能集", Version: "1.0.0", Enabled: true,
		Config: []cmfapp.ConfigItem{{Key: "greeting", Name: "示例问候语", Value: "Hello", Type: "string", Group: "demo", Remark: "示例功能集配置"}},
		Routes: []cmfapp.Route{
			{Method: "GET", Path: "/ping", Auth: cmfapp.AuthPublic, Handler: ping},
			{Method: "GET", Path: "/me", Auth: cmfapp.AuthUser, Handler: me},
			{Method: "GET", Path: "/items", Auth: cmfapp.AuthAdmin, Handler: listItems},
		},
		Permissions: []cmfapp.Permission{
			{Type: cmfapp.PermMenu, Name: "示例管理", Code: "demo:menu", MenuPath: "/demo", Component: "demo/index", SortOrder: 90},
			{Type: cmfapp.PermAPI, Name: "查询示例", Code: "demo:items:list", Path: "/api/v1/demo/items", Method: "GET", Parent: "demo:menu"},
			{Type: cmfapp.PermButton, Name: "示例新增按钮", Code: "demo:items:add", Parent: "demo:menu"},
		},
	})
}

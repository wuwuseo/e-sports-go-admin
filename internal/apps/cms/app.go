package cms

import cmfapp "github.com/wuwuseo/cmf/app"

func init() {
	cmfapp.Register(cmfapp.App{
		Code: "cms", Name: "内容管理", Version: "1.0.0", Enabled: true,
		Routes: []cmfapp.Route{
			{Method: "GET", Path: "/articles", Auth: cmfapp.AuthPublic, Handler: publicArticles},
			{Method: "GET", Path: "/articles/:id", Auth: cmfapp.AuthPublic, Handler: publicArticle},
			{Method: "GET", Path: "/pages", Auth: cmfapp.AuthPublic, Handler: publicPages},
			{Method: "GET", Path: "/pages/:id", Auth: cmfapp.AuthPublic, Handler: publicPage},
			{Method: "GET", Path: "/categories", Auth: cmfapp.AuthPublic, Handler: publicCategories},
			{Method: "GET", Path: "/admin/contents", Auth: cmfapp.AuthAdmin, Handler: adminContents},
			{Method: "GET", Path: "/admin/contents/:id", Auth: cmfapp.AuthAdmin, Handler: adminContent},
			{Method: "POST", Path: "/admin/contents", Auth: cmfapp.AuthAdmin, Handler: createContent},
			{Method: "PUT", Path: "/admin/contents/:id", Auth: cmfapp.AuthAdmin, Handler: updateContent},
			{Method: "DELETE", Path: "/admin/contents/:id", Auth: cmfapp.AuthAdmin, Handler: deleteContent},
			{Method: "GET", Path: "/admin/categories", Auth: cmfapp.AuthAdmin, Handler: adminCategories},
			{Method: "POST", Path: "/admin/categories", Auth: cmfapp.AuthAdmin, Handler: createCategory},
			{Method: "PUT", Path: "/admin/categories/:id", Auth: cmfapp.AuthAdmin, Handler: updateCategory},
			{Method: "DELETE", Path: "/admin/categories/:id", Auth: cmfapp.AuthAdmin, Handler: deleteCategory},
		},
		Permissions: []cmfapp.Permission{
			{Type: cmfapp.PermMenu, Name: "内容管理", Code: "cms:menu", MenuPath: "/cms", Component: "cms/index", Icon: "icon-file", SortOrder: 90},
			{Type: cmfapp.PermAPI, Name: "查看内容列表", Code: "cms:contents:list", Path: "/api/v1/cms/admin/contents", Method: "GET", Parent: "cms:menu"},
			{Type: cmfapp.PermAPI, Name: "查看内容详情", Code: "cms:contents:get", Path: "/api/v1/cms/admin/contents/{id}", Method: "GET", Parent: "cms:menu"},
			{Type: cmfapp.PermAPI, Name: "新增内容", Code: "cms:contents:create", Path: "/api/v1/cms/admin/contents", Method: "POST", Parent: "cms:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑内容", Code: "cms:contents:update", Path: "/api/v1/cms/admin/contents/{id}", Method: "PUT", Parent: "cms:menu"},
			{Type: cmfapp.PermAPI, Name: "删除内容", Code: "cms:contents:delete", Path: "/api/v1/cms/admin/contents/{id}", Method: "DELETE", Parent: "cms:menu"},
			{Type: cmfapp.PermAPI, Name: "查看分类", Code: "cms:categories:list", Path: "/api/v1/cms/admin/categories", Method: "GET", Parent: "cms:menu"},
			{Type: cmfapp.PermAPI, Name: "新增分类", Code: "cms:categories:create", Path: "/api/v1/cms/admin/categories", Method: "POST", Parent: "cms:menu"},
			{Type: cmfapp.PermAPI, Name: "编辑分类", Code: "cms:categories:update", Path: "/api/v1/cms/admin/categories/{id}", Method: "PUT", Parent: "cms:menu"},
			{Type: cmfapp.PermAPI, Name: "删除分类", Code: "cms:categories:delete", Path: "/api/v1/cms/admin/categories/{id}", Method: "DELETE", Parent: "cms:menu"},
			{Type: cmfapp.PermButton, Name: "新增内容按钮", Code: "cms:contents:add", Parent: "cms:menu"},
			{Type: cmfapp.PermButton, Name: "编辑内容按钮", Code: "cms:contents:edit", Parent: "cms:menu"},
			{Type: cmfapp.PermButton, Name: "删除内容按钮", Code: "cms:contents:remove", Parent: "cms:menu"},
			{Type: cmfapp.PermButton, Name: "管理分类按钮", Code: "cms:categories:manage", Parent: "cms:menu"},
		},
	})
}

package cms

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"
)

func TestContentValidationAndPublication(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:cms_content_test?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	ctx := t.Context()
	category := client.CmsCategory.Create().SetName("新闻").SaveX(ctx)
	input := contentInput{
		Title: " Article ", Kind: "article", Format: "html",
		Content:    `<p>Hello</p><script>alert(1)</script><a href="javascript:alert(1)">bad link</a>`,
		CategoryID: category.ID, Status: 0,
	}
	item, err := saveContent(ctx, client, 0, input)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Article" || strings.Contains(item.Content, "<script") || strings.Contains(item.Content, "javascript:") {
		t.Fatalf("content was not normalized safely: %+v", item)
	}
	list, err := listContents(ctx, client, "article", true, 0, 1, 10, "")
	if err != nil || list.Total != 0 {
		t.Fatalf("draft leaked publicly: %+v, %v", list, err)
	}
	input.Status = 1
	if _, err := saveContent(ctx, client, item.ID, input); err != nil {
		t.Fatal(err)
	}
	list, err = listContents(ctx, client, "article", true, category.ID, 1, 10, "Article")
	if err != nil || list.Total != 1 || list.List[0].ID != item.ID {
		t.Fatalf("published article missing: %+v, %v", list, err)
	}
	category.Update().SetStatus(0).SaveX(ctx)
	list, err = listContents(ctx, client, "article", true, 0, 1, 10, "")
	if err != nil || list.Total != 0 {
		t.Fatalf("disabled category leaked publicly: %+v, %v", list, err)
	}
	if _, err := saveContent(ctx, client, 0, contentInput{Title: "Unsafe image", Kind: "page", Format: "image", Content: "javascript:alert(1)"}); err == nil {
		t.Fatal("unsafe image URL was accepted")
	}
	if _, err := saveContent(ctx, client, 0, contentInput{Title: "Orphan", Kind: "article", Format: "markdown", Content: "text", CategoryID: 999}); err == nil {
		t.Fatal("missing category was accepted")
	}
}

func TestPublicAPIHidesDrafts(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:cms_public_test?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	previous := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = previous })
	ctx := t.Context()
	draft := client.CmsContent.Create().SetTitle("Draft").SetContent("hidden").SetStatus(0).SaveX(ctx)
	client.CmsContent.Create().SetTitle("Live").SetContent("visible").SetStatus(1).SaveX(ctx)
	app := fiber.New()
	app.Get("/articles", publicArticles)
	app.Get("/articles/:id", publicArticle)
	request := func(path string, want int) *http.Response {
		t.Helper()
		response, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("GET %s returned %d, want %d", path, response.StatusCode, want)
		}
		return response
	}
	response := request("/articles", http.StatusOK)
	var body struct {
		Code int `json:"code"`
		Data struct {
			Total int `json:"total"`
			List  []struct {
				Title string `json:"title"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if body.Code != 1 || body.Data.Total != 1 || len(body.Data.List) != 1 || body.Data.List[0].Title != "Live" {
		t.Fatalf("public article response leaked a draft: %+v", body)
	}
	request("/articles/"+strconv.Itoa(draft.ID), http.StatusNotFound).Body.Close()
}

// 对齐只能以 style="text-align: ..." 表达（浏览器 justify* 命令没有标签式替代），
// 因此策略放行了这一条样式；该测试锁定"只放行单个 text-align 声明"这一边界。
func TestRichTextPolicyAllowsOnlyTextAlign(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantHas []string
		wantNot []string
	}{
		{
			name:    "保留段落居中",
			input:   `<p style="text-align: center">居中</p>`,
			wantHas: []string{`style="text-align: center"`},
		},
		{
			name:    "保留带分号的右对齐",
			input:   `<div style="text-align:right;">右</div>`,
			wantHas: []string{"text-align:right"},
		},
		{
			name:    "保留标题左对齐",
			input:   `<h2 style="text-align: left">标题</h2>`,
			wantHas: []string{`style="text-align: left"`},
		},
		{
			name:    "保留两端对齐",
			input:   `<p style="text-align: justify">两端</p>`,
			wantHas: []string{"text-align: justify"},
		},
		{
			name:    "丢弃非对齐样式",
			input:   `<p style="color: red">红</p>`,
			wantNot: []string{"color", "style="},
		},
		{
			name:    "丢弃夹带其他声明的对齐",
			input:   `<p style="text-align: center; color: red">夹带</p>`,
			wantNot: []string{"color", "text-align"},
		},
		{
			name:    "丢弃夹带 url 的对齐",
			input:   `<p style="text-align:center;background:url(javascript:alert(1))">x</p>`,
			wantNot: []string{"background", "text-align", "javascript"},
		},
		{
			name:    "丢弃定位样式",
			input:   `<div style="position: fixed; top: 0">覆盖</div>`,
			wantNot: []string{"position", "style="},
		},
		{
			name:    "放行对齐的同时仍丢弃 class",
			input:   `<p class="evil" style="text-align: center">x</p>`,
			wantHas: []string{`style="text-align: center"`},
			wantNot: []string{"class"},
		},
		{
			name:    "表格单元格对齐保留",
			input:   `<table><tbody><tr><td align="center">c</td></tr></tbody></table>`,
			wantHas: []string{`align="center"`},
		},
		{
			name:    "对齐放行不影响脚本与事件属性清除",
			input:   `<p onclick="alert(1)" style="text-align: center">x<script>alert(2)</script></p>`,
			wantHas: []string{`style="text-align: center"`},
			wantNot: []string{"onclick", "<script"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := richTextPolicy.Sanitize(tc.input)
			for _, want := range tc.wantHas {
				if !strings.Contains(got, want) {
					t.Fatalf("清洗结果 %q 缺少 %q", got, want)
				}
			}
			for _, not := range tc.wantNot {
				if strings.Contains(got, not) {
					t.Fatalf("清洗结果 %q 意外包含 %q", got, not)
				}
			}
		})
	}
}

// 纯图片内容的 content 是逗号分隔的图片地址列表，数量不限。
func TestImageFormatStoresImageList(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:cms_image_test?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	ctx := t.Context()

	multi, err := saveContent(ctx, client, 0, contentInput{
		Title: "Gallery", Kind: "page", Format: "image", Status: 1,
		Content: " /uploads/cms/a.png , /uploads/cms/b.png ,/uploads/cms/c.png ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if multi.Content != "/uploads/cms/a.png,/uploads/cms/b.png,/uploads/cms/c.png" {
		t.Fatalf("地址列表未被规范化：%q", multi.Content)
	}

	// 更新后按新列表保存
	updated, err := saveContent(ctx, client, multi.ID, contentInput{
		Title: "Gallery", Kind: "page", Format: "image", Status: 1,
		Content: "/uploads/cms/z.png,/uploads/cms/y.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Content != "/uploads/cms/z.png,/uploads/cms/y.png" {
		t.Fatalf("更新后列表不符：%q", updated.Content)
	}

	// 空列表、纯分隔符与非法地址都要拒绝
	for _, bad := range []string{"", " , , ", "/uploads/cms/a.png,javascript:alert(1)", "<p>只有文字</p>"} {
		if _, err := saveContent(ctx, client, 0, contentInput{
			Title: "Bad", Kind: "page", Format: "image", Status: 1, Content: bad,
		}); err == nil {
			t.Fatalf("非法图片内容被接受了：%q", bad)
		}
	}

	// http/https 绝对地址同样可用
	remote, err := saveContent(ctx, client, 0, contentInput{
		Title: "Remote", Kind: "page", Format: "image", Status: 1,
		Content: "https://example.com/a.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	if remote.Content != "https://example.com/a.png" {
		t.Fatalf("绝对地址未被保留：%q", remote.Content)
	}
}

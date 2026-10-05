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
	if _, err := saveContent(ctx, client, 0, contentInput{Title: "Unsafe image", Kind: "page", Format: "image", Image: "javascript:alert(1)"}); err == nil {
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

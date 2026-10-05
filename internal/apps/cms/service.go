package cms

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/microcosm-cc/bluemonday"

	"go-admin/internal/ent"
	"go-admin/internal/ent/cmscategory"
	"go-admin/internal/ent/cmscontent"
)

var errInvalidInput = errors.New("invalid CMS input")
var richTextPolicy = bluemonday.UGCPolicy()

type contentInput struct {
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	Format     string `json:"format"`
	Content    string `json:"content"`
	Image      string `json:"image"`
	CategoryID int    `json:"category_id"`
	Status     int    `json:"status"`
}

type categoryInput struct {
	Name   string `json:"name"`
	Status int    `json:"status"`
}

type pageResult[T any] struct {
	List     []T `json:"list"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

func validImageURL(value string) bool {
	if strings.HasPrefix(value, "/uploads/") && !strings.ContainsAny(value, "?#\\") {
		return true
	}
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

func normalizeContent(ctx context.Context, db *ent.Client, in *contentInput) error {
	in.Title = strings.TrimSpace(in.Title)
	in.Image = strings.TrimSpace(in.Image)
	if len([]rune(in.Title)) == 0 || len([]rune(in.Title)) > 200 ||
		(in.Kind != "article" && in.Kind != "page") ||
		(in.Format != "markdown" && in.Format != "html" && in.Format != "image") ||
		(in.Status != 0 && in.Status != 1) || in.CategoryID < 0 || len(in.Image) > 500 {
		return fmt.Errorf("%w: 标题、类型、格式或状态无效", errInvalidInput)
	}
	if in.Kind == "page" {
		in.CategoryID = 0
	} else if in.CategoryID > 0 {
		exists, err := db.CmsCategory.Query().Where(cmscategory.IDEQ(in.CategoryID)).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: 分类不存在", errInvalidInput)
		}
	}
	if in.Image != "" && !validImageURL(in.Image) {
		return fmt.Errorf("%w: 图片地址必须是 HTTPS、HTTP 或 /uploads/ 路径", errInvalidInput)
	}
	switch in.Format {
	case "image":
		if in.Image == "" {
			return fmt.Errorf("%w: 纯图片内容必须指定图片", errInvalidInput)
		}
		in.Content = ""
	case "html":
		in.Content = richTextPolicy.Sanitize(in.Content)
		if strings.TrimSpace(in.Content) == "" {
			return fmt.Errorf("%w: 内容不能为空", errInvalidInput)
		}
	case "markdown":
		if strings.TrimSpace(in.Content) == "" {
			return fmt.Errorf("%w: 内容不能为空", errInvalidInput)
		}
	}
	return nil
}

func listContents(ctx context.Context, db *ent.Client, kind string, published bool, categoryID, page, pageSize int, keyword string) (pageResult[*ent.CmsContent], error) {
	q := db.CmsContent.Query().Where(cmscontent.KindEQ(cmscontent.Kind(kind)))
	if published {
		q.Where(cmscontent.StatusEQ(1))
		if kind == "article" {
			activeIDs, err := db.CmsCategory.Query().Where(cmscategory.StatusEQ(1)).IDs(ctx)
			if err != nil {
				return pageResult[*ent.CmsContent]{}, err
			}
			q.Where(cmscontent.Or(cmscontent.CategoryIDEQ(0), cmscontent.CategoryIDIn(activeIDs...)))
		}
	}
	if categoryID > 0 && kind == "article" {
		q.Where(cmscontent.CategoryIDEQ(categoryID))
	}
	if keyword != "" {
		q.Where(cmscontent.TitleContains(keyword))
	}
	count, err := q.Clone().Count(ctx)
	if err != nil {
		return pageResult[*ent.CmsContent]{}, err
	}
	rows, err := q.Order(ent.Desc(cmscontent.FieldCreatedAt), ent.Desc(cmscontent.FieldID)).Offset((page - 1) * pageSize).Limit(pageSize).All(ctx)
	if err != nil {
		return pageResult[*ent.CmsContent]{}, err
	}
	return pageResult[*ent.CmsContent]{List: rows, Total: count, Page: page, PageSize: pageSize}, nil
}

func saveContent(ctx context.Context, db *ent.Client, id int, in contentInput) (*ent.CmsContent, error) {
	if err := normalizeContent(ctx, db, &in); err != nil {
		return nil, err
	}
	if id == 0 {
		return db.CmsContent.Create().SetTitle(in.Title).SetKind(cmscontent.Kind(in.Kind)).SetFormat(cmscontent.Format(in.Format)).
			SetContent(in.Content).SetImage(in.Image).SetCategoryID(in.CategoryID).SetStatus(in.Status).Save(ctx)
	}
	return db.CmsContent.UpdateOneID(id).SetTitle(in.Title).SetKind(cmscontent.Kind(in.Kind)).SetFormat(cmscontent.Format(in.Format)).
		SetContent(in.Content).SetImage(in.Image).SetCategoryID(in.CategoryID).SetStatus(in.Status).Save(ctx)
}

func saveCategory(ctx context.Context, db *ent.Client, id int, in categoryInput) (*ent.CmsCategory, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 100 || (in.Status != 0 && in.Status != 1) {
		return nil, fmt.Errorf("%w: 分类名称或状态无效", errInvalidInput)
	}
	if id == 0 {
		return db.CmsCategory.Create().SetName(in.Name).SetStatus(in.Status).Save(ctx)
	}
	return db.CmsCategory.UpdateOneID(id).SetName(in.Name).SetStatus(in.Status).Save(ctx)
}

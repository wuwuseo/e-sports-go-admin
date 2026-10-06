package cms

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"

	"go-admin/internal/ent"
	"go-admin/internal/ent/cmscategory"
	"go-admin/internal/ent/cmscontent"
)

var errInvalidInput = errors.New("invalid CMS input")

// richTextAlignStyle 只放行单个 text-align 声明。浏览器对 justify* 命令一律产出
// style="text-align: ..."（没有标签式替代），因此正文对齐必须允许这一条样式；
// 整个 style 值必须完全匹配，其他任何 CSS 都会被丢弃。
var richTextAlignStyle = regexp.MustCompile(
	`(?i)^\s*text-align\s*:\s*(left|center|right|justify)\s*;?\s*$`,
)

func newRichTextPolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("style").Matching(richTextAlignStyle).
		OnElements("p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "li")
	return p
}

var richTextPolicy = newRichTextPolicy()

type contentInput struct {
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	Format     string `json:"format"`
	Content    string `json:"content"`
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

// 纯图片内容是以逗号分隔的图片地址列表，至少一张。逐条校验地址，
// 因此不按 HTML 处理，地址里的 & 也不会被转义成实体。
func normalizeImageList(content string) (string, error) {
	parts := strings.Split(content, ",")
	list := make([]string, 0, len(parts))
	for _, part := range parts {
		src := strings.TrimSpace(part)
		if src == "" {
			continue
		}
		if !validImageURL(src) {
			return "", fmt.Errorf("%w: 图片地址必须是 HTTPS、HTTP 或 /uploads/ 路径", errInvalidInput)
		}
		list = append(list, src)
	}
	if len(list) == 0 {
		return "", fmt.Errorf("%w: 纯图片内容必须指定图片", errInvalidInput)
	}
	return strings.Join(list, ","), nil
}

func normalizeContent(ctx context.Context, db *ent.Client, in *contentInput) error {
	in.Title = strings.TrimSpace(in.Title)
	if len([]rune(in.Title)) == 0 || len([]rune(in.Title)) > 200 ||
		(in.Kind != "article" && in.Kind != "page") ||
		(in.Format != "markdown" && in.Format != "html" && in.Format != "image") ||
		(in.Status != 0 && in.Status != 1) || in.CategoryID < 0 {
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
	switch in.Format {
	case "image":
		list, err := normalizeImageList(in.Content)
		if err != nil {
			return err
		}
		in.Content = list
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
			SetContent(in.Content).SetCategoryID(in.CategoryID).SetStatus(in.Status).Save(ctx)
	}
	return db.CmsContent.UpdateOneID(id).SetTitle(in.Title).SetKind(cmscontent.Kind(in.Kind)).SetFormat(cmscontent.Format(in.Format)).
		SetContent(in.Content).SetCategoryID(in.CategoryID).SetStatus(in.Status).Save(ctx)
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

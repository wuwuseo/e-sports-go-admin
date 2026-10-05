package cms

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/cmscategory"
	"go-admin/internal/ent/cmscontent"
	"go-admin/internal/global"
)

func success(c fiber.Ctx, data any) error {
	return c.JSON(fiber.Map{"code": 1, "message": "成功", "data": data})
}

func failure(c fiber.Ctx, status int, message string) error {
	return c.Status(status).JSON(fiber.Map{"code": status, "message": message})
}

func databaseFailure(c fiber.Ctx, err error) error {
	if ent.IsNotFound(err) {
		return failure(c, fiber.StatusNotFound, "记录不存在")
	}
	if errors.Is(err, errInvalidInput) || ent.IsValidationError(err) {
		return failure(c, fiber.StatusBadRequest, err.Error())
	}
	return failure(c, fiber.StatusInternalServerError, "内容管理操作失败")
}

func pathID(c fiber.Ctx) (int, error) {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return 0, errors.New("无效的 ID")
	}
	return id, nil
}

func queryInt(c fiber.Ctx, key string, fallback int) (int, error) {
	raw := c.Query(key)
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}

func list(c fiber.Ctx, kind string, published bool) error {
	page, err := queryInt(c, "page", 1)
	if err != nil || page < 1 {
		return failure(c, fiber.StatusBadRequest, "页码无效")
	}
	pageSize, err := queryInt(c, "page_size", 10)
	if err != nil || pageSize < 1 || pageSize > 100 {
		return failure(c, fiber.StatusBadRequest, "每页数量必须在 1 到 100 之间")
	}
	categoryID, err := queryInt(c, "category_id", 0)
	if err != nil || categoryID < 0 {
		return failure(c, fiber.StatusBadRequest, "分类 ID 无效")
	}
	if published && categoryID > 0 {
		active, err := global.ORM.CmsCategory.Query().Where(cmscategory.IDEQ(categoryID), cmscategory.StatusEQ(1)).Exist(c.Context())
		if err != nil {
			return databaseFailure(c, err)
		}
		if !active {
			return failure(c, fiber.StatusNotFound, "分类不存在")
		}
	}
	result, err := listContents(c.Context(), global.ORM, kind, published, categoryID, page, pageSize, strings.TrimSpace(c.Query("keyword")))
	if err != nil {
		return databaseFailure(c, err)
	}
	return success(c, result)
}

func publicArticles(c fiber.Ctx) error { return list(c, "article", true) }
func publicPages(c fiber.Ctx) error    { return list(c, "page", true) }

func adminContents(c fiber.Ctx) error {
	kind := c.Query("kind", "article")
	if kind != "article" && kind != "page" {
		return failure(c, fiber.StatusBadRequest, "内容类型无效")
	}
	return list(c, kind, false)
}

func getContent(c fiber.Ctx, kind string, published bool) error {
	id, err := pathID(c)
	if err != nil {
		return failure(c, fiber.StatusBadRequest, err.Error())
	}
	q := global.ORM.CmsContent.Query().Where(cmscontent.IDEQ(id))
	if kind != "" {
		q.Where(cmscontent.KindEQ(cmscontent.Kind(kind)))
	}
	if published {
		q.Where(cmscontent.StatusEQ(1))
	}
	item, err := q.Only(c.Context())
	if err != nil {
		return databaseFailure(c, err)
	}
	if published && kind == "article" && item.CategoryID > 0 {
		active, err := global.ORM.CmsCategory.Query().Where(cmscategory.IDEQ(item.CategoryID), cmscategory.StatusEQ(1)).Exist(c.Context())
		if err != nil {
			return databaseFailure(c, err)
		}
		if !active {
			return failure(c, fiber.StatusNotFound, "文章不存在")
		}
	}
	return success(c, item)
}

func publicArticle(c fiber.Ctx) error { return getContent(c, "article", true) }
func publicPage(c fiber.Ctx) error    { return getContent(c, "page", true) }
func adminContent(c fiber.Ctx) error  { return getContent(c, "", false) }

func createContent(c fiber.Ctx) error { return writeContent(c, 0) }
func updateContent(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return failure(c, fiber.StatusBadRequest, err.Error())
	}
	return writeContent(c, id)
}

func writeContent(c fiber.Ctx, id int) error {
	var input contentInput
	if err := c.Bind().Body(&input); err != nil {
		return failure(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := saveContent(c.Context(), global.ORM, id, input)
	if err != nil {
		return databaseFailure(c, err)
	}
	return success(c, item)
}

func deleteContent(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return failure(c, fiber.StatusBadRequest, err.Error())
	}
	if err := global.ORM.CmsContent.DeleteOneID(id).Exec(c.Context()); err != nil {
		return databaseFailure(c, err)
	}
	return success(c, nil)
}

func categories(c fiber.Ctx, published bool) error {
	q := global.ORM.CmsCategory.Query()
	if published {
		q.Where(cmscategory.StatusEQ(1))
	}
	items, err := q.Order(ent.Asc(cmscategory.FieldID)).All(c.Context())
	if err != nil {
		return databaseFailure(c, err)
	}
	return success(c, items)
}

func publicCategories(c fiber.Ctx) error { return categories(c, true) }
func adminCategories(c fiber.Ctx) error  { return categories(c, false) }

func createCategory(c fiber.Ctx) error { return writeCategory(c, 0) }
func updateCategory(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return failure(c, fiber.StatusBadRequest, err.Error())
	}
	return writeCategory(c, id)
}

func writeCategory(c fiber.Ctx, id int) error {
	var input categoryInput
	if err := c.Bind().Body(&input); err != nil {
		return failure(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := saveCategory(c.Context(), global.ORM, id, input)
	if err != nil {
		return databaseFailure(c, err)
	}
	return success(c, item)
}

func deleteCategory(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return failure(c, fiber.StatusBadRequest, err.Error())
	}
	used, err := global.ORM.CmsContent.Query().Where(cmscontent.CategoryIDEQ(id)).Exist(c.Context())
	if err != nil {
		return databaseFailure(c, err)
	}
	if used {
		return failure(c, fiber.StatusConflict, "分类下还有文章，不能删除")
	}
	if err := global.ORM.CmsCategory.DeleteOneID(id).Exec(c.Context()); err != nil {
		return databaseFailure(c, err)
	}
	return success(c, nil)
}

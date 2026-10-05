package ent

import (
	"testing"

	"go-admin/internal/ent/cmscategory"
	"go-admin/internal/ent/cmscontent"
	"go-admin/internal/ent/migrate"
)

func TestInitTablePrefixIncludesGeneratedCMSTables(t *testing.T) {
	previous := cmscontent.TablePrefix
	t.Cleanup(func() { InitTablePrefix(previous) })
	InitTablePrefix("admin_")
	InitTablePrefix("admin_")
	if cmscontent.Table != "admin_cms_contents" || cmscategory.Table != "admin_cms_categories" ||
		migrate.CmsContentsTable.Name != "admin_cms_contents" || migrate.CmsCategoriesTable.Name != "admin_cms_categories" {
		t.Fatalf("CMS tables lost prefix: content=%s category=%s migration=%s/%s",
			cmscontent.Table, cmscategory.Table, migrate.CmsContentsTable.Name, migrate.CmsCategoriesTable.Name)
	}
}

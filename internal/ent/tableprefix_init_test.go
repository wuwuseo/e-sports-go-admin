package ent

import (
	"testing"

	"go-admin/internal/ent/democategory"
	"go-admin/internal/ent/demoitem"
	"go-admin/internal/ent/migrate"
)

func TestInitTablePrefixIncludesGeneratedAppRelations(t *testing.T) {
	previous := demoitem.TablePrefix
	t.Cleanup(func() { InitTablePrefix(previous) })
	InitTablePrefix("admin_")
	InitTablePrefix("admin_")
	if demoitem.Table != "admin_demo_items" || demoitem.CategoryTable != "admin_demo_items" ||
		demoitem.CategoryInverseTable != "admin_demo_categories" ||
		democategory.ItemsTable != "admin_demo_items" ||
		migrate.DemoItemsTable.Name != "admin_demo_items" {
		t.Fatalf("app tables lost prefix: item=%s edge=%s inverse=%s category-edge=%s migration=%s",
			demoitem.Table, demoitem.CategoryTable, demoitem.CategoryInverseTable,
			democategory.ItemsTable, migrate.DemoItemsTable.Name)
	}
}

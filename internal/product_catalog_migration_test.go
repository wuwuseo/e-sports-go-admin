package internal

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/wuwuseo/cmf/orm"
	_ "modernc.org/sqlite"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
)

func TestMigrateSingleProductCatalogPreservesOrderAndCommissionLinks(t *testing.T) {
	db, err := sql.Open("sqlite", "file:single_product_migration?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	for _, stmt := range []string{
		"CREATE TABLE esports_products (id integer primary key, name text)",
		"INSERT INTO esports_products (id,name) VALUES (1,'旧父商品')",
		"CREATE TABLE esports_skus (id integer primary key, code text, title text, unit_price_cents integer, product_id integer, spec_group_id integer, required_seats integer, status text)",
		"CREATE INDEX esportssku_product_id_sort_order ON esports_skus (product_id, id)",
		"INSERT INTO esports_skus (id,code,title,unit_price_cents,product_id,required_seats,status) VALUES (42,'P42','商品',9900,1,1,'on_sale')",
		"INSERT INTO esports_skus (id,code,title,unit_price_cents,product_id,required_seats,status) VALUES (43,'P43','历史多人商品',19900,1,2,'on_sale')",
		"CREATE TABLE esports_spec_groups (id integer primary key, name text)",
		"CREATE TABLE esports_orders (id integer primary key, sku_id integer, sku_code text, sku_name text)",
		"INSERT INTO esports_orders (id,sku_id,sku_code,sku_name) VALUES (77,42,'P42','商品')",
		"CREATE TABLE esports_commission_entries (id integer primary key, order_id integer, amount_cents integer)",
		"INSERT INTO esports_commission_entries (id,order_id,amount_cents) VALUES (88,77,1200)",
		"CREATE TABLE esports_quota_holds (id integer primary key, sku_id integer)",
		"INSERT INTO esports_quota_holds (id,sku_id) VALUES (9,42)",
		"CREATE TABLE esports_sku_suspensions (id integer primary key, sku_id integer, pending_key text)",
		"INSERT INTO esports_sku_suspensions (id,sku_id,pending_key) VALUES (10,42,'sku:42')",
		`CREATE TABLE esports_page_documents (id integer primary key, draft_config text)`,
		`INSERT INTO esports_page_documents (id,draft_config) VALUES (1,'{"components":[{"props":{"sku_ids":[42],"data_source":"sku","scope":"sku","filter":{"sku_id":42}}}]}')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := migrateSingleProductCatalog(ctx, db, "sqlite", ""); err != nil {
			t.Fatal(err)
		}
	}
	var productID, orderID, commission int
	if err := db.QueryRowContext(ctx, "SELECT o.product_id,c.order_id,c.amount_cents FROM esports_orders AS o JOIN esports_commission_entries AS c ON c.order_id=o.id JOIN esports_products AS p ON p.id=o.product_id WHERE o.id=77").Scan(&productID, &orderID, &commission); err != nil {
		t.Fatal(err)
	}
	if productID != 42 || orderID != 77 || commission != 1200 {
		t.Fatalf("association changed: %d %d %d", productID, orderID, commission)
	}
	var multiStatus string
	if err := db.QueryRowContext(ctx, "SELECT status FROM esports_products WHERE id=43").Scan(&multiStatus); err != nil || multiStatus != "off_sale" {
		t.Fatalf("legacy multi-seat product should be off sale: %s %v", multiStatus, err)
	}
	var pendingKey string
	if err := db.QueryRowContext(ctx, "SELECT pending_key FROM esports_product_suspensions WHERE id=10").Scan(&pendingKey); err != nil || pendingKey != "product:42" {
		t.Fatalf("suspension key changed incorrectly: %s %v", pendingKey, err)
	}
	for _, field := range []string{"product_id", "spec_group_id"} {
		exists, err := catalogColumnExists(ctx, db, "esports_products", field)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("old product field remains: %s", field)
		}
	}
	if err := finalizeSingleProductCatalog(ctx, db, "sqlite", ""); err != nil {
		t.Fatal(err)
	}
	var pageConfig string
	if err := db.QueryRowContext(ctx, "SELECT draft_config FROM esports_page_documents WHERE id=1").Scan(&pageConfig); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pageConfig, `"product_ids":[42]`) || !strings.Contains(pageConfig, `"product_id":42`) ||
		!strings.Contains(pageConfig, `"data_source":"product"`) || !strings.Contains(pageConfig, `"scope":"product"`) || strings.Contains(pageConfig, `"sku_id"`) {
		t.Fatalf("page product references not migrated: %s", pageConfig)
	}
	for _, name := range []string{"esports_skus", "esports_sku_suspensions", "esports_spec_groups", "esports_product_groups_legacy"} {
		exists, err := catalogTableExists(ctx, db, "sqlite", name)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("obsolete table remains: %s", name)
		}
	}
}

func TestSingleProductCatalogMigrationRunsWithEntSchema(t *testing.T) {
	const source = "file:single_product_ent_schema?mode=memory&cache=shared&_fk=1"
	client := enttest.Open(t, "sqlite3", source, enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	ctx := t.Context()
	product := client.EsportsSku.Create().SetCode("PERSIST-42").SetTitle("旧商品").
		SetServiceType("play").SetUnitPriceCents(9900).SetServiceUnits(1).
		SetServiceUnitName("小时").SetRequiredSeats(1).SaveX(ctx)
	db, err := sql.Open("sqlite3", source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	prefix := orm.GetTablePrefix()
	for _, stmt := range []string{
		fmt.Sprintf("ALTER TABLE %sesports_products RENAME TO %sesports_skus", prefix, prefix),
		fmt.Sprintf("ALTER TABLE %sesports_skus ADD COLUMN product_id integer", prefix),
		fmt.Sprintf("ALTER TABLE %sesports_skus ADD COLUMN spec_group_id integer", prefix),
		fmt.Sprintf("CREATE INDEX esportssku_product_id_sort_order ON %sesports_skus (product_id, sort_order)", prefix),
		fmt.Sprintf("CREATE TABLE %sesports_products (id integer primary key, name text)", prefix),
		fmt.Sprintf("ALTER TABLE %sesports_orders RENAME COLUMN product_id TO sku_id", prefix),
		fmt.Sprintf("ALTER TABLE %sesports_orders RENAME COLUMN product_code TO sku_code", prefix),
		fmt.Sprintf("ALTER TABLE %sesports_orders RENAME COLUMN product_name TO sku_name", prefix),
		fmt.Sprintf("ALTER TABLE %sesports_quota_holds RENAME COLUMN product_id TO sku_id", prefix),
		fmt.Sprintf("ALTER TABLE %sesports_confirmations RENAME COLUMN product_id TO sku_id", prefix),
		fmt.Sprintf("ALTER TABLE %sesports_product_suspensions RENAME TO %sesports_sku_suspensions", prefix, prefix),
		fmt.Sprintf("ALTER TABLE %sesports_sku_suspensions RENAME COLUMN product_id TO sku_id", prefix),
		fmt.Sprintf("ALTER TABLE %sesports_after_sale_rules RENAME COLUMN scope_product_id TO scope_sku_id", prefix),
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("simulate old catalog: %s: %v", stmt, err)
		}
	}
	if err := migrateSingleProductCatalog(ctx, db, "sqlite3", prefix); err != nil {
		t.Fatal(err)
	}
	if err := client.Schema.Create(ctx, migrate.WithDropIndex(true), migrate.WithDropColumn(true), migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	if err := finalizeSingleProductCatalog(ctx, db, "sqlite3", prefix); err != nil {
		t.Fatal(err)
	}
	if got := client.EsportsSku.GetX(ctx, product.ID); got.Code != "PERSIST-42" {
		t.Fatalf("product ID or data changed: %+v", got)
	}
	for _, field := range []string{"product_id", "spec_group_id"} {
		exists, err := catalogColumnExists(ctx, db, prefix+"esports_products", field)
		if err != nil || exists {
			t.Fatalf("obsolete column %s remains: %v", field, err)
		}
	}
}

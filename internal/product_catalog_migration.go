package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var catalogIdentifier = regexp.MustCompile("^[a-zA-Z0-9_]+$")

// migrateSingleProductCatalog 在 Ent 自动迁移前把可售 SKU 原 ID 原数据迁入商品表。
// 订单、名额、确认、停服和售后规则只改列名，ID 不重新编号，因此报表与分成关联不变。
func migrateSingleProductCatalog(ctx context.Context, db *sql.DB, driver, prefix string) error {
	if !catalogIdentifier.MatchString(prefix) && prefix != "" {
		return fmt.Errorf("invalid catalog table prefix")
	}
	oldItems := prefix + "esports_skus"
	products := prefix + "esports_products"
	legacyGroups := prefix + "esports_product_groups_legacy"
	oldSuspensions := prefix + "esports_sku_suspensions"
	suspensions := prefix + "esports_product_suspensions"

	oldExists, err := catalogTableExists(ctx, db, driver, oldItems)
	if err != nil {
		return err
	}
	productExists, err := catalogTableExists(ctx, db, driver, products)
	if err != nil {
		return err
	}
	legacyExists, err := catalogTableExists(ctx, db, driver, legacyGroups)
	if err != nil {
		return err
	}
	if !oldExists && productExists {
		priced, err := catalogColumnExists(ctx, db, products, "unit_price_cents")
		if err != nil {
			return err
		}
		if !priced {
			if legacyExists {
				return fmt.Errorf("商品迁移冲突：父商品表与临时表同时存在")
			}
			if err := catalogRenameTable(ctx, db, driver, products, legacyGroups); err != nil {
				return err
			}
		}
	}

	if oldExists {
		if productExists {
			if legacyExists {
				return fmt.Errorf("商品迁移冲突：父商品表与临时表同时存在")
			}
			if err := catalogRenameTable(ctx, db, driver, products, legacyGroups); err != nil {
				return err
			}
		}
		if err := catalogRenameTable(ctx, db, driver, oldItems, products); err != nil {
			return err
		}
	}
	if exists, err := catalogTableExists(ctx, db, driver, products); err != nil {
		return err
	} else if exists {
		if err := catalogDropLegacyProductFields(ctx, db, driver, products); err != nil {
			return err
		}
		// 历史多人商品保留供旧订单追溯，但停止新增报单；运营编辑后会按单人商品保存。
		seats, err := catalogColumnExists(ctx, db, products, "required_seats")
		if err != nil {
			return err
		}
		status, err := catalogColumnExists(ctx, db, products, "status")
		if err != nil {
			return err
		}
		if seats && status {
			if _, err := db.ExecContext(ctx, "UPDATE "+catalogQuote(driver, products)+" SET status = 'off_sale' WHERE required_seats > 1 AND status = 'on_sale'"); err != nil {
				return fmt.Errorf("retire multi-seat products: %w", err)
			}
		}
	}

	oldSuspExists, err := catalogTableExists(ctx, db, driver, oldSuspensions)
	if err != nil {
		return err
	}
	newSuspExists, err := catalogTableExists(ctx, db, driver, suspensions)
	if err != nil {
		return err
	}
	if oldSuspExists {
		if newSuspExists {
			return fmt.Errorf("商品停服迁移冲突：新旧申请表同时存在")
		}
		if err := catalogRenameTable(ctx, db, driver, oldSuspensions, suspensions); err != nil {
			return err
		}
	}

	changes := []struct{ table, old, new, mysqlType string }{
		{prefix + "esports_orders", "sku_id", "product_id", "bigint NOT NULL"},
		{prefix + "esports_orders", "sku_code", "product_code", "varchar(80) NOT NULL DEFAULT ''"},
		{prefix + "esports_orders", "sku_name", "product_name", "varchar(160) NOT NULL DEFAULT ''"},
		{prefix + "esports_quota_holds", "sku_id", "product_id", "bigint NOT NULL"},
		{prefix + "esports_confirmations", "sku_id", "product_id", "bigint NOT NULL"},
		{suspensions, "sku_id", "product_id", "bigint NOT NULL"},
		{prefix + "esports_after_sale_rules", "scope_sku_id", "scope_product_id", "bigint NULL"},
	}
	for _, c := range changes {
		exists, err := catalogTableExists(ctx, db, driver, c.table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		oldColumn, err := catalogColumnExists(ctx, db, c.table, c.old)
		if err != nil {
			return err
		}
		newColumn, err := catalogColumnExists(ctx, db, c.table, c.new)
		if err != nil {
			return err
		}
		if oldColumn && newColumn {
			return fmt.Errorf("商品迁移冲突：%s 同时存在 %s 和 %s", c.table, c.old, c.new)
		}
		if !oldColumn {
			continue
		}
		var query string
		if driver == "mysql" {
			query = fmt.Sprintf("ALTER TABLE %s CHANGE COLUMN %s %s %s",
				catalogQuote(driver, c.table), catalogQuote(driver, c.old), catalogQuote(driver, c.new), c.mysqlType)
		} else {
			query = fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s",
				catalogQuote(driver, c.table), catalogQuote(driver, c.old), catalogQuote(driver, c.new))
		}
		if _, err := db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("rename %s.%s: %w", c.table, c.old, err)
		}
	}
	if exists, err := catalogTableExists(ctx, db, driver, suspensions); err != nil {
		return err
	} else if exists {
		if hasKey, err := catalogColumnExists(ctx, db, suspensions, "pending_key"); err != nil {
			return err
		} else if hasKey {
			query := "UPDATE " + catalogQuote(driver, suspensions) + " SET pending_key = 'product:' || substr(pending_key, 5) WHERE pending_key LIKE 'sku:%'"
			if driver == "mysql" {
				query = "UPDATE " + catalogQuote(driver, suspensions) + " SET pending_key = CONCAT('product:', SUBSTRING(pending_key, 5)) WHERE pending_key LIKE 'sku:%'"
			}
			if _, err := db.ExecContext(ctx, query); err != nil {
				return fmt.Errorf("rename product suspension key: %w", err)
			}
		}
	}
	if exists, err := catalogTableExists(ctx, db, driver, products); err != nil {
		return err
	} else if exists {
		for _, table := range []string{prefix + "esports_orders", prefix + "esports_quota_holds", prefix + "esports_confirmations", suspensions} {
			exists, err := catalogTableExists(ctx, db, driver, table)
			if err != nil {
				return err
			}
			if !exists {
				continue
			}
			var missing int
			query := fmt.Sprintf("SELECT COUNT(*) FROM %s AS r LEFT JOIN %s AS p ON p.id = r.product_id WHERE r.product_id > 0 AND p.id IS NULL", catalogQuote(driver, table), catalogQuote(driver, products))
			if err := db.QueryRowContext(ctx, query).Scan(&missing); err != nil {
				return err
			}
			if missing > 0 {
				return fmt.Errorf("商品迁移中止：%s 有 %d 条无效商品关联", table, missing)
			}
		}
	}
	return nil
}

// 旧父商品和规格组的挂载列不再属于单商品表。先删依赖索引，再删列；重试时逐项跳过已完成的步骤。
func catalogDropLegacyProductFields(ctx context.Context, db *sql.DB, driver, products string) error {
	for _, index := range []string{"esportssku_product_id_sort_order"} {
		var exists bool
		switch driver {
		case "mysql":
			var count int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?", products, index).Scan(&count); err != nil {
				return err
			}
			exists = count > 0
		case "sqlite", "sqlite3":
			var count int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ? AND tbl_name = ?", index, products).Scan(&count); err != nil {
				return err
			}
			exists = count > 0
		default:
			return fmt.Errorf("unsupported catalog migration driver %q", driver)
		}
		if exists {
			query := "DROP INDEX " + catalogQuote(driver, index)
			if driver == "mysql" {
				query += " ON " + catalogQuote(driver, products)
			}
			if _, err := db.ExecContext(ctx, query); err != nil {
				return fmt.Errorf("drop old product index %s: %w", index, err)
			}
		}
	}
	for _, field := range []string{"product_id", "spec_group_id"} {
		exists, err := catalogColumnExists(ctx, db, products, field)
		if err != nil {
			return err
		}
		if exists {
			if _, err := db.ExecContext(ctx, "ALTER TABLE "+catalogQuote(driver, products)+" DROP COLUMN "+catalogQuote(driver, field)); err != nil {
				return fmt.Errorf("drop old product field %s: %w", field, err)
			}
		}
	}
	return nil
}

func catalogRenameTable(ctx context.Context, db *sql.DB, driver, oldName, newName string) error {
	if !catalogIdentifier.MatchString(oldName) || !catalogIdentifier.MatchString(newName) {
		return fmt.Errorf("invalid catalog table name")
	}
	query := "ALTER TABLE " + catalogQuote(driver, oldName) + " RENAME TO " + catalogQuote(driver, newName)
	if driver == "mysql" {
		query = "RENAME TABLE " + catalogQuote(driver, oldName) + " TO " + catalogQuote(driver, newName)
	}
	if _, err := db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("rename table %s: %w", oldName, err)
	}
	return nil
}

func finalizeSingleProductCatalog(ctx context.Context, db *sql.DB, driver, prefix string) error {
	if err := migrateProductPageConfigs(ctx, db, driver, prefix); err != nil {
		return err
	}
	for _, name := range []string{prefix + "esports_product_groups_legacy", prefix + "esports_spec_groups"} {
		exists, err := catalogTableExists(ctx, db, driver, name)
		if err != nil {
			return err
		}
		if exists {
			if _, err := db.ExecContext(ctx, "DROP TABLE "+catalogQuote(driver, name)); err != nil {
				return fmt.Errorf("drop obsolete catalog table %s: %w", name, err)
			}
		}
	}
	return nil
}

// 页面装修持久化的是自由 JSON 属性；旧指定 SKU 列表改为指定商品列表，保留原 ID。
func migrateProductPageConfigs(ctx context.Context, db *sql.DB, driver, prefix string) error {
	for _, item := range []struct{ table, column string }{
		{prefix + "esports_page_documents", "draft_config"},
		{prefix + "esports_page_versions", "config"},
	} {
		exists, err := catalogTableExists(ctx, db, driver, item.table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		rows, err := db.QueryContext(ctx, "SELECT id, "+catalogQuote(driver, item.column)+" FROM "+catalogQuote(driver, item.table))
		if err != nil {
			return err
		}
		type change struct {
			id   int
			data []byte
		}
		var updates []change
		for rows.Next() {
			var id int
			var raw []byte
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			var payload interface{}
			if err := json.Unmarshal(raw, &payload); err != nil {
				rows.Close()
				return fmt.Errorf("decode page config %s#%d: %w", item.table, id, err)
			}
			if renameProductPageKeys(payload) {
				data, err := json.Marshal(payload)
				if err != nil {
					rows.Close()
					return err
				}
				updates = append(updates, change{id: id, data: data})
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, update := range updates {
			if _, err := db.ExecContext(ctx, "UPDATE "+catalogQuote(driver, item.table)+" SET "+catalogQuote(driver, item.column)+" = ? WHERE id = ?", update.data, update.id); err != nil {
				return fmt.Errorf("update page config %s#%d: %w", item.table, update.id, err)
			}
		}
	}
	return nil
}

func renameProductPageKeys(value interface{}) bool {
	changed := false
	switch v := value.(type) {
	case map[string]interface{}:
		for old, name := range map[string]string{"sku_id": "product_id", "sku_ids": "product_ids"} {
			if legacy, exists := v[old]; exists {
				if _, present := v[name]; !present {
					v[name] = legacy
				}
				delete(v, old)
				changed = true
			}
		}
		for _, key := range []string{"data_source", "scope"} {
			if raw, ok := v[key].(string); ok && raw == "sku" {
				v[key] = "product"
				changed = true
			}
		}
		for _, child := range v {
			if renameProductPageKeys(child) {
				changed = true
			}
		}
	case []interface{}:
		for _, child := range v {
			if renameProductPageKeys(child) {
				changed = true
			}
		}
	}
	return changed
}

func catalogQuote(driver, name string) string {
	if driver == "mysql" {
		return "`" + name + "`"
	}
	return "\"" + name + "\""
}

func catalogTableExists(ctx context.Context, db *sql.DB, driver, name string) (bool, error) {
	var count int
	var err error
	switch driver {
	case "mysql":
		err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", name).Scan(&count)
	case "sqlite", "sqlite3":
		err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&count)
	default:
		return false, fmt.Errorf("unsupported catalog migration driver %q", driver)
	}
	return count > 0, err
}

func catalogColumnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	if !catalogIdentifier.MatchString(table) || !catalogIdentifier.MatchString(column) {
		return false, fmt.Errorf("invalid catalog identifier")
	}
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+table+" LIMIT 0")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return false, err
	}
	for _, item := range columns {
		if strings.EqualFold(item, column) {
			return true, nil
		}
	}
	return false, nil
}

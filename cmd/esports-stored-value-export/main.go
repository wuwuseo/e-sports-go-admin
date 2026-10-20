// esports-stored-value-export 储值卡存量数据导出工具。
//
// 背景：储值卡功能已于 2026-10-20 由产品决定彻底移除，Ent schema 与相关代码
// 一并删除，下一次迁移会 DROP 掉三张表：
//
//   - {prefix}esports_stored_value_cards      储值卡主表
//   - {prefix}esports_stored_value_packages   储值卡套餐
//   - {prefix}esports_stored_value_exceptions 储值卡异常队列
//
// 客户卡内的本金/赠送是**真实预付资金**，DROP 之前必须留一份清单用于清退与财务对账，
// 因此提供本只读导出工具。它**只读不写**，绝不修改任何数据。
//
// 输出内容（CSV，UTF-8 带 BOM，Excel 可直接打开）：
//
//	卡号、客户 ID、开卡来源、结算渠道、状态、有效期起止、开卡时间、
//	本金（分）、赠送（分）、已退（分）、
//	台账剩余本金（分）、台账剩余赠送（分）
//
// 其中「剩余」一列不是卡表字段，而是由钱包流水（account_type=stored_value_card）
// 按卡归属人求和派生——账本才是余额真相，卡表上的 principal/bonus 只是开卡时的快照。
// 若台账缺失（例如历史数据被清理过），该列留空并在统计里单列计数，不做静默补零。
//
// 用法：
//
//	go run ./cmd/esports-stored-value-export                     # 导出到 ./stored-value-export-<时间戳>.csv
//	go run ./cmd/esports-stored-value-export --out D:\backup\sv.csv
//	go run ./cmd/esports-stored-value-export --out -             # 输出到标准输出（便于管道处理）
//
// 表名带动态前缀，故通过 information_schema 解析，不依赖内部前缀常量。
package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"go-admin/internal"
)

// storedValueCardRow 是导出行（字段名与 CSV 表头一一对应）。
type storedValueCardRow struct {
	// CardID 是卡主键，同时就是储值卡钱包账户的 owner_id，不输出到 CSV，只用于对齐台账。
	CardID          int64
	CardNo          string
	CustomerID      int64
	Source          string
	PaymentChannel  string
	Status          string
	ValidFrom       string
	ValidUntil      string
	CreatedAt       string
	PrincipalCents  int64
	BonusCents      int64
	RefundedCents   int64
	RemainPrincipal sql.NullInt64
	RemainBonus     sql.NullInt64
}

func main() {
	out := flag.String("out", "", "输出 CSV 路径；省略时写到当前目录 stored-value-export-<时间戳>.csv，传 - 表示标准输出")
	sampleEmpty := flag.Bool("sample", false, "仅打印前 10 条样例与汇总，不写文件（用于上线前快速核对）")
	flag.Parse()

	app, err := internal.InitializeApp()
	if err != nil {
		fail("初始化应用（读取 config 并连接数据库）失败", err)
	}
	if app.DB == nil {
		fail("应用未提供数据库连接（app.DB 为空）", nil)
	}
	defer func() { _ = app.DB.Close() }()
	if app.EntClient != nil {
		defer func() { _ = app.EntClient.Close() }()
	}
	// app.DB 是连接池管理器，这里取默认库的原生连接做只读导出。
	sqlDB := app.DB.GetDB()
	if sqlDB == nil {
		fail("默认数据库连接未就绪（GetDB 返回空）", nil)
	}

	ctx := context.Background()

	cardsTable, err := resolveTable(ctx, sqlDB, "esports_stored_value_cards")
	if err != nil {
		fail("未找到储值卡主表（可能已被迁移删除，或当前库没有该功能的历史数据）", err)
	}
	walletTxTable, walletTxErr := resolveTable(ctx, sqlDB, "esports_wallet_txes")

	rows, err := loadCards(ctx, sqlDB, cardsTable)
	if err != nil {
		fail("导出储值卡主表失败", err)
	}

	var balanceErr error
	if walletTxErr == nil && len(rows) > 0 {
		balanceErr = attachRemainingBalances(ctx, sqlDB, walletTxTable, rows)
	} else if walletTxErr != nil {
		balanceErr = walletTxErr
	}

	printSummary(rows, walletTxErr, balanceErr)

	if *sampleEmpty {
		fmt.Println("\n样例（最多 10 条）：")
		for i, row := range rows {
			if i >= 10 {
				break
			}
			fmt.Printf("  %s | 客户=%d | 状态=%s | 本金=%d 分 | 赠送=%d 分 | 剩余本金=%s | 剩余赠送=%s\n",
				row.CardNo, row.CustomerID, row.Status, row.PrincipalCents, row.BonusCents,
				nullText(row.RemainPrincipal), nullText(row.RemainBonus))
		}
		return
	}

	path := *out
	toStdout := path == "-"
	if path == "" {
		path = fmt.Sprintf("stored-value-export-%s.csv", time.Now().Format("20060102150405"))
	}

	if toStdout {
		if err := writeCSV(os.Stdout, rows); err != nil {
			fail("写入标准输出失败", err)
		}
		return
	}
	file, err := os.Create(path)
	if err != nil {
		fail("创建输出文件失败", err)
	}
	defer func() { _ = file.Close() }()
	if err := writeCSV(file, rows); err != nil {
		fail("写入 CSV 失败", err)
	}
	fmt.Printf("\n已导出到：%s\n", path)
}

// resolveTable 通过 information_schema 找出带动态前缀的真实表名。
// 只在本库内匹配后缀，避免把其它库的同名表算进来。
func resolveTable(ctx context.Context, db *sql.DB, suffix string) (string, error) {
	const query = `SELECT TABLE_NAME FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME LIKE ?
		ORDER BY TABLE_NAME LIMIT 1`
	var name string
	if err := db.QueryRowContext(ctx, query, "%"+suffix).Scan(&name); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("当库中不存在以 %q 结尾的表", suffix)
		}
		return "", err
	}
	return name, nil
}

// loadCards 读取储值卡主表。只用 SELECT，绝不写库。
func loadCards(ctx context.Context, db *sql.DB, table string) ([]*storedValueCardRow, error) {
	query := fmt.Sprintf(`SELECT id, card_no, customer_id, source, payment_channel, status,
			valid_from, valid_until, created_at, principal_cents, bonus_cents, refunded_cents
		FROM %s ORDER BY id`, quoteIdent(table))
	result, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = result.Close() }()

	rows := make([]*storedValueCardRow, 0, 64)
	for result.Next() {
		var (
			row                    storedValueCardRow
			cardID, customerID     int64
			source, channel, state sql.NullString
			validFrom, validUntil  sql.NullTime
			createdAt              sql.NullTime
			principal, bonus       sql.NullInt64
			refunded               sql.NullInt64
		)
		if err = result.Scan(&cardID, &row.CardNo, &customerID, &source, &channel, &state,
			&validFrom, &validUntil, &createdAt, &principal, &bonus, &refunded); err != nil {
			return nil, err
		}
		row.CardID = cardID
		row.CustomerID = customerID
		row.Source = source.String
		row.PaymentChannel = channel.String
		row.Status = state.String
		row.ValidFrom = formatTime(validFrom)
		row.ValidUntil = formatTime(validUntil)
		row.CreatedAt = formatTime(createdAt)
		row.PrincipalCents = principal.Int64
		row.BonusCents = bonus.Int64
		row.RefundedCents = refunded.Int64
		rows = append(rows, &row)
	}
	if err = result.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

// attachRemainingBalances 用钱包流水派生每张卡的台账剩余（本金/赠送）。
//
// 不直接信任卡表快照：储值卡账户的余额一律由流水求和得出（与系统内其它账户同口径），
// 卡表的 principal/bonus 只是开卡瞬间的值，退卡与核销之后会与真实余额脱节。
func attachRemainingBalances(ctx context.Context, db *sql.DB, walletTxTable string, rows []*storedValueCardRow) error {
	query := fmt.Sprintf(`SELECT owner_id,
			COALESCE(SUM(principal_delta_cents), 0),
			COALESCE(SUM(bonus_delta_cents), 0)
		FROM %s WHERE account_type = 'stored_value_card' GROUP BY owner_id`, quoteIdent(walletTxTable))
	result, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { _ = result.Close() }()

	// 卡 ID → 剩余本金/赠送。卡 ID 即储值卡账户的 owner_id（每卡独立账户）。
	remaining := make(map[int64][2]int64, len(rows))
	for result.Next() {
		var ownerID sql.NullInt64
		var principal, bonus sql.NullInt64
		if err = result.Scan(&ownerID, &principal, &bonus); err != nil {
			return err
		}
		remaining[ownerID.Int64] = [2]int64{principal.Int64, bonus.Int64}
	}
	if err = result.Err(); err != nil {
		return err
	}

	for _, row := range rows {
		if value, hit := remaining[row.CardID]; hit {
			row.RemainPrincipal = sql.NullInt64{Int64: value[0], Valid: true}
			row.RemainBonus = sql.NullInt64{Int64: value[1], Valid: true}
		}
	}
	return nil
}

func writeCSV(out *os.File, rows []*storedValueCardRow) error {
	// 带 BOM，Excel 打开中文表头不乱码。
	if _, err := out.WriteString("\ufeff"); err != nil {
		return err
	}
	writer := csv.NewWriter(out)
	header := []string{
		"卡号", "客户ID", "开卡来源", "结算渠道", "状态", "有效期起", "有效期止", "开卡时间",
		"本金(分)", "赠送(分)", "已退(分)", "台账剩余本金(分)", "台账剩余赠送(分)",
	}
	if err := writer.Write(header); err != nil {
		return err
	}
	for _, row := range rows {
		record := []string{
			row.CardNo,
			strconv.FormatInt(row.CustomerID, 10),
			row.Source,
			row.PaymentChannel,
			row.Status,
			row.ValidFrom,
			row.ValidUntil,
			row.CreatedAt,
			strconv.FormatInt(row.PrincipalCents, 10),
			strconv.FormatInt(row.BonusCents, 10),
			strconv.FormatInt(row.RefundedCents, 10),
			nullText(row.RemainPrincipal),
			nullText(row.RemainBonus),
		}
		if err := writer.Write(record); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

// printSummary 打印汇总，便于上线前一眼核对规模。
func printSummary(rows []*storedValueCardRow, walletTxErr, balanceErr error) {
	var principal, bonus, refunded, remainPrincipal, remainBonus int64
	var missingRemain int
	for _, row := range rows {
		principal += row.PrincipalCents
		bonus += row.BonusCents
		refunded += row.RefundedCents
		if !row.RemainPrincipal.Valid {
			missingRemain++
			continue
		}
		remainPrincipal += row.RemainPrincipal.Int64
		remainBonus += row.RemainBonus.Int64
	}
	writeLine := func(format string, args ...any) { fmt.Printf(format+"\n", args...) }

	writeLine("储值卡存量导出（只读）")
	writeLine("  卡数：%d", len(rows))
	writeLine("  开卡本金合计：%s", yuan(principal))
	writeLine("  开卡赠送合计：%s", yuan(bonus))
	writeLine("  已退合计：    %s", yuan(refunded))
	writeLine("  台账剩余本金：%s", yuan(remainPrincipal))
	writeLine("  台账剩余赠送：%s", yuan(remainBonus))
	if walletTxErr != nil {
		writeLine("  ⚠️ 未找到钱包流水表（%v），剩余列留空；清退金额请以卡表本金/赠送为准并人工复核", walletTxErr)
	} else if balanceErr != nil {
		writeLine("  ⚠️ 台账求和失败（%v），剩余列留空；清退金额请人工复核", balanceErr)
	} else if missingRemain > 0 {
		writeLine("  ⚠️ 有 %d 张卡在台账中查不到对应账户（历史数据可能被清理），这些卡的剩余列为空", missingRemain)
	}
}

func formatTime(value sql.NullTime) string {
	if !value.Valid {
		return ""
	}
	return value.Time.Format("2006-01-02 15:04:05")
}

func nullText(value sql.NullInt64) string {
	if !value.Valid {
		return ""
	}
	return strconv.FormatInt(value.Int64, 10)
}

// yuan 把「分」格式化成两位小数的「元」，仅用于控制台汇总展示。
func yuan(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d 元", sign, cents/100, cents%100)
}

// quoteIdent 用反引号包裹标识符。表名来自 information_schema，仍是外部输入，
// 这里对反引号做转义，避免拼接出非法 SQL。
func quoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func fail(message string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s：%v\n", message, err)
	} else {
		fmt.Fprintln(os.Stderr, message)
	}
	os.Exit(1)
}

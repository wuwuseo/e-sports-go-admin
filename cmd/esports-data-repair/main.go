// esports-data-repair 陪玩报单历史脏数据修复工具（审核报告 §8.2-17）。
//
// 默认 dry-run：只统计并打印样例，绝不写库；--apply 才执行第一类补齐，且必须显式传入
// --per-seat-units N（不传则拒绝执行）。三类历史数据：
//
//  1. 多人 SKU（required_seats > 1）缺「每席承诺服务量 per_seat_service_units」——可补齐，
//     但补齐值无法从历史数据推断，必须人工显式指定（--per-seat-units N）。
//  2. SKU required_level 全为 novice——多人 / 多档 SKU 的历史等级门槛可能被默认值刷平，
//     真实等级无法自动判定，只做统计报告，不自动改。
//  3. 优先名单 game 与商品分类 game 对不上——无法安全推断哪一侧是对的，只做统计报告，不自动改。
//
// 可重复执行：第一类补齐后该行不再命中候选集，重复跑 --apply 的更新行数为 0。
//
// 用法：
//
//	go run ./cmd/esports-data-repair                       # dry-run（默认）
//	go run ./cmd/esports-data-repair --dry-run             # 同上，显式表述
//	go run ./cmd/esports-data-repair --apply --per-seat-units 2
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	_ "github.com/go-sql-driver/mysql"

	"go-admin/internal"
	appent "go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchprioritygrant"
	"go-admin/internal/ent/esportsproductcategory"
	"go-admin/internal/ent/esportssku"
)

func main() {
	apply := flag.Bool("apply", false, "真正写库（默认关闭，只做 dry-run 统计）")
	dryRun := flag.Bool("dry-run", true, "只统计并打印将受影响的记录数与样例（默认行为）")
	perSeatUnits := flag.Int64("per-seat-units", 0, "--apply 时必填：多人 SKU 补齐的每席承诺服务量（须为正整数）")
	sample := flag.Int("sample", 10, "每类打印的样例条数")
	flag.Parse()

	// 只有显式 --apply 才写库；显式关掉 dry-run 却没给 --apply 属于参数自相矛盾，直接拒绝。
	if !*apply && !*dryRun {
		exitMessage("参数错误：只写 --dry-run=false 不会执行修复，请显式使用 --apply")
	}
	write := *apply
	if write && *perSeatUnits <= 0 {
		exitMessage("参数错误：--apply 必须同时显式传入 --per-seat-units N（N 为正整数），补齐值不能由工具猜测")
	}
	if *sample < 0 {
		exitMessage("参数错误：--sample 不能为负数")
	}

	app, err := internal.InitializeApp()
	if err != nil {
		exitError("初始化应用（读取 config 并连接数据库）失败", err)
	}
	if app.EntClient != nil {
		defer app.EntClient.Close()
	}
	if app.DB != nil {
		defer app.DB.Close()
	}
	ctx := context.Background()

	if write {
		fmt.Println("模式：--apply（会写库）")
	} else {
		fmt.Println("模式：dry-run（只统计，不写库；确认无误后加 --apply --per-seat-units N 执行）")
	}
	fmt.Println(strings.Repeat("=", 72))

	if err := repairMultiSeatSkuUnits(ctx, app.EntClient, write, *perSeatUnits, *sample); err != nil {
		exitError("处理多人 SKU 每席承诺服务量失败", err)
	}
	fmt.Println()
	if err := reportNoviceLevelSkus(ctx, app.EntClient, *sample); err != nil {
		exitError("统计 SKU 等级门槛失败", err)
	}
	fmt.Println()
	if err := reportPriorityGameMismatch(ctx, app.EntClient, *sample); err != nil {
		exitError("统计优先名单游戏分区不一致失败", err)
	}
	fmt.Println()
	if !write {
		fmt.Println("提示：以上仅为统计结果，未做任何写入。")
	}
}

// repairMultiSeatSkuUnits 处理第一类：多人 SKU（required_seats > 1）缺每席承诺服务量。
// dry-run 只统计与打印样例；--apply 时把候选行补成 perSeatUnits 并抬升版本号。
func repairMultiSeatSkuUnits(ctx context.Context, client *appent.Client, write bool,
	perSeatUnits int64, sample int) error {
	skus, err := client.EsportsSku.Query().Order(appent.Asc(esportssku.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	var candidate, alreadyConfigured, singleSeat []*appent.EsportsSku
	for _, sku := range skus {
		if sku.RequiredSeats <= 1 {
			singleSeat = append(singleSeat, sku)
			continue
		}
		if sku.PerSeatServiceUnits > 0 {
			alreadyConfigured = append(alreadyConfigured, sku)
			continue
		}
		candidate = append(candidate, sku)
	}

	fmt.Println("【第一类】多人 SKU 缺「每席承诺服务量」")
	fmt.Printf("  SKU 总数：%d；其中多人 SKU（required_seats>1）：%d\n",
		len(skus), len(candidate)+len(alreadyConfigured))
	fmt.Printf("  待补齐：%d 条；跳过原因统计：已配置每席服务量 %d 条、非多人 SKU %d 条\n",
		len(candidate), len(alreadyConfigured), len(singleSeat))
	printSkuSamples(candidate, sample)
	if !write {
		return nil
	}
	if len(candidate) == 0 {
		fmt.Println("  无需补齐，未执行任何更新。")
		return nil
	}
	updated := 0
	for _, sku := range candidate {
		// 只改每席服务量这一列，并按既有 SKU 更新口径抬版本号，避免并发读到半更新快照。
		n, err := client.EsportsSku.Update().
			Where(esportssku.IDEQ(sku.ID), esportssku.PerSeatServiceUnitsEQ(sku.PerSeatServiceUnits)).
			SetPerSeatServiceUnits(perSeatUnits).AddVersion(1).Save(ctx)
		if err != nil {
			return fmt.Errorf("补齐 SKU %s（id=%d）失败: %w", sku.Code, sku.ID, err)
		}
		updated += n
	}
	fmt.Printf("  已补齐 %d 条：per_seat_service_units = %d（重复执行时更新行数为 0）\n",
		updated, perSeatUnits)
	return nil
}

// printSkuSamples 打印待处理 SKU 样例，便于人工核对。
func printSkuSamples(skus []*appent.EsportsSku, sample int) {
	for i, sku := range skus {
		if i >= sample {
			fmt.Printf("  … 其余 %d 条不再列出\n", len(skus)-sample)
			break
		}
		fmt.Printf("    - id=%d code=%q title=%q required_seats=%d per_seat_service_units=%d "+
			"service_units=%d required_level=%s status=%s\n",
			sku.ID, sku.Code, sku.Title, sku.RequiredSeats, sku.PerSeatServiceUnits,
			sku.ServiceUnits, sku.RequiredLevel, sku.Status)
	}
}

// reportNoviceLevelSkus 处理第二类：统计 required_level 仍为 novice 的 SKU。
// 等级门槛的真实意图无法从历史数据推断（多人 / 多档 SKU 也可能是历史默认值刷平），
// 因此只出报告，不自动改。
func reportNoviceLevelSkus(ctx context.Context, client *appent.Client, sample int) error {
	skus, err := client.EsportsSku.Query().
		Where(esportssku.RequiredLevelEQ("novice")).
		Order(appent.Asc(esportssku.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	var multiSeat []*appent.EsportsSku
	for _, sku := range skus {
		if sku.RequiredSeats > 1 {
			multiSeat = append(multiSeat, sku)
		}
	}
	fmt.Println("【第二类】SKU required_level 仍为 novice（只报告，不自动改）")
	fmt.Printf("  required_level=novice 的 SKU：%d 条（其中多人 SKU %d 条）\n",
		len(skus), len(multiSeat))
	fmt.Println("  跳过原因：真实等级门槛无法从历史数据推断，自动改写会改变派单资格，须人工逐个确认")
	for i, sku := range multiSeat {
		if i >= sample {
			fmt.Printf("    … 其余 %d 条不再列出\n", len(multiSeat)-sample)
			break
		}
		fmt.Printf("    - id=%d code=%q title=%q required_seats=%d（多人 SKU，重点核对等级门槛）\n",
			sku.ID, sku.Code, sku.Title, sku.RequiredSeats)
	}
	return nil
}

// reportPriorityGameMismatch 处理第三类：优先名单 game 与商品分类 game 不一致。
// 两侧都可能写错（名单分区手填、分类 game 后补），无法安全推断正确值，只出报告。
func reportPriorityGameMismatch(ctx context.Context, client *appent.Client, sample int) error {
	categories, err := client.EsportsProductCategory.Query().
		Order(appent.Asc(esportsproductcategory.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(categories))
	for _, category := range categories {
		if game := strings.TrimSpace(category.Game); game != "" {
			known[game] = true
		}
	}
	games := make([]string, 0, len(known))
	for game := range known {
		games = append(games, game)
	}
	sort.Strings(games)

	grants, err := client.EsportsDispatchPriorityGrant.Query().
		Order(appent.Asc(esportsdispatchprioritygrant.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	var emptyGame, mismatched []*appent.EsportsDispatchPriorityGrant
	for _, grant := range grants {
		game := strings.TrimSpace(grant.Game)
		if game == "" {
			emptyGame = append(emptyGame, grant)
			continue
		}
		if !known[game] {
			mismatched = append(mismatched, grant)
		}
	}
	fmt.Println("【第三类】优先名单 game 与 SKU 分类 game 不一致（只报告，不自动改）")
	fmt.Printf("  商品分类中已有 game 取值：%d 个 %v\n", len(games), games)
	fmt.Printf("  优先名单总数：%d；game 与分类不一致：%d 条；跳过原因统计：game 为空 %d 条\n",
		len(grants), len(mismatched), len(emptyGame))
	fmt.Println("  跳过原因：无法判定是名单分区写错还是分类 game 漏补，自动改写会改变派单范围，须人工确认")
	for i, grant := range mismatched {
		if i >= sample {
			fmt.Printf("    … 其余 %d 条不再列出\n", len(mismatched)-sample)
			break
		}
		fmt.Printf("    - id=%d player_id=%d game=%q period=%s status=%s\n",
			grant.ID, grant.PlayerID, grant.Game, grant.Period, grant.Status)
	}
	return nil
}

// exitMessage 打印中文提示并退出（参数类错误）。
func exitMessage(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(2)
}

// exitError 打印中文错误并退出。
func exitError(message string, err error) {
	fmt.Fprintln(os.Stderr, "[失败]", message, "::", err)
	os.Exit(1)
}

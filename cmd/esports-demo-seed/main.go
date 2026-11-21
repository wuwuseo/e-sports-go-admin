// esports-demo-seed 演示数据播种工具（幂等）
// 用法: esports-demo-seed
//
// 铺一套联调/演示用数据：三角洲行动分区下的 1 个测试商品、1 个测试客户（含身份绑定与
// 客服归属）、5 个陪玩（含可登录账号与钱包）。全部按唯一键判存，重复执行不会产生重复
// 数据，也不会覆盖已存在记录上的运营改动。
//
// 与随服务启动的 ESPORTS_DEMO_SEED 开关共用同一份实现（esports.SeedDemoData），
// 本命令用于「不想重启服务、只想补一次数据」的场景。
package main

import (
	"context"
	"fmt"
	"os"

	"go-admin/internal"
	"go-admin/internal/apps/esports"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	app, err := internal.InitializeApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, "初始化失败:", err)
		os.Exit(1)
	}
	defer app.EntClient.Close()
	defer app.DB.Close()

	res, err := esports.SeedDemoData(context.Background(), app.EntClient)
	if err != nil {
		fmt.Fprintln(os.Stderr, "播种失败:", err)
		os.Exit(1)
	}

	if res.Empty() {
		fmt.Println("演示数据已齐备，本次未新建任何记录（幂等跳过）")
	} else {
		fmt.Printf("播种完成：商品新建=%v 客户新建=%v 陪玩新建=%d 账号新建=%d 钱包新建=%d 身份绑定新建=%v 客户归属补齐=%v\n",
			res.ProductCreated, res.CustomerCreated, res.PlayersCreated, res.AccountsCreated,
			res.WalletsCreated, res.BindingCreated, res.OwnerAssigned)
	}

	fmt.Printf("\n测试商品编码: %s（分区 %s）\n", esports.DemoSeedSkuCode, esports.DemoSeedGameZoneCode)
	// 口令在首次调用时确定（环境变量优先，否则随机生成），必须在播种后打印，
	// 因为播种前该值尚未生成。
	fmt.Printf("统一初始密码: %s\n", esports.DemoSeedPassword())
	fmt.Println("客户账号: demo_customer01 / 13800000001")
	fmt.Println("陪玩账号: demo_player01 ~ demo_player05 / 13800000011 ~ 13800000015")
	fmt.Printf("客户归属指定: 设置 %s=<管理员用户名> 可覆盖默认（默认取 cs_admin 角色管理员）\n", esports.DemoSeedOwnerAdminEnv)
}
